// Configuration: the YAML file, its defaults and validation, and the host,
// alias and target rules the meta file shares.
package app

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"regexp"
	"slices"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
)

// Config is the whole configuration file. config.example.yaml documents
// every key.
type Config struct {
	Listen         string      `yaml:"listen"`
	DataDir        string      `yaml:"data_dir"`
	SyncEvery      Duration    `yaml:"sync_every"`
	MinLength      int         `yaml:"min_length"`
	DefaultTTL     Duration    `yaml:"default_ttl"`
	ShortURLFormat string      `yaml:"short_url_format"`
	FallbackURL    string      `yaml:"fallback_url"`
	Cache          CacheConfig `yaml:"cache"`
	NotFoundLimit  LimitConfig `yaml:"not_found_limit"`
	Log            LogConfig   `yaml:"log"`
	Blacklist      []string    `yaml:"blacklist"`
}

// CacheConfig sizes the in-memory link cache.
type CacheConfig struct {
	TTL        Duration `yaml:"ttl"`
	MaxEntries int      `yaml:"max_entries"`
}

// LimitConfig tunes the per-IP limit on requests for codes that do not exist.
type LimitConfig struct {
	PerMinute      int      `yaml:"per_minute"`
	BlockFor       Duration `yaml:"block_for"`
	ClientIPHeader string   `yaml:"client_ip_header"`
}

// LogConfig controls the optional logs: access, error and admin.
type LogConfig struct {
	Enabled     bool   `yaml:"enabled"`
	Dir         string `yaml:"dir"`
	Redirects   bool   `yaml:"redirects"`
	PlainDays   int    `yaml:"plain_days"`   // days a file stays as text
	ArchiveDays int    `yaml:"archive_days"` // days it is then kept compressed
}

// shortURLFormats are the accepted values of short_url_format and of the
// API's "format" field.
var shortURLFormats = []string{"https", "https_www", "bare", "www"}

// maxMinLength is the largest min_length: codes are at most 10 characters
// long, and they grow past min_length only when a length fills up.
const maxMinLength = maxCodeLength

// reservedAliases can never be a code or a static alias: they are routes.
var reservedAliases = []string{"api", "healthz"}

var (
	hostPattern   = regexp.MustCompile(`^[a-z0-9]([a-z0-9.-]*[a-z0-9])?$`)
	targetPattern = regexp.MustCompile(`^(\*\.)?[a-z0-9]([a-z0-9.-]*[a-z0-9])?$`)
	aliasPattern  = regexp.MustCompile(`^[A-Za-z0-9_-]{3,64}$`)
	wordPattern   = regexp.MustCompile(`^[a-z0-9]{3,}$`)
)

// LoadConfig reads, completes and validates the config file at path.
// Unknown keys are errors, so a typo never silently disables a setting, and
// a key that is no longer read is named rather than ignored.
func LoadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	// Defaults of keys whose 0 means something are set before the file is
	// read: archive_days 0 is no archive, and plain_days 0 is refused.
	c := Config{Log: LogConfig{PlainDays: 7, ArchiveDays: 30}}
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&c); err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("config %s: %w", path, err)
	}
	if err := c.finish(); err != nil {
		return nil, fmt.Errorf("config %s: %w", path, err)
	}
	return &c, nil
}

// finish applies defaults, normalizes values and validates the config.
func (c *Config) finish() error {
	if c.Listen == "" {
		c.Listen = ":8080"
	}
	if c.DataDir == "" {
		c.DataDir = "./data"
	}

	// The shortest code is 3 characters, whatever the file says.
	switch {
	case c.MinLength == 0:
		c.MinLength = 5
	case c.MinLength < 3:
		c.MinLength = 3
	case c.MinLength > maxMinLength:
		return fmt.Errorf("min_length: %d must be at most %d", c.MinLength, maxMinLength)
	}

	if err := c.finishDurations(); err != nil {
		return err
	}
	if d := c.SyncEvery.D(); d != 0 && (d < minSyncEvery || d > maxSyncEvery) {
		return fmt.Errorf("sync_every: %s must be 0, or from 100ms to 1m", c.SyncEvery.text)
	}

	if c.ShortURLFormat == "" {
		c.ShortURLFormat = "https"
	}
	if !slices.Contains(shortURLFormats, c.ShortURLFormat) {
		return fmt.Errorf("short_url_format: %q must be one of %s",
			c.ShortURLFormat, strings.Join(shortURLFormats, ", "))
	}
	if err := checkOptionalURL("fallback_url", c.FallbackURL); err != nil {
		return err
	}

	if c.Cache.MaxEntries == 0 {
		c.Cache.MaxEntries = 10000
	}
	if c.Cache.MaxEntries < 1 {
		return fmt.Errorf("cache.max_entries: %d must be at least 1", c.Cache.MaxEntries)
	}
	if c.NotFoundLimit.PerMinute == 0 {
		c.NotFoundLimit.PerMinute = 30
	}
	if c.NotFoundLimit.PerMinute < 1 {
		return fmt.Errorf("not_found_limit.per_minute: %d must be at least 1", c.NotFoundLimit.PerMinute)
	}
	if c.Log.Dir == "" {
		c.Log.Dir = "./logs"
	}
	if c.Log.PlainDays < 1 {
		return fmt.Errorf("log.plain_days: %d must be at least 1", c.Log.PlainDays)
	}
	if c.Log.ArchiveDays < 0 {
		return fmt.Errorf("log.archive_days: %d must be at least 0", c.Log.ArchiveDays)
	}

	for i, word := range c.Blacklist {
		word = strings.ToLower(strings.TrimSpace(word))
		if !wordPattern.MatchString(word) {
			return fmt.Errorf("blacklist: %q must be at least 3 letters or digits", word)
		}
		c.Blacklist[i] = word
	}
	return nil
}

// finishDurations parses every duration key and fills in the defaults.
func (c *Config) finishDurations() error {
	return errors.Join(
		c.DefaultTTL.resolve("default_ttl", 10*day, false),
		c.Cache.TTL.resolve("cache.ttl", 6*time.Hour, false),
		c.NotFoundLimit.BlockFor.resolve("not_found_limit.block_for", 5*time.Minute, false),
		c.SyncEvery.resolve("sync_every", 0, true),
	)
}

// checkOptionalURL accepts an empty value or an absolute http(s) URL.
func checkOptionalURL(key, value string) error {
	if value == "" || isHTTPURL(value) {
		return nil
	}
	return fmt.Errorf("%s: %q must start with http:// or https://", key, value)
}

// isHTTPURL reports whether s is an absolute http or https URL with a host.
func isHTTPURL(s string) bool {
	u, err := url.Parse(s)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != ""
}

// NormalizeHost turns a Host header into the form domains are kept in:
// lower case, without port, trailing dot or leading "www.".
func NormalizeHost(hostport string) string {
	host := strings.ToLower(strings.TrimSpace(hostport))
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	} else {
		host = strings.Trim(host, "[]")
	}
	host = strings.TrimSuffix(host, ".")
	return strings.TrimPrefix(host, "www.")
}

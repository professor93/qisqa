// Tests for loading and validating the config file.
package app

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// minimalYAML is the smallest valid config: every key has a default. Tests
// append to it.
const minimalYAML = ""

// loadYAML writes yaml to a temporary file and loads it as a config. The
// file's folder is not named after the test: an error names the file, and
// it must not pass a check by naming the test.
func loadYAML(t *testing.T, yaml string) (*Config, error) {
	t.Helper()
	dir, err := os.MkdirTemp("", "qisqa-config-")
	must(t, err)
	t.Cleanup(func() { os.RemoveAll(dir) })
	return LoadConfig(writeFile(t, dir, "config.yaml", yaml))
}

func TestLoadExampleConfig(t *testing.T) {
	cfg, err := LoadConfig(filepath.Join(repoRoot, "config.example.yaml"))
	must(t, err)
	eq(t, cfg.MinLength, 5)
	eq(t, cfg.DefaultTTL.D(), 240*time.Hour)
	eq(t, cfg.DataDir, "./data")
}

func TestConfigDefaults(t *testing.T) {
	cfg, err := loadYAML(t, minimalYAML)
	must(t, err)
	eq(t, cfg.Listen, ":8080")
	eq(t, cfg.DataDir, "./data")
	eq(t, cfg.MinLength, 5)
	eq(t, cfg.DefaultTTL.D(), 240*time.Hour)
	eq(t, cfg.ShortURLFormat, "https")
	eq(t, cfg.FallbackURL, "")
	eq(t, cfg.Cache.TTL.D(), 6*time.Hour)
	eq(t, cfg.Cache.MaxEntries, 10000)
	eq(t, cfg.NotFoundLimit.PerMinute, 30)
	eq(t, cfg.NotFoundLimit.BlockFor.D(), 5*time.Minute)
	eq(t, cfg.Log.Enabled, false)
	eq(t, cfg.Log.Dir, "./logs")
	eq(t, cfg.Log.PlainDays, 7)
	eq(t, cfg.Log.ArchiveDays, 30)
}

// TestLogDays: an absent key takes its default, also in a log section
// that has other keys, and archive_days 0 means no archive.
func TestLogDays(t *testing.T) {
	cases := map[string][2]int{
		"":                          {7, 30},
		"log:\n":                    {7, 30},
		"log:\n  enabled: true\n":   {7, 30},
		"log:\n  archive_days: 0\n": {7, 0},
		"log:\n  plain_days: 1\n  archive_days: 365\n": {1, 365},
	}
	for yaml, want := range cases {
		t.Run(yaml, func(t *testing.T) {
			cfg, err := loadYAML(t, minimalYAML+yaml)
			must(t, err)
			eq(t, [2]int{cfg.Log.PlainDays, cfg.Log.ArchiveDays}, want)
		})
	}
}

func TestMinLengthClamp(t *testing.T) {
	cases := map[string]int{
		"":                 5, // absent
		"min_length: 0\n":  5,
		"min_length: 1\n":  3,
		"min_length: 2\n":  3,
		"min_length: 3\n":  3,
		"min_length: 7\n":  7,
		"min_length: 10\n": 10,
	}
	for extra, want := range cases {
		t.Run(extra, func(t *testing.T) {
			cfg, err := loadYAML(t, minimalYAML+extra)
			must(t, err)
			eq(t, cfg.MinLength, want)
		})
	}
}

func TestConfigRejects(t *testing.T) {
	cases := []struct {
		name, yaml, want string
	}{
		// Accounts and domains are not config keys: the data folder holds them.
		{"tokens", minimalYAML + "tokens: [secret]\n", "tokens"},
		{"domains", minimalYAML + "domains:\n  - host: short.example\n", "domains"},
		{"delete_expired_after", minimalYAML + "delete_expired_after: 30d\n", "delete_expired_after"},
		{"bad fallback_url", minimalYAML + "fallback_url: not-a-url\n", "fallback_url"},
		{"bad short_url_format", minimalYAML + "short_url_format: http\n", "short_url_format"},
		{"bad bare_url_zones", minimalYAML + "bare_url_zones: top50\n", "bare_url_zones"},
		{"zero default_ttl", minimalYAML + "default_ttl: \"0\"\n", "default_ttl"},
		{"invalid default_ttl", minimalYAML + "default_ttl: 10x\n", "default_ttl"},
		{"blacklist word too short", minimalYAML + "blacklist: [ab]\n", "blacklist"},
		{"unknown key", minimalYAML + "listn: x\n", "listn"},
		{"min_length too long", minimalYAML + "min_length: 11\n", "min_length"},
		{"default_ttl too long", minimalYAML + "default_ttl: 40000d\n", "default_ttl"},
		{"zero log.plain_days", minimalYAML + "log:\n  plain_days: 0\n", "log.plain_days"},
		{"negative log.archive_days", minimalYAML + "log:\n  archive_days: -1\n", "log.archive_days"},
		{"log.keep_days", minimalYAML + "log:\n  keep_days: 14\n", "keep_days"}, // replaced by plain_days and archive_days
		{"pages.files names no page", minimalYAML + "pages:\n  files:\n    403: ./x.html\n", "pages.files"},
		{"pages.files without a path", minimalYAML + "pages:\n  files:\n    404: \"\"\n", "pages.files.404"},
		{"pages with an unknown key", minimalYAML + "pages:\n  folder: ./pages\n", "folder"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := loadYAML(t, c.yaml)
			wantErr(t, err, c.want)
		})
	}
}

// TestBareURLZonesConfig: bare_url_zones is top100 unless set, and may be
// top500 or all.
func TestBareURLZonesConfig(t *testing.T) {
	for yaml, want := range map[string]string{
		"":                         "top100",
		"bare_url_zones: top100\n": "top100",
		"bare_url_zones: top500\n": "top500",
		"bare_url_zones: all\n":    "all",
	} {
		t.Run(yaml, func(t *testing.T) {
			cfg, err := loadYAML(t, minimalYAML+yaml)
			must(t, err)
			eq(t, cfg.BareURLZones, want)
		})
	}
}

// TestTrimCodesConfig: trim_codes is on unless the file turns it off.
func TestTrimCodesConfig(t *testing.T) {
	for yaml, want := range map[string]bool{"": true, "trim_codes: true\n": true, "trim_codes: false\n": false} {
		cfg, err := loadYAML(t, minimalYAML+yaml)
		must(t, err)
		eq(t, cfg.TrimCodes, want)
	}
}

// TestPagesConfig: pages names a folder and a path per page; a page's name
// may be written as a number or as a string.
func TestPagesConfig(t *testing.T) {
	cfg, err := loadYAML(t, minimalYAML)
	must(t, err)
	eq(t, cfg.Pages.Dir, "")
	eq(t, len(cfg.Pages.Files), 0)

	cfg, err = loadYAML(t, minimalYAML+"pages:\n  dir: ./pages\n  files:\n    404: ./a.html\n    \"500\": ./b.html\n    index: ./c.html\n")
	must(t, err)
	eq(t, cfg.Pages.Dir, "./pages")
	eq(t, len(cfg.Pages.Files), 3)
	eq(t, cfg.Pages.Files["404"], "./a.html")
	eq(t, cfg.Pages.Files["500"], "./b.html")
	eq(t, cfg.Pages.Files["index"], "./c.html")
}

// TestSyncEveryConfig: sync_every is 0 unless set, and otherwise an
// interval from 100ms to 1m.
func TestSyncEveryConfig(t *testing.T) {
	for yaml, want := range map[string]time.Duration{
		"":                    0,
		"sync_every: 0\n":     0,
		"sync_every: 1s\n":    time.Second,
		"sync_every: 100ms\n": 100 * time.Millisecond,
		"sync_every: 1m\n":    time.Minute,
	} {
		t.Run(yaml, func(t *testing.T) {
			cfg, err := loadYAML(t, minimalYAML+yaml)
			must(t, err)
			eq(t, cfg.SyncEvery.D(), want)
		})
	}
	for _, value := range []string{"50ms", "2m", "-1s", "1x"} {
		t.Run(value, func(t *testing.T) {
			_, err := loadYAML(t, minimalYAML+"sync_every: "+value+"\n")
			wantErr(t, err, "sync_every")
		})
	}
}

func TestNormalizeHost(t *testing.T) {
	cases := map[string]string{
		"short.example":          "short.example",
		"SHORT.EXAMPLE:8080":     "short.example",
		"www.short.example":      "short.example",
		"short.example.":         "short.example",
		"WWW.Short.Example.:443": "short.example",
		"[::1]:8080":             "::1",
		"":                       "",
	}
	for in, want := range cases {
		t.Run(in, func(t *testing.T) {
			eq(t, NormalizeHost(in), want)
		})
	}
}

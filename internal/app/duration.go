// Durations for config and API values: "30s", "15m", "12h", "10d".
package app

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

const day = 24 * time.Hour

// MaxDuration is the longest duration accepted: a hundred years.
const MaxDuration = 36500 * day

// ParseDuration parses a duration such as "30s", "15m", "12h" or "10d".
// Days are whole numbers; everything else follows time.ParseDuration.
// Negative durations and durations above MaxDuration are rejected.
func ParseDuration(s string) (time.Duration, error) {
	var d time.Duration
	if days, ok := strings.CutSuffix(s, "d"); ok {
		n, err := strconv.Atoi(days)
		if err != nil {
			return 0, durationError(s)
		}
		// Compared as days: a huge number would overflow as a duration.
		d = time.Duration(min(n, 36501)) * day
	} else {
		var err error
		if d, err = time.ParseDuration(s); err != nil {
			return 0, durationError(s)
		}
	}
	switch {
	case d < 0:
		return 0, durationError(s)
	case d > MaxDuration:
		return 0, fmt.Errorf("duration %q is too long (at most 36500d)", s)
	}
	return d, nil
}

// durationError is the single message shown for any unparsable duration.
func durationError(s string) error {
	return fmt.Errorf("invalid duration %q (examples: 30m, 12h, 10d)", s)
}

// Duration is a duration read from the config file. The raw text is kept
// until validation, so an error can name the key it belongs to and a missing
// value can be told apart from an explicit "0".
type Duration struct {
	text string
	d    time.Duration
}

// UnmarshalText stores the raw config text; config validation parses it.
func (d *Duration) UnmarshalText(b []byte) error {
	d.text = string(b)
	return nil
}

// D returns the parsed value.
func (d Duration) D() time.Duration { return d.d }

// Dur wraps a known duration, for code that builds a Config directly.
func Dur(d time.Duration) Duration { return Duration{d: d} }

// resolve parses the stored text into d. A missing value becomes def.
// Zero is rejected unless allowZero is set. key names the config key in errors.
func (d *Duration) resolve(key string, def time.Duration, allowZero bool) error {
	if d.text == "" {
		d.d = def
		return nil
	}
	v, err := ParseDuration(d.text)
	if err != nil {
		return fmt.Errorf("%s: %w", key, err)
	}
	if v == 0 && !allowZero {
		return fmt.Errorf("%s: must be greater than zero", key)
	}
	d.d = v
	return nil
}

// Tests for parsing durations.
package main

import (
	"testing"
	"time"
)

func TestParseDuration(t *testing.T) {
	valid := map[string]time.Duration{
		"30s": 30 * time.Second,
		"15m": 15 * time.Minute,
		"12h": 12 * time.Hour,
		"10d": 240 * time.Hour,
		"1d":  24 * time.Hour,
		"0":   0,

		"36500d":  MaxDuration,
		"876000h": MaxDuration,
	}
	for in, want := range valid {
		t.Run(in, func(t *testing.T) {
			got, err := ParseDuration(in)
			must(t, err)
			eq(t, got, want)
		})
	}

	for _, in := range []string{"", "10x", "-5m", "-1d", "d", "1.5d", "abc", "36501d", "876001h", "106752d", "99999999999999999999d"} {
		t.Run("invalid "+in, func(t *testing.T) {
			if _, err := ParseDuration(in); err == nil {
				t.Fatalf("ParseDuration(%q) succeeded, want an error", in)
			}
		})
	}
}

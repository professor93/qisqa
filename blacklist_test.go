// Tests for the word blacklist that generated codes must pass.
package main

import (
	"regexp"
	"slices"
	"testing"
)

func TestNormalizeCode(t *testing.T) {
	cases := map[string]string{
		"S3X":   "sex",
		"5h1T":  "shit",
		"aB3xK": "abexk",
		"70":    "to",
		"Q9q8Z": "q9q8z",
	}
	for in, want := range cases {
		t.Run(in, func(t *testing.T) {
			eq(t, normalizeCode(in), want)
		})
	}
}

func TestBuiltinBlacklistShape(t *testing.T) {
	letters := regexp.MustCompile(`^[a-z]{3,}$`)
	for _, word := range builtinBlacklist {
		if !letters.MatchString(word) {
			t.Errorf("%q must be at least 3 lower-case letters", word)
		}
	}
	if !slices.IsSorted(builtinBlacklist) {
		t.Error("the list is not sorted")
	}
	if len(slices.Compact(slices.Clone(builtinBlacklist))) != len(builtinBlacklist) {
		t.Error("the list has duplicates")
	}
	for name, words := range map[string][]string{"uzbek": uzbekWords, "russian": russianWords, "english": englishWords} {
		if len(words) < 40 {
			t.Errorf("%s list has %d words, want at least 40", name, len(words))
		}
	}
	if len(builtinBlacklist) < 120 {
		t.Errorf("the list has %d words, want at least 120", len(builtinBlacklist))
	}
	for _, word := range []string{"fuck", "shit", "suka", "jalab"} {
		if !slices.Contains(builtinBlacklist, word) {
			t.Errorf("%q is missing", word)
		}
	}
}

func TestBlocked(t *testing.T) {
	eq(t, Blocked("xFuCkx", nil), true)
	eq(t, Blocked("5h1Tz", nil), true)
	eq(t, Blocked("aSUKAb", nil), true)
	eq(t, Blocked("Q9q8Z", nil), false)

	eq(t, Blocked("xYzZyx", nil), false)
	eq(t, Blocked("xYzZyx", []string{"yzzy"}), true)
}

// Tests for finding and replacing URLs in text.
package app

import (
	"slices"
	"testing"
)

func TestTrimURLTail(t *testing.T) {
	cases := map[string]string{
		"https://example.com/a.":      "https://example.com/a",
		"https://example.com/a),":     "https://example.com/a",
		"https://example.com/a?!":     "https://example.com/a",
		"https://example.com/a;:":     "https://example.com/a",
		"https://example.com/a]}":     "https://example.com/a",
		"https://example.com/a.b?c=d": "https://example.com/a.b?c=d",
		"https://example.com/a":       "https://example.com/a",

		// A closing bracket that matches one opened inside the URL is part
		// of the URL; a surplus one is not.
		"https://example.com/wiki/Go_(til)":   "https://example.com/wiki/Go_(til)",
		"https://example.com/wiki/Go_(til),":  "https://example.com/wiki/Go_(til)",
		"https://example.com/wiki/Go_(til)).": "https://example.com/wiki/Go_(til)",
		"https://example.com/list[1]":         "https://example.com/list[1]",
		"https://example.com/(a":              "https://example.com/(a",

		// An apostrophe ends a URL only when nothing follows it.
		"https://example.com/wiki/O'zbekiston": "https://example.com/wiki/O'zbekiston",
		"https://example.com/a'":               "https://example.com/a",

		// Punctuation outside ASCII never ends a URL; letters may.
		"https://example.com/a».":  "https://example.com/a",
		"https://example.com/a”":   "https://example.com/a",
		"https://example.com/a…":   "https://example.com/a",
		"https://example.com/a—":   "https://example.com/a",
		"https://example.com/вики": "https://example.com/вики",
	}
	for in, want := range cases {
		t.Run(in, func(t *testing.T) {
			eq(t, trimURLTail(in), want)
		})
	}
}

func TestFindURLs(t *testing.T) {
	const a = "https://example.com/a"
	cases := []struct {
		name, text string
		want       []string
	}{
		{"sentence end", "See: https://example.com/a?x=1. Thanks!", []string{"https://example.com/a?x=1"}},
		{"in brackets", "(see https://example.com/a)", []string{a}},
		{"cyrillic text", "Смотрите: https://example.com/a, спасибо", []string{a}},
		{"apostrophe inside", "see https://example.com/wiki/O'zbekiston_tarixi now", []string{"https://example.com/wiki/O'zbekiston_tarixi"}},
		{"single quoted", "see 'https://example.com/a' now", []string{a}},
		{"guillemets", "«https://example.com/a».", []string{a}},
		{"curly quotes", "“https://example.com/a”", []string{a}},
		{"ellipsis", "https://example.com/a… and more", []string{a}},
		{"same url twice", "https://example.com/a and https://example.com/a", []string{a}},
		{"prefix of another", "https://example.com/ab then https://example.com/a", []string{"https://example.com/ab", a}},
		{"no scheme", "no scheme: example.com/page", nil},
		{"quoted", `quoted "https://example.com/a" here`, []string{a}},
		{"http", "old http://example.com/a link", []string{"http://example.com/a"}},
		{"brackets inside and around", "(see https://example.com/wiki/Go_(til))", []string{"https://example.com/wiki/Go_(til)"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := FindURLs(c.text)
			if !slices.Equal(got, c.want) {
				t.Fatalf("got %q, want %q", got, c.want)
			}
		})
	}
}

func TestReplaceURLs(t *testing.T) {
	repl := map[string]string{"https://example.com/a": "short.example/Xy12z"}

	eq(t, ReplaceURLs("x https://example.com/a. y https://example.com/ab z", repl),
		"x short.example/Xy12z. y https://example.com/ab z")
	eq(t, ReplaceURLs("(https://example.com/a)", repl), "(short.example/Xy12z)")
	eq(t, ReplaceURLs("twice https://example.com/a, https://example.com/a!", repl),
		"twice short.example/Xy12z, short.example/Xy12z!")

	wiki := map[string]string{"https://example.com/wiki/Go_(til)": "short.example/Xy12z"}
	eq(t, ReplaceURLs("(see https://example.com/wiki/Go_(til)).", wiki), "(see short.example/Xy12z).")

	eq(t, ReplaceURLs("«https://example.com/a».", repl), "«short.example/Xy12z».")

	apostrophe := map[string]string{"https://example.com/wiki/O'zbekiston": "short.example/Xy12z"}
	eq(t, ReplaceURLs("see https://example.com/wiki/O'zbekiston.", apostrophe), "see short.example/Xy12z.")

	const untouched = "x https://example.com/a y"
	eq(t, ReplaceURLs(untouched, nil), untouched)
}

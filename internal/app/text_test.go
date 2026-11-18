// Tests for finding and replacing URLs in text.
package app

import (
	"slices"
	"strings"
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
			got := FindURLs(c.text, nil)
			if !slices.Equal(got, c.want) {
				t.Fatalf("got %q, want %q", got, c.want)
			}
			// With URLs without a scheme looked for as well, the URLs with
			// one are found as before, with the same bounds.
			var withScheme []string
			for _, u := range FindURLs(c.text, testZones) {
				if hasScheme(u) {
					withScheme = append(withScheme, u)
				}
			}
			if !slices.Equal(withScheme, c.want) {
				t.Fatalf("with zones: got %q, want %q", withScheme, c.want)
			}
		})
	}
}

func TestReplaceURLs(t *testing.T) {
	repl := map[string]string{"https://example.com/a": "short.example/Xy12z"}

	eq(t, ReplaceURLs("x https://example.com/a. y https://example.com/ab z", repl, nil),
		"x short.example/Xy12z. y https://example.com/ab z")
	eq(t, ReplaceURLs("(https://example.com/a)", repl, nil), "(short.example/Xy12z)")
	eq(t, ReplaceURLs("twice https://example.com/a, https://example.com/a!", repl, nil),
		"twice short.example/Xy12z, short.example/Xy12z!")

	wiki := map[string]string{"https://example.com/wiki/Go_(til)": "short.example/Xy12z"}
	eq(t, ReplaceURLs("(see https://example.com/wiki/Go_(til)).", wiki, nil), "(see short.example/Xy12z).")

	eq(t, ReplaceURLs("«https://example.com/a».", repl, nil), "«short.example/Xy12z».")

	apostrophe := map[string]string{"https://example.com/wiki/O'zbekiston": "short.example/Xy12z"}
	eq(t, ReplaceURLs("see https://example.com/wiki/O'zbekiston.", apostrophe, nil), "see short.example/Xy12z.")

	const untouched = "x https://example.com/a y"
	eq(t, ReplaceURLs(untouched, nil, nil), untouched)
}

// testZones stands for the zones of the config in the tests of URLs without
// a scheme: ".example" is no real zone, and the tests name example hosts.
var testZones = map[string]bool{"com": true, "org": true, "example": true, "biz": true, "xn--p1ai": true}

func TestFindBareURLs(t *testing.T) {
	cases := []struct {
		text string
		want []string
	}{
		{"see example.com/page now", []string{"example.com/page"}},
		{"www.example.com/a?b=1#c.", []string{"www.example.com/a?b=1#c"}},
		{"(example.com/a)", []string{"example.com/a"}},
		{"«example.com/a»", []string{"example.com/a"}},
		{"'example.org/a'", []string{"example.org/a"}},
		{"(see example.com/wiki/Go_(til)).", []string{"example.com/wiki/Go_(til)"}},
		{"Example.COM/Page", []string{"Example.COM/Page"}},
		{"example.com:8080/x", []string{"example.com:8080/x"}},
		{"example.com/@name", []string{"example.com/@name"}},
		{"the end: example.com.", []string{"example.com"}},
		{"пример.example/страница", []string{"пример.example/страница"}},
		{"сайт.рф/x и САЙТ.РФ/y", []string{"сайт.рф/x", "САЙТ.РФ/y"}},
		{"he came.Biz is a zone", []string{"came.Biz"}},
		{"example.com/a example.com/a", []string{"example.com/a"}},
		{"example.com/a,https://example.org/b", []string{"example.com/a", "https://example.org/b"}},
		{"https://example.com/a and example.org/b", []string{"https://example.com/a", "example.org/b"}},

		{"write to ali@example.com", nil},
		{"user@example.com/x", nil},
		{"on 12.05.2026 at 10.30", nil},
		{"version v1.2", nil},
		{"the file.txt", nil},
		{"it is done.Next one", nil},
		{"ftp://example.com/x", nil},
		{"mailto:ali@example.com", nil},
		{"HTTP://EXAMPLE.COM/x", nil},
		{"example..com/x", nil},
		{".com and com", nil},
		{"example.com:x/y", nil},
		{"example.com:/x", nil},
		{"example.com:123456/x", nil},
		{"Site:example.com/x", nil},
		{"a_b.com/x", nil},
		// What is inside a URL with a scheme, or runs into one, is no URL
		// of its own.
		{"https://example.com/r?to=example.org/x", []string{"https://example.com/r?to=example.org/x"}},
		{"example.com/r?to=https://example.org/x", []string{"https://example.org/x"}},
	}
	for _, c := range cases {
		t.Run(c.text, func(t *testing.T) {
			if got := FindURLs(c.text, testZones); !slices.Equal(got, c.want) {
				t.Fatalf("got %q, want %q", got, c.want)
			}
		})
	}
}

func TestReplaceBareURLs(t *testing.T) {
	repl := map[string]string{"example.com/page": "short.example/Xy12z", "example.org/b": "short.example/Ab34c"}
	eq(t, ReplaceURLs("Read example.com/page. And (example.org/b)!", repl, testZones),
		"Read short.example/Xy12z. And (short.example/Ab34c)!")
	// Only a whole URL is replaced, and only one that is a key.
	eq(t, ReplaceURLs("example.com/pages and example.org/a", repl, testZones), "example.com/pages and example.org/a")
	// Without zones a URL without a scheme is no URL, key or not.
	eq(t, ReplaceURLs("Read example.com/page.", repl, nil), "Read example.com/page.")
	// URLs with and without a scheme in one text.
	both := map[string]string{"example.com/page": "short.example/Xy12z", "https://example.com/a": "short.example/Ab34c"}
	eq(t, ReplaceURLs("example.com/page, https://example.com/a.", both, testZones), "short.example/Xy12z, short.example/Ab34c.")
}

// TestBareURLsInALongTextOfDottedWords: the largest text the API takes,
// made of words with dots that are no URLs, yields nothing.
func TestBareURLsInALongTextOfDottedWords(t *testing.T) {
	text := strings.Repeat("a.b v1.2 file.txt done.Next ", 10<<10/28)
	if got := FindURLs(text, testZones); got != nil {
		t.Fatalf("found %d URLs, the first %q", len(got), got[0])
	}
	eq(t, ReplaceURLs(text, map[string]string{"a.b": "x"}, testZones), text)
}

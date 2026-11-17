// URLs inside free text: finding them and replacing them with short links.
package main

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// urlPattern matches http:// or https:// and everything after it up to the
// next space, angle bracket or double quote. Text without a scheme
// ("example.com/page") is never a match, so ordinary words with dots are
// left alone. What the match took too much of, trimURLTail gives back.
var urlPattern = regexp.MustCompile(`https?://[^\s<>"]+`)

// trailingPunctuation is the ASCII punctuation that may follow a URL in a
// sentence without being part of it.
const trailingPunctuation = `.,;:!?)]}'`

// openers maps each closing bracket to the bracket that opens it.
var openers = map[byte]string{')': "(", ']': "[", '}': "{"}

// trimURLTail removes punctuation that follows a URL in running text but is
// not part of it: "see https://example.com/a." ends with a full stop that
// belongs to the sentence.
//
// A closing bracket is kept when it closes a bracket opened inside the URL,
// as in "https://example.com/wiki/Go_(til)"; a surplus one, as in
// "(see https://example.com/a)", is removed.
//
// Punctuation outside ASCII, such as » ” … —, never ends a URL: written
// text puts it next to a URL, a URL itself does not end with it.
func trimURLTail(u string) string {
	for u != "" {
		last, size := utf8.DecodeLastRuneInString(u)
		if last >= utf8.RuneSelf {
			if !unicode.IsPunct(last) {
				break // a letter, as in "https://example.com/вики"
			}
		} else {
			if !strings.ContainsRune(trailingPunctuation, last) {
				break
			}
			if open, isBracket := openers[byte(last)]; isBracket &&
				strings.Count(u, open) >= strings.Count(u, string(last)) {
				break // balanced: the bracket is part of the URL
			}
		}
		u = u[:len(u)-size]
	}
	return u
}

// FindURLs returns the distinct URLs in text, in order of first appearance.
func FindURLs(text string) []string {
	var urls []string
	seen := map[string]bool{}
	for _, match := range urlPattern.FindAllString(text, -1) {
		if u := trimURLTail(match); !seen[u] {
			seen[u] = true
			urls = append(urls, u)
		}
	}
	return urls
}

// ReplaceURLs replaces every URL in text that is a key of repl with its
// value. Only whole URLs are replaced: a URL that merely starts with a key
// is left as it is, and punctuation after a URL is preserved.
func ReplaceURLs(text string, repl map[string]string) string {
	if len(repl) == 0 {
		return text
	}
	return urlPattern.ReplaceAllStringFunc(text, func(match string) string {
		u := trimURLTail(match)
		if short, ok := repl[u]; ok {
			return short + match[len(u):]
		}
		return match
	})
}

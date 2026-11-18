// URLs inside free text: finding them and replacing them with short links.
package app

import (
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/net/idna"
)

// urlPattern matches http:// or https:// and everything after it up to the
// next space, angle bracket or double quote. Text without a scheme
// ("example.com/page") is never a match of it, so ordinary words with dots
// are left alone; such a URL is found only when the caller gives the zones
// to tell it by (see bareURL). What the match took too much of,
// trimURLTail gives back.
var urlPattern = regexp.MustCompile(`https?://[^\s<>"]+`)

// runPattern matches a run of characters that a URL may be made of: what
// stands between spaces, angle brackets and double quotes.
var runPattern = regexp.MustCompile(`[^\s<>"]+`)

// openingPunctuation may stand in front of a URL without being part of it.
const openingPunctuation = "([{'«“‘„"

// maxLabelLength is the most characters a label of a host name can have.
const maxLabelLength = 63

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

// textURL is where one URL stands in a text: text[start:end] is the URL as
// written, without the punctuation that follows it.
type textURL struct{ start, end int }

// hasScheme tells a URL written with http:// or https:// from one written
// without a scheme.
func hasScheme(written string) bool {
	return strings.HasPrefix(written, "http://") || strings.HasPrefix(written, "https://")
}

// scanURLs returns where the URLs of text stand, in order: those with a
// scheme, and, when zones is not nil, also those without one in the text
// between them. The URLs with a scheme are the same with and without zones.
func scanURLs(text string, zones map[string]bool) []textURL {
	var found []textURL
	at := 0
	for _, match := range urlPattern.FindAllStringIndex(text, -1) {
		if zones != nil {
			found = appendBareURLs(found, text, at, match[0], true, zones)
		}
		found = append(found, textURL{match[0], match[0] + len(trimURLTail(text[match[0]:match[1]]))})
		at = match[1]
	}
	if zones != nil {
		found = appendBareURLs(found, text, at, len(text), false, zones)
	}
	return found
}

// appendBareURLs adds the URLs without a scheme of text[from:to]. beforeURL
// says that a URL with a scheme starts at to: a run that reaches it with
// nothing between, as "example.com/r?to=" before "https://...", is the
// front of something else, not a URL of its own.
func appendBareURLs(found []textURL, text string, from, to int, beforeURL bool, zones map[string]bool) []textURL {
	for _, run := range runPattern.FindAllStringIndex(text[from:to], -1) {
		start, end := from+run[0], from+run[1]
		for start < end {
			first, size := utf8.DecodeRuneInString(text[start:end])
			if !strings.ContainsRune(openingPunctuation, first) {
				break
			}
			start += size
		}
		u := trimURLTail(text[start:end])
		if beforeURL && end == to && start+len(u) == end {
			continue
		}
		if bareURL(u, zones) {
			found = append(found, textURL{start, start + len(u)})
		}
	}
	return found
}

// bareURL reports whether u, a run of text without the punctuation around
// it, is a URL written without a scheme: a host name, perhaps a port, and
// perhaps a path, query or fragment.
//
// The host is two or more labels of letters, digits and hyphens, and its
// last label is one of zones, which holds zone names in lower case and in
// ASCII form. That is what tells "example.com/page" from "file.txt",
// "12.05.2026" and a sentence that lacks the space after its full stop. An
// "@" is no character of a label, so an e-mail address and a URL with user
// information are not taken; in the path it is like any other character.
//
// What written text glues to a URL must not become part of its host: a
// label does not start or end with a hyphen ("-example.com" in a list),
// does not mix Latin letters with those of another script ("сайт-example"),
// and a number is the first of three or more labels only in a list
// ("1.example.com"). Each of these leaves the text as it is.
func bareURL(u string, zones map[string]bool) bool {
	host := u
	if end := strings.IndexAny(u, "/?#"); end >= 0 {
		host = u[:end]
	}
	if colon := strings.LastIndexByte(host, ':'); colon >= 0 {
		port := host[colon+1:]
		if len(port) < 1 || len(port) > 5 || strings.Trim(port, "0123456789") != "" {
			return false
		}
		if n, _ := strconv.Atoi(port); n < 1 || n > 65535 {
			return false
		}
		host = host[:colon]
	}
	labels := strings.Split(host, ".")
	if len(labels) < 2 {
		return false
	}
	for _, label := range labels {
		if label == "" || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		length, latin, other := 0, false, false
		for _, r := range label {
			switch {
			case r == '-' || unicode.IsDigit(r):
			case unicode.Is(unicode.Latin, r):
				latin = true
			case unicode.IsLetter(r):
				other = true
			default:
				return false
			}
			// No label of a host name is longer, and the ASCII form of a
			// long one takes time that grows with the square of its length.
			if length++; length > maxLabelLength {
				return false
			}
		}
		if latin && other {
			return false
		}
	}
	if len(labels) > 2 && strings.Trim(labels[0], "0123456789") == "" {
		return false
	}
	zone := labels[len(labels)-1]
	if isASCII(zone) {
		zone = strings.ToLower(zone)
	} else {
		var err error
		if zone, err = idna.Lookup.ToASCII(zone); err != nil {
			return false
		}
	}
	return zones[zone]
}

// FindURLs returns the distinct URLs in text as they are written, in order
// of first appearance. With zones nil only URLs that start with http:// or
// https:// are found; otherwise also those without a scheme whose host ends
// in one of zones.
func FindURLs(text string, zones map[string]bool) []string {
	var urls []string
	seen := map[string]bool{}
	for _, at := range scanURLs(text, zones) {
		if u := text[at.start:at.end]; !seen[u] {
			seen[u] = true
			urls = append(urls, u)
		}
	}
	return urls
}

// ReplaceURLs replaces every URL in text that is a key of repl with its
// value. The URLs are those that FindURLs finds with the same zones. Only
// whole URLs are replaced: a URL that merely starts with a key is left as
// it is, and punctuation around a URL is preserved.
func ReplaceURLs(text string, repl map[string]string, zones map[string]bool) string {
	if len(repl) == 0 {
		return text
	}
	var b strings.Builder
	at := 0
	for _, u := range scanURLs(text, zones) {
		if short, ok := repl[text[u.start:u.end]]; ok {
			b.WriteString(text[at:u.start])
			b.WriteString(short)
			at = u.end
		}
	}
	if at == 0 {
		return text
	}
	b.WriteString(text[at:])
	return b.String()
}

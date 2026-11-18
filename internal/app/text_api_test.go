// Tests for text mode: shortening the URLs inside a text.
package app

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

const (
	urlA = "https://example.com/articles/2026/11/a-long-title?ref=mail"
	urlB = "https://example.com/downloads/2026/11/report-456789"
)

// textRequest builds the JSON body of a text-mode request.
func textRequest(t *testing.T, fields map[string]any) string {
	t.Helper()
	body, err := json.Marshal(fields)
	must(t, err)
	return string(body)
}

// shortened sends a text-mode request, asserts 200 and returns the result.
func shortened(t *testing.T, s *Server, fields map[string]any) textResponse {
	t.Helper()
	w := api(s, "POST", "/api/links", textRequest(t, fields))
	status(t, w, http.StatusOK)
	if strings.Contains(w.Body.String(), `"links":null`) {
		t.Fatalf(`"links" is null, want an array: %s`, w.Body.String())
	}
	var result textResponse
	must(t, json.Unmarshal(w.Body.Bytes(), &result))
	return result
}

// manyURLs returns text holding n distinct URLs, each long enough to be
// worth shortening.
func manyURLs(n int) string {
	var text strings.Builder
	for i := range n {
		fmt.Fprintf(&text, "https://example.com/long/path/to/item/%02d ", i)
	}
	return text.String()
}

func TestTextReplacesAll(t *testing.T) {
	s, _ := newTestServer(t)
	got := shortened(t, s, map[string]any{"text": "See: " + urlA + ". Ref " + urlB, "format": "bare"})

	eq(t, len(got.Links), 2)
	first, second := got.Links[0], got.Links[1]
	eq(t, first.URL, urlA)
	eq(t, second.URL, urlB)
	eq(t, first.ShortURL, "short.example/"+first.Code)
	eq(t, first.Created, true)
	eq(t, got.Text, "See: short.example/"+first.Code+". Ref short.example/"+second.Code)

	redirectsTo(t, do(s, "GET", "short.example", "/"+first.Code), urlA)
	redirectsTo(t, do(s, "GET", "short.example", "/"+second.Code), urlB)
}

func TestTextSameURLTwice(t *testing.T) {
	s, _ := newTestServer(t)
	got := shortened(t, s, map[string]any{"text": urlA + " and again " + urlA})

	eq(t, len(got.Links), 1)
	short := "https://short.example/" + got.Links[0].Code
	eq(t, got.Text, short+" and again "+short)
}

func TestTextCreatesWithOneSync(t *testing.T) {
	s, st := newTestServer(t)
	before := st.syncs()
	got := shortened(t, s, map[string]any{"text": manyURLs(5)})
	eq(t, len(got.Links), 5)
	eq(t, st.syncs(), before+2) // the new chunk file's head where it was created, then the five links
}

func TestTextReusesExistingLink(t *testing.T) {
	s, _ := newTestServer(t)
	existing := created(t, s, `{"url": "`+urlA+`"}`)

	got := shortened(t, s, map[string]any{"text": "See: " + urlA})
	eq(t, len(got.Links), 1)
	eq(t, got.Links[0].Code, existing.Code)
	eq(t, got.Links[0].Created, false)
}

func TestTextSkipsWhenNotShorter(t *testing.T) {
	s, _ := newTestServer(t)
	// "https://short.example/" plus a 5-character code is 27 bytes.
	for _, text := range []string{
		"see https://example.com/a",
		"see https://example.com/1234567", // 27 bytes: the same length, so no gain
	} {
		got := shortened(t, s, map[string]any{"text": text})
		eq(t, got.Text, text)
		eq(t, len(got.Links), 0)
	}

	got := shortened(t, s, map[string]any{"text": "see https://example.com/12345678"}) // 28 bytes
	eq(t, len(got.Links), 1)
	eq(t, got.Text, "see https://short.example/"+got.Links[0].Code)
}

func TestTextSkipsInvalidTargets(t *testing.T) {
	s, _ := newTestServer(t)
	text := "Not allowed: https://example.org/a/very/long/path/that/is/not/allowed" +
		" and ours: https://short.example/a/very/long/path/on/our/own/domain"
	got := shortened(t, s, map[string]any{"text": text, "domain": "go.example"})
	eq(t, got.Text, text)
	eq(t, len(got.Links), 0)
}

// TestTextKeepsOwnDomainURLs: a URL that points at one of the service's own
// short domains, however it is spelled, is never replaced. A short link is
// not shortened again, while the rest of the text is shortened as usual.
func TestTextKeepsOwnDomainURLs(t *testing.T) {
	s, _ := newTestServer(t)
	own := strings.Join([]string{
		"https://short.example/aB3xK",
		"http://www.short.example/a/long/path/that/would/otherwise/be/shortened",
		"https://GO.example:443/android?from=a-long-query-that-would-be-shortened",
		"https://go.example./a/long/path/that/would/otherwise/be/shortened",
	}, " ")
	got := shortened(t, s, map[string]any{"text": own + " " + urlA})

	eq(t, len(got.Links), 1)
	eq(t, got.Links[0].URL, urlA)
	eq(t, got.Text, own+" https://short.example/"+got.Links[0].Code)
}

func TestTextWithoutURLs(t *testing.T) {
	s, _ := newTestServer(t)
	got := shortened(t, s, map[string]any{"text": "hello, example.com/page is not a link"})
	eq(t, got.Text, "hello, example.com/page is not a link")
	eq(t, len(got.Links), 0)
}

func TestTextWithURLs(t *testing.T) {
	s, _ := newTestServer(t)
	got := shortened(t, s, map[string]any{"text": urlA + " " + urlB, "urls": []string{urlA}})

	eq(t, len(got.Links), 1)
	eq(t, got.Links[0].URL, urlA)
	eq(t, got.Text, "https://short.example/"+got.Links[0].Code+" "+urlB)
}

func TestTextURLsNotInText(t *testing.T) {
	s, _ := newTestServer(t)
	got := shortened(t, s, map[string]any{"text": "hello", "urls": []string{urlA, urlA}})

	eq(t, got.Text, "hello")
	eq(t, len(got.Links), 1) // the repeated entry is one link
	eq(t, got.Links[0].URL, urlA)
}

func TestTextEmptyURLsShortensNothing(t *testing.T) {
	s, _ := newTestServer(t)
	got := shortened(t, s, map[string]any{"text": "See: " + urlA, "urls": []string{}})
	eq(t, got.Text, "See: "+urlA)
	eq(t, len(got.Links), 0)
}

func TestTextURLsInvalid(t *testing.T) {
	s, _ := newTestServer(t)
	w := api(s, "POST", "/api/links", textRequest(t, map[string]any{
		"text": "hello", "urls": []string{urlA, "ftp://example.com/x"},
	}))
	badRequest(t, w, "urls", "ftp://example.com/x", "http")
}

func TestTextURLsAlwaysReplaced(t *testing.T) {
	s, _ := newTestServer(t)
	got := shortened(t, s, map[string]any{
		"text": "see https://example.com/a", "urls": []string{"https://example.com/a"},
	})
	eq(t, len(got.Links), 1)
	eq(t, got.Text, "see https://short.example/"+got.Links[0].Code) // longer than the original
}

func TestTextLimits(t *testing.T) {
	s, _ := newTestServer(t)

	tooLong := textRequest(t, map[string]any{"text": strings.Repeat("a", 10*1024+1)})
	badRequest(t, api(s, "POST", "/api/links", tooLong), "text", "10 KB")
	shortened(t, s, map[string]any{"text": strings.Repeat("a", 10*1024)})

	tooMany := textRequest(t, map[string]any{"text": manyURLs(21)})
	badRequest(t, api(s, "POST", "/api/links", tooMany), "20")
	eq(t, len(shortened(t, s, map[string]any{"text": manyURLs(20)}).Links), 20)
}

func TestTextTooManyCreatesNothing(t *testing.T) {
	s, _ := newTestServer(t)
	status(t, api(s, "POST", "/api/links", textRequest(t, map[string]any{"text": manyURLs(21)})), http.StatusBadRequest)

	// Had the first twenty been stored, this would report an existing link.
	first := strings.Fields(manyURLs(1))[0]
	eq(t, created(t, s, `{"url": "`+first+`"}`).Created, true)
}

func TestTextSecured(t *testing.T) {
	s, _ := newTestServer(t)
	got := shortened(t, s, map[string]any{"text": urlA + " " + urlB, "expires_after_open": "0", "expires_in": "2d"})

	eq(t, len(got.Links), 2)
	for _, link := range got.Links {
		eq(t, link.Secured, true)
		near(t, link.ExpiresAt, in(48*time.Hour))
	}
	opensTo(t, do(s, "POST", "short.example", "/"+got.Links[0].Code), urlA)
	gone(t, do(s, "POST", "short.example", "/"+got.Links[0].Code))
}

func TestTextEventsLogged(t *testing.T) {
	s, _ := newTestServer(t)
	dir := logTo(t, s)

	got := shortened(t, s, map[string]any{"text": urlA + " " + urlB})

	lines := loggedLines(t, dir, "access")
	eq(t, len(lines), 2)
	for i, line := range lines {
		eq(t, line, "create\tshort.example\t"+got.Links[i].Code+"\t203.0.113.7\t1")
	}
}

// bareA is urlA's kind of URL written without a scheme, long enough to be
// worth shortening.
const bareA = "example.com/a/long/page/here"

// TestTextLeavesBareURLsUnlessAsked: a URL without a scheme is replaced only
// when the request sets bare_urls.
func TestTextLeavesBareURLsUnlessAsked(t *testing.T) {
	s, _ := newTestServer(t)
	text := "Read " + bareA + "."
	for _, fields := range []map[string]any{
		{"text": text},
		{"text": text, "bare_urls": false},
	} {
		got := shortened(t, s, fields)
		eq(t, got.Text, text)
		eq(t, len(got.Links), 0)
	}
}

// TestTextBareURLs: with bare_urls a URL without a scheme becomes a link to
// https:// and the URL as written, and what stands around it stays.
func TestTextBareURLs(t *testing.T) {
	s, _ := newTestServer(t)
	got := shortened(t, s, map[string]any{"text": "Read (" + bareA + "). Or www.example.org/another/long/page!", "bare_urls": true})
	eq(t, len(got.Links), 2)
	eq(t, got.Links[0].URL, "https://"+bareA)
	eq(t, got.Links[1].URL, "https://www.example.org/another/long/page")
	eq(t, got.Text, "Read ("+got.Links[0].ShortURL+"). Or "+got.Links[1].ShortURL+"!")

	w := do(s, "GET", "short.example", "/"+got.Links[0].Code)
	status(t, w, http.StatusFound)
	eq(t, w.Header().Get("Location"), "https://"+bareA)
}

// TestTextBareAndFullSpellingMakeOneLink: the same target written with and
// without its scheme is one link, put in both places.
func TestTextBareAndFullSpellingMakeOneLink(t *testing.T) {
	s, _ := newTestServer(t)
	got := shortened(t, s, map[string]any{"text": bareA + " and https://" + bareA + " and " + bareA, "bare_urls": true})
	eq(t, len(got.Links), 1)
	short := got.Links[0].ShortURL
	eq(t, got.Text, short+" and "+short+" and "+short)
}

// TestTextBareURLNotWorthShortening: the length that counts is that of the
// URL as written, without the scheme that the target gets.
func TestTextBareURLNotWorthShortening(t *testing.T) {
	s, _ := newTestServer(t)
	const same = "example.com/123456789012345" // 27 bytes, as long as the short link
	got := shortened(t, s, map[string]any{"text": "see " + same, "bare_urls": true})
	eq(t, got.Text, "see "+same)
	eq(t, len(got.Links), 0)

	got = shortened(t, s, map[string]any{"text": "see " + same + "6", "bare_urls": true})
	eq(t, len(got.Links), 1)
}

// TestTextBareURLsKeepOwnDomains: the service's own short links, as the
// bare and www formats write them, are never shortened again.
func TestTextBareURLsKeepOwnDomains(t *testing.T) {
	s, _ := newTestServer(t)
	_, err := s.meta.AddDomain("links.example.com", "", nil, false)
	must(t, err)
	own := "links.example.com/aB3xK www.links.example.com/a/long/path/that/would/otherwise/be/shortened"
	got := shortened(t, s, map[string]any{"text": own + " " + bareA, "bare_urls": true})
	eq(t, len(got.Links), 1)
	eq(t, got.Text, own+" "+got.Links[0].ShortURL)
}

// TestTextBareURLsSkipWhatMayNotBeShortened: as with a scheme, a URL the
// domain does not allow is left in the text without an error.
func TestTextBareURLsSkipWhatMayNotBeShortened(t *testing.T) {
	s, _ := newTestServer(t)
	text := "Not allowed: example.org/a/very/long/path/that/is/not/allowed"
	got := shortened(t, s, map[string]any{"text": text, "domain": "go.example", "bare_urls": true})
	eq(t, got.Text, text)
	eq(t, len(got.Links), 0)
}

// TestTextURLsListIgnoresBareURLs: with urls the list names what is
// shortened, and bare_urls changes nothing.
func TestTextURLsListIgnoresBareURLs(t *testing.T) {
	s, _ := newTestServer(t)
	got := shortened(t, s, map[string]any{"text": bareA + " " + urlA, "urls": []string{urlA}, "bare_urls": true})
	eq(t, len(got.Links), 1)
	eq(t, got.Text, bareA+" "+got.Links[0].ShortURL)

	body := textRequest(t, map[string]any{"text": bareA, "urls": []string{bareA}, "bare_urls": true})
	badRequest(t, api(s, "POST", "/api/links", body), "urls", bareA)
}

// TestBareURLsNeedsText: bare_urls belongs to text mode.
func TestBareURLsNeedsText(t *testing.T) {
	s, _ := newTestServer(t)
	badRequest(t, api(s, "POST", "/api/links", `{"url": "`+urlA+`", "bare_urls": true}`),
		`"bare_urls" is only valid together with "text"`)
	created(t, s, `{"url": "`+urlA+`", "bare_urls": false}`)
}

// TestBareURLZonesChoosesTheZones: a zone outside the first hundred makes a
// URL only when bare_url_zones takes more of them.
func TestBareURLZonesChoosesTheZones(t *testing.T) {
	var zones []string
	for line := range strings.SplitSeq(zonesFile, "\n") {
		if line != "" && !strings.HasPrefix(line, "#") {
			zones = append(zones, line)
		}
	}
	texts := map[string]string{
		"top100": "see example." + zones[99] + "/a/long/page/that/is/worth/a/short/link now",
		"top500": "see example." + zones[100] + "/a/long/page/that/is/worth/a/short/link now",
		"all":    "see example." + zones[500] + "/a/long/page/that/is/worth/a/short/link now",
	}
	for i, tier := range bareURLZoneTiers {
		s, _ := newTestServer(t, func(c *Config) { c.BareURLZones = tier })
		for j, other := range bareURLZoneTiers {
			got := shortened(t, s, map[string]any{"text": texts[other], "bare_urls": true})
			if want := j <= i; (len(got.Links) == 1) != want {
				t.Errorf("bare_url_zones %s, a zone of %s: %d links, want replaced = %v", tier, other, len(got.Links), want)
			}
		}
	}
}

// TestTextBareURLsCountToTheLimit: URLs without a scheme count to the 20
// links of a request like the others.
func TestTextBareURLsCountToTheLimit(t *testing.T) {
	s, _ := newTestServer(t)
	many := func(n int) string {
		var text strings.Builder
		for i := range n {
			fmt.Fprintf(&text, "example.com/long/path/to/item/%02d ", i)
		}
		return text.String()
	}
	tooMany := textRequest(t, map[string]any{"text": many(21), "bare_urls": true})
	badRequest(t, api(s, "POST", "/api/links", tooMany), "20")
	eq(t, len(shortened(t, s, map[string]any{"text": many(20), "bare_urls": true}).Links), 20)
}

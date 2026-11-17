// Tests for text mode: shortening the URLs inside a text.
package main

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

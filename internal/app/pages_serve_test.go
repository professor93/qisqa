// Tests of serving the operator's own pages in place of the built-in index
// page and of the answers 404, 429 and 500.
package app

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// serverWithPages returns a test server whose pages are the given files of
// a folder, loaded as the program loads them at start.
func serverWithPages(t *testing.T, files map[string]string, changes ...func(*Config)) (*Server, *Store) {
	t.Helper()
	dir := pagesFolder(t, files)
	s, st := newTestServer(t, append(changes, func(c *Config) { c.Pages = PagesConfig{Dir: dir} })...)
	must(t, s.cfg.LoadPages())
	return s, st
}

// showsPage asserts an HTML answer with that status whose body is the page
// and then the signature comment, sent as the built-in pages are.
func showsPage(t *testing.T, w *httptest.ResponseRecorder, want int, page string) {
	t.Helper()
	status(t, w, want)
	eq(t, w.Header().Get("Content-Type"), "text/html; charset=utf-8")
	eq(t, w.Header().Get("Cache-Control"), "no-store")
	eq(t, w.Header().Get("X-Robots-Tag"), "noindex, nofollow")
	eq(t, w.Body.String(), page+signatureComment)
}

// saysJSON asserts a JSON error answer with that status.
func saysJSON(t *testing.T, w *httptest.ResponseRecorder, want int, body string) {
	t.Helper()
	status(t, w, want)
	eq(t, w.Header().Get("Content-Type"), "application/json")
	eq(t, strings.TrimSpace(w.Body.String()), body)
}

// builtIn returns a built-in page.
func builtIn(t *testing.T, name string) string {
	t.Helper()
	data, err := webFS.ReadFile("web/" + name)
	must(t, err)
	return string(data)
}

// block makes the test client ask for unknown codes until it is refused.
func block(t *testing.T, s *Server) {
	t.Helper()
	for i := range 20 {
		if do(s, "GET", "short.example", fmt.Sprintf("/gone%d", i)).Code == http.StatusTooManyRequests {
			return
		}
	}
	t.Fatal("twenty unknown codes did not get the client blocked")
}

const (
	ownIndex    = "<!doctype html>\n<p>our home</p>\n"
	ownNotFound = "<!doctype html>\n<p>nothing here</p>"
	ownBlocked  = "<!doctype html>\n<p>slow down</p>\n"
	ownFailed   = "<!doctype html>\n<p>our fault</p>\n"
)

// TestOwnIndexAndNotFoundPages: the operator's pages are shown where the
// built-in ones were; the pages of secured and password links stay.
func TestOwnIndexAndNotFoundPages(t *testing.T) {
	s, _ := serverWithPages(t, map[string]string{"index.html": ownIndex, "404.html": ownNotFound})

	showsPage(t, do(s, "GET", "short.example", "/"), http.StatusOK, ownIndex)
	showsPage(t, do(s, "GET", "short.example", "/none1"), http.StatusNotFound, ownNotFound)
	showsPage(t, do(s, "GET", "short.example", "/a/b"), http.StatusNotFound, ownNotFound)
	showsPage(t, do(s, "GET", "short.example", "/not!a!code"), http.StatusNotFound, ownNotFound)
	showsPage(t, do(s, "GET", "other.example", "/"), http.StatusNotFound, ownNotFound)

	open, _ := secured(t, s, time.Minute)
	showsPage(t, do(s, "GET", "short.example", "/"+open), http.StatusOK, builtIn(t, "open.html"))
	locked, _ := passwordLink(t, s, nil)
	showsPage(t, do(s, "GET", "short.example", "/"+locked), http.StatusOK, builtIn(t, "password.html"))

	// A link that works is a redirect, as ever.
	code, target := live(t, s)
	redirectsTo(t, do(s, "GET", "short.example", "/"+code), target)
}

// TestOwnPagesLeaveTheFallbackAlone: where a fallback URL is set, it is the
// answer, not a page.
func TestOwnPagesLeaveTheFallbackAlone(t *testing.T) {
	s, _ := serverWithPages(t, map[string]string{"index.html": ownIndex, "404.html": ownNotFound},
		func(c *Config) { c.FallbackURL = "https://example.org/elsewhere" })

	redirectsTo(t, do(s, "GET", "go.example", "/"), "https://example.org/home")
	redirectsTo(t, do(s, "GET", "go.example", "/none1"), "https://example.org/home")
	redirectsTo(t, do(s, "GET", "short.example", "/none2"), "https://example.org/elsewhere")
	redirectsTo(t, do(s, "GET", "other.example", "/"), "https://example.org/elsewhere")
	// The index page of a domain without a fallback of its own is a page.
	showsPage(t, do(s, "GET", "short.example", "/"), http.StatusOK, ownIndex)
}

// TestOwnPageForABlockedClient: a browser gets the operator's page with the
// status 429; what a program asks for is answered in JSON as before.
func TestOwnPageForABlockedClient(t *testing.T) {
	s, _ := serverWithPages(t, map[string]string{"429.html": ownBlocked})
	code, _ := live(t, s)
	open, _ := secured(t, s, time.Minute)
	block(t, s)

	const refusal = `{"error":"too many requests"}`
	showsPage(t, do(s, "GET", "short.example", "/"+code), http.StatusTooManyRequests, ownBlocked)
	showsPage(t, do(s, "GET", "short.example", "/"), http.StatusTooManyRequests, ownBlocked)
	showsPage(t, do(s, "GET", "short.example", "/robots.txt"), http.StatusTooManyRequests, ownBlocked)

	saysJSON(t, api(s, "POST", "/api/links", longURLBody), http.StatusTooManyRequests, refusal)
	saysJSON(t, do(s, "GET", "short.example", "/api/none"), http.StatusTooManyRequests, refusal)
	saysJSON(t, do(s, "GET", "short.example", "/api/openapi.yaml"), http.StatusTooManyRequests, refusal)
	saysJSON(t, do(s, "POST", "short.example", "/"+open), http.StatusTooManyRequests, refusal)
	saysJSON(t, do(s, "DELETE", "short.example", "/"+code), http.StatusTooManyRequests, refusal)

	status(t, do(s, "GET", "short.example", "/healthz"), http.StatusOK)
}

// TestOwnPageForABlockedClientOverHTTP: over a real connection a HEAD gets
// the status and the length of the operator's page and no body.
func TestOwnPageForABlockedClientOverHTTP(t *testing.T) {
	s, _ := serverWithPages(t, map[string]string{"429.html": ownBlocked})
	server := httptest.NewServer(s.Handler())
	defer server.Close()
	ask := func(method, path string) (*http.Response, string) {
		t.Helper()
		r, err := http.NewRequest(method, server.URL+path, nil)
		must(t, err)
		r.Host = "short.example"
		response, err := server.Client().Do(r)
		must(t, err)
		defer response.Body.Close()
		body, err := io.ReadAll(response.Body)
		must(t, err)
		return response, string(body)
	}
	for i := 0; ; i++ {
		if response, _ := ask("GET", fmt.Sprintf("/gone%d", i)); response.StatusCode == http.StatusTooManyRequests {
			break
		}
		if i == 20 {
			t.Fatal("twenty unknown codes did not get the client blocked")
		}
	}

	response, body := ask("GET", "/")
	eq(t, response.StatusCode, http.StatusTooManyRequests)
	eq(t, body, ownBlocked+signatureComment)

	response, body = ask("HEAD", "/")
	eq(t, response.StatusCode, http.StatusTooManyRequests)
	eq(t, response.Header.Get("Content-Type"), "text/html; charset=utf-8")
	eq(t, response.ContentLength, int64(len(ownBlocked)+len(signatureComment)))
	eq(t, body, "")
}

// TestOwnPageForAFailedLookup: when the store fails, a browser gets the
// operator's page with the status 500.
func TestOwnPageForAFailedLookup(t *testing.T) {
	captureLog(t)
	s, st := serverWithPages(t, map[string]string{"500.html": ownFailed})
	code, _ := live(t, s)
	must(t, st.Close())
	showsPage(t, do(s, "GET", "short.example", "/"+code), http.StatusInternalServerError, ownFailed)
}

// TestWithoutOwnPagesNothingChanges: a folder that holds none of the pages
// leaves every answer as it is without one.
func TestWithoutOwnPagesNothingChanges(t *testing.T) {
	captureLog(t)
	s, st := serverWithPages(t, map[string]string{"other.html": "<p>not a page</p>", "403.html": "<p>no</p>"})
	code, _ := live(t, s)

	showsPage(t, do(s, "GET", "short.example", "/"), http.StatusOK, builtIn(t, "index.html"))
	showsPage(t, do(s, "GET", "short.example", "/none1"), http.StatusNotFound, builtIn(t, "404.html"))
	status(t, do(s, "GET", "short.example", "/other.html"), http.StatusNotFound)

	block(t, s)
	saysJSON(t, do(s, "GET", "short.example", "/"+code), http.StatusTooManyRequests, `{"error":"too many requests"}`)

	s, st = serverWithPages(t, nil)
	code, _ = live(t, s)
	must(t, st.Close())
	w := do(s, "GET", "short.example", "/"+code)
	status(t, w, http.StatusInternalServerError)
	eq(t, w.Header().Get("Content-Type"), "text/plain; charset=utf-8")
	eq(t, w.Body.String(), "Internal Server Error\n")
}

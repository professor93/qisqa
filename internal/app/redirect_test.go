// Tests for the public pages: redirects, the root page, the health check.
package app

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// storeLink stores l on short.example for the test account, as a create
// request would, and returns its code.
func storeLink(t *testing.T, s *Server, l NewLink) string {
	t.Helper()
	created, err := s.create(testAccount, s.meta.Domain("short.example"), []NewLink{l})
	must(t, err)
	return created[0].Code
}

// liveLinks numbers the targets of live, so that every link leads elsewhere.
var liveLinks atomic.Int64

// live creates a regular link on short.example that expires in an hour and
// returns its code and target URL.
func live(t *testing.T, s *Server) (code, target string) {
	t.Helper()
	target = fmt.Sprintf("https://example.com/live/%d", liveLinks.Add(1))
	return storeLink(t, s, NewLink{URL: target, ExpiresAt: s.now().Add(time.Hour)}), target
}

func TestRedirectLiveLink(t *testing.T) {
	s, _ := newTestServer(t)
	code, target := live(t, s)

	w := do(s, "GET", "short.example", "/"+code)
	redirectsTo(t, w, target)
	eq(t, w.Header().Get("Cache-Control"), "no-store")
	eq(t, w.Header().Get("X-Robots-Tag"), "noindex, nofollow")
}

func TestRedirectHostVariants(t *testing.T) {
	s, _ := newTestServer(t)
	code, target := live(t, s)
	for _, host := range []string{"SHORT.EXAMPLE:8080", "www.short.example", "short.example."} {
		t.Run(host, func(t *testing.T) {
			redirectsTo(t, do(s, "GET", host, "/"+code), target)
		})
	}
}

func TestRedirectUnknownHost(t *testing.T) {
	s, _ := newTestServer(t)
	code, _ := live(t, s)
	w := do(s, "GET", "other.example", "/"+code)
	status(t, w, http.StatusNotFound)
	if !strings.Contains(w.Body.String(), "Link not found") {
		t.Fatalf("body is not the 404 page: %.200s", w.Body.String())
	}

	s, _ = newTestServer(t, func(c *Config) { c.FallbackURL = "https://example.org/" })
	redirectsTo(t, do(s, "GET", "other.example", "/"+code), "https://example.org/")
}

func TestRedirectIsPerDomain(t *testing.T) {
	s, _ := newTestServer(t)
	code, _ := live(t, s) // exists only on short.example
	redirectsTo(t, do(s, "GET", "go.example", "/"+code), "https://example.org/home")
}

func TestRedirectStaticAlias(t *testing.T) {
	s, _ := newTestServer(t)
	redirectsTo(t, do(s, "GET", "go.example", "/Android"), "https://example.com/android")
}

func TestRedirectExpiredIsNotAStrike(t *testing.T) {
	s, _ := newTestServer(t)
	old := storeLink(t, s, NewLink{URL: testURL, ExpiresAt: s.now().Add(-time.Hour)})
	code, target := live(t, s)

	for range 10 {
		status(t, do(s, "GET", "short.example", "/"+old), http.StatusNotFound)
	}
	redirectsTo(t, do(s, "GET", "short.example", "/"+code), target)
}

func TestRedirectMissingStrikes(t *testing.T) {
	s, _ := newTestServer(t)
	code, target := live(t, s)

	for i := range 4 {
		status(t, do(s, "GET", "short.example", fmt.Sprintf("/none%d", i)), http.StatusNotFound)
	}
	w := do(s, "GET", "short.example", "/"+code)
	status(t, w, http.StatusTooManyRequests)
	eq(t, strings.TrimSpace(w.Body.String()), `{"error":"too many requests"}`)

	// Another client is not affected.
	redirectsTo(t, do(s, "GET", "short.example", "/"+code, withRemote("203.0.113.8:999")), target)
}

// TestUnknownCodeStrikes: a code that was never handed out is a guess, and
// it is answered without reading a file.
func TestUnknownCodeStrikes(t *testing.T) {
	captureLog(t)
	s, st := newTestServer(t)
	code, _ := live(t, s)

	// From here on every chunk file fails to open: the store lets go of
	// the files it holds, and a folder takes the place of the one there is.
	st.abandon()
	path := firstChunk(st.dir)
	must(t, os.Remove(path))
	must(t, os.Mkdir(path, 0o700))
	status(t, do(s, "GET", "short.example", "/"+code), http.StatusInternalServerError)

	for i := range 4 {
		status(t, do(s, "GET", "short.example", fmt.Sprintf("/none%d", i)), http.StatusNotFound)
	}
	status(t, do(s, "GET", "short.example", "/"+code), http.StatusTooManyRequests)
}

// TestRedirectOnceTheStoreIsClosed: a lookup the closed store refuses is a
// failure of the store, answered as one, never as a link that is not there.
func TestRedirectOnceTheStoreIsClosed(t *testing.T) {
	captureLog(t)
	s, st := newTestServer(t)
	code, _ := live(t, s) // stored, not looked up: the cache does not hold it
	must(t, st.Close())
	status(t, do(s, "GET", "short.example", "/"+code), http.StatusInternalServerError)
}

func TestRedirectInvalidPathIsNotAStrike(t *testing.T) {
	s, _ := newTestServer(t)
	code, target := live(t, s)

	for range 10 {
		for _, path := range []string{"/favicon.ico", "/a", "/a/b"} {
			status(t, do(s, "GET", "short.example", path), http.StatusNotFound)
		}
	}
	redirectsTo(t, do(s, "GET", "short.example", "/"+code), target)
}

func TestRedirectServedFromCache(t *testing.T) {
	s, st := newTestServer(t)
	code, target := live(t, s)

	redirectsTo(t, do(s, "GET", "short.example", "/"+code), target)
	must(t, st.Delete(1, code, testAccount)) // behind the server's back
	redirectsTo(t, do(s, "GET", "short.example", "/"+code), target)
}

func TestRedirectCacheEndsWithLink(t *testing.T) {
	s, _ := newTestServer(t)
	code := storeLink(t, s, NewLink{URL: testURL, ExpiresAt: s.now().Add(10 * time.Minute)})

	redirectsTo(t, do(s, "GET", "short.example", "/"+code), testURL)
	advance(s, 11*time.Minute)
	status(t, do(s, "GET", "short.example", "/"+code), http.StatusNotFound)
}

// TestRemovedClosedFileStopsItsCachedLinks: a chunk file that the limit on
// open files has closed, removed, is noticed by the check, which empties
// the link cache: its cached link stops at once. So it is when a lookup of
// another of its links found the file missing first, and also when the
// file was put back after that lookup.
func TestRemovedClosedFileStopsItsCachedLinks(t *testing.T) {
	for _, order := range []string{"check first", "lookup first", "lookup first, then put back"} {
		t.Run(order, func(t *testing.T) {
			captureLog(t)
			defer func(n int) { maxOpenChunks = n }(maxOpenChunks)
			maxOpenChunks = 1
			s, st := newTestServer(t)
			code, target := live(t, s)
			other, _ := live(t, s)
			redirectsTo(t, do(s, "GET", "short.example", "/"+code), target) // now cached
			_, err := s.create(testAccount, s.meta.Domain("go.example"), []NewLink{{URL: "https://example.com/go", ExpiresAt: s.now().Add(time.Hour)}})
			must(t, err) // closes the file of short.example
			path := firstChunk(st.dir)
			must(t, os.Rename(path, path+".away"))
			if order != "check first" {
				status(t, do(s, "GET", "short.example", "/"+other), http.StatusNotFound)
			}
			if order == "lookup first, then put back" {
				must(t, os.Rename(path+".away", path))
			}
			redirectsTo(t, do(s, "GET", "short.example", "/"+code), target) // from the cache

			s.ForgetRemovedFiles()
			status(t, do(s, "GET", "short.example", "/"+code), http.StatusNotFound)
			status(t, do(s, "GET", "short.example", "/"+other), http.StatusNotFound)
		})
	}
}

func TestLinksOfDisabledAccount(t *testing.T) {
	s, _ := newTestServer(t)
	code, target := live(t, s)
	securedCode, securedTarget := secured(t, s, 0)
	redirectsTo(t, do(s, "GET", "short.example", "/"+code), target) // now cached

	must(t, s.meta.SetDisabled(testAccount, true))
	for range 10 { // more than the not-found limit: none is a strike
		status(t, do(s, "GET", "short.example", "/"+code), http.StatusNotFound)
	}
	status(t, do(s, "GET", "short.example", "/"+securedCode), http.StatusNotFound)
	gone(t, do(s, "POST", "short.example", "/"+securedCode))

	must(t, s.meta.SetDisabled(testAccount, false))
	redirectsTo(t, do(s, "GET", "short.example", "/"+code), target)
	opensTo(t, do(s, "POST", "short.example", "/"+securedCode), securedTarget) // not used up meanwhile
}

func TestLinksOfDeletedAccount(t *testing.T) {
	s, _ := newTestServer(t)
	code, target := live(t, s)
	redirectsTo(t, do(s, "GET", "short.example", "/"+code), target) // now cached

	must(t, s.meta.DeleteAccount(testAccount))
	for range 10 { // more than the not-found limit: none is a strike
		status(t, do(s, "GET", "short.example", "/"+code), http.StatusNotFound)
	}
}

func TestBlockedHostStopsExistingLinks(t *testing.T) {
	s, _ := newTestServer(t)
	code, target := live(t, s)
	// User info, a port and capitals must not hide the host.
	tricky := storeLink(t, s, NewLink{URL: "https://user@EXAMPLE.com:8443/x", ExpiresAt: s.now().Add(time.Hour)})
	securedCode, _ := secured(t, s, 0)
	redirectsTo(t, do(s, "GET", "short.example", "/"+code), target) // now cached

	must(t, s.meta.Block("example.com"))
	for range 10 { // more than the not-found limit: none is a strike
		status(t, do(s, "GET", "short.example", "/"+code), http.StatusNotFound)
	}
	status(t, do(s, "GET", "short.example", "/"+tricky), http.StatusNotFound)
	gone(t, do(s, "POST", "short.example", "/"+securedCode))

	must(t, s.meta.Unblock("example.com"))
	redirectsTo(t, do(s, "GET", "short.example", "/"+code), target)
	redirectsTo(t, do(s, "GET", "short.example", "/"+tricky), "https://user@EXAMPLE.com:8443/x")
}

func TestLinksOfRemovedDomainStayDead(t *testing.T) {
	s, _ := newTestServer(t)
	code, target := live(t, s)
	redirectsTo(t, do(s, "GET", "short.example", "/"+code), target) // now cached

	must(t, s.meta.RemoveDomain("short.example"))
	status(t, do(s, "GET", "short.example", "/"+code), http.StatusNotFound)
	_, err := s.meta.AddDomain("short.example", "", nil, true)
	must(t, err)
	status(t, do(s, "GET", "short.example", "/"+code), http.StatusNotFound)
}

// TestCacheFillRacingAMetaChange: a link must not stay in the cache when a
// disabled account or a blocked host empties the cache while a request is
// on its way to caching the link.
func TestCacheFillRacingAMetaChange(t *testing.T) {
	changes := map[string]func(*Server) error{
		"disabled account": func(s *Server) error { return s.meta.SetDisabled(testAccount, true) },
		"blocked host":     func(s *Server) error { return s.meta.Block("example.com") },
	}
	// Where the change comes: after the request decided to cache the link,
	// or, for a create, while the link is stored, after the token check.
	beforeCaching := func(s *Server, _ *Store, change func()) {
		s.beforeCaching = func() { s.beforeCaching = nil; change() }
	}
	whileStoring := func(_ *Server, st *Store, change func()) {
		var once sync.Once
		st.onWrite = func(string, int64, []byte) { once.Do(change) }
	}
	redirect := func(t *testing.T, s *Server, prepare func()) string {
		code, target := live(t, s)
		prepare()
		redirectsTo(t, do(s, "GET", "short.example", "/"+code), target) // answered as decided
		return code
	}
	create := func(t *testing.T, s *Server, prepare func()) string {
		prepare()
		return created(t, s, longURLBody).Code
	}
	for _, c := range []struct {
		name   string
		inject func(*Server, *Store, func())
		fill   func(*testing.T, *Server, func()) string
	}{
		{"redirect, before caching", beforeCaching, redirect},
		{"create, before caching", beforeCaching, create},
		{"create, while storing", whileStoring, create},
	} {
		for changeName, change := range changes {
			t.Run(c.name+", "+changeName, func(t *testing.T) {
				s, st := newTestServer(t)
				code := c.fill(t, s, func() {
					c.inject(s, st, func() { must(t, change(s)) })
				})
				status(t, do(s, "GET", "short.example", "/"+code), http.StatusNotFound)
			})
		}
	}
}

// TestCacheFillRacingAnEdit: a link edited or deleted while a redirect is on
// its way to caching it must not stay in the cache as it was.
func TestCacheFillRacingAnEdit(t *testing.T) {
	// stall makes the next redirect run edit after it decided to cache the
	// link and before it does.
	stall := func(s *Server, edit func()) {
		s.beforeCaching = func() { s.beforeCaching = nil; edit() }
	}
	t.Run("delete", func(t *testing.T) {
		s, _ := newTestServer(t)
		code, target := live(t, s)
		stall(s, func() { status(t, api(s, "DELETE", "/api/links/"+code, ""), http.StatusNoContent) })
		redirectsTo(t, do(s, "GET", "short.example", "/"+code), target) // answered as decided
		status(t, do(s, "GET", "short.example", "/"+code), http.StatusNotFound)
	})
	t.Run("new URL", func(t *testing.T) {
		s, _ := newTestServer(t)
		code, target := live(t, s)
		stall(s, func() {
			status(t, api(s, "PATCH", "/api/links/"+code, `{"url": "https://example.com/new"}`), http.StatusOK)
		})
		redirectsTo(t, do(s, "GET", "short.example", "/"+code), target) // answered as decided
		redirectsTo(t, do(s, "GET", "short.example", "/"+code), "https://example.com/new")
	})
}

func TestRedirectLogged(t *testing.T) {
	s, _ := newTestServer(t, func(c *Config) { c.Log.Redirects = true })
	dir := logTo(t, s)
	code, _ := live(t, s)

	do(s, "GET", "short.example", "/"+code)

	lines := loggedLines(t, dir, "access")
	eq(t, len(lines), 1)
	eq(t, lines[0], "redirect\tshort.example\t"+code+"\t203.0.113.7\t") // no account
}

func TestRedirectNotLoggedByDefault(t *testing.T) {
	s, _ := newTestServer(t)
	dir := logTo(t, s)
	code, target := live(t, s)

	redirectsTo(t, do(s, "GET", "short.example", "/"+code), target)
	entries, err := os.ReadDir(dir)
	must(t, err)
	eq(t, len(entries), 0) // not even an empty file
}

func TestRoot(t *testing.T) {
	s, _ := newTestServer(t)
	w := do(s, "GET", "short.example", "/")
	status(t, w, http.StatusOK)
	if !strings.Contains(w.Body.String(), "qisqa") {
		t.Fatalf("body is not the index page: %.200s", w.Body.String())
	}
	redirectsTo(t, do(s, "GET", "go.example", "/"), "https://example.org/home")
}

func TestRobots(t *testing.T) {
	s, _ := newTestServer(t)
	w := do(s, "GET", "other.example", "/robots.txt")
	status(t, w, http.StatusOK)
	eq(t, w.Body.String(), "User-agent: *\nDisallow: /\n")
}

func TestClientIP(t *testing.T) {
	request := func(headers map[string]string) *http.Request {
		r := httptest.NewRequest("GET", "/aaaaa", nil)
		r.RemoteAddr = testRemote
		for key, value := range headers {
			r.Header.Set(key, value)
		}
		return r
	}
	with := func(header string) *Server {
		s, _ := newTestServer(t, func(c *Config) { c.NotFoundLimit.ClientIPHeader = header })
		return s
	}

	// No header configured: a client cannot choose its own address.
	eq(t, with("").clientIP(request(map[string]string{"X-Real-IP": "198.51.100.9"})), "203.0.113.7")

	s := with("X-Real-IP")
	eq(t, s.clientIP(request(map[string]string{"X-Real-IP": "198.51.100.9"})), "198.51.100.9")
	eq(t, s.clientIP(request(nil)), "203.0.113.7")

	s = with("X-Forwarded-For")
	eq(t, s.clientIP(request(map[string]string{"X-Forwarded-For": "198.51.100.1, 198.51.100.2"})), "198.51.100.2")

	// A proxy may add a header line of its own after the one the client
	// sent. Only the proxy's line can be trusted.
	r := request(nil)
	r.Header.Add("X-Forwarded-For", "198.51.100.66") // written by the client
	r.Header.Add("X-Forwarded-For", "198.51.100.2")  // added by the proxy
	eq(t, s.clientIP(r), "198.51.100.2")

	// An empty header falls back to the connection address.
	eq(t, s.clientIP(request(map[string]string{"X-Forwarded-For": " "})), "203.0.113.7")
}

func TestHealthz(t *testing.T) {
	s, _ := newTestServer(t)

	w := do(s, "GET", "localhost:8080", "/healthz")
	status(t, w, http.StatusOK)
	eq(t, strings.TrimSpace(w.Body.String()), `{"status":"ok"}`)

	// A blocked client can still be health-checked.
	for i := range 4 {
		do(s, "GET", "short.example", fmt.Sprintf("/none%d", i))
	}
	status(t, do(s, "GET", "short.example", "/none9"), http.StatusTooManyRequests)
	w = do(s, "GET", "localhost:8080", "/healthz")
	status(t, w, http.StatusOK)
	eq(t, strings.TrimSpace(w.Body.String()), `{"status":"ok"}`)
}

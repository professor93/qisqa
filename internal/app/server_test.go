// The server every HTTP test runs against, and helpers to talk to it.
package app

import (
	"crypto/sha256"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const (
	testToken   = "test-token-0123456789"
	testAccount = 1 // the account testToken belongs to
	testRemote  = "203.0.113.7:1234"
)

// testConfig is the config every HTTP test starts from: a low not-found
// limit so blocking is quick to reach.
func testConfig() *Config {
	return &Config{
		Listen:         ":0",
		MinLength:      5,
		DefaultTTL:     Dur(240 * time.Hour),
		ShortURLFormat: "https",
		Cache:          CacheConfig{TTL: Dur(6 * time.Hour), MaxEntries: 100},
		NotFoundLimit:  LimitConfig{PerMinute: 3, BlockFor: Dur(5 * time.Minute)},
	}
}

// testMeta writes the meta file every HTTP test starts from into dir and
// opens it: short.example (id 1), go.example (id 2) with a fallback, allowed
// targets and an alias, and account 1 on both, whose token is testToken. The
// file is written by hand, as a restore would, so that a server costs no
// syncs to build.
func testMeta(t *testing.T, dir string) *Meta {
	t.Helper()
	data, err := json.Marshal(metaFile{
		NextDomain:  3,
		NextAccount: 2,
		Domains: []Domain{
			{ID: 1, Host: "short.example", AllowedTargets: []string{}, Static: map[string]string{}},
			{
				ID:             2,
				Host:           "go.example",
				FallbackURL:    "https://example.org/home",
				AllowedTargets: []string{"example.com", "*.example.com"},
				Static:         map[string]string{"android": "https://example.com/android"},
			},
		},
		Accounts: []storedAccount{{
			Account:   Account{ID: testAccount, Name: "test", Domains: []uint16{1, 2}},
			TokenHash: sha256.Sum256([]byte(testToken)),
		}},
		Blocked: []string{},
	})
	must(t, err)
	m, err := OpenMeta(writeFile(t, dir, "meta.json", string(data)))
	must(t, err)
	return m
}

// newTestServer returns a server on a fresh data folder, with the meta file
// of testMeta. Each change function may adjust the config before the server
// is built.
func newTestServer(t *testing.T, changes ...func(*Config)) (*Server, *Store) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "data")
	st, err := OpenStore(dir)
	must(t, err)
	t.Cleanup(func() { st.Close() })
	cfg := testConfig()
	for _, change := range changes {
		change(cfg)
	}
	return NewServer(NewWatcher("", cfg), testMeta(t, dir), st, nil), st
}

// secondAccount adds an account on short.example and returns its token.
func secondAccount(t *testing.T, s *Server) string {
	t.Helper()
	_, token, err := s.meta.AddAccount("second", []string{"short.example"})
	must(t, err)
	return token
}

// do sends one request to the server and returns the recorded response.
func do(s *Server, method, host, path string, opts ...func(*http.Request)) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, nil)
	r.Host = host
	r.RemoteAddr = testRemote
	for _, opt := range opts {
		opt(r)
	}
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	return w
}

// withBody sets a JSON request body.
func withBody(json string) func(*http.Request) {
	return func(r *http.Request) {
		r.Body = io.NopCloser(strings.NewReader(json))
		r.ContentLength = int64(len(json))
		r.Header.Set("Content-Type", "application/json")
	}
}

// withHeader sets one request header.
func withHeader(key, value string) func(*http.Request) {
	return func(r *http.Request) { r.Header.Set(key, value) }
}

// withToken authorizes the request with the test account's token.
func withToken() func(*http.Request) {
	return withTokenOf(testToken)
}

// withTokenOf authorizes the request with the given token.
func withTokenOf(token string) func(*http.Request) {
	return withHeader("Authorization", "Bearer "+token)
}

// withRemote makes the request come from another client address.
func withRemote(addr string) func(*http.Request) {
	return func(r *http.Request) { r.RemoteAddr = addr }
}

// advance moves the server's clock forward by d.
func advance(s *Server, d time.Duration) {
	previous := s.now
	s.now = func() time.Time { return previous().Add(d) }
}

// status asserts the response status code.
func status(t *testing.T, w *httptest.ResponseRecorder, want int) {
	t.Helper()
	if w.Code != want {
		t.Fatalf("status %d, want %d; body: %.200s", w.Code, want, w.Body.String())
	}
}

// redirectsTo asserts a 302 to the given location.
func redirectsTo(t *testing.T, w *httptest.ResponseRecorder, location string) {
	t.Helper()
	status(t, w, http.StatusFound)
	eq(t, w.Header().Get("Location"), location)
}

// Tests for the API: tokens, creating, editing and deleting links.
package app

import (
	"bytes"
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

const (
	longURL     = "https://example.com/long/path?x=1"
	longURLBody = `{"url": "` + longURL + `"}`
)

// api sends an authorized API request with a JSON body ("" for none).
func api(s *Server, method, path, body string, opts ...func(*http.Request)) *httptest.ResponseRecorder {
	all := []func(*http.Request){withToken()}
	if body != "" {
		all = append(all, withBody(body))
	}
	return do(s, method, "short.example", path, append(all, opts...)...)
}

// linkIn decodes a link object from a response.
func linkIn(t *testing.T, w *httptest.ResponseRecorder) linkResponse {
	t.Helper()
	var link linkResponse
	must(t, json.Unmarshal(w.Body.Bytes(), &link))
	return link
}

// created sends POST /api/links, asserts 201 and returns the new link.
func created(t *testing.T, s *Server, body string) linkResponse {
	t.Helper()
	w := api(s, "POST", "/api/links", body)
	status(t, w, http.StatusCreated)
	return linkIn(t, w)
}

// badRequest asserts a 400 whose error message contains every fragment.
func badRequest(t *testing.T, w *httptest.ResponseRecorder, fragments ...string) {
	t.Helper()
	refused(t, w, http.StatusBadRequest, fragments...)
}

// refused asserts an error answer with the given status whose message
// contains every fragment.
func refused(t *testing.T, w *httptest.ResponseRecorder, want int, fragments ...string) {
	t.Helper()
	status(t, w, want)
	var body struct {
		Error string `json:"error"`
	}
	must(t, json.Unmarshal(w.Body.Bytes(), &body))
	if body.Error == "" {
		t.Fatalf("response has no error message: %s", w.Body.String())
	}
	for _, fragment := range fragments {
		if !strings.Contains(body.Error, fragment) {
			t.Fatalf("error %q does not mention %q", body.Error, fragment)
		}
	}
}

func TestAuth(t *testing.T) {
	s, _ := newTestServer(t)
	for name, header := range map[string]string{"none": "", "wrong": "Bearer wrong", "basic": "Basic abc", "no scheme": testToken} {
		t.Run(name, func(t *testing.T) {
			s, _ := newTestServer(t)
			opts := []func(*http.Request){withBody(longURLBody)}
			if header != "" {
				opts = append(opts, withHeader("Authorization", header))
			}
			w := do(s, "POST", "short.example", "/api/links", opts...)
			status(t, w, http.StatusUnauthorized)
			eq(t, w.Header().Get("WWW-Authenticate"), "Bearer")
		})
	}
	status(t, api(s, "POST", "/api/links", longURLBody), http.StatusCreated)
}

func TestAuthFailuresStrike(t *testing.T) {
	s, _ := newTestServer(t)
	for range 4 {
		w := do(s, "POST", "short.example", "/api/links", withBody(longURLBody), withHeader("Authorization", "Bearer wrong"))
		status(t, w, http.StatusUnauthorized)
	}
	status(t, api(s, "POST", "/api/links", longURLBody), http.StatusTooManyRequests)
}

// TestAuthWithoutAccount: the token of a disabled or deleted account, and an
// empty token, open nothing.
func TestAuthWithoutAccount(t *testing.T) {
	s, _ := newTestServer(t)
	must(t, s.meta.SetDisabled(testAccount, true))
	status(t, api(s, "POST", "/api/links", longURLBody), http.StatusUnauthorized)

	must(t, s.meta.DeleteAccount(testAccount))
	for _, header := range []string{"Bearer ", "Bearer " + testToken} {
		w := do(s, "POST", "short.example", "/api/links", withBody(longURLBody), withHeader("Authorization", header))
		status(t, w, http.StatusUnauthorized)
	}
}

func TestCreate(t *testing.T) {
	s, _ := newTestServer(t)
	link := created(t, s, longURLBody)

	if !regexp.MustCompile(`^[0-9A-Za-z]{5}$`).MatchString(link.Code) {
		t.Fatalf("code %q is not 5 base62 characters", link.Code)
	}
	eq(t, link.Domain, "short.example")
	eq(t, link.ShortURL, "https://short.example/"+link.Code)
	eq(t, link.URL, longURL)
	near(t, link.ExpiresAt, in(240*time.Hour))
	eq(t, link.Secured, false)
	eq(t, link.Created, true)

	redirectsTo(t, do(s, "GET", "short.example", "/"+link.Code), longURL)
}

func TestRepeatedCreateWithinTenMinutes(t *testing.T) {
	s, _ := newTestServer(t)
	first := created(t, s, longURLBody)

	w := api(s, "POST", "/api/links", longURLBody)
	status(t, w, http.StatusOK)
	again := linkIn(t, w)
	eq(t, again.Code, first.Code)
	eq(t, again.Created, false)

	advance(s, 11*time.Minute)
	later := created(t, s, longURLBody)
	if later.Code == first.Code {
		t.Fatalf("a request after ten minutes got the old code %q", first.Code)
	}

	otherLifetime := created(t, s, `{"url": "`+longURL+`", "expires_in": "3d"}`)
	if otherLifetime.Code == later.Code {
		t.Fatalf("another expires_in got the same code %q", later.Code)
	}

	w = api(s, "POST", "/api/links", longURLBody, withTokenOf(secondAccount(t, s)))
	status(t, w, http.StatusCreated)
	if code := linkIn(t, w).Code; code == later.Code {
		t.Fatalf("another account got the same code %q", code)
	}
}

func TestCreateOnOwnDomainsOnly(t *testing.T) {
	s, _ := newTestServer(t)
	second := withTokenOf(secondAccount(t, s)) // on short.example only

	w := api(s, "POST", "/api/links", `{"url": "https://example.com/a", "domain": "go.example"}`, second)
	refused(t, w, http.StatusForbidden, `domain: "go.example" is not available to this account`)

	w = api(s, "POST", "/api/links", longURLBody, second)
	status(t, w, http.StatusCreated)
	eq(t, linkIn(t, w).Domain, "short.example")
}

func TestCreateRefusesBlockedHost(t *testing.T) {
	s, _ := newTestServer(t)
	must(t, s.meta.Block("example.com"))
	badRequest(t, api(s, "POST", "/api/links", `{"url": "https://www.example.com/x"}`), "blocked")
}

// A look-alike spelling of a host is refused, or taken in the ASCII form a
// browser looks up, and then meets the block list.
func TestCreateRefusesLookAlikeHosts(t *testing.T) {
	s, st := newTestServer(t)
	for _, ok := range []string{"https://xn--e1afmkfd.example/", "https://example.com/", "https://192.0.2.1/", "https://example.com:8443/x", "https://[2001:db8::1]/"} {
		status(t, api(s, "POST", "/api/links", `{"url": "`+ok+`"}`), http.StatusCreated)
	}
	for raw, want := range lookAlikeHosts {
		before := st.syncs()
		w := api(s, "POST", "/api/links", `{"url": "`+raw+`"}`)
		if want.refused != "" {
			badRequest(t, w, "url:", want.refused)
			eq(t, st.syncs(), before) // no link was stored
		} else {
			// 200 for all but the first that becomes example.com/x: the
			// same request again gets the link it made.
			if w.Code != http.StatusCreated && w.Code != http.StatusOK {
				t.Fatalf("%s: status %d; body: %s", raw, w.Code, w.Body.String())
			}
			eq(t, linkIn(t, w).URL, want.target)
		}
	}
	must(t, s.meta.Block("example.com"))
	must(t, s.meta.Block("192.0.2.1"))
	before := st.syncs()
	for raw, want := range lookAlikeHosts {
		w := api(s, "POST", "/api/links", `{"url": "`+raw+`"}`)
		if want.refused != "" {
			badRequest(t, w, "url:", want.refused)
		} else {
			badRequest(t, w, `host "example.com" is blocked`)
		}
	}
	eq(t, st.syncs(), before) // no link was stored
}

func TestResponseHasNoOpens(t *testing.T) {
	s, _ := newTestServer(t)
	w := api(s, "POST", "/api/links", longURLBody)
	status(t, w, http.StatusCreated)
	if strings.Contains(w.Body.String(), `"opens"`) {
		t.Fatalf("the response has opens: %s", w.Body.String())
	}
}

func TestCreateFormats(t *testing.T) {
	s, _ := newTestServer(t)
	code := created(t, s, longURLBody).Code
	formats := map[string]string{
		"https":     "https://short.example/" + code,
		"https_www": "https://www.short.example/" + code,
		"bare":      "short.example/" + code,
		"www":       "www.short.example/" + code,
	}
	for format, want := range formats {
		t.Run(format, func(t *testing.T) {
			w := api(s, "POST", "/api/links", `{"url": "`+longURL+`", "format": "`+format+`"}`)
			status(t, w, http.StatusOK)
			eq(t, linkIn(t, w).ShortURL, want)
		})
	}
	badRequest(t, api(s, "POST", "/api/links", `{"url": "`+longURL+`", "format": "ftp"}`),
		"format", "https, https_www, bare, www")

	// Without "format" the config decides.
	s, _ = newTestServer(t, func(c *Config) { c.ShortURLFormat = "bare" })
	link := created(t, s, longURLBody)
	eq(t, link.ShortURL, "short.example/"+link.Code)
}

func TestCreateDomain(t *testing.T) {
	s, _ := newTestServer(t)
	link := created(t, s, `{"url": "https://docs.example.com/i/1", "domain": "go.example"}`)
	eq(t, link.Domain, "go.example")
	eq(t, link.ShortURL, "https://go.example/"+link.Code)
	redirectsTo(t, do(s, "GET", "go.example", "/"+link.Code), "https://docs.example.com/i/1")

	refused(t, api(s, "POST", "/api/links", `{"url": "`+longURL+`", "domain": "other.example"}`),
		http.StatusForbidden, `domain: "other.example" is not available to this account`)
	badRequest(t, api(s, "POST", "/api/links", `{"url": "https://example.org/x", "domain": "go.example"}`), "not allowed")
}

func TestCreateRejectsBadTargets(t *testing.T) {
	s, _ := newTestServer(t)
	badRequest(t, api(s, "POST", "/api/links", `{"url": "ftp://example.com/a"}`), "http")
	badRequest(t, api(s, "POST", "/api/links", `{"url": "https://go.example/abcde"}`), "short link")
}

func TestCreateExpiresIn(t *testing.T) {
	s, _ := newTestServer(t)
	link := created(t, s, `{"url": "`+longURL+`", "expires_in": "3d"}`)
	near(t, link.ExpiresAt, in(72*time.Hour))

	badRequest(t, api(s, "POST", "/api/links", `{"url": "https://example.com/b", "expires_in": "10x"}`), "expires_in", "30m, 12h, 10d")
	badRequest(t, api(s, "POST", "/api/links", `{"url": "https://example.com/b", "expires_in": "0"}`), "expires_in")
	badRequest(t, api(s, "POST", "/api/links", `{"url": "https://example.com/b", "expires_in": "40000d"}`), "expires_in", "36500d")
}

func TestCreateSecured(t *testing.T) {
	s, _ := newTestServer(t)
	body := `{"url": "` + longURL + `", "expires_after_open": "1m"}`
	first := created(t, s, body)
	eq(t, first.Secured, true)

	second := created(t, s, body) // a secured link is never reused
	if second.Code == first.Code {
		t.Fatalf("both secured links have code %q", first.Code)
	}

	oneTime := created(t, s, `{"url": "`+longURL+`", "expires_after_open": "0"}`)
	eq(t, oneTime.Secured, true)
	opensTo(t, do(s, "POST", "short.example", "/"+oneTime.Code), longURL)
	gone(t, do(s, "POST", "short.example", "/"+oneTime.Code))

	badRequest(t, api(s, "POST", "/api/links", `{"url": "`+longURL+`", "expires_after_open": "-1m"}`), "expires_after_open")

	// The longest window there is: more seconds than a 32-bit number holds.
	longest := created(t, s, `{"url": "`+longURL+`", "expires_after_open": "36500d"}`)
	opensTo(t, do(s, "POST", "short.example", "/"+longest.Code), longURL)
}

func TestCreatePasswordLink(t *testing.T) {
	s, _ := newTestServer(t)
	w := api(s, "POST", "/api/links", `{"url": "`+longURL+`", "password": "`+testPassword+`"}`)
	status(t, w, http.StatusCreated)
	if !strings.Contains(w.Body.String(), `"password":true`) {
		t.Fatalf(`the response lacks "password":true: %s`, w.Body.String())
	}
	link := linkIn(t, w)
	eq(t, link.Password, true)
	eq(t, link.Secured, false)
	opensTo(t, do(s, "POST", "short.example", "/"+link.Code, withPassword(testPassword)), longURL)

	both := created(t, s, `{"url": "`+longURL+`", "password": "`+testPassword+`", "expires_after_open": "0"}`)
	eq(t, both.Password, true)
	eq(t, both.Secured, true)

	w = api(s, "POST", "/api/links", `{"url": "https://example.com/open"}`)
	status(t, w, http.StatusCreated)
	if !strings.Contains(w.Body.String(), `"password":false`) {
		t.Fatalf(`the response lacks "password":false: %s`, w.Body.String())
	}

	for _, password := range []string{"abc", strings.Repeat("a", maxPasswordLength+1), ""} {
		badRequest(t, api(s, "POST", "/api/links", `{"url": "`+longURL+`", "password": "`+password+`"}`), "password", "4 to 128 bytes")
	}
	for _, password := range []string{"abcd", strings.Repeat("я", maxPasswordLength/2)} {
		link := created(t, s, `{"url": "`+longURL+`", "password": "`+password+`"}`)
		opensTo(t, do(s, "POST", "short.example", "/"+link.Code, withPassword(password)), longURL)
	}
	badRequest(t, api(s, "POST", "/api/links", `{"url": "`+longURL+`", "password": 1234}`), `"password" must be a string`)

	for _, body := range []string{`{"expires_in": "1h"}`, `{"url": "https://example.com/other"}`} {
		refused(t, api(s, "PATCH", "/api/links/"+link.Code, body), http.StatusConflict, "password")
	}
}

// A password link is never handed out again for a repeated request, and
// never put into the link cache: a later GET must ask for the password.
func TestPasswordLinkIsNotCachedOrRepeated(t *testing.T) {
	s, _ := newTestServer(t)
	body := `{"url": "` + longURL + `", "password": "` + testPassword + `"}`
	first := created(t, s, body)
	second := created(t, s, body)
	if second.Code == first.Code {
		t.Fatalf("both password links have code %q", first.Code)
	}
	eq(t, s.cache.Len(), 0)

	opensTo(t, do(s, "POST", "short.example", "/"+first.Code, withPassword(testPassword)), longURL)
	isPasswordPage(t, do(s, "GET", "short.example", "/"+first.Code))
	eq(t, s.cache.Len(), 0)
}

// "password" applies to every way of creating links. The links of one
// request share one salt and one hash: the password is hashed once.
func TestPasswordForTextAndURLs(t *testing.T) {
	s, st := newTestServer(t)
	for name, fields := range map[string]map[string]any{
		"text": {"text": "See " + urlA + " and " + urlB, "password": testPassword},
		"urls": {"text": "x", "urls": []string{urlA, urlB}, "password": testPassword, "expires_after_open": "1m"},
	} {
		t.Run(name, func(t *testing.T) {
			got := shortened(t, s, fields)
			eq(t, len(got.Links), 2)
			var stored []Link
			for i, link := range got.Links {
				eq(t, link.Password, true)
				l, state, err := st.Lookup(1, link.Code)
				must(t, err)
				eq(t, state, LinkFound)
				stored = append(stored, l)
				isPasswordPage(t, do(s, "GET", "short.example", "/"+link.Code))
				if i == 0 { // the second link has the same salt and hash, checked below
					wrongPassword(t, do(s, "POST", "short.example", "/"+link.Code, withPassword("wrong one")))
				}
				opensTo(t, do(s, "POST", "short.example", "/"+link.Code, withPassword(testPassword)), link.URL)
			}
			eq(t, bytes.Equal(stored[0].Salt, stored[1].Salt) && bytes.Equal(stored[0].Hash, stored[1].Hash), true)
		})
	}
	eq(t, s.cache.Len(), 0)
}

// The password is kept nowhere: not in the data folder, not in a log, not
// in an answer. Only its salt and hash are stored.
func TestPasswordIsWrittenNowhere(t *testing.T) {
	const password = "unforgettable-9271"
	slogged := captureLog(t)
	s, st := newTestServer(t)
	logDir := logTo(t, s)

	var answers []*httptest.ResponseRecorder
	send := func(w *httptest.ResponseRecorder) *httptest.ResponseRecorder {
		answers = append(answers, w)
		return w
	}
	link := linkIn(t, send(api(s, "POST", "/api/links", `{"url": "`+longURL+`", "password": "`+password+`"}`)))
	eq(t, link.Password, true)
	send(api(s, "POST", "/api/links", textRequest(t, map[string]any{"text": "See " + urlA, "password": password})))
	send(api(s, "POST", "/api/links", `{"url": "`+longURL+`", "password": "`+password+strings.Repeat("x", maxPasswordLength)+`"}`))
	send(api(s, "POST", "/api/links", `{"url": "ftp://example.com/x", "password": "`+password+`"}`))
	send(api(s, "PATCH", "/api/links/"+link.Code, `{"expires_in": "1h"}`))
	send(do(s, "POST", "short.example", "/"+link.Code, withPassword(password)))
	send(do(s, "POST", "short.example", "/"+link.Code, withPassword(password+"!")))
	st.failWrite = errors.New("disk full")
	send(api(s, "POST", "/api/links", `{"url": "https://example.com/fails", "password": "`+password+`"}`))
	st.failWrite = nil

	for i, w := range answers {
		if strings.Contains(w.Body.String(), password) {
			t.Errorf("answer %d holds the password: %s", i, w.Body.String())
		}
	}
	for _, dir := range []string{st.dir, logDir} {
		must(t, filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
			if err != nil || entry.IsDir() {
				return err
			}
			data, err := os.ReadFile(path)
			if err == nil && bytes.Contains(data, []byte(password)) {
				t.Errorf("%s holds the password", path)
			}
			return err
		}))
	}
	if strings.Contains(slogged.String(), password) {
		t.Errorf("the service log holds the password:\n%s", slogged)
	}
	if len(loggedLines(t, logDir, "access")) == 0 || !strings.Contains(slogged.String(), "disk full") {
		t.Fatal("nothing was logged: the test proves nothing")
	}
}

func TestCreateBadBodies(t *testing.T) {
	s, _ := newTestServer(t)
	huge := `{"url": "https://example.com/` + strings.Repeat("a", 70<<10) + `"}`
	cases := []struct {
		name, body string
		fragments  []string
	}{
		{"empty object", `{}`, []string{"url"}},
		{"url and text", `{"url": "` + longURL + `", "text": "x"}`, []string{"url", "text"}},
		{"unknown field", `{"url": "` + longURL + `", "foo": 1}`, []string{"foo"}},
		{"wrong type", `{"url": 5}`, nil},
		{"not json", `hello`, nil},
		{"trailing data", longURLBody + ` {}`, nil},
		{"urls without text", `{"url": "` + longURL + `", "urls": ["` + longURL + `"]}`, []string{"urls"}},
		{"too large", huge, []string{"64 KB"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			badRequest(t, api(s, "POST", "/api/links", c.body), c.fragments...)
		})
	}
	t.Run("empty body", func(t *testing.T) {
		badRequest(t, do(s, "POST", "short.example", "/api/links", withToken()))
	})
}

func TestPatch(t *testing.T) {
	s, _ := newTestServer(t)
	link := created(t, s, longURLBody)
	other := created(t, s, `{"url": "https://example.com/other"}`)
	path := "/api/links/" + link.Code
	redirectsTo(t, do(s, "GET", "short.example", "/"+link.Code), longURL) // cached

	w := api(s, "PATCH", path, `{"url": "https://example.com/new"}`)
	status(t, w, http.StatusOK)
	patched := linkIn(t, w)
	eq(t, patched.URL, "https://example.com/new")
	eq(t, patched.Code, link.Code)
	eq(t, patched.Created, false)
	redirectsTo(t, do(s, "GET", "short.example", "/"+link.Code), "https://example.com/new")

	w = api(s, "PATCH", path, `{"expires_in": "1h"}`)
	status(t, w, http.StatusOK)
	near(t, linkIn(t, w).ExpiresAt, in(time.Hour))

	badRequest(t, api(s, "PATCH", path, `{}`), "url", "expires_in")
	badRequest(t, api(s, "PATCH", path, `{"expires_in": "0"}`), "expires_in")
	badRequest(t, api(s, "PATCH", path, `{"url": "ftp://example.com/x"}`), "http")
	status(t, api(s, "PATCH", "/api/links/nope1", `{"expires_in": "1h"}`), http.StatusNotFound)
	// Two links may lead to the same URL.
	status(t, api(s, "PATCH", path, `{"url": "`+other.URL+`"}`), http.StatusOK)

	securedLink := created(t, s, `{"url": "`+longURL+`", "expires_after_open": "1m"}`)
	status(t, api(s, "PATCH", "/api/links/"+securedLink.Code, `{"expires_in": "1h"}`), http.StatusConflict)
}

func TestPatchOnAnotherDomain(t *testing.T) {
	s, _ := newTestServer(t)
	link := created(t, s, `{"url": "https://docs.example.com/i/1", "domain": "go.example"}`)
	path := "/api/links/" + link.Code

	// Without ?domain= the default domain is searched.
	status(t, api(s, "PATCH", path, `{"expires_in": "1h"}`), http.StatusNotFound)

	w := api(s, "PATCH", path+"?domain=go.example", `{"url": "https://docs.example.com/i/2"}`)
	status(t, w, http.StatusOK)
	eq(t, linkIn(t, w).ShortURL, "https://go.example/"+link.Code)
	redirectsTo(t, do(s, "GET", "go.example", "/"+link.Code), "https://docs.example.com/i/2")

	refused(t, api(s, "PATCH", path+"?domain=other.example", `{"expires_in": "1h"}`),
		http.StatusForbidden, `domain: "other.example" is not available to this account`)
}

func TestPatchRevivesExpired(t *testing.T) {
	s, _ := newTestServer(t)
	old := storeLink(t, s, NewLink{URL: testURL, ExpiresAt: s.now().Add(-time.Hour)})
	status(t, do(s, "GET", "short.example", "/"+old), http.StatusNotFound)

	status(t, api(s, "PATCH", "/api/links/"+old, `{"expires_in": "1h"}`), http.StatusOK)
	redirectsTo(t, do(s, "GET", "short.example", "/"+old), testURL)
}

func TestOthersLinkIsNotFound(t *testing.T) {
	s, _ := newTestServer(t)
	link := created(t, s, longURLBody)
	securedLink := created(t, s, `{"url": "`+longURL+`", "expires_after_open": "1m"}`)
	second := withTokenOf(secondAccount(t, s))
	nothing := api(s, "DELETE", "/api/links/nope1", "", second) // a link that does not exist
	status(t, nothing, http.StatusNotFound)

	for _, w := range []*httptest.ResponseRecorder{
		api(s, "PATCH", "/api/links/"+link.Code, `{"url": "https://example.com/other"}`, second),
		api(s, "PATCH", "/api/links/"+securedLink.Code, `{"expires_in": "1h"}`, second),
		api(s, "DELETE", "/api/links/"+link.Code, "", second),
		api(s, "DELETE", "/api/links/"+securedLink.Code, "", second),
	} {
		status(t, w, http.StatusNotFound)
		eq(t, w.Body.String(), nothing.Body.String())
	}
	redirectsTo(t, do(s, "GET", "short.example", "/"+link.Code), longURL)
	status(t, do(s, "GET", "short.example", "/"+securedLink.Code), http.StatusOK)
}

func TestDelete(t *testing.T) {
	s, _ := newTestServer(t)
	link := created(t, s, longURLBody)
	path := "/api/links/" + link.Code
	redirectsTo(t, do(s, "GET", "short.example", "/"+link.Code), longURL) // cached

	w := api(s, "DELETE", path, "")
	status(t, w, http.StatusNoContent)
	eq(t, w.Body.Len(), 0)
	status(t, do(s, "GET", "short.example", "/"+link.Code), http.StatusNotFound)
	status(t, api(s, "DELETE", path, ""), http.StatusNotFound)

	securedLink := created(t, s, `{"url": "`+longURL+`", "expires_after_open": "1m"}`)
	status(t, api(s, "DELETE", "/api/links/"+securedLink.Code, ""), http.StatusNoContent)
	gone(t, do(s, "POST", "short.example", "/"+securedLink.Code))
}

func TestMalformedCodeIsNotFound(t *testing.T) {
	s, _ := newTestServer(t)
	for _, code := range []string{"%00abc", "ab", "a.b.c"} {
		t.Run(code, func(t *testing.T) {
			status(t, api(s, "PATCH", "/api/links/"+code, `{"expires_in": "1d"}`), http.StatusNotFound)
			status(t, api(s, "DELETE", "/api/links/"+code, ""), http.StatusNotFound)
		})
	}
}

func TestResponseKeepsURLReadable(t *testing.T) {
	s, _ := newTestServer(t)
	const target = "https://example.com/p?a=1&b=2"
	w := api(s, "POST", "/api/links", `{"url": "`+target+`"}`)
	status(t, w, http.StatusCreated)
	if !strings.Contains(w.Body.String(), `"url":"`+target+`"`) {
		t.Fatalf("the URL is not written as it is: %s", w.Body.String())
	}
}

func TestWrongTypeErrorNamesTheField(t *testing.T) {
	s, _ := newTestServer(t)
	cases := map[string]string{
		`{"url": 5}`:                             `"url" must be a string`,
		`{"text": "x", "urls": "y"}`:             `"urls" must be a list of strings`,
		`{"text": "x", "urls": [1]}`:             `"urls" must be a list of strings`,
		`{"url": "x", "expires_after_open": 60}`: `"expires_after_open" must be a string`,
	}
	for body, want := range cases {
		t.Run(body, func(t *testing.T) {
			badRequest(t, api(s, "POST", "/api/links", body), want)
		})
	}
	badRequest(t, api(s, "PATCH", "/api/links/aaaaa", `{"expires_in": 60}`), `"expires_in" must be a string`)
	badRequest(t, api(s, "POST", "/api/links", `[1]`), "must be a JSON object")
}

func TestAPIIgnoresHost(t *testing.T) {
	s, _ := newTestServer(t)
	w := do(s, "POST", "internal:8080", "/api/links", withToken(), withBody(longURLBody))
	status(t, w, http.StatusCreated)
	eq(t, linkIn(t, w).Domain, "short.example")
}

func TestAPIUnknownEndpoint(t *testing.T) {
	s, _ := newTestServer(t)
	w := api(s, "GET", "/api/links", "")
	status(t, w, http.StatusNotFound)
	eq(t, w.Header().Get("Content-Type"), "application/json")
}

func TestConfigChangeAppliesLive(t *testing.T) {
	s, _ := newTestServer(t)
	changed := testConfig()
	changed.MinLength = 7
	s.cfg.set(changed)

	eq(t, len(created(t, s, longURLBody).Code), 7)
}

func TestAPIEventsLogged(t *testing.T) {
	s, _ := newTestServer(t)
	dir := logTo(t, s)

	link := created(t, s, longURLBody)
	status(t, api(s, "PATCH", "/api/links/"+link.Code, `{"expires_in": "1h"}`), http.StatusOK)
	status(t, api(s, "DELETE", "/api/links/"+link.Code, ""), http.StatusNoContent)

	lines := loggedLines(t, dir, "access")
	eq(t, len(lines), 3)
	for i, event := range []string{"create", "update", "delete"} {
		eq(t, lines[i], event+"\tshort.example\t"+link.Code+"\t203.0.113.7\t1") // account 1, never its token
	}
	raw, err := os.ReadFile(filepath.Join(dir, "access-"+time.Now().UTC().Format(dayLayout)+".log"))
	must(t, err)
	if strings.Contains(string(raw), testToken) {
		t.Fatal("the log contains the raw API token")
	}
}

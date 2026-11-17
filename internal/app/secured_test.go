// Tests for secured and password links: the pages a GET gets and the POST
// that opens.
package app

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const goneBody = `{"error":"link not found or expired"}`

// secured creates a secured link on short.example that expires in an hour
// unless it is opened, and returns its code and target URL.
func secured(t *testing.T, s *Server, afterOpen time.Duration) (code, target string) {
	t.Helper()
	target = fmt.Sprintf("https://example.com/secret/%d", liveLinks.Add(1))
	return storeLink(t, s, NewLink{URL: target, ExpiresAt: s.now().Add(time.Hour), AfterOpen: &afterOpen}), target
}

// opensTo asserts that a POST response hands out the target URL.
func opensTo(t *testing.T, w *httptest.ResponseRecorder, target string) {
	t.Helper()
	status(t, w, http.StatusOK)
	var body struct {
		URL string `json:"url"`
	}
	must(t, json.Unmarshal(w.Body.Bytes(), &body))
	eq(t, body.URL, target)
}

// gone asserts the JSON answer for a link that cannot be opened.
func gone(t *testing.T, w *httptest.ResponseRecorder) {
	t.Helper()
	status(t, w, http.StatusNotFound)
	eq(t, strings.TrimSpace(w.Body.String()), goneBody)
}

func TestSecuredGetServesPage(t *testing.T) {
	s, _ := newTestServer(t)
	code, _ := secured(t, s, time.Minute)

	w := do(s, "GET", "short.example", "/"+code)
	status(t, w, http.StatusOK)
	if contentType := w.Header().Get("Content-Type"); !strings.HasPrefix(contentType, "text/html") {
		t.Fatalf("Content-Type %q, want text/html", contentType)
	}
	eq(t, w.Header().Get("Cache-Control"), "no-store")

	body := w.Body.String()
	for _, want := range []string{`<meta name="robots" content="noindex">`, "Opening link"} {
		if !strings.Contains(body, want) {
			t.Errorf("page lacks %q", want)
		}
	}
	// Nothing a link preview could show, and above all not the target.
	for _, banned := range []string{"example.com", "og:", "description"} {
		if strings.Contains(body, banned) {
			t.Errorf("page contains %q", banned)
		}
	}
}

func TestSecuredGetDoesNotBurn(t *testing.T) {
	s, _ := newTestServer(t)
	code, target := secured(t, s, 0)

	for range 5 { // preview bots only GET
		status(t, do(s, "GET", "short.example", "/"+code), http.StatusOK)
	}
	opensTo(t, do(s, "POST", "short.example", "/"+code), target)
}

func TestSecuredOneTime(t *testing.T) {
	s, _ := newTestServer(t)
	code, target := secured(t, s, 0)

	opensTo(t, do(s, "POST", "short.example", "/"+code), target)
	gone(t, do(s, "POST", "short.example", "/"+code))

	w := do(s, "GET", "short.example", "/"+code)
	status(t, w, http.StatusNotFound)
	if !strings.Contains(w.Body.String(), "Link not found") {
		t.Fatalf("body is not the 404 page: %.200s", w.Body.String())
	}
}

func TestSecuredWindow(t *testing.T) {
	s, _ := newTestServer(t)
	code, target := secured(t, s, time.Minute)

	opensTo(t, do(s, "POST", "short.example", "/"+code), target)
	opensTo(t, do(s, "POST", "short.example", "/"+code), target)

	// The page is still served inside the window.
	status(t, do(s, "GET", "short.example", "/"+code), http.StatusOK)
}

func TestSecuredPostOnRegularLink(t *testing.T) {
	s, _ := newTestServer(t)
	code, target := live(t, s)

	for range 10 {
		gone(t, do(s, "POST", "short.example", "/"+code))
	}
	redirectsTo(t, do(s, "GET", "short.example", "/"+code), target) // not blocked
}

func TestSecuredPostMissingStrikes(t *testing.T) {
	s, _ := newTestServer(t)
	for i := range 4 {
		gone(t, do(s, "POST", "short.example", fmt.Sprintf("/none%d", i)))
	}
	status(t, do(s, "POST", "short.example", "/none9"), http.StatusTooManyRequests)
}

func TestSecuredUnknownHost(t *testing.T) {
	s, _ := newTestServer(t)
	code, _ := secured(t, s, 0)
	gone(t, do(s, "POST", "other.example", "/"+code))
}

func TestSecuredIsPerDomain(t *testing.T) {
	s, _ := newTestServer(t)
	code, target := secured(t, s, 0)
	gone(t, do(s, "POST", "go.example", "/"+code))
	opensTo(t, do(s, "POST", "short.example", "/"+code), target) // still unopened
}

// testPassword is the password of the links passwordLink creates.
const testPassword = "correct horse"

const wrongPasswordBody = `{"error":"wrong password"}`

// testHash returns a salt and the hash of testPassword with it, made once:
// a hash takes a while, above all under the race detector.
var testHash = sync.OnceValues(func() (salt, hash []byte) {
	salt = newSalt()
	return salt, hashPassword(testPassword, salt)
})

// passwordLink creates a link on short.example that asks for testPassword
// and expires in an hour; with afterOpen set it is secured too. It returns
// its code and target URL.
func passwordLink(t *testing.T, s *Server, afterOpen *time.Duration) (code, target string) {
	t.Helper()
	target = fmt.Sprintf("https://example.com/vault/%d", liveLinks.Add(1))
	salt, hash := testHash()
	return storeLink(t, s, NewLink{URL: target, ExpiresAt: s.now().Add(time.Hour), AfterOpen: afterOpen, Salt: salt, Hash: hash}), target
}

// withPassword sends a password the way the password page does.
func withPassword(password string) func(*http.Request) {
	body, err := json.Marshal(map[string]string{"password": password})
	if err != nil {
		panic(err)
	}
	return withBody(string(body))
}

// wrongPassword asserts the answer to a wrong password.
func wrongPassword(t *testing.T, w *httptest.ResponseRecorder) {
	t.Helper()
	status(t, w, http.StatusForbidden)
	eq(t, strings.TrimSpace(w.Body.String()), wrongPasswordBody)
}

// countChecks makes the server count the password checks it makes, and
// returns the count.
func countChecks(s *Server) *atomic.Int64 {
	var n atomic.Int64
	check := s.passwordCheck
	s.passwordCheck = func(password string, salt, hash []byte) bool {
		n.Add(1)
		return check(password, salt, hash)
	}
	return &n
}

// isPasswordPage asserts that a response is the password page.
func isPasswordPage(t *testing.T, w *httptest.ResponseRecorder) {
	t.Helper()
	status(t, w, http.StatusOK)
	if !strings.Contains(w.Body.String(), `type="password"`) {
		t.Fatalf("not the password page: %.300s", w.Body.String())
	}
}

const (
	lockedBody = `{"error":"too many wrong passwords; try again in a few minutes"}`
	busyBody   = `{"error":"too many attempts at once; try again"}`
)

// isLocked asserts the answer for a link locked after wrong passwords.
func isLocked(t *testing.T, w *httptest.ResponseRecorder) {
	t.Helper()
	status(t, w, http.StatusTooManyRequests)
	eq(t, strings.TrimSpace(w.Body.String()), lockedBody)
}

// isBusy asserts the answer for an attempt turned away because others are
// being checked: try again in a second, with no word of wrong passwords or
// of minutes.
func isBusy(t *testing.T, w *httptest.ResponseRecorder) {
	t.Helper()
	status(t, w, http.StatusTooManyRequests)
	eq(t, strings.TrimSpace(w.Body.String()), busyBody)
	eq(t, w.Header().Get("Retry-After"), "1")
}

// holdChecks makes the server's password checks wait until release is
// called, and returns the number of checks begun.
func holdChecks(t *testing.T, s *Server) (begun *atomic.Int64, release func()) {
	begun = new(atomic.Int64)
	gate := make(chan struct{})
	check := s.passwordCheck
	s.passwordCheck = func(password string, salt, hash []byte) bool {
		begun.Add(1)
		<-gate
		return check(password, salt, hash)
	}
	var once sync.Once
	release = func() { once.Do(func() { close(gate) }) }
	t.Cleanup(release)
	return begun, release
}

// sendAtOnce sends n POSTs with password to code, each from its own
// address, without waiting for one another. It returns the answers as
// they come, and the number turned away with 429 so far.
func sendAtOnce(s *Server, code, password string, n int) (<-chan *httptest.ResponseRecorder, *atomic.Int64) {
	answers := make(chan *httptest.ResponseRecorder, n)
	turnedAway := new(atomic.Int64)
	for i := range n {
		go func() {
			w := do(s, "POST", "short.example", "/"+code, withPassword(password), withRemote(fmt.Sprintf("192.0.2.%d:1234", 10+i)))
			if w.Code == http.StatusTooManyRequests {
				turnedAway.Add(1)
			}
			answers <- w
		}()
	}
	return answers, turnedAway
}

func TestPasswordPage(t *testing.T) {
	s, _ := newTestServer(t)
	code, _ := passwordLink(t, s, nil)

	w := do(s, "GET", "short.example", "/"+code)
	isPasswordPage(t, w)
	if contentType := w.Header().Get("Content-Type"); !strings.HasPrefix(contentType, "text/html") {
		t.Fatalf("Content-Type %q, want text/html", contentType)
	}
	eq(t, w.Header().Get("Cache-Control"), "no-store")
	body := w.Body.String()
	for _, want := range []string{`<meta name="robots" content="noindex">`, `<meta name="referrer" content="no-referrer">`, "Password"} {
		if !strings.Contains(body, want) {
			t.Errorf("page lacks %q", want)
		}
	}
	for _, banned := range []string{"example.com", "og:", "description"} {
		if strings.Contains(body, banned) {
			t.Errorf("page contains %q", banned)
		}
	}

	// The same headers as the page of a secured link.
	securedCode, _ := secured(t, s, time.Minute)
	open := do(s, "GET", "short.example", "/"+securedCode)
	status(t, open, http.StatusOK)
	eq(t, len(w.Header()), len(open.Header()))
	for key := range open.Header() {
		eq(t, w.Header().Get(key), open.Header().Get(key))
	}
}

func TestPasswordOpens(t *testing.T) {
	s, _ := newTestServer(t)
	dir := logTo(t, s)
	code, target := passwordLink(t, s, nil)

	for range 3 {
		opensTo(t, do(s, "POST", "short.example", "/"+code, withPassword(testPassword)), target)
	}
	wrongPassword(t, do(s, "POST", "short.example", "/"+code, withPassword("correct horsf")))
	wrongPassword(t, do(s, "POST", "short.example", "/"+code))
	isPasswordPage(t, do(s, "GET", "short.example", "/"+code)) // still asks

	lines := loggedLines(t, dir, "access")
	eq(t, len(lines), 3) // each opening; nothing for a wrong password
	for _, line := range lines {
		eq(t, line, "open\tshort.example\t"+code+"\t203.0.113.7\t")
	}
}

func TestPasswordWithOneTime(t *testing.T) {
	s, _ := newTestServer(t)
	once := time.Duration(0)
	code, target := passwordLink(t, s, &once)

	isPasswordPage(t, do(s, "GET", "short.example", "/"+code)) // not the page of a secured link
	wrongPassword(t, do(s, "POST", "short.example", "/"+code, withPassword("wrong one")))
	wrongPassword(t, do(s, "POST", "short.example", "/"+code)) // no password: Open is not reached
	opensTo(t, do(s, "POST", "short.example", "/"+code, withPassword(testPassword)), target)
	gone(t, do(s, "POST", "short.example", "/"+code, withPassword(testPassword)))
	status(t, do(s, "GET", "short.example", "/"+code), http.StatusNotFound)
}

func TestWrongPasswordsLockTheLink(t *testing.T) {
	s, _ := newTestServer(t)
	code, target := passwordLink(t, s, nil)
	other, otherTarget := passwordLink(t, s, nil)
	from := func(i int) func(*http.Request) { return withRemote(fmt.Sprintf("203.0.113.%d:1234", 10+i)) }

	for i := range 5 {
		wrongPassword(t, do(s, "POST", "short.example", "/"+code, withPassword("wrong one"), from(i)))
	}
	isLocked(t, do(s, "POST", "short.example", "/"+code, withPassword(testPassword), from(5)))
	isPasswordPage(t, do(s, "GET", "short.example", "/"+code, from(5))) // the link itself still works
	opensTo(t, do(s, "POST", "short.example", "/"+other, withPassword(testPassword), from(5)), otherTarget)

	advance(s, 5*time.Minute+time.Second)
	opensTo(t, do(s, "POST", "short.example", "/"+code, withPassword(testPassword), from(5)), target)
}

// Guesses sent all at once do not get past the lock while the first ones
// are still being checked: a check takes its place before it is hashed, and
// no more are checked at once than could still lock the link. When the
// checks end, the five wrong ones lock it for five minutes.
func TestPasswordsSentAtOnceAreCountedBeforeTheCheck(t *testing.T) {
	s, _ := newTestServer(t)
	s.locks.maxChecks = maxWrongPasswords + 1 // the cap of the whole server is not what is tested here
	code, target := passwordLink(t, s, nil)
	begun, release := holdChecks(t, s)

	const guesses = 12
	answers, turnedAway := sendAtOnce(s, code, "wrong one", guesses)
	waitFor(t, "every guess to be checked or turned away", func() bool { return begun.Load()+turnedAway.Load() == guesses })
	eq(t, begun.Load(), int64(maxWrongPasswords))
	release()
	for range guesses {
		if w := <-answers; w.Code == http.StatusTooManyRequests {
			isBusy(t, w)
		} else {
			wrongPassword(t, w)
		}
	}

	isLocked(t, do(s, "POST", "short.example", "/"+code, withPassword(testPassword)))
	advance(s, 5*time.Minute-time.Second)
	isLocked(t, do(s, "POST", "short.example", "/"+code, withPassword(testPassword)))
	advance(s, time.Second)
	opensTo(t, do(s, "POST", "short.example", "/"+code, withPassword(testPassword)), target)
}

// Right passwords sent at once are not wrong ones: those beyond the five
// being checked are asked to try again in a second, not told the link is
// locked, and are neither counted nor struck. The link stays open.
func TestRightPasswordsSentAtOnceDoNotLock(t *testing.T) {
	s, _ := newTestServer(t)
	s.locks.maxChecks = maxWrongPasswords + 1 // the cap of the whole server is not what is tested here
	code, target := passwordLink(t, s, nil)
	begun, release := holdChecks(t, s)

	const people = maxWrongPasswords + 1
	answers, turnedAway := sendAtOnce(s, code, testPassword, people)
	waitFor(t, "every password to be checked or turned away", func() bool { return begun.Load()+turnedAway.Load() == people })
	eq(t, begun.Load(), int64(maxWrongPasswords))
	for range 4 { // from one address: a strike each would block it
		isBusy(t, do(s, "POST", "short.example", "/"+code, withPassword(testPassword)))
	}
	release()
	for range people {
		if w := <-answers; w.Code == http.StatusTooManyRequests {
			isBusy(t, w)
		} else {
			opensTo(t, w, target)
		}
	}
	eq(t, turnedAway.Load(), int64(1))

	eq(t, s.locks.Locked(cacheKey("short.example", code), s.now()), false)
	opensTo(t, do(s, "POST", "short.example", "/"+code, withPassword(testPassword)), target)
	eq(t, s.locks.Len(), 0) // nothing counted
}

// With four wrong passwords on record only one more password is checked at
// a time: a second right one is turned away as busy, not as locked, and
// the right one that is checked sets the count back to 0.
func TestOnePlaceLeftAfterFourWrongPasswords(t *testing.T) {
	s, _ := newTestServer(t)
	code, target := passwordLink(t, s, nil)
	from := func(i int) func(*http.Request) { return withRemote(fmt.Sprintf("203.0.113.%d:1234", 10+i)) }
	for i := range 4 {
		wrongPassword(t, do(s, "POST", "short.example", "/"+code, withPassword("wrong one"), from(i)))
	}

	begun, release := holdChecks(t, s)
	answers, _ := sendAtOnce(s, code, testPassword, 1)
	waitFor(t, "the first right password to be checked", func() bool { return begun.Load() == 1 })
	isBusy(t, do(s, "POST", "short.example", "/"+code, withPassword(testPassword), from(4)))
	release()
	opensTo(t, <-answers, target)
	eq(t, s.locks.Len(), 0)

	for i := range 4 {
		wrongPassword(t, do(s, "POST", "short.example", "/"+code, withPassword("wrong one"), from(5+i)))
	}
	opensTo(t, do(s, "POST", "short.example", "/"+code, withPassword(testPassword), from(9)), target)
}

// At most so many password checks run at once on all links together. An
// attempt beyond them gets the answer of an attempt whose own link is busy:
// try again in a second. It is not hashed, counted or struck.
func TestPasswordChecksAreCappedForTheWholeServer(t *testing.T) {
	s, _ := newTestServer(t)
	s.locks.maxChecks = 2
	first, firstTarget := passwordLink(t, s, nil)
	second, secondTarget := passwordLink(t, s, nil)
	third, thirdTarget := passwordLink(t, s, nil)
	begun, release := holdChecks(t, s)

	answers := make([]chan *httptest.ResponseRecorder, 2)
	for i, code := range []string{first, second} {
		answers[i] = make(chan *httptest.ResponseRecorder, 1)
		go func() {
			answers[i] <- do(s, "POST", "short.example", "/"+code, withPassword(testPassword), withRemote(fmt.Sprintf("192.0.2.%d:1234", 10+i)))
		}()
	}
	waitFor(t, "two checks to begin", func() bool { return begun.Load() == 2 })
	turnedAway := make(chan *httptest.ResponseRecorder, 1)
	go func() { turnedAway <- do(s, "POST", "short.example", "/"+third, withPassword(testPassword)) }()
	var w *httptest.ResponseRecorder
	waitFor(t, "the third link's password to be answered or checked", func() bool {
		select {
		case w = <-turnedAway:
			return true
		default:
			return begun.Load() > 2
		}
	})
	eq(t, begun.Load(), int64(2)) // the third link's password was not hashed
	isBusy(t, w)
	for range 3 { // from the same address: a strike each would block it
		isBusy(t, do(s, "POST", "short.example", "/"+third, withPassword(testPassword)))
	}
	eq(t, begun.Load(), int64(2))
	release()
	opensTo(t, <-answers[0], firstTarget)
	opensTo(t, <-answers[1], secondTarget)

	opensTo(t, do(s, "POST", "short.example", "/"+third, withPassword(testPassword)), thirdTarget)
	eq(t, s.locks.Len(), 0) // nothing counted
}

// A password check that panics gives its place back, for its link and for
// the whole server, and opens nothing; the panic goes on to the server.
func TestPanickingCheckGivesItsPlaceBack(t *testing.T) {
	s, _ := newTestServer(t)
	s.locks.maxChecks = 1
	code, target := passwordLink(t, s, nil)
	other, otherTarget := passwordLink(t, s, nil)
	check := s.passwordCheck
	s.passwordCheck = func(string, []byte, []byte) bool { panic("the check failed") }
	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("the panic did not reach the server")
			}
		}()
		do(s, "POST", "short.example", "/"+code, withPassword(testPassword))
	}()
	s.passwordCheck = check

	opensTo(t, do(s, "POST", "short.example", "/"+other, withPassword(testPassword)), otherTarget)
	opensTo(t, do(s, "POST", "short.example", "/"+code, withPassword(testPassword)), target)
}

// The sweep that forgets clients also forgets the locks that ran out.
func TestSweepForgetsLinkLocks(t *testing.T) {
	s, _ := newTestServer(t)
	code, _ := passwordLink(t, s, nil)
	counted, _ := passwordLink(t, s, nil)
	from := func(i int) func(*http.Request) { return withRemote(fmt.Sprintf("203.0.113.%d:1234", 10+i)) }
	for i := range 5 {
		wrongPassword(t, do(s, "POST", "short.example", "/"+code, withPassword("wrong one"), from(i)))
	}
	wrongPassword(t, do(s, "POST", "short.example", "/"+counted, withPassword("wrong one"), from(5)))
	eq(t, s.locks.Len(), 2)

	advance(s, 5*time.Minute)
	s.SweepLimiter()
	eq(t, s.locks.Len(), 1)
}

// The right password resets the count: wrong ones before it do not add up
// with those after.
func TestRightPasswordResetsTheCount(t *testing.T) {
	s, _ := newTestServer(t)
	code, target := passwordLink(t, s, nil)
	from := func(i int) func(*http.Request) { return withRemote(fmt.Sprintf("203.0.113.%d:1234", 10+i)) }

	for i := range 4 {
		wrongPassword(t, do(s, "POST", "short.example", "/"+code, withPassword("wrong one"), from(i)))
	}
	opensTo(t, do(s, "POST", "short.example", "/"+code, withPassword(testPassword), from(4)), target)
	// Without the reset this would be the fifth wrong password in a row.
	wrongPassword(t, do(s, "POST", "short.example", "/"+code, withPassword("wrong one"), from(5)))
	opensTo(t, do(s, "POST", "short.example", "/"+code, withPassword(testPassword), from(6)), target)
}

func TestWrongPasswordsStrikeTheClient(t *testing.T) {
	s, _ := newTestServer(t) // PerMinute 3
	code, _ := passwordLink(t, s, nil)
	other, _ := passwordLink(t, s, nil)

	for range 4 {
		wrongPassword(t, do(s, "POST", "short.example", "/"+code, withPassword("wrong one")))
	}
	status(t, do(s, "POST", "short.example", "/"+other, withPassword(testPassword)), http.StatusTooManyRequests)
	status(t, do(s, "GET", "short.example", "/"+other), http.StatusTooManyRequests)
	// Another address is not blocked, and four wrong passwords did not lock
	// the link.
	isPasswordPage(t, do(s, "GET", "short.example", "/"+code, withRemote("198.51.100.1:1234")))
	wrongPassword(t, do(s, "POST", "short.example", "/"+code, withRemote("198.51.100.1:1234")))
}

// While a link is locked an attempt costs no hash, and a body that holds no
// password the link can have, too large, malformed, or a password shorter
// than 4 or longer than 128 bytes, is wrong without one. The whole body is
// read: the right password with anything after it is wrong too.
func TestWrongPasswordsWithoutHashing(t *testing.T) {
	s, _ := newTestServer(t)
	checks := countChecks(s)
	code, _ := passwordLink(t, s, nil)
	from := func(i int) func(*http.Request) { return withRemote(fmt.Sprintf("203.0.113.%d:1234", 10+i)) }

	for i, body := range []string{
		"",
		"not json",
		`{"password": 5}`,
		`{"password": ""}`,
		`{"password": "abc"}`,
		`{"password": "` + strings.Repeat("a", maxPasswordLength+1) + `"}`,
		`{"password": "` + testPassword + `", "padding": "` + strings.Repeat("a", 2<<10) + `"}`,
		`{"password": "` + testPassword + `"}` + strings.Repeat("x", 5<<10),
		`{"password": "` + testPassword + `"} junk`,
		`{"password": "` + testPassword + `"}{"password": "` + testPassword + `"}`,
	} {
		opts := []func(*http.Request){from(i)}
		if body != "" {
			opts = append(opts, withBody(body))
		}
		// Each is a wrong password: counted, so fresh links keep the lock
		// out of the way.
		code, _ := passwordLink(t, s, nil)
		wrongPassword(t, do(s, "POST", "short.example", "/"+code, opts...))
	}
	eq(t, checks.Load(), int64(0))

	for i := range 5 {
		wrongPassword(t, do(s, "POST", "short.example", "/"+code, withPassword("wrong one"), from(10+i)))
	}
	eq(t, checks.Load(), int64(5))
	for i, password := range []string{testPassword, "wrong one", ""} {
		isLocked(t, do(s, "POST", "short.example", "/"+code, withPassword(password), from(20+i)))
	}
	eq(t, checks.Load(), int64(5))
}

// Nothing of a password link's target is given away before the right
// password: not by the page, not by a refused attempt, in no header, and
// nothing is cached that a later request could be redirected from.
func TestPasswordLinkGivesNothingAway(t *testing.T) {
	s, _ := newTestServer(t)
	code, target := passwordLink(t, s, nil)
	once := time.Duration(0)
	securedCode, _ := passwordLink(t, s, &once)
	from := func(i int) func(*http.Request) { return withRemote(fmt.Sprintf("203.0.113.%d:1234", 10+i)) }

	var answers []*httptest.ResponseRecorder
	for _, c := range []string{code, securedCode} {
		answers = append(answers,
			do(s, "GET", "short.example", "/"+c),
			do(s, "HEAD", "short.example", "/"+c),
			do(s, "GET", "www.short.example", "/"+c),
			do(s, "POST", "short.example", "/"+c, from(0)),
			do(s, "POST", "short.example", "/"+c, withBody("{"), from(1)),
			do(s, "POST", "short.example", "/"+c, withPassword("abc"), from(2)),
		)
		for i := range 4 {
			answers = append(answers, do(s, "POST", "short.example", "/"+c, withPassword("wrong one"), from(3+i)))
		}
		answers = append(answers, do(s, "POST", "short.example", "/"+c, withPassword(testPassword), from(7))) // locked
	}
	for i, w := range answers {
		if w.Code == http.StatusFound || w.Code == http.StatusOK && w.Header().Get("Content-Type") == "application/json" {
			t.Fatalf("answer %d opens the link: %d %s", i, w.Code, w.Body.String())
		}
		for _, leak := range []string{target, "example.com", "vault"} {
			if strings.Contains(w.Body.String(), leak) {
				t.Fatalf("answer %d holds %q in its body: %s", i, leak, w.Body.String())
			}
			for key, values := range w.Header() {
				for _, value := range values {
					if strings.Contains(value, leak) {
						t.Fatalf("answer %d holds %q in its %s header", i, leak, key)
					}
				}
			}
		}
	}
	eq(t, s.cache.Len(), 0)

	advance(s, 5*time.Minute+time.Second)
	opensTo(t, do(s, "POST", "short.example", "/"+code, withPassword(testPassword), from(8)), target)
	isPasswordPage(t, do(s, "GET", "short.example", "/"+code))
	eq(t, s.cache.Len(), 0)
}

// A password link that does not work answers as any dead link does: the
// 404 page, never the password page, and no password opens it.
func TestDeadPasswordLinkIsNotFound(t *testing.T) {
	s, _ := newTestServer(t)
	salt, hash := testHash()
	expired := storeLink(t, s, NewLink{URL: "https://example.com/vault/expired", ExpiresAt: s.now().Add(-time.Minute), Salt: salt, Hash: hash})
	deleted, _ := passwordLink(t, s, nil)
	status(t, api(s, "DELETE", "/api/links/"+deleted, ""), http.StatusNoContent)
	blocked := storeLink(t, s, NewLink{URL: "https://blocked.example/vault", ExpiresAt: s.now().Add(time.Hour), Salt: salt, Hash: hash})
	must(t, s.meta.Block("blocked.example"))

	check := func(t *testing.T, code string) {
		t.Helper()
		w := do(s, "GET", "short.example", "/"+code)
		status(t, w, http.StatusNotFound)
		if !strings.Contains(w.Body.String(), "Link not found") || strings.Contains(w.Body.String(), `type="password"`) {
			t.Fatalf("not the 404 page: %.300s", w.Body.String())
		}
		gone(t, do(s, "POST", "short.example", "/"+code, withPassword(testPassword)))
	}
	for name, code := range map[string]string{"expired": expired, "deleted": deleted, "blocked host": blocked} {
		t.Run(name, func(t *testing.T) { check(t, code) })
	}
	t.Run("disabled account", func(t *testing.T) {
		code, _ := passwordLink(t, s, nil)
		must(t, s.meta.SetDisabled(testAccount, true))
		defer s.meta.SetDisabled(testAccount, false)
		check(t, code)
	})
}

// Only a password link opens with a password, and only with its password:
// a regular link does not open by POST, whatever the body says, and a
// secured link without a password opens as before, whatever the body says.
func TestPasswordOpensOnlyPasswordLinks(t *testing.T) {
	s, _ := newTestServer(t)
	checks := countChecks(s)
	regular, regularTarget := live(t, s)
	gone(t, do(s, "POST", "short.example", "/"+regular, withPassword(testPassword)))
	redirectsTo(t, do(s, "GET", "short.example", "/"+regular), regularTarget)

	for _, body := range []string{"", "{", `{"password": "` + testPassword + `"}`, `{"password": "x"}`} {
		code, target := secured(t, s, 0)
		opts := []func(*http.Request){}
		if body != "" {
			opts = append(opts, withBody(body))
		}
		opensTo(t, do(s, "POST", "short.example", "/"+code, opts...), target)
	}
	eq(t, checks.Load(), int64(0))

	// A password link is not redirected to, from the store or the cache.
	code, _ := passwordLink(t, s, nil)
	for range 2 {
		w := do(s, "GET", "short.example", "/"+code)
		isPasswordPage(t, w)
		eq(t, w.Header().Get("Location"), "")
	}
}

func TestSecuredOpenLogged(t *testing.T) {
	s, _ := newTestServer(t)
	dir := logTo(t, s)
	code, _ := secured(t, s, 0)

	do(s, "POST", "short.example", "/"+code)

	lines := loggedLines(t, dir, "access")
	eq(t, len(lines), 1)
	eq(t, lines[0], "open\tshort.example\t"+code+"\t203.0.113.7\t") // no account
}

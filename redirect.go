// Public pages: following a short link, the root page and robots.txt.
package main

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"time"
)

// codePattern is what a code or static alias can look like. Anything else
// (favicon.ico, a single letter) is not a guess at a code.
var codePattern = regexp.MustCompile(`^[A-Za-z0-9_-]{3,64}$`)

// handleRedirect answers GET and HEAD /{code}.
//
// Lookup order: static alias, link cache, store. Only a live regular link
// from the store fills the link cache, and it stays there no longer than
// the link itself lives, nor past the next change of the meta file.
func (s *Server) handleRedirect(w http.ResponseWriter, r *http.Request) {
	d := s.meta.Domain(r.Host)
	if d == nil {
		s.notFound(w, r, nil, false)
		return
	}
	code := r.PathValue("code")
	if !codePattern.MatchString(code) {
		s.page(w, http.StatusNotFound, "404.html")
		return
	}
	if target, ok := d.StaticURL(code); ok {
		s.redirect(w, r, target)
		return
	}

	key := cacheKey(d.Host, code)
	now := s.now()
	if target, ok := s.cache.Get(key, now); ok {
		s.opened(r, d, code)
		s.redirect(w, r, target)
		return
	}

	changes := s.changes.Load() // before the link and the meta file are read
	link, state, err := s.store.Lookup(d.ID, code)
	switch {
	case err != nil:
		// Not the client's fault: no strike, and nothing is cached.
		slog.Error("redirect lookup failed", "domain", d.Host, "code", code, "error", err)
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
	case state == LinkUnknown:
		s.notFound(w, r, d, true) // a code never handed out is a guess
	case state == LinkGone || !s.live(link, now):
		s.notFound(w, r, d, false)
	// A live secured or password link is not redirected: its page asks for
	// the target with a POST (see handleOpen). Nothing is cached: a secured
	// link can die at any moment, and a password link opens only with its
	// password. A link that is both asks for the password.
	case link.Password:
		s.page(w, http.StatusOK, "password.html")
	case link.Secured:
		s.page(w, http.StatusOK, "open.html")
	default:
		s.cacheLink(key, changes, func() {
			s.cache.Set(key, link.URL, earliest(now.Add(s.cfg.Config().Cache.TTL.D()), link.ExpiresAt))
		})
		s.opened(r, d, code)
		s.redirect(w, r, link.URL)
	}
}

// handleOpen answers POST /{code}: it opens a secured or password link and
// returns its target as JSON. Only the script of a built-in page sends this
// request. Link-preview bots fetch pages with GET, so they never start a
// secured link's countdown and never learn where a link leads.
func (s *Server) handleOpen(w http.ResponseWriter, r *http.Request) {
	const gone = "link not found or expired"
	d := s.meta.Domain(r.Host)
	code := r.PathValue("code")
	if d == nil || !codePattern.MatchString(code) {
		writeError(w, http.StatusNotFound, gone)
		return
	}
	failed := func(err error) {
		slog.Error("opening a link failed", "domain", d.Host, "code", code, "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
	}
	now := s.now()
	// The link is looked at first: opening it would use up the link of a
	// disabled account, or one to a blocked host.
	link, state, err := s.store.Lookup(d.ID, code)
	if err != nil {
		failed(err)
		return
	}
	if state == LinkUnknown {
		s.strike(r) // a code never handed out is a guess; a regular or dead link is not
	}
	if state != LinkFound || !(link.Secured || link.Password) || !s.live(link, now) {
		writeError(w, http.StatusNotFound, gone)
		return
	}
	// Only the right password goes on, so only it uses up a secured link.
	if link.Password && !s.unlocks(w, r, cacheKey(d.Host, code), link) {
		return
	}
	target := link.URL
	if link.Secured {
		var ok bool
		target, ok, err = s.store.Open(d.ID, code, now)
		switch {
		case err != nil:
			failed(err)
			return
		case !ok: // used up or deleted since the lookup
			writeError(w, http.StatusNotFound, gone)
			return
		}
	}
	s.logs.Access.Line("open", d.Host, code, s.clientIP(r), "")
	writeJSON(w, http.StatusOK, map[string]string{"url": target})
}

// maxPasswordBody is the most of a POST body read for a password:
// {"password": "..."} with the longest password fits, even with every
// character escaped.
const maxPasswordBody = 1 << 10

// unlocks reports whether the body of r holds the password of link, whose
// lock key is key. Otherwise it has answered with 429 or 403:
//
//   - 429 while the link is locked, before the body is read;
//   - 429 with Retry-After when as many of the link's passwords are being
//     checked as could still lock it, or as many passwords of all links as
//     the server checks at once: a moment later there is room. Such an
//     attempt is neither hashed, counted nor struck;
//   - 403 for a wrong password, counted against the link and struck against
//     the client. A body that holds no password the link can have is wrong
//     without hashing.
func (s *Server) unlocks(w http.ResponseWriter, r *http.Request, key string, link Link) bool {
	locked := func() bool {
		writeError(w, http.StatusTooManyRequests, "too many wrong passwords; try again in a few minutes")
		return false
	}
	if s.locks.Locked(key, s.now()) {
		return locked()
	}
	password, ok := readPassword(w, r)
	switch s.locks.Start(key, s.now()) {
	case checkLocked:
		return locked()
	case checkBusy:
		w.Header().Set("Retry-After", "1")
		writeError(w, http.StatusTooManyRequests, "too many attempts at once; try again")
		return false
	}
	right := s.checkInPlace(key, password, ok, link)
	if !right {
		s.strike(r)
		writeError(w, http.StatusForbidden, "wrong password")
	}
	return right
}

// checkInPlace checks password, which ok says the body held, against link
// in the place Start gave it, and gives the place back with Done however
// the check ends. A check that panics counts as a wrong password, and the
// panic goes on to the server.
func (s *Server) checkInPlace(key, password string, ok bool, link Link) (right bool) {
	defer func() { s.locks.Done(key, right, s.now()) }()
	return ok && s.passwordCheck(password, link.Salt, link.Hash)
}

// readPassword returns the password a POST body holds. The body is read
// whole, up to maxPasswordBody, and must be one JSON object and nothing
// else: {"password": "..."}. ok is false for a body that is missing, too
// large or anything else, and for a password no link can have.
func readPassword(w http.ResponseWriter, r *http.Request) (password string, ok bool) {
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxPasswordBody))
	if err != nil {
		return "", false
	}
	var body struct {
		Password string `json:"password"`
	}
	// Unlike a Decoder, Unmarshal refuses anything after the first value.
	if err := json.Unmarshal(data, &body); err != nil || !possiblePassword(body.Password) {
		return "", false
	}
	return body.Password, true
}

// live reports whether a link found in the store may be followed at now:
// it has not expired, its account exists and is enabled, and its host is
// not blocked.
func (s *Server) live(l Link, now time.Time) bool {
	owner := s.meta.Owner(l.Owner)
	return l.ExpiresAt.After(now) && owner != nil && !owner.Disabled && !s.meta.Blocked(targetHost(l.URL))
}

// opened logs one successful redirect when redirect logging is on. HEAD
// requests only look; they are not logged.
func (s *Server) opened(r *http.Request, d *Domain, code string) {
	if r.Method == http.MethodGet && s.cfg.Config().Log.Redirects {
		s.logs.Access.Line("redirect", d.Host, code, s.clientIP(r), "")
	}
}

// handleRoot answers GET /: the domain's fallback URL if it has one,
// otherwise the built-in index page.
func (s *Server) handleRoot(w http.ResponseWriter, r *http.Request) {
	d := s.meta.Domain(r.Host)
	switch {
	case d == nil:
		s.notFound(w, r, nil, false)
	case d.FallbackURL != "":
		s.redirect(w, r, d.FallbackURL)
	default:
		s.page(w, http.StatusOK, "index.html")
	}
}

// earliest returns the earlier of two times.
func earliest(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}

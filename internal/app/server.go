// HTTP server: the Server type, its routes and the helpers every handler
// shares (client address, JSON responses, the "not found" behaviour).
package app

import (
	"embed"
	"encoding/json"
	"hash/maphash"
	"io"
	"net"
	"net/http"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// webFS holds the pages and documents built into the binary.
//
//go:embed web
var webFS embed.FS

// Server holds everything a request needs. Its methods are the handlers.
type Server struct {
	cfg     *Watcher
	meta    *Meta
	store   *Store
	cache   *Cache        // live regular links: key -> target URL; emptied by every meta change
	changes atomic.Uint64 // meta changes and link edits so far; see cacheLink
	seed    maphash.Seed  // of the keys that find a repeated create request
	limiter *Limiter
	locks   *LinkLocks // wrong passwords and checks per link, by cacheKey, and checks in all
	logs    *Logs      // never nil; its logs are nil when logging is off

	now func() time.Time // the clock; tests replace it
	// passwordCheck is checkPassword; tests replace it to count the checks.
	passwordCheck func(password string, salt, hash []byte) bool
	// beforeCaching, a test hook, runs when a request has decided to cache
	// a link and has not done so yet.
	beforeCaching func()

	// adminMu lets one admin command run at a time, so that what a command
	// reads before its change still holds when it makes it.
	adminMu sync.Mutex
	// socketBound, a test hook, runs when the admin socket is bound under
	// its temporary name and has not been given its own yet.
	socketBound func(path string)

	handler http.Handler
}

// NewServer wires a server. logs may be nil: then nothing is logged, as
// with the logs OpenLogs returns when logging is off.
//
// Every change of the meta file empties the link cache, so that a link of
// an account that was disabled or deleted, or to a host that was blocked,
// stops at once: the store's answer is checked again at the next request.
//
// Password checks run on one core fewer than the program may use, or on
// the one it has: with two or more, guesses never take every core from the
// redirects.
func NewServer(cfg *Watcher, meta *Meta, store *Store, logs *Logs) *Server {
	if logs == nil {
		logs = &Logs{}
	}
	s := &Server{
		cfg:     cfg,
		meta:    meta,
		store:   store,
		cache:   NewCache(cfg.Config().Cache.MaxEntries),
		seed:    maphash.MakeSeed(),
		limiter: NewLimiter(),
		locks:   NewLinkLocks(maxCountedLinks, max(1, runtime.GOMAXPROCS(0)-1)),
		logs:    logs,
		now:     time.Now,

		passwordCheck: checkPassword,
	}
	meta.OnChange(func() {
		s.changes.Add(1) // first, so that a fill that comes after the clearing sees it
		s.cache.Clear()
	})
	s.handler = s.routes()
	return s
}

// Handler returns the HTTP handler for the whole service.
func (s *Server) Handler() http.Handler {
	return s.handler
}

// routes builds the request router. A blocked client gets 429 for
// everything except the health check: the operator's page for it in a
// browser, JSON otherwise. Every response, whatever answers it, leaves with
// the signature header.
func (s *Server) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.handleHealth)
	mux.HandleFunc("GET /robots.txt", document("robots.txt", "text/plain; charset=utf-8"))
	mux.HandleFunc("GET /{$}", s.handleRoot)
	mux.HandleFunc("GET /{code}", s.handleRedirect)
	mux.HandleFunc("POST /{code}", s.handleOpen)

	mux.HandleFunc("POST /api/links", s.auth(s.handleCreate))
	mux.HandleFunc("PATCH /api/links/{code}", s.auth(s.handlePatch))
	mux.HandleFunc("DELETE /api/links/{code}", s.auth(s.handleDelete))
	// The API's own documentation needs no token.
	mux.HandleFunc("GET /api/llms.txt", document("llms.txt", "text/plain; charset=utf-8"))
	mux.HandleFunc("GET /api/openapi.yaml", document("openapi.yaml", "application/yaml"))
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotFound, "unknown API endpoint")
	})

	mux.HandleFunc("/", s.handleOther)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header()[signatureHeader] = signatureValue
		if r.URL.Path != "/healthz" && s.limiter.Blocked(s.clientIP(r), s.now()) {
			if !s.ownErrorPage(w, r, http.StatusTooManyRequests, "429") {
				writeError(w, http.StatusTooManyRequests, "too many requests")
			}
			return
		}
		mux.ServeHTTP(w, r)
	})
}

// handleOther answers every path that no route takes: a 404 page, or, with
// trim_codes on, a code followed by "/" and maybe more, such as "/qwert/",
// which is the link of that code. The API's paths never come here.
func (s *Server) handleOther(w http.ResponseWriter, r *http.Request) {
	written := strings.TrimPrefix(r.URL.Path, "/")
	if s.cfg.Config().TrimCodes && strings.Contains(written, "/") {
		r.SetPathValue("code", written)
		switch r.Method {
		case http.MethodGet, http.MethodHead:
			s.handleRedirect(w, r)
			return
		case http.MethodPost:
			s.handleOpen(w, r)
			return
		}
	}
	s.page(w, http.StatusNotFound, "404.html")
}

// SweepLimiter forgets clients the limiter no longer needs to remember, and
// links whose lock ran out.
func (s *Server) SweepLimiter() {
	now := s.now()
	s.limiter.Sweep(now)
	s.locks.Sweep(now)
}

// ForgetRemovedFiles lets go of the chunk files removed from the data
// folder. When there were any, the link cache is emptied too, counted as
// cacheLink expects, so that their links stop at once, cached or not.
func (s *Server) ForgetRemovedFiles() {
	if s.store.ForgetRemovedFiles() > 0 {
		s.changes.Add(1)
		s.cache.Clear()
	}
}

// handleHealth answers GET /healthz for process supervisors: 200 whenever
// the process can answer at all.
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, struct {
		Status string `json:"status"`
	}{"ok"})
}

// clientIP returns the address the limiter and the log attribute a request
// to. Behind a reverse proxy the configured header is used. A client can
// send that header too, so only what the proxy itself added is trusted: the
// last entry of the last header line. Without that setting the connection
// address is used, so a client cannot pick its own address.
func (s *Server) clientIP(r *http.Request) string {
	if header := s.cfg.Config().NotFoundLimit.ClientIPHeader; header != "" {
		if lines := r.Header.Values(header); len(lines) > 0 {
			last := lines[len(lines)-1]
			if ip := strings.TrimSpace(last[strings.LastIndexByte(last, ',')+1:]); ip != "" {
				return ip
			}
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// strike records one bad request against the client's address.
func (s *Server) strike(r *http.Request) {
	limit := s.cfg.Config().NotFoundLimit
	s.limiter.Strike(s.clientIP(r), s.now(), limit.PerMinute, limit.BlockFor.D())
}

// notFound is the single answer for "this link does not work here": an
// unknown host (d is nil), an unknown code or a dead link. It redirects to
// the domain's fallback URL, else to the global one, else shows the 404
// page. strike says whether the request counts against the client.
func (s *Server) notFound(w http.ResponseWriter, r *http.Request, d *Domain, strike bool) {
	if strike {
		s.strike(r)
	}
	fallback := s.cfg.Config().FallbackURL
	if d != nil && d.FallbackURL != "" {
		fallback = d.FallbackURL
	}
	if fallback != "" {
		s.redirect(w, r, fallback)
		return
	}
	s.page(w, http.StatusNotFound, "404.html")
}

// redirect sends a 302 that neither browsers nor search engines keep.
func (s *Server) redirect(w http.ResponseWriter, r *http.Request, target string) {
	private(w)
	http.Redirect(w, r, target, http.StatusFound)
}

// page serves one HTML page with the given status: the operator's own page
// in the place of the built-in one of that name when there is one.
func (s *Server) page(w http.ResponseWriter, status int, name string) {
	data := s.cfg.Pages().page(strings.TrimSuffix(name, ".html"))
	if data == nil {
		var err error
		if data, err = webFS.ReadFile("web/" + name); err != nil {
			http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
			return
		}
	}
	writePage(w, status, data)
}

// ownErrorPage answers a GET or HEAD outside the API with the operator's
// page of that name, and reports whether it did. Every other request, and
// one for which there is no such page, keeps the answer its caller gives:
// programs read those, and so do the pages of secured and password links.
func (s *Server) ownErrorPage(w http.ResponseWriter, r *http.Request, status int, name string) bool {
	if r.Method != http.MethodGet && r.Method != http.MethodHead || strings.HasPrefix(r.URL.Path, "/api/") {
		return false
	}
	data := s.cfg.Pages().page(name)
	if data == nil {
		return false
	}
	writePage(w, status, data)
	return true
}

// writePage sends an HTML page that is neither cached nor indexed, and the
// signature comment after it.
func writePage(w http.ResponseWriter, status int, data []byte) {
	private(w)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	w.Write(data)
	io.WriteString(w, signatureComment)
}

// document returns a handler that serves one built-in text file as it is:
// robots.txt and the API documentation.
func document(name, contentType string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		data, err := webFS.ReadFile("web/" + name)
		if err != nil {
			http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", contentType)
		w.Write(data)
	}
}

// private marks a response as not to be cached and not to be indexed.
func private(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Robots-Tag", "noindex, nofollow")
}

// writeJSON writes v as a JSON response.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false) // an API answer is not HTML: "&" in a URL stays "&"
	enc.Encode(v)
}

// writeError writes the API's error shape: {"error": "..."}.
func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// cacheLink puts a link a request decided to cache into the link cache;
// put stores it under key. changes is what s.changes was before the request
// read the link and the meta file for its decision. If the meta file
// changed since, or a link was edited or deleted, the decision may rest on
// what is no longer so, and the entry is taken out again: a change empties
// the cache only after it is counted, so either it removes the entry or
// this check sees it.
func (s *Server) cacheLink(key string, changes uint64, put func()) {
	if s.beforeCaching != nil {
		s.beforeCaching()
	}
	put()
	if s.changes.Load() != changes {
		s.cache.Delete(key)
	}
}

// forget takes a link that was edited or deleted out of the link cache,
// counted as cacheLink expects.
func (s *Server) forget(key string) {
	s.changes.Add(1)
	s.cache.Delete(key)
}

// cacheKey is the key of a link in the link cache.
func cacheKey(domain, code string) string {
	return domain + "/" + code
}

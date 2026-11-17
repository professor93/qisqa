// The API: with its token an account creates links, and edits and deletes
// its own.
package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"
)

// maxBody is the largest request body the API reads.
const maxBody = 64 << 10

// createRequest is the body of POST /api/links.
type createRequest struct {
	URL              string   `json:"url"`
	Text             string   `json:"text"`
	URLs             []string `json:"urls"`
	Domain           string   `json:"domain"`
	ExpiresIn        string   `json:"expires_in"`
	ExpiresAfterOpen *string  `json:"expires_after_open"`
	Password         *string  `json:"password"`
	Format           string   `json:"format"`
}

// patchRequest is the body of PATCH /api/links/{code}. A nil field is left
// unchanged.
type patchRequest struct {
	URL       *string `json:"url"`
	ExpiresIn *string `json:"expires_in"`
}

// linkResponse is a link as the API returns it.
type linkResponse struct {
	Code      string    `json:"code"`
	Domain    string    `json:"domain"`
	ShortURL  string    `json:"short_url"`
	URL       string    `json:"url"`
	ExpiresAt time.Time `json:"expires_at"`
	Secured   bool      `json:"secured"`
	Password  bool      `json:"password"`
	Created   bool      `json:"created"`
}

// toResponse converts a stored link on domain d into its API form.
func toResponse(l Link, d *Domain, format string, created bool) linkResponse {
	return linkResponse{
		Code:      l.Code,
		Domain:    d.Host,
		ShortURL:  ShortURL(format, d.Host, l.Code),
		URL:       l.URL,
		ExpiresAt: l.ExpiresAt.UTC(),
		Secured:   l.Secured,
		Password:  l.Password,
		Created:   created,
	}
}

// textResponse is the answer to a text-mode request: the text with its URLs
// replaced, and the links that replaced them.
type textResponse struct {
	Text  string         `json:"text"`
	Links []linkResponse `json:"links"`
}

// createOptions are the settings of a create request after defaults and
// validation.
type createOptions struct {
	domain    *Domain
	format    string
	ttl       time.Duration
	afterOpen *time.Duration // nil: not secured
	password  *string        // nil: no password
}

// auth wraps a handler so that it runs only for requests carrying the token
// of an enabled account, which the handler is given. A failed attempt
// counts as a strike, which blocks an address that keeps guessing.
func (s *Server) auth(next func(http.ResponseWriter, *http.Request, *Account)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		a := s.meta.Account(bearerToken(r))
		if a == nil {
			s.strike(r)
			w.Header().Set("WWW-Authenticate", "Bearer")
			writeError(w, http.StatusUnauthorized, "missing or invalid API token")
			return
		}
		next(w, r, a)
	}
}

// bearerToken returns the token of an "Authorization: Bearer <token>"
// header, or "" if the header is missing or of another kind.
func bearerToken(r *http.Request) string {
	token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok {
		return ""
	}
	return token
}

// decodeBody reads one JSON object from the request body into v. The error
// is written for the API caller. Unknown fields are rejected so that a typo
// in a field name is reported instead of silently ignored.
func decodeBody(w http.ResponseWriter, r *http.Request, v any) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxBody)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		var tooLarge *http.MaxBytesError
		var wrongType *json.UnmarshalTypeError
		switch {
		case errors.As(err, &tooLarge):
			return errors.New("request body is larger than 64 KB")
		case errors.As(err, &wrongType):
			return typeError(wrongType)
		case errors.Is(err, io.EOF):
			return errors.New("request body is empty; send a JSON object")
		default:
			return fmt.Errorf("invalid JSON: %s", strings.TrimPrefix(err.Error(), "json: "))
		}
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("request body must be a single JSON object")
	}
	return nil
}

// typeError says which field of a request body has the wrong JSON type.
// Every field is a string, except "urls".
func typeError(err *json.UnmarshalTypeError) error {
	field, _, _ := strings.Cut(err.Field, ".") // "urls.0" is an entry of "urls"
	switch field {
	case "":
		return errors.New("request body must be a JSON object")
	case "urls":
		return errors.New(`"urls" must be a list of strings`)
	}
	return fmt.Errorf("%q must be a string", field)
}

// positiveDuration parses a duration that must be greater than zero. key
// names the field in the error.
func positiveDuration(key, value string) (time.Duration, error) {
	d, err := ParseDuration(value)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", key, err)
	}
	if d == 0 {
		return 0, fmt.Errorf("%s: must be greater than zero", key)
	}
	return d, nil
}

// accountDomain resolves the domain a request names; an empty name means
// the account's first domain. A domain the account may not use is refused
// the same way whether it exists or not.
func (s *Server) accountDomain(a *Account, name string) (*Domain, error) {
	if name == "" {
		if len(a.Domains) > 0 {
			if d := s.meta.DomainByID(a.Domains[0]); d != nil {
				return d, nil
			}
		}
		return nil, errors.New("domain: this account has no domain")
	}
	if d := s.meta.Domain(name); d != nil && slices.Contains(a.Domains, d.ID) {
		return d, nil
	}
	return nil, fmt.Errorf("domain: %q is not available to this account", name)
}

// parseOptions validates the optional fields of a create request for links
// on domain d and fills in the config defaults.
func parseOptions(cfg *Config, req createRequest, d *Domain) (createOptions, error) {
	opts := createOptions{domain: d, format: cfg.ShortURLFormat, ttl: cfg.DefaultTTL.D()}
	var err error
	if req.Format != "" {
		if !slices.Contains(shortURLFormats, req.Format) {
			return opts, fmt.Errorf("format: %q must be one of %s", req.Format, strings.Join(shortURLFormats, ", "))
		}
		opts.format = req.Format
	}
	if req.ExpiresIn != "" {
		if opts.ttl, err = positiveDuration("expires_in", req.ExpiresIn); err != nil {
			return opts, err
		}
	}
	if req.ExpiresAfterOpen != nil {
		afterOpen, err := ParseDuration(*req.ExpiresAfterOpen)
		if err != nil {
			return opts, fmt.Errorf("expires_after_open: %w", err)
		}
		opts.afterOpen = &afterOpen
	}
	if req.Password != nil {
		if !possiblePassword(*req.Password) { // the error never repeats the password
			return opts, fmt.Errorf("password: must be %d to %d bytes", minPasswordLength, maxPasswordLength)
		}
		opts.password = req.Password
	}
	return opts, nil
}

// handleCreate answers POST /api/links.
func (s *Server) handleCreate(w http.ResponseWriter, r *http.Request, a *Account) {
	var req createRequest
	if err := decodeBody(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	req.URL = strings.TrimSpace(req.URL)
	switch {
	case req.URL == "" && req.Text == "":
		writeError(w, http.StatusBadRequest, `either "url" or "text" is required`)
		return
	case req.URL != "" && req.Text != "":
		writeError(w, http.StatusBadRequest, `send either "url" or "text", not both`)
		return
	}
	d, err := s.accountDomain(a, req.Domain)
	if err != nil {
		writeError(w, http.StatusForbidden, err.Error())
		return
	}
	cfg := s.cfg.Config()
	opts, err := parseOptions(cfg, req, d)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.Text != "" {
		s.createFromText(w, r, a, cfg, req, opts)
		return
	}

	if req.URLs != nil {
		writeError(w, http.StatusBadRequest, `"urls" is only valid together with "text"`)
		return
	}
	target, err := CheckTarget(req.URL, d, s.meta)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	links, err := s.createLinks(a, opts, []string{target})
	if err != nil {
		s.internalError(w, "create link", err)
		return
	}
	link := links[0]
	s.logEvent(r, a, "create", link.Domain, link.Code)
	status := http.StatusOK
	if link.Created {
		status = http.StatusCreated
	}
	writeJSON(w, status, link)
}

// createFromText answers POST /api/links in text mode: every URL in the
// text that is worth shortening is replaced by a short link.
func (s *Server) createFromText(w http.ResponseWriter, r *http.Request, a *Account, cfg *Config, req createRequest, opts createOptions) {
	written, targets, err := s.textTargets(cfg, req, opts)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	links, err := s.createLinks(a, opts, targets)
	if err != nil {
		s.internalError(w, "shorten text", err)
		return
	}
	short := make(map[string]string, len(links))
	for i, link := range links {
		short[written[i]] = link.ShortURL
		s.logEvent(r, a, "create", link.Domain, link.Code)
	}
	writeJSON(w, http.StatusOK, textResponse{Text: ReplaceURLs(req.Text, short), Links: links})
}

// handlePatch answers PATCH /api/links/{code}?domain=...
func (s *Server) handlePatch(w http.ResponseWriter, r *http.Request, a *Account) {
	d, err := s.accountDomain(a, r.URL.Query().Get("domain"))
	if err != nil {
		writeError(w, http.StatusForbidden, err.Error())
		return
	}
	code := r.PathValue("code")
	if !codePattern.MatchString(code) {
		writeError(w, http.StatusNotFound, "link not found")
		return
	}
	var req patchRequest
	if err := decodeBody(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.URL == nil && req.ExpiresIn == nil {
		writeError(w, http.StatusBadRequest, `at least one of "url" and "expires_in" is required`)
		return
	}
	if req.URL != nil {
		target, err := CheckTarget(*req.URL, d, s.meta)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		req.URL = &target
	}
	var expiresAt *time.Time
	if req.ExpiresIn != nil {
		ttl, err := positiveDuration("expires_in", *req.ExpiresIn)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		at := s.now().Add(ttl)
		expiresAt = &at
	}

	link, err := s.store.Update(d.ID, code, a.ID, req.URL, expiresAt)
	switch {
	case errors.Is(err, ErrNotFound):
		writeError(w, http.StatusNotFound, "link not found")
		return
	case errors.Is(err, ErrSecured):
		writeError(w, http.StatusConflict, "a secured or password link cannot be edited; delete it and create a new one")
		return
	case err != nil:
		s.internalError(w, "update link", err)
		return
	}
	// The next redirect reads the link again, and checks it.
	s.forget(cacheKey(d.Host, code))
	s.logEvent(r, a, "update", d.Host, code)
	writeJSON(w, http.StatusOK, toResponse(link, d, s.cfg.Config().ShortURLFormat, false))
}

// handleDelete answers DELETE /api/links/{code}?domain=...
func (s *Server) handleDelete(w http.ResponseWriter, r *http.Request, a *Account) {
	d, err := s.accountDomain(a, r.URL.Query().Get("domain"))
	if err != nil {
		writeError(w, http.StatusForbidden, err.Error())
		return
	}
	code := r.PathValue("code")
	if !codePattern.MatchString(code) {
		writeError(w, http.StatusNotFound, "link not found")
		return
	}
	err = s.store.Delete(d.ID, code, a.ID)
	switch {
	case errors.Is(err, ErrNotFound):
		writeError(w, http.StatusNotFound, "link not found")
		return
	case err != nil:
		s.internalError(w, "delete link", err)
		return
	}
	s.forget(cacheKey(d.Host, code))
	s.logEvent(r, a, "delete", d.Host, code)
	w.WriteHeader(http.StatusNoContent)
}

// logEvent writes an API event to the access log, identifying the caller by
// its account's number; its token is never logged.
func (s *Server) logEvent(r *http.Request, a *Account, event, domain, code string) {
	s.logs.Access.Line(event, domain, code, s.clientIP(r), strconv.FormatUint(uint64(a.ID), 10))
}

// internalError logs the real error and tells the caller only that
// something went wrong on our side.
func (s *Server) internalError(w http.ResponseWriter, what string, err error) {
	slog.Error(what+" failed", "error", err)
	writeError(w, http.StatusInternalServerError, "internal error")
}

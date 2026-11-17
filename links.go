// Link creation: storing new links, answering a repeated create request
// with the link it made, choosing the URLs inside a text to shorten, and
// building the short URL in the requested shape.
package main

import (
	"errors"
	"fmt"
	"hash/maphash"
	"log/slog"
	"strings"
	"time"
)

const (
	// maxTextLength is the longest text accepted in text mode, in bytes.
	maxTextLength = 10 << 10
	// maxTextURLs is how many links one text-mode request may create.
	maxTextURLs = 20
	// repeatWindow is how long a create request sent again gets the link
	// it made: a retry is answered with the same link, not a second one.
	repeatWindow = 10 * time.Minute
)

// createLinks returns a link of account a on opts.domain for every target,
// in order. A regular link that the same request made within repeatWindow
// comes back as it is, with Created false. Every other link is new, and the
// new links are stored with one call of the store.
//
// The links of a request with a password share one salt and one hash: they
// share the password anyway, and one hash takes about 10 ms of a core.
func (s *Server) createLinks(a *Account, opts createOptions, targets []string) ([]linkResponse, error) {
	d := opts.domain
	now := s.now()
	responses := make([]linkResponse, len(targets)) // [] in JSON, never null
	requests := make([]uint64, len(targets))
	var salt, hash []byte
	if opts.password != nil && len(targets) > 0 {
		salt = newSalt()
		hash = hashPassword(*opts.password, salt)
	}
	regular := opts.afterOpen == nil && opts.password == nil
	var fresh []NewLink
	var freshAt []int // where each new link goes in responses
	for i, target := range targets {
		if regular {
			requests[i] = s.requestKey(a.ID, d.ID, opts.ttl, target)
			link, found, err := s.repeated(d, a.ID, target, requests[i], now)
			if err != nil {
				return nil, err
			}
			if found {
				responses[i] = toResponse(link, d, opts.format, false)
				continue
			}
		}
		fresh = append(fresh, NewLink{URL: target, ExpiresAt: now.Add(opts.ttl), AfterOpen: opts.afterOpen, Salt: salt, Hash: hash})
		freshAt = append(freshAt, i)
	}
	if len(fresh) == 0 {
		return responses, nil
	}
	created, err := s.create(a.ID, d, fresh)
	if err != nil {
		return nil, err
	}
	cacheTTL := s.cfg.Config().Cache.TTL.D()
	changes := s.changes.Load() // before live reads the meta file
	for j, link := range created {
		i := freshAt[j]
		// The account or the host may have changed since the request began.
		if regular && s.live(link, now) {
			key := cacheKey(d.Host, link.Code)
			s.cacheLink(key, changes, func() {
				s.cache.SetCreated(key, link.URL, earliest(now.Add(cacheTTL), link.ExpiresAt), requests[i], now.Add(repeatWindow))
			})
		}
		responses[i] = toResponse(link, d, opts.format, true)
	}
	return responses, nil
}

// requestKey identifies a create request by what decides its link: the
// account, the domain, the lifetime asked for and the URL.
func (s *Server) requestKey(account uint32, domain uint16, ttl time.Duration, url string) uint64 {
	return maphash.Comparable(s.seed, struct {
		account uint32
		domain  uint16
		ttl     time.Duration
		url     string
	}{account, domain, ttl, url})
}

// repeated returns the link that the create request with this key made on
// d within repeatWindow, if it still leads to url, is the account's and has
// not expired: a link changed, deleted or expired since is not handed out
// again.
func (s *Server) repeated(d *Domain, account uint32, url string, request uint64, now time.Time) (Link, bool, error) {
	key, ok := s.cache.CreatedKey(request, now)
	if !ok {
		return Link{}, false, nil
	}
	code, ok := strings.CutPrefix(key, cacheKey(d.Host, ""))
	if !ok {
		return Link{}, false, nil
	}
	link, state, err := s.store.Lookup(d.ID, code)
	if err != nil {
		return Link{}, false, err
	}
	return link, state == LinkFound && link.URL == url && link.Owner == account && link.ExpiresAt.After(now), nil
}

// create stores links of owner on domain d with one call of the store, so
// that one sync covers them all, and returns them in order with their codes.
func (s *Server) create(owner uint32, d *Domain, links []NewLink) ([]Link, error) {
	cfg := s.cfg.Config()
	shortest := codeLength(cfg)
	created, err := s.store.Create(d.ID, owner, shortest, func(code string) bool {
		return Acceptable(code, d, cfg.Blacklist)
	}, links)
	if err != nil {
		return nil, err
	}
	if len(created) > 0 {
		if used := len(created[len(created)-1].Code); used > shortest {
			slog.Warn("codes of the configured length are running out; consider raising min_length",
				"domain", d.Host, "min_length", shortest, "used_length", used)
		}
	}
	return created, nil
}

// codeLength is the length new codes start at.
func codeLength(cfg *Config) int {
	return max(minCodeLength, cfg.MinLength)
}

// textTargets lists the URLs a text-mode request turns into links, in the
// order the links are returned: each as written, which is what the text
// holds, and its target, which CheckTarget returned for it. The error is
// written for the API caller.
//
// Without "urls" the targets are the URLs found in the text, in order of
// first appearance. A URL that may not be shortened, or whose short form
// would not be shorter than the URL itself, is not a target: it simply
// stays in the text.
//
// With "urls" the targets are exactly that list, repeats removed, whether
// or not the text contains them; an entry that may not be shortened is an
// error.
func (s *Server) textTargets(cfg *Config, req createRequest, opts createOptions) (written, targets []string, err error) {
	if len(req.Text) > maxTextLength {
		return nil, nil, errors.New("text is larger than 10 KB")
	}
	if req.URLs != nil {
		seen := map[string]bool{}
		for _, u := range req.URLs {
			if seen[u] {
				continue
			}
			seen[u] = true
			target, err := CheckTarget(u, opts.domain, s.meta)
			if err != nil {
				return nil, nil, fmt.Errorf("urls: %q: %w", u, err)
			}
			written, targets = append(written, u), append(targets, target)
		}
	} else {
		shortLength := len(ShortURL(opts.format, opts.domain.Host, "")) + codeLength(cfg)
		for _, u := range FindURLs(req.Text) {
			if len(u) <= shortLength {
				continue
			}
			if target, err := CheckTarget(u, opts.domain, s.meta); err == nil {
				written, targets = append(written, u), append(targets, target)
			}
		}
	}
	if len(targets) > maxTextURLs {
		return nil, nil, fmt.Errorf("%d URLs to shorten; one request may shorten at most %d", len(targets), maxTextURLs)
	}
	return written, targets, nil
}

// ShortURL builds the short URL for a code in one of the four formats:
//
//	https      https://short.example/aB3xK
//	https_www  https://www.short.example/aB3xK
//	bare       short.example/aB3xK
//	www        www.short.example/aB3xK
func ShortURL(format, host, code string) string {
	switch format {
	case "https_www":
		return "https://www." + host + "/" + code
	case "bare":
		return host + "/" + code
	case "www":
		return "www." + host + "/" + code
	default:
		return "https://" + host + "/" + code
	}
}

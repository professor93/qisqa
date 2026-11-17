// Accounts, domains with their static aliases, blocked hosts and the hosts
// of removed domains: one JSON file in the data folder. Requests read an
// immutable snapshot without a lock; a change writes a new file and only
// then publishes a new snapshot.
package main

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
)

// Account may create and manage links on its domains with its token.
type Account struct {
	ID       uint32   `json:"id"`
	Name     string   `json:"name"`
	Domains  []uint16 `json:"domains"`
	Disabled bool     `json:"disabled,omitempty"`
}

// Domain is one short domain with its own links, aliases and fallback.
// Static is keyed by the lower-cased alias.
type Domain struct {
	ID             uint16            `json:"id"`
	Host           string            `json:"host"`
	FallbackURL    string            `json:"fallback_url,omitempty"`
	AllowedTargets []string          `json:"allowed_targets"`
	Static         map[string]string `json:"static"`
}

// StaticURL returns the target of a static alias, ignoring letter case.
func (d *Domain) StaticURL(alias string) (string, bool) {
	target, ok := d.Static[strings.ToLower(alias)]
	return target, ok
}

// A link's slot keeps its domain in 16 bits and its owner in 24, so these
// are the largest ids.
const (
	maxDomainID  = math.MaxUint16
	maxAccountID = 1<<24 - 1
)

// maxNameLength is the longest account name, in bytes.
const maxNameLength = 64

// tokenHash is the SHA-256 of a token. The file keeps it as hex; the token
// itself is never kept.
type tokenHash [sha256.Size]byte

func (h tokenHash) MarshalText() ([]byte, error) { return hex.AppendEncode(nil, h[:]), nil }

func (h *tokenHash) UnmarshalText(b []byte) error {
	if hex.DecodedLen(len(b)) != len(h) {
		return fmt.Errorf("a token hash is %d hex digits, not %d", hex.EncodedLen(len(h)), len(b))
	}
	_, err := hex.Decode(h[:], b)
	return err
}

// metaFile is what the file holds. Domains are sorted by host, accounts by
// id, blocked and removed hosts by name. Lists and maps are never omitted,
// so nil and empty stay apart and a restart reads back exactly what was
// written. A file from before removed hosts were kept has none.
type metaFile struct {
	NextDomain  uint32          `json:"next_domain"`
	NextAccount uint32          `json:"next_account"`
	Domains     []Domain        `json:"domains"`
	Accounts    []storedAccount `json:"accounts"`
	Blocked     []string        `json:"blocked"`
	// Removed holds every host a domain was removed from. Nothing is ever
	// taken off it: AddDomain asks for again to add such a host.
	Removed []string `json:"removed"`
}

// storedAccount is an account with the hash of its token.
type storedAccount struct {
	Account
	TokenHash tokenHash `json:"token_sha256"`
}

// metaSnap is one published state with its lookups. Nothing changes it.
type metaSnap struct {
	file    metaFile
	byHost  map[string]*Domain
	byID    map[uint16]*Domain
	owners  map[uint32]*Account
	byToken map[tokenHash]*Account
	blocked map[string]bool
}

// Meta holds the accounts, domains and blocked hosts. Reads are lock-free;
// changes run one at a time, and each is on disk before anyone sees it.
// A *Domain or *Account a read returns is shared: do not change it.
type Meta struct {
	path     string
	snap     atomic.Pointer[metaSnap]
	mu       sync.Mutex // held by a change
	onChange []func()
}

// OpenMeta reads the file at path. A missing file is an empty start, which
// the first change writes, but only while the folder holds no links: ids
// from 1 again would hand the old links to new domains and accounts.
func OpenMeta(path string) (*Meta, error) {
	f := metaFile{NextDomain: 1, NextAccount: 1}
	data, err := os.ReadFile(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		links, err := holdsLinks(filepath.Dir(path))
		if err != nil {
			return nil, err
		}
		if links {
			return nil, fmt.Errorf("%s is missing but the folder holds links: put the file back, or move the d* folders away to start empty", path)
		}
	case err != nil:
		return nil, err
	default:
		if err := json.Unmarshal(data, &f); err != nil {
			return nil, fmt.Errorf("%s is damaged: %w", path, err)
		}
		if err := f.check(); err != nil {
			return nil, fmt.Errorf("%s is damaged: %w", path, err)
		}
	}
	m := &Meta{path: path}
	m.snap.Store(newMetaSnap(f))
	return m, nil
}

func newMetaSnap(f metaFile) *metaSnap {
	s := &metaSnap{
		file:    f,
		byHost:  make(map[string]*Domain, len(f.Domains)),
		byID:    make(map[uint16]*Domain, len(f.Domains)),
		owners:  make(map[uint32]*Account, len(f.Accounts)),
		byToken: make(map[tokenHash]*Account, len(f.Accounts)),
		blocked: make(map[string]bool, len(f.Blocked)),
	}
	for i := range f.Domains {
		d := &f.Domains[i]
		s.byHost[d.Host] = d
		s.byID[d.ID] = d
	}
	for i := range f.Accounts {
		a := &f.Accounts[i]
		s.owners[a.ID] = &a.Account
		s.byToken[a.TokenHash] = &a.Account
	}
	for _, host := range f.Blocked {
		s.blocked[host] = true
	}
	return s
}

// Domain returns the domain for a Host header value, or nil.
func (m *Meta) Domain(hostport string) *Domain {
	return m.snap.Load().byHost[NormalizeHost(hostport)]
}

// DomainByID returns the domain with the id, or nil.
func (m *Meta) DomainByID(id uint16) *Domain {
	return m.snap.Load().byID[id]
}

// Account returns the account a token belongs to, or nil for an unknown
// token or a disabled account.
func (m *Meta) Account(token string) *Account {
	a := m.snap.Load().byToken[sha256.Sum256([]byte(token))]
	if a == nil || a.Disabled {
		return nil
	}
	return a
}

// Owner returns the account with the id, disabled or not, or nil when it
// has been deleted.
func (m *Meta) Owner(id uint32) *Account {
	return m.snap.Load().owners[id]
}

// Blocked reports whether host or a domain it belongs to is blocked.
// NormalizeHost also drops a leading "www.", which is harmless: Block
// refuses names that start with it, and www.x is under x anyway.
func (m *Meta) Blocked(host string) bool {
	blocked := m.snap.Load().blocked
	for h := NormalizeHost(host); h != ""; _, h, _ = strings.Cut(h, ".") {
		if blocked[h] {
			return true
		}
	}
	return false
}

// Domains returns a copy of every domain, sorted by host.
func (m *Meta) Domains() []Domain {
	return m.snap.Load().file.clone().Domains
}

// Accounts returns a copy of every account, sorted by id.
func (m *Meta) Accounts() []Account {
	f := m.snap.Load().file.clone()
	accounts := make([]Account, len(f.Accounts))
	for i, a := range f.Accounts {
		accounts[i] = a.Account
	}
	return accounts
}

// BlockedHosts returns a copy of the blocked hosts, sorted.
func (m *Meta) BlockedHosts() []string {
	return slices.Clone(m.snap.Load().file.Blocked)
}

// OnChange adds f to the functions that run after every successful change.
// They run after the change is published, so they may read the Meta.
func (m *Meta) OnChange(f func()) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.onChange = append(m.onChange, f)
}

// change edits a copy of the current state, writes it and only then
// publishes it. A refused edit or a failed write changes nothing.
func (m *Meta) change(edit func(f *metaFile) error) error {
	m.mu.Lock()
	f := m.snap.Load().file.clone()
	err := edit(&f)
	if err == nil {
		err = m.save(f)
	}
	listeners := m.onChange
	m.mu.Unlock()
	if err != nil {
		return err
	}
	for _, fn := range listeners {
		fn()
	}
	return nil
}

// save writes f to the file and makes it the current state.
func (m *Meta) save(f metaFile) error {
	slices.SortFunc(f.Domains, func(a, b Domain) int { return strings.Compare(a.Host, b.Host) })
	slices.Sort(f.Blocked)
	slices.Sort(f.Removed)
	b, err := json.MarshalIndent(f, "", "\t")
	if err != nil {
		return err
	}
	if err := writeAtomic(m.path, append(b, '\n'), (*os.File).Sync); err != nil {
		return fmt.Errorf("the change was not saved: %w", err)
	}
	m.snap.Store(newMetaSnap(f))
	return nil
}

// AddDomain adds a short domain and returns it with its new id. A host a
// domain was removed from is added only with again: the new domain hands
// out its own codes, among them codes the old one handed out.
func (m *Meta) AddDomain(host, fallback string, allowed []string, again bool) (Domain, error) {
	var added Domain
	err := m.change(func(f *metaFile) error {
		if err := checkHost(host); err != nil {
			return err
		}
		if _, err := f.domain(host); err == nil {
			return fmt.Errorf("domain %q already exists", host)
		}
		if !again && slices.Contains(f.Removed, host) {
			return fmt.Errorf("domain %q was removed before, and its old short links are dead. A domain added again hands out its own codes, so an old short link still in someone's hands may one day lead to a new target. To add it anyway: qisqa domain add %s --again", host, host)
		}
		if err := checkOptionalURL("fallback URL", fallback); err != nil {
			return err
		}
		targets, err := checkTargets(allowed)
		if err != nil {
			return err
		}
		if f.NextDomain > maxDomainID {
			return fmt.Errorf("cannot add a domain: all %d domain ids have been used, and ids are never reused", maxDomainID)
		}
		added = Domain{ID: uint16(f.NextDomain), Host: host, FallbackURL: fallback, AllowedTargets: targets}
		f.NextDomain++
		f.Domains = append(f.Domains, added)
		return nil
	})
	if err != nil {
		return Domain{}, err
	}
	return added.clone(), nil
}

// SetDomain changes a domain's fallback URL and allowed targets; nil leaves
// either as it is, and an empty list allows every target.
func (m *Meta) SetDomain(host string, fallback *string, allowed []string) error {
	return m.change(func(f *metaFile) error {
		d, err := f.domain(host)
		if err != nil {
			return err
		}
		if fallback != nil {
			if err := checkOptionalURL("fallback URL", *fallback); err != nil {
				return err
			}
			d.FallbackURL = *fallback
		}
		if allowed != nil {
			if d.AllowedTargets, err = checkTargets(allowed); err != nil {
				return err
			}
		}
		return nil
	})
}

// RemoveDomain removes a domain and takes it off every account. Its id is
// never handed out again, so its links stay its own, and its host is kept
// among the removed ones.
func (m *Meta) RemoveDomain(host string) error {
	return m.change(func(f *metaFile) error {
		d, err := f.domain(host)
		if err != nil {
			return err
		}
		if !slices.Contains(f.Removed, d.Host) {
			f.Removed = append(f.Removed, d.Host)
		}
		id := d.ID
		f.Domains = slices.DeleteFunc(f.Domains, func(other Domain) bool { return other.ID == id })
		for i := range f.Accounts {
			f.Accounts[i].Domains = slices.DeleteFunc(f.Accounts[i].Domains, func(other uint16) bool { return other == id })
		}
		return nil
	})
}

// AddAlias makes host/alias redirect to url. Aliases ignore letter case.
func (m *Meta) AddAlias(host, alias, url string) error {
	return m.change(func(f *metaFile) error {
		d, err := f.domain(host)
		if err != nil {
			return err
		}
		lower := strings.ToLower(alias)
		_, taken := d.Static[lower]
		switch {
		case !aliasPattern.MatchString(alias):
			return fmt.Errorf("alias %q must be 3-64 characters of A-Z a-z 0-9 _ -", alias)
		case slices.Contains(reservedAliases, lower):
			return fmt.Errorf("alias %q is reserved", alias)
		case taken:
			return fmt.Errorf("domain %q already has alias %q (aliases ignore letter case)", d.Host, lower)
		case !isHTTPURL(url):
			return fmt.Errorf("alias %q: %q must start with http:// or https://", alias, url)
		}
		if d.Static == nil {
			d.Static = map[string]string{}
		}
		d.Static[lower] = url
		return nil
	})
}

// RemoveAlias removes a static alias.
func (m *Meta) RemoveAlias(host, alias string) error {
	return m.change(func(f *metaFile) error {
		d, err := f.domain(host)
		if err != nil {
			return err
		}
		lower := strings.ToLower(alias)
		if _, ok := d.Static[lower]; !ok {
			return fmt.Errorf("domain %q has no alias %q", d.Host, alias)
		}
		delete(d.Static, lower)
		return nil
	})
}

// AddAccount adds an account that may use the given domains. It returns
// the account's token; only its hash is kept, so it cannot be shown again.
func (m *Meta) AddAccount(name string, hosts []string) (Account, string, error) {
	token := newToken()
	var added Account
	err := m.change(func(f *metaFile) error {
		if err := checkName(name); err != nil {
			return err
		}
		ids, err := f.domainIDs(hosts)
		if err != nil {
			return err
		}
		if f.NextAccount > maxAccountID {
			return fmt.Errorf("cannot add an account: all %d account ids have been used, and ids are never reused", maxAccountID)
		}
		added = Account{ID: f.NextAccount, Name: name, Domains: ids}
		f.NextAccount++
		f.Accounts = append(f.Accounts, storedAccount{Account: added, TokenHash: sha256.Sum256([]byte(token))})
		return nil
	})
	if err != nil {
		return Account{}, "", err
	}
	return added.clone(), token, nil
}

// SetAccount changes an account's name and domains; nil leaves either as
// it is.
func (m *Meta) SetAccount(id uint32, name *string, hosts []string) error {
	return m.change(func(f *metaFile) error {
		a, err := f.account(id)
		if err != nil {
			return err
		}
		if name != nil {
			if err := checkName(*name); err != nil {
				return err
			}
			a.Name = *name
		}
		if hosts != nil {
			if a.Domains, err = f.domainIDs(hosts); err != nil {
				return err
			}
		}
		return nil
	})
}

// ResetToken gives an account a new token and returns it; the old one stops
// working.
func (m *Meta) ResetToken(id uint32) (string, error) {
	token := newToken()
	err := m.change(func(f *metaFile) error {
		a, err := f.account(id)
		if err != nil {
			return err
		}
		a.TokenHash = sha256.Sum256([]byte(token))
		return nil
	})
	if err != nil {
		return "", err
	}
	return token, nil
}

// SetDisabled turns an account's token off or on again. Its links stay.
func (m *Meta) SetDisabled(id uint32, disabled bool) error {
	return m.change(func(f *metaFile) error {
		a, err := f.account(id)
		if err != nil {
			return err
		}
		a.Disabled = disabled
		return nil
	})
}

// DeleteAccount removes an account. Its id is never handed out again.
func (m *Meta) DeleteAccount(id uint32) error {
	return m.change(func(f *metaFile) error {
		if _, err := f.account(id); err != nil {
			return err
		}
		f.Accounts = slices.DeleteFunc(f.Accounts, func(a storedAccount) bool { return a.ID == id })
		return nil
	})
}

// Block stops links to host and to every subdomain of it.
func (m *Meta) Block(host string) error {
	name := strings.ToLower(host)
	return m.change(func(f *metaFile) error {
		if err := checkHost(name); err != nil {
			return err
		}
		if slices.Contains(f.Blocked, name) {
			return fmt.Errorf("%q is already blocked", name)
		}
		f.Blocked = append(f.Blocked, name)
		return nil
	})
}

// Unblock lifts a block.
func (m *Meta) Unblock(host string) error {
	name := strings.ToLower(host)
	return m.change(func(f *metaFile) error {
		i := slices.Index(f.Blocked, name)
		if i < 0 {
			return fmt.Errorf("%q is not blocked", name)
		}
		f.Blocked = slices.Delete(f.Blocked, i, i+1)
		return nil
	})
}

// check refuses a file, from a bad restore or a hand edit, that would hand
// out an id again or make one id stand for two domains or accounts.
func (f *metaFile) check() error {
	if f.NextDomain < 1 || f.NextAccount < 1 {
		return errors.New("next_domain and next_account must be at least 1")
	}
	hosts := map[string]bool{}
	domains := map[uint16]string{}
	for _, d := range f.Domains {
		if d.ID < 1 || uint32(d.ID) >= f.NextDomain {
			return fmt.Errorf("domain %q has id %d; domain ids must be at least 1 and below next_domain (%d)", d.Host, d.ID, f.NextDomain)
		}
		if other, ok := domains[d.ID]; ok {
			return fmt.Errorf("domains %q and %q share id %d", other, d.Host, d.ID)
		}
		if hosts[d.Host] {
			return fmt.Errorf("domain %q is listed twice", d.Host)
		}
		domains[d.ID] = d.Host
		hosts[d.Host] = true
	}
	accounts := map[uint32]bool{}
	for _, a := range f.Accounts {
		if a.ID < 1 || a.ID >= f.NextAccount {
			return fmt.Errorf("account %d (%q) has an id that must be at least 1 and below next_account (%d)", a.ID, a.Name, f.NextAccount)
		}
		if accounts[a.ID] {
			return fmt.Errorf("account %d is listed twice", a.ID)
		}
		accounts[a.ID] = true
	}
	return nil
}

// domain finds a domain the way Meta.Domain does.
func (f *metaFile) domain(host string) (*Domain, error) {
	name := NormalizeHost(host)
	for i := range f.Domains {
		if f.Domains[i].Host == name {
			return &f.Domains[i], nil
		}
	}
	return nil, fmt.Errorf("there is no domain %q", host)
}

// domainIDs turns the hosts an account may use into domain ids.
func (f *metaFile) domainIDs(hosts []string) ([]uint16, error) {
	if len(hosts) == 0 {
		return nil, errors.New("an account needs at least one domain")
	}
	var ids []uint16
	for _, host := range hosts {
		d, err := f.domain(host)
		if err != nil {
			return nil, err
		}
		if !slices.Contains(ids, d.ID) {
			ids = append(ids, d.ID)
		}
	}
	return ids, nil
}

// account finds an account by id.
func (f *metaFile) account(id uint32) (*storedAccount, error) {
	for i := range f.Accounts {
		if f.Accounts[i].ID == id {
			return &f.Accounts[i], nil
		}
	}
	return nil, fmt.Errorf("there is no account %d", id)
}

// clone returns a copy of f that shares nothing with it.
func (f metaFile) clone() metaFile {
	c := f
	c.Domains = make([]Domain, len(f.Domains))
	for i, d := range f.Domains {
		c.Domains[i] = d.clone()
	}
	c.Accounts = make([]storedAccount, len(f.Accounts))
	for i, a := range f.Accounts {
		a.Account = a.Account.clone()
		c.Accounts[i] = a
	}
	c.Blocked = slices.Clone(f.Blocked)
	c.Removed = slices.Clone(f.Removed)
	return c
}

func (d Domain) clone() Domain {
	d.AllowedTargets = slices.Clone(d.AllowedTargets)
	d.Static = maps.Clone(d.Static)
	return d
}

func (a Account) clone() Account {
	a.Domains = slices.Clone(a.Domains)
	return a
}

// checkHost accepts a domain or blocked host as the config did.
func checkHost(host string) error {
	if !hostPattern.MatchString(host) || strings.HasPrefix(host, "www.") {
		return fmt.Errorf(`%q must be a bare lower-case host name without scheme, port or "www."`, host)
	}
	return nil
}

// checkTargets lower-cases allowed targets and refuses a malformed one.
func checkTargets(allowed []string) ([]string, error) {
	var targets []string
	for _, target := range allowed {
		target = strings.ToLower(strings.TrimSpace(target))
		if !targetPattern.MatchString(target) {
			return nil, fmt.Errorf(`allowed target %q must be a host name such as "example.com" or "*.example.com"`, target)
		}
		targets = append(targets, target)
	}
	return targets, nil
}

// checkName refuses an empty or overlong account name.
func checkName(name string) error {
	if len(name) < 1 || len(name) > maxNameLength {
		return fmt.Errorf("an account name must be 1 to %d bytes long; this one is %d", maxNameLength, len(name))
	}
	return nil
}

// newToken returns 32 random bytes as 64 hex characters.
func newToken() string {
	b := make([]byte, 32)
	rand.Read(b)
	return hex.EncodeToString(b)
}

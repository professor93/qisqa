// Tests for the meta file: accounts, domains, aliases and blocked hosts.
package app

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// newTestMeta opens a meta file in a fresh folder.
func newTestMeta(t *testing.T) *Meta {
	t.Helper()
	m, err := OpenMeta(filepath.Join(t.TempDir(), "meta.json"))
	must(t, err)
	return m
}

// reopen opens m's file again, as a restart does.
func reopen(t *testing.T, m *Meta) *Meta {
	t.Helper()
	again, err := OpenMeta(m.path)
	must(t, err)
	return again
}

// metaFromFile opens a meta file written by hand.
func metaFromFile(t *testing.T, content string) *Meta {
	t.Helper()
	m, err := OpenMeta(writeFile(t, t.TempDir(), "meta.json", content))
	must(t, err)
	return m
}

// metaWithNextIDs opens a meta file written by hand whose next ids are the
// given ones.
func metaWithNextIDs(t *testing.T, domain, account uint32) *Meta {
	t.Helper()
	return metaFromFile(t, fmt.Sprintf(`{"next_domain": %d, "next_account": %d}`, domain, account))
}

// withDomains adds the given hosts as plain domains.
func withDomains(t *testing.T, m *Meta, hosts ...string) {
	t.Helper()
	for _, host := range hosts {
		_, err := m.AddDomain(host, "", nil, false)
		must(t, err)
	}
}

func TestRemovedDomain(t *testing.T) {
	m := newTestMeta(t)
	first, err := m.AddDomain("short.example", "", nil, false)
	must(t, err)
	_, err = m.AddDomain("go.example", "", nil, false)
	must(t, err)
	account, _, err := m.AddAccount("a", []string{"short.example", "go.example"})
	must(t, err)

	must(t, m.RemoveDomain("short.example"))
	if m.Domain("short.example") != nil {
		t.Fatal("removed domain is still served")
	}
	eq(t, len(m.Owner(account.ID).Domains), 1)

	again, err := m.AddDomain("short.example", "", nil, true)
	must(t, err)
	if again.ID == first.ID {
		t.Fatal("a new domain got the removed domain's id, and with it its links")
	}
}

// A host that was removed is added again only when that is asked for: its
// old short links may still be in people's hands, and a domain added again
// hands out its own codes. The memory of removed hosts is kept in the file,
// and nothing is ever taken off it.
func TestRemovedHostNeedsAgain(t *testing.T) {
	m := newTestMeta(t)
	first, err := m.AddDomain("short.example", "", nil, false)
	must(t, err)
	withDomains(t, m, "go.example")
	must(t, m.RemoveDomain("short.example"))
	must(t, m.RemoveDomain("WWW.Go.Example")) // remembered as the domain's host

	for _, host := range []string{"short.example", "go.example"} {
		_, err = m.AddDomain(host, "", nil, false)
		wantErr(t, err, `domain "`+host+`" was removed before`)
		wantErr(t, err, "--again")
		if m.Domain(host) != nil {
			t.Fatalf("%s was added", host)
		}
	}

	m = reopen(t, m)
	_, err = m.AddDomain("short.example", "", nil, false)
	wantErr(t, err, "--again")
	again, err := m.AddDomain("short.example", "", nil, true)
	must(t, err)
	if again.ID == first.ID {
		t.Fatal("the host added again got its old id")
	}
	must(t, m.RemoveDomain("short.example"))
	_, err = reopen(t, m).AddDomain("short.example", "", nil, false)
	wantErr(t, err, "--again")

	// A host never removed needs no again, and again does it no harm.
	_, err = m.AddDomain("new.example", "", nil, false)
	must(t, err)
	_, err = m.AddDomain("other.example", "", nil, true)
	must(t, err)

	// A file written before removed hosts were kept opens, with none.
	old := metaFromFile(t, `{"next_domain": 3, "next_account": 1, "domains": [{"id": 2, "host": "go.example"}]}`)
	d, err := old.AddDomain("short.example", "", nil, false)
	must(t, err)
	eq(t, d.ID, 3)
}

func TestBlockedCoversSubdomains(t *testing.T) {
	m := newTestMeta(t)
	must(t, m.Block("Evil.Example"))
	for host, want := range map[string]bool{
		"evil.example": true, "www.evil.example": true, "a.b.evil.example:8443": true, "EVIL.example.": true,
		"notevil.example": false, "evil.example.org": false, "example": false,
	} {
		if got := m.Blocked(host); got != want {
			t.Errorf("Blocked(%q) = %v, want %v", host, got, want)
		}
	}
}

func TestBlockedHosts(t *testing.T) {
	m := newTestMeta(t)
	must(t, m.Block("evil.example"))
	must(t, m.Block("bad.example"))
	for _, host := range []string{"", "https://evil.example", "evil.example:8443", "www.evil.example", "evil.example"} {
		if err := m.Block(host); err == nil {
			t.Errorf("Block(%q) was accepted", host)
		}
	}
	eq(t, strings.Join(m.BlockedHosts(), " "), "bad.example evil.example")

	must(t, m.Unblock("Evil.Example"))
	if m.Blocked("evil.example") {
		t.Fatal("an unblocked host is still blocked")
	}
	wantErr(t, m.Unblock("evil.example"), "not blocked")
	eq(t, strings.Join(m.BlockedHosts(), " "), "bad.example")
}

func TestMetaSurvivesRestart(t *testing.T) {
	m := newTestMeta(t)
	_, err := m.AddDomain("short.example", "https://example.com/", []string{"example.com", "*.example.org"}, false)
	must(t, err)
	withDomains(t, m, "go.example")
	must(t, m.AddAlias("short.example", "Android", "https://example.com/android"))
	_, token, err := m.AddAccount("a", []string{"go.example"})
	must(t, err)
	off, _, err := m.AddAccount("b", []string{"short.example", "go.example"})
	must(t, err)
	must(t, m.SetDisabled(off.ID, true))
	must(t, m.Block("evil.example"))

	again := reopen(t, m)
	eq(t, len(again.Domains()), 2)
	eq(t, len(again.Accounts()), 2)
	if !reflect.DeepEqual(again.Domains(), m.Domains()) {
		t.Errorf("domains after a restart: %+v, want %+v", again.Domains(), m.Domains())
	}
	if !reflect.DeepEqual(again.Accounts(), m.Accounts()) {
		t.Errorf("accounts after a restart: %+v, want %+v", again.Accounts(), m.Accounts())
	}
	if !reflect.DeepEqual(again.BlockedHosts(), m.BlockedHosts()) {
		t.Errorf("blocked hosts after a restart: %v, want %v", again.BlockedHosts(), m.BlockedHosts())
	}
	if url, _ := again.Domain("short.example").StaticURL("android"); url != "https://example.com/android" {
		t.Errorf("alias after a restart points at %q", url)
	}
	if again.Account(token) == nil {
		t.Error("the token stopped working after a restart")
	}

	info, err := os.Stat(m.path)
	must(t, err)
	eq(t, info.Mode().Perm(), os.FileMode(0o600))
	entries, err := os.ReadDir(filepath.Dir(m.path))
	must(t, err)
	for _, entry := range entries {
		if entry.Name() != "meta.json" {
			t.Errorf("the folder holds %s besides the meta file", entry.Name())
		}
	}
}

func TestMetaRefusesDamagedFile(t *testing.T) {
	// Starting empty would lose every account at the next change.
	for _, content := range []string{"", `{"domains": [`, `{"accounts": [{"id": 1, "token_sha256": "abc"}]}`} {
		path := writeFile(t, t.TempDir(), "meta.json", content)
		if _, err := OpenMeta(path); err == nil {
			t.Errorf("OpenMeta accepted %q", content)
		}
	}
}

func TestMetaRefusesToStartEmptyBesideLinks(t *testing.T) {
	// Starting empty would hand domain 1's links to the next domain added.
	dir := filepath.Join(t.TempDir(), "data")
	st, err := OpenStore(dir)
	must(t, err)
	create(t, st, 1)
	must(t, st.Close())

	path := filepath.Join(dir, "meta.json")
	_, err = OpenMeta(path)
	wantErr(t, err, path+" is missing but the folder holds links")
	if _, err := os.Stat(path); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("a refused start left a file behind: %v", err)
	}

	must(t, os.WriteFile(path, []byte(`{"next_domain": 2, "next_account": 2, "domains": [{"id": 1, "host": "short.example"}]}`), 0o600))
	m, err := OpenMeta(path)
	must(t, err)
	if m.DomainByID(1) == nil {
		t.Fatal("the file put back was not read")
	}

	// Without links, a missing file is an empty start, and a folder whose
	// name only starts with d holds no links.
	empty := t.TempDir()
	must(t, os.Mkdir(filepath.Join(empty, "docs"), 0o700))
	m, err = OpenMeta(filepath.Join(empty, "meta.json"))
	must(t, err)
	eq(t, len(m.Domains()), 0)
	if _, err := os.Stat(filepath.Join(empty, "meta.json")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("opening created the file: %v", err)
	}
}

func TestMetaRefusesIDsThatWouldBeReused(t *testing.T) {
	for _, c := range []struct{ content, want string }{
		{`{"next_domain": 0, "next_account": 1}`, "next_domain and next_account must be at least 1"},
		{`{"next_domain": 1, "next_account": 0}`, "next_domain and next_account must be at least 1"},
		{`{"next_domain": 2, "next_account": 1, "domains": [{"id": 0, "host": "short.example"}]}`,
			`domain "short.example" has id 0; domain ids must be at least 1 and below next_domain (2)`},
		{`{"next_domain": 2, "next_account": 1, "domains": [{"id": 2, "host": "short.example"}]}`,
			`domain "short.example" has id 2; domain ids must be at least 1 and below next_domain (2)`},
		{`{"next_domain": 2, "next_account": 1, "domains": [{"id": 7, "host": "short.example"}]}`,
			`domain "short.example" has id 7`},
		{`{"next_domain": 3, "next_account": 1, "domains": [{"id": 1, "host": "go.example"}, {"id": 1, "host": "short.example"}]}`,
			`domains "go.example" and "short.example" share id 1`},
		{`{"next_domain": 3, "next_account": 1, "domains": [{"id": 1, "host": "short.example"}, {"id": 2, "host": "short.example"}]}`,
			`domain "short.example" is listed twice`},
		{`{"next_domain": 1, "next_account": 2, "accounts": [{"id": 0, "name": "a"}]}`,
			`account 0 ("a") has an id that must be at least 1 and below next_account (2)`},
		{`{"next_domain": 1, "next_account": 2, "accounts": [{"id": 2, "name": "a"}]}`,
			`account 2 ("a") has an id that must be at least 1 and below next_account (2)`},
		{`{"next_domain": 1, "next_account": 3, "accounts": [{"id": 1, "name": "a"}, {"id": 1, "name": "b"}]}`,
			`account 1 is listed twice`},
	} {
		path := writeFile(t, t.TempDir(), "meta.json", c.content)
		_, err := OpenMeta(path)
		if err == nil {
			t.Errorf("OpenMeta accepted %s", c.content)
			continue
		}
		if !strings.Contains(err.Error(), path) || !strings.Contains(err.Error(), c.want) {
			t.Errorf("OpenMeta(%s): error %q does not name the file and %q", c.content, err, c.want)
		}
	}

	// Gaps left by removals are fine; the next ids carry on after them.
	m := metaFromFile(t, `{"next_domain": 5, "next_account": 4,
		"domains": [{"id": 2, "host": "go.example"}, {"id": 4, "host": "short.example"}],
		"accounts": [{"id": 3, "name": "a", "domains": [4]}]}`)
	d, err := m.AddDomain("new.example", "", nil, false)
	must(t, err)
	eq(t, d.ID, 5)
	a, _, err := m.AddAccount("b", []string{"short.example"})
	must(t, err)
	eq(t, a.ID, 4)
}

func TestFailedWriteChangesNothing(t *testing.T) {
	m := newTestMeta(t)
	changed := false
	m.OnChange(func() { changed = true })
	// A folder where the file goes makes the write fail, also for root,
	// whom a read-only folder does not stop.
	must(t, os.MkdirAll(filepath.Join(m.path, "in-the-way"), 0o700))

	if _, err := m.AddDomain("short.example", "", nil, false); err == nil {
		t.Fatal("AddDomain succeeded although the file could not be written")
	}
	if m.Domain("short.example") != nil {
		t.Fatal("a change that was not saved is served")
	}
	if changed {
		t.Fatal("OnChange ran after a change that was not saved")
	}

	must(t, os.RemoveAll(m.path))
	d, err := m.AddDomain("short.example", "", nil, false)
	must(t, err)
	eq(t, d.ID, 1) // the failed change did not use up the id
}

func TestAccountToken(t *testing.T) {
	m := newTestMeta(t)
	withDomains(t, m, "short.example")
	account, token, err := m.AddAccount("a", []string{"short.example"})
	must(t, err)
	eq(t, len(token), 64)
	if _, err := hex.DecodeString(token); err != nil {
		t.Fatalf("token %q is not hex", token)
	}

	if got := m.Account(token); got == nil || got.ID != account.ID {
		t.Fatalf("Account(token) = %+v, want account %d", got, account.ID)
	}
	sum := sha256.Sum256([]byte(token))
	for _, wrong := range []string{strings.Repeat("0", 64), "", hex.EncodeToString(sum[:])} {
		if got := m.Account(wrong); got != nil {
			t.Errorf("Account(%q) = %+v, want nil", wrong, got)
		}
	}

	data, err := os.ReadFile(m.path)
	must(t, err)
	if bytes.Contains(data, []byte(token)) {
		t.Fatal("the file holds the token")
	}
}

func TestResetToken(t *testing.T) {
	m := newTestMeta(t)
	withDomains(t, m, "short.example")
	account, old, err := m.AddAccount("a", []string{"short.example"})
	must(t, err)

	fresh, err := m.ResetToken(account.ID)
	must(t, err)
	if m.Account(old) != nil {
		t.Fatal("the old token still works")
	}
	if got := m.Account(fresh); got == nil || got.ID != account.ID {
		t.Fatalf("Account(new token) = %+v, want account %d", got, account.ID)
	}
	_, err = m.ResetToken(account.ID + 1)
	wantErr(t, err, "no account")
}

func TestDisabledAccount(t *testing.T) {
	m := newTestMeta(t)
	withDomains(t, m, "short.example")
	account, token, err := m.AddAccount("a", []string{"short.example"})
	must(t, err)

	must(t, m.SetDisabled(account.ID, true))
	if m.Account(token) != nil {
		t.Fatal("a disabled account's token works")
	}
	if owner := m.Owner(account.ID); owner == nil || !owner.Disabled {
		t.Fatalf("Owner of a disabled account = %+v", owner)
	}

	must(t, m.SetDisabled(account.ID, false))
	if m.Account(token) == nil {
		t.Fatal("the token does not work after enabling the account")
	}
}

func TestDeletedAccountIDIsNotReused(t *testing.T) {
	m := newTestMeta(t)
	withDomains(t, m, "short.example")
	hosts := []string{"short.example"}
	first, token, err := m.AddAccount("a", hosts)
	must(t, err)

	must(t, m.DeleteAccount(first.ID))
	if m.Owner(first.ID) != nil {
		t.Fatal("a deleted account is still an owner")
	}
	if m.Account(token) != nil {
		t.Fatal("a deleted account's token works")
	}
	second, _, err := m.AddAccount("b", hosts)
	must(t, err)
	if second.ID <= first.ID {
		t.Fatalf("new account got id %d after deleted id %d", second.ID, first.ID)
	}

	must(t, m.DeleteAccount(second.ID))
	m = reopen(t, m)
	third, _, err := m.AddAccount("c", hosts)
	must(t, err)
	if third.ID <= second.ID {
		t.Fatalf("after a restart, new account got id %d after deleted id %d", third.ID, second.ID)
	}
	wantErr(t, m.DeleteAccount(second.ID), "no account")
}

func TestAccountNeedsKnownDomains(t *testing.T) {
	m := newTestMeta(t)
	withDomains(t, m, "short.example")

	_, _, err := m.AddAccount("a", nil)
	wantErr(t, err, "at least one domain")
	_, _, err = m.AddAccount("a", []string{"short.example", "other.example"})
	wantErr(t, err, `"other.example"`)
	_, _, err = m.AddAccount(strings.Repeat("a", 65), []string{"short.example"})
	wantErr(t, err, "1 to 64 bytes")
	_, _, err = m.AddAccount("", []string{"short.example"})
	wantErr(t, err, "1 to 64 bytes")
	eq(t, len(m.Accounts()), 0)

	account, _, err := m.AddAccount(strings.Repeat("a", 64), []string{"short.example", "SHORT.example"})
	must(t, err)
	eq(t, len(account.Domains), 1)
}

func TestAccountSettings(t *testing.T) {
	m := newTestMeta(t)
	withDomains(t, m, "short.example", "go.example")
	account, token, err := m.AddAccount("a", []string{"short.example"})
	must(t, err)

	name := "b"
	must(t, m.SetAccount(account.ID, &name, nil))
	got := m.Owner(account.ID)
	eq(t, got.Name, "b")
	eq(t, len(got.Domains), 1)

	must(t, m.SetAccount(account.ID, nil, []string{"go.example", "short.example"}))
	got = m.Owner(account.ID)
	eq(t, got.Name, "b")
	eq(t, len(got.Domains), 2)
	if m.Account(token) == nil {
		t.Fatal("changing an account stopped its token")
	}

	wantErr(t, m.SetAccount(account.ID, nil, []string{}), "at least one domain")
	long := strings.Repeat("b", 65)
	wantErr(t, m.SetAccount(account.ID, &long, nil), "1 to 64 bytes")
	wantErr(t, m.SetAccount(account.ID+1, &name, nil), "no account")
	eq(t, len(m.Owner(account.ID).Domains), 2)
}

func TestAccountIDLimit(t *testing.T) {
	m := metaWithNextIDs(t, 1, 1<<24-1)
	withDomains(t, m, "short.example")
	last, _, err := m.AddAccount("a", []string{"short.example"})
	must(t, err)
	eq(t, last.ID, 1<<24-1)

	_, _, err = m.AddAccount("b", []string{"short.example"})
	wantErr(t, err, "all 16777215 account ids")
}

func TestDomainIDLimit(t *testing.T) {
	m := metaWithNextIDs(t, 65535, 1)
	last, err := m.AddDomain("short.example", "", nil, false)
	must(t, err)
	eq(t, last.ID, 65535)

	_, err = m.AddDomain("go.example", "", nil, false)
	wantErr(t, err, "all 65535 domain ids")
}

func TestDomainValidation(t *testing.T) {
	m := newTestMeta(t)
	withDomains(t, m, "short.example")

	for _, host := range []string{"https://short.example", "short.example:8080", "www.short.example", "Short.Example", ""} {
		_, err := m.AddDomain(host, "", nil, false)
		wantErr(t, err, `must be a bare lower-case host name`)
	}
	_, err := m.AddDomain("short.example", "", nil, false)
	wantErr(t, err, "already exists")
	_, err = m.AddDomain("go.example", "", []string{"https://example.com"}, false)
	wantErr(t, err, `"https://example.com" must be a host name`)
	_, err = m.AddDomain("go.example", "ftp://example.com", nil, false)
	wantErr(t, err, "must start with http:// or https://")
	eq(t, len(m.Domains()), 1)

	d, err := m.AddDomain("go.example", "https://example.com/", []string{" Example.COM", "*.example.org"}, false)
	must(t, err)
	eq(t, strings.Join(d.AllowedTargets, " "), "example.com *.example.org")
	eq(t, d.FallbackURL, "https://example.com/")
}

func TestDomainSettings(t *testing.T) {
	m := newTestMeta(t)
	_, err := m.AddDomain("short.example", "https://example.com/", []string{"example.com"}, false)
	must(t, err)

	fallback := "https://example.org/"
	must(t, m.SetDomain("short.example", &fallback, nil))
	d := m.Domain("short.example")
	eq(t, d.FallbackURL, fallback)
	eq(t, strings.Join(d.AllowedTargets, " "), "example.com")

	must(t, m.SetDomain("short.example", nil, []string{}))
	d = m.Domain("short.example")
	eq(t, d.FallbackURL, fallback)
	eq(t, len(d.AllowedTargets), 0)

	none := ""
	must(t, m.SetDomain("short.example", &none, nil))
	eq(t, m.Domain("short.example").FallbackURL, "")

	bad := "ftp://example.com"
	wantErr(t, m.SetDomain("short.example", &bad, nil), "must start with http:// or https://")
	wantErr(t, m.SetDomain("short.example", nil, []string{"*example.com"}), "must be a host name")
	wantErr(t, m.SetDomain("go.example", nil, nil), `no domain "go.example"`)
}

func TestAliases(t *testing.T) {
	m := newTestMeta(t)
	withDomains(t, m, "short.example")
	must(t, m.AddAlias("short.example", "Android", "https://example.com/android"))
	before := m.Domain("short.example")
	if url, ok := before.StaticURL("aNDROID"); !ok || url != "https://example.com/android" {
		t.Fatalf("StaticURL(aNDROID) = %q, %v", url, ok)
	}

	for alias, url := range map[string]string{
		"api":     "https://example.com/api",
		"HealthZ": "https://example.com/healthz",
		"ab":      "https://example.com/ab",
		"ANDROID": "https://example.com/other",
		"ios":     "ftp://example.com/ios",
	} {
		if err := m.AddAlias("short.example", alias, url); err == nil {
			t.Errorf("AddAlias(%q, %q) was accepted", alias, url)
		}
	}
	wantErr(t, m.AddAlias("go.example", "ios", "https://example.com/ios"), `no domain "go.example"`)

	must(t, m.RemoveAlias("short.example", "ANDROID"))
	if _, ok := m.Domain("short.example").StaticURL("android"); ok {
		t.Fatal("a removed alias is still found")
	}
	if _, ok := before.StaticURL("android"); !ok {
		t.Fatal("a change altered a domain that was already handed out")
	}
	wantErr(t, m.RemoveAlias("short.example", "android"), "no alias")
}

func TestDomainLookupNormalizes(t *testing.T) {
	m := newTestMeta(t)
	d, err := m.AddDomain("short.example", "", nil, false)
	must(t, err)

	if got := m.Domain("WWW.Short.Example:8080"); got == nil || got.Host != "short.example" {
		t.Fatalf("Domain(WWW.Short.Example:8080) = %+v", got)
	}
	if got := m.DomainByID(d.ID); got == nil || got.Host != "short.example" {
		t.Fatalf("DomainByID(%d) = %+v", d.ID, got)
	}
	if m.DomainByID(d.ID+1) != nil || m.Domain("go.example") != nil {
		t.Fatal("an unknown domain was found")
	}
}

func TestListingsAreSortedCopies(t *testing.T) {
	m := newTestMeta(t)
	_, err := m.AddDomain("short.example", "", []string{"example.com"}, false)
	must(t, err)
	withDomains(t, m, "go.example")
	must(t, m.AddAlias("short.example", "android", "https://example.com/android"))
	_, _, err = m.AddAccount("b", []string{"short.example"})
	must(t, err)
	_, _, err = m.AddAccount("a", []string{"short.example"})
	must(t, err)
	must(t, m.Block("evil.example"))
	must(t, m.Block("bad.example"))

	domains := m.Domains()
	eq(t, domains[0].Host, "go.example")
	eq(t, domains[1].Host, "short.example")
	accounts := m.Accounts()
	eq(t, accounts[0].Name, "b")
	eq(t, accounts[1].Name, "a")
	blocked := m.BlockedHosts()
	eq(t, blocked[0], "bad.example")

	domains[1].AllowedTargets[0] = "example.org"
	domains[1].Static["android"] = "https://example.org/"
	accounts[0].Domains[0] = 99
	blocked[0] = "good.example"
	d := m.Domain("short.example")
	eq(t, d.AllowedTargets[0], "example.com")
	eq(t, d.Static["android"], "https://example.com/android")
	eq(t, m.Owner(accounts[0].ID).Domains[0], d.ID)
	if !m.Blocked("bad.example") {
		t.Fatal("changing a listing unblocked a host")
	}
}

func TestOnChange(t *testing.T) {
	m := newTestMeta(t)
	calls, want := 0, 0
	m.OnChange(func() { calls++ })
	check := func(err error, saved bool) {
		t.Helper()
		if saved {
			must(t, err)
			want++
		} else if err == nil {
			t.Fatal("a change that should be refused was accepted")
		}
		eq(t, calls, want)
	}

	_, err := m.AddDomain("short.example", "", nil, false)
	check(err, true)
	_, err = m.AddDomain("short.example", "", nil, false)
	check(err, false)
	check(m.SetDomain("short.example", nil, []string{"example.com"}), true)
	check(m.AddAlias("short.example", "android", testURL), true)
	check(m.AddAlias("short.example", "api", testURL), false)
	check(m.RemoveAlias("short.example", "android"), true)
	account, _, err := m.AddAccount("a", []string{"short.example"})
	check(err, true)
	_, _, err = m.AddAccount("b", []string{"go.example"})
	check(err, false)
	name := "b"
	check(m.SetAccount(account.ID, &name, nil), true)
	_, err = m.ResetToken(account.ID)
	check(err, true)
	check(m.SetDisabled(account.ID, true), true)
	check(m.DeleteAccount(account.ID), true)
	check(m.DeleteAccount(account.ID), false)
	check(m.Block("evil.example"), true)
	check(m.Unblock("evil.example"), true)
	check(m.Unblock("evil.example"), false)
	check(m.RemoveDomain("short.example"), true)
	check(m.RemoveDomain("short.example"), false)
}

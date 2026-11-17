// Tests for the admin commands: what each one prints and changes, its
// errors, the admin log, and the socket the command line reaches them by.
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"testing"
	"time"
)

// tokenPattern finds a token: 64 hex digits.
var tokenPattern = regexp.MustCompile(`[0-9a-f]{64}`)

// shortTempDir returns a new folder, removed when the test ends, whose path
// is short enough for a socket in it: a socket's path has a length limit,
// and a test's own temporary folder can come close to it.
func shortTempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "qisqa")
	must(t, err)
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

// serverOn returns a server on the data folder dir, opened as the program
// opens it.
func serverOn(t *testing.T, dir string) *Server {
	t.Helper()
	st, err := OpenStore(dir)
	must(t, err)
	t.Cleanup(func() { st.Close() })
	meta, err := OpenMeta(filepath.Join(dir, "meta.json"))
	must(t, err)
	return NewServer(NewWatcher("", testConfig()), meta, st, nil)
}

// newAdminServer returns a server with no domains, accounts or blocked
// hosts on a fresh data folder, and the folder.
func newAdminServer(t *testing.T) (*Server, string) {
	t.Helper()
	dir := shortTempDir(t)
	return serverOn(t, dir), dir
}

// words splits a command line at spaces; "" stands for an empty word.
func words(line string) []string {
	w := strings.Fields(line)
	for i := range w {
		if w[i] == `""` {
			w[i] = ""
		}
	}
	return w
}

// listen opens the admin socket of s in dir until the test ends.
func listen(t *testing.T, dir string, s *Server) net.Listener {
	t.Helper()
	l, err := ListenAdmin(dir, s)
	must(t, err)
	t.Cleanup(func() { l.Close() })
	return l
}

// leaveSocket leaves a socket file at path that nobody listens on, as a
// server that was killed leaves it.
func leaveSocket(t *testing.T, path string) {
	t.Helper()
	l, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	must(t, err)
	l.SetUnlinkOnClose(false)
	must(t, l.Close())
}

// shortenAdminTimeout makes both sides of the admin socket give up after d
// until the test ends.
func shortenAdminTimeout(t *testing.T, d time.Duration) {
	t.Helper()
	old := adminTimeout
	adminTimeout = d
	t.Cleanup(func() { adminTimeout = old })
}

// dialAdmin opens a plain connection to the admin socket in dir.
func dialAdmin(t *testing.T, dir string) net.Conn {
	t.Helper()
	conn, err := net.Dial("unix", filepath.Join(dir, "qisqa.sock"))
	must(t, err)
	t.Cleanup(func() { conn.Close() })
	return conn
}

// holdAdminClients connects two clients to the admin socket in dir that
// never finish, and returns their connections: one says nothing, the other
// stops in the middle of its command.
func holdAdminClients(t *testing.T, dir string) []net.Conn {
	t.Helper()
	silent, stuck := dialAdmin(t, dir), dialAdmin(t, dir)
	_, err := io.WriteString(stuck, "POST / HTTP/1.1\r\nHost: qisqa\r\nContent-Length: 100\r\n\r\n[\"domain\"")
	must(t, err)
	time.Sleep(200 * time.Millisecond) // for the server to take both
	return []net.Conn{silent, stuck}
}

// requestAdmin sends one HTTP request to the admin socket in dir and returns
// the status and the answer.
func requestAdmin(t *testing.T, dir, method, path, body string) (int, adminAnswer) {
	t.Helper()
	client := &http.Client{
		Timeout: 5 * time.Second,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, "unix", filepath.Join(dir, "qisqa.sock"))
			},
			DisableKeepAlives: true,
		},
	}
	r, err := http.NewRequest(method, "http://qisqa"+path, strings.NewReader(body))
	must(t, err)
	response, err := client.Do(r)
	must(t, err)
	defer response.Body.Close()
	var answer adminAnswer
	json.NewDecoder(response.Body).Decode(&answer) // an answer that is not JSON stays empty
	return response.StatusCode, answer
}

// domainOf returns the domain of host, failing the test if there is none.
func domainOf(t *testing.T, m *Meta, host string) *Domain {
	t.Helper()
	d := m.Domain(host)
	if d == nil {
		t.Fatalf("there is no domain %q", host)
	}
	return d
}

func TestAdminCommands(t *testing.T) {
	const (
		addShort = "domain add short.example"
		addGo    = "domain add go.example"
		addFull  = "domain add short.example --fallback https://example.org/home --allow example.com"
		addA     = "account add a --domain short.example"
	)
	tests := []struct {
		setup []string // command lines run first
		line  string
		want  string // the output, with every token written as <token>
		// check looks at the effect; tokens are the tokens printed by the
		// setup and the command, in order.
		check func(t *testing.T, m *Meta, tokens []string)
	}{
		{
			line: "domain add short.example",
			want: "added domain 1  short.example",
			check: func(t *testing.T, m *Meta, _ []string) {
				d := domainOf(t, m, "short.example")
				eq(t, d.ID, 1)
				eq(t, d.FallbackURL, "")
				eq(t, len(d.AllowedTargets), 0)
			},
		},
		{
			// Flags may stand anywhere after the first two words.
			line: "domain add --allow example.com go.example --fallback https://example.org/home --allow *.example.com",
			want: "added domain 1  go.example",
			check: func(t *testing.T, m *Meta, _ []string) {
				d := domainOf(t, m, "go.example")
				eq(t, d.FallbackURL, "https://example.org/home")
				eq(t, strings.Join(d.AllowedTargets, ","), "example.com,*.example.com")
			},
		},
		{
			setup: []string{addShort},
			line:  "domain set short.example --fallback https://example.org/home --allow example.com",
			want:  "updated domain short.example",
			check: func(t *testing.T, m *Meta, _ []string) {
				d := domainOf(t, m, "short.example")
				eq(t, d.FallbackURL, "https://example.org/home")
				eq(t, strings.Join(d.AllowedTargets, ","), "example.com")
			},
		},
		{
			// A flag left out leaves its value.
			setup: []string{addFull},
			line:  "domain set short.example --allow example.org --allow *.example.org",
			want:  "updated domain short.example",
			check: func(t *testing.T, m *Meta, _ []string) {
				d := domainOf(t, m, "short.example")
				eq(t, d.FallbackURL, "https://example.org/home")
				eq(t, strings.Join(d.AllowedTargets, ","), "example.org,*.example.org")
			},
		},
		{
			// An empty value clears the fallback and the allowed targets.
			setup: []string{addFull},
			line:  `domain set short.example --fallback "" --allow ""`,
			want:  "updated domain short.example",
			check: func(t *testing.T, m *Meta, _ []string) {
				d := domainOf(t, m, "short.example")
				eq(t, d.FallbackURL, "")
				eq(t, len(d.AllowedTargets), 0)
			},
		},
		{
			// A host removed before is added again only with --again: a new
			// domain with a new number.
			setup: []string{addShort, "domain remove short.example"},
			line:  "domain add short.example --again",
			want:  "added domain 2  short.example",
			check: func(t *testing.T, m *Meta, _ []string) {
				eq(t, domainOf(t, m, "short.example").ID, 2)
			},
		},
		{
			// --again does no harm to a host never removed.
			line: "domain add --again short.example",
			want: "added domain 1  short.example",
		},
		{
			setup: []string{addGo, addShort},
			line:  "domain remove short.example",
			want:  "removed domain short.example; its files stay in d2",
			check: func(t *testing.T, m *Meta, _ []string) {
				if m.Domain("short.example") != nil {
					t.Error("the domain is still there")
				}
			},
		},
		{
			setup: []string{
				addShort,
				"domain add go.example --fallback https://example.org/home --allow example.com --allow *.example.com",
				"alias add go.example ios https://example.com/ios",
				"alias add go.example android https://example.com/android",
			},
			line: "domain list",
			want: "2  go.example  fallback=https://example.org/home  allow=example.com,*.example.com  aliases=android,ios\n" +
				"1  short.example",
		},
		{
			setup: []string{addShort},
			line:  "alias add short.example Android https://example.com/android",
			want:  "added alias short.example/android",
			check: func(t *testing.T, m *Meta, _ []string) {
				target, ok := domainOf(t, m, "short.example").StaticURL("android")
				eq(t, ok, true)
				eq(t, target, "https://example.com/android")
			},
		},
		{
			setup: []string{addShort, "alias add short.example android https://example.com/android"},
			line:  "alias remove short.example android",
			want:  "removed alias short.example/android",
			check: func(t *testing.T, m *Meta, _ []string) {
				_, ok := domainOf(t, m, "short.example").StaticURL("android")
				eq(t, ok, false)
			},
		},
		{
			setup: []string{addShort, addGo},
			line:  "account add a --domain short.example --domain go.example",
			want:  "added account 1  a\ntoken: <token>\nKeep the token: it is not shown again.",
			check: func(t *testing.T, m *Meta, tokens []string) {
				a := m.Account(tokens[0])
				if a == nil {
					t.Fatal("the printed token is not accepted")
				}
				eq(t, a.ID, 1)
				eq(t, a.Name, "a")
				eq(t, fmt.Sprint(a.Domains), "[1 2]")
			},
		},
		{
			setup: []string{addShort, addGo, addA},
			line:  "account set 1 --name b --domain go.example",
			want:  "updated account 1",
			check: func(t *testing.T, m *Meta, _ []string) {
				a := m.Owner(1)
				eq(t, a.Name, "b")
				eq(t, fmt.Sprint(a.Domains), "[2]")
			},
		},
		{
			setup: []string{addShort, addA},
			line:  "account token 1",
			want:  "token: <token>\nThe old token no longer works.",
			check: func(t *testing.T, m *Meta, tokens []string) {
				if m.Account(tokens[0]) != nil {
					t.Error("the old token still works")
				}
				if a := m.Account(tokens[1]); a == nil || a.ID != 1 {
					t.Errorf("the new token gives account %v", a)
				}
			},
		},
		{
			setup: []string{addShort, addA},
			line:  "account disable 1",
			want:  "disabled account 1; its links are stopped",
			check: func(t *testing.T, m *Meta, tokens []string) {
				if m.Account(tokens[0]) != nil {
					t.Error("the token of a disabled account still works")
				}
				eq(t, m.Owner(1).Disabled, true)
			},
		},
		{
			setup: []string{addShort, addA, "account disable 1"},
			line:  "account enable 1",
			want:  "enabled account 1; its links work again",
			check: func(t *testing.T, m *Meta, tokens []string) {
				if m.Account(tokens[0]) == nil {
					t.Error("the token does not work again")
				}
			},
		},
		{
			setup: []string{addShort, addA},
			line:  "account delete 1",
			want:  "deleted account 1; its links are stopped for good",
			check: func(t *testing.T, m *Meta, tokens []string) {
				if m.Owner(1) != nil || m.Account(tokens[0]) != nil {
					t.Error("the account is still there")
				}
			},
		},
		{
			setup: []string{
				addShort, addGo,
				"account add a --domain short.example --domain go.example",
				"account add b --domain short.example",
				"account disable 2",
			},
			line: "account list",
			want: "1  a  short.example,go.example  enabled\n2  b  short.example  disabled",
		},
		{
			line: "block add Evil.example",
			want: "blocked evil.example",
			check: func(t *testing.T, m *Meta, _ []string) {
				eq(t, m.Blocked("evil.example"), true)
			},
		},
		{
			setup: []string{"block add evil.example"},
			line:  "block remove evil.example",
			want:  "unblocked evil.example",
			check: func(t *testing.T, m *Meta, _ []string) {
				eq(t, m.Blocked("evil.example"), false)
			},
		},
		{
			setup: []string{"block add evil.example", "block add bad.example"},
			line:  "block list",
			want:  "bad.example\nevil.example",
		},
		// An empty list prints nothing.
		{line: "domain list", want: ""},
		{line: "account list", want: ""},
		{line: "block list", want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.line, func(t *testing.T) {
			s, _ := newAdminServer(t)
			var tokens []string
			var output string
			for _, line := range append(tt.setup, tt.line) {
				var err error
				output, err = s.Admin(words(line))
				if err != nil {
					t.Fatalf("%s: %v", line, err)
				}
				tokens = append(tokens, tokenPattern.FindAllString(output, -1)...)
			}
			eq(t, tokenPattern.ReplaceAllString(output, "<token>"), tt.want)
			if tt.check != nil {
				tt.check(t, s.meta, tokens)
			}
		})
	}
}

func TestAdminTokenIsShownOnce(t *testing.T) {
	s, _ := newAdminServer(t)
	_, err := s.Admin(words("domain add short.example"))
	must(t, err)
	output, err := s.Admin(words("account add a --domain short.example"))
	must(t, err)
	token := tokenPattern.FindString(output)
	hash := sha256.Sum256([]byte(token))

	list, err := s.Admin(words("account list"))
	must(t, err)
	eq(t, list, "1  a  short.example  enabled")
	for _, secret := range []string{token, hex.EncodeToString(hash[:])} {
		if strings.Contains(list, secret) {
			t.Errorf("account list shows %s", secret)
		}
	}
}

func TestAdminCommandsAreLogged(t *testing.T) {
	s, _ := newAdminServer(t)
	_, err := s.meta.AddDomain("short.example", "", nil, false) // not a command: not logged
	must(t, err)
	logDir := logTo(t, s)

	output, err := s.Admin(words("account add a --domain short.example"))
	must(t, err)
	token := tokenPattern.FindString(output)
	_, err = s.Admin(words("account disable 99"))
	wantErr(t, err, "there is no account 99")

	lines := loggedLines(t, logDir, "admin")
	eq(t, len(lines), 2)
	eq(t, lines[0], "ok\taccount\tadd\ta\t--domain\tshort.example")
	eq(t, lines[1], "there is no account 99\taccount\tdisable\t99")
	for _, line := range lines {
		if strings.Contains(line, token) {
			t.Errorf("the admin log holds the token: %s", line)
		}
	}
}

// TestAdminLogKeepsTheUsageOut: an unknown command is logged with the first
// line of its error; the usage text that follows is not.
func TestAdminLogKeepsTheUsageOut(t *testing.T) {
	s, _ := newAdminServer(t)
	logDir := logTo(t, s)
	_, err := s.Admin(words("domain frob"))
	wantErr(t, err, "domain list")
	eq(t, strings.Join(loggedLines(t, logDir, "admin"), "\n"), "unknown command \"domain frob\"\tdomain\tfrob")
}

func TestAdminErrors(t *testing.T) {
	s, dir := newAdminServer(t)
	for _, line := range []string{"domain add short.example", "account add a --domain short.example", "domain add old.example", "domain remove old.example"} {
		_, err := s.Admin(words(line))
		must(t, err)
	}
	before, err := os.ReadFile(filepath.Join(dir, "meta.json"))
	must(t, err)

	for _, tt := range []struct {
		line string
		want []string // what the error says
	}{
		{"domain frob", []string{`unknown command "domain frob"`, "\n  account add NAME --domain HOST [--domain HOST]...\n"}},
		{"domain", []string{`unknown command "domain"`, "\n  block list"}},
		{"", []string{"unknown command", "\n  domain list\n"}},
		{"domain add", []string{"HOST is missing", "usage: qisqa domain add HOST [--fallback URL] [--allow HOST]... [--again]"}},
		{"domain add old.example", []string{`domain "old.example" was removed before`, "its old short links are dead", "may one day lead to a new target", "To add it anyway: qisqa domain add old.example --again"}},
		{"domain add old.example --again yes", []string{`unexpected word "yes"`}},
		{"domain add old.example --again --again", []string{"--again is given twice"}},
		{"alias add short.example android", []string{"URL is missing", "usage: qisqa alias add HOST NAME URL"}},
		{"domain list all", []string{`unexpected word "all"`, "usage: qisqa domain list"}},
		{"account add b", []string{"an account needs at least one domain"}},
		{"account add b --domain go.example", []string{`there is no domain "go.example"`}},
		{"account disable 99", []string{"there is no account 99"}},
		{"account disable one", []string{`account ID "one" is not a number`}},
		{"account token 4294967296", []string{"there is no account 4294967296"}},
		{"domain add https://short.example", []string{`"https://short.example" must be a bare lower-case host name`}},
		{"domain add go.example --colour red", []string{`unknown flag "--colour"`, "usage: qisqa domain add"}},
		{"domain add go.example --name go", []string{`unknown flag "--name"`}},
		{"domain add go.example --fallback", []string{"--fallback needs a value", "usage: qisqa domain add"}},
		{"domain add go.example --fallback https://example.org/a --fallback https://example.org/b", []string{"--fallback is given twice"}},
		{"domain set short.example", []string{"nothing to change: give --fallback, --allow or both"}},
		{"account set 1", []string{"nothing to change: give --name, --domain or both"}},
		{"account set 1 --domain short.example --name", []string{"--name needs a value"}},
	} {
		output, err := s.Admin(words(tt.line))
		if err == nil {
			t.Errorf("%q: no error; output %q", tt.line, output)
			continue
		}
		eq(t, output, "")
		for _, want := range tt.want {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("%q: error %q does not say %q", tt.line, err, want)
			}
		}
	}

	after, err := os.ReadFile(filepath.Join(dir, "meta.json"))
	must(t, err)
	if !bytes.Equal(after, before) {
		t.Error("a command that failed changed the meta file")
	}
}

func TestAdminOverSocket(t *testing.T) {
	s, dir := newAdminServer(t)
	_, err := s.Admin(words("domain add short.example"))
	must(t, err)
	path := filepath.Join(dir, "qisqa.sock")
	// A socket file left behind does not stop ListenAdmin.
	leaveSocket(t, path)

	l, err := ListenAdmin(dir, s)
	must(t, err)
	defer l.Close()
	eq(t, l.Addr().String(), path)
	info, err := os.Lstat(path)
	must(t, err)
	eq(t, info.Mode().Type(), fs.ModeSocket)
	eq(t, info.Mode().Perm(), 0o600)

	want, err := s.Admin(words("domain list"))
	must(t, err)
	got, err := SendAdmin(dir, words("domain list"))
	must(t, err)
	eq(t, got, want)
	// An error comes back as the server's error text.
	if _, err := SendAdmin(dir, words("account disable 99")); err == nil || err.Error() != "there is no account 99" {
		t.Errorf("an error comes back as %v", err)
	}

	// The socket file exists only while the server listens.
	must(t, l.Close())
	if _, err := os.Lstat(path); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("the socket file is still there after Close: %v", err)
	}
	_, err = SendAdmin(dir, words("domain list"))
	wantErr(t, err, "not running")
}

// TestAdminSocketIsNeverOpenToOthers: in a data folder others may enter, the
// socket is made in a folder only the owner may enter and is private before
// it gets its name, so no other user can connect to that name at any time.
func TestAdminSocketIsNeverOpenToOthers(t *testing.T) {
	s, dir := newAdminServer(t)
	must(t, os.Chmod(dir, 0o755))
	path := filepath.Join(dir, "qisqa.sock")
	bound := ""
	s.socketBound = func(name string) {
		bound = name
		if _, err := os.Lstat(path); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("%s exists before the socket is private: %v", path, err)
		}
		folder := filepath.Dir(name)
		if !strings.HasPrefix(folder, dir+string(filepath.Separator)) {
			t.Errorf("the socket is bound at %s, outside the data folder", name)
		}
		info, err := os.Stat(folder)
		must(t, err)
		if info.Mode().Perm()&0o077 != 0 {
			t.Errorf("the socket is bound in %s, which others may enter: %v", folder, info.Mode())
		}
		info, err = os.Lstat(name)
		must(t, err)
		if info.Mode().Type() != fs.ModeSocket || info.Mode().Perm() != 0o600 {
			t.Errorf("%s is not a private socket before it gets its name: %v", name, info.Mode())
		}
	}
	listen(t, dir, s)
	if bound == "" {
		t.Fatal("the socket was not bound under a name of its own first")
	}
	info, err := os.Lstat(path)
	must(t, err)
	eq(t, info.Mode().Type(), fs.ModeSocket)
	eq(t, info.Mode().Perm(), 0o600)
	// The folder it was made in is gone.
	if _, err := os.Lstat(filepath.Dir(bound)); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("%s is still there: %v", filepath.Dir(bound), err)
	}
	got, err := SendAdmin(dir, words("block list"))
	must(t, err)
	eq(t, got, "")
}

// TestListenAdminFollowsNoLink: a link planted at the socket's name is
// removed; the file it points at is left as it is.
func TestListenAdminFollowsNoLink(t *testing.T) {
	s, dir := newAdminServer(t)
	target := writeFile(t, t.TempDir(), "kept.txt", "kept")
	must(t, os.Symlink(target, filepath.Join(dir, "qisqa.sock")))
	listen(t, dir, s)
	data, err := os.ReadFile(target)
	must(t, err)
	eq(t, string(data), "kept")
	info, err := os.Lstat(filepath.Join(dir, "qisqa.sock"))
	must(t, err)
	eq(t, info.Mode().Type(), fs.ModeSocket)
}

func TestListenAdminRefusesAFolderInTheWay(t *testing.T) {
	s, dir := newAdminServer(t)
	path := filepath.Join(dir, "qisqa.sock")
	must(t, os.MkdirAll(filepath.Join(path, "in the way"), 0o700))
	_, err := ListenAdmin(dir, s)
	wantErr(t, err, path)
	wantErr(t, err, "not empty")
}

// TestListenAdminRemovesWhatAKilledStartLeft: a start killed while it made
// the socket leaves the folder the socket is made in behind, with the
// socket in it. The next start removes both.
func TestListenAdminRemovesWhatAKilledStartLeft(t *testing.T) {
	s, dir := newAdminServer(t)
	tmpDir := filepath.Join(dir, "sock.tmp")
	must(t, os.Mkdir(tmpDir, 0o700))
	leaveSocket(t, filepath.Join(tmpDir, "s"))
	listen(t, dir, s)
	if _, err := os.Lstat(tmpDir); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("%s is still there: %v", tmpDir, err)
	}
	_, err := SendAdmin(dir, words("block list"))
	must(t, err)
}

// TestListenAdminRemovesOnlyItsOwnFiles: what qisqa never puts in the
// folder the socket is made in stays there, and the start fails, naming
// the folder.
func TestListenAdminRemovesOnlyItsOwnFiles(t *testing.T) {
	s, dir := newAdminServer(t)
	tmpDir := filepath.Join(dir, "sock.tmp")
	must(t, os.Mkdir(tmpDir, 0o700))
	kept := writeFile(t, tmpDir, "kept.txt", "kept")
	_, err := ListenAdmin(dir, s)
	wantErr(t, err, tmpDir)
	data, err := os.ReadFile(kept)
	must(t, err)
	eq(t, string(data), "kept")
}

// TestListenAdminFollowsNoLinkAtItsFolder: a link planted at the name of
// the folder the socket is made in is removed; the folder it points at,
// and a file there with the socket's temporary name, are left as they are.
func TestListenAdminFollowsNoLinkAtItsFolder(t *testing.T) {
	s, dir := newAdminServer(t)
	target := t.TempDir()
	kept := writeFile(t, target, "s", "kept")
	must(t, os.Symlink(target, filepath.Join(dir, "sock.tmp")))
	listen(t, dir, s)
	data, err := os.ReadFile(kept)
	must(t, err)
	eq(t, string(data), "kept")
	_, err = SendAdmin(dir, words("block list"))
	must(t, err)
}

// TestAdminCloseDropsClientsAfterAGrace: at shutdown, a client that said
// nothing or stopped in the middle of its command gets a short grace and
// is then dropped. That is no failure of Close.
func TestAdminCloseDropsClientsAfterAGrace(t *testing.T) {
	s, dir := newAdminServer(t)
	l, err := ListenAdmin(dir, s)
	must(t, err)
	clients := holdAdminClients(t, dir)

	closing := time.Now()
	must(t, l.Close())
	if took := time.Since(closing); took > 4*time.Second {
		t.Errorf("Close took %s", took.Round(time.Millisecond))
	}
	for i, conn := range clients {
		conn.SetReadDeadline(time.Now().Add(time.Second))
		if _, err := io.ReadAll(conn); errors.Is(err, os.ErrDeadlineExceeded) {
			t.Errorf("client %d is still connected after Close", i+1)
		}
	}
	if _, err := os.Lstat(filepath.Join(dir, "qisqa.sock")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("the socket file is still there after Close: %v", err)
	}
}

func TestSendAdminWithoutServer(t *testing.T) {
	dir := shortTempDir(t)
	_, err := SendAdmin(dir, words("domain list"))
	wantErr(t, err, "not running")
	wantErr(t, err, dir)

	// A socket file nobody listens on is the same.
	leaveSocket(t, filepath.Join(dir, "qisqa.sock"))
	_, again := SendAdmin(dir, words("domain list"))
	if again == nil || again.Error() != err.Error() {
		t.Fatalf("with a socket file nobody listens on: %v; without one: %v", again, err)
	}
}

// TestSendAdminGivesUp: a server that takes the connection and never answers
// does not keep the command waiting.
func TestSendAdminGivesUp(t *testing.T) {
	shortenAdminTimeout(t, 200*time.Millisecond)
	dir := shortTempDir(t)
	l, err := net.Listen("unix", filepath.Join(dir, "qisqa.sock"))
	must(t, err)
	defer l.Close()
	go func() {
		var held []net.Conn
		defer func() {
			for _, conn := range held {
				conn.Close()
			}
		}()
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			held = append(held, conn)
		}
	}()

	done := make(chan error, 1)
	go func() {
		_, err := SendAdmin(dir, words("domain list"))
		done <- err
	}()
	select {
	case err := <-done:
		wantErr(t, err, "did not answer")
	case <-time.After(5 * time.Second):
		t.Fatal("SendAdmin still waits for a server that does not answer")
	}
}

func TestSocketPathTooLong(t *testing.T) {
	s, _ := newAdminServer(t)
	dir := filepath.Join(shortTempDir(t), "d")
	dir += strings.Repeat("x", 200-len(dir))
	must(t, os.Mkdir(dir, 0o700))
	_, err := ListenAdmin(dir, s)
	wantErr(t, err, dir)
	wantErr(t, err, "shorter")

	// No server can run there, and the command line says why.
	_, err = SendAdmin(dir, words("domain list"))
	wantErr(t, err, "not running")
	wantErr(t, err, "too long")
}

// TestSocketPathAtTheLimit: a data folder whose socket path is as long as
// the system allows works, and one byte more is refused.
func TestSocketPathAtTheLimit(t *testing.T) {
	s, _ := newAdminServer(t)
	base := shortTempDir(t)
	longest := len(syscall.RawSockaddrUnix{}.Path) - 1 // the name ends with a 0 byte
	dir := filepath.Join(base, strings.Repeat("x", longest-len(base)-len("/")-len("/qisqa.sock")))
	must(t, os.Mkdir(dir, 0o700))
	eq(t, len(filepath.Join(dir, "qisqa.sock")), longest)
	listen(t, dir, s)
	_, err := SendAdmin(dir, words("domain list"))
	must(t, err)

	longer := dir + "x"
	must(t, os.Mkdir(longer, 0o700))
	_, err = ListenAdmin(longer, s)
	wantErr(t, err, "shorter")
}

// TestAdminSocketDropsAStuckClient: a client that stops in the middle of its
// request is dropped after the timeout.
func TestAdminSocketDropsAStuckClient(t *testing.T) {
	shortenAdminTimeout(t, 200*time.Millisecond)
	s, dir := newAdminServer(t)
	listen(t, dir, s)
	for name, request := range map[string]string{
		"in the header": "POST / HTTP/1.1\r\nHost: qisqa\r\n",
		"in the body":   "POST / HTTP/1.1\r\nHost: qisqa\r\nContent-Length: 100\r\n\r\n[\"domain\"",
	} {
		conn := dialAdmin(t, dir)
		_, err := io.WriteString(conn, request)
		must(t, err)
		conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		if _, err := io.ReadAll(conn); errors.Is(err, os.ErrDeadlineExceeded) {
			t.Errorf("stuck %s: the server still holds the connection after 5s", name)
		}
	}
}

// TestAdminSocketDropsAClientThatDoesNotRead: a client that never reads its
// answer is dropped after the timeout.
func TestAdminSocketDropsAClientThatDoesNotRead(t *testing.T) {
	shortenAdminTimeout(t, 200*time.Millisecond)
	dir := shortTempDir(t)
	// An answer far larger than a socket's buffers.
	hosts := make([]string, 100_000)
	for i := range hosts {
		hosts[i] = fmt.Sprintf("h%06d.example", i)
	}
	data, err := json.Marshal(metaFile{NextDomain: 1, NextAccount: 1, Domains: []Domain{}, Accounts: []storedAccount{}, Blocked: hosts})
	must(t, err)
	writeFile(t, dir, "meta.json", string(data))
	s := serverOn(t, dir)
	listen(t, dir, s)

	conn := dialAdmin(t, dir)
	body := `["block","list"]`
	_, err = fmt.Fprintf(conn, "POST / HTTP/1.1\r\nHost: qisqa\r\nContent-Length: %d\r\n\r\n%s", len(body), body)
	must(t, err)
	time.Sleep(5 * adminTimeout) // the server's writes stop and time out
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	answer, err := io.ReadAll(conn)
	if errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatal("the server still holds the connection after 5s")
	}
	if bytes.Contains(answer, []byte(hosts[len(hosts)-1])) {
		t.Fatal("the whole answer arrived: the server waited for a client that did not read")
	}
}

// TestAdminSocketRefusesAHugeCommand: a request body over the limit is
// refused unread, and the socket goes on answering.
func TestAdminSocketRefusesAHugeCommand(t *testing.T) {
	s, dir := newAdminServer(t)
	listen(t, dir, s)
	host := strings.Repeat("a", 100_000) + ".example"
	body, err := json.Marshal([]string{"block", "add", host})
	must(t, err)
	status, answer := requestAdmin(t, dir, "POST", "/", string(body))
	eq(t, status, http.StatusRequestEntityTooLarge)
	wantErr(t, errors.New(answer.Error), "at most 65536 bytes")
	eq(t, s.meta.Blocked(host), false)

	_, err = SendAdmin(dir, words("block list"))
	must(t, err)
}

// TestAdminSocketServesPostRootOnly: a request with another method or path
// runs no command.
func TestAdminSocketServesPostRootOnly(t *testing.T) {
	s, dir := newAdminServer(t)
	listen(t, dir, s)
	body := `["block","add","evil.example"]`
	for _, request := range []struct{ method, path string }{{"GET", "/"}, {"PUT", "/"}, {"POST", "/block"}} {
		status, answer := requestAdmin(t, dir, request.method, request.path, body)
		if status == http.StatusOK || answer.Error == "" {
			t.Errorf("%s %s: status %d, answer %+v", request.method, request.path, status, answer)
		}
	}
	eq(t, s.meta.Blocked("evil.example"), false)
	status, answer := requestAdmin(t, dir, "POST", "/", `{"words": ["block", "list"]}`)
	eq(t, status, http.StatusBadRequest)
	wantErr(t, errors.New(answer.Error), "JSON array of words")
}

// TestAdminTokenIsWrittenNowhere: over the socket, with every log on, a
// token appears in the output of account add and account token only.
func TestAdminTokenIsWrittenNowhere(t *testing.T) {
	captured := captureLog(t)
	s, dir := newAdminServer(t)
	logDir := logTo(t, s)
	listen(t, dir, s)

	var printed []string
	for _, line := range []string{"domain add short.example", "account add a --domain short.example", "account token 1"} {
		output, err := SendAdmin(dir, words(line))
		must(t, err)
		printed = append(printed, tokenPattern.FindAllString(output, -1)...)
	}
	eq(t, len(printed), 2)
	list, err := SendAdmin(dir, words("account list"))
	must(t, err)
	if tokenPattern.MatchString(list) {
		t.Errorf("account list shows a token or a hash: %s", list)
	}
	// An error never carries the new token of a command that failed.
	_, err = SendAdmin(dir, words("account token 99"))
	wantErr(t, err, "there is no account 99")
	if tokenPattern.MatchString(err.Error()) {
		t.Errorf("an error holds a token: %v", err)
	}

	for name, text := range map[string]string{
		"the admin log":  strings.Join(loggedLines(t, logDir, "admin"), "\n"),
		"the error log":  strings.Join(loggedLines(t, logDir, "error"), "\n"),
		"the access log": strings.Join(loggedLines(t, logDir, "access"), "\n"),
		"slog":           captured.String(),
	} {
		if tokenPattern.MatchString(text) {
			t.Errorf("%s holds a token: %s", name, text)
		}
	}
}

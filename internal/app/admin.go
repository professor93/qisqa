// Admin commands: accounts, domains, aliases and blocked hosts are managed
// from the command line only. The same binary, run with command words,
// sends them over a Unix socket in the data folder to the running server,
// which parses them, makes the change through its own Meta and answers with
// the text to print.
package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"maps"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// adminCommand is one admin command.
type adminCommand struct {
	// usage is the command's line in the usage text. Its first two words
	// name the command; the words after them, up to the first flag, name
	// the words it takes.
	usage string
	flags map[string]flagCount // the flags it takes, without the dashes
	run   func(s *Server, c adminCall) (string, error)
}

// flagCount says how often a flag may be given, and whether it takes a
// value.
type flagCount int

const (
	once  flagCount = iota // at most once
	many                   // any number of times
	alone                  // at most once, without a value
)

// adminCommands are the commands, in the order of the usage text.
var adminCommands = []adminCommand{
	{"domain add HOST [--fallback URL] [--allow HOST]... [--again]", map[string]flagCount{"fallback": once, "allow": many, "again": alone}, (*Server).domainAdd},
	{"domain set HOST [--fallback URL] [--allow HOST]...", map[string]flagCount{"fallback": once, "allow": many}, (*Server).domainSet},
	{"domain remove HOST", nil, (*Server).domainRemove},
	{"domain list", nil, (*Server).domainList},
	{"alias add HOST NAME URL", nil, (*Server).aliasAdd},
	{"alias remove HOST NAME", nil, (*Server).aliasRemove},
	{"account add NAME --domain HOST [--domain HOST]...", map[string]flagCount{"domain": many}, (*Server).accountAdd},
	{"account set ID [--name NAME] [--domain HOST]...", map[string]flagCount{"name": once, "domain": many}, (*Server).accountSet},
	{"account token ID", nil, (*Server).accountToken},
	{"account disable ID", nil, (*Server).accountDisable},
	{"account enable ID", nil, (*Server).accountEnable},
	{"account delete ID", nil, (*Server).accountDelete},
	{"account list", nil, (*Server).accountList},
	{"block add HOST", nil, (*Server).blockAdd},
	{"block remove HOST", nil, (*Server).blockRemove},
	{"block list", nil, (*Server).blockList},
}

// adminUsage is the text an unknown command answers with.
func adminUsage() string {
	lines := []string{"usage: qisqa [-config FILE | -data DIR] COMMAND", "", "commands, run by the server that uses the data folder:"}
	for _, c := range adminCommands {
		lines = append(lines, "  "+c.usage)
	}
	return strings.Join(lines, "\n")
}

// Admin runs one admin command, given as the words after the program name
// and its flags, and returns the text to print. Every command is written to
// the admin log: the result, "ok" or the error, then the words as typed. A
// token is only ever in the output, so it is never logged.
func (s *Server) Admin(args []string) (string, error) {
	s.adminMu.Lock()
	defer s.adminMu.Unlock()
	output, err := s.admin(args)
	result := "ok"
	if err != nil {
		// The usage text that follows an unknown command stays out.
		result, _, _ = strings.Cut(err.Error(), "\n")
	}
	s.logs.Admin.Line(append([]string{result}, args...)...)
	return output, err
}

// admin finds the command its first two words name, sorts out the rest and
// runs it.
func (s *Server) admin(args []string) (string, error) {
	for _, c := range adminCommands {
		if len(args) >= 2 && args[0]+" "+args[1] == c.name() {
			call, err := c.parse(args[2:])
			if err != nil {
				return "", fmt.Errorf("%w; usage: qisqa %s", err, c.usage)
			}
			return c.run(s, call)
		}
	}
	return "", fmt.Errorf("unknown command %q\n\n%s", strings.Join(args[:min(len(args), 2)], " "), adminUsage())
}

// name returns the command's first two words.
func (c adminCommand) name() string {
	return strings.Join(strings.Fields(c.usage)[:2], " ")
}

// wordNames returns the names of the words the command takes after its
// first two: those its usage lists before the first flag.
func (c adminCommand) wordNames() []string {
	fields := strings.Fields(c.usage)[2:]
	for i, field := range fields {
		if strings.HasPrefix(field, "-") || strings.HasPrefix(field, "[") {
			return fields[:i]
		}
	}
	return fields
}

// adminCall is a command's words after its first two, sorted out.
type adminCall struct {
	words []string            // the words that are not flags, in order
	flags map[string][]string // the values of each flag given, in order
}

// parse sorts out the words after a command's first two. Flags may stand
// anywhere among them; a flag that takes a value takes the word after it,
// whatever that word is. A flag without a value gets "".
func (c adminCommand) parse(args []string) (adminCall, error) {
	call := adminCall{flags: map[string][]string{}}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if !strings.HasPrefix(arg, "--") {
			call.words = append(call.words, arg)
			continue
		}
		name := arg[2:]
		count, known := c.flags[name]
		switch {
		case !known:
			return adminCall{}, fmt.Errorf("unknown flag %q", arg)
		case count == alone && len(call.flags[name]) > 0:
			return adminCall{}, fmt.Errorf("%s is given twice", arg)
		case count == alone:
			call.flags[name] = []string{""}
			continue
		case i+1 == len(args):
			return adminCall{}, fmt.Errorf("%s needs a value", arg)
		case count == once && len(call.flags[name]) > 0:
			return adminCall{}, fmt.Errorf("%s is given twice", arg)
		}
		i++
		call.flags[name] = append(call.flags[name], args[i])
	}
	names := c.wordNames()
	switch {
	case len(call.words) < len(names):
		return adminCall{}, fmt.Errorf("%s is missing", names[len(call.words)])
	case len(call.words) > len(names):
		return adminCall{}, fmt.Errorf("unexpected word %q", call.words[len(names)])
	}
	return call, nil
}

// flag returns the value of a flag given at most once, and whether it was
// given.
func (c adminCall) flag(name string) (string, bool) {
	values := c.flags[name]
	if len(values) == 0 {
		return "", false
	}
	return values[0], true
}

// allowed returns the values of --allow: nil when it was not given, and an
// empty list for a single --allow "", which allows every target again.
func (c adminCall) allowed() []string {
	values := c.flags["allow"]
	if len(values) == 1 && values[0] == "" {
		return []string{}
	}
	return values
}

// accountID reads the ID word of an account command.
func accountID(word string) (uint32, error) {
	id, err := strconv.ParseUint(word, 10, 32)
	switch {
	case errors.Is(err, strconv.ErrRange):
		return 0, fmt.Errorf("there is no account %s", word)
	case err != nil:
		return 0, fmt.Errorf("account ID %q is not a number", word)
	}
	return uint32(id), nil
}

// The commands. Each one returns the text to print, one line per item and
// two spaces between fields, without a newline at the end. Admin holds
// adminMu, so a domain a command finds before or after its change is the
// one the change was made to.

func (s *Server) domainAdd(c adminCall) (string, error) {
	fallback, _ := c.flag("fallback")
	_, again := c.flag("again")
	d, err := s.meta.AddDomain(c.words[0], fallback, c.allowed(), again)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("added domain %d  %s", d.ID, d.Host), nil
}

// domainSet changes what its flags name; --fallback "" removes the fallback.
func (s *Server) domainSet(c adminCall) (string, error) {
	var fallback *string
	if value, ok := c.flag("fallback"); ok {
		fallback = &value
	}
	allowed := c.allowed()
	if fallback == nil && allowed == nil {
		return "", errors.New("nothing to change: give --fallback, --allow or both")
	}
	if err := s.meta.SetDomain(c.words[0], fallback, allowed); err != nil {
		return "", err
	}
	return "updated domain " + s.meta.Domain(c.words[0]).Host, nil
}

func (s *Server) domainRemove(c adminCall) (string, error) {
	d := s.meta.Domain(c.words[0])
	if err := s.meta.RemoveDomain(c.words[0]); err != nil {
		return "", err
	}
	return fmt.Sprintf("removed domain %s; its files stay in d%d", d.Host, d.ID), nil
}

// domainList leaves out what a domain does not have: without allow= every
// target is allowed.
func (s *Server) domainList(adminCall) (string, error) {
	var lines []string
	for _, d := range s.meta.Domains() {
		line := fmt.Sprintf("%d  %s", d.ID, d.Host)
		if d.FallbackURL != "" {
			line += "  fallback=" + d.FallbackURL
		}
		if len(d.AllowedTargets) > 0 {
			line += "  allow=" + strings.Join(d.AllowedTargets, ",")
		}
		if len(d.Static) > 0 {
			line += "  aliases=" + strings.Join(slices.Sorted(maps.Keys(d.Static)), ",")
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n"), nil
}

func (s *Server) aliasAdd(c adminCall) (string, error) {
	host, alias, url := c.words[0], c.words[1], c.words[2]
	if err := s.meta.AddAlias(host, alias, url); err != nil {
		return "", err
	}
	return "added alias " + s.meta.Domain(host).Host + "/" + strings.ToLower(alias), nil
}

func (s *Server) aliasRemove(c adminCall) (string, error) {
	host, alias := c.words[0], c.words[1]
	if err := s.meta.RemoveAlias(host, alias); err != nil {
		return "", err
	}
	return "removed alias " + s.meta.Domain(host).Host + "/" + strings.ToLower(alias), nil
}

func (s *Server) accountAdd(c adminCall) (string, error) {
	a, token, err := s.meta.AddAccount(c.words[0], c.flags["domain"])
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("added account %d  %s\ntoken: %s\nKeep the token: it is not shown again.", a.ID, a.Name, token), nil
}

// accountSet changes what its flags name; --domain replaces the account's
// list of domains.
func (s *Server) accountSet(c adminCall) (string, error) {
	id, err := accountID(c.words[0])
	if err != nil {
		return "", err
	}
	var name *string
	if value, ok := c.flag("name"); ok {
		name = &value
	}
	hosts := c.flags["domain"]
	if name == nil && hosts == nil {
		return "", errors.New("nothing to change: give --name, --domain or both")
	}
	if err := s.meta.SetAccount(id, name, hosts); err != nil {
		return "", err
	}
	return fmt.Sprintf("updated account %d", id), nil
}

func (s *Server) accountToken(c adminCall) (string, error) {
	id, err := accountID(c.words[0])
	if err != nil {
		return "", err
	}
	token, err := s.meta.ResetToken(id)
	if err != nil {
		return "", err
	}
	return "token: " + token + "\nThe old token no longer works.", nil
}

func (s *Server) accountDisable(c adminCall) (string, error) {
	return onAccount(c, func(id uint32) error { return s.meta.SetDisabled(id, true) }, "disabled account %d; its links are stopped")
}

func (s *Server) accountEnable(c adminCall) (string, error) {
	return onAccount(c, func(id uint32) error { return s.meta.SetDisabled(id, false) }, "enabled account %d; its links work again")
}

func (s *Server) accountDelete(c adminCall) (string, error) {
	return onAccount(c, s.meta.DeleteAccount, "deleted account %d; its links are stopped for good")
}

// onAccount makes change to the account the command names and says what was
// done: done with the account's id.
func onAccount(c adminCall, change func(id uint32) error, done string) (string, error) {
	id, err := accountID(c.words[0])
	if err != nil {
		return "", err
	}
	if err := change(id); err != nil {
		return "", err
	}
	return fmt.Sprintf(done, id), nil
}

// accountList shows each account's domains by host, in the account's order.
func (s *Server) accountList(adminCall) (string, error) {
	var lines []string
	for _, a := range s.meta.Accounts() {
		hosts := make([]string, len(a.Domains))
		for i, id := range a.Domains {
			if d := s.meta.DomainByID(id); d != nil {
				hosts[i] = d.Host
			} else {
				hosts[i] = strconv.Itoa(int(id)) // left by a hand edit of the meta file
			}
		}
		state := "enabled"
		if a.Disabled {
			state = "disabled"
		}
		lines = append(lines, fmt.Sprintf("%d  %s  %s  %s", a.ID, a.Name, strings.Join(hosts, ","), state))
	}
	return strings.Join(lines, "\n"), nil
}

func (s *Server) blockAdd(c adminCall) (string, error) {
	if err := s.meta.Block(c.words[0]); err != nil {
		return "", err
	}
	return "blocked " + strings.ToLower(c.words[0]), nil
}

func (s *Server) blockRemove(c adminCall) (string, error) {
	if err := s.meta.Unblock(c.words[0]); err != nil {
		return "", err
	}
	return "unblocked " + strings.ToLower(c.words[0]), nil
}

func (s *Server) blockList(adminCall) (string, error) {
	return strings.Join(s.meta.BlockedHosts(), "\n"), nil
}

// The admin socket.

const (
	socketName = "qisqa.sock" // in the data folder
	// socketTmpDir is the folder in the data folder that the socket is made
	// in. With the socket's temporary name "s", its path is exactly as long
	// as the socket's own.
	socketTmpDir = "sock.tmp"

	maxAdminRequest = 64 << 10 // bytes: far more than any command needs
)

// maxSocketPath is the longest path a socket may have on this system: the
// address holds the path and a 0 byte after it.
const maxSocketPath = len(syscall.RawSockaddrUnix{}.Path) - 1

// adminTimeout is how long one admin command may take, on either side of
// the socket. It is a variable so that tests can shorten it.
var adminTimeout = 10 * time.Second

// adminGrace is how long the clients of the socket have at shutdown to
// finish a command in progress.
const adminGrace = 2 * time.Second

// adminAnswer is the server's answer to one command. Error is empty when
// the command worked.
type adminAnswer struct {
	Output string `json:"output"`
	Error  string `json:"error"`
}

// ListenAdmin listens on <dataDir>/qisqa.sock, which only the owner may
// use, and serves admin commands on it until the listener is closed.
// Closing the listener also removes the socket file.
//
// The socket is never open to anyone else, whatever the umask and the mode
// of the data folder: it is made in a new folder only the owner may enter,
// made private there and only then renamed to its own name. Whatever an
// earlier run left at that name, or in that folder, is removed first, by
// its name only, so a link planted there is not followed. The store's lock
// already proves that no other server uses the folder.
func ListenAdmin(dataDir string, s *Server) (net.Listener, error) {
	path := filepath.Join(dataDir, socketName)
	tmpDir := filepath.Join(dataDir, socketTmpDir)
	bound := filepath.Join(tmpDir, "s")
	if max(len(path), len(bound)) > maxSocketPath {
		return nil, fmt.Errorf("admin socket: the path %s is too long for a socket on this system, which allows %d bytes: choose a shorter data_dir", path, maxSocketPath)
	}
	if err := remove(path); err != nil {
		return nil, fmt.Errorf("admin socket: %w", err)
	}
	if err := removeSocketTmpDir(tmpDir, bound); err != nil {
		return nil, fmt.Errorf("admin socket: %w", err)
	}
	if err := os.Mkdir(tmpDir, 0o700); err != nil {
		return nil, fmt.Errorf("admin socket: %w", err)
	}
	defer os.Remove(tmpDir) // empty by then: the socket has left it or is removed
	l, err := net.ListenUnix("unix", &net.UnixAddr{Name: bound, Net: "unix"})
	if err != nil {
		return nil, fmt.Errorf("admin socket: %w", err)
	}
	err = os.Chmod(bound, 0o600)
	if err == nil && s.socketBound != nil {
		s.socketBound(bound)
	}
	if err == nil {
		err = os.Rename(bound, path)
	}
	if err != nil {
		l.Close() // removes the socket under its temporary name
		return nil, fmt.Errorf("admin socket: %w", err)
	}
	// On close, Go would remove the name the socket was bound under, which
	// it no longer has; adminListener.Close removes its own name instead.
	l.SetUnlinkOnClose(false)

	server := &http.Server{
		Handler: http.HandlerFunc(s.handleAdmin),
		// A client that stops sending or reading is dropped, so that it
		// cannot hold the socket.
		ReadTimeout:  adminTimeout,
		WriteTimeout: adminTimeout,
	}
	go func() {
		if err := server.Serve(l); !errors.Is(err, http.ErrServerClosed) {
			slog.Error("the admin socket stopped taking commands", "error", err)
		}
	}()
	return &adminListener{Listener: l, path: path, server: server}, nil
}

// removeSocketTmpDir removes the folder the socket is made in, which a run
// killed while it started can leave behind, with the socket in it at bound.
// Only what qisqa puts there is removed, each by its name: anything else
// in the folder stays, and so does the folder, which is an error then. A
// link at the folder's name is removed, not followed.
func removeSocketTmpDir(dir, bound string) error {
	info, err := os.Lstat(dir)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil
	case err != nil:
		return err
	case info.IsDir():
		if err := remove(bound); err != nil {
			return err
		}
	}
	return remove(dir)
}

// adminListener is the admin socket while the server serves on it. Accept
// is the server's.
type adminListener struct {
	net.Listener
	path   string // the socket's own name, not the one it was bound under
	server *http.Server
}

// Addr returns the socket's own name.
func (l *adminListener) Addr() net.Addr {
	return &net.UnixAddr{Name: l.path, Net: "unix"}
}

// Close stops taking commands, gives the clients adminGrace to finish a
// command in progress, drops those still connected after it and removes
// the socket file. Dropping them is no failure: a command takes far less
// than the grace, so a client still there has said nothing, or stopped in
// the middle of its command.
func (l *adminListener) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), adminGrace)
	defer cancel()
	err := l.server.Shutdown(ctx)
	if errors.Is(err, context.DeadlineExceeded) {
		l.server.Close()
		err = nil
	}
	return errors.Join(err, remove(l.path))
}

// handleAdmin serves POST / with a JSON array of words: it runs them as one
// command and answers with an adminAnswer, as every other request is
// answered too.
func (s *Server) handleAdmin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || r.URL.Path != "/" {
		writeJSON(w, http.StatusNotFound, adminAnswer{Error: "the admin socket takes POST / only"})
		return
	}
	var words []string
	err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxAdminRequest)).Decode(&words)
	var tooLarge *http.MaxBytesError
	switch {
	case errors.As(err, &tooLarge):
		writeJSON(w, http.StatusRequestEntityTooLarge, adminAnswer{Error: fmt.Sprintf("a command may be at most %d bytes long", maxAdminRequest)})
		return
	case err != nil:
		writeJSON(w, http.StatusBadRequest, adminAnswer{Error: "the request must be a JSON array of words"})
		return
	}
	output, err := s.Admin(words)
	answer := adminAnswer{Output: output}
	if err != nil {
		answer.Error = err.Error()
	}
	writeJSON(w, http.StatusOK, answer)
}

// SendAdmin sends one admin command to the server that uses dataDir and
// returns its answer; the server's error text comes back as the error. Only
// the socket is opened: every change goes through the running server, whose
// link cache hears only the changes it makes itself.
func SendAdmin(dataDir string, args []string) (string, error) {
	path := filepath.Join(dataDir, socketName)
	if len(path) > maxSocketPath { // ListenAdmin refuses such a folder
		return "", fmt.Errorf("qisqa is not running for data folder %s: %s is too long a path for a socket on this system", dataDir, path)
	}
	client := &http.Client{
		Timeout: adminTimeout,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, "unix", path)
			},
			DisableKeepAlives: true,
		},
	}
	body, err := json.Marshal(args)
	if err != nil {
		return "", err
	}
	response, err := client.Post("http://qisqa/", "application/json", bytes.NewReader(body))
	var netErr net.Error
	switch {
	// No socket, or a socket file nobody listens on.
	case errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ECONNREFUSED):
		return "", fmt.Errorf("qisqa is not running for data folder %s", dataDir)
	case errors.As(err, &netErr) && netErr.Timeout():
		return "", fmt.Errorf("qisqa for data folder %s did not answer within %s", dataDir, adminTimeout)
	case err != nil:
		var urlErr *url.Error
		if errors.As(err, &urlErr) {
			err = urlErr.Err // without the made-up URL
		}
		return "", fmt.Errorf("cannot reach qisqa for data folder %s: %w", dataDir, err)
	}
	defer response.Body.Close()
	var answer adminAnswer
	if err := json.NewDecoder(response.Body).Decode(&answer); err != nil {
		return "", fmt.Errorf("the answer of qisqa for data folder %s cannot be read (%s): %w", dataDir, response.Status, err)
	}
	if answer.Error != "" {
		return "", errors.New(answer.Error)
	}
	return answer.Output, nil
}

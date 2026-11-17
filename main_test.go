// Tests for the program as a whole: flags, startup errors and shutdown.
package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"
)

// started is a program started in the background by startProgram.
type started struct {
	stop   context.CancelFunc
	exit   chan int
	stderr *bytes.Buffer // read it only after the program has exited
}

// startProgram runs the program with the given config file in the
// background, as main would.
func startProgram(configPath string) *started {
	runCtx, stop := context.WithCancel(context.Background())
	p := &started{stop: stop, exit: make(chan int, 1), stderr: &bytes.Buffer{}}
	go func() { p.exit <- run(runCtx, []string{"-config", configPath}, io.Discard, p.stderr) }()
	return p
}

// exitCode waits for the program to end and returns its exit code.
func (p *started) exitCode(t *testing.T, within time.Duration) int {
	t.Helper()
	select {
	case code := <-p.exit:
		return code
	case <-time.After(within):
		t.Fatalf("the program did not exit within %s", within)
		return -1
	}
}

// waitHealthy waits until the program answers its health check at address.
// If it does not, it stops the program and fails the test with what the
// program printed.
func waitHealthy(t *testing.T, p *started, address string) {
	t.Helper()
	client := &http.Client{Timeout: 5 * time.Second}
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		response, err := client.Get("http://" + address + "/healthz")
		if err != nil {
			continue // not listening yet
		}
		body, _ := io.ReadAll(response.Body)
		response.Body.Close()
		if response.StatusCode == http.StatusOK && strings.TrimSpace(string(body)) == `{"status":"ok"}` {
			return
		}
	}
	p.stop()
	p.exitCode(t, 15*time.Second)
	t.Fatalf("the program did not become healthy; stderr: %s", p.stderr.String())
}

// freeAddress returns a local address nothing listens on.
func freeAddress(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	must(t, err)
	defer l.Close()
	return l.Addr().String()
}

// fixture is what a test of the whole program sets up before the start: a
// config file with a free address and a new data folder that already holds
// the domain short.example and an account on it.
type fixture struct {
	config, address, dataDir string
	domain                   Domain
	account                  Account
	token                    string // the account's
}

// newFixture sets up a program whose log is on, in logDir, unless logDir is
// empty. The data folder's path is short enough for the admin socket.
func newFixture(t *testing.T, logDir string) fixture {
	t.Helper()
	f := fixture{address: freeAddress(t), dataDir: filepath.Join(shortTempDir(t), "data")}
	config := "listen: \"" + f.address + "\"\ndata_dir: \"" + f.dataDir + "\"\n"
	if logDir != "" {
		config += "log:\n  enabled: true\n  dir: \"" + logDir + "\"\n"
	}
	f.config = writeFile(t, t.TempDir(), "config.yaml", config)

	must(t, os.MkdirAll(f.dataDir, 0o700))
	meta, err := OpenMeta(filepath.Join(f.dataDir, "meta.json"))
	must(t, err)
	f.domain, err = meta.AddDomain("short.example", "", nil, false)
	must(t, err)
	f.account, f.token, err = meta.AddAccount("e2e", []string{"short.example"})
	must(t, err)
	return f
}

// request sends a request for short.example, with the account's token, to
// the running program and returns the answer and its body. A redirect is an
// answer to inspect here, not to follow.
func (f fixture) request(t *testing.T, method, path, body string) (*http.Response, string) {
	t.Helper()
	r, err := http.NewRequest(method, "http://"+f.address+path, strings.NewReader(body))
	must(t, err)
	r.Host = "short.example"
	r.Header.Set("Authorization", "Bearer "+f.token)
	client := &http.Client{
		Timeout:       5 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	response, err := client.Do(r)
	must(t, err)
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	must(t, err)
	return response, string(data)
}

// createLink creates a link to url over HTTP and returns its code.
func (f fixture) createLink(t *testing.T, url string) string {
	t.Helper()
	response, body := f.request(t, "POST", "/api/links", `{"url": "`+url+`"}`)
	eq(t, response.StatusCode, http.StatusCreated)
	var link linkResponse
	must(t, json.Unmarshal([]byte(body), &link))
	return link.Code
}

// waitUpTo checks done every 20ms until it reports true, and fails the test
// with what when it has not within the given time.
func waitUpTo(t *testing.T, within time.Duration, what string, done func() bool) {
	t.Helper()
	for deadline := time.Now().Add(within); !done(); time.Sleep(20 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("%s did not happen within %s", what, within)
		}
	}
}

// TestSecondSignalEndsTheProgram starts this test binary once more as a
// program whose shutdown hangs, and asks it to stop twice. It sends TERM and
// not an interrupt: a shell that starts a program in the background makes it
// ignore interrupts, and that would say nothing about this program.
func TestSecondSignalEndsTheProgram(t *testing.T) {
	const role = "QISQA_TEST_HANGING_SHUTDOWN"
	if os.Getenv(role) != "" {
		ctx, stop := signalContext()
		defer stop()
		fmt.Println("ready")
		<-ctx.Done()
		fmt.Println("stopping")
		time.Sleep(time.Minute)
		return
	}

	program := exec.Command(os.Args[0], "-test.run=^TestSecondSignalEndsTheProgram$")
	program.Env = append(os.Environ(), role+"=1")
	stdout, err := program.StdoutPipe()
	must(t, err)
	must(t, program.Start())
	defer program.Process.Kill()

	lines := bufio.NewScanner(stdout)
	waitForLine := func(want string) {
		t.Helper()
		for lines.Scan() {
			if lines.Text() == want {
				return
			}
		}
		t.Fatalf("the program ended without printing %q", want)
	}
	waitForLine("ready")
	must(t, program.Process.Signal(syscall.SIGTERM))
	waitForLine("stopping")
	must(t, program.Process.Signal(syscall.SIGTERM))

	exited := make(chan error, 1)
	go func() { exited <- program.Wait() }()
	select {
	case <-exited:
	case <-time.After(5 * time.Second):
		t.Fatal("the program is still running after the second signal")
	}
}

func TestVersionFlag(t *testing.T) {
	var stdout, stderr bytes.Buffer
	eq(t, run(ctx, []string{"-version"}, &stdout, &stderr), 0)
	eq(t, stdout.String(), "qisqa dev\n")
	eq(t, stderr.String(), "")
}

func TestRunUnknownFlag(t *testing.T) {
	var stdout, stderr bytes.Buffer
	eq(t, run(ctx, []string{"-no-such-flag"}, &stdout, &stderr), 2)
	if !strings.Contains(stderr.String(), "no-such-flag") {
		t.Fatalf("stderr does not name the flag: %s", stderr.String())
	}
}

func TestRunMissingConfig(t *testing.T) {
	var stdout, stderr bytes.Buffer
	eq(t, run(ctx, []string{"-config", "/nonexistent.yaml"}, &stdout, &stderr), 1)
	if !strings.Contains(stderr.String(), "/nonexistent.yaml") {
		t.Fatalf("stderr does not name the file: %s", stderr.String())
	}
}

func TestRunAddressInUse(t *testing.T) {
	taken, err := net.Listen("tcp", "127.0.0.1:0")
	must(t, err)
	defer taken.Close()

	config := writeFile(t, t.TempDir(), "config.yaml", `
listen: "`+taken.Addr().String()+`"
data_dir: "`+filepath.Join(shortTempDir(t), "data")+`"
`)
	p := startProgram(config)
	defer p.stop()
	eq(t, p.exitCode(t, 10*time.Second), 1)
	if !strings.Contains(p.stderr.String(), taken.Addr().String()) {
		t.Fatalf("stderr does not name the address: %s", p.stderr.String())
	}
}

// TestRunRefusesALogFolderItCannotWrite: with the log on, a log folder that
// cannot be written is a startup error that names it.
func TestRunRefusesALogFolderItCannotWrite(t *testing.T) {
	logDir := t.TempDir()
	// A folder that is not empty, at the name of the check's file, stops
	// root too.
	must(t, os.MkdirAll(filepath.Join(logDir, ".qisqa-probe", "in the way"), 0o700))
	config := writeFile(t, t.TempDir(), "config.yaml", `
listen: "`+freeAddress(t)+`"
data_dir: "`+filepath.Join(shortTempDir(t), "data")+`"
log:
  enabled: true
  dir: "`+logDir+`"
`)
	p := startProgram(config)
	defer p.stop()
	eq(t, p.exitCode(t, 10*time.Second), 1)
	if !strings.Contains(p.stderr.String(), logDir) {
		t.Fatalf("stderr does not name the log folder: %s", p.stderr.String())
	}
}

// TestRunRefusesAFolderEveryoneCanWrite: a data or log folder that every
// user may write to is a startup error that names the folder, its mode and
// the command that fixes it. Nothing is written into the folder. A link to
// such a folder is followed to it.
func TestRunRefusesAFolderEveryoneCanWrite(t *testing.T) {
	for _, name := range []string{"data", "log", "data through a link"} {
		t.Run(name, func(t *testing.T) {
			dataDir := filepath.Join(shortTempDir(t), "data")
			logDir := filepath.Join(t.TempDir(), "logs")
			open, named := dataDir, dataDir
			switch name {
			case "log":
				open, named = logDir, logDir
			case "data through a link":
				open = filepath.Join(t.TempDir(), "open")
				must(t, os.Symlink(open, dataDir))
			}
			must(t, os.Mkdir(open, 0o700))
			must(t, os.Chmod(open, 0o777)) // past the umask
			config := writeFile(t, t.TempDir(), "config.yaml", `
listen: "`+freeAddress(t)+`"
data_dir: "`+dataDir+`"
log:
  enabled: true
  dir: "`+logDir+`"
`)
			p := startProgram(config)
			defer p.stop()
			eq(t, p.exitCode(t, 10*time.Second), 1)
			for _, want := range []string{named, "0777", "chmod o-w " + named} {
				if !strings.Contains(p.stderr.String(), want) {
					t.Errorf("stderr does not say %q: %s", want, p.stderr.String())
				}
			}
			entries, err := os.ReadDir(open)
			must(t, err)
			for _, e := range entries {
				t.Errorf("%s was written into the folder", e.Name())
			}
		})
	}
}

// TestRunWarnsAboutAFolderItsGroupCanWrite: a data or log folder its group
// may write to is warned about once, and the program starts.
func TestRunWarnsAboutAFolderItsGroupCanWrite(t *testing.T) {
	for _, name := range []string{"data", "log"} {
		t.Run(name, func(t *testing.T) {
			logDir := filepath.Join(t.TempDir(), "logs")
			f := newFixture(t, logDir)
			open := f.dataDir
			if name == "log" {
				open = logDir
				must(t, os.Mkdir(logDir, 0o700))
			}
			must(t, os.Chmod(open, 0o770))
			p := startProgram(f.config)
			defer p.stop()
			waitHealthy(t, p, f.address)
			p.stop()
			eq(t, p.exitCode(t, 15*time.Second), 0)

			var warnings []string
			for line := range strings.Lines(p.stderr.String()) {
				if strings.Contains(line, "level=WARN") {
					warnings = append(warnings, line)
				}
			}
			if len(warnings) != 1 || !strings.Contains(warnings[0], open) {
				t.Errorf("want one warning naming %s; stderr: %s", open, p.stderr.String())
			}
		})
	}
}

func TestRunServesAndShutsDownCleanly(t *testing.T) {
	logDir := filepath.Join(t.TempDir(), "logs")
	f := newFixture(t, logDir)
	p := startProgram(f.config)
	defer p.stop()
	waitHealthy(t, p, f.address)

	code := f.createLink(t, "https://example.com/e2e")
	response, _ := f.request(t, "GET", "/"+code, "")
	eq(t, response.StatusCode, http.StatusFound)
	eq(t, response.Header.Get("Location"), "https://example.com/e2e")

	p.stop()
	eq(t, p.exitCode(t, 15*time.Second), 0)

	// A clean shutdown lets go of the data folder, and the link is there.
	st, err := OpenStore(f.dataDir)
	must(t, err)
	defer st.Close()
	stored, state, err := st.Lookup(f.domain.ID, code)
	must(t, err)
	eq(t, state, LinkFound)
	eq(t, stored.URL, "https://example.com/e2e")

	// The create is the access log's only line; the redirect is not logged.
	files, err := os.ReadDir(logDir)
	must(t, err)
	eq(t, len(files), 1)
	eq(t, strings.HasPrefix(files[0].Name(), "access-"), true)
	lines := withoutTime(t, logLines(t, logDir, files[0].Name()))
	eq(t, len(lines), 1)
	eq(t, lines[0], fmt.Sprintf("create\tshort.example\t%s\t127.0.0.1\t%d", code, f.account.ID))

	for _, want := range []string{"qisqa started", "qisqa stopped"} {
		if !strings.Contains(p.stderr.String(), want) {
			t.Errorf("stderr does not say %q: %s", want, p.stderr.String())
		}
	}
	if strings.Contains(p.stderr.String(), f.token) {
		t.Error("stderr contains the API token")
	}
}

// TestSecondInstanceRefusesToStart: a second program on the same data
// folder stops at the folder's lock and says so; the first one, and its
// admin socket, go on as before.
func TestSecondInstanceRefusesToStart(t *testing.T) {
	f := newFixture(t, "")
	first := startProgram(f.config)
	defer first.stop()
	waitHealthy(t, first, f.address)

	other := writeFile(t, t.TempDir(), "config.yaml", "listen: \""+freeAddress(t)+"\"\ndata_dir: \""+f.dataDir+"\"\n")
	second := startProgram(other)
	defer second.stop()
	eq(t, second.exitCode(t, 10*time.Second), 1)

	waitHealthy(t, first, f.address)
	_, err := SendAdmin(f.dataDir, []string{"domain", "list"})
	must(t, err)
	first.stop()
	eq(t, first.exitCode(t, 15*time.Second), 0)
	// Read only now that the first has ended too: the log is the whole
	// process's, and the second start took it over while the first ran.
	if !strings.Contains(second.stderr.String(), f.dataDir) {
		t.Errorf("stderr does not name the data folder: %s", second.stderr.String())
	}
}

// TestRemovedFileStopsItsLinks: a chunk file removed while the program runs
// is noticed within 10 seconds, and its links stop then, cached or not.
func TestRemovedFileStopsItsLinks(t *testing.T) {
	f := newFixture(t, "")
	p := startProgram(f.config)
	defer p.stop()
	waitHealthy(t, p, f.address)

	code := f.createLink(t, "https://example.com/removed")
	response, _ := f.request(t, "GET", "/"+code, "") // the link is cached now
	eq(t, response.StatusCode, http.StatusFound)

	must(t, os.Remove(firstChunk(f.dataDir)))
	waitUpTo(t, 15*time.Second, "a 404 for the link", func() bool {
		response, _ := f.request(t, "GET", "/"+code, "")
		return response.StatusCode == http.StatusNotFound
	})
	p.stop()
	eq(t, p.exitCode(t, 15*time.Second), 0)
}

// TestShutdownRemovesSocket: a clean stop removes the admin socket, and the
// folder is ready for the next start. Clients of the socket that never
// finish get a short grace, then are dropped: they do not hold up the stop,
// and dropping them is no failure.
func TestShutdownRemovesSocket(t *testing.T) {
	f := newFixture(t, "")
	p := startProgram(f.config)
	defer p.stop()
	waitHealthy(t, p, f.address)
	holdAdminClients(t, f.dataDir)

	stopping := time.Now()
	p.stop()
	eq(t, p.exitCode(t, 15*time.Second), 0)
	if took := time.Since(stopping); took > 4*time.Second {
		t.Errorf("the stop took %s", took.Round(time.Millisecond))
	}
	if strings.Contains(p.stderr.String(), "failed") {
		t.Errorf("a clean stop logged a failure: %s", p.stderr.String())
	}
	if _, err := os.Lstat(filepath.Join(f.dataDir, "qisqa.sock")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("the socket file is still there: %v", err)
	}

	again := startProgram(f.config)
	defer again.stop()
	waitHealthy(t, again, f.address)
	_, err := SendAdmin(f.dataDir, []string{"domain", "list"})
	must(t, err)
	again.stop()
	eq(t, again.exitCode(t, 15*time.Second), 0)
}

// TestDiskFailureStopsTheProgram: a failed sync of a chunk file, the disk
// itself failing, ends the program with status 1 by the same shutdown as a
// signal, so that whatever restarts it starts again from what is on disk.
// No code is handed out twice.
func TestDiskFailureStopsTheProgram(t *testing.T) {
	f := newFixture(t, "")
	p := startProgram(f.config)
	defer p.stop()
	waitHealthy(t, p, f.address)
	before := f.createLink(t, "https://example.com/before")
	p.stop()
	eq(t, p.exitCode(t, 15*time.Second), 0)

	// The next program's store fails every sync of a chunk file, as a disk
	// that is failing does.
	failure := errors.New("input/output error")
	openStore = func(dir string) (*Store, error) {
		st, err := OpenStore(dir)
		if err == nil {
			st.failSync = failure
		}
		return st, err
	}
	t.Cleanup(func() { openStore = OpenStore })
	failing := startProgram(f.config)
	defer failing.stop()
	waitHealthy(t, failing, f.address)
	response, _ := f.request(t, "POST", "/api/links", `{"url": "https://example.com/lost"}`)
	eq(t, response.StatusCode, http.StatusInternalServerError)
	eq(t, failing.exitCode(t, 15*time.Second), 1)
	openStore = OpenStore
	stderr := failing.stderr.String()
	for _, want := range []string{firstChunk(f.dataDir), failure.Error(), "qisqa failed"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("stderr does not say %q: %s", want, stderr)
		}
	}

	again := startProgram(f.config)
	defer again.stop()
	waitHealthy(t, again, f.address)
	if after := f.createLink(t, "https://example.com/after"); after == before {
		t.Errorf("code %s was handed out twice", after)
	}
	response, _ = f.request(t, "GET", "/"+before, "")
	eq(t, response.StatusCode, http.StatusFound)
	eq(t, response.Header.Get("Location"), "https://example.com/before")
	again.stop()
	eq(t, again.exitCode(t, 15*time.Second), 0)
}

// TestRunAnswersBeforeTheSyncWithSyncEvery: with sync_every set, a create
// through the API is answered before its chunk file is synced, and a clean
// stop syncs it.
func TestRunAnswersBeforeTheSyncWithSyncEvery(t *testing.T) {
	f := newFixture(t, "")
	f.config = writeFile(t, t.TempDir(), "config.yaml", readDoc(t, f.config)+"sync_every: 1m\n")
	opened := make(chan *Store, 1)
	openStore = func(dir string) (*Store, error) {
		st, err := OpenStore(dir)
		if err == nil {
			opened <- st
		}
		return st, err
	}
	t.Cleanup(func() { openStore = OpenStore })
	p := startProgram(f.config)
	defer p.stop()
	st := <-opened
	waitHealthy(t, p, f.address)
	code := f.createLink(t, "https://example.com/at-once")
	eq(t, st.syncs(), int64(1)) // the new chunk file's head, synced where it was created
	p.stop()
	eq(t, p.exitCode(t, 15*time.Second), 0)
	openStore = OpenStore
	eq(t, st.syncs(), int64(2))

	st, err := OpenStore(f.dataDir)
	must(t, err)
	defer st.Close()
	stored, state, err := st.Lookup(f.domain.ID, code)
	must(t, err)
	eq(t, state, LinkFound)
	eq(t, stored.URL, "https://example.com/at-once")
}

// TestLastSyncFailingAtAStopEndsWithAnError: with sync_every set, the last
// pass, the one a stop in order asks for, fails. The program ends with
// status 1, and the failure reaches the error log as well as stderr.
func TestLastSyncFailingAtAStopEndsWithAnError(t *testing.T) {
	logDir := filepath.Join(t.TempDir(), "logs")
	f := newFixture(t, logDir)
	f.config = writeFile(t, t.TempDir(), "config.yaml", readDoc(t, f.config)+"sync_every: 1m\n")
	opened := make(chan *Store, 1)
	openStore = func(dir string) (*Store, error) {
		st, err := OpenStore(dir)
		if err == nil {
			opened <- st
		}
		return st, err
	}
	t.Cleanup(func() { openStore = OpenStore })
	p := startProgram(f.config)
	defer p.stop()
	st := <-opened
	waitHealthy(t, p, f.address)
	f.createLink(t, "https://example.com/answered")
	failure := errors.New("input/output error")
	st.failSync = failure // read by the last pass, which the stop asks for
	p.stop()
	if code := p.exitCode(t, 15*time.Second); code != 1 {
		t.Errorf("the program ended with status %d, want 1", code)
	}
	openStore = OpenStore

	const failed = "a sync of a chunk file failed"
	stderr := p.stderr.String()
	for _, want := range []string{failed, failure.Error(), "qisqa failed"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("stderr does not say %q: %s", want, stderr)
		}
	}
	errorLogs, err := filepath.Glob(filepath.Join(logDir, "error-*.log"))
	must(t, err)
	var logged string
	for _, path := range errorLogs {
		logged += readDoc(t, path)
	}
	if !strings.Contains(logged, failed) || !strings.Contains(logged, failure.Error()) {
		t.Errorf("the error log does not hold the failed sync:\n%s", logged)
	}
}

// TestWarningsReachTheErrorLog: the program's warnings and errors go to the
// error log as well as to stderr; its other lines go to stderr only.
func TestWarningsReachTheErrorLog(t *testing.T) {
	logDir := filepath.Join(t.TempDir(), "logs")
	f := newFixture(t, logDir)
	p := startProgram(f.config)
	defer p.stop()
	waitHealthy(t, p, f.address)
	f.createLink(t, "https://example.com/warned")

	// The newest chunk file, where new links go, is lost: an error.
	must(t, os.Remove(firstChunk(f.dataDir)))
	var errorLog string // the folder is new: its only error file is today's
	waitUpTo(t, 15*time.Second, "a line in the error log", func() bool {
		files, err := filepath.Glob(filepath.Join(logDir, "error-*.log"))
		must(t, err)
		if len(files) == 0 {
			return false
		}
		errorLog = files[0]
		data, err := os.ReadFile(errorLog)
		must(t, err)
		return strings.HasSuffix(string(data), "\n")
	})
	p.stop()
	eq(t, p.exitCode(t, 15*time.Second), 0)

	lines := withoutTime(t, logLines(t, logDir, filepath.Base(errorLog)))
	if len(lines) != 1 {
		t.Fatalf("the error log holds %d lines, want the one error:\n%s", len(lines), strings.Join(lines, "\n"))
	}
	level, message, _ := strings.Cut(lines[0], "\t")
	message, _, _ = strings.Cut(message, "\t")
	eq(t, level, "ERROR")
	if !strings.Contains(message, "chunk file") || !strings.Contains(p.stderr.String(), message) {
		t.Errorf("stderr does not say %q: %s", message, p.stderr.String())
	}
}

// TestOldLogsAreTidiedAtStartup: the log folder is tidied when the program
// starts, not only an hour later.
func TestOldLogsAreTidiedAtStartup(t *testing.T) {
	logDir := t.TempDir()
	old := writeFile(t, logDir, "access-"+time.Now().UTC().AddDate(0, 0, -10).Format(dayLayout)+".log",
		"12:00:00\tcreate\tshort.example\taB3xK\t127.0.0.1\t1\n")
	f := newFixture(t, logDir)
	p := startProgram(f.config)
	defer p.stop()
	waitHealthy(t, p, f.address)

	waitUpTo(t, 5*time.Second, "the compression of "+old, func() bool {
		_, textErr := os.Lstat(old)
		_, archiveErr := os.Lstat(old + ".gz")
		return errors.Is(textErr, fs.ErrNotExist) && archiveErr == nil
	})
	p.stop()
	eq(t, p.exitCode(t, 15*time.Second), 0)
}

// TestRunAdminCommand: with words after the flags, the program sends them
// to the running server, prints its answer and exits.
func TestRunAdminCommand(t *testing.T) {
	address := freeAddress(t)
	dataDir := filepath.Join(shortTempDir(t), "data")
	config := writeFile(t, t.TempDir(), "config.yaml", `
listen: "`+address+`"
data_dir: "`+dataDir+`"
`)
	admin := func(args ...string) (code int, stdout, stderr string) {
		t.Helper()
		var out, errOut bytes.Buffer
		code = run(ctx, args, &out, &errOut)
		return code, out.String(), errOut.String()
	}
	client := &http.Client{Timeout: 5 * time.Second}
	// createLink creates a link on short.example with token and returns the
	// status.
	createLink := func(token string) int {
		t.Helper()
		r, err := http.NewRequest("POST", "http://"+address+"/api/links", strings.NewReader(`{"url": "https://example.com/admin"}`))
		must(t, err)
		r.Host = "short.example"
		r.Header.Set("Authorization", "Bearer "+token)
		response, err := client.Do(r)
		must(t, err)
		response.Body.Close()
		return response.StatusCode
	}

	p := startProgram(config)
	defer p.stop()
	waitHealthy(t, p, address)

	code, stdout, stderr := admin("-config", config, "domain", "add", "short.example")
	eq(t, code, 0)
	eq(t, stdout, "added domain 1  short.example\n")
	eq(t, stderr, "")

	code, stdout, _ = admin("-config", config, "account", "add", "a", "--domain", "short.example")
	eq(t, code, 0)
	eq(t, tokenPattern.ReplaceAllString(stdout, "<token>"), "added account 1  a\ntoken: <token>\nKeep the token: it is not shown again.\n")
	eq(t, createLink(tokenPattern.FindString(stdout)), http.StatusCreated)

	// -data names the folder instead of the config, and wins over it.
	code, stdout, _ = admin("-data", dataDir, "account", "add", "b", "--domain", "short.example")
	eq(t, code, 0)
	eq(t, createLink(tokenPattern.FindString(stdout)), http.StatusCreated)
	code, stdout, _ = admin("-config", "/nonexistent.yaml", "-data", dataDir, "account", "list")
	eq(t, code, 0)
	eq(t, stdout, "1  a  short.example  enabled\n2  b  short.example  enabled\n")

	// An empty answer prints nothing; an error goes to stderr.
	code, stdout, _ = admin("-data", dataDir, "block", "list")
	eq(t, code, 0)
	eq(t, stdout, "")
	code, stdout, stderr = admin("-data", dataDir, "account", "disable", "99")
	eq(t, code, 1)
	eq(t, stdout, "")
	eq(t, stderr, "there is no account 99\n")

	p.stop()
	eq(t, p.exitCode(t, 15*time.Second), 0)
	// The socket is there only while the program runs.
	if _, err := os.Lstat(filepath.Join(dataDir, "qisqa.sock")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("the socket file is still there: %v", err)
	}
	code, stdout, stderr = admin("-config", config, "account", "list")
	eq(t, code, 1)
	eq(t, stdout, "")
	if !strings.Contains(stderr, "not running") {
		t.Errorf("stderr does not say the program is not running: %s", stderr)
	}
}

// TestRunAdminCommandOpensNoData: an admin command never opens the data
// folder itself; without a server it fails and creates nothing.
func TestRunAdminCommandOpensNoData(t *testing.T) {
	dataDir := filepath.Join(shortTempDir(t), "data")
	var stdout, stderr bytes.Buffer
	eq(t, run(ctx, []string{"-data", dataDir, "domain", "add", "short.example"}, &stdout, &stderr), 1)
	if !strings.Contains(stderr.String(), "not running") {
		t.Errorf("stderr does not say the program is not running: %s", stderr.String())
	}
	if _, err := os.Lstat(dataDir); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("the command created the data folder: %v", err)
	}
}

// TestRunSkip: -skip adds to the skip of a data folder that is there and
// that no qisqa holds, and exits; it never starts a server.
func TestRunSkip(t *testing.T) {
	skip := func(args ...string) (code int, stdout, stderr string) {
		t.Helper()
		// A server would serve until the context ends; run then returns late.
		runCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		var out, errOut bytes.Buffer
		code = run(runCtx, args, &out, &errOut)
		if runCtx.Err() != nil {
			t.Fatalf("%q did not return: it started a server; stderr: %s", args, errOut.String())
		}
		return code, out.String(), errOut.String()
	}
	// dataFolder returns a data folder qisqa has used, as a copy put back is.
	dataFolder := func() string {
		t.Helper()
		dir := filepath.Join(t.TempDir(), "data")
		st, err := OpenStore(dir)
		must(t, err)
		must(t, st.Close())
		return dir
	}
	// nothingWritten fails the test if dir holds more than the names given.
	nothingWritten := func(dir string, names ...string) {
		t.Helper()
		entries, err := os.ReadDir(dir)
		must(t, err)
		for _, e := range entries {
			if !slices.Contains(names, e.Name()) {
				t.Errorf("%s was written into %s", e.Name(), dir)
			}
		}
	}
	dataDir := dataFolder()
	skipped := filepath.Join(dataDir, "skipped")
	holds := func(want string) {
		t.Helper()
		b, err := os.ReadFile(skipped)
		must(t, err)
		eq(t, string(b), want)
	}
	says := func(output string, words ...string) {
		t.Helper()
		for _, w := range words {
			if !strings.Contains(output, w) {
				t.Errorf("%q does not say %q", output, w)
			}
		}
	}

	code, stdout, stderr := skip("-data", dataDir, "-skip", "500")
	eq(t, code, 0)
	eq(t, stderr, "")
	eq(t, strings.Count(stdout, "\n"), 1)
	says(stdout, "500", skipped)
	holds("500\n")
	code, stdout, _ = skip("-data", dataDir, "-skip", "500")
	eq(t, code, 0)
	says(stdout, "500", "1000", skipped)
	holds("1000\n")
	// Without -data the folder is the config's data_dir.
	config := writeFile(t, t.TempDir(), "config.yaml", "data_dir: \""+dataDir+"\"\n")
	code, stdout, _ = skip("-config", config, "-skip", "1")
	eq(t, code, 0)
	says(stdout, "1001", skipped)
	holds("1001\n")

	st, err := OpenStore(dataDir)
	must(t, err)
	code, _, stderr = skip("-data", dataDir, "-skip", "500")
	eq(t, code, 1)
	says(stderr, "in use by another qisqa process")
	holds("1001\n")
	must(t, st.Close())

	fresh := filepath.Join(t.TempDir(), "fresh")
	must(t, os.Mkdir(fresh, 0o700))
	for _, args := range [][]string{
		{"-data", fresh, "-skip", "0"},
		{"-data", fresh, "-skip", "x"},
		{"-data", fresh, "-skip", "-5"},
		{"-data", fresh, "-skip", "5", "domain", "list"},
		{"-config", config, "-skip", "0"},
	} {
		code, _, stderr := skip(args...)
		eq(t, code, 2)
		says(stderr, "-skip")
	}
	nothingWritten(fresh)
	holds("1001\n")

	// The folder is the copy put back: one that is not there is not made,
	// and one without key is no copy, such as a new volume or the data
	// folder of another working folder.
	missing := filepath.Join(t.TempDir(), "missing")
	code, _, stderr = skip("-data", missing, "-skip", "500")
	eq(t, code, 1)
	says(stderr, missing)
	if _, err := os.Lstat(missing); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("-skip made the folder %s: %v", missing, err)
	}
	code, stdout, stderr = skip("-data", fresh, "-skip", "500")
	eq(t, code, 1)
	eq(t, stdout, "")
	says(stderr, fresh, "key", "put the copy back", "-config", "-data")
	nothingWritten(fresh)

	// The folder is looked at as the server looks at it.
	open := dataFolder()
	must(t, os.Chmod(open, 0o777)) // past the umask
	code, _, stderr = skip("-data", open, "-skip", "500")
	eq(t, code, 1)
	says(stderr, "chmod o-w "+open)
	nothingWritten(open, "LOCK", "key")
	must(t, os.Chmod(open, 0o770))
	code, _, stderr = skip("-data", open, "-skip", "500")
	eq(t, code, 0)
	says(stderr, "chmod g-w", open)
}

// TestRunDataFlagNeedsACommand: -data names the folder of an admin command;
// the server takes its folder from the config only.
func TestRunDataFlagNeedsACommand(t *testing.T) {
	var stdout, stderr bytes.Buffer
	eq(t, run(ctx, []string{"-data", t.TempDir()}, &stdout, &stderr), 2)
	if !strings.Contains(stderr.String(), "-data names the folder of an admin command") {
		t.Errorf("stderr does not explain -data: %s", stderr.String())
	}
}

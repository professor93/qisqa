// Tests for reloading the config file while the program runs.
package app

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// watchYAML is the file a watcher starts from; changedYAML changes a key
// that applies without a restart.
const (
	watchYAML   = minimalYAML + "min_length: 6\n"
	changedYAML = minimalYAML + "min_length: 7\n"
)

// startWatcher loads yaml from a temporary file, with the pages it names,
// and runs a watcher on it that polls every 10ms until the test ends.
func startWatcher(t *testing.T, yaml string) (*Watcher, string) {
	t.Helper()
	path := writeFile(t, t.TempDir(), "config.yaml", yaml)
	cfg, err := LoadConfig(path)
	must(t, err)
	w := NewWatcher(path, cfg)
	must(t, w.LoadPages())
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go w.Run(ctx, 10*time.Millisecond)
	return w, path
}

var rewrites atomic.Int64

// rewrite replaces the config file and moves its modification time forward,
// so every rewrite is visible even on file systems with coarse timestamps.
func rewrite(t *testing.T, path, yaml string) {
	t.Helper()
	must(t, os.WriteFile(path, []byte(yaml), 0o644))
	stamp := time.Now().Add(time.Duration(rewrites.Add(1)) * time.Minute)
	must(t, os.Chtimes(path, stamp, stamp))
}

// waitFor polls cond for up to one second.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// minLengthIs reports whether the live config has this min_length.
func minLengthIs(w *Watcher, n int) func() bool {
	return func() bool { return w.Config().MinLength == n }
}

func TestWatcherReloadsValidChange(t *testing.T) {
	w, path := startWatcher(t, watchYAML)
	rewrite(t, path, changedYAML)
	waitFor(t, "the new min_length", minLengthIs(w, 7))
}

func TestWatcherKeepsOldOnInvalid(t *testing.T) {
	w, path := startWatcher(t, watchYAML)
	before := w.Config()

	rewrite(t, path, "blacklist: [")
	time.Sleep(200 * time.Millisecond)
	if w.Config() != before {
		t.Fatal("a broken file replaced the running config")
	}

	rewrite(t, path, changedYAML)
	waitFor(t, "the repaired file to load", minLengthIs(w, 7))
}

func TestWatcherKeepsOldWhenFileMissing(t *testing.T) {
	w, path := startWatcher(t, watchYAML)
	before := w.Config()

	must(t, os.Remove(path))
	time.Sleep(200 * time.Millisecond)
	if w.Config() != before {
		t.Fatal("a missing file replaced the running config")
	}
}

func TestWatcherKeepsRestartOnlyFields(t *testing.T) {
	captureLog(t) // the warning that the changes apply after a restart
	w, path := startWatcher(t, watchYAML)
	rewrite(t, path, `listen: ":9999"
data_dir: /elsewhere
sync_every: 1s
min_length: 7
cache:
  max_entries: 5
log:
  enabled: true
`)
	waitFor(t, "the new min_length", minLengthIs(w, 7))

	cfg := w.Config()
	eq(t, cfg.Listen, ":8080")
	eq(t, cfg.DataDir, "./data")
	eq(t, cfg.SyncEvery.D(), time.Duration(0))
	eq(t, cfg.Cache.MaxEntries, 10000)
	eq(t, cfg.Log.Enabled, false)
}

// A change of sync_every, read at startup only, is one the warning names.
func TestSyncEveryChangeNeedsARestart(t *testing.T) {
	old, err := loadYAML(t, minimalYAML)
	must(t, err)
	next, err := loadYAML(t, minimalYAML+"sync_every: 1s\n")
	must(t, err)
	eq(t, needsRestart(old, next), true)
}

func TestWatcherWithoutPathReturnsAtOnce(t *testing.T) {
	cfg, err := loadYAML(t, watchYAML)
	must(t, err)
	done := make(chan struct{})
	go func() {
		NewWatcher("", cfg).Run(context.Background(), time.Hour)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Run with an empty path did not return")
	}
}

func TestWatcherSet(t *testing.T) {
	first, err := loadYAML(t, watchYAML)
	must(t, err)
	second, err := loadYAML(t, minimalYAML)
	must(t, err)

	w := NewWatcher("", first)
	eq(t, w.Config(), first)
	w.set(second)
	eq(t, w.Config(), second)
}

// pagesYAML is a config whose pages are in the folder dir.
func pagesYAML(dir string) string {
	return minimalYAML + fmt.Sprintf("pages:\n  dir: %q\n", dir)
}

// pageIs reports whether the live page of that name is content; an empty
// content means there is none.
func pageIs(w *Watcher, name, content string) func() bool {
	return func() bool {
		page := w.Pages().page(name)
		return string(page) == content && (content != "" || page == nil)
	}
}

// replaceFile puts content in the place of the file at path in one step,
// so that no poll sees a file that is half written.
func replaceFile(t *testing.T, path, content string) {
	t.Helper()
	fresh := writeFile(t, t.TempDir(), "fresh", content)
	must(t, os.Rename(fresh, path))
}

// watchedLog collects what is logged while a test runs; the watcher writes
// to it from its own goroutine.
type watchedLog struct {
	mu   sync.Mutex
	text strings.Builder
}

func (l *watchedLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.text.Write(p)
}

// count returns how many lines hold the fragment.
func (l *watchedLog) count(fragment string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return strings.Count(l.text.String(), fragment)
}

// watchLog sends the log to a watchedLog until the test ends.
func watchLog(t *testing.T) *watchedLog {
	t.Helper()
	old := slog.Default()
	log := &watchedLog{}
	slog.SetDefault(slog.New(slog.NewTextHandler(log, nil)))
	t.Cleanup(func() { slog.SetDefault(old) })
	return log
}

func TestWatcherLoadsPagesAtStart(t *testing.T) {
	dir := pagesFolder(t, map[string]string{"404.html": "own 404"})
	w, _ := startWatcher(t, pagesYAML(dir))
	eq(t, string(w.Pages().page("404")), "own 404")

	// A watcher whose pages were never loaded has none.
	if page := NewWatcher("", testConfig()).Pages().page("404"); page != nil {
		t.Fatalf("a new watcher has the page %q", page)
	}
}

// pagesFailed is what the log says when the pages did not load.
const pagesFailed = "own pages did not load"

// TestWatcherStartsWithABrokenPage: at start a page that cannot be a page
// is said once in the log, there are no pages, and when it is repaired the
// pages load.
func TestWatcherStartsWithABrokenPage(t *testing.T) {
	log := watchLog(t)
	dir := pagesFolder(t, map[string]string{"index.html": "home"})
	must(t, os.Mkdir(filepath.Join(dir, "404.html"), 0o755))
	path := writeFile(t, t.TempDir(), "config.yaml", pagesYAML(dir))
	cfg, err := LoadConfig(path)
	must(t, err)
	w := NewWatcher(path, cfg)
	err = w.LoadPages()
	if err == nil || !strings.Contains(err.Error(), "404.html") {
		t.Fatalf("LoadPages gave %v, want an error that names 404.html", err)
	}
	eq(t, log.count("level=ERROR"), 1)
	eq(t, log.count(pagesFailed), 1)
	if !pageIs(w, "index", "")() || !pageIs(w, "404", "")() {
		t.Fatal("there are pages though one of them is broken")
	}

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go w.Run(ctx, 10*time.Millisecond)
	must(t, os.Remove(filepath.Join(dir, "404.html")))
	waitFor(t, "the pages after the repair", pageIs(w, "index", "home"))
	eq(t, log.count("level=ERROR"), 1)
}

// TestWatcherTriesAgainWhileThePagesAreBroken: a repair that changes
// neither the time nor the size of a file is seen too, and while the pages
// are broken the error is logged once, not at every try.
func TestWatcherTriesAgainWhileThePagesAreBroken(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads every file")
	}
	log := watchLog(t)
	dir := pagesFolder(t, map[string]string{"404.html": "good"})
	w, _ := startWatcher(t, pagesYAML(dir))

	// A new page that qisqa may not read takes the place of the good one.
	fresh := writeFile(t, t.TempDir(), "fresh", "repaired")
	must(t, os.Chmod(fresh, 0o000))
	must(t, os.Rename(fresh, filepath.Join(dir, "404.html")))
	waitFor(t, "the error in the log", func() bool { return log.count(pagesFailed) > 0 })
	time.Sleep(100 * time.Millisecond) // ten tries
	eq(t, log.count("level=ERROR"), 1)
	eq(t, string(w.Pages().page("404")), "good")

	must(t, os.Chmod(filepath.Join(dir, "404.html"), 0o644))
	waitFor(t, "the page after chmod", pageIs(w, "404", "repaired"))
	eq(t, log.count("level=ERROR"), 1)
}

// TestWatcherLetsGoOfPagesWhenTheFolderIsRepaired: the config names a
// folder that is a file, so the pages in use stay; when an empty folder
// takes its place, which no stamp shows, the built-in pages come back.
func TestWatcherLetsGoOfPagesWhenTheFolderIsRepaired(t *testing.T) {
	log := watchLog(t)
	good := pagesFolder(t, map[string]string{"404.html": "good"})
	wrong := writeFile(t, t.TempDir(), "pages", "a file, not a folder")
	w, path := startWatcher(t, pagesYAML(good))

	rewrite(t, path, pagesYAML(wrong))
	waitFor(t, "the error in the log", func() bool { return log.count(pagesFailed) > 0 })
	eq(t, string(w.Pages().page("404")), "good")

	must(t, os.Remove(wrong))
	must(t, os.Mkdir(wrong, 0o755))
	waitFor(t, "the built-in pages", pageIs(w, "404", ""))
}

// TestWatcherSaysLittleOfPages: a path that names no file is noted once,
// not at every poll, and a config without pages says nothing of pages.
func TestWatcherSaysLittleOfPages(t *testing.T) {
	log := watchLog(t)
	w, path := startWatcher(t, minimalYAML+"min_length: 6\n")
	rewrite(t, path, minimalYAML+"min_length: 7\n")
	waitFor(t, "the changed config", minLengthIs(w, 7))
	time.Sleep(50 * time.Millisecond)
	eq(t, log.count("pages"), 0)

	missing := filepath.Join(t.TempDir(), "none.html")
	rewrite(t, path, minimalYAML+fmt.Sprintf("pages:\n  files:\n    404: %q\n", missing))
	waitFor(t, "the note of the missing file", func() bool { return log.count("level=WARN") > 0 })
	time.Sleep(100 * time.Millisecond)
	eq(t, log.count("level=WARN"), 1)
	eq(t, log.count("none.html"), 1)
}

// TestWatcherReloadsAChangedPage: a page file that changes, goes away or
// appears is seen without a restart and without a change of the config.
func TestWatcherReloadsAChangedPage(t *testing.T) {
	dir := pagesFolder(t, map[string]string{"404.html": "first"})
	w, _ := startWatcher(t, pagesYAML(dir))
	eq(t, string(w.Pages().page("404")), "first")

	replaceFile(t, filepath.Join(dir, "404.html"), "second, longer")
	waitFor(t, "the changed page", pageIs(w, "404", "second, longer"))

	must(t, os.Remove(filepath.Join(dir, "404.html")))
	waitFor(t, "the built-in page to come back", pageIs(w, "404", ""))

	replaceFile(t, filepath.Join(dir, "index.html"), "home")
	waitFor(t, "the page that appeared", pageIs(w, "index", "home"))
}

// TestWatcherKeepsPagesWhenAPageBreaks: a page file that turns into
// something that is no page leaves the running pages, and is logged once,
// not at every poll.
func TestWatcherKeepsPagesWhenAPageBreaks(t *testing.T) {
	log := watchLog(t)
	dir := pagesFolder(t, map[string]string{"404.html": "good"})
	w, _ := startWatcher(t, pagesYAML(dir))

	replaceFile(t, filepath.Join(dir, "404.html"), strings.Repeat("a", maxPageSize+1))
	waitFor(t, "the error in the log", func() bool { return log.count("level=ERROR") > 0 })
	time.Sleep(100 * time.Millisecond) // ten polls
	eq(t, log.count("level=ERROR"), 1)
	eq(t, log.count(pagesFailed), 1)
	eq(t, string(w.Pages().page("404")), "good")

	replaceFile(t, filepath.Join(dir, "404.html"), "repaired")
	waitFor(t, "the repaired page", pageIs(w, "404", "repaired"))
}

func TestWatcherReloadsPagesWithTheConfig(t *testing.T) {
	first := pagesFolder(t, map[string]string{"404.html": "first folder"})
	second := pagesFolder(t, map[string]string{"404.html": "second folder"})
	w, path := startWatcher(t, pagesYAML(first))
	eq(t, string(w.Pages().page("404")), "first folder")

	rewrite(t, path, pagesYAML(second))
	waitFor(t, "the pages of the second folder", pageIs(w, "404", "second folder"))

	rewrite(t, path, minimalYAML)
	waitFor(t, "the built-in pages", pageIs(w, "404", ""))
}

// TestWatcherAppliesAConfigWhosePagesAreBroken: the config changes, the
// running pages stay, and when the page is repaired it loads without
// another change of the config.
func TestWatcherAppliesAConfigWhosePagesAreBroken(t *testing.T) {
	log := watchLog(t)
	good := pagesFolder(t, map[string]string{"404.html": "good"})
	broken := t.TempDir()
	must(t, os.Mkdir(filepath.Join(broken, "404.html"), 0o755))
	w, path := startWatcher(t, pagesYAML(good)+"min_length: 6\n")

	rewrite(t, path, pagesYAML(broken)+"min_length: 7\n")
	waitFor(t, "the error in the log", func() bool { return log.count(pagesFailed) > 0 })
	eq(t, w.Config().MinLength, 7)
	eq(t, string(w.Pages().page("404")), "good")

	must(t, os.Remove(filepath.Join(broken, "404.html")))
	replaceFile(t, filepath.Join(broken, "404.html"), "repaired")
	waitFor(t, "the repaired page", pageIs(w, "404", "repaired"))
}

// Tests for reloading the config file while the program runs.
package main

import (
	"context"
	"os"
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

// startWatcher loads yaml from a temporary file and runs a watcher on it
// that polls every 10ms until the test ends.
func startWatcher(t *testing.T, yaml string) (*Watcher, string) {
	t.Helper()
	path := writeFile(t, t.TempDir(), "config.yaml", yaml)
	cfg, err := LoadConfig(path)
	must(t, err)
	w := NewWatcher(path, cfg)
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

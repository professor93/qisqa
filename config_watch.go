// Hot reload: keeps the live config and re-reads the file when it changes.
package main

import (
	"context"
	"log/slog"
	"os"
	"sync/atomic"
	"time"
)

// Watcher holds the config every request reads. Run replaces it when the
// file changes; a file that fails to load never replaces a working config.
type Watcher struct {
	path    string
	current atomic.Pointer[Config]
	last    fileStamp
}

// fileStamp identifies one version of the config file. The zero value means
// the file could not be read.
type fileStamp struct {
	modified time.Time
	size     int64
}

// stampOf returns the current stamp of the file at path.
func stampOf(path string) fileStamp {
	info, err := os.Stat(path)
	if err != nil {
		return fileStamp{}
	}
	return fileStamp{modified: info.ModTime(), size: info.Size()}
}

// NewWatcher returns a watcher serving initial, which was loaded from path.
// An empty path means the config never reloads.
func NewWatcher(path string, initial *Config) *Watcher {
	w := &Watcher{path: path}
	if path != "" {
		w.last = stampOf(path)
	}
	w.current.Store(initial)
	return w
}

// Config returns the live config. Callers must not modify it.
func (w *Watcher) Config() *Config {
	return w.current.Load()
}

// set replaces the live config.
func (w *Watcher) set(c *Config) {
	w.current.Store(c)
}

// Run checks the file every interval until ctx ends and reloads it when its
// modification time or size changed.
func (w *Watcher) Run(ctx context.Context, every time.Duration) {
	if w.path == "" {
		return
	}
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		if stamp := stampOf(w.path); stamp != w.last {
			w.last = stamp
			w.reload()
		}
	}
}

// reload loads the file and makes it live. Settings that are only read at
// startup keep their running values, so the live config always describes
// what the process is really doing.
func (w *Watcher) reload() {
	next, err := LoadConfig(w.path)
	if err != nil {
		slog.Error("config reload failed, keeping the running config", "error", err)
		return
	}
	old := w.Config()
	if needsRestart(old, next) {
		slog.Warn("listen, data_dir, sync_every, cache and log changes apply after a restart")
	}
	next.Listen = old.Listen
	next.DataDir = old.DataDir
	next.SyncEvery = old.SyncEvery
	next.Cache = old.Cache
	next.Log = old.Log
	w.set(next)
	slog.Info("config reloaded", "path", w.path)
}

// needsRestart reports whether a startup-only setting differs.
func needsRestart(old, next *Config) bool {
	return old.Listen != next.Listen ||
		old.DataDir != next.DataDir ||
		old.SyncEvery.D() != next.SyncEvery.D() ||
		old.Cache.TTL.D() != next.Cache.TTL.D() ||
		old.Cache.MaxEntries != next.Cache.MaxEntries ||
		old.Log != next.Log
}

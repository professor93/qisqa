// The syncing goroutine of the store. It alone syncs chunk files that hold
// links; the one other sync of a chunk file is that of a new, empty file,
// made where it is created (see createChunk). A pass syncs every file
// written to since the pass before it began, then lets go, all at once,
// every call that waits for those writes. Without sync_every a change is
// answered after the pass that covers it, and a pass begins as soon as
// there is something to sync. With sync_every a change is answered at once,
// and a pass runs at most once an interval, only when something was
// written.
package main

import (
	"errors"
	"fmt"
	"log/slog"
	"runtime"
	"sync"
	"time"
)

// Bounds of sync_every when it is not 0.
const (
	minSyncEvery = 100 * time.Millisecond
	maxSyncEvery = time.Minute
)

// reserveAhead is how many numbers state.json is saved ahead of the
// numbering with sync_every set (see reserve).
const reserveAhead = 4096

// errClosed is the error of a call made once the store is shut that would
// write, open a chunk file or save state.json, and of a change that waits
// for a pass that never runs, for the store was abandoned, as a crash
// leaves it.
var errClosed = errors.New("the data folder is closed")

// pass is one run of the syncing goroutine over the files written to.
type pass struct {
	done chan struct{} // closed once the pass has ended
	err  error         // the pass's error, set before done is closed
}

// newPass returns a pass that has not begun.
func newPass() *pass {
	return &pass{done: make(chan struct{})}
}

// closedPass is what a change made after the goroutine ended waits for: it
// has ended already, with errClosed.
var closedPass = func() *pass {
	p := &pass{done: make(chan struct{}), err: errClosed}
	close(p.done)
	return p
}()

// syncer is what the syncing goroutine shares with the calls that write.
// Its fields are guarded by mu, unless they say otherwise. A call that
// writes takes mu while it holds the store's write lock; the goroutine takes
// no other lock.
type syncer struct {
	mu sync.Mutex
	// files are the files written to since the last pass began. Each holds
	// a use, so that it stays open, and the one handle of its chunk, until a
	// pass has synced it.
	files []*openFile
	// next is the pass that covers the writes made now. It begins when the
	// goroutine takes files.
	next *pass
	// stopped says that the goroutine has ended.
	stopped bool

	wake  chan struct{} // holds a signal once files go from none to some
	stop  chan bool     // true from Close: a last pass first; false from abandon
	ended chan struct{} // closed once the goroutine has ended

	// Only the goroutine uses lastGood and final; Close reads final once
	// ended is closed.
	lastGood time.Time // when the last pass that succeeded began, or the store was opened
	final    error     // the error of the last pass, the one Close asks for
}

// startSyncing starts the syncing goroutine of a store just opened.
func (s *Store) startSyncing() {
	k := &s.syncer
	k.next = newPass()
	k.wake, k.stop, k.ended = make(chan struct{}, 1), make(chan bool), make(chan struct{})
	k.lastGood = time.Now()
	s.after = time.After
	go s.syncLoop()
}

// syncLoop is the syncing goroutine. Woken by a write, with sync_every set
// it waits for the interval to end, then takes the files. Without, when
// something was written while the last pass ran it takes the files at once,
// so that the disk is not left idle while those calls wait. When it had
// nothing to do, it first lets the goroutines that are ready to run go
// ahead of it: a pass lets every waiting call go at once, and the first of
// them to write wakes this goroutine, which the scheduler would run before
// the others; on one processor the two would take turns, a sync for every
// create, while the others wait. With nothing else ready to run, the yield
// returns at once. syncEvery and the after hook are read once a write has
// woken it: both are set before the store is first used.
func (s *Store) syncLoop() {
	k := &s.syncer
	defer close(k.ended)
	for {
		idle := false
		select {
		case <-k.wake: // written to while the last pass ran
		default:
			idle = true
			select {
			case <-k.wake:
			case save := <-k.stop:
				s.endSyncing(save)
				return
			}
		}
		var now time.Time // when the pass begins; the interval's end
		if every := s.syncEvery; every > 0 {
			select {
			case now = <-s.after(every):
			case save := <-k.stop:
				s.endSyncing(save)
				return
			}
		} else if idle {
			runtime.Gosched()
		}
		s.runPass(now)
	}
}

// endSyncing ends the goroutine. With save, from Close, a last pass syncs
// everything written: no change is made meanwhile, for Close holds the write
// lock. Without, from abandon, nothing is synced, as in a crash, and a call
// still waiting for a pass gets errClosed.
func (s *Store) endSyncing(save bool) {
	k := &s.syncer
	if save {
		k.final = s.runPass(time.Now())
	}
	k.mu.Lock()
	files, p := k.files, k.next
	for _, f := range files {
		f.queued = false
	}
	k.files, k.next, k.stopped = nil, nil, true
	k.mu.Unlock()
	for _, f := range files {
		s.release(f)
	}
	p.err = errClosed
	close(p.done)
}

// toSync gives the syncing goroutine the files a call has written to, while
// the call holds the write lock: each stays open, and the one handle of its
// chunk, until a pass has synced it. It returns the pass that covers every
// write made to them so far, or nil for no files. The caller holds a use of
// each file, so that none is closed while its use here begins.
func (s *Store) toSync(files ...*openFile) *pass {
	if len(files) == 0 {
		return nil
	}
	k := &s.syncer
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.stopped {
		return closedPass
	}
	first := len(k.files) == 0
	for _, f := range files {
		if !f.queued {
			f.queued = true
			f.users.Add(1)
			k.files = append(k.files, f)
		}
	}
	if first {
		select {
		case k.wake <- struct{}{}:
		default: // signalled already
		}
	}
	return k.next
}

// await returns once pass p has ended, with its error. With sync_every set
// it returns at once: the change is answered before its sync.
func (s *Store) await(p *pass) error {
	if p == nil || s.syncEvery > 0 {
		return nil
	}
	if s.onWait != nil {
		s.onWait()
	}
	<-p.done
	return p.err
}

// runPass syncs every file written to since the last pass began, then lets
// go every call waiting for those writes, all at once, with the pass's
// error, and returns it. Once a sync has failed no file is synced again: the
// pass fails with that error. now is when the pass began, with sync_every
// set or from Close; zero otherwise.
func (s *Store) runPass(now time.Time) error {
	k := &s.syncer
	k.mu.Lock()
	files, p := k.files, k.next
	if len(files) == 0 {
		k.mu.Unlock()
		return nil
	}
	for _, f := range files {
		f.queued = false
	}
	k.files, k.next = nil, newPass()
	k.mu.Unlock()

	err := s.failed()
	for _, f := range files {
		if err == nil {
			if syncErr := f.commit(); syncErr != nil {
				err = s.syncFailed(f, syncErr, now)
			}
		}
		s.release(f)
	}
	if err == nil && !now.IsZero() {
		k.lastGood = now
	}
	p.err = err
	close(p.done)
	return err
}

// syncFailed makes a failed sync of f the store's failure: nothing written to
// the file since its last good sync can be trusted to be on disk, whatever a
// later sync says. It is logged, fatal is called, and every change from then
// on returns the failure without writing. With sync_every set the changes
// answered since the last good sync may not be on disk: the log says so, and
// how long before now that sync began. It runs once: no file is synced after
// a failure.
func (s *Store) syncFailed(f *openFile, err error, now time.Time) error {
	failure := fmt.Errorf("sync of %s failed: %w", f.path, err)
	s.failure.Store(&failure)
	if s.syncEvery == 0 {
		slog.Error("a sync of a chunk file failed; qisqa stops so that it starts again from what is on disk",
			"file", f.path, "error", err)
	} else {
		slog.Error("a sync of a chunk file failed; changes answered since the last good sync may not be on disk, and qisqa stops so that it starts again from what is on disk",
			"file", f.path, "error", err, "since_last_good_sync", now.Sub(s.syncer.lastGood).Round(time.Millisecond))
	}
	s.fatal(failure)
	return failure
}

// failed returns the error of the first failed sync, or nil.
func (s *Store) failed() error {
	if err := s.failure.Load(); err != nil {
		return *err
	}
	return nil
}

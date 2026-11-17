// Tests for the syncing goroutine of the store: one pass covers every call
// that waits, a failed pass stops the store, and changes made from many
// goroutines at once keep every link that was answered.
package app

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// inCreate reports whether the calling goroutine runs inside Store.Create.
// The test binary names the package by its import path, not main.
func inCreate() bool {
	buf := make([]byte, 64<<10)
	return bytes.Contains(buf[:runtime.Stack(buf, false)], []byte(".(*Store).Create("))
}

// clock drives the interval of a store's syncing goroutine: a pass that
// waits for the interval to end waits for a tick the test gives.
type clock struct {
	ticks chan time.Time
	now   time.Time    // the time of the last tick
	waits atomic.Int64 // how often the goroutine began to wait for the interval
}

// withInterval sets sync_every on st, a store not used yet, to an interval
// that the returned clock ends.
func withInterval(st *Store) *clock {
	c := &clock{ticks: make(chan time.Time), now: t0}
	st.syncEvery = time.Second
	st.after = func(time.Duration) <-chan time.Time {
		c.waits.Add(1)
		return c.ticks
	}
	return c
}

// tick ends the interval the syncing goroutine waits for, a second after
// the last tick, and returns the error of the pass that follows once it has
// ended.
func (c *clock) tick(t *testing.T, st *Store) error {
	t.Helper()
	st.syncer.mu.Lock()
	p := st.syncer.next // the goroutine takes it once it has the tick
	st.syncer.mu.Unlock()
	c.now = c.now.Add(time.Second)
	select {
	case c.ticks <- c.now:
	case <-time.After(5 * time.Second):
		t.Fatal("no pass waits for the interval to end")
	}
	<-p.done
	return p.err
}

// noPassWaits fails the test if the syncing goroutine waits for the
// interval to end.
func (c *clock) noPassWaits(t *testing.T) {
	t.Helper()
	select {
	case c.ticks <- c.now:
		t.Fatal("a pass waited for the interval with nothing written")
	default:
	}
}

// savedState returns what the state.json at path holds for code length 5.
func savedState(t *testing.T, path string) savedLength {
	t.Helper()
	b, err := os.ReadFile(path)
	must(t, err)
	var saved map[int]savedLength
	must(t, json.Unmarshal(b, &saved))
	return saved[5]
}

// statePath is the path of domain 1's state.json in the data folder dir.
func statePath(dir string) string {
	return filepath.Join(dir, "d1", "state.json")
}

// result waits for one error from results, at most 10 seconds.
func result(t *testing.T, results <-chan error, what string) error {
	t.Helper()
	select {
	case err := <-results:
		return err
	case <-time.After(10 * time.Second):
		t.Fatalf("%s did not return", what)
		return nil
	}
}

// Creates that arrive while a sync runs wait for the one pass that follows,
// and that pass lets them all go. It is the store's syncing goroutine that
// syncs, never a create, and no create returns before the pass that covers
// it has synced.
func TestOnePassLetsGoEveryWaitingCreate(t *testing.T) {
	st := newTestStore(t)
	create(t, st, 1) // starts the chunk
	before := st.syncs()
	// The store counts a sync as it begins; this counts it once it has ended.
	f := st.files[chunkID{1, 5, 0}]
	var ended atomic.Int64
	syncFile := f.sync
	f.sync = func() error {
		err := syncFile()
		ended.Add(1)
		return err
	}
	// returned checks, as a create returns, that the pass that covers it, the
	// want-th sync, has ended.
	returned := func(err error, want int64) error {
		if got := ended.Load(); err == nil && got < want {
			err = fmt.Errorf("a create returned when %d syncs had ended, before the pass that covers it", got)
		}
		return err
	}

	const n = 16
	var waiting sync.WaitGroup
	waiting.Add(1 + n)
	st.onWait = waiting.Done
	held, release := make(chan struct{}), make(chan struct{})
	var hold sync.Once
	var syncedInACreate atomic.Bool
	st.onSync = func(string) {
		if inCreate() {
			syncedInACreate.Store(true)
		}
		hold.Do(func() {
			close(held)
			<-release
		})
	}

	results := make(chan error, 1+n)
	go func() {
		_, err := st.Create(1, 1, 5, anyCode, oneLink())
		results <- returned(err, 1)
	}()
	<-held
	for range n {
		go func() {
			_, err := st.Create(1, 1, 5, anyCode, oneLink())
			results <- returned(err, 2)
		}()
	}
	waiting.Wait() // every create waits for a pass
	close(release)
	for range 1 + n {
		must(t, result(t, results, "a create"))
	}
	eq(t, st.syncs()-before, int64(2))
	if syncedInACreate.Load() {
		t.Error("a create synced a chunk file itself")
	}
}

// A failed pass makes the store fail: every call that waits gets the error,
// later creates return it without writing, and fatal is called once.
func TestFailedPassStopsTheStore(t *testing.T) {
	t.Run("without an interval", func(t *testing.T) {
		logs := captureLog(t)
		st := newTestStore(t)
		var fatal atomic.Int64
		st.fatal = func(error) { fatal.Add(1) }
		old := create(t, st, 1)

		// The first create's sync is held; n creates wait for the next pass.
		const n = 8
		var waiting sync.WaitGroup
		waiting.Add(1 + n)
		st.onWait = waiting.Done
		held, release := make(chan struct{}), make(chan struct{})
		var hold sync.Once
		st.onSync = func(string) {
			hold.Do(func() {
				close(held)
				<-release
			})
		}
		results := make(chan error, 1+n)
		createOne := func() {
			_, err := st.Create(1, 1, 5, anyCode, oneLink())
			results <- err
		}
		go createOne()
		<-held
		for range n {
			go createOne()
		}
		waiting.Wait()
		failure := errors.New("input/output error")
		st.failSync = failure // read by the held sync once it goes on
		close(release)
		var first error
		for range 1 + n {
			err := result(t, results, "a create")
			if !errors.Is(err, failure) {
				t.Fatalf("Create returned %v, want %v", err, failure)
			}
			first = err
		}
		eq(t, fatal.Load(), int64(1))

		st.onWait, st.onSync = nil, nil
		st.onWrite = func(path string, offset int64, b []byte) {
			t.Errorf("a write of %d bytes at byte %d of %s after the failure", len(b), offset, path)
		}
		_, err := st.Create(1, 1, 5, anyCode, oneLink())
		eq(t, err, first)
		st.onWrite = nil
		found(t, st, old) // lookups go on
		eq(t, fatal.Load(), int64(1))
		eq(t, strings.Count(logs.String(), "level=ERROR"), 1)
		if !strings.Contains(logs.String(), "starts again from what is on disk") {
			t.Fatalf("the log does not say what happens next:\n%s", logs)
		}
	})

	// With an interval no call waits, and the failure cannot take back what
	// was answered: the log says that it may not be on disk, and since when.
	t.Run("with an interval", func(t *testing.T) {
		logs := captureLog(t)
		st := newTestStore(t)
		c := withInterval(st)
		var fatal atomic.Int64
		st.fatal = func(error) { fatal.Add(1) }
		old := create(t, st, 1)
		must(t, c.tick(t, st)) // the last good sync
		answered := create(t, st, 1)
		failure := errors.New("input/output error")
		st.failSync = failure // read once the interval has ended
		if err := c.tick(t, st); !errors.Is(err, failure) {
			t.Fatalf("the pass returned %v, want %v", err, failure)
		}
		eq(t, fatal.Load(), int64(1))

		st.onWrite = func(path string, offset int64, b []byte) {
			t.Errorf("a write of %d bytes at byte %d of %s after the failure", len(b), offset, path)
		}
		if _, err := st.Create(1, 1, 5, anyCode, oneLink()); !errors.Is(err, failure) {
			t.Fatalf("Create returned %v, want %v", err, failure)
		}
		st.onWrite = nil
		found(t, st, old)
		found(t, st, answered)
		c.noPassWaits(t)
		eq(t, fatal.Load(), int64(1))
		eq(t, strings.Count(logs.String(), "level=ERROR"), 1)
		for _, want := range []string{
			"changes answered since the last good sync may not be on disk",
			"since_last_good_sync=1s",
			"starts again from what is on disk",
			failure.Error(),
			firstChunk(st.dir),
		} {
			if !strings.Contains(logs.String(), want) {
				t.Fatalf("the log does not say %q:\n%s", want, logs)
			}
		}
	})
}

// Close while a pass runs and calls wait for the next one: every call that
// wrote before Close is answered as done and found after; one that comes
// after Close gets errClosed.
func TestCloseWhileCallsWait(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	st := openAt(t, dir)
	create(t, st, 1)
	const n = 8
	var waiting sync.WaitGroup
	waiting.Add(1 + n)
	st.onWait = waiting.Done
	held, release := make(chan struct{}), make(chan struct{})
	var hold sync.Once
	st.onSync = func(string) {
		hold.Do(func() {
			close(held)
			<-release
		})
	}
	type res struct {
		links []Link
		err   error
	}
	results := make(chan res, 1+n)
	one := func(to chan res) {
		l, err := st.Create(1, 1, 5, anyCode, oneLink())
		to <- res{l, err}
	}
	go one(results)
	<-held
	for range n {
		go one(results)
	}
	waiting.Wait()
	st.onWait = nil
	closed := make(chan error, 1)
	go func() { closed <- st.Close() }()
	for st.mu.TryLock() { // until Close holds the write lock
		st.mu.Unlock()
		runtime.Gosched()
	}
	late := make(chan res, 1)
	go one(late)
	select {
	case err := <-closed:
		t.Fatalf("Close returned %v while a sync was held", err)
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	var answered []Link
	for range 1 + n {
		r := <-results
		must(t, r.err)
		answered = append(answered, r.links...)
	}
	must(t, <-closed)
	r := <-late
	if !errors.Is(r.err, errClosed) {
		t.Errorf("a create after Close returned %v, %v", r.links, r.err)
	}
	eq(t, savedState(t, statePath(dir)), savedLength{Chunk: 0, Next: 1 + 1 + n, End: 1 + 1 + n})

	st = openAt(t, dir)
	defer st.Close()
	found(t, st, answered)
}

// abandon while a pass runs and calls wait for the next one: the running
// pass ends, the calls that wait for a pass that never runs get errClosed,
// nothing hangs, and no file is left open.
func TestAbandonWhileCallsWait(t *testing.T) {
	st := openAt(t, filepath.Join(t.TempDir(), "data"))
	create(t, st, 1)
	const n = 8
	var waiting sync.WaitGroup
	waiting.Add(1 + n)
	st.onWait = waiting.Done
	held, release := make(chan struct{}), make(chan struct{})
	var hold sync.Once
	st.onSync = func(string) {
		hold.Do(func() {
			close(held)
			<-release
		})
	}
	results := make(chan error, 1+n)
	one := func() {
		_, err := st.Create(1, 1, 5, anyCode, oneLink())
		results <- err
	}
	go one()
	<-held
	for range n {
		go one()
	}
	waiting.Wait()
	done := make(chan struct{})
	go func() { st.abandon(); close(done) }()
	for st.mu.TryLock() { // until abandon holds the write lock
		st.mu.Unlock()
		runtime.Gosched()
	}
	close(release)
	var ok, closed int
	for range 1 + n {
		switch err := result(t, results, "a create"); {
		case err == nil:
			ok++
		case errors.Is(err, errClosed):
			closed++
		default:
			t.Errorf("a create returned %v", err)
		}
	}
	<-done
	t.Logf("answered as done: %d, errClosed: %d", ok, closed)
	if ok+closed != 1+n || ok < 1 {
		t.Errorf("answered as done: %d, errClosed: %d", ok, closed)
	}
	st.openMu.RLock()
	eq(t, len(st.files), 0)
	st.openMu.RUnlock()
}

// With an interval a create is answered before its sync, and lookups find
// its links at once. The tick that ends the interval syncs the file once for
// every create made in it. With nothing written no pass waits for an
// interval, and a clean stop finds nothing left to sync.
func TestIntervalAnswersBeforeTheSync(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	st := openAt(t, dir)
	c := withInterval(st)
	first := create(t, st, 3)
	second := create(t, st, 2)
	eq(t, st.syncs(), int64(1)) // the new file's head, synced where it was created
	found(t, st, first)
	found(t, st, second)
	must(t, c.tick(t, st))
	eq(t, st.syncs(), int64(2))
	c.noPassWaits(t)
	must(t, st.Close())
	eq(t, st.syncs(), int64(2))
	eq(t, c.waits.Load(), int64(1))

	st = openAt(t, dir)
	defer st.Close()
	found(t, st, first)
	found(t, st, second)
}

// Edits, deletes and the first open of a secured link follow the mode, as
// creates do: without an interval they return once their sync has run, with
// one before it, the change seen at once, and the next pass syncs them.
func TestEditsFollowTheMode(t *testing.T) {
	url, later := "https://example.org/moved", in(48*time.Hour).Truncate(time.Second)
	for _, edit := range []struct {
		name string
		do   func(st *Store, l, secured Link) error
		seen func(t *testing.T, st *Store, l, secured Link)
	}{
		{
			"expiry",
			func(st *Store, l, _ Link) error { _, err := st.Update(1, l.Code, 1, nil, &later); return err },
			func(t *testing.T, st *Store, l, _ Link) { eq(t, mustFind(t, st, l.Code).ExpiresAt.Equal(later), true) },
		},
		{
			"URL",
			func(st *Store, l, _ Link) error { _, err := st.Update(1, l.Code, 1, &url, nil); return err },
			func(t *testing.T, st *Store, l, _ Link) { eq(t, mustFind(t, st, l.Code).URL, url) },
		},
		{
			"delete",
			func(st *Store, l, _ Link) error { return st.Delete(1, l.Code, 1) },
			func(t *testing.T, st *Store, l, _ Link) { mustBeGone(t, st, l.Code) },
		},
		{
			"open",
			func(st *Store, _, secured Link) error {
				_, ok, err := st.Open(1, secured.Code, time.Now())
				if err == nil && !ok {
					err = errors.New("the secured link did not open")
				}
				return err
			},
			func(t *testing.T, st *Store, _, secured Link) { eq(t, mustFind(t, st, secured.Code).Opened, true) },
		},
	} {
		for _, interval := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/interval=%v", edit.name, interval), func(t *testing.T) {
				st := newTestStore(t)
				var c *clock
				if interval {
					c = withInterval(st)
				}
				minute := time.Minute
				links, err := st.Create(1, 1, 5, anyCode, []NewLink{
					regular("https://example.com/a"),
					{URL: "https://example.com/secured", ExpiresAt: in(time.Hour), AfterOpen: &minute},
				})
				must(t, err)
				if interval {
					must(t, c.tick(t, st))
				}
				if !interval {
					// Each change waits for its slot, the last write it makes
					// before it returns: a sync that began once that write had
					// returned must have ended when the change returns. One
					// goroutine syncs the file, so syncs end in the order they
					// began.
					f := st.files[chunkID{1, 5, 0}]
					var mu sync.Mutex
					begun, ended, atSlot := 0, 0, -1 // syncs begun and ended; syncs begun when the slot was written
					write, syncFile := f.write, f.sync
					f.write = func(b []byte, offset int64) (int, error) {
						n, err := write(b, offset)
						if offset >= slotsOffset && offset < recordsOffset {
							mu.Lock()
							atSlot = begun
							mu.Unlock()
						}
						return n, err
					}
					f.sync = func() error {
						mu.Lock()
						begun++
						mu.Unlock()
						err := syncFile()
						mu.Lock()
						ended++
						mu.Unlock()
						return err
					}
					must(t, edit.do(st, links[0], links[1]))
					mu.Lock()
					slot, done := atSlot, ended
					mu.Unlock()
					if slot < 0 || done <= slot {
						t.Fatalf("the change returned before a sync that began after its slot was written had ended (syncs begun at the slot %d, ended %d)", slot, done)
					}
					edit.seen(t, st, links[0], links[1])
					return
				}
				before := st.syncs()
				must(t, edit.do(st, links[0], links[1]))
				eq(t, st.syncs(), before)
				edit.seen(t, st, links[0], links[1])
				must(t, c.tick(t, st))
				eq(t, st.syncs(), before+1)
			})
		}
	}
}

// A clean stop with an interval syncs everything first, then writes the
// exact numbering, and where the links end, to state.json: it loses
// nothing.
func TestCleanStopSyncsEverything(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	st := openAt(t, dir)
	withInterval(st)
	links := create(t, st, 3)
	eq(t, st.syncs(), int64(1)) // the new file's head, synced where it was created
	var atSync savedLength
	st.onSync = func(string) { atSync = savedState(t, statePath(dir)) }
	must(t, st.Close())
	eq(t, st.syncs(), int64(2))
	eq(t, atSync.Next, uint64(reserveAhead)) // the exact number came after the sync
	eq(t, savedState(t, statePath(dir)), savedLength{Chunk: 0, Next: 3, End: 3})

	st = openAt(t, dir)
	defer st.Close()
	found(t, st, links)
}

// A clean stop with an interval wastes none of the numbers saved ahead.
func TestCleanStopWastesNoNumber(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	st := openAt(t, dir)
	withInterval(st)
	create(t, st, 5)
	must(t, st.Close())

	st = openAt(t, dir)
	withInterval(st)
	defer st.Close()
	eq(t, numberOf(t, st, create(t, st, 1)[0].Code), uint64(5))
}

// With an interval the links answered since the last pass are lost when the
// machine loses power. Their codes are never handed out again, for state.json
// was saved ahead of them, and they answer as codes nobody was given. That
// is no damage, and the next start does not call it one.
func TestNoNumberTwiceAfterPowerLoss(t *testing.T) {
	logs := captureLog(t)
	dir := filepath.Join(t.TempDir(), "data")
	st := openAt(t, dir)
	c := withInterval(st)
	kept := create(t, st, 10)
	must(t, c.tick(t, st))
	// The chunk file as the disk holds it after that sync. state.json is
	// synced whenever it is written, so the disk holds it as it is.
	synced, err := os.ReadFile(firstChunk(dir))
	must(t, err)
	lost := create(t, st, 20)
	lost = append(lost, create(t, st, reserveAhead)...) // past the numbers saved ahead
	st.abandon()
	must(t, os.WriteFile(firstChunk(dir), synced, 0o600)) // what no sync covered is gone

	st = openAt(t, dir)
	defer st.Close()
	for _, l := range lost {
		notALink(t, st, l.Code)
	}
	fresh := create(t, st, 5)
	allDiffer(t, kept, lost, fresh)
	found(t, st, kept)
	found(t, st, fresh)
	noWarnings(t, logs)
}

// With an interval the machine loses power in the interval in which a chunk
// was started, before any pass. The new file's head was synced where the
// file was created, before its folder named it: the next start finds the
// chunk neither damaged nor lost, and numbering goes on from the number
// saved ahead.
func TestPowerLossAfterAChunkStart(t *testing.T) {
	logs := captureLog(t)
	dir := filepath.Join(t.TempDir(), "data")
	st := openAt(t, dir)
	withInterval(st)
	// The disk after the power loss holds what a sync covered; without one,
	// a file of its size with nothing in it.
	image := make([]byte, recordsOffset)
	st.onSync = func(path string) {
		b, err := os.ReadFile(path) // what the sync now begun makes durable
		must(t, err)
		image = b
	}
	lost := create(t, st, 3)
	st.abandon()
	must(t, os.WriteFile(firstChunk(dir), image, 0o600))

	st = openAt(t, dir)
	withInterval(st)
	defer st.Close()
	for _, l := range lost {
		notALink(t, st, l.Code)
	}
	next := create(t, st, 1)
	if m := numberOf(t, st, next[0].Code); m > reserveAhead {
		t.Errorf("the next link has number %d, past the %d saved ahead", m, reserveAhead)
	}
	found(t, st, next)
	noWarnings(t, logs)
}

// A sync of a new chunk file that fails is an error of the create that
// started the chunk, as a failed sync of its folder is: the file is removed
// and the numbering put back, the store does not fail, fatal is not called,
// and the next create starts the same chunk.
func TestFailedSyncOfANewChunkFailsOnlyThatCreate(t *testing.T) {
	for _, interval := range []bool{false, true} {
		t.Run(fmt.Sprintf("interval=%v", interval), func(t *testing.T) {
			st := newTestStore(t)
			if interval {
				withInterval(st)
			}
			var fatal atomic.Int64
			st.fatal = func(error) { fatal.Add(1) }
			failure := errors.New("input/output error")
			st.failSync = failure
			if _, err := st.Create(1, 1, 5, anyCode, oneLink()); !errors.Is(err, failure) {
				t.Fatalf("Create returned %v, want %v", err, failure)
			}
			st.failSync = nil
			eq(t, fatal.Load(), int64(0))
			must(t, st.failed())
			if _, err := os.Stat(firstChunk(st.dir)); !errors.Is(err, fs.ErrNotExist) {
				t.Fatalf("the file of the chunk is there after the failed start: %v", err)
			}

			links := create(t, st, 1)
			eq(t, numberOf(t, st, links[0].Code), uint64(0))
			found(t, st, links)
		})
	}
}

// Once the store is shut, by Close or by abandon, a create, every edit, a
// delete and a lookup return errClosed, with an interval or without, before
// they write, open a chunk file or save state.json.
func TestNoCallIsTakenOnceShut(t *testing.T) {
	for _, how := range []string{"Close", "abandon"} {
		for _, interval := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/interval=%v", how, interval), func(t *testing.T) {
				dir := filepath.Join(t.TempDir(), "data")
				st := openAt(t, dir)
				if interval {
					withInterval(st)
				}
				minute := time.Minute
				links, err := st.Create(1, 1, 5, anyCode, []NewLink{
					regular("https://example.com/a"),
					{URL: "https://example.com/secured", ExpiresAt: in(time.Hour), AfterOpen: &minute},
				})
				must(t, err)
				if how == "Close" {
					must(t, st.Close())
				} else {
					st.abandon()
				}
				state, err := os.ReadFile(statePath(dir)) // as the shut left it
				must(t, err)
				syncs := st.syncs()
				st.onWrite = func(path string, offset int64, b []byte) {
					t.Errorf("a write of %d bytes at byte %d of %s once the store was shut", len(b), offset, path)
				}

				url, later := "https://example.org/moved", in(48*time.Hour)
				for _, call := range []struct {
					name string
					do   func() error
				}{
					{"create", func() error { _, err := st.Create(1, 1, 5, anyCode, oneLink()); return err }},
					{"URL change", func() error { _, err := st.Update(1, links[0].Code, 1, &url, nil); return err }},
					{"expiry change", func() error { _, err := st.Update(1, links[0].Code, 1, nil, &later); return err }},
					{"first open", func() error { _, _, err := st.Open(1, links[1].Code, time.Now()); return err }},
					{"delete", func() error { return st.Delete(1, links[0].Code, 1) }},
					{"lookup", func() error { _, _, err := st.Lookup(1, links[0].Code); return err }},
				} {
					if err := call.do(); !errors.Is(err, errClosed) {
						t.Errorf("a %s once the store was shut returned %v, want errClosed", call.name, err)
					}
				}

				after, err := os.ReadFile(statePath(dir))
				must(t, err)
				eq(t, string(after), string(state))
				st.openMu.RLock()
				open := len(st.files)
				st.openMu.RUnlock()
				eq(t, open, 0)
				eq(t, st.syncs(), syncs)
			})
		}
	}
}

// The numbers saved ahead can end past the newest chunk, in one with no file
// yet. After a stop that was not clean numbering goes on from there: the
// numbers passed over lead nowhere, those of the chunk with no file too, and
// the next create starts that chunk.
func TestSavedNumberPastTheNewestChunk(t *testing.T) {
	logs := captureLog(t)
	dir := filepath.Join(t.TempDir(), "data")
	st := openAt(t, dir)
	create(t, st, 1)
	must(t, st.Close())
	// Numbering goes on 1,000 numbers before the end of the first chunk.
	writeFile(t, filepath.Join(dir, "d1"), "state.json", `{"5":{"chunk":0,"next":64536,"end":1}}`)
	st = openAt(t, dir)
	withInterval(st)
	last := create(t, st, 1)
	eq(t, numberOf(t, st, last[0].Code), uint64(64536))
	eq(t, savedState(t, statePath(dir)).Next, uint64(64536+reserveAhead)) // inside the second chunk
	st.abandon()

	st = openAt(t, dir)
	defer st.Close()
	coder := NewCoder(st.key, 1)
	passed := []uint64{64537, chunkLinks - 1, chunkLinks, 64536 + reserveAhead - 1}
	for _, m := range passed {
		notALink(t, st, coder.Code(5, m))
	}
	found(t, st, last)
	next := create(t, st, 1)
	eq(t, numberOf(t, st, next[0].Code), uint64(64536+reserveAhead))
	found(t, st, next)
	for _, m := range passed {
		notALink(t, st, coder.Code(5, m))
	}
	noWarnings(t, logs)
}

// Without an interval state.json is written as before: when a chunk is
// started and at Close, never ahead of the links.
func TestNothingIsSavedAheadWithoutAnInterval(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	st := openAt(t, dir)
	create(t, st, 1)
	must(t, st.Close())
	// Numbering goes on 10 numbers before the end of the first chunk.
	writeFile(t, filepath.Join(dir, "d1"), "state.json", `{"5":{"chunk":0,"next":65526,"end":1}}`)
	written, err := os.Stat(statePath(dir))
	must(t, err)
	unchanged := func(when string) {
		t.Helper()
		info, err := os.Stat(statePath(dir))
		must(t, err)
		if !os.SameFile(written, info) {
			t.Fatalf("state.json was written %s", when)
		}
	}

	st = openAt(t, dir)
	create(t, st, 5)
	unchanged("while links went into the same chunk")
	create(t, st, reserveAhead+10) // into the second chunk, and past a block of numbers in it
	info, err := os.Stat(statePath(dir))
	must(t, err)
	if os.SameFile(written, info) {
		t.Fatal("state.json was not written when the second chunk was started")
	}
	written = info
	eq(t, savedState(t, statePath(dir)), savedLength{Chunk: 1, Next: chunkLinks, End: chunkLinks})
	create(t, st, 1)
	unchanged("while links went into the second chunk")
	must(t, st.Close())
	next := uint64(65526 + 5 + reserveAhead + 10 + 1)
	eq(t, savedState(t, statePath(dir)), savedLength{Chunk: 1, Next: next, End: next})
}

// With an interval a file holds writes not yet synced for a while. The limit
// on open files does not close it before its sync, even when more files are
// then open than the limit; the next pass syncs each.
func TestFileWithUnsyncedWritesIsNotClosed(t *testing.T) {
	// openNow returns how many chunk files are open, and whether the file
	// of domain 1 was closed to stay within the limit.
	openNow := func(st *Store) (int, bool) {
		st.openMu.RLock()
		defer st.openMu.RUnlock()
		return len(st.files), st.closed[chunkID{1, 5, 0}]
	}
	t.Run("written to by a create", func(t *testing.T) {
		defer func(n int) { maxOpenChunks = n }(maxOpenChunks)
		maxOpenChunks = 1
		st := newTestStore(t)
		c := withInterval(st)
		a := create(t, st, 1)
		b, err := st.Create(2, 1, 5, anyCode, oneLink()) // would close the file of domain 1
		must(t, err)
		open, closed := openNow(st)
		eq(t, open, 2)
		eq(t, closed, false)
		must(t, c.tick(t, st))
		eq(t, st.syncs(), int64(4)) // each new file's head, then both files in one pass
		found(t, st, a)
		_, state, err := st.Lookup(2, b[0].Code)
		must(t, err)
		eq(t, state, LinkFound)
	})
	// A create that fails after it wrote part of its link leaves a write
	// not yet synced in the file too.
	t.Run("written to by a create that failed", func(t *testing.T) {
		defer func(n int) { maxOpenChunks = n }(maxOpenChunks)
		maxOpenChunks = 1
		st := newTestStore(t)
		c := withInterval(st)
		a := create(t, st, 1)
		must(t, c.tick(t, st))
		syncs := st.syncs()
		f := st.files[chunkID{1, 5, 0}]
		write := f.write
		failure := errors.New("no space left on device")
		f.write = func(b []byte, offset int64) (int, error) {
			if offset >= slotsOffset && offset < recordsOffset {
				return 0, failure // the slot; the record before it is written
			}
			return write(b, offset)
		}
		if _, err := st.Create(1, 1, 5, anyCode, oneLink()); !errors.Is(err, failure) {
			t.Fatalf("Create returned %v, want %v", err, failure)
		}
		f.write = write
		_, err := st.Create(2, 1, 5, anyCode, oneLink()) // would close the file of domain 1
		must(t, err)
		open, closed := openNow(st)
		eq(t, open, 2)
		eq(t, closed, false)
		must(t, c.tick(t, st))
		eq(t, st.syncs()-syncs, int64(3)) // the head of the new file of domain 2, then both files
		found(t, st, a)
	})
}

// With an interval the parts of a link answered since the last pass reach
// the disk in any order, or not at all, when the machine loses power: its
// slot, its record, and its host's line in the host table. Whichever part is
// missing or cut off, every code leads to its own target or nowhere, also
// after the next link is created, and that link gets a code nobody was
// given.
func TestTornLinkNeverGivesAWrongTarget(t *testing.T) {
	captureLog(t)
	dir := filepath.Join(t.TempDir(), "data")
	st := openAt(t, dir)
	withInterval(st)
	links, err := st.Create(1, 1, 5, anyCode, []NewLink{regular("https://go.example/a"), regular("https://example.org/b")})
	must(t, err)
	st.abandon() // nothing was synced; the images below are what the disk may hold

	files := map[string][]byte{}
	for _, name := range []string{"key", "d1/state.json", chunkFileName} {
		b, err := os.ReadFile(filepath.Join(dir, name))
		must(t, err)
		files[name] = b
	}
	chunk := files[chunkFileName]
	p := promises{ever: map[string][]string{}, answered: map[string]bool{}}
	type part struct {
		name    string
		at, end int // where it is in the chunk file
	}
	var parts []part
	line := headerSize // each link brought a host of its own into the table
	for i, l := range links {
		p.ever[l.Code] = []string{l.URL}
		p.answered[l.Code] = true
		at := slotsOffset + int(numberOf(t, st, l.Code))*slotSize
		s, state := decodeSlot(chunk[at:])
		eq(t, state, slotOK)
		record := recordsOffset + int(s.offset)
		parts = append(parts,
			part{fmt.Sprintf("slot of link %d", i), at, at + slotSize},
			part{fmt.Sprintf("record of link %d", i), record, record + int(s.length)},
			part{fmt.Sprintf("host line of link %d", i), line, line + 1 + int(chunk[line])})
		line += 1 + int(chunk[line])
	}
	image := filepath.Join(t.TempDir(), "image")
	for _, part := range parts {
		zeroed := bytes.Clone(chunk)
		clear(zeroed[part.at:part.end])
		files[chunkFileName] = zeroed
		checkImage(t, image, part.name+" zeroed", files, p)
		for _, n := range []int{part.at, (part.at + part.end) / 2} {
			files[chunkFileName] = chunk[:n]
			checkImage(t, image, fmt.Sprintf("%s cut at byte %d", part.name, n), files, p)
		}
	}
}

// changed is what the stress test answered for one link.
type changed struct {
	url     string // the URL it leads to; empty once deleted
	secured bool
}

// Creates, edits and lookups run at once on three domains while the limit on
// open files closes files and removed files are looked for, with and
// without an interval, whose passes then run as often as they can. Without
// an interval no change returns before a sync of its file that began after
// its last write had returned has ended. Every answered link leads where it
// was answered to, then, and after the store is abandoned, as a crash of
// qisqa leaves it, and opened again: the system has whatever was written,
// synced or not.
func TestConcurrentChangesKeepEveryAnsweredLink(t *testing.T) {
	for _, limit := range []int{1, 2} {
		for _, interval := range []bool{false, true} {
			t.Run(fmt.Sprintf("maxOpenChunks=%d/interval=%v", limit, interval), func(t *testing.T) {
				changeConcurrently(t, limit, interval)
			})
		}
	}
}

// changeConcurrently is TestConcurrentChangesKeepEveryAnsweredLink with one
// limit on open files, and with an interval or without.
func changeConcurrently(t *testing.T, limit int, interval bool) {
	defer func(n int) { maxOpenChunks = n }(maxOpenChunks)
	maxOpenChunks = limit
	dir := filepath.Join(t.TempDir(), "data")
	st := openAt(t, dir)
	var c *clock
	if interval {
		c = withInterval(st)
	}
	// Without an interval each change, as it returns, finds a sync of its file
	// that began after its last write had returned and that has ended. The
	// writes of every chunk file and its syncs are followed through each of
	// its handles: those kept now, after a first link on each domain has
	// started its chunk, and those opened again after the limit closed them.
	covered := func(domain uint16, code string) bool { return true }
	if !interval {
		type event struct {
			offset int64 // of a write; -1 for a sync
			done   bool  // a sync that has ended
		}
		var mu sync.Mutex
		events := map[string][]*event{} // by file: writes once returned, syncs once begun, in order
		follow := func(file *chunkFile) {
			write, syncFile := file.write, file.sync
			file.write = func(b []byte, offset int64) (int, error) {
				n, err := write(b, offset)
				mu.Lock()
				events[file.path] = append(events[file.path], &event{offset: offset})
				mu.Unlock()
				return n, err
			}
			file.sync = func() error {
				e := &event{offset: -1}
				mu.Lock()
				events[file.path] = append(events[file.path], e)
				mu.Unlock()
				err := syncFile()
				mu.Lock()
				e.done = err == nil
				mu.Unlock()
				return err
			}
		}
		for domain := uint16(1); domain <= 3; domain++ {
			if _, err := st.Create(domain, 1, 5, anyCode, oneLink()); err != nil {
				t.Fatal(err)
			}
		}
		st.openMu.Lock()
		for _, f := range st.files {
			follow(f.chunkFile)
		}
		st.onOpened = follow
		st.openMu.Unlock()
		// covered reports whether a change of the link a code stands for
		// returned after a sync that began once the change had written the
		// link's slot, its last write, and that has ended. Only the writer of
		// a link writes its slot.
		covered = func(domain uint16, code string) bool {
			_, m, _ := NewCoder(st.key, domain).Number(code)
			at := slotsOffset + int64(m%chunkLinks)*slotSize
			mu.Lock()
			defer mu.Unlock()
			es := events[st.chunkPath(chunkID{domain, 5, m / chunkLinks})]
			last := -1
			for i, e := range es {
				if e.offset == at {
					last = i
				}
			}
			for _, e := range es[last+1:] {
				if last >= 0 && e.offset < 0 && e.done {
					return true
				}
			}
			return false
		}
	}

	stop := make(chan struct{})
	var background sync.WaitGroup
	background.Go(func() {
		for {
			select {
			case <-stop:
				return
			default:
				st.ForgetRemovedFiles()
			}
		}
	})
	if interval {
		background.Go(func() {
			for {
				select {
				case <-stop:
					return
				case c.ticks <- time.Now():
				}
			}
		})
	}

	var mu sync.Mutex
	answered := map[uint16]map[string]changed{}
	var writers sync.WaitGroup
	for w := range 6 {
		domain := uint16(1 + w%3)
		writers.Go(func() {
			mine := map[string]changed{}
			defer func() {
				mu.Lock()
				defer mu.Unlock()
				if answered[domain] == nil {
					answered[domain] = map[string]changed{}
				}
				for code, want := range mine {
					answered[domain][code] = want
				}
			}()
			for i := range 12 {
				none := time.Duration(0)
				links, err := st.Create(domain, 1, 5, anyCode, []NewLink{
					regular(fmt.Sprintf("https://example.com/%d/%d/a", w, i)),
					regular(fmt.Sprintf("https://go.example/%d/%d/b", w, i)),
					{URL: fmt.Sprintf("https://example.org/%d/%d/s", w, i), ExpiresAt: in(time.Hour), AfterOpen: &none},
				})
				if err != nil {
					t.Error(err)
					return
				}
				// returned fails the test unless the change of a link that
				// just returned is covered.
				returned := func(what string, l Link) bool {
					if !covered(domain, l.Code) {
						t.Errorf("%s of %s on domain %d returned with no sync ended that began after its slot was written", what, l.Code, domain)
						return false
					}
					return true
				}
				if !returned("the create", links[2]) { // its last link's slot is its last write
					return
				}
				for _, l := range links {
					mine[l.Code] = changed{url: l.URL, secured: l.Secured}
				}
				url := fmt.Sprintf("https://moved.example/%d/%d", w, i)
				if _, err := st.Update(domain, links[0].Code, 1, &url, nil); err != nil {
					t.Error(err)
					return
				}
				if !returned("the URL change", links[0]) {
					return
				}
				mine[links[0].Code] = changed{url: url}
				if err := st.Delete(domain, links[1].Code, 1); err != nil {
					t.Error(err)
					return
				}
				if !returned("the delete", links[1]) {
					return
				}
				mine[links[1].Code] = changed{}
				if got, ok, err := st.Open(domain, links[2].Code, time.Now()); err != nil || !ok || got != links[2].URL {
					t.Errorf("open: %q, %v, %v", got, ok, err)
					return
				}
				if !returned("the first open", links[2]) {
					return
				}
				for code, want := range mine {
					l, state, err := st.Lookup(domain, code)
					if err != nil || (want.url == "") != (state != LinkFound) || l.URL != want.url {
						t.Errorf("lookup of %s: %v %q, %v; answered %q", code, state, l.URL, err, want.url)
						return
					}
				}
			}
		})
	}
	writers.Wait()
	close(stop)
	background.Wait()
	if t.Failed() {
		st.abandon()
		return
	}

	st.abandon()
	st = openAt(t, dir)
	defer st.Close()
	for domain, links := range answered {
		for code, want := range links {
			l, state, err := st.Lookup(domain, code)
			must(t, err)
			if (want.url == "") != (state != LinkFound) || l.URL != want.url || (want.secured && !l.Opened) {
				t.Errorf("after a restart, %s on domain %d is %v %q opened %v; answered %q", code, domain, state, l.URL, l.Opened, want.url)
			}
		}
	}
}

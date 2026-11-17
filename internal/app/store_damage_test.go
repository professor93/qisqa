// Crash and damage tests: the store is opened on every disk image a crash
// can leave behind, and on chunk files with a damaged byte or cut short, and
// must keep its promises on each.
package app

import (
	"bytes"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// chunkFileName is the chunk file of length 5 on domain 1 within a data
// folder, the one file these tests write images of.
const chunkFileName = "d1/5-000000000000.chunk"

// promises is what a store opened after a crash, or on damaged files, must
// keep.
type promises struct {
	must     map[string][]string // code → the URLs it must lead to one of; none: it must lead nowhere
	opened   map[string]bool     // codes whose link must read as opened
	ever     map[string][]string // code → every URL it was answered with
	answered map[string]bool     // codes handed out; no new link may get one
}

// checkImage writes files into the folder dir, opens a store there and fails
// the test unless it keeps p: it starts; every code in must leads to one of
// its URLs, or nowhere when it has none; every code in opened reads as
// opened; no code in ever leads to a URL it was not answered with, before or
// after one more link is created; and that link gets a code not in
// answered. The new link may get a code the script answered only after the
// crash: no one had it yet. what names the image in a failure.
func checkImage(t *testing.T, dir, what string, files map[string][]byte, p promises) {
	t.Helper()
	must(t, os.RemoveAll(dir))
	for name, b := range files {
		path := filepath.Join(dir, name)
		must(t, os.MkdirAll(filepath.Dir(path), 0o700))
		must(t, os.WriteFile(path, b, 0o600))
	}
	st, err := OpenStore(dir)
	if err != nil {
		t.Fatalf("%s: the store does not start: %v", what, err)
	}
	defer st.abandon()
	st.noSync = true // nothing is read back from this folder after a crash
	lookup := func(code string) (Link, LinkState) {
		t.Helper()
		l, state, err := st.Lookup(1, code)
		if err != nil {
			t.Fatalf("%s: lookup of %s: %v", what, code, err)
		}
		return l, state
	}

	for code, urls := range p.must {
		l, state := lookup(code)
		switch {
		case len(urls) == 0 && state == LinkFound:
			t.Fatalf("%s: deleted link %s leads to %q", what, code, l.URL)
		case len(urls) > 0 && (state != LinkFound || !slices.Contains(urls, l.URL)):
			t.Fatalf("%s: answered link %s is %v %q, want one of %q", what, code, state, l.URL, urls)
		}
	}
	for code := range p.opened {
		if l, _ := lookup(code); !l.Opened {
			t.Fatalf("%s: opened link %s does not read as opened", what, code)
		}
	}
	var fresh string // the code of the link created here
	leadsNowhereElse := func(when string) {
		t.Helper()
		for code, urls := range p.ever {
			if l, state := lookup(code); code != fresh && state == LinkFound && !slices.Contains(urls, l.URL) {
				t.Fatalf("%s, %s: code %s leads to %q; it was answered with %q", what, when, code, l.URL, urls)
			}
		}
	}
	leadsNowhereElse("at start")
	created, err := st.Create(1, 1, 5, anyCode, []NewLink{regular("https://new.example/x")})
	if err != nil {
		t.Fatalf("%s: create: %v", what, err)
	}
	if fresh = created[0].Code; p.answered[fresh] {
		t.Fatalf("%s: code %s handed out again", what, fresh)
	}
	leadsNowhereElse("after a create")
}

// write is one write to the chunk file, as onWrite saw it.
type write struct {
	offset int64
	b      []byte
}

// apply returns disk with the writes whose bit is set in mask made, in
// order.
func apply(disk []byte, writes []write, mask int) []byte {
	img := bytes.Clone(disk)
	for i, w := range writes {
		if mask&(1<<i) == 0 {
			continue
		}
		if end := int(w.offset) + len(w.b); end > len(img) {
			img = append(img, make([]byte, end-len(img))...)
		}
		copy(img[w.offset:], w.b)
	}
	return img
}

// crashPoint is a moment a crash can come: the chunk file as the disk held
// it after the last sync, the writes made since, any of which may have
// reached the disk, state.json, and what had been answered by then. ever
// is filled in at the end, when every URL is known.
type crashPoint struct {
	disk    []byte
	pending []write
	state   []byte
	promises
}

// crashHarness follows the chunk file of a store running a script: the
// image the disk holds after the last sync, and the writes made since. At
// every sync it keeps a crash point. The store's syncing goroutine syncs
// while the script goes on, so mu guards everything below it.
type crashHarness struct {
	t   *testing.T
	dir string // the data folder

	mu      sync.Mutex
	disk    []byte
	pending []write
	points  []crashPoint
	// promises as the script stands; at the end ever holds every URL
	// every code was answered with.
	promises
}

// newCrashHarness returns a harness for a new data folder.
func newCrashHarness(t *testing.T) *crashHarness {
	return &crashHarness{
		t: t, dir: filepath.Join(t.TempDir(), "data"), disk: []byte{},
		promises: promises{
			must: map[string][]string{}, opened: map[string]bool{},
			ever: map[string][]string{}, answered: map[string]bool{},
		},
	}
}

// open opens a store on the data folder, its chunk file followed by the
// harness. A sync that fails drops the writes it should have saved from
// the disk image; the live file still reads as if they had been saved,
// which is what Linux can do.
func (h *crashHarness) open() *Store {
	h.t.Helper()
	st, err := OpenStore(h.dir)
	must(h.t, err)
	h.t.Cleanup(st.abandon)
	path := filepath.Join(h.dir, chunkFileName)
	st.onWrite = func(file string, offset int64, b []byte) {
		if file != path {
			h.t.Errorf("a write to %s; the harness follows %s only", file, path)
		}
		h.mu.Lock()
		defer h.mu.Unlock()
		h.pending = append(h.pending, write{offset, bytes.Clone(b)})
	}
	st.onSync = func(string) {
		h.mu.Lock()
		defer h.mu.Unlock()
		h.keepPoint()
		if st.failSync == nil {
			h.disk = apply(h.disk, h.pending, 1<<len(h.pending)-1)
			// The sync makes the size durable too: the file was extended
			// past the slots without a write.
			info, err := os.Stat(path)
			must(h.t, err)
			h.disk = append(h.disk, make([]byte, max(0, int(info.Size())-len(h.disk)))...)
		}
		h.pending = nil
	}
	return st
}

// create stores links for account 1 on domain 1 and notes what was answered.
func (h *crashHarness) create(st *Store, links ...NewLink) []Link {
	h.t.Helper()
	created, err := st.Create(1, 1, 5, anyCode, links)
	must(h.t, err)
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, l := range created {
		h.must[l.Code] = []string{l.URL}
		h.ever[l.Code] = []string{l.URL}
		h.answered[l.Code] = true
	}
	return created
}

// change changes the promises as the script goes on.
func (h *crashHarness) change(promise func()) {
	h.mu.Lock()
	defer h.mu.Unlock()
	promise()
}

// settle waits for the pass that covers every write made to the chunk file
// so far, as clock.tick waits for the next pass. An edit or a delete returns
// before the pass that syncs the zeros it erased with has ended, and a write
// the script made while that pass runs would be counted as synced when the
// pass's sync begins, although that sync need not cover it. When the pass
// has begun already, the one queued here comes after it, and syncs nothing:
// nothing is left to sync.
func (h *crashHarness) settle(st *Store) {
	h.t.Helper()
	f, err := st.acquire(chunkID{1, 5, 0})
	must(h.t, err)
	defer st.release(f)
	st.mu.Lock()
	p := st.toSync(f)
	st.mu.Unlock()
	<-p.done
	must(h.t, p.err)
}

// point keeps the present moment as a crash point.
func (h *crashHarness) point() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.keepPoint()
}

// keepPoint is point with mu held.
func (h *crashHarness) keepPoint() {
	state, err := os.ReadFile(filepath.Join(h.dir, "d1", "state.json"))
	must(h.t, err)
	h.points = append(h.points, crashPoint{
		disk: h.disk, pending: slices.Clone(h.pending), state: state,
		promises: promises{must: maps.Clone(h.must), opened: maps.Clone(h.opened), answered: maps.Clone(h.answered)},
	})
}

// check opens a store on every disk image of every crash point and fails
// the test unless it keeps the promises there.
func (h *crashHarness) check() {
	h.t.Helper()
	key, err := os.ReadFile(filepath.Join(h.dir, "key"))
	must(h.t, err)
	h.mu.Lock()
	defer h.mu.Unlock()
	dir := filepath.Join(h.t.TempDir(), "image")
	images := 0
	for i, p := range h.points {
		if len(p.pending) > 12 {
			h.t.Fatalf("crash point %d has %d writes pending: %d images are too many to try", i, len(p.pending), 1<<len(p.pending))
		}
		p.ever = h.ever
		for mask := range 1 << len(p.pending) {
			files := map[string][]byte{"key": key, "d1/state.json": p.state, chunkFileName: apply(p.disk, p.pending, mask)}
			checkImage(h.t, dir, fmt.Sprintf("crash point %d with writes %0*b of %d", i, len(p.pending), mask, len(p.pending)), files, p.promises)
			images++
		}
	}
	h.t.Logf("%d crash points, %d disk images", len(h.points), images)
}

// Every moment a crash can happen is tried: after each sync, with every
// possible subset of the writes made since the previous sync having reached
// the disk. Whatever survives, a link that was answered opens to the URL
// that was answered (or, during an edit, the URL before or after it), and
// no code ever leads anywhere else.
//
// A new link must get a code not answered before the crash. Codes the
// script hands out after that moment never were, as far as the store on
// that image can know, and one of them may be handed out again.
func TestEveryCrashPointKeepsThePromise(t *testing.T) {
	captureLog(t)
	h := newCrashHarness(t)
	st := h.open()
	first := h.create(st, regular("https://go.example/a"), regular("https://go.example/b"))
	h.create(st, regular("https://example.org/c"))
	edited, deleted := first[0], first[1]

	// While the URL changes, to http and a host the table does not have yet,
	// the link leads to the old URL or the new one; once Update returned,
	// to the new one.
	url := "http://example.com/a2"
	h.change(func() {
		h.ever[edited.Code] = append(h.ever[edited.Code], url)
		h.must[edited.Code] = []string{edited.URL, url}
	})
	_, err := st.Update(1, edited.Code, 1, &url, nil)
	must(t, err)
	h.change(func() { h.must[edited.Code] = []string{url} })
	h.settle(st) // the old record's erase

	// While the delete runs the link may still be there; once Delete
	// returned, it leads nowhere.
	h.change(func() { delete(h.must, deleted.Code) })
	must(t, st.Delete(1, deleted.Code, 1))
	h.change(func() { h.must[deleted.Code] = nil })
	h.settle(st) // the deleted record's erase

	none := time.Duration(0)
	secured := h.create(st, NewLink{URL: "https://go.example/secret", ExpiresAt: in(time.Hour), AfterOpen: &none})[0]
	_, ok, err := st.Open(1, secured.Code, time.Now())
	must(t, err)
	eq(t, ok, true)
	h.change(func() { h.opened[secured.Code] = true })

	h.point()
	h.check()
}

// A sync fails and the writes it should have saved never reach the disk,
// although the files still read as if they had (which is what Linux can do).
// The store is opened again on those files, more links are created to the
// same new host and their sync succeeds; then the machine loses power. Every
// link answered after the restart must still open.
//
// A known limit this test does not cover: a one-time link whose "opened"
// mark was written in a sync that then failed can read as opened after the
// restart, although nobody received its URL.
func TestFailedSyncThenRestartKeepsThePromise(t *testing.T) {
	captureLog(t)
	h := newCrashHarness(t)
	st := h.open()
	h.create(st, regular("https://go.example/a"))

	failure := errors.New("input/output error")
	st.fatal = func(error) {}
	st.failSync = failure
	if _, err := st.Create(1, 1, 5, anyCode, []NewLink{regular("https://example.org/b")}); !errors.Is(err, failure) {
		t.Fatalf("Create returned %v, want %v", err, failure)
	}
	st.abandon()

	st = h.open()
	h.create(st, regular("https://example.org/c"), regular("https://example.org/d"))
	h.point()
	h.check()
}

// smallChunk makes a data folder with links of every kind, changed in every
// way, as a crash leaves it. It returns the folder's files, the promises
// they must keep, and the parts of the chunk file that hold something:
// header, host table with the byte that ends it, used slots and records.
func smallChunk(t *testing.T) (map[string][]byte, promises, [][2]int) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "data")
	st, err := OpenStore(dir)
	must(t, err)
	window := 10 * time.Minute
	salt, hash := bytes.Repeat([]byte{1}, saltSize), bytes.Repeat([]byte{2}, hashSize)
	links, err := st.Create(1, 1, 5, anyCode, []NewLink{
		regular("https://go.example/a"),
		regular("https://go.example/b"),
		regular("http://example.org/c"),
		{URL: "https://go.example/w", ExpiresAt: in(time.Hour), AfterOpen: &window},
		{URL: "https://example.com/p", ExpiresAt: in(time.Hour), Salt: salt, Hash: hash},
		regular("https://" + strings.Repeat("h", 250) + ".example/i"), // too long for the host table
	})
	must(t, err)
	p := promises{ever: map[string][]string{}, answered: map[string]bool{}}
	for _, l := range links {
		p.ever[l.Code] = []string{l.URL}
		p.answered[l.Code] = true
	}
	moved := "http://example.com/b2"
	_, err = st.Update(1, links[1].Code, 1, &moved, nil)
	must(t, err)
	p.ever[links[1].Code] = append(p.ever[links[1].Code], moved)
	must(t, st.Delete(1, links[2].Code, 1))
	_, ok, err := st.Open(1, links[3].Code, time.Now())
	must(t, err)
	eq(t, ok, true)
	st.abandon()

	files := map[string][]byte{}
	for _, name := range []string{"key", "d1/state.json", chunkFileName} {
		b, err := os.ReadFile(filepath.Join(dir, name))
		must(t, err)
		files[name] = b
	}
	c, err := openChunk(filepath.Join(dir, chunkFileName))
	must(t, err)
	defer c.close()
	return files, p, [][2]int{
		{0, headerSize},
		{headerSize, int(c.tableEnd) + 1},
		{slotsOffset, slotsOffset + len(links)*slotSize},
		{recordsOffset, len(files[chunkFileName])},
	}
}

// One byte at a time is changed in everything a small chunk holds: header,
// host table, used slots, records. Each time the store must start, and every
// code must lead to its own URL or nowhere.
func TestDamagedBytesNeverMisdirect(t *testing.T) {
	captureLog(t)
	files, p, regions := smallChunk(t)
	chunk := files[chunkFileName]
	dir := filepath.Join(t.TempDir(), "image")
	images := 0
	for _, r := range regions {
		for at := r[0]; at < r[1]; at++ {
			damaged := bytes.Clone(chunk)
			damaged[at] ^= 1 << (at % 8) // a bit that moves along, so that every position in a byte is tried
			files[chunkFileName] = damaged
			checkImage(t, dir, fmt.Sprintf("byte %d changed", at), files, p)
			images++
		}
	}
	t.Logf("%d damaged images", images)
}

// The file is cut at every length from 0 to its full size in the regions
// that hold something, and at 4,096-byte steps elsewhere.
func TestCutFileNeverMisdirects(t *testing.T) {
	captureLog(t)
	files, p, regions := smallChunk(t)
	chunk := files[chunkFileName]
	lengths := map[int]bool{len(chunk): true}
	for n := 0; n < len(chunk); n += 4096 {
		lengths[n] = true
	}
	for _, r := range regions {
		for n := r[0]; n <= r[1]; n++ {
			lengths[n] = true
		}
	}
	dir := filepath.Join(t.TempDir(), "image")
	for _, n := range slices.Sorted(maps.Keys(lengths)) {
		files[chunkFileName] = chunk[:n]
		checkImage(t, dir, fmt.Sprintf("cut at %d bytes", n), files, p)
	}
	t.Logf("%d lengths", len(lengths))
}

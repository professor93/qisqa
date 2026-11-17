// Tests for the store: the data folder, creating and looking up links, and
// starting again after a clean stop, a crash, a failing disk or lost files.
package main

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// newTestStore opens a store in a fresh folder and closes it with the test.
func newTestStore(t *testing.T) *Store {
	t.Helper()
	st, err := OpenStore(filepath.Join(t.TempDir(), "data"))
	must(t, err)
	t.Cleanup(func() { st.Close() })
	return st
}

// anyCode accepts every code.
func anyCode(string) bool { return true }

// create stores n regular links owned by account 1 on domain 1 and returns them.
func create(t *testing.T, st *Store, n int) []Link {
	t.Helper()
	links := make([]NewLink, n)
	for i := range links {
		links[i] = NewLink{URL: fmt.Sprintf("https://example.com/p/%d", i), ExpiresAt: in(time.Hour)}
	}
	created, err := st.Create(1, 1, 5, anyCode, links)
	must(t, err)
	return created
}

// oneLink is a batch of one regular link.
func oneLink() []NewLink {
	return []NewLink{{URL: "https://example.org/one", ExpiresAt: in(time.Hour)}}
}

// numberOf returns the link number a code of domain 1 stands for.
func numberOf(t *testing.T, st *Store, code string) uint64 {
	t.Helper()
	_, m, ok := NewCoder(st.key, 1).Number(code)
	if !ok {
		t.Fatalf("%q is not a code", code)
	}
	return m
}

// firstChunk is the path of the first chunk file of length 5 on domain 1.
func firstChunk(dir string) string {
	return filepath.Join(dir, "d1", "5-000000000000.chunk")
}

// found fails the test unless every link opens on domain 1 to its URL.
func found(t *testing.T, st *Store, links []Link) {
	t.Helper()
	for _, l := range links {
		got, state, err := st.Lookup(1, l.Code)
		must(t, err)
		eq(t, state, LinkFound)
		eq(t, got.URL, l.URL)
	}
}

// allDiffer fails the test if a code appears twice among the links.
func allDiffer(t *testing.T, links ...[]Link) {
	t.Helper()
	seen := map[string]bool{}
	for _, batch := range links {
		for _, l := range batch {
			if seen[l.Code] {
				t.Fatalf("code %s handed out twice", l.Code)
			}
			seen[l.Code] = true
		}
	}
}

func TestStoreCreatesDataFolder(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	st, err := OpenStore(dir)
	must(t, err)
	info, err := os.Stat(dir)
	must(t, err)
	eq(t, info.Mode().Perm(), os.FileMode(0o700))

	keyPath := filepath.Join(dir, "key")
	key, err := os.ReadFile(keyPath)
	must(t, err)
	eq(t, len(key), 32)
	info, err = os.Stat(keyPath)
	must(t, err)
	eq(t, info.Mode().Perm(), os.FileMode(0o600))
	must(t, st.Close())

	st, err = OpenStore(dir)
	must(t, err)
	defer st.Close()
	again, err := os.ReadFile(keyPath)
	must(t, err)
	eq(t, bytes.Equal(again, key), true)
}

func TestStoreRefusesSecondProcess(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	st, err := OpenStore(dir)
	must(t, err)
	_, err = OpenStore(dir)
	wantErr(t, err, "in use")
	wantErr(t, err, dir)

	must(t, st.Close())
	must(t, st.Close()) // a second Close does nothing
	st, err = OpenStore(dir)
	must(t, err)
	must(t, st.Close())
}

func TestStoreRejectsBadKey(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	must(t, os.MkdirAll(dir, 0o700))
	keyPath := writeFile(t, dir, "key", "short")
	_, err := OpenStore(dir)
	wantErr(t, err, keyPath)

	// The refusal let go of the folder.
	must(t, os.WriteFile(keyPath, make([]byte, 32), 0o600))
	st, err := OpenStore(dir)
	must(t, err)
	must(t, st.Close())
}

func TestStoreRefusesToReplaceALostKey(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	st, err := OpenStore(dir)
	must(t, err)
	old := create(t, st, 2)
	must(t, st.Close())

	keyPath := filepath.Join(dir, "key")
	key, err := os.ReadFile(keyPath)
	must(t, err)
	must(t, os.Remove(keyPath))
	_, err = OpenStore(dir)
	wantErr(t, err, "key is missing")
	if _, err := os.Stat(keyPath); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("a refused start left a key file behind: %v", err)
	}

	must(t, os.WriteFile(keyPath, key, 0o600))
	st, err = OpenStore(dir)
	must(t, err)
	defer st.Close()
	found(t, st, old)
}

func TestCreateAndLookup(t *testing.T) {
	st := newTestStore(t)
	expires := time.Date(2027, 3, 4, 5, 6, 7, 890_000_000, time.UTC)
	links := make([]NewLink, 3)
	for i := range links {
		links[i] = NewLink{URL: fmt.Sprintf("https://example.com/p/%d", i), ExpiresAt: expires}
	}
	created, err := st.Create(1, 7, 5, anyCode, links)
	must(t, err)
	eq(t, len(created), 3)
	allDiffer(t, created)

	for i, l := range created {
		eq(t, len(l.Code), 5)
		got, state, err := st.Lookup(1, l.Code)
		must(t, err)
		eq(t, state, LinkFound)
		eq(t, got.Code, l.Code)
		eq(t, got.URL, links[i].URL)
		eq(t, got.Owner, uint32(7))
		if !got.ExpiresAt.Equal(expires.Truncate(time.Second)) {
			t.Fatalf("expires at %v, want %v", got.ExpiresAt, expires.Truncate(time.Second))
		}
		eq(t, got.ExpiresAt.Equal(l.ExpiresAt), true)

		_, state, err = st.Lookup(2, l.Code)
		must(t, err)
		if state == LinkFound {
			t.Fatalf("code %s of domain 1 found on domain 2", l.Code)
		}
	}
}

func TestCreateKeepsSecuredAndPasswordLinks(t *testing.T) {
	st := newTestStore(t)
	minute, none := time.Minute+time.Millisecond, time.Duration(0)
	salt, hash := bytes.Repeat([]byte{1}, saltSize), bytes.Repeat([]byte{2}, hashSize)
	created, err := st.Create(1, 1, 5, anyCode, []NewLink{
		{URL: "http://example.com/window", ExpiresAt: in(time.Hour), AfterOpen: &minute},
		{URL: "https://example.com/once", ExpiresAt: in(time.Hour), AfterOpen: &none},
		{URL: "https://example.com/locked", ExpiresAt: in(time.Hour), Salt: salt, Hash: hash},
	})
	must(t, err)
	got := make([]Link, len(created))
	for i, l := range created {
		var state LinkState
		got[i], state, err = st.Lookup(1, l.Code)
		must(t, err)
		eq(t, state, LinkFound)
		eq(t, got[i].Opened, false)
	}
	eq(t, got[0].URL, "http://example.com/window")
	eq(t, got[0].Secured, true)
	eq(t, got[0].AfterOpen, time.Minute+time.Second) // whole seconds, rounded up
	eq(t, got[1].Secured, true)
	eq(t, got[1].AfterOpen, time.Duration(0))
	eq(t, got[2].Secured, false)
	eq(t, got[2].Password, true)
	eq(t, bytes.Equal(got[2].Salt, salt) && bytes.Equal(got[2].Hash, hash), true)
}

// The record's check does not cover the domain, so the header does: a
// chunk file put into another domain's folder leads nowhere, and the domain
// goes on with its following chunk.
func TestChunkInAnotherDomainsFolderLeadsNowhere(t *testing.T) {
	logs := captureLog(t)
	dir := filepath.Join(t.TempDir(), "data")
	st, err := OpenStore(dir)
	must(t, err)
	create(t, st, 1)
	second, err := st.Create(2, 1, 5, anyCode, oneLink())
	must(t, err)
	must(t, st.Close())
	b, err := os.ReadFile(firstChunk(dir))
	must(t, err)
	must(t, os.WriteFile(filepath.Join(dir, "d2", "5-000000000000.chunk"), b, 0o600))

	st, err = OpenStore(dir)
	must(t, err)
	defer st.Close()
	got, state, err := st.Lookup(2, second[0].Code)
	must(t, err)
	if state == LinkFound {
		t.Fatalf("a link of domain 2 leads to %q", got.URL)
	}
	created, err := st.Create(2, 1, 5, anyCode, oneLink())
	must(t, err)
	allDiffer(t, second, created)
	if !strings.Contains(logs.String(), "its header names") {
		t.Fatalf("the log does not say what is wrong:\n%s", logs)
	}
}

// A chunk file whose header is damaged, or belongs to another file, is lost
// as a missing one is: its links are gone, the rest of the domain works,
// and new links go into the following chunk.
func TestDamagedHeaderLosesTheChunk(t *testing.T) {
	for name, at := range map[string]int64{
		"magic":        0,
		"version":      8,
		"another name": 12, // the low byte of the chunk index
	} {
		t.Run(name, func(t *testing.T) {
			logs := captureLog(t)
			dir := filepath.Join(t.TempDir(), "data")
			st, err := OpenStore(dir)
			must(t, err)
			old := create(t, st, 2)
			other, err := st.Create(1, 1, 7, anyCode, oneLink()) // another file of the domain
			must(t, err)
			must(t, st.Close())
			path := firstChunk(dir)
			f, err := os.OpenFile(path, os.O_RDWR, 0)
			must(t, err)
			b := make([]byte, 1)
			_, err = f.ReadAt(b, at)
			must(t, err)
			_, err = f.WriteAt([]byte{b[0] ^ 1}, at)
			must(t, err)
			must(t, f.Close())

			st, err = OpenStore(dir)
			must(t, err)
			defer st.Close()
			for range 2 {
				for _, l := range old {
					_, state, err := st.Lookup(1, l.Code)
					must(t, err)
					eq(t, state, LinkGone)
				}
			}
			found(t, st, other)
			fresh := create(t, st, 1)
			allDiffer(t, old, other, fresh)
			eq(t, numberOf(t, st, fresh[0].Code)/chunkLinks, uint64(1))

			// Named once, however often its links are looked up.
			eq(t, strings.Count(logs.String(), "lead nowhere\" file="+path), 1)
		})
	}
}

func TestCreateRefusesAURLItCannotStore(t *testing.T) {
	st := newTestStore(t)
	_, err := st.Create(1, 1, 5, anyCode, []NewLink{
		{URL: "https://example.com/fine", ExpiresAt: in(time.Hour)},
		{URL: "ftp://example.com/file", ExpiresAt: in(time.Hour)},
	})
	wantErr(t, err, "ftp://example.com/file")
	created := create(t, st, 1)
	eq(t, numberOf(t, st, created[0].Code), uint64(0)) // the refused batch used up nothing
}

// A link the store cannot keep is refused before any number is taken and
// before any link of its batch is written, as a URL it cannot store is.
func TestCreateRefusesALinkItCannotStore(t *testing.T) {
	salt, hash := bytes.Repeat([]byte{1}, saltSize), bytes.Repeat([]byte{2}, hashSize)
	// largest is a secured password link whose record, with its host written
	// into it, takes 65,535 bytes and extra: 4 for the check, 4 for
	// afterOpen, the salt and the hash, 1 for the host id, 1 and 11 for the
	// host, and the rest of the URL.
	largest := func(extra int) NewLink {
		none := time.Duration(0)
		rest := "/" + strings.Repeat("a", math.MaxUint16-4-4-saltSize-hashSize-1-1-11-1+extra)
		return NewLink{URL: "https://example.com" + rest, ExpiresAt: in(time.Hour), AfterOpen: &none, Salt: salt, Hash: hash}
	}
	st := newTestStore(t)
	found(t, st, []Link{createOne(t, st, largest(0))})

	for name, l := range map[string]NewLink{
		"short salt":     {Salt: salt[:saltSize-1], Hash: hash},
		"long hash":      {Salt: salt, Hash: append(hash, 0)},
		"salt only":      {Salt: salt},
		"hash only":      {Hash: hash},
		"record too big": largest(1),
	} {
		t.Run(name, func(t *testing.T) {
			st := newTestStore(t)
			if l.URL == "" {
				l.URL = "https://example.com/refused"
			}
			l.ExpiresAt = in(time.Hour)
			fine := NewLink{URL: "https://example.com/fine", ExpiresAt: in(time.Hour)}
			_, err := st.Create(1, 1, 5, anyCode, []NewLink{fine, l})
			if err == nil {
				t.Fatal("the link was stored")
			}
			_, err = os.Stat(firstChunk(st.dir))
			if !errors.Is(err, fs.ErrNotExist) {
				t.Fatalf("the refused batch wrote a chunk file: %v", err)
			}
			created := create(t, st, 1)
			eq(t, numberOf(t, st, created[0].Code), uint64(0)) // the refused batch used up nothing
		})
	}
}

func TestLookupUnknownCode(t *testing.T) {
	st := newTestStore(t)
	for _, code := range []string{"zzzzz", "ab", "abc-1"} {
		_, state, err := st.Lookup(1, code)
		must(t, err)
		eq(t, state, LinkUnknown)
	}
	eq(t, len(st.files), 0) // none of them needed a file

	create(t, st, 1)
	_, state, err := st.Lookup(1, NewCoder(st.key, 1).Code(5, 1))
	must(t, err)
	eq(t, state, LinkUnknown)
}

func TestCreateSkipsUnacceptableCodes(t *testing.T) {
	st := newTestStore(t)
	var asked []string
	acceptable := func(code string) bool {
		asked = append(asked, code)
		return len(asked) > 2
	}
	created, err := st.Create(1, 1, 5, acceptable, oneLink())
	must(t, err)
	eq(t, len(asked), 3)
	eq(t, created[0].Code, asked[2])
	for _, code := range asked[:2] {
		_, state, err := st.Lookup(1, code)
		must(t, err)
		eq(t, state, LinkUnknown)
	}
}

// A link gives up after 1,000 rejected codes in a row, rather than running
// through every number of its length under the write lock.
func TestCreateGivesUpWhenEveryCodeIsRejected(t *testing.T) {
	st, err := OpenStore(filepath.Join(t.TempDir(), "data"))
	must(t, err)
	asked := 0
	result := make(chan error, 1)
	go func() {
		_, err := st.Create(1, 1, 5, func(string) bool { asked++; return false }, oneLink())
		result <- err
	}()
	select {
	case err := <-result:
		if !errors.Is(err, ErrNoCode) {
			t.Fatalf("Create returned %v, want ErrNoCode", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Create does not give up") // the store stays open: closing it would wait for the lock
	}
	eq(t, asked, 1000)
	if _, err := os.Stat(filepath.Join(st.dir, "d1", "5-000000000001.chunk")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("a second chunk was started: %v", err)
	}
	// The numbers it tried stay used.
	eq(t, numberOf(t, st, create(t, st, 1)[0].Code), uint64(1000))

	// The count is per link: each of these two sees 999 rejections.
	n := 0
	created, err := st.Create(1, 1, 5, func(string) bool { n++; return n%1000 == 0 }, append(oneLink(), oneLink()...))
	must(t, err)
	eq(t, len(created), 2)
	must(t, st.Close())
}

// acceptable is the caller's code. When it panics the panic reaches the
// caller, and the store stays usable: the write lock and the file are let go.
func TestPanicInAcceptableLetsGoOfTheStore(t *testing.T) {
	st, err := OpenStore(filepath.Join(t.TempDir(), "data"))
	must(t, err)
	old := create(t, st, 1)
	recovered := make(chan any, 1)
	go func() {
		defer func() { recovered <- recover() }()
		st.Create(1, 1, 5, func(string) bool { panic("acceptable failed") }, oneLink())
	}()
	eq(t, <-recovered, any("acceptable failed"))

	done := make(chan struct{})
	go func() {
		defer close(done)
		if _, err := st.Create(1, 1, 5, anyCode, oneLink()); err != nil {
			t.Errorf("Create after the panic: %v", err)
		}
		if _, state, err := st.Lookup(1, old[0].Code); err != nil || state != LinkFound {
			t.Errorf("Lookup after the panic: %v, %v", state, err)
		}
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the store is still locked after the panic") // left open: closing it would wait too
	}
	eq(t, st.files[chunkID{1, 5, 0}].users.Load(), 0)
	must(t, st.Close())
}

func TestCreateBatchIsOneSync(t *testing.T) {
	st := newTestStore(t)
	create(t, st, 1)
	before := st.syncs()
	create(t, st, 20)
	eq(t, st.syncs()-before, int64(1))
}

func TestCreateGrowsWhenLengthIsFull(t *testing.T) {
	st := newTestStore(t)
	const full = 23832 // 62^3 / 10
	for done := 0; done < full; {
		batch := make([]NewLink, min(1000, full-done))
		for i := range batch {
			batch[i] = NewLink{URL: fmt.Sprintf("https://example.com/p/%d", done+i), ExpiresAt: in(time.Hour)}
		}
		created, err := st.Create(1, 1, 3, anyCode, batch)
		must(t, err)
		for _, l := range created {
			if len(l.Code) != 3 {
				t.Fatalf("link %d got code %q", done, l.Code)
			}
		}
		done += len(batch)
	}
	next, err := st.Create(1, 1, 3, anyCode, oneLink())
	must(t, err)
	eq(t, len(next[0].Code), 4)
}

// Going back to a length continues where that length stopped.
func TestMinLengthChange(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	st, err := OpenStore(dir)
	must(t, err)
	var all []Link
	add := func(minLength int, number uint64) {
		t.Helper()
		created, err := st.Create(1, 1, minLength, anyCode, oneLink())
		must(t, err)
		eq(t, len(created[0].Code), minLength)
		eq(t, numberOf(t, st, created[0].Code), number)
		all = append(all, created[0])
		allDiffer(t, all)
	}
	add(5, 0)
	add(7, 0)
	add(5, 1)
	must(t, st.Close())

	st, err = OpenStore(dir)
	must(t, err)
	defer st.Close()
	add(5, 2)
	add(7, 1)
}

func TestCodesNeverRepeatAcrossRestart(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	st, err := OpenStore(dir)
	must(t, err)
	first := create(t, st, 10)
	must(t, st.Close())

	st, err = OpenStore(dir)
	must(t, err)
	defer st.Close()
	seen := map[string]bool{}
	for _, l := range append(first, create(t, st, 10)...) {
		if seen[l.Code] {
			t.Fatalf("code %s handed out twice", l.Code)
		}
		seen[l.Code] = true
	}
	for _, l := range first {
		got, state, err := st.Lookup(1, l.Code)
		must(t, err)
		eq(t, state, LinkFound)
		eq(t, got.URL, l.URL)
	}
}

func TestStartupAfterCrash(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	st, err := OpenStore(dir)
	must(t, err)
	old := create(t, st, 5)
	st.abandon()

	st, err = OpenStore(dir)
	must(t, err)
	defer st.Close()
	found(t, st, old)
	allDiffer(t, old, create(t, st, 5))
}

// A state.json that does not parse costs only what a missing one would:
// the floor it kept. It is logged, and the domain goes on from its files.
func TestStartupWithDamagedStateFile(t *testing.T) {
	logs := captureLog(t)
	dir := filepath.Join(t.TempDir(), "data")
	st, err := OpenStore(dir)
	must(t, err)
	old := create(t, st, 5)
	must(t, st.Close())
	statePath := filepath.Join(dir, "d1", "state.json")
	must(t, os.WriteFile(statePath, []byte(`{"5":{"chunk":0,"ne`), 0o600))

	st, err = OpenStore(dir)
	must(t, err)
	defer st.Close()
	found(t, st, old)
	allDiffer(t, old, create(t, st, 5))
	eq(t, strings.Count(logs.String(), "level=ERROR"), 1)
	if !strings.Contains(logs.String(), statePath) {
		t.Fatalf("the log does not name %s:\n%s", statePath, logs)
	}
}

func TestStartupWithoutStateFile(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	st, err := OpenStore(dir)
	must(t, err)
	old := create(t, st, 5)
	must(t, st.Close())
	must(t, os.Remove(filepath.Join(dir, "d1", "state.json")))

	st, err = OpenStore(dir)
	must(t, err)
	defer st.Close()
	found(t, st, old)
	allDiffer(t, old, create(t, st, 5))
}

// Slots that lost their links make the file look emptier than it was;
// state.json, written at a clean stop, keeps their numbers from coming back.
func TestStartupWhenSlotsWereZeroed(t *testing.T) {
	logs := captureLog(t)
	dir := filepath.Join(t.TempDir(), "data")
	st, err := OpenStore(dir)
	must(t, err)
	old := create(t, st, 5)
	must(t, st.Close())

	path := firstChunk(dir)
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	must(t, err)
	for _, l := range old[2:] {
		_, err := f.WriteAt(make([]byte, slotSize), slotsOffset+int64(numberOf(t, st, l.Code))*slotSize)
		must(t, err)
	}
	must(t, f.Close())

	st, err = OpenStore(dir)
	must(t, err)
	defer st.Close()
	allDiffer(t, old, create(t, st, 1))
	if !strings.Contains(logs.String(), path) {
		t.Fatalf("the log does not name %s:\n%s", path, logs)
	}
}

func TestStartupWhenCurrentChunkIsGoneOrCut(t *testing.T) {
	for name, damage := range map[string]func(path string) error{
		"removed": os.Remove,
		"cut":     func(path string) error { return os.Truncate(path, 5000) },
	} {
		t.Run(name, func(t *testing.T) {
			logs := captureLog(t)
			dir := filepath.Join(t.TempDir(), "data")
			st, err := OpenStore(dir)
			must(t, err)
			old := create(t, st, 5)
			must(t, st.Close())
			must(t, damage(filepath.Join(dir, "d1", "5-000000000000.chunk")))

			st, err = OpenStore(dir)
			must(t, err)
			defer st.Close()
			fresh := create(t, st, 1)[0]
			for _, l := range old {
				if l.Code == fresh.Code {
					t.Fatalf("code %s handed out again", l.Code)
				}
				if got, state, _ := st.Lookup(1, l.Code); state == LinkFound {
					t.Fatalf("lost link %s answers %q", l.Code, got.URL)
				}
			}
			// The startup said so once; a lookup of a lost link does not.
			eq(t, strings.Count(logs.String(), "level=ERROR"), 1)
			if !strings.Contains(logs.String(), firstChunk(dir)) {
				t.Fatalf("the log does not name the file:\n%s", logs)
			}
		})
	}
}

// The newest chunk can be closed to make room and removed while closed;
// the next create notices, as a startup would, and moves to the next chunk.
func TestCreateAfterCurrentChunkWasRemoved(t *testing.T) {
	logs := captureLog(t)
	defer func(n int) { maxOpenChunks = n }(maxOpenChunks)
	maxOpenChunks = 1
	st := newTestStore(t)
	old := create(t, st, 3)
	_, err := st.Create(2, 1, 5, anyCode, oneLink()) // closes the file of domain 1
	must(t, err)
	must(t, os.Remove(firstChunk(st.dir)))

	allDiffer(t, old, create(t, st, 1))
	_, err = os.Stat(filepath.Join(st.dir, "d1", "5-000000000001.chunk"))
	must(t, err)
	if !strings.Contains(logs.String(), firstChunk(st.dir)) {
		t.Fatalf("the log does not name the removed file:\n%s", logs)
	}
}

// Only a chunk file that does not exist, or is too short for its slots, is
// lost. Any other error of opening or creating one fails the call and
// changes nothing, so a passing error never uses up a chunk's numbers.
func TestCreateKeepsItsPlaceWhenAChunkCannotBeOpened(t *testing.T) {
	captureLog(t)
	// inTheWay puts a folder where the file at path is (opening a folder as
	// a file fails, even for root) and returns what puts the file back.
	inTheWay := func(t *testing.T, path string) func() {
		t.Helper()
		must(t, os.Rename(path, path+".away"))
		must(t, os.Mkdir(path, 0o700))
		return func() {
			must(t, os.Remove(path))
			must(t, os.Rename(path+".away", path))
		}
	}
	// goesOn fails the test unless creating fails until restore is called,
	// and then hands out link number next with no chunk started after it.
	goesOn := func(t *testing.T, st *Store, restore func(), next uint64) {
		t.Helper()
		if _, err := st.Create(1, 1, 5, anyCode, oneLink()); err == nil {
			t.Fatal("Create succeeded while the chunk file was out of reach")
		}
		restore()
		eq(t, numberOf(t, st, create(t, st, 1)[0].Code), next)
		following := filepath.Join(st.dir, "d1", chunkName(5, next/chunkLinks+1))
		if _, err := os.Stat(following); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("a chunk was started after the one in use: %v", err)
		}
	}

	t.Run("at startup", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "data")
		st, err := OpenStore(dir)
		must(t, err)
		create(t, st, 2)
		must(t, st.Close())
		restore := inTheWay(t, firstChunk(dir))
		st, err = OpenStore(dir)
		must(t, err)
		defer st.Close()
		goesOn(t, st, restore, 2)
	})

	t.Run("after its handle was closed", func(t *testing.T) {
		defer func(n int) { maxOpenChunks = n }(maxOpenChunks)
		maxOpenChunks = 1
		st := newTestStore(t)
		old := create(t, st, 2)
		_, err := st.Create(2, 1, 5, anyCode, oneLink()) // closes the file of domain 1
		must(t, err)
		restore := inTheWay(t, firstChunk(st.dir))
		// A lookup says so too, rather than calling the link gone.
		if _, _, err := st.Lookup(1, old[0].Code); err == nil {
			t.Fatal("Lookup hid the error of opening the file")
		}
		goesOn(t, st, restore, 2)
	})

	t.Run("creating the next chunk", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "data")
		st, err := OpenStore(dir)
		must(t, err)
		create(t, st, 1)
		must(t, st.Close())
		// Numbering goes on at the last number of the first chunk.
		must(t, os.WriteFile(filepath.Join(dir, "d1", "state.json"), []byte(`{"5":{"chunk":0,"next":65535}}`), 0o600))
		st, err = OpenStore(dir)
		must(t, err)
		defer st.Close()
		eq(t, numberOf(t, st, create(t, st, 1)[0].Code), uint64(65535))

		next := filepath.Join(dir, "d1", "5-000000000001.chunk")
		must(t, os.Mkdir(next, 0o700))
		goesOn(t, st, func() { must(t, os.Remove(next)) }, 65536)
	})
}

func TestCreateSurvivesAFailedWrite(t *testing.T) {
	st := newTestStore(t)
	before := create(t, st, 3)
	failure := errors.New("no space left on device")
	st.failWrite = failure
	if _, err := st.Create(1, 1, 5, anyCode, oneLink()); !errors.Is(err, failure) {
		t.Fatalf("Create returned %v, want %v", err, failure)
	}

	st.failWrite = nil
	after := create(t, st, 1)
	allDiffer(t, before, after)
	// The number the failed call took is not used again: part of it may be on disk.
	eq(t, numberOf(t, st, after[0].Code), uint64(4))
	found(t, st, before)
}

// A failed sync is fatal: nothing written to the file since its last good
// sync can be trusted to be on disk, whatever a later sync says. The store
// takes no more writes and qisqa stops, to start again from what is on disk.
func TestFailedSyncIsFatal(t *testing.T) {
	logs := captureLog(t)
	dir := filepath.Join(t.TempDir(), "data")
	st, err := OpenStore(dir)
	must(t, err)
	var fatal []error
	st.fatal = func(err error) { fatal = append(fatal, err) }
	before := create(t, st, 3)

	failure := errors.New("input/output error")
	st.failSync = failure
	_, first := st.Create(1, 1, 5, anyCode, oneLink())
	if !errors.Is(first, failure) {
		t.Fatalf("Create returned %v, want %v", first, failure)
	}
	eq(t, len(fatal), 1)
	eq(t, fatal[0], first)

	st.failSync = nil
	st.onWrite = func(path string, offset int64, b []byte) {
		t.Errorf("a write of %d bytes at byte %d of %s after the failure", len(b), offset, path)
	}
	for _, domain := range []uint16{1, 2} {
		_, err := st.Create(domain, 1, 5, anyCode, oneLink())
		eq(t, err, first)
	}
	st.onWrite = nil
	eq(t, len(fatal), 1)
	found(t, st, before)

	eq(t, strings.Count(logs.String(), "level=ERROR"), 1)
	if line := logs.String(); !strings.Contains(line, firstChunk(dir)) || !strings.Contains(line, failure.Error()) ||
		!strings.Contains(line, "starts again from what is on disk") {
		t.Fatalf("the log does not name the file, the error and what happens next:\n%s", line)
	}
	must(t, st.Close())

	st, err = OpenStore(dir)
	must(t, err)
	defer st.Close()
	allDiffer(t, before, create(t, st, 3))
	// No numbers were skipped: the links stay in the first chunk.
	if _, err := os.Stat(filepath.Join(dir, "d1", "5-000000000001.chunk")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("a second chunk was started: %v", err)
	}
}

// onWrite sees every write, the header of a new file included, before it is
// made.
func TestStoreOnWriteSeesEveryWrite(t *testing.T) {
	st := newTestStore(t)
	path := firstChunk(st.dir)
	var offsets []int64
	st.onWrite = func(file string, offset int64, b []byte) {
		if file != path {
			t.Errorf("a write to %s", file)
		}
		offsets = append(offsets, offset)
	}
	create(t, st, 1)
	// The header, the host table entry, the record and the slot.
	eq(t, fmt.Sprint(offsets), fmt.Sprint([]int64{0, headerSize, recordsOffset, slotsOffset}))
}

// Creates that share a failing sync all fail, but qisqa is stopped and the
// failure logged only once.
func TestConcurrentFailedSyncsAreFatalOnce(t *testing.T) {
	logs := captureLog(t)
	st := newTestStore(t)
	var calls atomic.Int64
	st.fatal = func(error) { calls.Add(1) }
	create(t, st, 1)

	failure := errors.New("input/output error")
	syncing, release := make(chan struct{}), make(chan struct{})
	st.files[chunkID{1, 5, 0}].sync = func() error {
		close(syncing) // a second create waits for this sync and does not run its own
		<-release
		return failure
	}
	errs := make(chan error, 2)
	createOne := func() {
		_, err := st.Create(1, 1, 5, anyCode, oneLink())
		errs <- err
	}
	go createOne()
	<-syncing
	writing := make(chan struct{})
	var once sync.Once
	st.onWrite = func(string, int64, []byte) { once.Do(func() { close(writing) }) }
	go createOne() // past the failure check once it writes
	<-writing
	close(release)

	for range 2 {
		if err := <-errs; !errors.Is(err, failure) {
			t.Fatalf("Create returned %v, want %v", err, failure)
		}
	}
	eq(t, calls.Load(), int64(1))
	eq(t, strings.Count(logs.String(), "level=ERROR"), 1)
}

// A store going on with a file it did not create writes the header and host
// table back first: after a failed sync the system may hold them as saved
// although they never reached the disk. A file opened only to read is not
// written to.
func TestContinuedChunkWritesItsTableAgain(t *testing.T) {
	defer func(n int) { maxOpenChunks = n }(maxOpenChunks)
	maxOpenChunks = 1
	dir := filepath.Join(t.TempDir(), "data")
	st, err := OpenStore(dir)
	must(t, err)
	old := create(t, st, 1)
	must(t, st.Close())
	path := firstChunk(dir)
	b, err := os.ReadFile(path)
	must(t, err)
	head := b[:slotsOffset]

	type write struct {
		offset int64
		b      []byte
	}
	var writes []write
	st, err = OpenStore(dir)
	must(t, err)
	defer st.Close()
	st.onWrite = func(file string, offset int64, b []byte) {
		if file == path {
			writes = append(writes, write{offset, bytes.Clone(b)})
		}
	}
	firstWrite := func(when string) {
		t.Helper()
		if len(writes) < 2 || writes[0].offset != 0 || !bytes.Equal(writes[0].b, head) {
			t.Fatalf("%s, the first of %d writes is not the 8,192 bytes the file began with", when, len(writes))
		}
		writes = nil
	}

	found(t, st, old)
	eq(t, len(writes), 0)
	create(t, st, 1)
	firstWrite("at startup")

	// Its handle closed to make room, the file is opened again.
	_, err = st.Create(2, 1, 5, anyCode, oneLink())
	must(t, err)
	create(t, st, 1)
	firstWrite("after the handle was closed")
}

func TestCreateRefusesOwnerOutOfRange(t *testing.T) {
	st := newTestStore(t)
	if _, err := st.Create(1, 1<<24, 5, anyCode, oneLink()); err == nil {
		t.Fatal("an owner of 24 bits and more was accepted")
	}
	created, err := st.Create(1, 1<<24-1, 5, anyCode, oneLink())
	must(t, err)
	eq(t, numberOf(t, st, created[0].Code), uint64(0)) // no code was used up
	got, state, err := st.Lookup(1, created[0].Code)
	must(t, err)
	eq(t, state, LinkFound)
	eq(t, got.Owner, uint32(1<<24-1))
}

// A damaged slot or record makes its link gone, never another one, and the
// damage is logged.
func TestLookupOfDamagedLinkIsGone(t *testing.T) {
	logs := captureLog(t)
	st := newTestStore(t)
	links := create(t, st, 2)
	path := firstChunk(st.dir)
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	must(t, err)
	defer f.Close()
	flip := func(at int64) {
		t.Helper()
		b := make([]byte, 1)
		_, err := f.ReadAt(b, at)
		must(t, err)
		b[0] ^= 0x40
		_, err = f.WriteAt(b, at)
		must(t, err)
	}

	// The expiry of the first link's slot.
	flip(slotsOffset + int64(numberOf(t, st, links[0].Code))*slotSize + 6)
	// The last byte of the second link's record.
	raw := make([]byte, slotSize)
	_, err = f.ReadAt(raw, slotsOffset+int64(numberOf(t, st, links[1].Code))*slotSize)
	must(t, err)
	s, state := decodeSlot(raw)
	eq(t, state, slotOK)
	flip(recordsOffset + int64(s.offset) + int64(s.length) - 1)

	for _, l := range links {
		_, state, err := st.Lookup(1, l.Code)
		must(t, err)
		eq(t, state, LinkGone)
	}
	eq(t, strings.Count(logs.String(), "level=ERROR"), 2)
	if !strings.Contains(logs.String(), path) {
		t.Fatalf("the log does not name the file:\n%s", logs)
	}
}

// A lookup runs alongside writes, so it may read a slot half-way through
// one. It reads again under the write lock before it calls the link damaged.
func TestLookupReadsAgainBeforeCallingDamage(t *testing.T) {
	logs := captureLog(t)
	st := newTestStore(t)
	earlier := create(t, st, 1)[0]
	f, err := os.OpenFile(firstChunk(st.dir), os.O_RDWR, 0)
	must(t, err)
	defer f.Close()
	at := slotsOffset + int64(numberOf(t, st, earlier.Code))*slotSize

	result := make(chan LinkState, 1)
	var once sync.Once
	st.onWrite = func(string, int64, []byte) {
		once.Do(func() {
			good := make([]byte, slotSize)
			if _, err := f.ReadAt(good, at); err != nil {
				t.Error(err)
			}
			if _, err := f.WriteAt(bytes.Repeat([]byte{0xa5}, slotSize), at); err != nil {
				t.Error(err)
			}
			go func() {
				_, state, err := st.Lookup(1, earlier.Code)
				if err != nil {
					t.Error(err)
				}
				result <- state
			}()
			time.Sleep(50 * time.Millisecond)
			if _, err := f.WriteAt(good, at); err != nil {
				t.Error(err)
			}
		})
	}
	create(t, st, 1)
	eq(t, <-result, LinkFound)
	eq(t, logs.String(), "")
}

func TestOpenFilesAreLimited(t *testing.T) {
	defer func(n int) { maxOpenChunks = n }(maxOpenChunks)
	maxOpenChunks = 2
	st := newTestStore(t)
	links := map[uint16]Link{}
	for domain := uint16(1); domain <= 5; domain++ {
		created, err := st.Create(domain, 1, 5, anyCode, oneLink())
		must(t, err)
		links[domain] = created[0]
	}
	for range 2 {
		for domain, l := range links {
			got, state, err := st.Lookup(domain, l.Code)
			must(t, err)
			eq(t, state, LinkFound)
			eq(t, got.URL, l.URL)
			if n := len(st.files); n > 2 {
				t.Fatalf("%d chunk files are open", n)
			}
		}
	}
}

// Finding a domain that was read already takes no lock, so that lookups
// from many goroutines do not all meet at one.
func TestFindingADomainTakesNoLock(t *testing.T) {
	st := newTestStore(t)
	create(t, st, 1)
	st.mu.Lock()
	st.openMu.Lock()
	found := make(chan *domainLinks, 1)
	go func() {
		d, err := st.domain(1)
		if err != nil {
			t.Error(err)
		}
		found <- d
	}()
	select {
	case d := <-found:
		if d == nil {
			t.Error("domain 1 was not found")
		}
	case <-time.After(5 * time.Second):
		t.Error("finding a domain waited for a lock")
	}
	st.openMu.Unlock()
	st.mu.Unlock()
}

// A lookup in a file that is open already takes the open-files lock
// shared: it does not wait for other lookups of open files, which hold it
// the same way.
func TestLookupsOfAnOpenFileShareTheLock(t *testing.T) {
	st := newTestStore(t)
	links := create(t, st, 1)
	found(t, st, links) // the file is open and the domain read
	st.openMu.RLock()   // as another lookup of an open file
	defer st.openMu.RUnlock()
	result := make(chan LinkState, 1)
	go func() {
		_, state, err := st.Lookup(1, links[0].Code)
		if err != nil {
			t.Error(err)
		}
		result <- state
	}()
	select {
	case state := <-result:
		eq(t, state, LinkFound)
	case <-time.After(5 * time.Second):
		t.Error("a lookup in an open file waited for another that holds the lock shared")
	}
}

// A chunk file is opened and kept in one step, so that a chunk has one
// handle, whose end and hosts are current. Were a create to take a link
// through a handle of its own between the open and the keep, and that
// handle then be closed, the handle kept would know the file's old end:
// the link would be gone at once, and written over by the next create.
func TestChunkFileIsOpenedAndKeptInOneStep(t *testing.T) {
	logs := captureLog(t)
	defer func(n int) { maxOpenChunks = n }(maxOpenChunks)
	maxOpenChunks = 1
	dir := filepath.Join(t.TempDir(), "data")
	st, err := OpenStore(dir)
	must(t, err)
	a := create(t, st, 1)
	other, err := st.Create(2, 1, 5, anyCode, oneLink())
	must(t, err)
	must(t, st.Close())

	st, err = OpenStore(dir)
	must(t, err)
	defer func() { st.Close() }()
	// Both domains are read, and the lookup in domain 2 closes the file of
	// domain 1, as in a store of more than 512 chunk files.
	found(t, st, a)
	_, state, err := st.Lookup(2, other[0].Code)
	must(t, err)
	eq(t, state, LinkFound)

	var b []Link
	entered := false
	st.onOpened = func(c *chunkFile) {
		if c.path != firstChunk(dir) || entered {
			return
		}
		entered = true
		if !st.openMu.TryLock() {
			return // opened under the lock: nothing can come in between
		}
		st.openMu.Unlock()
		// Nothing holds the lock, so a create comes in between, through a
		// handle of its own, which a lookup in domain 2 then closes.
		b = create(t, st, 1)
		if _, _, err := st.Lookup(2, other[0].Code); err != nil {
			t.Fatal(err)
		}
	}
	found(t, st, a) // opens the file of domain 1 again
	st.onOpened = nil
	if b == nil {
		b = create(t, st, 1)
	}
	if _, state, err := st.Lookup(1, b[0].Code); err != nil || state != LinkFound {
		t.Errorf("lookup of the link just created: %v, %v", state, err)
	}
	c := create(t, st, 1)
	must(t, st.Close())

	st, err = OpenStore(dir)
	must(t, err)
	for name, l := range map[string]Link{"a": a[0], "b": b[0], "c": c[0]} {
		got, state, err := st.Lookup(1, l.Code)
		must(t, err)
		if state != LinkFound || got.URL != l.URL {
			t.Errorf("after a restart, link %s: %v, %q, want %q", name, state, got.URL, l.URL)
		}
	}
	if strings.Contains(logs.String(), "damaged") {
		t.Errorf("a link was called damaged:\n%s", logs)
	}
}

// Lookups that find a chunk file closed, all at once, open it once: the
// first opens and keeps it, the others find it kept. Afterwards the store
// holds one descriptor of the file.
func TestLookupsOfAClosedFileOpenItOnce(t *testing.T) {
	defer func(n int) { maxOpenChunks = n }(maxOpenChunks)
	maxOpenChunks = 1
	st := newTestStore(t)
	a := create(t, st, 1)
	_, err := st.Create(2, 1, 5, anyCode, oneLink()) // closes the file of domain 1
	must(t, err)

	const n = 8
	var missed sync.WaitGroup
	missed.Add(n)
	st.onNotOpen = func(path string) {
		if path == firstChunk(st.dir) {
			missed.Done()
			missed.Wait() // every lookup has found the file closed before any opens it
		}
	}
	var mu sync.Mutex
	var opened []*chunkFile
	st.onOpened = func(c *chunkFile) {
		if c.path == firstChunk(st.dir) {
			mu.Lock()
			opened = append(opened, c)
			mu.Unlock()
		}
	}
	var wg sync.WaitGroup
	for range n {
		wg.Go(func() {
			if _, state, err := st.Lookup(1, a[0].Code); err != nil || state != LinkFound {
				t.Errorf("lookup: %v, %v", state, err)
			}
		})
	}
	wg.Wait()
	eq(t, len(opened), 1)
	openNow := 0
	for _, c := range opened {
		if _, err := c.f.Stat(); err == nil {
			openNow++
		}
	}
	eq(t, openNow, 1)
	if kept := st.files[chunkID{1, 5, 0}]; kept == nil || kept.chunkFile != opened[0] {
		t.Error("the handle opened is not the one kept")
	}
}

// Lookups that find a damaged chunk file closed, all at once, get it read
// once: the first records the damage and logs it, the others find it
// recorded.
func TestLookupsOfAClosedDamagedFileLogItOnce(t *testing.T) {
	logs := captureLog(t)
	defer func(n int) { maxOpenChunks = n }(maxOpenChunks)
	maxOpenChunks = 1
	st := newTestStore(t)
	a := create(t, st, 1)
	_, err := st.Create(2, 1, 5, anyCode, oneLink()) // closes the file of domain 1
	must(t, err)
	path := firstChunk(st.dir)
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	must(t, err)
	_, err = f.WriteAt([]byte{'x'}, 0) // the magic
	must(t, err)
	must(t, f.Close())

	const n = 8
	var missed sync.WaitGroup
	missed.Add(n)
	st.onNotOpen = func(p string) {
		if p == path {
			missed.Done()
			missed.Wait() // every lookup has found the file closed before any opens it
		}
	}
	var wg sync.WaitGroup
	for range n {
		wg.Go(func() {
			if _, state, err := st.Lookup(1, a[0].Code); err != nil || state != LinkGone {
				t.Errorf("lookup: %v, %v", state, err)
			}
		})
	}
	wg.Wait()
	eq(t, strings.Count(logs.String(), "lead nowhere\" file="+path), 1)
}

// Tests for changing links in the store: a new URL or expiry, deleting,
// opening a secured link, and chunk files removed while the store runs.
package app

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// regular is a regular link to url that expires in an hour.
func regular(url string) NewLink {
	return NewLink{URL: url, ExpiresAt: in(time.Hour)}
}

// createOne stores one link owned by account 1 on domain 1 and returns it.
func createOne(t *testing.T, st *Store, l NewLink) Link {
	t.Helper()
	created, err := st.Create(1, 1, 5, anyCode, []NewLink{l})
	must(t, err)
	return created[0]
}

// mustFind returns the link a code of domain 1 stands for and fails the test
// unless it is found.
func mustFind(t *testing.T, st *Store, code string) Link {
	t.Helper()
	l, state, err := st.Lookup(1, code)
	must(t, err)
	eq(t, state, LinkFound)
	return l
}

// mustBeGone fails the test unless the code of domain 1 is gone.
func mustBeGone(t *testing.T, st *Store, code string) {
	t.Helper()
	_, state, err := st.Lookup(1, code)
	must(t, err)
	eq(t, state, LinkGone)
}

// fileHolds reports whether the chunk file at path holds s.
func fileHolds(t *testing.T, path, s string) bool {
	t.Helper()
	b, err := os.ReadFile(path)
	must(t, err)
	return bytes.Contains(b, []byte(s))
}

func TestUpdateExpiry(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	st, err := OpenStore(dir)
	must(t, err)
	l := createOne(t, st, regular("https://example.com/expiry"))
	later := time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)
	got, err := st.Update(1, l.Code, 1, nil, &later)
	must(t, err)
	eq(t, got.ExpiresAt.Equal(later), true)
	eq(t, got.URL, l.URL)
	eq(t, mustFind(t, st, l.Code).ExpiresAt.Equal(later), true)
	must(t, st.Close())

	st, err = OpenStore(dir)
	must(t, err)
	defer st.Close()
	eq(t, mustFind(t, st, l.Code).ExpiresAt.Equal(later), true)

	sooner := time.Date(2027, 6, 7, 8, 9, 10, 0, time.UTC)
	_, err = st.Update(1, l.Code, 1, nil, &sooner)
	must(t, err)
	got = mustFind(t, st, l.Code)
	eq(t, got.ExpiresAt.Equal(sooner), true)
	eq(t, got.URL, l.URL)
}

func TestUpdateURL(t *testing.T) {
	st := newTestStore(t)
	l := createOne(t, st, regular("https://example.com/old-address"))
	url := "http://example.org/new-address?x=1"
	got, err := st.Update(1, l.Code, 1, &url, nil)
	must(t, err)
	eq(t, got.URL, url)
	stored := mustFind(t, st, l.Code)
	eq(t, stored.URL, url) // http, as given, though the old URL was https
	eq(t, stored.ExpiresAt.Equal(l.ExpiresAt), true)
	if fileHolds(t, firstChunk(st.dir), "/old-address") {
		t.Fatal("the chunk file still holds the old URL")
	}
}

func TestUpdateRevivesExpired(t *testing.T) {
	st := newTestStore(t)
	l := createOne(t, st, NewLink{URL: "https://example.com/expired", ExpiresAt: in(-time.Hour)})
	later := in(time.Hour).Truncate(time.Second)
	_, err := st.Update(1, l.Code, 1, nil, &later)
	must(t, err)
	eq(t, mustFind(t, st, l.Code).ExpiresAt.Equal(later), true)
}

func TestUpdateOfOthersLinkIsNotFound(t *testing.T) {
	st := newTestStore(t)
	l := createOne(t, st, regular("https://example.com/mine"))
	url, later := "https://example.org/theirs", in(48*time.Hour)
	for _, change := range []struct {
		url       *string
		expiresAt *time.Time
	}{{&url, nil}, {nil, &later}, {&url, &later}} {
		if _, err := st.Update(1, l.Code, 2, change.url, change.expiresAt); !errors.Is(err, ErrNotFound) {
			t.Fatalf("Update by another owner returned %v, want ErrNotFound", err)
		}
	}
	got := mustFind(t, st, l.Code)
	eq(t, got.URL, l.URL)
	eq(t, got.ExpiresAt.Equal(l.ExpiresAt), true)
}

func TestUpdateSecuredIsRefused(t *testing.T) {
	st := newTestStore(t)
	minute := time.Minute
	salt, hash := bytes.Repeat([]byte{1}, saltSize), bytes.Repeat([]byte{2}, hashSize)
	links, err := st.Create(1, 1, 5, anyCode, []NewLink{
		{URL: "https://example.com/secured", ExpiresAt: in(time.Hour), AfterOpen: &minute},
		{URL: "https://example.com/locked", ExpiresAt: in(time.Hour), Salt: salt, Hash: hash},
	})
	must(t, err)
	url, later := "https://example.org/else", in(48*time.Hour)
	for _, l := range links {
		for _, change := range []struct {
			url       *string
			expiresAt *time.Time
		}{{&url, nil}, {nil, &later}} {
			if _, err := st.Update(1, l.Code, 1, change.url, change.expiresAt); !errors.Is(err, ErrSecured) {
				t.Fatalf("Update of %s returned %v, want ErrSecured", l.URL, err)
			}
		}
		got := mustFind(t, st, l.Code)
		eq(t, got.URL, l.URL)
		eq(t, got.ExpiresAt.Equal(l.ExpiresAt), true)
	}
}

func TestDeleteLeadsNowhere(t *testing.T) {
	st := newTestStore(t)
	links, err := st.Create(1, 1, 5, anyCode, []NewLink{
		regular("https://example.com/to-be-deleted"), regular("https://example.com/kept"),
	})
	must(t, err)
	must(t, st.Delete(1, links[0].Code, 1))
	mustBeGone(t, st, links[0].Code)
	if fileHolds(t, firstChunk(st.dir), "/to-be-deleted") {
		t.Fatal("the chunk file still holds the deleted URL")
	}

	for _, del := range []struct {
		code  string
		owner uint32
	}{{links[0].Code, 1}, {links[1].Code, 2}, {"zzzzz", 1}} {
		if err := st.Delete(1, del.code, del.owner); !errors.Is(err, ErrNotFound) {
			t.Fatalf("Delete of %s by %d returned %v, want ErrNotFound", del.code, del.owner, err)
		}
	}
	found(t, st, links[1:])
	allDiffer(t, links, create(t, st, 1))
}

func TestOpenWindow(t *testing.T) {
	st := newTestStore(t)
	minute := time.Minute
	l := createOne(t, st, NewLink{URL: "https://example.com/window", ExpiresAt: t0.Add(time.Hour), AfterOpen: &minute})
	for _, at := range []time.Duration{0, 59 * time.Second} {
		url, ok, err := st.Open(1, l.Code, t0.Add(at))
		must(t, err)
		eq(t, ok, true)
		eq(t, url, l.URL)
	}
	url, ok, err := st.Open(1, l.Code, t0.Add(61*time.Second))
	must(t, err)
	eq(t, ok, false)
	eq(t, url, "")

	got := mustFind(t, st, l.Code)
	eq(t, got.Opened, true)
	eq(t, got.ExpiresAt.Equal(t0.Add(time.Minute)), true)
}

func TestOpenOneTime(t *testing.T) {
	st := newTestStore(t)
	none := time.Duration(0)
	l := createOne(t, st, NewLink{URL: "https://example.com/once", ExpiresAt: t0.Add(time.Hour), AfterOpen: &none})
	url, ok, err := st.Open(1, l.Code, t0)
	must(t, err)
	eq(t, ok, true)
	eq(t, url, l.URL)
	// Not again, even for a request whose clock reads a moment earlier.
	for _, at := range []time.Time{t0, t0.Add(-time.Second)} {
		_, ok, err := st.Open(1, l.Code, at)
		must(t, err)
		eq(t, ok, false)
	}
}

func TestOpenConcurrent(t *testing.T) {
	st := newTestStore(t)
	none := time.Duration(0)
	l := createOne(t, st, NewLink{URL: "https://example.com/once", ExpiresAt: in(time.Hour), AfterOpen: &none})
	var opened atomic.Int64
	var wg sync.WaitGroup
	for range 50 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, ok, err := st.Open(1, l.Code, time.Now())
			if err != nil {
				t.Error(err)
			}
			if ok {
				opened.Add(1)
			}
		}()
	}
	wg.Wait()
	eq(t, opened.Load(), int64(1))
}

// A URL change lets go of the write lock while its new record syncs. A
// delete that comes in then wins: the change reads the link again before
// it points the slot at the new record, and erases that record instead.
func TestDeleteDuringURLChangeStaysDeleted(t *testing.T) {
	st := newTestStore(t)
	l := createOne(t, st, regular("https://example.com/before-the-change"))
	deleted := make(chan error, 1)
	var syncing, writing sync.Once
	st.onSync = func(string) {
		syncing.Do(func() {
			// The new record is written; its sync is about to run.
			wrote := make(chan struct{})
			st.onWrite = func(string, int64, []byte) { writing.Do(func() { close(wrote) }) }
			go func() { deleted <- st.Delete(1, l.Code, 1) }()
			<-wrote // the delete has written its slot and waits for this sync
		})
	}
	url := "https://example.com/after-the-change"
	if _, err := st.Update(1, l.Code, 1, &url, nil); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Update returned %v, want ErrNotFound", err)
	}
	must(t, <-deleted)
	mustBeGone(t, st, l.Code)
	for _, rest := range []string{"/before-the-change", "/after-the-change"} {
		if fileHolds(t, firstChunk(st.dir), rest) {
			t.Fatalf("the chunk file still holds %s", rest)
		}
	}
}

// A lookup reads without the write lock, so it can read the old slot and
// then find the old record erased; it reads again under the lock rather than
// calling the link damaged.
func TestLookupDuringEditsNeverSeesDamage(t *testing.T) {
	logs := captureLog(t)
	st := newTestStore(t)
	urls := [2]string{"https://example.com/one", "https://example.org/two"}
	l := createOne(t, st, regular(urls[0]))
	// 4,000 real syncs would take most of a minute on some disks; what this
	// test is about is reads racing writes.
	st.files[chunkID{1, 5, 0}].sync = func() error { return nil }

	stop := make(chan struct{})
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				got, state, err := st.Lookup(1, l.Code)
				if err != nil || state != LinkFound || (got.URL != urls[0] && got.URL != urls[1]) {
					t.Errorf("a lookup during edits got %v %q, %v", state, got.URL, err)
					return
				}
			}
		}()
	}
	for i := range 2000 {
		url := urls[(i+1)%2]
		if _, err := st.Update(1, l.Code, 1, &url, nil); err != nil {
			t.Error(err)
			break
		}
	}
	close(stop)
	wg.Wait()
	eq(t, logs.String(), "")
}

func TestOpenRejects(t *testing.T) {
	st := newTestStore(t)
	minute := time.Minute
	links, err := st.Create(1, 1, 5, anyCode, []NewLink{
		{URL: "https://example.com/regular", ExpiresAt: t0.Add(time.Hour)},
		{URL: "https://example.com/expired", ExpiresAt: t0.Add(-time.Second), AfterOpen: &minute},
		{URL: "https://example.com/deleted", ExpiresAt: t0.Add(time.Hour), AfterOpen: &minute},
	})
	must(t, err)
	must(t, st.Delete(1, links[2].Code, 1))
	for _, code := range []string{links[0].Code, links[1].Code, links[2].Code, "zzzzz"} {
		url, ok, err := st.Open(1, code, t0)
		must(t, err)
		eq(t, ok, false)
		eq(t, url, "")
	}
}

// A chunk file removed while open still answers through its handle until
// ForgetRemovedFiles lets go of it. It was where new links went, so that is
// logged at once and numbering goes on in the following chunk.
func TestRemovedFileIsNoticed(t *testing.T) {
	logs := captureLog(t)
	st := newTestStore(t)
	old := create(t, st, 3)
	must(t, os.Remove(firstChunk(st.dir)))
	found(t, st, old)

	eq(t, st.ForgetRemovedFiles(), 1)
	eq(t, strings.Count(logs.String(), "level=ERROR"), 1)
	if !strings.Contains(logs.String(), firstChunk(st.dir)) {
		t.Fatalf("the log does not name the removed file:\n%s", logs)
	}
	// The removed file is not tried again: a lookup takes no lock to open it.
	opens := 0
	st.onNotOpen = func(string) { opens++ }
	for _, l := range old {
		mustBeGone(t, st, l.Code)
	}
	eq(t, opens, 0)
	fresh := create(t, st, 1)
	allDiffer(t, old, fresh)
	eq(t, numberOf(t, st, fresh[0].Code)/chunkLinks, uint64(1))
	eq(t, st.ForgetRemovedFiles(), 0)
	eq(t, strings.Count(logs.String(), "level=ERROR"), 1)
}

// A chunk file that the limit on open files has closed is noticed too when
// it is removed, and handled as an open one: its links are gone, the file
// is not tried again, and numbering goes on in the following chunk.
func TestRemovedClosedFileIsNoticed(t *testing.T) {
	logs := captureLog(t)
	defer func(n int) { maxOpenChunks = n }(maxOpenChunks)
	maxOpenChunks = 1
	st := newTestStore(t)
	old := create(t, st, 3)
	_, err := st.Create(2, 1, 5, anyCode, oneLink()) // closes the file of domain 1
	must(t, err)
	eq(t, st.ForgetRemovedFiles(), 0) // the closed file is still there
	must(t, os.Remove(firstChunk(st.dir)))

	eq(t, st.ForgetRemovedFiles(), 1)
	if !strings.Contains(logs.String(), firstChunk(st.dir)) {
		t.Fatalf("the log does not name the removed file:\n%s", logs)
	}
	opens := 0
	st.onNotOpen = func(string) { opens++ }
	for _, l := range old {
		mustBeGone(t, st, l.Code)
	}
	eq(t, opens, 0)
	st.onNotOpen = nil
	fresh := create(t, st, 1)
	allDiffer(t, old, fresh)
	eq(t, numberOf(t, st, fresh[0].Code)/chunkLinks, uint64(1))
	eq(t, st.ForgetRemovedFiles(), 0)
}

// A lookup can be the first to find that a closed chunk file was removed:
// it answers gone, and the file is not tried again. The check still counts
// the file, once, so that the server empties its link cache.
func TestRemovedClosedFileFoundByALookup(t *testing.T) {
	captureLog(t)
	defer func(n int) { maxOpenChunks = n }(maxOpenChunks)
	maxOpenChunks = 1
	st := newTestStore(t)
	old := create(t, st, 3)
	_, err := st.Create(2, 1, 5, anyCode, oneLink()) // closes the file of domain 1
	must(t, err)
	must(t, os.Remove(firstChunk(st.dir)))

	opens := 0
	st.onNotOpen = func(string) { opens++ }
	mustBeGone(t, st, old[0].Code)
	eq(t, opens, 1) // the first lookup tries to open the file
	for _, l := range old {
		mustBeGone(t, st, l.Code)
	}
	eq(t, opens, 1) // the others do not
	eq(t, st.ForgetRemovedFiles(), 1)
	eq(t, st.ForgetRemovedFiles(), 0)
	mustBeGone(t, st, old[1].Code)
	eq(t, opens, 1)
}

// A closed chunk file that a lookup found missing is counted by the next
// check even when it was put back before that check, so that the server
// empties its link cache. It is counted once, and is not opened again.
func TestRemovedClosedFilePutBackIsCounted(t *testing.T) {
	captureLog(t)
	defer func(n int) { maxOpenChunks = n }(maxOpenChunks)
	maxOpenChunks = 1
	st := newTestStore(t)
	old := create(t, st, 2)
	_, err := st.Create(2, 1, 5, anyCode, oneLink()) // closes the file of domain 1
	must(t, err)
	path := firstChunk(st.dir)
	must(t, os.Rename(path, path+".away"))
	mustBeGone(t, st, old[0].Code)
	must(t, os.Rename(path+".away", path))

	eq(t, st.ForgetRemovedFiles(), 1)
	eq(t, st.ForgetRemovedFiles(), 0)
	st.openMu.RLock()
	closed := st.closed[chunkID{1, 5, 0}]
	st.openMu.RUnlock()
	eq(t, closed, false)
	mustBeGone(t, st, old[1].Code) // read again after a restart only
}

// A chunk file that is missing when it is first opened, as an old file
// deleted while qisqa was stopped is, answers gone and is not tried again:
// later lookups of its links take no lock to open it.
func TestMissingFileIsRemembered(t *testing.T) {
	captureLog(t)
	dir := filepath.Join(t.TempDir(), "data")
	st, err := OpenStore(dir)
	must(t, err)
	create(t, st, 1)
	must(t, st.Close())
	// Numbering goes on near the end of the first chunk, so that the next
	// links fill it and start the second.
	must(t, os.WriteFile(filepath.Join(dir, "d1", "state.json"), []byte(`{"5":{"chunk":0,"next":65534}}`), 0o600))
	st, err = OpenStore(dir)
	must(t, err)
	older := create(t, st, 2)
	newer := create(t, st, 1)
	eq(t, numberOf(t, st, newer[0].Code), uint64(chunkLinks))
	must(t, st.Close())
	must(t, os.Remove(firstChunk(dir)))

	st, err = OpenStore(dir)
	must(t, err)
	defer st.Close()
	found(t, st, newer) // starts the domain from its newest file
	opens := 0
	st.onNotOpen = func(string) { opens++ }
	mustBeGone(t, st, older[0].Code)
	eq(t, opens, 1) // the first lookup tries to open the file
	mustBeGone(t, st, older[1].Code)
	mustBeGone(t, st, older[0].Code)
	eq(t, opens, 1) // the others do not
}

// An error of the system at open says nothing about the file: it is not
// remembered, and the next lookup tries again.
func TestSystemErrorAtOpenIsTriedAgain(t *testing.T) {
	captureLog(t)
	defer func(n int) { maxOpenChunks = n }(maxOpenChunks)
	maxOpenChunks = 1
	st := newTestStore(t)
	old := create(t, st, 1)
	_, err := st.Create(2, 1, 5, anyCode, oneLink()) // closes the file of domain 1
	must(t, err)
	path := firstChunk(st.dir)
	must(t, os.Rename(path, path+".away"))
	must(t, os.Mkdir(path, 0o700)) // opening a folder as a file fails, even for root

	opens := 0
	st.onNotOpen = func(string) { opens++ }
	for range 2 {
		if _, _, err := st.Lookup(1, old[0].Code); err == nil {
			t.Fatal("Lookup hid the error of opening the file")
		}
	}
	eq(t, opens, 2) // tried again
	must(t, os.Remove(path))
	must(t, os.Rename(path+".away", path))
	found(t, st, old)
	eq(t, opens, 3)
}

// An older chunk file is the kind an operator removes on purpose: it is let
// go of quietly, and numbering stays where it is.
func TestRemovedOlderFileKeepsNumbering(t *testing.T) {
	logs := captureLog(t)
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
	older := create(t, st, 1)
	newer := create(t, st, 1)
	eq(t, numberOf(t, st, newer[0].Code), uint64(65536))

	must(t, os.Remove(firstChunk(dir)))
	found(t, st, older)
	eq(t, st.ForgetRemovedFiles(), 1)
	mustBeGone(t, st, older[0].Code)
	found(t, st, newer)
	eq(t, numberOf(t, st, create(t, st, 1)[0].Code), uint64(65537))
	eq(t, logs.String(), "")
}

// After a failed sync an edit returns its error without writing; an edit
// whose own sync fails is fatal once, as a create is.
func TestEditsAfterFailedSync(t *testing.T) {
	captureLog(t)
	st := newTestStore(t)
	var fatal []error
	st.fatal = func(err error) { fatal = append(fatal, err) }
	minute := time.Minute
	links, err := st.Create(1, 1, 5, anyCode, []NewLink{
		regular("https://example.com/a"), regular("https://example.com/b"),
		{URL: "https://example.com/secured", ExpiresAt: in(time.Hour), AfterOpen: &minute},
	})
	must(t, err)

	failure := errors.New("input/output error")
	st.failSync = failure
	first := st.Delete(1, links[0].Code, 1)
	if !errors.Is(first, failure) {
		t.Fatalf("Delete returned %v, want %v", first, failure)
	}
	eq(t, len(fatal), 1)

	st.failSync = nil
	st.onWrite = func(path string, offset int64, b []byte) {
		t.Errorf("a write of %d bytes at byte %d of %s after the failure", len(b), offset, path)
	}
	url, later := "https://example.org/else", in(48*time.Hour)
	_, err = st.Update(1, links[1].Code, 1, &url, nil)
	eq(t, err, first)
	_, err = st.Update(1, links[1].Code, 1, nil, &later)
	eq(t, err, first)
	eq(t, st.Delete(1, links[1].Code, 1), first)
	_, ok, err := st.Open(1, links[2].Code, time.Now())
	eq(t, err, first)
	eq(t, ok, false)
	eq(t, len(fatal), 1)
}

// An edit, like a create, writes the head of a file it did not create back
// before anything else: after a failed sync the system may hold those bytes
// as saved although they never reached the disk.
func TestEditOfContinuedChunkWritesItsHeadFirst(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	st, err := OpenStore(dir)
	must(t, err)
	minute := time.Minute
	links, err := st.Create(1, 1, 5, anyCode, []NewLink{
		regular("https://example.com/a"),
		{URL: "https://example.com/secured", ExpiresAt: in(time.Hour), AfterOpen: &minute},
	})
	must(t, err)
	must(t, st.Close())

	url, later := "https://example.com/a2", in(48*time.Hour)
	for _, edit := range []struct {
		name string
		do   func(st *Store) error
	}{
		{"expiry", func(st *Store) error { _, err := st.Update(1, links[0].Code, 1, nil, &later); return err }},
		{"URL", func(st *Store) error { _, err := st.Update(1, links[0].Code, 1, &url, nil); return err }},
		{"open", func(st *Store) error { _, _, err := st.Open(1, links[1].Code, time.Now()); return err }},
		{"delete", func(st *Store) error { return st.Delete(1, links[0].Code, 1) }},
	} {
		b, err := os.ReadFile(firstChunk(dir))
		must(t, err)
		head := b[:slotsOffset]
		st, err := OpenStore(dir)
		must(t, err)
		var offsets []int64
		var first []byte
		st.onWrite = func(_ string, offset int64, b []byte) {
			if offsets = append(offsets, offset); len(offsets) == 1 {
				first = bytes.Clone(b)
			}
		}
		must(t, edit.do(st))
		if len(offsets) < 2 || offsets[0] != 0 || !bytes.Equal(first, head) {
			t.Fatalf("%s: the first of the writes at %v is not the 8,192 bytes the file began with", edit.name, offsets)
		}
		must(t, st.Close())
	}
}

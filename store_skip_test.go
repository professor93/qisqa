// Tests for the skip: once a copy of the data folder is put back, every
// numbering is moved forward, so that the codes made since the copy are not
// handed out again.
package main

import (
	"errors"
	"fmt"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// openAt opens the store in dir.
func openAt(t *testing.T, dir string) *Store {
	t.Helper()
	st, err := OpenStore(dir)
	must(t, err)
	return st
}

// skipIn adds n to the skip of the data folder dir, as -skip does.
func skipIn(t *testing.T, dir string, n uint64) {
	t.Helper()
	st := openAt(t, dir)
	_, err := st.Skip(n)
	must(t, err)
	must(t, st.Close())
}

// readAndClose opens the store in dir, reads each domain with a lookup and
// closes the store, which writes the state.json of each domain that has
// links.
func readAndClose(t *testing.T, dir string, domains ...uint16) {
	t.Helper()
	st := openAt(t, dir)
	for _, domain := range domains {
		_, _, err := st.Lookup(domain, "aaaaa")
		must(t, err)
	}
	must(t, st.Close())
}

// copyFolder copies the data folder dir, which no store holds, to a new
// folder and returns its path.
func copyFolder(t *testing.T, dir string) string {
	t.Helper()
	to := filepath.Join(t.TempDir(), "copy")
	must(t, filepath.WalkDir(dir, func(path string, e fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		if e.IsDir() {
			return os.MkdirAll(filepath.Join(to, rel), 0o700)
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(to, rel), b, 0o600)
	}))
	return to
}

// notALink fails the test if a code of domain 1 leads to a link: it must
// answer as a code nobody was given, or as a link that is gone.
func notALink(t *testing.T, st *Store, code string) {
	t.Helper()
	l, state, err := st.Lookup(1, code)
	must(t, err)
	if state == LinkFound {
		t.Fatalf("code %s, lost or passed over, leads to %q", code, l.URL)
	}
}

// noWarnings fails the test if the log holds a warning or an error.
func noWarnings(t *testing.T, logs fmt.Stringer) {
	t.Helper()
	if s := logs.String(); strings.Contains(s, "level=WARN") || strings.Contains(s, "level=ERROR") {
		t.Fatalf("logged:\n%s", s)
	}
}

func TestRestoredCopyWithSkipHandsOutNoLostCode(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	st := openAt(t, dir)
	kept := create(t, st, 10)
	must(t, st.Close())
	copied := copyFolder(t, dir)

	st = openAt(t, dir)
	lost := create(t, st, 20) // made after the copy: their codes are lost with it
	must(t, st.Close())

	skipIn(t, copied, 20)
	st = openAt(t, copied)
	defer st.Close()
	fresh := create(t, st, 30)
	allDiffer(t, lost, fresh)
	for _, l := range lost {
		notALink(t, st, l.Code)
	}
	found(t, st, kept)
	found(t, st, fresh)
}

func TestSkipIsAppliedOnce(t *testing.T) {
	const n = 1000
	dir := filepath.Join(t.TempDir(), "data")
	st := openAt(t, dir)
	create(t, st, 10)
	must(t, st.Close())

	skipIn(t, dir, n)
	readAndClose(t, dir, 1) // moves the numbering on and writes state.json
	st = openAt(t, dir)
	eq(t, numberOf(t, st, create(t, st, 1)[0].Code), uint64(10+n))
	must(t, st.Close())

	readAndClose(t, dir, 1)
	st = openAt(t, dir)
	eq(t, numberOf(t, st, create(t, st, 1)[0].Code), uint64(10+n+1))
	must(t, st.Close())

	// A second skip moves on by what it adds.
	skipIn(t, dir, n)
	st = openAt(t, dir)
	eq(t, numberOf(t, st, create(t, st, 1)[0].Code), uint64(10+n+2+n))
	must(t, st.Close())
	// A total lowered by hand once it was applied moves nothing back.
	writeFile(t, dir, "skipped", "0\n")
	st = openAt(t, dir)
	defer st.Close()
	eq(t, numberOf(t, st, create(t, st, 1)[0].Code), uint64(10+n+3+n))
}

func TestSkipCoversLengthsAndDomainsNotYetUsed(t *testing.T) {
	const n = 1000
	dir := filepath.Join(t.TempDir(), "data")
	st := openAt(t, dir)
	create(t, st, 10) // length 5 on domain 1
	must(t, st.Close())

	skipIn(t, dir, n)
	// A length or a domain with no state yet is moved from 0 each time it is
	// read, to the same number.
	readAndClose(t, dir, 1, 2)
	readAndClose(t, dir, 1, 2)
	st = openAt(t, dir)
	defer st.Close()
	longer, err := st.Create(1, 1, 7, anyCode, oneLink())
	must(t, err)
	eq(t, len(longer[0].Code), 7)
	eq(t, numberOf(t, st, longer[0].Code), uint64(n))
	other, err := st.Create(2, 1, 5, anyCode, oneLink())
	must(t, err)
	_, m, _ := NewCoder(st.key, 2).Number(other[0].Code)
	eq(t, m, uint64(n))
	eq(t, numberOf(t, st, create(t, st, 1)[0].Code), uint64(10+n))
}

func TestSkipPastTheEndOfALength(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	st := openAt(t, dir)
	first, err := st.Create(1, 1, 3, anyCode, oneLink())
	must(t, err)
	eq(t, len(first[0].Code), 3)
	must(t, st.Close())

	skipIn(t, dir, 30000) // more than the 23,832 numbers of length 3
	st = openAt(t, dir)
	defer st.Close()
	next, err := st.Create(1, 1, 3, anyCode, oneLink())
	must(t, err)
	eq(t, len(next[0].Code), 4)
	eq(t, numberOf(t, st, next[0].Code), uint64(30000)) // length 4 was never used
}

// The skip ends in the middle of a chunk that has no file yet, and a lookup
// may reach that chunk before the create that starts it. The chunk then
// works as any other: also once the limit on open files has closed its file,
// and after a look for removed files.
func TestSkippedNumberInAChunkNotYetStarted(t *testing.T) {
	logs := captureLog(t)
	defer func(n int) { maxOpenChunks = n }(maxOpenChunks)
	maxOpenChunks = 1
	dir := filepath.Join(t.TempDir(), "data")
	st := openAt(t, dir)
	create(t, st, 10)
	must(t, st.Close())
	// From 10 to 200,010: chunks 1 and 2 are passed over whole, and the
	// skip ends in the middle of chunk 3.
	skipIn(t, dir, 200000)

	st = openAt(t, dir)
	defer st.Close()
	coder := NewCoder(st.key, 1)
	below := coder.Code(5, 200009)
	notALink(t, st, below)
	notALink(t, st, coder.Code(5, 70000)) // in chunk 1
	link := create(t, st, 1)
	eq(t, numberOf(t, st, link[0].Code), uint64(200010))
	found(t, st, link)
	notALink(t, st, below)

	_, err := st.Create(2, 1, 5, anyCode, oneLink()) // the limit closes the file of chunk 3
	must(t, err)
	eq(t, st.ForgetRemovedFiles(), 0)
	found(t, st, link)
	notALink(t, st, below)
	eq(t, numberOf(t, st, create(t, st, 1)[0].Code), uint64(200011))
	for _, index := range []uint64{1, 2} {
		if _, err := os.Lstat(filepath.Join(dir, "d1", chunkName(5, index))); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("chunk %d, passed over whole, has a file: %v", index, err)
		}
	}
	noWarnings(t, logs)
}

// A lookup of a passed-over number in the chunk a create is starting does
// not open the file while it is being made: it would find a file that is not
// a chunk file yet, call it damaged and leave its links gone.
func TestLookupWhileTheSkippedChunkIsMade(t *testing.T) {
	logs := captureLog(t)
	dir := filepath.Join(t.TempDir(), "data")
	st := openAt(t, dir)
	create(t, st, 10)
	must(t, st.Close())
	skipIn(t, dir, 200000) // ends in the middle of chunk 3

	st = openAt(t, dir)
	defer st.Close()
	coder := NewCoder(st.key, 1)
	below := coder.Code(5, 200009)
	notALink(t, st, coder.Code(5, 10)) // reads the domain
	made := filepath.Join(dir, "d1", chunkName(5, 3))
	st.onWrite = func(path string, offset int64, b []byte) {
		if path == made && offset == 0 { // the header, into a file still empty
			notALink(t, st, below)
		}
	}
	link := create(t, st, 1)
	st.onWrite = nil
	found(t, st, link)
	notALink(t, st, below)
	noWarnings(t, logs)
}

// The skip moves the numbering past where the links end. That is no damage,
// and starting again does not call it one.
func TestSkipLogsNothingOnRestart(t *testing.T) {
	for _, n := range []uint64{20, 100000} { // within the newest chunk, past it
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			logs := captureLog(t)
			dir := filepath.Join(t.TempDir(), "data")
			st := openAt(t, dir)
			create(t, st, 10)
			must(t, st.Close())

			skipIn(t, dir, n)
			readAndClose(t, dir, 1)
			readAndClose(t, dir, 1)
			noWarnings(t, logs)
			st = openAt(t, dir)
			defer st.Close()
			eq(t, numberOf(t, st, create(t, st, 1)[0].Code), 10+n)
		})
	}
}

// Links made after a skip are recorded as any others: when their slots are
// lost, the startup says so, and numbering goes on after them.
func TestDamageIsStillLoggedAfterASkip(t *testing.T) {
	logs := captureLog(t)
	dir := filepath.Join(t.TempDir(), "data")
	st := openAt(t, dir)
	create(t, st, 10)
	must(t, st.Close())
	skipIn(t, dir, 20)
	st = openAt(t, dir)
	after := create(t, st, 5) // numbers 30 to 34
	must(t, st.Close())

	path := firstChunk(dir)
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	must(t, err)
	for _, l := range after[2:] {
		_, err := f.WriteAt(make([]byte, slotSize), slotsOffset+int64(numberOf(t, st, l.Code))*slotSize)
		must(t, err)
	}
	must(t, f.Close())

	st = openAt(t, dir)
	defer st.Close()
	eq(t, numberOf(t, st, create(t, st, 1)[0].Code), uint64(35))
	if !strings.Contains(logs.String(), "fewer links than state.json recorded") || !strings.Contains(logs.String(), path) {
		t.Fatalf("the damage to %s is not logged:\n%s", path, logs)
	}
}

// After a skip past the newest chunk, and before a link of its length was
// made, that chunk's file is lost while qisqa runs. Numbering stays past the
// skip: the numbers between are the lost links', and a chunk passed over
// whole gets no file.
func TestChunkLostWhileRunningAfterASkip(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	st := openAt(t, dir)
	create(t, st, 10)
	must(t, st.Close())
	copied := copyFolder(t, dir)
	st = openAt(t, dir)
	var lost []Link // numbers 10 to 70,009: into the second chunk
	for range 7 {
		lost = append(lost, create(t, st, 10000)...)
	}
	must(t, st.Close())

	for _, c := range []struct {
		name    string
		skip    uint64
		byLimit bool
	}{
		{"removed while open", 100000, false},               // ends in chunk 1
		{"closed by the limit, then removed", 200000, true}, // ends in chunk 3
	} {
		t.Run(c.name, func(t *testing.T) {
			captureLog(t) // the lost file is logged
			if c.byLimit {
				defer func(n int) { maxOpenChunks = n }(maxOpenChunks)
				maxOpenChunks = 1
			}
			restored := copyFolder(t, copied)
			skipIn(t, restored, c.skip)
			st := openAt(t, restored)
			defer st.Close()
			notALink(t, st, lost[0].Code) // reads the domain
			if c.byLimit {
				_, err := st.Create(2, 1, 5, anyCode, oneLink()) // closes the file of domain 1
				must(t, err)
			}
			must(t, os.Remove(firstChunk(restored)))
			eq(t, st.ForgetRemovedFiles(), 1)

			fresh := create(t, st, 100)
			eq(t, numberOf(t, st, fresh[0].Code), 10+c.skip)
			allDiffer(t, lost, fresh)
			for index := uint64(1); index < (10+c.skip)/chunkLinks; index++ {
				if _, err := os.Lstat(filepath.Join(restored, "d1", chunkName(5, index))); !errors.Is(err, fs.ErrNotExist) {
					t.Errorf("chunk %d, passed over whole, has a file: %v", index, err)
				}
			}
		})
	}
}

// A crash before state.json was written may apply the skip again from where
// the files end; no code is handed out twice either way.
func TestSkipSurvivesACrash(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	st := openAt(t, dir)
	kept := create(t, st, 10)
	must(t, st.Close())
	copied := copyFolder(t, dir)
	st = openAt(t, dir)
	lost := create(t, st, 20)
	must(t, st.Close())

	skipIn(t, copied, 20)
	st = openAt(t, copied)
	before := create(t, st, 5) // into the newest chunk: no chunk is started, and state.json is not written
	st.abandon()

	st = openAt(t, copied)
	defer st.Close()
	after := create(t, st, 30)
	allDiffer(t, kept, lost, before, after)
	for _, l := range lost {
		notALink(t, st, l.Code)
	}
	found(t, st, kept)
	found(t, st, before)
}

func TestSkippedFileMustBeANumber(t *testing.T) {
	for _, content := range []string{"", "\n", "abc", "-5", "+5", "1.5", "12 34", "0x10", "18446744073709551616"} {
		dir := filepath.Join(t.TempDir(), "data")
		must(t, os.Mkdir(dir, 0o700))
		path := writeFile(t, dir, "skipped", content)
		if st, err := OpenStore(dir); err == nil {
			st.Close()
			t.Errorf("a skipped file holding %q was accepted", content)
		} else {
			wantErr(t, err, path)
		}
	}

	// Missing is 0; the number may end its line.
	dir := filepath.Join(t.TempDir(), "data")
	st := openAt(t, dir)
	total, err := st.Skip(7)
	must(t, err)
	eq(t, total, uint64(7))
	must(t, st.Close())
	path := writeFile(t, dir, "skipped", "250\n")
	st = openAt(t, dir)
	defer st.Close()
	total, err = st.Skip(1)
	must(t, err)
	eq(t, total, uint64(251))
	b, err := os.ReadFile(path)
	must(t, err)
	eq(t, string(b), "251\n")
	info, err := os.Stat(path)
	must(t, err)
	eq(t, info.Mode().Perm(), os.FileMode(0o600))

	// A total past the largest number is refused, and nothing is written.
	_, err = st.Skip(math.MaxUint64)
	wantErr(t, err, "251")
	b, err = os.ReadFile(path)
	must(t, err)
	eq(t, string(b), "251\n")
}

// A chunk started after a skip records its own first number as where its
// links end until a link is written, not where the chunk before it ended.
func TestStartedChunkRecordsItsFirstNumberAsItsEnd(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	st := openAt(t, dir)
	create(t, st, 10)
	must(t, st.Close())
	skipIn(t, dir, 200000) // ends in the middle of chunk 3

	st = openAt(t, dir)
	defer st.Close()
	create(t, st, 1) // starts chunk 3, which writes state.json, then takes 200,010
	eq(t, savedState(t, statePath(dir)), savedLength{Chunk: 3, Next: 200010, End: 3 * chunkLinks, Skipped: 200000})
}

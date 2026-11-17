// Tests for the chunk file: its bytes, the checks that keep a link from
// leading anywhere but to its own URL, and which writes a commit covers.
package app

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// newTestChunk creates a chunk for code length 5 on domain 1 in a fresh
// folder and closes it with the test.
func newTestChunk(t *testing.T) *chunkFile {
	t.Helper()
	c, err := createChunk(filepath.Join(t.TempDir(), "5-000000000000.chunk"), 5, 1, 0, t0, nil)
	must(t, err)
	t.Cleanup(func() { c.close() })
	return c
}

// putRecord appends r as the record of link number m and returns the slot
// that points at it.
func putRecord(t *testing.T, c *chunkFile, m uint64, flags uint8, r record) slot {
	t.Helper()
	s := slot{expires: 1000, owner: 1, flags: flagUsed | flags}
	var err error
	s.offset, s.length, err = c.appendRecord(recordCheck(c.length, m, s, r), s.flags, r)
	must(t, err)
	return s
}

// readsBack fails the test unless the record s points at is want and its
// stored check is the one for link number m.
func readsBack(t *testing.T, c *chunkFile, m uint64, s slot, want record) {
	t.Helper()
	check, got, err := c.readRecord(s)
	must(t, err)
	if got.afterOpen != want.afterOpen || !bytes.Equal(got.salt, want.salt) ||
		!bytes.Equal(got.hash, want.hash) || got.host != want.host || got.rest != want.rest {
		t.Fatalf("read back %+v, want %+v", got, want)
	}
	eq(t, check, recordCheck(c.length, m, s, want))
}

// fileSize returns the size of the file at path.
func fileSize(t *testing.T, path string) int64 {
	t.Helper()
	info, err := os.Stat(path)
	must(t, err)
	return info.Size()
}

// bytesAt returns n bytes of the file at path, starting at offset.
func bytesAt(t *testing.T, path string, offset int64, n int) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	must(t, err)
	return data[offset : offset+int64(n)]
}

func TestSplitJoinURL(t *testing.T) {
	same := []string{
		"https://example.com",
		"https://example.com/",
		"http://example.com/a?b=1#c",
		"https://example.com?x=1",
		"https://example.com#top",
		"https://user:pw@example.com:8443/p",
		"https://[2001:db8::1]:8080/x",
		"https://Example.COM/Path",
	}
	for _, u := range same {
		https, host, rest, ok := splitURL(u)
		eq(t, ok, true)
		eq(t, joinURL(https, host, rest), u)
	}
	https, host, rest, ok := splitURL("HTTPS://Example.COM/Path")
	eq(t, ok, true)
	eq(t, joinURL(https, host, rest), "https://Example.COM/Path") // only the scheme is lower-cased
	for _, bad := range []string{"", "example.com/x", "ftp://example.com", "https://", "https:///x"} {
		if _, _, _, ok := splitURL(bad); ok {
			t.Errorf("splitURL(%q) succeeded", bad)
		}
	}
}

func TestSlotDamageIsDetected(t *testing.T) {
	encoded := slot{offset: 77, length: 40, expires: 1000, owner: 9, flags: flagUsed | flagHTTPS}.encode()
	for bit := range slotSize * 8 {
		damaged := encoded
		damaged[bit/8] ^= 1 << (bit % 8)
		if _, state := decodeSlot(damaged[:]); state == slotOK {
			t.Fatalf("flipping bit %d went unnoticed", bit)
		}
	}
	_, state := decodeSlot(make([]byte, slotSize))
	eq(t, state, slotEmpty)
	// A matching check does not make a link of a slot without the used flag.
	encoded = slot{offset: 77, length: 40, expires: 1000, owner: 9, flags: flagHTTPS}.encode()
	_, state = decodeSlot(encoded[:])
	eq(t, state, slotDamaged)
}

func TestSlotRoundTrip(t *testing.T) {
	want := slot{offset: math.MaxUint32, length: math.MaxUint16, expires: math.MaxUint32, owner: 1<<24 - 1, flags: math.MaxUint8}
	encoded := want.encode()
	got, state := decodeSlot(encoded[:])
	eq(t, state, slotOK)
	eq(t, got, want)
}

// The check must change when anything that decides where the link leads,
// or whose it is, changes.
func TestRecordCheckCoversEverything(t *testing.T) {
	s := slot{owner: 7, flags: flagUsed | flagHTTPS | flagSecured | flagPassword}
	r := record{afterOpen: 60, salt: bytes.Repeat([]byte{1}, 16), hash: bytes.Repeat([]byte{2}, 32), host: "example.com", rest: "/a"}
	base := recordCheck(5, 100, s, r)
	changed := map[string]uint32{
		"length": recordCheck(6, 100, s, r),
		"number": recordCheck(5, 101, s, r),
	}
	other := s
	other.owner = 8
	changed["owner"] = recordCheck(5, 100, other, r)
	for name, flag := range map[string]uint8{"https": flagHTTPS, "secured": flagSecured, "password": flagPassword} {
		other = s
		other.flags ^= flag
		changed[name] = recordCheck(5, 100, other, r)
	}
	for name, change := range map[string]func(*record){
		"afterOpen": func(r *record) { r.afterOpen = 61 },
		"salt":      func(r *record) { r.salt = bytes.Repeat([]byte{3}, 16) },
		"hash":      func(r *record) { r.hash = bytes.Repeat([]byte{4}, 32) },
		"host":      func(r *record) { r.host = "example.org" },
		"rest":      func(r *record) { r.rest = "/b" },
	} {
		modified := r
		change(&modified)
		changed[name] = recordCheck(5, 100, s, modified)
	}
	for name, check := range changed {
		if check == base {
			t.Errorf("changing %s does not change the check", name)
		}
	}
	// Mutable slot fields are not part of it: they change during a link's life.
	other = s
	other.expires, other.flags = 99, s.flags|flagOpened|flagDeleted
	eq(t, recordCheck(5, 100, other, r), base)
}

func TestExpirySeconds(t *testing.T) {
	for _, at := range []time.Time{t0, t0.Add(MaxDuration), t0.Add(1500 * time.Millisecond)} {
		got, want := secondsToExpiry(expiryToSeconds(at)), at.Truncate(time.Second)
		if !got.Equal(want) {
			t.Errorf("%v comes back as %v, want %v", at, got, want)
		}
	}
	// Times outside the range are pinned to its ends instead of wrapping
	// around: a time before 2020 must not become one far in the future.
	eq(t, expiryToSeconds(time.Date(2019, 12, 31, 0, 0, 0, 0, time.UTC)), uint32(0))
	eq(t, expiryToSeconds(time.Date(2200, 1, 1, 0, 0, 0, 0, time.UTC)), uint32(math.MaxUint32))
}

func TestChunkCreateAndOpen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "7-000000000003.chunk")
	c, err := createChunk(path, 7, 2, 3, t0, nil)
	must(t, err)
	eq(t, fileSize(t, path), int64(recordsOffset))
	info, err := os.Stat(path)
	must(t, err)
	eq(t, info.Mode().Perm(), os.FileMode(0o600))
	must(t, c.close())

	c, err = openChunk(path)
	must(t, err)
	defer c.close()
	eq(t, c.length, 7)
	eq(t, c.domain, uint16(2))
	eq(t, c.index, uint64(3))
	for _, i := range []int{0, chunkLinks - 1} {
		_, state, err := c.readSlot(i)
		must(t, err)
		eq(t, state, slotEmpty)
	}
	last, err := c.lastUsedSlot()
	must(t, err)
	eq(t, last, -1)

	// An existing chunk is never overwritten.
	_, err = createChunk(path, 7, 2, 3, t0, nil)
	wantErr(t, err, "exists")
}

// A file that is not a chunk file says so with errNotChunk, which tells
// damage apart from an error of the system.
func TestOpenChunkRejectsOtherFiles(t *testing.T) {
	notChunk := func(err error, path string) {
		t.Helper()
		wantErr(t, err, path)
		if !errors.Is(err, errNotChunk) {
			t.Fatalf("error %q is not errNotChunk", err)
		}
	}
	dir := t.TempDir()
	zeros := writeFile(t, dir, "zeros.chunk", string(make([]byte, recordsOffset)))
	_, err := openChunk(zeros)
	notChunk(err, zeros)

	short := writeFile(t, dir, "short.chunk", "qisqa\x00ch")
	_, err = openChunk(short)
	notChunk(err, short)

	// A folder in the file's place is the system's error, not damage.
	_, err = openChunk(dir)
	if err == nil || errors.Is(err, errNotChunk) {
		t.Fatalf("opening a folder returned %v", err)
	}

	newer := filepath.Join(dir, "5-000000000000.chunk")
	c, err := createChunk(newer, 5, 1, 0, t0, nil)
	must(t, err)
	must(t, c.close())
	f, err := os.OpenFile(newer, os.O_WRONLY, 0)
	must(t, err)
	_, err = f.WriteAt([]byte{2}, 8) // the version byte
	must(t, err)
	must(t, f.Close())
	_, err = openChunk(newer)
	notChunk(err, newer)
}

// The bytes on disk are the format; they are pinned here.
func TestChunkBytes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "7-000000000000.chunk")
	c, err := createChunk(path, 7, 0x0201, 0x0a09080706050403, t0, nil)
	must(t, err)
	defer c.close()

	header := make([]byte, headerSize)
	copy(header, "qisqa\x00ch")
	header[8], header[9] = 1, 7
	binary.LittleEndian.PutUint16(header[10:], 0x0201)
	binary.LittleEndian.PutUint64(header[12:], 0x0a09080706050403)
	binary.LittleEndian.PutUint64(header[20:], uint64(t0.Unix()))
	eq(t, string(bytesAt(t, path, 0, headerSize)), string(header))

	must(t, c.writeSlot(2, slot{offset: 0x04030201, length: 0x0605, expires: 0x0a090807, owner: 0x0d0c0b, flags: flagUsed | flagPassword}))
	wantSlot := []byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, flagUsed | flagPassword}
	wantSlot = binary.LittleEndian.AppendUint16(wantSlot, uint16(crc32.Checksum(wantSlot, crc32.MakeTable(crc32.Castagnoli))))
	eq(t, string(bytesAt(t, path, slotsOffset+2*slotSize, slotSize)), string(wantSlot))

	salt, hash := bytes.Repeat([]byte{0xaa}, 16), bytes.Repeat([]byte{0xbb}, 32)
	r := record{afterOpen: 0x04030201, salt: salt, hash: hash, host: "example.com", rest: "/a"}
	s := putRecord(t, c, 0, flagSecured|flagPassword, r)
	eq(t, string(bytesAt(t, path, headerSize, 13)), "\x0bexample.com\x00")
	want := binary.LittleEndian.AppendUint32(nil, recordCheck(7, 0, s, r))
	want = append(want, 1, 2, 3, 4)
	want = append(append(append(want, salt...), hash...), 1) // host id 1
	want = append(want, "/a"...)
	eq(t, string(bytesAt(t, path, recordsOffset+int64(s.offset), int(s.length))), string(want))

	long := record{host: strings.Repeat("a", 248) + ".example", rest: "/b"} // 256 bytes: inline
	s = putRecord(t, c, 1, flagHTTPS, long)
	want = binary.LittleEndian.AppendUint32(nil, recordCheck(7, 1, s, long))
	want = append(want, 0, 0x80, 0x02) // host id 0, then 256 as a uvarint
	want = append(append(want, long.host...), long.rest...)
	eq(t, string(bytesAt(t, path, recordsOffset+int64(s.offset), int(s.length))), string(want))
}

func TestRecordRoundTrip(t *testing.T) {
	c := newTestChunk(t)
	salt, hash := bytes.Repeat([]byte{1}, 16), bytes.Repeat([]byte{2}, 32)
	long := "https://example.com/" + strings.Repeat("p", 2048-len("https://example.com/"))
	_, longHost, longRest, _ := splitURL(long)
	eq(t, len(longHost)+len(longRest), 2048-len("https://"))
	cases := []struct {
		name  string
		flags uint8
		r     record
	}{
		{"regular", flagHTTPS, record{host: "example.com", rest: "/a"}},
		{"secured", flagHTTPS | flagSecured, record{afterOpen: 3600, host: "example.com", rest: "/b"}},
		{"one-time", flagSecured, record{host: "example.org"}},
		{"password", flagPassword, record{salt: salt, hash: hash, host: "example.com", rest: "?q=1"}},
		{"secured and password", flagSecured | flagPassword, record{afterOpen: 60, salt: salt, hash: hash, host: "example.org", rest: "#x"}},
		{"2,048-byte URL", flagHTTPS, record{host: longHost, rest: longRest}},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := putRecord(t, c, uint64(i), tc.flags, tc.r)
			readsBack(t, c, uint64(i), s, tc.r)
		})
	}
}

// The slot's flags, not the record's fields, decide which fields are
// written: a one-time link has afterOpen 0 and is still secured.
func TestAppendRecordFollowsTheFlags(t *testing.T) {
	c := newTestChunk(t)
	oneTime := record{host: "example.com", rest: "/once"}
	s := putRecord(t, c, 0, flagSecured, oneTime)
	eq(t, int(s.length), 4+4+1+len(oneTime.rest)) // check, afterOpen, host id, rest
	readsBack(t, c, 0, s, oneTime)
	regular := putRecord(t, c, 1, 0, oneTime)
	eq(t, int(regular.length), 4+1+len(oneTime.rest)) // no afterOpen

	// A field the flags do not call for, or one of the wrong size, would not
	// read back as it was given and the record would never pass its check,
	// so it is refused at once.
	salt, hash := bytes.Repeat([]byte{1}, 16), bytes.Repeat([]byte{2}, 32)
	for name, tc := range map[string]struct {
		flags uint8
		r     record
	}{
		"afterOpen without secured": {0, record{afterOpen: 60, host: "example.com"}},
		"salt without password":     {0, record{salt: salt, hash: hash, host: "example.com"}},
		"hash without password":     {flagSecured, record{hash: hash, host: "example.com"}},
		"password without salt":     {flagPassword, record{hash: hash, host: "example.com"}},
		"short salt":                {flagPassword, record{salt: salt[:15], hash: hash, host: "example.com"}},
		"long hash":                 {flagPassword, record{salt: salt, hash: append(hash, 0), host: "example.com"}},
	} {
		if _, _, err := c.appendRecord(0, flagUsed|tc.flags, tc.r); err == nil {
			t.Errorf("%s: appendRecord succeeded", name)
		}
	}
}

func TestHostTable(t *testing.T) {
	t.Run("a host is entered once", func(t *testing.T) {
		c := newTestChunk(t)
		a := record{host: "example.com", rest: "/a"}
		b := record{host: "example.com", rest: "/b"}
		first := putRecord(t, c, 0, flagHTTPS, a)
		before := fileSize(t, c.path)
		second := putRecord(t, c, 1, flagHTTPS, b)
		eq(t, fileSize(t, c.path), before+int64(4+1+len(b.rest))) // check, host id, rest
		eq(t, len(c.hosts), 1)
		must(t, c.close())

		c, err := openChunk(c.path)
		must(t, err)
		defer c.close()
		readsBack(t, c, 0, first, a)
		readsBack(t, c, 1, second, b)
		before = fileSize(t, c.path)
		third := record{host: "example.com", rest: "/c"}
		putRecord(t, c, 2, flagHTTPS, third)
		eq(t, fileSize(t, c.path), before+int64(4+1+len(third.rest)))
		eq(t, len(c.hosts), 1)
	})

	t.Run("hosts that do not fit are written inline", func(t *testing.T) {
		c := newTestChunk(t)
		var slots []slot
		var records []record
		put := func(r record) slot {
			s := putRecord(t, c, uint64(len(slots)), 0, r)
			slots, records = append(slots, s), append(records, r)
			return s
		}
		long := record{host: strings.Repeat("a", 248) + ".example", rest: "/long"} // 256 bytes
		s := put(long)
		eq(t, int(s.length), 4+1+2+len(long.host)+len(long.rest)) // a two-byte uvarint length
		eq(t, len(c.hosts), 0)                                    // although there is room
		for i := range maxHosts {
			put(record{host: fmt.Sprintf("h%d.example", i), rest: "/"})
		}
		eq(t, len(c.hosts), maxHosts)
		extra := record{host: "extra.example", rest: "/x"}
		s = put(extra)
		eq(t, int(s.length), 4+1+1+len(extra.host)+len(extra.rest))
		eq(t, len(c.hosts), maxHosts)
		must(t, c.close())

		c, err := openChunk(c.path)
		must(t, err)
		defer c.close()
		eq(t, len(c.hosts), maxHosts)
		for i, s := range slots {
			readsBack(t, c, uint64(i), s, records[i])
		}
	})

	t.Run("the table never reaches the slots", func(t *testing.T) {
		c := newTestChunk(t)
		c.onWrite = func(offset int64, b []byte) {
			if offset < recordsOffset && offset+int64(len(b)) > slotsOffset {
				t.Errorf("a write of %d bytes at byte %d reaches the slots", len(b), offset)
			}
		}
		var slots []slot
		var records []record
		for i := range 300 {
			r := record{host: fmt.Sprintf("%s%03d.example", strings.Repeat("h", 189), i), rest: "/"} // 200 bytes
			slots, records = append(slots, putRecord(t, c, uint64(i), 0, r)), append(records, r)
		}
		eq(t, len(c.hosts), hostTableSize/201) // as many entries as fit
		_, state, err := c.readSlot(0)
		must(t, err)
		eq(t, state, slotEmpty)
		for i, s := range slots {
			readsBack(t, c, uint64(i), s, records[i])
		}
	})

	t.Run("a damaged table gives at most 255 hosts", func(t *testing.T) {
		c := newTestChunk(t)
		must(t, c.close())
		f, err := os.OpenFile(c.path, os.O_WRONLY, 0)
		must(t, err)
		_, err = f.WriteAt(bytes.Repeat([]byte{1, 'h'}, 300), headerSize) // 300 entries
		must(t, err)
		must(t, f.Close())
		c, err = openChunk(c.path)
		must(t, err)
		defer c.close()
		eq(t, len(c.hosts), maxHosts)
	})
}

// A record appended to a cut file goes where the records begin, not at the
// end of what is left, which would be among the slots.
func TestCutChunkKeepsRecordsOutOfTheSlots(t *testing.T) {
	c := newTestChunk(t)
	must(t, c.close())
	must(t, os.Truncate(c.path, slotsOffset+100))
	c, err := openChunk(c.path)
	must(t, err)
	defer c.close()
	r := record{host: "example.com", rest: "/a"}
	s := putRecord(t, c, 0, flagHTTPS, r)
	eq(t, s.offset, uint32(0))
	readsBack(t, c, 0, s, r)
}

func TestReadRecordRejectsBadSlots(t *testing.T) {
	c := newTestChunk(t)
	s := putRecord(t, c, 0, flagHTTPS, record{host: "example.com", rest: "/a"})
	for name, bad := range map[string]slot{
		"far past the end":        {offset: 1 << 30, length: 10, flags: flagUsed},
		"one byte past the end":   {offset: s.offset, length: s.length + 1, flags: flagUsed},
		"too short for its flags": {offset: s.offset, length: s.length, flags: flagUsed | flagSecured | flagPassword},
	} {
		if _, _, err := c.readRecord(bad); err == nil {
			t.Errorf("%s: readRecord succeeded", name)
		}
	}

	putRecord(t, c, 1, flagHTTPS, record{host: "example.org", rest: "/b"})
	third := putRecord(t, c, 2, flagHTTPS, record{host: "example.com", rest: "/c"})
	eq(t, len(c.hosts), 2)
	_, err := c.f.WriteAt([]byte{9}, recordsOffset+int64(third.offset)+4) // the host id
	must(t, err)
	_, _, err = c.readRecord(third)
	wantErr(t, err, "host 9")

	inline := putRecord(t, c, 3, flagHTTPS, record{host: strings.Repeat("a", 248) + ".example", rest: "/d"})
	_, err = c.f.WriteAt([]byte{0xff, 0x7f}, recordsOffset+int64(inline.offset)+5) // the host's length
	must(t, err)
	if _, _, err := c.readRecord(inline); err == nil {
		t.Error("a host longer than its record was read")
	}
}

func TestZero(t *testing.T) {
	c := newTestChunk(t)
	rest := []byte("/a-path-that-must-go")
	s := putRecord(t, c, 0, flagHTTPS, record{host: "example.com", rest: string(rest)})
	data, err := os.ReadFile(c.path)
	must(t, err)
	eq(t, bytes.Contains(data, rest), true)
	must(t, c.zero(s.offset, s.length))
	data, err = os.ReadFile(c.path)
	must(t, err)
	eq(t, bytes.Contains(data, rest), false)
}

func TestLastUsedSlot(t *testing.T) {
	c := newTestChunk(t)
	written := slot{offset: 3, length: 10, expires: 1000, owner: 1, flags: flagUsed}
	for _, i := range []int{0, 1, 5} {
		must(t, c.writeSlot(i, written))
	}
	got, state, err := c.readSlot(5)
	must(t, err)
	eq(t, state, slotOK)
	eq(t, got, written)
	last, err := c.lastUsedSlot()
	must(t, err)
	eq(t, last, 5)

	// A damaged slot counts as used: its number may have been handed out, and
	// a number is never handed out twice.
	_, err = c.f.WriteAt([]byte{1}, slotsOffset+9*slotSize+3)
	must(t, err)
	_, state, err = c.readSlot(9)
	must(t, err)
	eq(t, state, slotDamaged)
	last, err = c.lastUsedSlot()
	must(t, err)
	eq(t, last, 9)
}

// The highest used slot is found wherever it is, whatever blocks of empty
// slots lie below or above it.
func TestLastUsedSlotAcrossBlocks(t *testing.T) {
	written := slot{offset: 3, length: 10, expires: 1000, owner: 1, flags: flagUsed}
	for _, tc := range []struct {
		used []int
		want int
	}{
		{nil, -1},
		{[]int{0}, 0},
		{[]int{255}, 255}, // the last slot of the first 4 KB block
		{[]int{256}, 256}, // the first slot of the second
		{[]int{chunkLinks - 1}, chunkLinks - 1},
		{[]int{10, 5000}, 5000}, // blocks of zeros between them
	} {
		c := newTestChunk(t)
		for _, i := range tc.used {
			must(t, c.writeSlot(i, written))
		}
		last, err := c.lastUsedSlot()
		must(t, err)
		if last != tc.want {
			t.Errorf("slots %v used: the last used is %d, want %d", tc.used, last, tc.want)
		}
	}

	// A damaged slot above the last good one counts, down to a single byte.
	c := newTestChunk(t)
	must(t, c.writeSlot(10, written))
	_, err := c.f.WriteAt([]byte{1}, slotsOffset+5000*slotSize+slotSize-1)
	must(t, err)
	_, state, err := c.readSlot(5000)
	must(t, err)
	eq(t, state, slotDamaged)
	last, err := c.lastUsedSlot()
	must(t, err)
	eq(t, last, 5000)

	// A file cut short within its slots cannot be read, as before.
	must(t, c.f.Truncate(slotsOffset+5001*slotSize))
	if _, err := c.lastUsedSlot(); err == nil {
		t.Fatal("lastUsedSlot read a file cut short within its slots")
	}
}

// The writes onWrite reports, applied to the file as it was created, give
// the file as it is: no write passes the hook by.
func TestOnWriteSeesEveryWrite(t *testing.T) {
	c := newTestChunk(t)
	image, err := os.ReadFile(c.path)
	must(t, err)
	c.onWrite = func(offset int64, b []byte) {
		if grow := offset + int64(len(b)) - int64(len(image)); grow > 0 {
			image = append(image, make([]byte, grow)...)
		}
		copy(image[offset:], b)
	}
	first := putRecord(t, c, 0, flagHTTPS, record{host: "example.com", rest: "/a"})
	must(t, c.writeSlot(0, first))
	second := putRecord(t, c, 1, flagHTTPS, record{host: strings.Repeat("a", 248) + ".example", rest: "/b"})
	must(t, c.writeSlot(1, second))
	must(t, c.zero(first.offset, first.length))
	data, err := os.ReadFile(c.path)
	must(t, err)
	eq(t, bytes.Equal(data, image), true)
}

// commit covers the writes that had returned when it was called. A write
// that returns while its sync runs, as the store's writes do while its
// syncing goroutine commits, is left to the next commit; a commit with
// nothing new to cover makes no sync.
func TestCommitCoversTheWritesBeforeIt(t *testing.T) {
	c := newTestChunk(t)
	putRecord(t, c, 0, flagHTTPS, record{host: "example.com", rest: "/a"})
	running, release := make(chan struct{}), make(chan struct{})
	syncs := 0
	c.sync = func() error {
		if syncs++; syncs == 1 {
			close(running)
			<-release
		}
		return nil
	}
	first := make(chan error)
	go func() { first <- c.commit() }()
	<-running
	if _, _, err := c.appendRecord(0, flagUsed|flagHTTPS, record{host: "example.com", rest: "/b"}); err != nil {
		t.Fatalf("appendRecord during the sync: %v", err)
	}
	close(release)
	must(t, <-first)
	eq(t, syncs, 1)
	must(t, c.commit()) // the record written during the first sync
	eq(t, syncs, 2)
	must(t, c.commit()) // nothing new
	eq(t, syncs, 2)
}

// After a failed sync nothing written since the last good one can be
// trusted to reach the disk, whatever a later sync says. The file takes no
// more writes and every commit fails, until it is opened again.
func TestFailedSyncStopsWrites(t *testing.T) {
	c := newTestChunk(t)
	r := record{host: "example.com", rest: "/a"}
	s := putRecord(t, c, 0, flagHTTPS, r)
	must(t, c.writeSlot(0, s))
	failure := errors.New("disk full")
	syncs := 0
	c.sync = func() error {
		syncs++
		if syncs == 1 {
			return failure
		}
		return nil
	}
	for range 3 { // now and later, although the hook would succeed again
		if err := c.commit(); !errors.Is(err, failure) {
			t.Fatalf("commit returned %v, want %v", err, failure)
		}
	}
	eq(t, syncs, 1)
	if err := c.failed(); !errors.Is(err, failure) {
		t.Fatalf("failed() returned %v, want %v", err, failure)
	}

	before, err := os.ReadFile(c.path)
	must(t, err)
	c.onWrite = func(offset int64, b []byte) {
		t.Errorf("a write of %d bytes at byte %d after the failure", len(b), offset)
	}
	if _, _, err := c.appendRecord(0, flagUsed|flagHTTPS, record{host: "example.org", rest: "/b"}); !errors.Is(err, failure) {
		t.Errorf("appendRecord returned %v, want %v", err, failure)
	}
	if err := c.writeSlot(1, s); !errors.Is(err, failure) {
		t.Errorf("writeSlot returned %v, want %v", err, failure)
	}
	if err := c.zero(s.offset, s.length); !errors.Is(err, failure) {
		t.Errorf("zero returned %v, want %v", err, failure)
	}
	after, err := os.ReadFile(c.path)
	must(t, err)
	eq(t, bytes.Equal(after, before), true)

	got, state, err := c.readSlot(0)
	must(t, err)
	eq(t, state, slotOK)
	eq(t, got, s)
	readsBack(t, c, 0, s, r)
	last, err := c.lastUsedSlot()
	must(t, err)
	eq(t, last, 0)
	must(t, c.close())

	c, err = openChunk(c.path)
	must(t, err)
	defer c.close()
	must(t, c.failed())
	next := putRecord(t, c, 1, flagHTTPS, record{host: "example.org", rest: "/b"})
	must(t, c.writeSlot(1, next))
	must(t, c.zero(s.offset, s.length))
	must(t, c.commit())
}

// A record that lands while a sync is running relies on a later sync. If
// the running one fails, the commit that would cover the record fails too,
// without a sync, even when that sync would succeed.
func TestCommitDuringAFailingSync(t *testing.T) {
	c := newTestChunk(t)
	putRecord(t, c, 0, flagHTTPS, record{host: "example.com", rest: "/a"})
	failure := errors.New("disk full")
	running, release := make(chan struct{}), make(chan struct{})
	calls := 0
	c.sync = func() error {
		if calls++; calls > 1 {
			return nil
		}
		close(running)
		<-release
		return failure
	}
	first := make(chan error)
	go func() { first <- c.commit() }()
	<-running
	if _, _, err := c.appendRecord(0, flagUsed|flagHTTPS, record{host: "example.com", rest: "/b"}); err != nil {
		t.Fatalf("appendRecord during the sync: %v", err)
	}
	close(release)
	if err := <-first; !errors.Is(err, failure) {
		t.Errorf("the commit that ran the sync returned %v, want %v", err, failure)
	}
	if err := c.commit(); !errors.Is(err, failure) {
		t.Errorf("the commit of the record written during the sync returned %v, want %v", err, failure)
	}
	eq(t, calls, 1)
}

// A write that fails is reported to its caller only: unlike a failed sync,
// it leaves the file usable.
func TestFailedWriteDoesNotFailTheFile(t *testing.T) {
	c := newTestChunk(t)
	writable := c.f
	readOnly, err := os.Open(c.path)
	must(t, err)
	defer readOnly.Close()
	s := slot{offset: 0, length: 10, expires: 1000, owner: 1, flags: flagUsed}
	c.write = readOnly.WriteAt
	if err := c.writeSlot(0, s); err == nil {
		t.Fatal("a write through a read-only handle succeeded")
	}
	c.write = writable.WriteAt
	must(t, c.failed())
	must(t, c.writeSlot(0, s))
	must(t, c.commit())
}

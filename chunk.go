// Chunk files: one file holds 65,536 links of one code length on one domain.
// A link's number picks a 16-byte slot, and the slot points at a record in
// the same file that holds the URL. The checks are shaped so that damage to
// the file makes a link lead nowhere, never to another URL.
package main

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"math"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// The layout of a chunk file. A slot is 16 bytes so that it never straddles
// a 512-byte disk sector, which lets it be rewritten in place without a
// journal.
const (
	chunkLinks    = 65536                             // links in one file
	headerSize    = 64                                // at byte 0
	hostTableSize = 8128                              // at byte 64
	slotsOffset   = headerSize + hostTableSize        // 8,192
	slotSize      = 16                                // one per link
	recordsOffset = slotsOffset + chunkLinks*slotSize // 1,056,768; records are appended from here
	saltSize      = 16                                // of a password link
	hashSize      = 32                                // of a password link
)

// maxHosts is how many hosts the host table holds: host ids are 1 to 255; 0
// means the host is in the record. It is a variable so that a benchmark can
// switch the table off.
var maxHosts = 255

// chunkMagic opens every chunk file; chunkVersion is the layout described
// in this file.
const (
	chunkMagic   = "qisqa\x00ch"
	chunkVersion = 1
)

// Slot flags. Used, https, secured and password are set when the link is
// created and never change; deleted and opened are set later.
const (
	flagUsed     = 1 << iota // the slot holds a link
	flagHTTPS                // the scheme is https rather than http
	flagDeleted              // the link was deleted
	flagSecured              // the link expires a while after it is first opened
	flagOpened               // the secured link has been opened
	flagPassword             // the link asks for a password
)

// fixedFlags are the flags that take part in a record's check.
const fixedFlags = flagHTTPS | flagSecured | flagPassword

// errNotChunk marks the errors of a file whose bytes are not those of a
// chunk file this qisqa reads: damage, as opposed to an error of the system
// that holds the file.
var errNotChunk = errors.New("not a chunk file")

// castagnoli is the CRC-32C table used by every check in the file.
var castagnoli = crc32.MakeTable(crc32.Castagnoli)

// slot is the fixed-size entry of one link. On disk, little-endian: offset
// u32, length u16, expires u32, owner u24, flags u8, then the low 16 bits of
// the CRC-32C of those 14 bytes.
type slot struct {
	offset  uint32 // where the record starts, counted from recordsOffset
	length  uint16 // the record's size in bytes
	expires uint32 // seconds since expiryEpoch
	owner   uint32 // account number; only the low 24 bits are stored
	flags   uint8
}

// slotState says what the bytes of a slot hold.
type slotState int

const (
	slotEmpty   slotState = iota // all zeros: no link was stored here
	slotOK                       // a link whose check matches
	slotDamaged                  // anything else
)

// encode returns the slot's bytes as stored in the file.
func (s slot) encode() [slotSize]byte {
	var b [slotSize]byte
	binary.LittleEndian.PutUint32(b[0:], s.offset)
	binary.LittleEndian.PutUint16(b[4:], s.length)
	binary.LittleEndian.PutUint32(b[6:], s.expires)
	b[10], b[11], b[12] = byte(s.owner), byte(s.owner>>8), byte(s.owner>>16)
	b[13] = s.flags
	binary.LittleEndian.PutUint16(b[14:], slotCheck(b[:14]))
	return b
}

// decodeSlot reads a slot from its first slotSize bytes. A slot without the
// used flag or with a check that does not match is damaged.
func decodeSlot(b []byte) (slot, slotState) {
	b = b[:slotSize]
	var empty [slotSize]byte
	if bytes.Equal(b, empty[:]) {
		return slot{}, slotEmpty
	}
	s := slot{
		offset:  binary.LittleEndian.Uint32(b[0:]),
		length:  binary.LittleEndian.Uint16(b[4:]),
		expires: binary.LittleEndian.Uint32(b[6:]),
		owner:   uint32(b[10]) | uint32(b[11])<<8 | uint32(b[12])<<16,
		flags:   b[13],
	}
	if s.flags&flagUsed == 0 || binary.LittleEndian.Uint16(b[14:]) != slotCheck(b[:14]) {
		return slot{}, slotDamaged
	}
	return s, slotOK
}

// slotCheck is the low 16 bits of the CRC-32C of a slot's first 14 bytes.
func slotCheck(b []byte) uint16 {
	return uint16(crc32.Checksum(b, castagnoli))
}

// expiryEpoch is the zero of slot expiry times. Whole seconds from it fit in
// 32 bits until the year 2156.
var expiryEpoch = time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)

// expiryToSeconds converts an expiry time to slot form, dropping the
// fraction of a second. A time before the epoch becomes 0, one that does not
// fit becomes the largest value: an expired link stays expired.
func expiryToSeconds(t time.Time) uint32 {
	s := t.Unix() - expiryEpoch.Unix()
	return uint32(min(max(s, 0), math.MaxUint32))
}

// secondsToExpiry converts a slot's expiry back to a time in UTC.
func secondsToExpiry(s uint32) time.Time {
	return time.Unix(expiryEpoch.Unix()+int64(s), 0).UTC()
}

// splitURL cuts an http or https URL into its scheme, its host part and the
// rest. The scheme may be in any letter case. The host part runs from after
// "://" to the first "/", "?" or "#" and is kept as written, user info and
// port included; the rest is everything after it. ok is false for another
// scheme or an empty host part.
func splitURL(raw string) (https bool, host, rest string, ok bool) {
	var after string
	switch {
	case hasPrefixFold(raw, "https://"):
		https, after = true, raw[len("https://"):]
	case hasPrefixFold(raw, "http://"):
		after = raw[len("http://"):]
	default:
		return false, "", "", false
	}
	end := strings.IndexAny(after, "/?#")
	if end < 0 {
		end = len(after)
	}
	if end == 0 {
		return false, "", "", false
	}
	return https, after[:end], after[end:], true
}

// hasPrefixFold reports whether s starts with the ASCII prefix in any
// letter case.
func hasPrefixFold(s, prefix string) bool {
	return len(s) >= len(prefix) && strings.EqualFold(s[:len(prefix)], prefix)
}

// joinURL puts a URL cut by splitURL back together; the scheme comes back
// in lower case.
func joinURL(https bool, host, rest string) string {
	scheme := "http://"
	if https {
		scheme = "https://"
	}
	return scheme + host + rest
}

// record is what a slot points at. A record never changes after it is
// written: a new URL gets a new record. On disk: [check u32], [afterOpen
// u32] if the link is secured, [salt][hash] if it has a password, [host id
// u8], [uvarint length][host] if the id is 0, and the rest of the URL.
type record struct {
	afterOpen  uint32 // a secured link lives this many seconds after its first open
	salt, hash []byte // a password link's; empty otherwise
	host, rest string // the URL as cut by splitURL
}

// recordCheck is the CRC-32C of everything that decides where a link leads
// and whose it is: the code length and number, the owner, the flags fixed at
// creation and the record's fields, with the host as text rather than its
// id in the table. A record read through another link's slot, a host table
// entry that was lost or replaced, or a damaged byte therefore fails the
// check, and the link is refused instead of sent elsewhere. The expiry and
// the deleted and opened flags change during a link's life and are left out.
func recordCheck(length int, m uint64, s slot, r record) uint32 {
	b := make([]byte, 0, 32+len(r.salt)+len(r.hash)+len(r.host)+len(r.rest))
	b = append(b, byte(length))
	b = binary.LittleEndian.AppendUint64(b, m)
	b = binary.LittleEndian.AppendUint32(b, s.owner)
	b = append(b, s.flags&fixedFlags)
	b = binary.LittleEndian.AppendUint32(b, r.afterOpen)
	// Each field but the last carries its length, so that bytes cannot move
	// from one field to the next without changing the check.
	for _, field := range [][]byte{r.salt, r.hash, []byte(r.host)} {
		b = binary.AppendUvarint(b, uint64(len(field)))
		b = append(b, field...)
	}
	b = append(b, r.rest...)
	return crc32.Checksum(b, castagnoli)
}

// chunkFile is one open chunk file. Its methods are safe for concurrent use;
// keeping a link's slot and record consistent is up to the caller.
type chunkFile struct {
	path   string
	f      *os.File
	length int    // the code length of the links in the file
	domain uint16 // the domain they belong to
	index  uint64 // the file's place among the files of that domain and length

	mu       sync.RWMutex
	hosts    []string         // the host table: hosts[i] has id i+1
	hostIDs  map[string]uint8 // the same table by host
	tableEnd int64            // where the next table entry goes
	end      int64            // the end of the file, where the next record goes
	// failure is the error of the first sync that failed. After it nothing
	// written since the last good sync can be trusted to reach the disk,
	// whatever a later sync says: the file takes no more writes and every
	// commit returns this error. Only opening the file again clears it.
	failure error

	written atomic.Int64 // writes made so far
	synced  int64        // writes up to this number are on disk; only commit uses it

	// Test hooks. sync makes the writes durable; it is the file's Sync.
	// write makes one write; it is the file's WriteAt. onWrite, when set,
	// sees every write just before it is made.
	sync    func() error
	write   func(b []byte, offset int64) (int, error)
	onWrite func(offset int64, b []byte)
}

// createChunk creates the chunk file at path with mode 0600. An existing
// file is never overwritten. The header is written, the file is extended to
// recordsOffset, so every slot reads as empty, and the file is synced before
// createChunk returns, so that its head is on disk before its folder names
// it. That is the one sync of a chunk file not made by the store's syncing
// goroutine: the file holds no link yet, and no other call has it. hook,
// when not nil, is given the file before anything is written to it, so that
// the test hooks it sets see the header and the sync too.
func createChunk(path string, length int, domain uint16, index uint64, now time.Time, hook func(*chunkFile)) (*chunkFile, error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, err
	}
	c := newChunkFile(path, f, length, domain, index)
	if hook != nil {
		hook(c)
	}
	var header [headerSize]byte
	copy(header[:], chunkMagic)
	header[8] = chunkVersion
	header[9] = byte(length)
	binary.LittleEndian.PutUint16(header[10:], domain)
	binary.LittleEndian.PutUint64(header[12:], index)
	binary.LittleEndian.PutUint64(header[20:], uint64(now.Unix()))
	err = c.writeAt(header[:], 0)
	if err == nil {
		err = f.Truncate(recordsOffset)
	}
	if err == nil {
		err = c.sync()
	}
	if err != nil {
		f.Close()
		os.Remove(path)
		return nil, err
	}
	return c, nil
}

// openChunk opens an existing chunk file and reads its header and host
// table. A file with another magic or format version is refused.
func openChunk(path string) (*chunkFile, error) {
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return nil, err
	}
	c, err := readChunk(path, f)
	if err != nil {
		f.Close()
		return nil, err
	}
	return c, nil
}

// readChunk reads the header and the host table of the open file f.
func readChunk(path string, f *os.File) (*chunkFile, error) {
	// A file cut short within the host table keeps what it has; the rest
	// reads as zeros, which end the table.
	b := make([]byte, slotsOffset)
	n, err := f.ReadAt(b, 0)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	if n < headerSize || string(b[:len(chunkMagic)]) != chunkMagic {
		return nil, fmt.Errorf("%s is %w", path, errNotChunk)
	}
	if b[8] != chunkVersion {
		return nil, fmt.Errorf("%s is %w of format version %d: it has version %d", path, errNotChunk, chunkVersion, b[8])
	}
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	c := newChunkFile(path, f, int(b[9]), binary.LittleEndian.Uint16(b[10:]), binary.LittleEndian.Uint64(b[12:]))
	// Records are never appended inside the slots, even to a cut file.
	c.end = max(info.Size(), recordsOffset)
	c.loadHosts(b[headerSize:slotsOffset])
	return c, nil
}

// newChunkFile returns the in-memory state of a chunk file with an empty
// host table.
func newChunkFile(path string, f *os.File, length int, domain uint16, index uint64) *chunkFile {
	return &chunkFile{
		path: path, f: f, length: length, domain: domain, index: index,
		hostIDs: make(map[string]uint8), tableEnd: headerSize, end: recordsOffset,
		sync: f.Sync, write: f.WriteAt,
	}
}

// loadHosts reads the host table: [length u8][host] entries up to a zero
// length, the 255th entry or the end of the table. An entry that runs past
// the end of the table can only be damaged and ends it too; the next host
// entered takes its place, and the records that named it fail their checks.
func (c *chunkFile) loadHosts(table []byte) {
	pos := 0
	for len(c.hosts) < maxHosts && pos < len(table) {
		n := int(table[pos])
		if n == 0 || pos+1+n > len(table) {
			break
		}
		c.addHost(string(table[pos+1 : pos+1+n]))
		pos += 1 + n
	}
	c.tableEnd = headerSize + int64(pos)
}

// addHost gives host the next id in the in-memory table. A host that is
// already there, which only damage can cause, keeps its first id.
func (c *chunkFile) addHost(host string) {
	c.hosts = append(c.hosts, host)
	if _, ok := c.hostIDs[host]; !ok {
		c.hostIDs[host] = uint8(len(c.hosts))
	}
}

// writeAt writes b at offset. Every write to the file goes through here, so
// that onWrite sees it and commit knows which writes a sync covers.
func (c *chunkFile) writeAt(b []byte, offset int64) error {
	if c.onWrite != nil {
		c.onWrite(offset, b)
	}
	_, err := c.write(b, offset)
	// Counted only once the write has returned, so that a sync that starts
	// after reading the count covers it.
	c.written.Add(1)
	return err
}

// readSlot reads slot i, a number in [0, chunkLinks).
func (c *chunkFile) readSlot(i int) (slot, slotState, error) {
	if i < 0 || i >= chunkLinks {
		return slot{}, slotDamaged, fmt.Errorf("slot %d does not exist", i)
	}
	var b [slotSize]byte
	if _, err := c.f.ReadAt(b[:], slotsOffset+int64(i)*slotSize); err != nil {
		return slot{}, slotDamaged, fmt.Errorf("read slot %d of %s: %w", i, c.path, err)
	}
	s, state := decodeSlot(b[:])
	return s, state, nil
}

// writeSlot writes s into slot i. The flags of s must include flagUsed.
func (c *chunkFile) writeSlot(i int, s slot) error {
	if i < 0 || i >= chunkLinks {
		return fmt.Errorf("slot %d does not exist", i)
	}
	// Held through the write, so that no write starts once the file failed.
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.failure != nil {
		return c.failure
	}
	b := s.encode()
	return c.writeAt(b[:], slotsOffset+int64(i)*slotSize)
}

// appendRecord writes r at the end of the file and returns where it is, for
// the slot. flags are those of the slot that will point at it: flagSecured
// and flagPassword decide which fields are written, as they decide which
// are read. The host is entered into the host table if it is not there yet
// and there is room; otherwise it is written into the record.
func (c *chunkFile) appendRecord(check uint32, flags uint8, r record) (offset uint32, length uint16, err error) {
	secured, password := flags&flagSecured != 0, flags&flagPassword != 0
	switch {
	case !secured && r.afterOpen != 0:
		return 0, 0, errors.New("a link that is not secured has no afterOpen")
	case password && (len(r.salt) != saltSize || len(r.hash) != hashSize):
		return 0, 0, fmt.Errorf("a password link needs a %d-byte salt and a %d-byte hash", saltSize, hashSize)
	case !password && (len(r.salt) != 0 || len(r.hash) != 0):
		return 0, 0, errors.New("a link without a password has no salt or hash")
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.failure != nil {
		return 0, 0, c.failure
	}
	id, enter := c.tableID(r.host)

	b := make([]byte, 0, 4+4+saltSize+hashSize+1+binary.MaxVarintLen64+len(r.host)+len(r.rest))
	b = binary.LittleEndian.AppendUint32(b, check)
	if secured {
		b = binary.LittleEndian.AppendUint32(b, r.afterOpen)
	}
	b = append(append(b, r.salt...), r.hash...)
	b = append(b, id)
	if id == 0 {
		b = binary.AppendUvarint(b, uint64(len(r.host)))
		b = append(b, r.host...)
	}
	b = append(b, r.rest...)
	if len(b) > math.MaxUint16 {
		return 0, 0, fmt.Errorf("a record of %d bytes is too long", len(b))
	}
	if c.end-recordsOffset+int64(len(b)) > math.MaxUint32 {
		return 0, 0, fmt.Errorf("%s is full", c.path)
	}

	if enter {
		entry := append([]byte{byte(len(r.host))}, r.host...)
		if err := c.writeAt(entry, c.tableEnd); err != nil {
			return 0, 0, err
		}
		c.tableEnd += int64(len(entry))
		c.addHost(r.host)
	}
	if err := c.writeAt(b, c.end); err != nil {
		return 0, 0, err
	}
	offset, length = uint32(c.end-recordsOffset), uint16(len(b))
	c.end += int64(len(b))
	return offset, length, nil
}

// rewriteHead writes the header and the host table back exactly as the file
// holds them. Whoever goes on writing to a file it did not create calls it
// before any other write: after a failed sync the system may keep such
// bytes in memory, marked as saved, although they never reached the disk;
// written again, they are part of the next sync.
func (c *chunkFile) rewriteHead() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.failure != nil {
		return c.failure
	}
	b := make([]byte, slotsOffset)
	if _, err := c.f.ReadAt(b, 0); err != nil {
		return fmt.Errorf("read the head of %s: %w", c.path, err)
	}
	return c.writeAt(b, 0)
}

// tableID returns the id a new record names its host by: the host's id in
// the table, the id it is about to be entered under (enter is then true),
// or 0 when the host is written into the record. The caller holds c.mu.
func (c *chunkFile) tableID(host string) (id uint8, enter bool) {
	if id, ok := c.hostIDs[host]; ok {
		return id, false
	}
	if host == "" || len(host) > math.MaxUint8 || len(c.hosts) >= maxHosts ||
		c.tableEnd+1+int64(len(host)) > slotsOffset {
		return 0, false
	}
	return uint8(len(c.hosts) + 1), true
}

// readRecord reads the record s points at and returns its stored check with
// its fields. It fails when the slot points outside the file, when the
// record is too short for the fields the flags of s call for, or when it
// names a host the table does not have. Comparing the check with
// recordCheck is up to the caller, who knows the link's number.
func (c *chunkFile) readRecord(s slot) (check uint32, r record, err error) {
	start := recordsOffset + int64(s.offset)
	c.mu.RLock()
	end := c.end
	c.mu.RUnlock()
	if start+int64(s.length) > end {
		return 0, record{}, fmt.Errorf("slot points outside %s", c.path)
	}
	b := make([]byte, s.length)
	if _, err := c.f.ReadAt(b, start); err != nil {
		return 0, record{}, fmt.Errorf("read record at byte %d of %s: %w", start, c.path, err)
	}
	malformed := func() error {
		return fmt.Errorf("record at byte %d of %s does not fit its slot", start, c.path)
	}

	need := 4 + 1
	if s.flags&flagSecured != 0 {
		need += 4
	}
	if s.flags&flagPassword != 0 {
		need += saltSize + hashSize
	}
	if len(b) < need {
		return 0, record{}, malformed()
	}
	check, b = binary.LittleEndian.Uint32(b), b[4:]
	if s.flags&flagSecured != 0 {
		r.afterOpen, b = binary.LittleEndian.Uint32(b), b[4:]
	}
	if s.flags&flagPassword != 0 {
		r.salt, r.hash, b = b[:saltSize:saltSize], b[saltSize:saltSize+hashSize:saltSize+hashSize], b[saltSize+hashSize:]
	}
	id, b := b[0], b[1:]
	if id == 0 {
		n, k := binary.Uvarint(b)
		if k <= 0 || n > uint64(len(b)-k) {
			return 0, record{}, malformed()
		}
		r.host, b = string(b[k:k+int(n)]), b[k+int(n):]
	} else {
		c.mu.RLock()
		if int(id) <= len(c.hosts) {
			r.host = c.hosts[id-1]
		}
		hosts := len(c.hosts)
		c.mu.RUnlock()
		if int(id) > hosts {
			return 0, record{}, fmt.Errorf("record at byte %d of %s names host %d; the table has %d", start, c.path, id, hosts)
		}
	}
	r.rest = string(b)
	return check, r, nil
}

// zero overwrites a record with zeros, so that a deleted or replaced URL
// does not stay in the file.
func (c *chunkFile) zero(offset uint32, length uint16) error {
	start := recordsOffset + int64(offset)
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.failure != nil {
		return c.failure
	}
	if start+int64(length) > c.end {
		return fmt.Errorf("record to zero lies outside %s", c.path)
	}
	return c.writeAt(make([]byte, length), start)
}

// lastUsedSlot returns the highest slot that is not empty, or -1 when all
// are. A damaged slot counts as used: its number may have been handed out,
// and a number is never handed out twice.
func (c *chunkFile) lastUsedSlot() (int, error) {
	b := make([]byte, recordsOffset-slotsOffset)
	if _, err := c.f.ReadAt(b, slotsOffset); err != nil {
		return 0, fmt.Errorf("read the slots of %s: %w", c.path, err)
	}
	// Whole 4 KB blocks of empty slots at the end are passed over at once,
	// so that a file holding few links is not read slot by slot. The slots
	// fill 256 such blocks exactly.
	var zeros [4096]byte
	for len(b) > 0 && bytes.Equal(b[len(b)-len(zeros):], zeros[:]) {
		b = b[:len(b)-len(zeros)]
	}
	var empty [slotSize]byte
	for i := len(b)/slotSize - 1; i >= 0; i-- {
		if !bytes.Equal(b[i*slotSize:(i+1)*slotSize], empty[:]) {
			return i, nil
		}
	}
	return -1, nil
}

// commit returns once every write made before it was called is on disk. It
// counts only on a sync that started after those writes: a write that
// returns while the sync runs is left to the next commit, and a commit with
// nothing new to cover makes no sync. Its one caller is the store's syncing
// goroutine, which commits one file at a time, so synced needs no lock.
// Writes go on meanwhile, under the store's write lock; written tells them
// apart. Once a sync has failed, commit returns that error, also for the
// writes that landed while the failing sync ran.
func (c *chunkFile) commit() error {
	if err := c.failed(); err != nil {
		return err
	}
	covered := c.written.Load()
	if c.synced >= covered {
		return nil
	}
	if err := c.sync(); err != nil {
		c.mu.Lock()
		c.failure = err
		c.mu.Unlock()
		return err
	}
	c.synced = covered
	return nil
}

// failed returns the error of the sync that failed the file, or nil. A
// failed file still reads; to write to it again it must be opened again.
func (c *chunkFile) failed() error {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.failure
}

// close closes the file. Writes not yet committed may be lost.
func (c *chunkFile) close() error {
	return c.f.Close()
}

// The link store: every domain keeps its links in chunk files in the data
// folder, one series of files per code length. A code decodes to its link
// number and the number names the file and the slot, so a lookup reads one
// slot and one record and needs no index.
package main

import (
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"maps"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

// keySize is the size of the data folder's key, the secret every code
// depends on.
const keySize = 32

// maxOpenChunks is how many chunk files the store keeps open at most. It is
// a variable so that tests can lower it.
var maxOpenChunks = 512

var (
	// ErrNoCode means no free code was found at any allowed length, or that
	// a link met maxRejectedCodes codes in a row that acceptable rejected.
	ErrNoCode = errors.New("could not find a free code")
	// ErrNotFound means the owner has no link with that code.
	ErrNotFound = errors.New("link not found")
	// ErrSecured means the link is secured or asks for a password, and so
	// cannot be edited.
	ErrSecured = errors.New("a secured link cannot be edited")
)

// errMissing means a chunk file does not exist, or is too short to hold its
// slots, which comes to the same.
var errMissing = errors.New("missing or cut short")

// Link is a stored link as the store reads it back.
type Link struct {
	Code, URL string
	ExpiresAt time.Time
	Owner     uint32 // the account that created it
	Secured   bool   // it expires AfterOpen after its first open
	Password  bool   // it asks for a password; Salt and Hash check it
	Opened    bool   // a secured link that has been opened
	AfterOpen time.Duration
	Salt      []byte
	Hash      []byte
}

// NewLink is a link to create. With AfterOpen set it is secured; with Salt
// and Hash set it asks for a password.
type NewLink struct {
	URL        string
	ExpiresAt  time.Time
	AfterOpen  *time.Duration
	Salt, Hash []byte
}

// LinkState is what a lookup found.
type LinkState int

const (
	LinkUnknown LinkState = iota // never handed out: a guess
	LinkGone                     // handed out, no longer readable: deleted, file removed, damaged
	LinkFound
)

// Store keeps the links of every domain in one data folder. Its methods are
// safe for concurrent use. Once Close or abandon has shut it, a call that
// would write, open a chunk file or save state.json returns errClosed. The
// locks are taken in this order: mu, then openMu or syncer.mu, never both of
// those.
type Store struct {
	dir string
	// lock holds the lock on dir/LOCK; it is nil once the store is shut.
	// shut sets it under mu and openMu, so either guards a read of it.
	lock *os.File
	key  []byte

	// syncEvery is sync_every: 0 answers a change after its sync, an
	// interval answers it at once and syncs within the interval. It is set,
	// as fatal is, before the store is first used.
	syncEvery time.Duration

	// mu is the write lock. Every write of a slot or a record is made while
	// it is held, and so is the start of a domain or a chunk. Lookup takes
	// it only to read a link again before calling it damaged.
	mu sync.Mutex
	// failure is the error of the first failed sync. Once it is set the
	// store takes no more writes. The syncing goroutine sets it.
	failure atomic.Pointer[error]
	// fatal is called once, with failure: it ends qisqa, which starts
	// again from what is on disk.
	fatal func(error)
	// syncer is what the syncing goroutine shares with the writes (see
	// store_sync.go).
	syncer syncer
	// skipped is the folder's skip, from the file skipped (see Skip).
	// Guarded by mu.
	skipped uint64

	// domains holds the state of every domain read so far. The map is
	// replaced, never changed, and only while mu is held, so that a lookup
	// reads it without a lock.
	domains atomic.Pointer[map[uint16]*domainLinks]

	// openMu guards files, closed and gone. A lookup of a file that is open
	// takes it shared, so that such lookups do not wait for each other. It
	// is held exclusively to change the maps, which closes files and, in
	// ForgetRemovedFiles, looks at them, and to open a file: a kept handle's
	// end and hosts must be current, so a chunk has one handle, opened and
	// kept in one step.
	openMu sync.RWMutex
	files  map[chunkID]*openFile
	uses   atomic.Uint64 // counts acquires, to tell the least recently used file
	// closed holds every chunk file that keep closed to stay within
	// maxOpenChunks and that has not been opened since, so that
	// ForgetRemovedFiles can notice one removed.
	closed map[chunkID]bool
	// gone holds the error of every chunk file found missing, cut short or
	// damaged, by an open or by ForgetRemovedFiles, so that it is not opened
	// again: a damaged file is read and logged once rather than at every
	// lookup, and a lookup in a missing one takes no exclusive lock. A
	// restart reads them again. It also holds, until startChunk takes it
	// out, a chunk not started yet that a skip ended inside (see
	// startDomain). A chunk is never in files and gone at once.
	gone map[chunkID]error

	syncCount atomic.Int64 // syncs of chunk files that ran

	// Test hooks. When set, every sync or every write of a chunk file
	// returns failSync or failWrite, onWrite sees every write to a chunk
	// file just before it is made, onSync every sync, failing or not,
	// onNotOpen every acquire that finds its file not open, before it takes
	// the lock to open it, onOpened every chunk file just after it is
	// opened, before it is kept, and onWait every call that begins to wait
	// for a pass. after is time.After, which the syncing goroutine waits on
	// for the interval to end.
	failSync, failWrite error
	onWrite             func(path string, offset int64, b []byte)
	onSync              func(path string)
	onNotOpen           func(path string)
	onOpened            func(c *chunkFile)
	onWait              func()
	after               func(time.Duration) <-chan time.Time
	// noSync, set on a store a test opens only to read it back, skips every
	// sync it would make: of chunk files, of state.json and of folders.
	noSync bool
}

// domainLinks is what the store knows about the links of one domain.
type domainLinks struct {
	coder   *Coder
	lengths [maxCodeLength + 1]lengthLinks // by code length; those below minCodeLength are unused
}

// lengthLinks is the numbering of one code length on a domain.
type lengthLinks struct {
	// next is the first number not yet taken. Create changes it under the
	// write lock once a number is used up; Lookup reads it without.
	next atomic.Uint64
	// chunk is the newest chunk started, the one state.json names, or -1
	// when the length was never used. It is guarded by the write lock.
	chunk int64
	// skipped is how much of the folder's skip the numbering has been moved
	// by. It is guarded by the write lock.
	skipped uint64
	// end is where the links of chunk end: after the last link written into
	// it, or at its first number while it has none; next may be past it.
	// savedEnd is the end state.json records, one known to be on disk: the
	// end found when the domain was read, a new chunk's first number, and
	// end at Close, once everything is synced. reserved, with sync_every
	// set, is the number state.json holds as next while the store runs (see
	// reserve). All three are guarded by the write lock.
	end, savedEnd, reserved uint64
}

// chunkID names a chunk file.
type chunkID struct {
	domain uint16
	length int
	index  uint64
}

// openFile is a chunk file the store holds open.
type openFile struct {
	*chunkFile
	id      chunkID
	users   atomic.Int64  // calls using it now; it is not closed while there are any
	lastUse atomic.Uint64 // the acquire that last used it, or one made at the same time
	// written says that this handle has written the file's head, by
	// creating the file or by rewriteHead, so links may be written through
	// it. Guarded by the write lock.
	written bool
	// queued says that the file is among those the next pass syncs.
	// Guarded by syncer.mu.
	queued bool
}

// savedLength is what a domain's state.json holds for one code length.
type savedLength struct {
	Chunk   uint64 `json:"chunk"`   // the newest chunk started
	Next    uint64 `json:"next"`    // no number below it is handed out again
	End     uint64 `json:"end"`     // where the links of Chunk ended when this was written
	Skipped uint64 `json:"skipped"` // how much of the folder's skip Next has been moved by
}

// OpenStore opens the data folder dir, creating it if needed, locks it
// against a second qisqa and starts the store's syncing goroutine, which
// Close and abandon end. Each domain is read on its first use.
func OpenStore(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	lock, err := lockFolder(dir)
	if err != nil {
		return nil, err
	}
	key, err := loadKey(dir)
	if err != nil {
		lock.Close()
		return nil, err
	}
	skipped, err := loadSkipped(dir)
	if err != nil {
		lock.Close()
		return nil, err
	}
	s := &Store{
		dir: dir, lock: lock, key: key, skipped: skipped, fatal: func(error) { os.Exit(1) },
		files: make(map[chunkID]*openFile), closed: make(map[chunkID]bool), gone: make(map[chunkID]error),
	}
	s.domains.Store(&map[uint16]*domainLinks{})
	s.startSyncing()
	return s, nil
}

// lockFolder takes the lock that keeps a second qisqa off the folder. The
// system lets go of it when the process ends, however it ends.
func lockFolder(dir string) (*os.File, error) {
	f, err := os.OpenFile(filepath.Join(dir, "LOCK"), os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, fmt.Errorf("data folder %s is in use by another qisqa process", dir)
		}
		return nil, fmt.Errorf("lock data folder %s: %w", dir, err)
	}
	return f, nil
}

// loadKey reads the folder's key. A missing key is created only while the
// folder holds no links: a new key would make every old code lead nowhere,
// and putting the old key back later would do the same to the new codes.
func loadKey(dir string) ([]byte, error) {
	path := filepath.Join(dir, "key")
	key, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		links, err := holdsLinks(dir)
		if err != nil {
			return nil, err
		}
		if links {
			return nil, fmt.Errorf("%s is missing but the folder holds links: put the key file back, or move the d* folders away to start empty", path)
		}
		key = make([]byte, keySize)
		rand.Read(key)
		return key, writeAtomic(path, key, (*os.File).Sync)
	}
	if err != nil {
		return nil, err
	}
	if len(key) != keySize {
		return nil, fmt.Errorf("%s holds %d bytes; a key is %d bytes", path, len(key), keySize)
	}
	return key, nil
}

// holdsLinks reports whether dir holds a domain's link folder, d<number>.
// A file that every code depends on may not start afresh beside one.
func holdsLinks(dir string) (bool, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false, err
	}
	for _, e := range entries {
		if digits, ok := strings.CutPrefix(e.Name(), "d"); ok {
			if _, err := strconv.ParseUint(digits, 10, 64); err == nil {
				return true, nil
			}
		}
	}
	return false, nil
}

// Close syncs every write not yet synced, then writes the exact numbering of
// every domain to its state.json and closes every file. It is safe to call
// more than once.
func (s *Store) Close() error {
	return s.shut(true)
}

// abandon drops every file handle and the lock without a sync and without
// writing state.json, as a crash of qisqa would. For tests.
func (s *Store) abandon() {
	s.shut(false)
}

// shut closes the store. With save the syncing goroutine syncs everything
// before it ends, and every domain's state.json is written then.
func (s *Store) shut(save bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.lock == nil {
		return nil
	}
	// The goroutine never takes the write lock, and no write is made while
	// it is held here.
	s.syncer.stop <- save
	<-s.syncer.ended
	var errs []error
	if save {
		errs = append(errs, s.syncer.final)
		// Everything is synced: state.json gets the exact next number, so
		// that none of those reserved is wasted, and where the links end.
		for id, d := range *s.domains.Load() {
			for i := range d.lengths {
				ls := &d.lengths[i]
				ls.reserved, ls.savedEnd = 0, ls.end
			}
			errs = append(errs, s.saveState(id, d))
		}
	}
	// From here no chunk file is opened again: acquire reads lock under
	// openMu.
	s.openMu.Lock()
	for id, f := range s.files {
		errs = append(errs, f.close())
		delete(s.files, id)
	}
	errs = append(errs, s.lock.Close())
	s.lock = nil
	s.openMu.Unlock()
	return errors.Join(errs...)
}

// Skip adds n to the folder's skip and returns the new total. The skip is
// how far every numbering of the folder has been moved forward, to pass over
// the numbers, and so the codes, that links made after a copy of the folder
// was taken have had: each code length of each domain, used or not, is moved
// forward by what it has not been moved by yet when its domain is read (see
// startDomain). The total is written to the file skipped in the folder; no
// state.json changes. A total that would not fit in 64 bits is refused, and
// nothing is written.
func (s *Store) Skip(n uint64) (total uint64, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	path := filepath.Join(s.dir, "skipped")
	if n > math.MaxUint64-s.skipped {
		return 0, fmt.Errorf("%s holds %d already: %d more is past the largest number qisqa counts to", path, s.skipped, n)
	}
	total = s.skipped + n
	if err := writeAtomic(path, []byte(strconv.FormatUint(total, 10)+"\n"), s.fsync); err != nil {
		return 0, err
	}
	s.skipped = total
	return total, nil
}

// loadSkipped reads the folder's skip from the file skipped: one decimal
// number, and 0 when the file is missing.
func loadSkipped(dir string) (uint64, error) {
	path := filepath.Join(dir, "skipped")
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	n, err := strconv.ParseUint(strings.TrimSpace(string(b)), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%s holds %.20q, not one decimal number: write into it the total that qisqa -skip printed last", path, b)
	}
	return n, nil
}

// syncs returns how many syncs of chunk files have run. For tests.
func (s *Store) syncs() int64 {
	return s.syncCount.Load()
}

// Create stores links on a domain for owner and returns them, in order, with
// their codes. A code is minLength characters long, and at least 3; when
// that length is full the next one is used. A number whose code acceptable
// rejects is skipped. acceptable runs under the write lock, so it must be
// quick and must not call the store. Without sync_every the links are on
// disk when Create returns: the one pass that follows syncs each file the
// batch went into, for every create that waits. With it Create returns at
// once. After a failed sync Create returns that sync's error without
// writing.
func (s *Store) Create(domain uint16, owner uint32, minLength int, acceptable func(code string) bool, links []NewLink) ([]Link, error) {
	if owner >= 1<<24 {
		return nil, fmt.Errorf("owner %d does not fit in the 24 bits a slot holds", owner)
	}
	// The files written to are let go however Create ends, a panic in
	// acceptable included; the syncing goroutine holds them until synced.
	var files []*openFile
	defer func() {
		for _, f := range files {
			s.release(f)
		}
	}()
	created, p, err := s.write(domain, owner, minLength, acceptable, links, &files)
	if err != nil {
		return nil, err
	}
	// Waited for after the lock is let go, so that concurrent creates write
	// while a pass runs and then share the next one.
	if err := s.await(p); err != nil {
		return nil, err
	}
	return created, nil
}

// maxRejectedCodes is how many codes in a row acceptable may reject before
// a link gives up with ErrNoCode, rather than running through every number
// of its length under the write lock.
const maxRejectedCodes = 1000

// write does the work of Create under the write lock, which it lets go
// however it ends. Every file it writes to is added to files, acquired, for
// the caller to release, and given to the syncing goroutine, even when write
// fails or panics: a file with writes not yet synced stays open until they
// are. p is the pass that covers the writes.
func (s *Store) write(domain uint16, owner uint32, minLength int, acceptable func(string) bool, links []NewLink, files *[]*openFile) (created []Link, p *pass, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.lock == nil {
		return nil, nil, errClosed
	}
	if err := s.failed(); err != nil {
		return nil, nil, err
	}
	defer func() { p = s.toSync(*files...) }()
	type prepared struct {
		s slot
		r record
	}
	batch := make([]prepared, len(links))
	for i, l := range links {
		var err error
		if batch[i].s, batch[i].r, err = prepare(owner, l); err != nil {
			return nil, nil, err
		}
	}
	d, err := s.startDomain(domain)
	if err != nil {
		return nil, nil, err
	}

	var f *openFile // the file the last number went into
	created = make([]Link, 0, len(links))
	length := max(minLength, minCodeLength)
	for _, prep := range batch {
		for rejected := 0; ; {
			for length <= maxCodeLength && d.lengths[length].next.Load() >= classCapacity(length) {
				length++
			}
			if length > maxCodeLength {
				return nil, nil, ErrNoCode
			}
			ls := &d.lengths[length]
			m := ls.next.Load()
			// Within one call lengths and chunks only grow, so a file once
			// left is not met again.
			if id := (chunkID{domain, length, m / chunkLinks}); f == nil || f.id != id {
				if f, err = s.chunkFor(d, id); err != nil {
					return nil, nil, err
				}
				if f == nil {
					continue // the chunk was lost; numbering moved past it
				}
				*files = append(*files, f)
			}
			if err := s.reserve(domain, d, length, m); err != nil {
				return nil, nil, err
			}
			code := d.coder.Code(length, m)
			if !acceptable(code) {
				// The numbers tried stay used: their codes were seen.
				ls.next.Store(m + 1)
				if rejected++; rejected == maxRejectedCodes {
					return nil, nil, ErrNoCode
				}
				continue
			}
			sl := prep.s
			sl.offset, sl.length, err = f.appendRecord(recordCheck(length, m, sl, prep.r), sl.flags, prep.r)
			if err == nil {
				err = f.writeSlot(int(m%chunkLinks), sl)
			}
			// Used up even when a write failed: part of the link may be on
			// disk, and a number is never handed out twice.
			ls.next.Store(m + 1)
			if err != nil {
				return nil, nil, err
			}
			ls.end = m + 1
			created = append(created, toLink(code, sl, prep.r))
			break
		}
	}
	return created, nil, nil
}

// prepare turns a new link into the slot and the record that store it. The
// slot learns where the record is once the record is written. A link that
// appendRecord would refuse is refused here, before its batch takes a
// number or writes a link.
func prepare(owner uint32, l NewLink) (slot, record, error) {
	https, host, rest, ok := splitURL(l.URL)
	if !ok {
		return slot{}, record{}, fmt.Errorf("%q is not an http or https URL", l.URL)
	}
	s := slot{expires: expiryToSeconds(l.ExpiresAt), owner: owner, flags: flagUsed}
	r := record{salt: l.Salt, hash: l.Hash, host: host, rest: rest}
	if https {
		s.flags |= flagHTTPS
	}
	if l.AfterOpen != nil {
		s.flags |= flagSecured
		r.afterOpen = uint32(min(max(math.Ceil(l.AfterOpen.Seconds()), 0), math.MaxUint32))
	}
	if len(l.Salt) > 0 || len(l.Hash) > 0 {
		if len(l.Salt) != saltSize || len(l.Hash) != hashSize {
			return slot{}, record{}, fmt.Errorf("a password link needs a %d-byte salt and a %d-byte hash", saltSize, hashSize)
		}
		s.flags |= flagPassword
	}
	if size := largestRecord(s.flags, r); size > math.MaxUint16 {
		return slot{}, record{}, fmt.Errorf("the link to %.100q needs a record of %d bytes; a record holds at most %d", l.URL, size, math.MaxUint16)
	}
	return s, r, nil
}

// largestRecord is the size of r on disk with its host written into it
// rather than into the host table, the most it can take (see appendRecord).
func largestRecord(flags uint8, r record) int {
	size := 4 + len(r.salt) + len(r.hash) + 1 + len(binary.AppendUvarint(nil, uint64(len(r.host)))) + len(r.host) + len(r.rest)
	if flags&flagSecured != 0 {
		size += 4
	}
	return size
}

// toLink puts a link together from its slot and its record.
func toLink(code string, s slot, r record) Link {
	return Link{
		Code:      code,
		URL:       joinURL(s.flags&flagHTTPS != 0, r.host, r.rest),
		ExpiresAt: secondsToExpiry(s.expires),
		Owner:     s.owner,
		Secured:   s.flags&flagSecured != 0,
		Password:  s.flags&flagPassword != 0,
		Opened:    s.flags&flagOpened != 0,
		AfterOpen: time.Duration(r.afterOpen) * time.Second,
		Salt:      r.salt,
		Hash:      r.hash,
	}
}

// chunkFor returns, acquired, the file of chunk id, where the next number of
// its length goes, starting the chunk if it is new. A file the store goes on
// with has its head written again before anything else. A file that went
// missing or damaged since is handled as at startup: numbering moves on to
// the following chunk and chunkFor returns nil. An error of the system
// changes nothing. The caller holds the write lock.
func (s *Store) chunkFor(d *domainLinks, id chunkID) (*openFile, error) {
	ls := &d.lengths[id.length]
	if int64(id.index) != ls.chunk {
		if err := s.startChunk(d, id); err != nil {
			return nil, err
		}
	}
	f, err := s.acquire(id)
	if lost(err) {
		s.moveOn(ls, id, err)
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if err := f.beginWrites(); err != nil {
		s.release(f)
		return nil, err
	}
	return f, nil
}

// beginWrites readies f to be written to: a file this store did not create
// has its head written again first (see rewriteHead). The caller holds the
// write lock.
func (f *openFile) beginWrites() error {
	if f.written {
		return nil
	}
	if err := f.rewriteHead(); err != nil {
		return err
	}
	f.written = true
	return nil
}

// moveOn logs that chunk id, where new links of its length go, is lost, and
// moves numbering on to the following chunk. Numbering that is past it
// already, after a skip, stays where it is: it never goes back. The caller
// holds the write lock.
func (s *Store) moveOn(ls *lengthLinks, id chunkID, err error) {
	s.lostChunk(id, err)
	ls.next.Store(max(ls.next.Load(), min((id.index+1)*chunkLinks, classCapacity(id.length))))
}

// startChunk starts chunk id. state.json names it first, so that its numbers
// are never handed out again even if the file is lost; then the file is
// created, its head synced, and its folder synced. A failure of any of these
// is an error of the call that starts the chunk, not the store's failure:
// the file is removed and the state put back, and the next call starts the
// chunk again. The caller holds the write lock.
func (s *Store) startChunk(d *domainLinks, id chunkID) error {
	dir := s.domainDir(id.domain)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if err := syncDir(s.dir, s.fsync); err != nil {
		return err
	}
	ls := &d.lengths[id.length]
	chunk, end, savedEnd := ls.chunk, ls.end, ls.savedEnd
	restore := func() { ls.chunk, ls.end, ls.savedEnd = chunk, end, savedEnd }
	// A chunk has no links yet: they end at its first number, also when a
	// skip put next further in.
	ls.chunk, ls.end, ls.savedEnd = int64(id.index), id.index*chunkLinks, id.index*chunkLinks
	if err := s.saveState(id.domain, d); err != nil {
		restore()
		return err
	}
	path := s.chunkPath(id)
	c, err := createChunk(path, id.length, id.domain, id.index, time.Now(), s.hook)
	if err == nil {
		if err = syncDir(dir, s.fsync); err != nil {
			c.close()
			os.Remove(path)
		}
	}
	if err != nil {
		restore()
		return err
	}
	// In one step, so that a lookup finds the chunk gone or its file kept:
	// gone may hold it since a skip ended inside it (see startDomain).
	s.openMu.Lock()
	delete(s.gone, id)
	s.keep(id, c).written = true
	s.openMu.Unlock()
	return nil
}

// Lookup returns the link a code stands for on a domain, expired or not.
// LinkUnknown means no link ever had the code; LinkGone means one had but
// it can no longer be read. Damage is logged, not returned; an error of the
// system is returned, and so is errClosed once the store is shut.
func (s *Store) Lookup(domain uint16, code string) (Link, LinkState, error) {
	d, err := s.domain(domain)
	if err != nil {
		return Link{}, LinkUnknown, err
	}
	length, m, ok := d.coder.Number(code)
	if !ok || m >= d.lengths[length].next.Load() {
		return Link{}, LinkUnknown, nil
	}
	id := chunkID{domain, length, m / chunkLinks}
	l, state, damage, err := s.read(id, m, code)
	if damage != nil {
		// The read may have met a write half-way. Once the write lock is
		// held no write is under way: what is still wrong then is damage.
		l, state, damage, err = s.readAgain(id, m, code)
		if damage != nil {
			slog.Error("a link is damaged and leads nowhere", "domain", domain, "code", code, "error", damage)
		}
	}
	if err != nil {
		return Link{}, LinkUnknown, err
	}
	return l, state, nil
}

// readAgain reads link number m under the write lock, once no write is
// under way.
func (s *Store) readAgain(id chunkID, m uint64, code string) (l Link, state LinkState, damage, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.read(id, m, code)
}

// read reads link number m from chunk id. damage says why a link that was
// handed out cannot be read; a missing file, a damaged one (logged when it
// is found) and a deleted link are not reported as damage here. err is an
// error of the system, which says nothing about the link.
func (s *Store) read(id chunkID, m uint64, code string) (l Link, state LinkState, damage, err error) {
	f, err := s.acquire(id)
	if lost(err) {
		return Link{}, LinkGone, nil, nil
	}
	if err != nil {
		return Link{}, LinkUnknown, nil, err
	}
	defer s.release(f)
	sl, r, state, damage := f.readLink(m)
	if state != LinkFound {
		return Link{}, state, damage, nil
	}
	return toLink(code, sl, r), LinkFound, nil, nil
}

// readLink reads the slot and the record of link number m, which is in f.
// damage says why a link that was handed out cannot be read; a deleted link
// is gone without damage.
func (f *openFile) readLink(m uint64) (sl slot, r record, state LinkState, damage error) {
	i := int(m % chunkLinks)
	sl, slotState, err := f.readSlot(i)
	switch {
	case err != nil:
		return slot{}, record{}, LinkGone, err
	case slotState == slotEmpty:
		return slot{}, record{}, LinkUnknown, nil
	case slotState == slotDamaged:
		return slot{}, record{}, LinkGone, fmt.Errorf("slot %d of %s is damaged", i, f.path)
	case sl.flags&flagDeleted != 0:
		return slot{}, record{}, LinkGone, nil
	}
	check, r, err := f.readRecord(sl)
	if err == nil && check != recordCheck(f.id.length, m, sl, r) {
		err = fmt.Errorf("the record of slot %d of %s fails its check", i, f.path)
	}
	if err != nil {
		return slot{}, record{}, LinkGone, err
	}
	return sl, r, LinkFound, nil
}

// domain returns the state of a domain, reading it on first use.
func (s *Store) domain(id uint16) (*domainLinks, error) {
	if d := (*s.domains.Load())[id]; d != nil {
		return d, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.startDomain(id)
}

// startDomain returns the state of a domain, reading it from its folder on
// first use. For every code length the current chunk is the newer of the
// one state.json names and the newest file; numbering goes on after its
// last used slot, never below the next number state.json recorded. It is
// then moved forward by the part of the folder's skip it has not been moved
// by, at most to the length's capacity: by all of it for a length with no
// state, used or not, and again after a crash before state.json recorded
// the move. The numbers passed over are never handed out, and no file is
// made for a chunk passed over whole. The caller holds the write lock.
func (s *Store) startDomain(id uint16) (*domainLinks, error) {
	if d := (*s.domains.Load())[id]; d != nil {
		return d, nil
	}

	dir := s.domainDir(id)
	statePath := filepath.Join(dir, "state.json")
	saved := map[int]savedLength{}
	b, err := os.ReadFile(statePath)
	switch {
	case err == nil:
		// Damage costs what a missing file would: the floor it kept.
		if err := json.Unmarshal(b, &saved); err != nil {
			slog.Error("state.json is damaged; the domain goes on from its chunk files alone", "file", statePath, "error", err)
			saved = map[int]savedLength{}
		}
	case !errors.Is(err, fs.ErrNotExist):
		return nil, err
	}
	newest := map[int]uint64{}
	entries, err := os.ReadDir(dir)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	for _, e := range entries {
		if length, index, ok := parseChunkName(e.Name()); ok {
			newest[length] = max(newest[length], index)
		}
	}

	d := &domainLinks{coder: NewCoder(s.key, id)}
	var unstarted []chunkID
	for length := minCodeLength; length <= maxCodeLength; length++ {
		ls := &d.lengths[length]
		ls.chunk = -1
		capacity := classCapacity(length)
		var next uint64 // a length never used starts at 0
		st, inState := saved[length]
		index, inFiles := newest[length]
		if inState || inFiles {
			chunk := chunkID{id, length, min(max(index, st.Chunk), capacity/chunkLinks)}
			found, ok, err := s.scanChunk(chunk)
			if err != nil {
				return nil, err
			}
			// Compared with where the links ended, not with next: a skip moves
			// next past them, and that is no damage.
			if ok && found < st.End {
				slog.Error("a chunk file holds fewer links than state.json recorded; it was damaged, and numbering goes on after the recorded links",
					"file", s.chunkPath(chunk), "found", found, "recorded", st.End)
			}
			next = max(found, st.Next)
			ls.chunk = int64(chunk.index)
			ls.end = chunk.index * chunkLinks // a lost file holds no links
			if ok {
				ls.end = found
			}
			ls.savedEnd = ls.end
		}
		// What the numbering has not been moved by yet of the folder's skip.
		// A length with no state has been moved by none of it.
		next = min(next, capacity)
		if s.skipped > st.Skipped {
			next += min(s.skipped-st.Skipped, capacity-next)
		}
		ls.skipped = s.skipped
		ls.next.Store(next)
		// When a skip ended inside a chunk not started yet, numbers of it are
		// below next. A lookup of one finds the chunk in gone until
		// startChunk has made its file and kept it, and so never opens a
		// file still being made.
		if k := next / chunkLinks; next%chunkLinks != 0 && int64(k) > ls.chunk {
			unstarted = append(unstarted, chunkID{id, length, k})
		}
	}
	if len(unstarted) > 0 {
		s.openMu.Lock()
		for _, c := range unstarted {
			s.gone[c] = errMissing
		}
		s.openMu.Unlock()
	}
	domains := maps.Clone(*s.domains.Load())
	domains[id] = d
	s.domains.Store(&domains)
	return d, nil
}

// scanChunk returns the number after the last used slot of chunk id. When
// the file is missing, cut short or damaged, that is logged and the number
// is the first of the following chunk, so that none of its numbers is
// handed out again; ok is false then. An error of the system is returned:
// a passing one must not use up a chunk's numbers.
func (s *Store) scanChunk(id chunkID) (next uint64, ok bool, err error) {
	f, err := s.acquire(id)
	if lost(err) {
		s.lostChunk(id, err)
		return (id.index + 1) * chunkLinks, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	defer s.release(f)
	last, err := f.lastUsedSlot()
	if err != nil {
		return 0, false, err
	}
	return id.index*chunkLinks + uint64(last+1), true, nil
}

// lost reports whether an error of opening a chunk file means the file is
// gone for good: missing, cut short or damaged. Any other error is the
// system's and may pass.
func lost(err error) bool {
	return errors.Is(err, errMissing) || errors.Is(err, errNotChunk)
}

// lostChunk logs that the chunk new links of a length go into is lost.
func (s *Store) lostChunk(id chunkID, err error) {
	slog.Error("the newest chunk file of a code length is lost; new links of that length go into the following chunk",
		"file", s.chunkPath(id), "error", err)
}

// saveState writes the domain's state.json: for every code length used, the
// newest chunk started, the next number, or the one reserved past it, where
// the chunk's links end as far as that is known to be on disk, and how much
// of the folder's skip the numbering has been moved by. The caller holds
// the write lock.
func (s *Store) saveState(domain uint16, d *domainLinks) error {
	state := map[int]savedLength{}
	for length := minCodeLength; length <= maxCodeLength; length++ {
		if ls := &d.lengths[length]; ls.chunk >= 0 {
			next := max(ls.next.Load(), ls.reserved)
			state[length] = savedLength{Chunk: uint64(ls.chunk), Next: next, End: ls.savedEnd, Skipped: ls.skipped}
		}
	}
	if len(state) == 0 {
		return nil
	}
	b, err := json.Marshal(state)
	if err != nil {
		return err
	}
	return writeAtomic(filepath.Join(s.domainDir(domain), "state.json"), b, s.fsync)
}

// reserve makes state.json hold a number past m, with sync_every set,
// before number m of a length is handed out. A link answered and not yet
// synced is lost with the machine, and its number must not be handed out
// again after that: numbering goes on from the number saved. That number is
// reserveAhead past m, at most the length's capacity, so that state.json is
// written, and synced, once for so many numbers, by the create that needs
// it; every other create compares one number. The caller holds the write
// lock, and length has a chunk started.
func (s *Store) reserve(domain uint16, d *domainLinks, length int, m uint64) error {
	ls := &d.lengths[length]
	if s.syncEvery == 0 || m < ls.reserved {
		return nil
	}
	before := ls.reserved
	ls.reserved = min(m+reserveAhead, classCapacity(length))
	if err := s.saveState(domain, d); err != nil {
		ls.reserved = before
		return err
	}
	return nil
}

// acquire returns the open file of chunk id, opening it if needed. The
// caller releases it when done; until then it stays open. A file that is
// open already is found under the shared lock. One that is not is opened
// and kept in one step under the exclusive lock: a kept handle's end and
// hosts must be current, so a chunk has one handle, and none is opened
// beside it. A file found missing, cut short or damaged is remembered in
// gone, so that the next calls for it stop at the shared lock. No call
// reaches the open of a chunk whose file is still to be made. Only a number
// below next leads here: one of a chunk started, whose file startChunk made
// and kept before handing out any of its numbers; or one a skip passed
// over, in a chunk that is never started, or in the one next lies in, which
// stays in gone until startChunk has made its file and kept it.
func (s *Store) acquire(id chunkID) (*openFile, error) {
	s.openMu.RLock()
	f := s.files[id]
	if f != nil {
		s.use(f)
	}
	gone := s.gone[id]
	s.openMu.RUnlock()
	switch {
	case f != nil:
		return f, nil
	case gone != nil:
		return nil, gone
	}
	if s.onNotOpen != nil {
		s.onNotOpen(s.chunkPath(id))
	}

	s.openMu.Lock()
	defer s.openMu.Unlock()
	if s.lock == nil { // shut: it closed every file, and none is opened again
		return nil, errClosed
	}
	// Another call may have kept the file, or found it damaged or removed,
	// meanwhile.
	if f = s.files[id]; f == nil {
		if err := s.gone[id]; err != nil {
			return nil, err
		}
		c, err := s.open(id)
		if lost(err) {
			s.gone[id] = err
		}
		if errors.Is(err, errNotChunk) {
			slog.Error("a chunk file is damaged and its links lead nowhere", "file", s.chunkPath(id), "error", err)
		}
		if err != nil {
			return nil, err
		}
		f = s.keep(id, c)
	}
	s.use(f)
	return f, nil
}

// use counts a use of f, which is not closed until it is released. The
// caller holds openMu, shared or not.
func (s *Store) use(f *openFile) {
	f.users.Add(1)
	f.lastUse.Store(s.uses.Add(1))
}

// release ends a use of a file that acquire returned.
func (s *Store) release(f *openFile) {
	f.users.Add(-1)
}

// keep adds a file the store opened to the open files. While too many are
// open it first closes the least recently used one that nothing uses, and
// counts it among the closed: a use starts under openMu, so none starts
// while keep runs. The caller holds openMu exclusively.
func (s *Store) keep(id chunkID, c *chunkFile) *openFile {
	for len(s.files) >= maxOpenChunks {
		var oldest *openFile
		for _, f := range s.files {
			if f.users.Load() == 0 && (oldest == nil || f.lastUse.Load() < oldest.lastUse.Load()) {
				oldest = f
			}
		}
		if oldest == nil {
			break
		}
		oldest.close()
		delete(s.files, oldest.id)
		s.closed[oldest.id] = true
	}
	f := &openFile{chunkFile: c, id: id}
	s.files[id] = f
	delete(s.closed, id)
	return f
}

// open opens the file of chunk id with the store's hooks. A file shorter
// than its slots is missing, as one that does not exist: openChunk does not
// check the size. A file whose header names another chunk is not this
// chunk's file: errNotChunk, as for any other damaged header.
func (s *Store) open(id chunkID) (*chunkFile, error) {
	c, err := openChunk(s.chunkPath(id))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, errMissing
	}
	if err != nil {
		return nil, err
	}
	info, err := c.f.Stat()
	switch {
	case err != nil:
	case info.Size() < recordsOffset:
		err = errMissing
	case c.length != id.length || c.domain != id.domain || c.index != id.index:
		err = fmt.Errorf("%s is %w for length %d, domain %d, chunk %d: its header names length %d, domain %d, chunk %d",
			c.path, errNotChunk, id.length, id.domain, id.index, c.length, c.domain, c.index)
	}
	if err != nil {
		c.close()
		return nil, err
	}
	s.hook(c)
	if s.onOpened != nil {
		s.onOpened(c)
	}
	return c, nil
}

// fsync makes the writes to f durable, unless the noSync test hook is set.
func (s *Store) fsync(f *os.File) error {
	if s.noSync {
		return nil
	}
	return f.Sync()
}

// hook gives a chunk file the store's test hooks and counts its syncs.
func (s *Store) hook(c *chunkFile) {
	f := c.f
	c.sync = func() error {
		if s.onSync != nil {
			s.onSync(c.path)
		}
		if s.failSync != nil {
			return s.failSync
		}
		s.syncCount.Add(1)
		return s.fsync(f)
	}
	c.write = func(b []byte, offset int64) (int, error) {
		if s.failWrite != nil {
			return 0, s.failWrite
		}
		return f.WriteAt(b, offset)
	}
	c.onWrite = func(offset int64, b []byte) {
		if s.onWrite != nil {
			s.onWrite(c.path, offset, b)
		}
	}
}

// domainDir is the folder of a domain's files.
func (s *Store) domainDir(domain uint16) string {
	return filepath.Join(s.dir, "d"+strconv.Itoa(int(domain)))
}

// chunkPath is the path of a chunk file: <length>-<index as 12 digits>.chunk
// in its domain's folder.
func (s *Store) chunkPath(id chunkID) string {
	return filepath.Join(s.domainDir(id.domain), chunkName(id.length, id.index))
}

// chunkName is the name of a chunk file.
func chunkName(length int, index uint64) string {
	return fmt.Sprintf("%d-%012d.chunk", length, index)
}

// parseChunkName reads the code length and index from a chunk file's name.
func parseChunkName(name string) (length int, index uint64, ok bool) {
	before, after, _ := strings.Cut(name, "-")
	digits, _ := strings.CutSuffix(after, ".chunk")
	length, err := strconv.Atoi(before)
	if err != nil || length < minCodeLength || length > maxCodeLength {
		return 0, 0, false
	}
	index, err = strconv.ParseUint(digits, 10, 64)
	return length, index, err == nil && chunkName(length, index) == name
}

// writeAtomic replaces the file at path with b: a temporary file is written
// and synced, renamed over path, and the folder synced, so that after a
// crash path holds the old content or the new. fsync is what syncs.
func writeAtomic(path string, b []byte, fsync func(*os.File) error) error {
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	_, err = f.Write(b)
	if err == nil {
		err = fsync(f)
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(tmp, path)
	}
	if err != nil {
		os.Remove(tmp)
		return err
	}
	return syncDir(filepath.Dir(path), fsync)
}

// syncDir makes the names in a folder durable: files created, renamed or
// removed in it. fsync is what syncs.
func syncDir(dir string, fsync func(*os.File) error) error {
	f, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer f.Close()
	return fsync(f)
}

// Changing links in the store: a new URL or expiry, deleting, opening a
// secured link, and letting go of chunk files removed while qisqa runs.
// Every change is made under the write lock and refused after a failed
// sync. Without sync_every it is on disk before the call returns; with it
// the call returns at once (see store_sync.go).
package main

import (
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"syscall"
	"time"
)

// stored is a link as an edit finds it: its file, acquired, its number, its
// slot and its record.
type stored struct {
	f *openFile
	m uint64
	s slot
	r record
}

// Update changes the URL of a regular link, its expiry, or both; nil leaves
// a value as it is. The expiry is set as given, sooner or later, so that an
// expired link can come back. ErrNotFound means owner has no such link;
// ErrSecured that the link is secured or asks for a password, which cannot
// be edited.
func (s *Store) Update(domain uint16, code string, owner uint32, url *string, expiresAt *time.Time) (Link, error) {
	editable := func(l stored) error {
		switch {
		case l.s.owner != owner:
			return ErrNotFound
		case l.s.flags&(flagSecured|flagPassword) != 0:
			return ErrSecured
		}
		return nil
	}
	expiry := func(sl slot) slot {
		if expiresAt != nil {
			sl.expires = expiryToSeconds(*expiresAt)
		}
		return sl
	}
	if url == nil {
		l, sl, p, err := s.editSlot(domain, code, func(l stored) (slot, error) {
			return expiry(l.s), editable(l)
		})
		if err != nil {
			return Link{}, err
		}
		defer s.release(l.f)
		if err := s.await(p); err != nil {
			return Link{}, err
		}
		return toLink(code, sl, l.r), nil
	}

	https, host, rest, ok := splitURL(*url)
	if !ok {
		return Link{}, fmt.Errorf("%q is not an http or https URL", *url)
	}
	r := record{host: host, rest: rest}
	// pointed is the slot sl becomes, but for where the record is.
	pointed := func(sl slot) slot {
		sl = expiry(sl)
		sl.flags &^= flagHTTPS
		if https {
			sl.flags |= flagHTTPS
		}
		return sl
	}
	// Without sync_every the new record is on disk before the slot points at
	// it, and the old one is erased only once the slot on disk points
	// elsewhere: a crash at any moment leaves the old URL or the new one.
	// With it a power loss may also leave the link leading nowhere, never
	// elsewhere: the record's check covers its URL.
	f, at, p, err := s.appendFor(domain, code, editable, pointed, r)
	if err != nil {
		return Link{}, err
	}
	defer s.release(f)
	if err := s.await(p); err != nil {
		return Link{}, err
	}
	// The link is read again: it may have changed while the lock was let go.
	old, sl, p, err := s.editSlot(domain, code, func(l stored) (slot, error) {
		sl := pointed(l.s)
		sl.offset, sl.length = at.offset, at.length
		return sl, editable(l)
	})
	if errors.Is(err, ErrNotFound) {
		s.erase(f, at) // deleted meanwhile: no slot points at the new record
	}
	if err != nil {
		return Link{}, err
	}
	defer s.release(old.f)
	if err := s.await(p); err != nil {
		return Link{}, err
	}
	s.erase(old.f, old.s)
	return toLink(code, sl, r), nil
}

// Delete deletes owner's link, secured or not: from then on it leads
// nowhere and its URL is erased from the file. Its code is never handed out
// again. ErrNotFound means owner has no such link.
func (s *Store) Delete(domain uint16, code string, owner uint32) error {
	l, _, p, err := s.editSlot(domain, code, func(l stored) (slot, error) {
		if l.s.owner != owner {
			return slot{}, ErrNotFound
		}
		sl := l.s
		sl.flags |= flagDeleted
		return sl, nil
	})
	if err != nil {
		return err
	}
	defer s.release(l.f)
	// Without sync_every the URL is erased only once the slot on disk says
	// deleted: a crash never leaves a live slot without its record.
	if err := s.await(p); err != nil {
		return err
	}
	s.erase(l.f, l.s)
	return nil
}

// Open opens a secured link at now and returns its URL. The first open
// shortens the link's life to afterOpen from now, if it was to live longer;
// a one-time link (afterOpen 0) expires at once. ok is false when the link
// is not secured, not found, deleted or expired at now. Without sync_every
// the open is on disk before Open returns, so that no crash lets a one-time
// link open twice; with it Open, on the way of a redirect, does not wait for
// the disk, and a power loss may let the link open once more.
func (s *Store) Open(domain uint16, code string, now time.Time) (url string, ok bool, err error) {
	l, sl, p, err := s.editSlot(domain, code, func(l stored) (slot, error) {
		sl, opened := l.s, l.s.flags&flagOpened != 0
		switch {
		case sl.flags&flagSecured == 0 || !secondsToExpiry(sl.expires).After(now):
			return slot{}, ErrNotFound
		case opened && l.r.afterOpen == 0:
			// Once, even for a request whose clock reads earlier than the
			// first one's.
			return slot{}, ErrNotFound
		case !opened:
			sl.flags |= flagOpened
			sl.expires = min(sl.expires, expiryToSeconds(now.Add(time.Duration(l.r.afterOpen)*time.Second)))
		}
		return sl, nil
	})
	if errors.Is(err, ErrNotFound) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	defer s.release(l.f)
	// A later open of a link opened already waits for the first open's sync.
	if err := s.await(p); err != nil {
		return "", false, err
	}
	return toLink(code, sl, l.r).URL, true, nil
}

// ForgetRemovedFiles lets go of every chunk file removed from its folder
// since the last call, so that its links lead nowhere from then on, and
// returns how many. That is every open file that was removed, every file
// the limit on open files closed whose name is gone, and every such closed
// file a lookup has found missing, cut short or damaged since, whatever its
// name says now: the link cache may still hold the links of each. A
// removed file is not opened again until qisqa restarts. An open file in
// use at that moment is left for the next call. A removed file where new
// links of its length go is logged, and numbering moves on to the
// following chunk, as at startup.
//
// Each call looks at the open files through their handles, and at the
// closed ones by name before it takes a lock, so that lookups do not wait
// for those: there are none while no more than maxOpenChunks files were
// ever opened.
func (s *Store) ForgetRemovedFiles() int {
	var lost, closed []chunkID
	s.openMu.RLock()
	for id := range s.closed {
		if s.gone[id] != nil {
			lost = append(lost, id) // a lookup found it lost: its name says nothing
		} else {
			closed = append(closed, id)
		}
	}
	s.openMu.RUnlock()
	for _, id := range closed {
		if _, err := os.Lstat(s.chunkPath(id)); errors.Is(err, fs.ErrNotExist) {
			lost = append(lost, id)
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.openMu.Lock()
	defer s.openMu.Unlock()
	n := 0
	for id, f := range s.files {
		if f.users.Load() > 0 || !removed(f.f) {
			continue
		}
		f.close()
		delete(s.files, id)
		s.forgetLost(id)
		n++
	}
	for _, id := range lost {
		if s.closed[id] { // not opened again since it was looked at
			delete(s.closed, id)
			s.forgetLost(id)
			n++
		}
	}
	return n
}

// forgetLost remembers that chunk id is lost, so that it is not opened
// again, and moves numbering on when new links of its length go there. A
// file that a lookup found lost keeps the error it was found with. The
// caller holds the write lock and openMu exclusively.
func (s *Store) forgetLost(id chunkID) {
	why := s.gone[id]
	if why == nil {
		why = errors.New("removed while qisqa was running")
		s.gone[id] = errMissing
	}
	if d := (*s.domains.Load())[id.domain]; d != nil && d.lengths[id.length].chunk == int64(id.index) {
		s.moveOn(&d.lengths[id.length], id, why)
	}
}

// removed reports whether a file no longer has a name in any folder.
func removed(f *os.File) bool {
	info, err := f.Stat()
	if err != nil {
		return false
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	return ok && st.Nlink == 0
}

// find returns the link a code stands for on a domain, its file acquired.
// ErrNotFound means there is no link to change: the code is unknown, or its
// link deleted, damaged or in a lost file. Once the store is shut it
// returns errClosed, and after a failed sync that sync's error, before
// anything else. The caller holds the write lock.
func (s *Store) find(domain uint16, code string) (stored, error) {
	if s.lock == nil {
		return stored{}, errClosed
	}
	if err := s.failed(); err != nil {
		return stored{}, err
	}
	d, err := s.startDomain(domain)
	if err != nil {
		return stored{}, err
	}
	length, m, ok := d.coder.Number(code)
	if !ok || m >= d.lengths[length].next.Load() {
		return stored{}, ErrNotFound
	}
	f, err := s.acquire(chunkID{domain, length, m / chunkLinks})
	if lost(err) {
		return stored{}, ErrNotFound
	}
	if err != nil {
		return stored{}, err
	}
	sl, r, state, damage := f.readLink(m)
	if damage != nil {
		slog.Error("a link is damaged and leads nowhere", "domain", domain, "code", code, "error", damage)
	}
	if state != LinkFound {
		s.release(f)
		return stored{}, ErrNotFound
	}
	return stored{f, m, sl, r}, nil
}

// editSlot finds the link a code stands for and, under the write lock,
// writes the slot edit returns in place of its slot; when edit returns an
// error, or the same slot, nothing is written. It returns the link as it
// was, its file acquired, the slot it has now, and the pass that covers the
// writes made to the file so far. That is so also when nothing was written
// now: the change waits for earlier writes too, such as the first open of a
// link opened again.
func (s *Store) editSlot(domain uint16, code string, edit func(stored) (slot, error)) (stored, slot, *pass, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	l, err := s.find(domain, code)
	if err != nil {
		return stored{}, slot{}, nil, err
	}
	sl, err := edit(l)
	if err != nil {
		s.release(l.f)
		return stored{}, slot{}, nil, err
	}
	if sl != l.s {
		if err = l.f.beginWrites(); err == nil {
			err = l.f.writeSlot(int(l.m%chunkLinks), sl)
		}
	}
	p := s.toSync(l.f) // also after a failed write: part of it may be in the file
	if err != nil {
		s.release(l.f)
		return stored{}, slot{}, nil, err
	}
	return l, sl, p, nil
}

// appendFor writes record r for the link a code stands for, under the write
// lock, once editable accepts the link. slotFor gives the slot that will
// point at the record, whose owner and flags its check covers. It returns
// the file, acquired, that slot with where the record is, and the pass that
// covers the writes made to the file so far.
func (s *Store) appendFor(domain uint16, code string, editable func(stored) error, slotFor func(slot) slot, r record) (*openFile, slot, *pass, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	l, err := s.find(domain, code)
	if err != nil {
		return nil, slot{}, nil, err
	}
	sl := slotFor(l.s)
	if err := editable(l); err != nil {
		s.release(l.f)
		return nil, slot{}, nil, err
	}
	err = l.f.beginWrites()
	if err == nil {
		sl.offset, sl.length, err = l.f.appendRecord(recordCheck(l.f.id.length, l.m, sl, r), sl.flags, r)
	}
	p := s.toSync(l.f) // also after a failed write: part of it may be in the file
	if err != nil {
		s.release(l.f)
		return nil, slot{}, nil, err
	}
	return l.f, sl, p, nil
}

// erase overwrites with zeros the record sl points at, once no slot points
// at it (on disk, without sync_every), so that a replaced or deleted URL
// does not stay in the file. The next pass syncs the zeros; nothing waits
// for it. It writes nothing after a failed sync, nor once the store is
// shut. A failure of its own is only logged: the change it follows is made
// already.
func (s *Store) erase(f *openFile, sl slot) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.lock == nil || s.failed() != nil {
		return
	}
	if err := f.zero(sl.offset, sl.length); err != nil {
		slog.Error("a replaced or deleted URL could not be erased from its chunk file", "file", f.path, "error", err)
	}
	s.toSync(f)
}

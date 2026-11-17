// Password links: the hash a link's password is kept as, the check of a
// password against it, and the locks that stop guessing. A password itself
// is never kept, and nothing of the locks is written to disk.
package app

import (
	"container/list"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"sync"
	"time"
)

const (
	// minPasswordLength and maxPasswordLength bound a link's password, in
	// bytes.
	minPasswordLength = 4
	maxPasswordLength = 128
	// passwordIterations is the PBKDF2 iteration count. One check takes
	// about 10 ms on one core: slow for a guesser, nothing for a person.
	passwordIterations = 70_000
	// maxWrongPasswords wrong passwords in a row lock a link for
	// passwordLockFor.
	maxWrongPasswords = 5
	passwordLockFor   = 5 * time.Minute
	// maxCountedLinks is how many links the server counts wrong passwords
	// for at most.
	maxCountedLinks = 10_000
)

// newSalt returns a new random salt for a password.
func newSalt() []byte {
	salt := make([]byte, saltSize)
	rand.Read(salt)
	return salt
}

// hashPassword returns the hash a password is kept as: PBKDF2-HMAC-SHA256
// of the password and the salt.
func hashPassword(password string, salt []byte) []byte {
	hash, err := pbkdf2.Key(sha256.New, password, salt, passwordIterations, hashSize)
	if err != nil {
		// Key fails only for a key length, or in FIPS 140-only mode a salt
		// shorter than 16 bytes, that this program never passes.
		panic(err)
	}
	return hash
}

// possiblePassword reports whether password is as long as a link's password
// may be.
func possiblePassword(password string) bool {
	return len(password) >= minPasswordLength && len(password) <= maxPasswordLength
}

// checkPassword reports whether password is the one salt and hash were made
// from, comparing the hashes in constant time. A password no link can have,
// and a link without a salt and a hash, are wrong without hashing.
func checkPassword(password string, salt, hash []byte) bool {
	if !possiblePassword(password) || len(salt) != saltSize || len(hash) != hashSize {
		return false
	}
	return subtle.ConstantTimeCompare(hashPassword(password, salt), hash) == 1
}

// LinkLocks keeps, per link, the wrong passwords in a row and the passwords
// being checked. A check may begin only while the link is not locked and
// the two together are under maxWrongPasswords, so that checks running at
// once can never pass the lock, and while fewer than maxChecks checks run
// on all links together, so that guesses at many links cannot take every
// core. maxWrongPasswords wrong ones in a row lock the link for
// passwordLockFor. It remembers at most max links: one more makes it forget
// the link used longest ago. Safe for concurrent use.
type LinkLocks struct {
	mu        sync.Mutex
	max       int
	maxChecks int        // passwords checked at once on all links together, at most
	checks    int        // passwords being checked now, on all links together
	order     *list.List // of *linkLock; the front is the link used last
	links     map[string]*list.Element
}

// linkLock is what LinkLocks keeps for one link.
type linkLock struct {
	key         string
	wrong       int       // wrong passwords in a row since the last lock or right one
	checking    int       // passwords being checked now
	lockedUntil time.Time // zero when the link was never locked
}

// checkStart is the answer of Start.
type checkStart int

const (
	checkGo     checkStart = iota // the password may be checked; Done must follow
	checkBusy                     // others are being checked: try again in a moment
	checkLocked                   // the link is locked after wrong passwords
)

// NewLinkLocks returns locks that remember at most max links and let at
// most maxChecks checks run at once.
func NewLinkLocks(max, maxChecks int) *LinkLocks {
	return &LinkLocks{max: max, maxChecks: maxChecks, order: list.New(), links: make(map[string]*list.Element)}
}

// Locked reports whether the link key is locked at now.
func (l *LinkLocks) Locked(key string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	el := l.links[key]
	return el != nil && now.Before(el.Value.(*linkLock).lockedUntil)
}

// Start asks to check a password of the link key at now. With checkGo the
// check has its place, which Done gives back; looking and taking the place
// are one step. An attempt turned away is not counted, and one turned away
// because maxChecks run leaves no entry behind: a new one could push out a
// link that holds a count.
func (l *LinkLocks) Start(key string, now time.Time) checkStart {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.links[key] == nil && l.checks >= l.maxChecks {
		return checkBusy
	}
	lock := l.get(key)
	switch {
	case now.Before(lock.lockedUntil):
		return checkLocked
	case lock.wrong+lock.checking >= maxWrongPasswords, l.checks >= l.maxChecks:
		return checkBusy
	}
	lock.checking++
	l.checks++
	return checkGo
}

// Done ends a check that Start let begin and gives its place back. A wrong
// password counts toward the lock, unless the link is locked already; the
// maxWrongPasswords-th in a row locks it, and the count starts again. A
// right one sets the count back to 0. A link holding nothing any more is
// forgotten. If the link was dropped while its check ran, a wrong password
// is counted afresh and a right one changes nothing else.
func (l *LinkLocks) Done(key string, right bool, now time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.checks = max(l.checks-1, 0)
	el := l.links[key]
	if el == nil && right {
		return
	}
	lock := l.get(key)
	lock.checking = max(lock.checking-1, 0)
	switch {
	case right:
		lock.wrong = 0
	case !now.Before(lock.lockedUntil):
		if lock.wrong++; lock.wrong >= maxWrongPasswords {
			lock.wrong, lock.lockedUntil = 0, now.Add(passwordLockFor)
		}
	}
	if lock.holdsNothing(now) {
		l.forget(l.links[key])
	}
}

// Sweep forgets the links that hold nothing at now: no wrong password, no
// check under way, no lock that has not run out.
func (l *LinkLocks) Sweep(now time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, el := range l.links {
		if el.Value.(*linkLock).holdsNothing(now) {
			l.forget(el)
		}
	}
}

// get returns the entry of the link key, made the most recently used, and
// adds it when it is missing, forgetting the oldest entry when the table is
// full. The caller holds mu.
func (l *LinkLocks) get(key string) *linkLock {
	if el := l.links[key]; el != nil {
		l.order.MoveToFront(el)
		return el.Value.(*linkLock)
	}
	if l.order.Len() >= l.max {
		l.forget(l.order.Back())
	}
	lock := &linkLock{key: key}
	l.links[key] = l.order.PushFront(lock)
	return lock
}

// forget drops the entry el. The caller holds mu.
func (l *LinkLocks) forget(el *list.Element) {
	l.order.Remove(el)
	delete(l.links, el.Value.(*linkLock).key)
}

// holdsNothing reports whether the entry has nothing to keep at now.
func (lock *linkLock) holdsNothing(now time.Time) bool {
	return lock.wrong == 0 && lock.checking == 0 && !now.Before(lock.lockedUntil)
}

// Len returns the number of links remembered.
func (l *LinkLocks) Len() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.links)
}

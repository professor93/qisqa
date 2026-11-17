// Tests for password links: the hash, the check and the locks.
package main

import (
	"bytes"
	"fmt"
	"testing"
	"time"
)

func TestPasswordHash(t *testing.T) {
	salt, other := newSalt(), newSalt()
	eq(t, len(salt), saltSize)
	eq(t, bytes.Equal(salt, other), false)

	hash := hashPassword("correct horse", salt)
	eq(t, len(hash), hashSize)
	eq(t, bytes.Equal(hashPassword("correct horse", salt), hash), true)
	eq(t, bytes.Equal(hashPassword("correct horse", other), hash), false)
	eq(t, bytes.Equal(hashPassword("correct horsf", salt), hash), false)

	eq(t, checkPassword("correct horse", salt, hash), true)
	for _, wrong := range []string{"correct horsf", "correct hors", "Correct horse", "correct horse ", ""} {
		eq(t, checkPassword(wrong, salt, hash), false)
	}
	eq(t, checkPassword("correct horse", other, hash), false)
}

// A link without a password has no salt and no hash: no password opens it,
// not even one whose hash would compare equal to nothing.
func TestPasswordCheckNeedsASaltAndAHash(t *testing.T) {
	salt := newSalt()
	hash := hashPassword("correct horse", salt)
	eq(t, checkPassword("correct horse", nil, nil), false)
	eq(t, checkPassword("correct horse", salt, nil), false)
	eq(t, checkPassword("correct horse", nil, hash), false)
	eq(t, checkPassword("correct horse", salt, hash[:hashSize-1]), false)
	eq(t, checkPassword("correct horse", salt[:saltSize-1], hash), false)
}

// A password the API would not have taken is wrong without hashing: no
// password link has one.
func TestPasswordOutsideTheLimitsIsWrong(t *testing.T) {
	salt := newSalt()
	for _, password := range []string{"abc", string(bytes.Repeat([]byte("a"), maxPasswordLength+1))} {
		eq(t, checkPassword(password, salt, hashPassword(password, salt)), false)
	}
	for _, password := range []string{"abcd", string(bytes.Repeat([]byte("a"), maxPasswordLength))} {
		eq(t, checkPassword(password, salt, hashPassword(password, salt)), true)
	}
}

// checked runs one check of a password for key on locks, which must let it
// begin, and ends it as right or wrong.
func checked(t *testing.T, locks *LinkLocks, key string, right bool, now time.Time) {
	t.Helper()
	eq(t, locks.Start(key, now), checkGo)
	locks.Done(key, right, now)
}

func TestLinkLocks(t *testing.T) {
	locks := NewLinkLocks(10, 10)
	for range 4 {
		checked(t, locks, "a", false, t0)
	}
	eq(t, locks.Locked("a", t0), false)
	checked(t, locks, "a", false, t0)
	eq(t, locks.Locked("a", t0), true)
	eq(t, locks.Start("a", t0), checkLocked)
	eq(t, locks.Locked("a", t0.Add(5*time.Minute-time.Second)), true)
	eq(t, locks.Start("a", t0.Add(5*time.Minute-time.Second)), checkLocked)
	eq(t, locks.Locked("a", t0.Add(5*time.Minute)), false)
	eq(t, locks.Locked("b", t0), false) // a lock is per link

	// After the lock the count starts again.
	later := t0.Add(5 * time.Minute)
	for range 4 {
		checked(t, locks, "a", false, later)
	}
	eq(t, locks.Locked("a", later), false)

	// The right password sets the count back to 0.
	for range 3 {
		checked(t, locks, "c", false, t0)
	}
	checked(t, locks, "c", true, t0)
	for range 4 {
		checked(t, locks, "c", false, t0)
	}
	eq(t, locks.Locked("c", t0), false)
	checked(t, locks, "c", false, t0)
	eq(t, locks.Locked("c", t0), true)

	// The checks running on all links together never pass the cap: a link
	// with room is busy too while it is reached.
	locks = NewLinkLocks(10, 2)
	eq(t, locks.Start("a", t0), checkGo)
	eq(t, locks.Start("b", t0), checkGo)
	eq(t, locks.Start("c", t0), checkBusy)
	eq(t, locks.Len(), 2) // c, turned away, left nothing behind
	eq(t, locks.Start("a", t0), checkBusy)
	eq(t, locks.Locked("c", t0), false)
	// Done gives the place back.
	locks.Done("a", true, t0)
	eq(t, locks.Start("c", t0), checkGo)
	eq(t, locks.Start("a", t0), checkBusy)
	locks.Done("b", false, t0)
	locks.Done("c", true, t0)
	eq(t, locks.Len(), 1) // b's wrong password

	// A link dropped while its check runs gives its place back all the same.
	locks = NewLinkLocks(1, 2)
	eq(t, locks.Start("a", t0), checkGo)
	eq(t, locks.Start("b", t0), checkGo) // drops a
	locks.Done("a", false, t0)           // drops b
	locks.Done("b", true, t0)            // b is gone, its place is not
	eq(t, locks.Start("c", t0), checkGo)
	eq(t, locks.Start("d", t0), checkGo)
	eq(t, locks.Start("e", t0), checkBusy)
}

// No more passwords of a link are checked at once than could still lock it:
// the wrong ones so far and those being checked stay under five. A check
// turned away is busy, not locked, and counts for nothing.
func TestLinkLocksLimitChecksAtOnce(t *testing.T) {
	locks := NewLinkLocks(10, 10)
	for range 5 {
		eq(t, locks.Start("a", t0), checkGo)
	}
	eq(t, locks.Start("a", t0), checkBusy)
	eq(t, locks.Locked("a", t0), false)
	locks.Done("a", true, t0)
	eq(t, locks.Start("a", t0), checkGo) // the place was given back
	for range 5 {
		locks.Done("a", true, t0)
	}
	eq(t, locks.Len(), 0) // nothing counted, nothing being checked

	for range 4 {
		checked(t, locks, "b", false, t0)
	}
	eq(t, locks.Start("b", t0), checkGo)
	eq(t, locks.Start("b", t0), checkBusy)
	locks.Done("b", true, t0)
	eq(t, locks.Len(), 0) // the right one set the count back to 0
	for range 4 {
		checked(t, locks, "b", false, t0)
	}
	eq(t, locks.Locked("b", t0), false)
}

func TestLinkLocksDropTheOldest(t *testing.T) {
	locks := NewLinkLocks(2, 10)
	for range 5 {
		checked(t, locks, "a", false, t0)
	}
	checked(t, locks, "b", false, t0)
	checked(t, locks, "c", false, t0) // a is the oldest
	eq(t, locks.Len(), 2)
	eq(t, locks.Locked("a", t0), false)

	// The oldest is the link counted longest ago, not the first one counted.
	locks = NewLinkLocks(2, 10)
	for range 4 {
		checked(t, locks, "a", false, t0)
	}
	checked(t, locks, "b", false, t0)
	checked(t, locks, "a", false, t0) // the fifth locks a, and b is now the oldest
	checked(t, locks, "c", false, t0)
	eq(t, locks.Len(), 2)
	eq(t, locks.Locked("a", t0), true)
}

// A check whose link was dropped while it ran ends without harm: no panic,
// and no count of checks below zero, which would let more run at once.
func TestLinkLocksEndACheckWhoseLinkWasDropped(t *testing.T) {
	locks := NewLinkLocks(1, 10)
	eq(t, locks.Start("a", t0), checkGo)
	eq(t, locks.Start("b", t0), checkGo) // drops a
	locks.Done("a", false, t0)           // the wrong password still counts, and b is dropped
	eq(t, locks.Len(), 1)
	locks.Done("b", true, t0)  // b is gone: nothing to end
	locks.Done("a", false, t0) // more ends than begins: a second wrong password

	for range 3 {
		eq(t, locks.Start("a", t0), checkGo)
	}
	eq(t, locks.Start("a", t0), checkBusy) // two wrong and three being checked
}

func TestLinkLocksSweep(t *testing.T) {
	locks := NewLinkLocks(10, 10)
	for range 5 {
		checked(t, locks, "locked", false, t0)
	}
	checked(t, locks, "counted", false, t0)
	eq(t, locks.Start("checking", t0), checkGo)
	locks.Sweep(t0.Add(5*time.Minute - time.Second))
	eq(t, locks.Len(), 3)
	eq(t, locks.Locked("locked", t0.Add(5*time.Minute-time.Second)), true)

	// An expired lock is forgotten. A count is kept, since it counts wrong
	// passwords in a row, and so is a check still under way.
	locks.Sweep(t0.Add(5 * time.Minute))
	eq(t, locks.Len(), 2)
	for range 4 {
		checked(t, locks, "counted", false, t0.Add(5*time.Minute))
	}
	eq(t, locks.Locked("counted", t0.Add(5*time.Minute)), true)
	locks.Done("checking", false, t0.Add(5*time.Minute))
	eq(t, locks.Len(), 2)
}

func TestLinkLocksAreSafeForConcurrentUse(t *testing.T) {
	locks := NewLinkLocks(4, 3)
	done := make(chan struct{})
	for i := range 8 {
		go func() {
			defer func() { done <- struct{}{} }()
			key := fmt.Sprintf("link%d", i%6)
			for j := range 50 {
				if locks.Start(key, t0) == checkGo {
					locks.Done(key, j%3 == 0, t0)
				}
				locks.Locked(key, t0)
				locks.Sweep(t0)
			}
		}()
	}
	for range 8 {
		<-done
	}
}

// BenchmarkPasswordCheck measures one check of a password, the cost
// passwordIterations is chosen by: about 10 ms on one core.
func BenchmarkPasswordCheck(b *testing.B) {
	salt := newSalt()
	hash := hashPassword("correct horse", salt)
	for b.Loop() {
		checkPassword("correct horse", salt, hash)
	}
}

// Tests for turning link numbers into codes and codes back into numbers.
package app

import (
	"math/rand/v2"
	"strings"
	"testing"
)

func TestClassSizes(t *testing.T) {
	eq(t, classSize(3), uint64(238_328))
	eq(t, classCapacity(3), uint64(23_832))
	eq(t, classCapacity(5), uint64(91_613_283))
	eq(t, classSize(10), uint64(839_299_365_868_340_224))
}

func TestBase62(t *testing.T) {
	eq(t, encodeBase62(0, 5), "00000")
	eq(t, encodeBase62(61, 3), "00z")
	eq(t, encodeBase62(62, 3), "010")
	x, ok := decodeBase62("00z")
	eq(t, ok, true)
	eq(t, x, uint64(61))
	for _, bad := range []string{"", "ab", "abc-1", "abc d", "aaaaaaaaaaa"} { // 11 characters is too long
		if _, ok := decodeBase62(bad); ok {
			t.Errorf("decodeBase62(%q) succeeded", bad)
		}
	}
}

// Every number of the smallest class is tried: all results are in range,
// all differ, and each comes back.
func TestPermutationIsOneToOne(t *testing.T) {
	size := classSize(3)
	p := NewPermutation([16]byte{1, 2, 3}, size)
	seen := make([]bool, size)
	for x := range size {
		y := p.Encrypt(x)
		if y >= size || seen[y] {
			t.Fatalf("Encrypt(%d) = %d: out of range or repeated", x, y)
		}
		seen[y] = true
		if back := p.Decrypt(y); back != x {
			t.Fatalf("Decrypt(Encrypt(%d)) = %d", x, back)
		}
	}
}

// The larger classes are too big to walk through, so random numbers stand
// in for all of them.
func TestPermutationRoundTripsAtEveryLength(t *testing.T) {
	for length := minCodeLength; length <= maxCodeLength; length++ {
		size := classSize(length)
		p := NewPermutation([16]byte{byte(length)}, size)
		for range 1000 {
			x := rand.Uint64N(size)
			y := p.Encrypt(x)
			if y >= size {
				t.Fatalf("length %d: Encrypt(%d) = %d, not below %d", length, x, y, size)
			}
			if back := p.Decrypt(y); back != x {
				t.Fatalf("length %d: Decrypt(Encrypt(%d)) = %d", length, x, back)
			}
		}
	}
}

func TestPermutationDependsOnKey(t *testing.T) {
	size := classSize(5)
	a := NewPermutation([16]byte{1}, size)
	b := NewPermutation([16]byte{2}, size)
	differ := 0
	for x := range uint64(100) {
		if a.Encrypt(x) != b.Encrypt(x) {
			differ++
		}
	}
	if differ < 90 {
		t.Fatalf("the two keys differ on only %d of 100 numbers", differ)
	}
}

func TestCoderRoundTrip(t *testing.T) {
	c := NewCoder([]byte("a master key for tests"), 1)
	for _, length := range []int{3, 5, 10} {
		for _, m := range []uint64{0, 1, 65535, 65536, classCapacity(length) - 1} {
			code := c.Code(length, m)
			eq(t, len(code), length)
			gotLength, gotM, ok := c.Number(code)
			if !ok || gotLength != length || gotM != m {
				t.Fatalf("Number(%q) = %d, %d, %v; want %d, %d, true", code, gotLength, gotM, ok, length, m)
			}
		}
	}
}

func TestCoderDomainsDiffer(t *testing.T) {
	master := []byte("a master key for tests")
	if a, b := NewCoder(master, 1).Code(5, 0), NewCoder(master, 2).Code(5, 0); a == b {
		t.Fatalf("domains 1 and 2 both give %q for number 0", a)
	}
}

func TestCoderRejectsMalformed(t *testing.T) {
	c := NewCoder([]byte("a master key for tests"), 1)
	for _, bad := range []string{"", "ab", "abc-1", strings.Repeat("a", 11)} {
		if _, _, ok := c.Number(bad); ok {
			t.Errorf("Number(%q) succeeded", bad)
		}
	}
}

// Codes already handed out must decode the same way forever, so the format
// is pinned here: any change to the algorithm fails this test.
func TestCoderIsStable(t *testing.T) {
	master := make([]byte, 32)
	for i := range master {
		master[i] = byte(i)
	}
	c := NewCoder(master, 1)
	for _, tc := range []struct {
		length int
		m      uint64
		want   string
	}{
		{3, 0, "Tyo"},
		{5, 0, "6luC2"},
		{5, 1, "tnCXw"},
		{10, 12345678901, "NxyeIQPoSz"},
	} {
		if got := c.Code(tc.length, tc.m); got != tc.want {
			t.Errorf("Code(%d, %d) = %q, want %q", tc.length, tc.m, got, tc.want)
		}
	}
}

// Codes from numbers: within a domain every code length counts its links
// 0, 1, 2, ..., and a link's code is its number run through a keyed
// permutation and written in base62. A code therefore decodes back to its
// number, while neighbouring numbers give unrelated codes.
package app

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"math/bits"
	"strings"
)

// alphabet is base62. Codes are case-sensitive.
const alphabet = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"

// Codes are 3 to 10 characters long; 62^10 still fits in a uint64.
const minCodeLength, maxCodeLength = 3, 10

// feistelRounds is the number of rounds of the permutation's network.
const feistelRounds = 10

// classSize is the number of codes of the given length: 62^length.
func classSize(length int) uint64 {
	size := uint64(1)
	for range length {
		size *= 62
	}
	return size
}

// classCapacity is how many links one length may hold: a tenth of its
// codes, so that at most one code in ten guessed at random leads anywhere.
func classCapacity(length int) uint64 {
	return classSize(length) / 10
}

// encodeBase62 writes x as exactly length base62 digits, padded with
// leading zeros. x must be below classSize(length).
func encodeBase62(x uint64, length int) string {
	code := make([]byte, length)
	for i := length - 1; i >= 0; i-- {
		code[i] = alphabet[x%62]
		x /= 62
	}
	return string(code)
}

// decodeBase62 reads the number back from a code. It fails when the code
// is shorter than minCodeLength, longer than maxCodeLength or has a
// character outside the alphabet.
func decodeBase62(code string) (x uint64, ok bool) {
	if len(code) < minCodeLength || len(code) > maxCodeLength {
		return 0, false
	}
	for i := range len(code) {
		digit := strings.IndexByte(alphabet, code[i])
		if digit < 0 {
			return 0, false
		}
		x = x*62 + uint64(digit)
	}
	return x, true
}

// Permutation is a keyed one-to-one mapping of the numbers [0, size) onto
// themselves. It is a Feistel network on 2w bits with AES-128 as the round
// function; a result of size or more is put through the network again
// (cycle walking) until it falls below size.
type Permutation struct {
	block cipher.Block
	size  uint64
	w     uint   // bits in each half
	mask  uint64 // the low w bits set
}

// NewPermutation returns the permutation of [0, size) that key selects.
func NewPermutation(key [16]byte, size uint64) *Permutation {
	block, _ := aes.NewCipher(key[:]) // never fails: the key is 16 bytes
	// 2w is the smallest even number of bits that holds size-1.
	w := uint(bits.Len64(size-1)+1) / 2
	return &Permutation{block: block, size: size, w: w, mask: 1<<w - 1}
}

// round is the round function: the AES encryption of a block holding the
// round number and one half, cut to w bits.
func (p *Permutation) round(i byte, half uint64) uint64 {
	var b [16]byte // byte 0 is the round, bytes 8-15 the half, the rest zero
	b[0] = i
	binary.BigEndian.PutUint64(b[8:], half)
	p.block.Encrypt(b[:], b[:])
	return binary.BigEndian.Uint64(b[:8]) & p.mask
}

// Encrypt returns the number x maps to. x must be below size.
func (p *Permutation) Encrypt(x uint64) uint64 {
	for {
		l, r := x>>p.w, x&p.mask
		for i := range feistelRounds {
			l, r = r, l^p.round(byte(i), r)
		}
		x = l<<p.w | r
		if x < p.size {
			return x
		}
	}
}

// Decrypt returns the number that maps to y: it undoes Encrypt. y must be
// below size.
func (p *Permutation) Decrypt(y uint64) uint64 {
	for {
		l, r := y>>p.w, y&p.mask
		for i := feistelRounds - 1; i >= 0; i-- {
			r, l = l, r^p.round(byte(i), l)
		}
		y = l<<p.w | r
		if y < p.size {
			return y
		}
	}
}

// Coder turns the link numbers of one domain into codes and codes back
// into numbers. Every code length has its own key. Nothing in a Coder
// changes after NewCoder, so it is safe for concurrent use.
type Coder struct {
	perms [maxCodeLength - minCodeLength + 1]*Permutation // by length - minCodeLength
}

// NewCoder derives the keys of one domain from master, the secret that
// every code depends on.
func NewCoder(master []byte, domain uint16) *Coder {
	c := &Coder{}
	for length := minCodeLength; length <= maxCodeLength; length++ {
		mac := hmac.New(sha256.New, master)
		mac.Write([]byte("qisqa code v1"))
		mac.Write([]byte{byte(domain >> 8), byte(domain), byte(length)})
		var key [16]byte
		copy(key[:], mac.Sum(nil)) // the first 16 of its 32 bytes
		c.perms[length-minCodeLength] = NewPermutation(key, classSize(length))
	}
	return c
}

// Code returns the code of link number m among the links of the given
// length. length must be from minCodeLength to maxCodeLength, and m below
// classSize(length).
func (c *Coder) Code(length int, m uint64) string {
	return encodeBase62(c.perms[length-minCodeLength].Encrypt(m), length)
}

// Number returns the length and link number that code stands for. ok is
// false when the code is malformed: too short, too long, or with a
// character outside the alphabet. Whether that number was ever handed out
// is for the caller to check.
func (c *Coder) Number(code string) (length int, m uint64, ok bool) {
	x, ok := decodeBase62(code)
	if !ok {
		return 0, 0, false
	}
	return len(code), c.perms[len(code)-minCodeLength].Decrypt(x), true
}

// Package ccm implements CCM (Counter with CBC-MAC) as per RFC 3610.
//
// Vendored from github.com/pion/dtls/v3/pkg/crypto/ccm (MIT License).
// See LICENSE file in this directory for the original license.
//
// Original code from https://github.com/bocajim/dtls
// See https://tools.ietf.org/html/rfc3610
package ccm

import (
	"crypto/cipher"
	"crypto/subtle"
	"encoding/binary"
	"errors"
	"math"
)

type ccm struct {
	b cipher.Block
	M uint8
	L uint8
}

const ccmBlockSize = 16

// CCM is a block cipher in Counter with CBC-MAC mode.
// It provides authenticated encryption with associated data via cipher.AEAD.
type CCM interface {
	cipher.AEAD
	// MaxLength returns the maximum length of plaintext in calls to Seal.
	MaxLength() int
}

var (
	errInvalidBlockSize = errors.New("ccm: NewCCM requires 128-bit block cipher")
	errInvalidTagSize   = errors.New("ccm: tagsize must be 4, 6, 8, 10, 12, 14, or 16")
	errInvalidNonceSize = errors.New("ccm: invalid nonce size")
)

// NewCCM returns the given 128-bit block cipher wrapped in CCM.
// The tagsize must be an even integer between 4 and 16 inclusive (CCM's M parameter).
// The noncesize must be an integer between 7 and 13 inclusive.
func NewCCM(b cipher.Block, tagsize, noncesize int) (CCM, error) {
	if b.BlockSize() != ccmBlockSize {
		return nil, errInvalidBlockSize
	}
	if tagsize < 4 || tagsize > 16 || tagsize&1 != 0 {
		return nil, errInvalidTagSize
	}
	lensize := 15 - noncesize
	if lensize < 2 || lensize > 8 {
		return nil, errInvalidNonceSize
	}
	c := &ccm{b: b, M: uint8(tagsize), L: uint8(lensize)}
	return c, nil
}

func (c *ccm) NonceSize() int { return 15 - int(c.L) }
func (c *ccm) Overhead() int  { return int(c.M) }
func (c *ccm) MaxLength() int { return maxlen(c.L, c.Overhead()) }

func maxlen(l uint8, tagsize int) int {
	mLen := (uint64(1) << (8 * l)) - 1
	if m64 := uint64(math.MaxInt64) - uint64(tagsize); l > 8 || mLen > m64 {
		mLen = m64
	}
	if mLen != uint64(int(mLen)) {
		return math.MaxInt32 - tagsize
	}
	return int(mLen)
}

func (c *ccm) cbcRound(mac, data []byte) {
	for i := range ccmBlockSize {
		mac[i] ^= data[i]
	}
	c.b.Encrypt(mac, mac)
}

func (c *ccm) cbcData(mac, data []byte) {
	for len(data) >= ccmBlockSize {
		c.cbcRound(mac, data[:ccmBlockSize])
		data = data[ccmBlockSize:]
	}
	if len(data) > 0 {
		var block [ccmBlockSize]byte
		copy(block[:], data)
		c.cbcRound(mac, block[:])
	}
}

var errPlaintextTooLong = errors.New("ccm: plaintext too large")

func (c *ccm) tag(nonce, plaintext, adata []byte) ([]byte, error) {
	var mac [ccmBlockSize]byte

	if len(adata) > 0 {
		mac[0] |= 1 << 6
	}
	mac[0] |= (c.M - 2) << 2
	mac[0] |= c.L - 1
	if len(nonce) != c.NonceSize() {
		return nil, errInvalidNonceSize
	}
	if len(plaintext) > c.MaxLength() {
		return nil, errPlaintextTooLong
	}
	binary.BigEndian.PutUint64(mac[ccmBlockSize-8:], uint64(len(plaintext)))
	copy(mac[1:ccmBlockSize-c.L], nonce)
	c.b.Encrypt(mac[:], mac[:])

	var block [ccmBlockSize]byte
	if adataLength := uint64(len(adata)); adataLength > 0 {
		i := 2
		if adataLength <= 0xfeff {
			binary.BigEndian.PutUint16(block[:i], uint16(adataLength))
		} else {
			binary.BigEndian.PutUint16(block[0:2], 0xfeff)
			if adataLength < uint64(1<<32) {
				i = 2 + 4
				binary.BigEndian.PutUint32(block[2:i], uint32(adataLength))
			} else {
				i = 2 + 8
				binary.BigEndian.PutUint64(block[2:i], adataLength)
			}
		}
		i = copy(block[i:], adata)
		c.cbcRound(mac[:], block[:])
		c.cbcData(mac[:], adata[i:])
	}

	if len(plaintext) > 0 {
		c.cbcData(mac[:], plaintext)
	}

	return mac[:c.M], nil
}

func sliceForAppend(in []byte, n int) (head, tail []byte) {
	if total := len(in) + n; cap(in) >= total {
		head = in[:total]
	} else {
		head = make([]byte, total)
		copy(head, in)
	}
	tail = head[len(in):]
	return
}

// Seal encrypts and authenticates plaintext, authenticates the
// additional data and appends the result to dst, returning the updated slice.
func (c *ccm) Seal(dst, nonce, plaintext, adata []byte) []byte {
	tag, err := c.tag(nonce, plaintext, adata)
	if err != nil {
		panic(err)
	}

	var iv, s0 [ccmBlockSize]byte
	iv[0] = c.L - 1
	copy(iv[1:ccmBlockSize-c.L], nonce)
	c.b.Encrypt(s0[:], iv[:])
	for i := 0; i < int(c.M); i++ {
		tag[i] ^= s0[i]
	}
	iv[len(iv)-1] |= 1
	stream := cipher.NewCTR(c.b, iv[:])
	ret, out := sliceForAppend(dst, len(plaintext)+int(c.M))
	stream.XORKeyStream(out, plaintext)
	copy(out[len(plaintext):], tag)

	return ret
}

var (
	errOpen               = errors.New("ccm: message authentication failed")
	errCiphertextTooShort = errors.New("ccm: ciphertext too short")
	errCiphertextTooLong  = errors.New("ccm: ciphertext too long")
)

// Open decrypts and authenticates ciphertext, authenticates the
// additional data and, if successful, appends the resulting plaintext to dst.
func (c *ccm) Open(dst, nonce, ciphertext, adata []byte) ([]byte, error) {
	if len(ciphertext) < int(c.M) {
		return nil, errCiphertextTooShort
	}
	if len(ciphertext) > c.MaxLength()+c.Overhead() {
		return nil, errCiphertextTooLong
	}

	tag := make([]byte, int(c.M))
	copy(tag, ciphertext[len(ciphertext)-int(c.M):])
	ciphertextWithoutTag := ciphertext[:len(ciphertext)-int(c.M)]

	var iv, s0 [ccmBlockSize]byte
	iv[0] = c.L - 1
	copy(iv[1:ccmBlockSize-c.L], nonce)
	c.b.Encrypt(s0[:], iv[:])
	for i := 0; i < int(c.M); i++ {
		tag[i] ^= s0[i]
	}
	iv[len(iv)-1] |= 1
	stream := cipher.NewCTR(c.b, iv[:])

	plaintext := make([]byte, len(ciphertextWithoutTag))
	stream.XORKeyStream(plaintext, ciphertextWithoutTag)
	expectedTag, err := c.tag(nonce, plaintext, adata)
	if err != nil {
		return nil, err
	}

	if subtle.ConstantTimeCompare(tag, expectedTag) != 1 {
		return nil, errOpen
	}

	return append(dst, plaintext...), nil
}

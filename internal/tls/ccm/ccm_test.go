package ccm

import (
	"bytes"
	"crypto/aes"
	"encoding/hex"
	"errors"
	"fmt"
	"testing"
)

func hexDecode(t *testing.T, s string) []byte {
	t.Helper()
	r, err := hex.DecodeString(s)
	if err != nil {
		t.Fatalf("hex decode %q: %v", s, err)
	}
	return r
}

// RFC 3610 Section 8 test vectors
type vector struct {
	aesKey            []byte
	cipherText        []byte
	clearHeaderOctets int
	data              []byte
	m                 int
	nonce             []byte
}

var aesKey1 = "c0c1c2c3c4c5c6c7c8c9cacbcccdcecf"
var aesKey2 = "d7828d13b2b0bdc325a76236df93cc6b"

func TestRFC3610Vectors(t *testing.T) {
	cases := []vector{
		// Vector 1: M=8, nonce=13
		{
			aesKey:            hexDecode(t, aesKey1),
			cipherText:        hexDecode(t, "0001020304050607588c979a61c663d2f066d0c2c0f989806d5f6b61dac38417e8d12cfdf926e0"),
			clearHeaderOctets: 8,
			data:              hexDecode(t, "000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e"),
			m:                 8,
			nonce:             hexDecode(t, "00000003020100a0a1a2a3a4a5"),
		},
		// Vector 2: M=8, nonce=13
		{
			aesKey:            hexDecode(t, aesKey1),
			cipherText:        hexDecode(t, "000102030405060772c91a36e135f8cf291ca894085c87e3cc15c439c9e43a3ba091d56e10400916"),
			clearHeaderOctets: 8,
			data:              hexDecode(t, "000102030405060708090A0B0C0D0E0F101112131415161718191A1B1C1D1E1F"),
			m:                 8,
			nonce:             hexDecode(t, "00000004030201a0a1a2a3a4a5"),
		},
		// Vector 3: M=8, nonce=13
		{
			aesKey:            hexDecode(t, aesKey1),
			cipherText:        hexDecode(t, "000102030405060751b1e5f44a197d1da46b0f8e2d282ae871e838bb64da8596574adaa76fbd9fb0c5"),
			clearHeaderOctets: 8,
			data:              hexDecode(t, "000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f20"),
			m:                 8,
			nonce:             hexDecode(t, "00000005040302a0a1a2a3a4a5"),
		},
		// Vector 7: M=10, nonce=13
		{
			aesKey:            hexDecode(t, aesKey1),
			cipherText:        hexDecode(t, "00010203040506070135d1b2c95f41d5d1d4fec185d166b8094e999dfed96c048c56602c97acbb7490"),
			clearHeaderOctets: 8,
			data:              hexDecode(t, "000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e"),
			m:                 10,
			nonce:             hexDecode(t, "00000009080706a0a1a2a3a4a5"),
		},
		// Vector 13: M=8, nonce=13, key set 2
		{
			aesKey:            hexDecode(t, aesKey2),
			cipherText:        hexDecode(t, "0be1a88bace018b14cb97f86a2a4689a877947ab8091ef5386a6ffbdd080f8e78cf7cb0cddd7b3"),
			clearHeaderOctets: 8,
			data:              hexDecode(t, "0be1a88bace018b108e8cf97d820ea258460e96ad9cf5289054d895ceac47c"),
			m:                 8,
			nonce:             hexDecode(t, "00412b4ea9cdbe3c9696766cfa"),
		},
		// Vector 19: M=10, nonce=13, key set 2
		{
			aesKey:            hexDecode(t, aesKey2),
			cipherText:        hexDecode(t, "d85bc7e69f944fb8bc218daa947427b6db386a99ac1aef23ade0b52939cb6a637cf9bec2408897c6ba"),
			clearHeaderOctets: 8,
			data:              hexDecode(t, "d85bc7e69f944fb88a19b950bcf71a018e5e6701c91787659809d67dbedd18"),
			m:                 10,
			nonce:             hexDecode(t, "0042fff8f1951c3c9696766cfa"),
		},
	}

	for idx, tc := range cases {
		t.Run(fmt.Sprintf("vector_%d", idx+1), func(t *testing.T) {
			blk, err := aes.NewCipher(tc.aesKey)
			if err != nil {
				t.Fatalf("aes.NewCipher: %v", err)
			}

			c, err := NewCCM(blk, tc.m, len(tc.nonce))
			if err != nil {
				t.Fatalf("NewCCM: %v", err)
			}

			// Test Seal
			t.Run("seal", func(t *testing.T) {
				plaintext := tc.data[tc.clearHeaderOctets:]
				adata := tc.data[:tc.clearHeaderOctets]
				got := c.Seal(nil, tc.nonce, plaintext, adata)
				want := tc.cipherText[tc.clearHeaderOctets:]
				if !bytes.Equal(got, want) {
					t.Errorf("Seal:\n  got  %x\n  want %x", got, want)
				}
			})

			// Test Open
			t.Run("open", func(t *testing.T) {
				ciphertext := tc.cipherText[tc.clearHeaderOctets:]
				adata := tc.cipherText[:tc.clearHeaderOctets]
				got, err := c.Open(nil, tc.nonce, ciphertext, adata)
				if err != nil {
					t.Fatalf("Open: %v", err)
				}
				want := tc.data[tc.clearHeaderOctets:]
				if !bytes.Equal(got, want) {
					t.Errorf("Open:\n  got  %x\n  want %x", got, want)
				}
			})
		})
	}
}

func TestCCM8ForTLS(t *testing.T) {
	// Verify CCM-8 parameters used by IEEE 2030.5
	// TLS_ECDHE_ECDSA_WITH_AES_128_CCM_8: tagsize=8, noncesize=12
	key := make([]byte, 16)
	blk, err := aes.NewCipher(key)
	if err != nil {
		t.Fatalf("aes.NewCipher: %v", err)
	}

	c, err := NewCCM(blk, 8, 12)
	if err != nil {
		t.Fatalf("NewCCM(tag=8, nonce=12): %v", err)
	}

	if c.NonceSize() != 12 {
		t.Errorf("NonceSize() = %d, want 12", c.NonceSize())
	}
	if c.Overhead() != 8 {
		t.Errorf("Overhead() = %d, want 8", c.Overhead())
	}

	// Round-trip test
	nonce := make([]byte, 12)
	plaintext := []byte("IEEE 2030.5 test payload")
	adata := []byte("additional data")

	ciphertext := c.Seal(nil, nonce, plaintext, adata)
	if len(ciphertext) != len(plaintext)+8 {
		t.Errorf("ciphertext length = %d, want %d", len(ciphertext), len(plaintext)+8)
	}

	decrypted, err := c.Open(nil, nonce, ciphertext, adata)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if !bytes.Equal(decrypted, plaintext) {
		t.Errorf("decrypted != plaintext")
	}
}

func TestCCMTamperedCiphertext(t *testing.T) {
	key := make([]byte, 16)
	blk, err := aes.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	c, err := NewCCM(blk, 8, 12)
	if err != nil {
		t.Fatal(err)
	}

	nonce := make([]byte, 12)
	plaintext := []byte("sensitive data")
	ciphertext := c.Seal(nil, nonce, plaintext, nil)

	// Flip a bit in the ciphertext
	tampered := make([]byte, len(ciphertext))
	copy(tampered, ciphertext)
	tampered[0] ^= 0x01

	_, err = c.Open(nil, nonce, tampered, nil)
	if err == nil {
		t.Error("Open should fail on tampered ciphertext")
	}
}

func TestNewCCMErrors(t *testing.T) {
	key := make([]byte, 16)
	blk, err := aes.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name      string
		tagsize   int
		noncesize int
		wantErr   error
	}{
		{"nonce too short", 8, 6, errInvalidNonceSize},
		{"nonce too long", 8, 14, errInvalidNonceSize},
		{"tag too small", 3, 12, errInvalidTagSize},
		{"tag too large", 17, 12, errInvalidTagSize},
		{"tag odd", 5, 12, errInvalidTagSize},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewCCM(blk, tt.tagsize, tt.noncesize)
			if !errors.Is(err, tt.wantErr) {
				t.Errorf("got error %v, want %v", err, tt.wantErr)
			}
		})
	}
}

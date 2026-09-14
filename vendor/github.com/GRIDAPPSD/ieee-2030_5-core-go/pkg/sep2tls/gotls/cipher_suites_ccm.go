package gotls

import (
	"crypto/aes"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2tls/ccm"
)

// TLS_ECDHE_ECDSA_WITH_AES_128_CCM_8 is the mandatory cipher suite
// for IEEE 2030.5-2018 (Smart Energy Profile 2).
// Cipher suite ID: 0xC0AE per RFC 7251.
const TLS_ECDHE_ECDSA_WITH_AES_128_CCM_8 uint16 = 0xC0AE

func init() {
	// Register CCM-8 in the cipher suite table
	cipherSuites = append(cipherSuites, &cipherSuite{
		id:     TLS_ECDHE_ECDSA_WITH_AES_128_CCM_8,
		keyLen: 16,
		macLen: 0,
		ivLen:  4,
		ka:     ecdheECDSAKA,
		flags:  suiteECDHE | suiteECSign | suiteTLS12,
		aead:   aeadAES128CCM8,
	})

	// Prepend to BOTH preference orders (with and without AES hardware):
	// IEEE 2030.5-2018 clause 6.7 makes CCM_8 mandatory, so pickCipherSuite
	// (handshake_server.go) must reach it before the GCM suite a dual-suite
	// client also offers. Appending ranked it last (GRIDAPPSD/ieee-2030_5-core-go#136).
	cipherSuitesPreferenceOrder = append([]uint16{TLS_ECDHE_ECDSA_WITH_AES_128_CCM_8}, cipherSuitesPreferenceOrder...)
	cipherSuitesPreferenceOrderNoAES = append([]uint16{TLS_ECDHE_ECDSA_WITH_AES_128_CCM_8}, cipherSuitesPreferenceOrderNoAES...)
}

// aeadAES128CCM8 creates an AES-128-CCM AEAD with 8-byte authentication tag.
// Uses the vendored pion CCM implementation (pure Go, RFC 3610).
func aeadAES128CCM8(key, noncePrefix []byte) aead {
	if len(noncePrefix) != noncePrefixLength {
		panic("gotls: internal error: wrong nonce length")
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		panic(err)
	}

	// CCM with tag=8, nonce=12 (4 implicit prefix + 8 explicit per-record)
	ccmAEAD, err := ccm.NewCCM(block, 8, aeadNonceLength)
	if err != nil {
		panic(err)
	}

	ret := &prefixNonceAEAD{aead: ccmAEAD}
	copy(ret.nonce[:], noncePrefix)
	return ret
}

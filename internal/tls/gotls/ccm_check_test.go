package gotls

import (
	"testing"
)

func TestCCMRegistered(t *testing.T) {
	t.Logf("cipherSuites count: %d", len(cipherSuites))

	found := false
	for _, cs := range cipherSuites {
		if cs.id == TLS_ECDHE_ECDSA_WITH_AES_128_CCM_8 {
			found = true
			t.Logf("Found CCM-8: id=0x%04X keyLen=%d macLen=%d ivLen=%d flags=%d",
				cs.id, cs.keyLen, cs.macLen, cs.ivLen, cs.flags)
			break
		}
	}
	if !found {
		t.Error("CCM-8 cipher suite not found in cipherSuites slice")
	}

	// Check it's in cipherSuitesPreferenceOrder
	foundInPref := false
	for _, id := range cipherSuitesPreferenceOrder {
		if id == TLS_ECDHE_ECDSA_WITH_AES_128_CCM_8 {
			foundInPref = true
			break
		}
	}
	t.Logf("cipherSuitesPreferenceOrder len=%d, has CCM=%v", len(cipherSuitesPreferenceOrder), foundInPref)

	// Also check cipherSuitesPreferenceOrderNoAES
	foundInNoAES := false
	for _, id := range cipherSuitesPreferenceOrderNoAES {
		if id == TLS_ECDHE_ECDSA_WITH_AES_128_CCM_8 {
			foundInNoAES = true
			break
		}
	}
	t.Logf("cipherSuitesPreferenceOrderNoAES len=%d, has CCM=%v", len(cipherSuitesPreferenceOrderNoAES), foundInNoAES)

	// Check it can be found by mutualCipherSuite
	suite := mutualCipherSuite([]uint16{TLS_ECDHE_ECDSA_WITH_AES_128_CCM_8}, TLS_ECDHE_ECDSA_WITH_AES_128_CCM_8)
	if suite == nil {
		t.Error("mutualCipherSuite did not find CCM-8")
	} else {
		t.Logf("mutualCipherSuite found: aead=%v", suite.aead != nil)
	}
}

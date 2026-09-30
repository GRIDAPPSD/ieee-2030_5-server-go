package metering_test

import (
	"bytes"
	"context"
	"encoding/xml"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/metering"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

// canonicalSelfLFDI carries a leading zero BYTE, so a serializer that drops
// leading zero bytes (the EPRI reference client's xml_output.c output_hex,
// "while (i < n-1 && value[i] == 0) i++;") produces a claim shorter than the
// canonical 40 hex characters. truncatedLowerSelfClaim is what that
// serializer actually emits for canonicalSelfLFDI's LFDI: leading zero byte
// dropped, remaining bytes lower case (xml_output.c's hex_char lower-cases).
const (
	canonicalSelfLFDI       = "001122334455667788990011223344556677889A"
	truncatedLowerSelfClaim = "1122334455667788990011223344556677889a"
)

// TestHandleCreateMirrorUsagePoint_LeadingZeroSelfClaimAccepted reproduces
// the EPRI client interop defect: a device whose own certificate LFDI has a
// leading zero byte self-mirrors, and the reference client's own
// serialization bug drops that byte and lower-cases the rest. Before
// restoring the dropped width, this claim compares unequal to the caller's
// full-width identity and is refused with 403 even though the caller is
// mirroring itself, a regression #720 must not introduce relative to the
// pre-#720 behavior (which ignored the claim entirely).
func TestHandleCreateMirrorUsagePoint_LeadingZeroSelfClaimAccepted(t *testing.T) {
	t.Parallel()
	s := memory.NewStore[sep2.MirrorUsagePoint]()
	mux := http.NewServeMux()
	mux.HandleFunc("POST /mup", metering.HandleCreateMirrorUsagePoint(s, nil, identityProvider(canonicalSelfLFDI), nil))

	mup := sep2.MirrorUsagePoint{MRID: "INV_LZ", DeviceLFDI: truncatedLowerSelfClaim}
	body, err := xml.Marshal(&mup)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/mup", bytes.NewReader(body)))

	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (self-claim, leading zero byte dropped by a known-buggy client); body: %s", w.Code, w.Body.String())
	}
	loc := w.Header().Get("Location")
	stored, err := s.Get(context.Background(), strings.TrimPrefix(loc, "/mup/"))
	if err != nil {
		t.Fatalf("get stored MirrorUsagePoint: %v", err)
	}
	if stored.DeviceLFDI != canonicalSelfLFDI {
		t.Errorf("stored DeviceLFDI = %q, want the full-width canonical %q (leading zero byte restored, upper case)", stored.DeviceLFDI, canonicalSelfLFDI)
	}
}

// TestHandleCreateMirrorUsagePoint_LowercaseFullWidthSelfClaimCanonicalized
// isolates the upper-casing half from the width-restoring half: a full-width
// (40 char) but lower-case self-claim must still compare equal to the
// caller's upper-case certificate identity and be stored canonical.
func TestHandleCreateMirrorUsagePoint_LowercaseFullWidthSelfClaimCanonicalized(t *testing.T) {
	t.Parallel()
	const caller = "AABBCCDDEEFF00112233445566778899AABBCCDD"
	s := memory.NewStore[sep2.MirrorUsagePoint]()
	mux := http.NewServeMux()
	mux.HandleFunc("POST /mup", metering.HandleCreateMirrorUsagePoint(s, nil, identityProvider(caller), nil))

	mup := sep2.MirrorUsagePoint{MRID: "INV_LC", DeviceLFDI: strings.ToLower(caller)}
	body, _ := xml.Marshal(&mup)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/mup", bytes.NewReader(body)))

	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (lower-case self-claim); body: %s", w.Code, w.Body.String())
	}
	loc := w.Header().Get("Location")
	stored, err := s.Get(context.Background(), strings.TrimPrefix(loc, "/mup/"))
	if err != nil {
		t.Fatalf("get stored MirrorUsagePoint: %v", err)
	}
	if stored.DeviceLFDI != caller {
		t.Errorf("stored DeviceLFDI = %q, want %q (canonical upper case)", stored.DeviceLFDI, caller)
	}
}

// TestHandleCreateMirrorUsagePoint_OverLongDeviceLFDIRejected: a claim longer
// than HexBinary160's 40-hex-character max cannot be a valid LFDI (IEEE
// 2030.5-2023 line 10727-10728) and is refused 400, not silently accepted or
// misread as an unclaimable-but-well-formed device (403).
func TestHandleCreateMirrorUsagePoint_OverLongDeviceLFDIRejected(t *testing.T) {
	t.Parallel()
	const caller = "AABBCCDDEEFF00112233445566778899AABBCCDD"
	s := memory.NewStore[sep2.MirrorUsagePoint]()
	mux := http.NewServeMux()
	mux.HandleFunc("POST /mup", metering.HandleCreateMirrorUsagePoint(s, nil, identityProvider(caller), nil))

	overLong := caller + "AA" // 42 hex chars
	mup := sep2.MirrorUsagePoint{MRID: "INV_OL", DeviceLFDI: overLong}
	body, _ := xml.Marshal(&mup)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/mup", bytes.NewReader(body)))

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (deviceLFDI exceeds HexBinary160's 40-char max); body: %s", w.Code, w.Body.String())
	}
	if count, _ := s.Count(context.Background()); count != 0 {
		t.Errorf("stored count = %d, want 0", count)
	}
}

// TestHandleCreateMirrorUsagePoint_OddLengthDeviceLFDIRejected: an odd number
// of hex digits cannot encode a whole number of bytes, so it cannot be a
// HexBinary160 value and is refused 400.
func TestHandleCreateMirrorUsagePoint_OddLengthDeviceLFDIRejected(t *testing.T) {
	t.Parallel()
	const caller = "AABBCCDDEEFF00112233445566778899AABBCCDD"
	s := memory.NewStore[sep2.MirrorUsagePoint]()
	mux := http.NewServeMux()
	mux.HandleFunc("POST /mup", metering.HandleCreateMirrorUsagePoint(s, nil, identityProvider(caller), nil))

	odd := "ABC" // 3 hex chars: odd length
	mup := sep2.MirrorUsagePoint{MRID: "INV_ODD", DeviceLFDI: odd}
	body, _ := xml.Marshal(&mup)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/mup", bytes.NewReader(body)))

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (odd hex length cannot be a whole-byte HexBinary160 value); body: %s", w.Code, w.Body.String())
	}
	if count, _ := s.Count(context.Background()); count != 0 {
		t.Errorf("stored count = %d, want 0", count)
	}
}

// TestHandlePutMirrorUsagePoint_MalformedDeviceLFDIRejected covers the PUT
// half of resolveMirroredDevice's validation, unreached by the POST tests
// above: a malformed claim is refused 400 and the stored record is
// untouched, mirroring TestHandleCreateMirrorUsagePoint_OddLengthDeviceLFDIRejected
// for PUT /mup/{id}.
func TestHandlePutMirrorUsagePoint_MalformedDeviceLFDIRejected(t *testing.T) {
	t.Parallel()
	mupStore := memory.NewStore[sep2.MirrorUsagePoint]()
	mmrStore := memory.NewScopedStore[sep2.MirrorMeterReading]()
	idA := seedDerivedMirror(t, mupStore, putOwnerLFDI, "MUP_A", "original A")

	mux := mirrorInstanceMux(mupStore, mmrStore, putOwnerLFDI)
	req := httptest.NewRequest(http.MethodPut, "/mup/"+idA,
		bytes.NewReader(mupWireBody("MUP_A", "probe", "ZZZ")))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (deviceLFDI is not valid hex); body: %s", w.Code, w.Body.String())
	}
	assertMirrorUnchanged(t, mupStore, idA, "MUP_A", putOwnerLFDI, "original A")
}

// evenLengthNonHex is 40 characters (even, and the canonical HexBinary160
// width), none of them hex digits. "ZZZ" (the malformed claim used above and
// in TestHandleCreateMirrorUsagePoint_OddLengthDeviceLFDIRejected) is both
// non-hex AND odd length, so canonicalDeviceLFDI's length%2 guard refuses it
// before the hex character-set loop ever runs: neither test proves that loop
// does anything. This value is even length so only the charset check can be
// what refuses it.
const evenLengthNonHex = "ZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZ"

// TestHandleCreateMirrorUsagePoint_NonHexEvenLengthDeviceLFDIRejected is the
// POST half of that coverage gap.
func TestHandleCreateMirrorUsagePoint_NonHexEvenLengthDeviceLFDIRejected(t *testing.T) {
	t.Parallel()
	const caller = "AABBCCDDEEFF00112233445566778899AABBCCDD"
	s := memory.NewStore[sep2.MirrorUsagePoint]()
	mux := http.NewServeMux()
	mux.HandleFunc("POST /mup", metering.HandleCreateMirrorUsagePoint(s, nil, identityProvider(caller), nil))

	mup := sep2.MirrorUsagePoint{MRID: "INV_NONHEX", DeviceLFDI: evenLengthNonHex}
	body, _ := xml.Marshal(&mup)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/mup", bytes.NewReader(body)))

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (deviceLFDI is even length but not hex); body: %s", w.Code, w.Body.String())
	}
	if count, _ := s.Count(context.Background()); count != 0 {
		t.Errorf("stored count = %d, want 0", count)
	}
}

// TestHandlePutMirrorUsagePoint_NonHexEvenLengthDeviceLFDIRejected is the PUT
// half.
func TestHandlePutMirrorUsagePoint_NonHexEvenLengthDeviceLFDIRejected(t *testing.T) {
	t.Parallel()
	mupStore := memory.NewStore[sep2.MirrorUsagePoint]()
	mmrStore := memory.NewScopedStore[sep2.MirrorMeterReading]()
	idA := seedDerivedMirror(t, mupStore, putOwnerLFDI, "MUP_A", "original A")

	mux := mirrorInstanceMux(mupStore, mmrStore, putOwnerLFDI)
	req := httptest.NewRequest(http.MethodPut, "/mup/"+idA,
		bytes.NewReader(mupWireBody("MUP_A", "probe", evenLengthNonHex)))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (deviceLFDI is even length but not hex); body: %s", w.Code, w.Body.String())
	}
	assertMirrorUnchanged(t, mupStore, idA, "MUP_A", putOwnerLFDI, "original A")
}

// TestHandleCreateMirrorUsagePoint_WhitespacePaddedSelfClaimAccepted: XSD's
// hexBinary type carries whiteSpace facet "collapse" (leading and trailing
// whitespace stripped, internal runs collapsed), so a claim like
// " 00AABB...\n" is a schema-valid document naming the same value as the
// trimmed one. Refusing it with 400 would be this server inventing a
// stricter grammar than the standard's own type.
func TestHandleCreateMirrorUsagePoint_WhitespacePaddedSelfClaimAccepted(t *testing.T) {
	t.Parallel()
	const caller = "AABBCCDDEEFF00112233445566778899AABBCCDD"
	s := memory.NewStore[sep2.MirrorUsagePoint]()
	mux := http.NewServeMux()
	mux.HandleFunc("POST /mup", metering.HandleCreateMirrorUsagePoint(s, nil, identityProvider(caller), nil))

	mup := sep2.MirrorUsagePoint{MRID: "INV_WS", DeviceLFDI: " " + caller + "\n"}
	body, err := xml.Marshal(&mup)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/mup", bytes.NewReader(body)))

	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (whitespace-collapsed self-claim); body: %s", w.Code, w.Body.String())
	}
	loc := w.Header().Get("Location")
	stored, err := s.Get(context.Background(), strings.TrimPrefix(loc, "/mup/"))
	if err != nil {
		t.Fatalf("get stored MirrorUsagePoint: %v", err)
	}
	if stored.DeviceLFDI != caller {
		t.Errorf("stored DeviceLFDI = %q, want %q (whitespace trimmed)", stored.DeviceLFDI, caller)
	}
}

// TestHandlePutMirrorUsagePoint_WhitespaceOnlyDeviceLFDITreatedAsAbsent: a
// claim that is whitespace ONLY collapses to the empty string under the same
// XSD facet, which is indistinguishable from an absent element once
// collapsed; it must default to the stored device (#720's PUT rule), not be
// refused as malformed.
func TestHandlePutMirrorUsagePoint_WhitespaceOnlyDeviceLFDITreatedAsAbsent(t *testing.T) {
	t.Parallel()
	mupStore := memory.NewStore[sep2.MirrorUsagePoint]()
	mmrStore := memory.NewScopedStore[sep2.MirrorMeterReading]()
	idA := seedDerivedMirror(t, mupStore, putOwnerLFDI, "MUP_A", "original A")

	mux := mirrorInstanceMux(mupStore, mmrStore, putOwnerLFDI)
	req := httptest.NewRequest(http.MethodPut, "/mup/"+idA,
		bytes.NewReader(mupWireBody("MUP_A", "updated", "   ")))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204 (whitespace-only deviceLFDI collapses to absent); body: %s", w.Code, w.Body.String())
	}
	stored, err := mupStore.Get(context.Background(), idA)
	if err != nil {
		t.Fatalf("get stored MirrorUsagePoint: %v", err)
	}
	if stored.DeviceLFDI != putOwnerLFDI {
		t.Errorf("stored DeviceLFDI = %q, want %q unchanged", stored.DeviceLFDI, putOwnerLFDI)
	}
}

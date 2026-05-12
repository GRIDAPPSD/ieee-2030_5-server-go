// Package sep2_test covers Event-base XML round-tripping for the
// IEEE-044a additions: `replyTo` and `responseRequired` child elements
// per IEEE 2030.5 §10.1.3 (Event rules) / 2023 XSD `RespondableResource`.
//
// These tests gate the IEEE-044 hook-wiring work: the OnTransition hook
// reads `ReplyTo` off a decoded DERControl to drive `(*SEP2Client).
// PostResponse`, and reads the `ResponseRequired` bitmap to decide
// which transition statuses warrant a Response POST. If either field
// fails round-trip, IEEE-044's downstream logic is wrong.
package sep2_test

import (
	"encoding/xml"
	"strings"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-go/pkg/sep2"
)

// TestEventReplyToRoundTrip asserts that a DERControl marshalled with
// ReplyTo set survives an unmarshal-marshal-unmarshal cycle without
// losing or mutating the URI. Both relative ("/edev/1/rsps/1/rsp")
// and absolute ("https://example/rsp") forms are exercised — the
// IEEE-044 hook resolves both via SEP2Client.resolveServerURL.
func TestEventReplyToRoundTrip(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		replyTo string
	}{
		{name: "relative_uri", replyTo: "/edev/1/rsps/1/rsp"},
		{name: "absolute_uri", replyTo: "https://example.invalid/rsps/1/rsp"},
		{name: "empty_omits_element", replyTo: ""},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctrl := sep2.DERControl{}
			ctrl.MRID = "DERC-001"
			ctrl.ReplyTo = tc.replyTo

			data, err := xml.Marshal(&ctrl)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}

			if tc.replyTo == "" {
				if strings.Contains(string(data), "replyTo") {
					t.Errorf("empty ReplyTo should be omitted via omitempty; got XML=%s", string(data))
				}
				return
			}

			if !strings.Contains(string(data), "<replyTo>"+tc.replyTo+"</replyTo>") {
				t.Errorf("ReplyTo element missing or wrong; got XML=%s", string(data))
			}

			var decoded sep2.DERControl
			if err := xml.Unmarshal(data, &decoded); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if decoded.ReplyTo != tc.replyTo {
				t.Errorf("ReplyTo round-trip = %q, want %q", decoded.ReplyTo, tc.replyTo)
			}
		})
	}
}

// TestEventResponseRequiredRoundTrip asserts the HexBinary8 bitmap
// (Table 32 — bits select which transition statuses require a
// Response POST) round-trips through xml.Marshal/Unmarshal. The
// IEEE-044 hook ANDs this mask against the transition status to
// decide whether to call PostResponse.
func TestEventResponseRequiredRoundTrip(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		mask uint8
	}{
		{name: "all_bits_clear", mask: 0x00},
		{name: "bit0_received_only", mask: 0x01},
		{name: "bits0_1_2_received_started_completed", mask: 0x07},
		{name: "all_bits_set", mask: 0xFF},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			mask := tc.mask
			ctrl := sep2.DERControl{}
			ctrl.MRID = "DERC-002"
			ctrl.ResponseRequired = &mask

			data, err := xml.Marshal(&ctrl)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			if !strings.Contains(string(data), "<responseRequired>") {
				t.Errorf("responseRequired element missing; got XML=%s", string(data))
			}

			var decoded sep2.DERControl
			if err := xml.Unmarshal(data, &decoded); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if decoded.ResponseRequired == nil {
				t.Fatalf("ResponseRequired nil after round-trip")
			}
			if *decoded.ResponseRequired != tc.mask {
				t.Errorf("ResponseRequired round-trip = %#x, want %#x", *decoded.ResponseRequired, tc.mask)
			}
		})
	}
}

// TestEventResponseRequiredOmitEmpty verifies that a DERControl with
// no ResponseRequired pointer set marshals WITHOUT a <responseRequired>
// element. This guards the omitempty contract — pre-2023 servers and
// clients must not see an unknown element.
func TestEventResponseRequiredOmitEmpty(t *testing.T) {
	t.Parallel()

	ctrl := sep2.DERControl{}
	ctrl.MRID = "DERC-003"

	data, err := xml.Marshal(&ctrl)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(data), "responseRequired") {
		t.Errorf("responseRequired should be omitted when nil; got XML=%s", string(data))
	}
}

// TestEventElementOrder asserts the XSD-required element order:
// replyTo, responseRequired come BEFORE mRID, and EventStatus comes
// AFTER mRID. The encoding/xml package marshals fields in struct
// declaration order; this test is the on-the-wire sanity check that
// the struct layout has not been reordered by a future refactor.
func TestEventElementOrder(t *testing.T) {
	t.Parallel()

	mask := uint8(0x07)
	ctrl := sep2.DERControl{}
	ctrl.MRID = "DERC-ORDER"
	ctrl.ReplyTo = "/rsp"
	ctrl.ResponseRequired = &mask
	ctrl.EventStatus = &sep2.EventStatus{CurrentStatus: 1, DateTime: 1000}

	data, err := xml.Marshal(&ctrl)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	xmlStr := string(data)

	posReplyTo := strings.Index(xmlStr, "<replyTo>")
	posRespReq := strings.Index(xmlStr, "<responseRequired>")
	posMRID := strings.Index(xmlStr, "<mRID>")
	posEvtStatus := strings.Index(xmlStr, "<EventStatus>")

	if posReplyTo < 0 || posRespReq < 0 || posMRID < 0 || posEvtStatus < 0 {
		t.Fatalf("missing element(s): replyTo=%d responseRequired=%d mRID=%d EventStatus=%d\nXML=%s",
			posReplyTo, posRespReq, posMRID, posEvtStatus, xmlStr)
	}
	if !(posReplyTo < posRespReq && posRespReq < posMRID && posMRID < posEvtStatus) {
		t.Errorf("XSD element order violated: replyTo=%d responseRequired=%d mRID=%d EventStatus=%d (want strictly increasing)\nXML=%s",
			posReplyTo, posRespReq, posMRID, posEvtStatus, xmlStr)
	}
}

// TestDERControlCopyResponseRequiredDeepCopy asserts that DERControl.Copy
// produces an independent ResponseRequired pointer — mutating the copy
// must not change the original. The IEEE-044 hook may stash a copy of
// the active DERControl and mutate the bitmap during retry-policy
// evaluation; without deep-copy, the canonical event in the store
// would silently change.
func TestDERControlCopyResponseRequiredDeepCopy(t *testing.T) {
	t.Parallel()

	mask := uint8(0x07)
	ctrl := sep2.DERControl{}
	ctrl.MRID = "DERC-COPY"
	ctrl.ResponseRequired = &mask

	copied := ctrl.Copy()
	if copied.ResponseRequired == nil {
		t.Fatalf("copy ResponseRequired = nil, want non-nil clone")
	}
	if copied.ResponseRequired == ctrl.ResponseRequired {
		t.Errorf("copy ResponseRequired shares pointer with original; want independent allocation")
	}
	*copied.ResponseRequired = 0xFF
	if *ctrl.ResponseRequired != 0x07 {
		t.Errorf("original ResponseRequired mutated to %#x after copy mutation; deep-copy violated", *ctrl.ResponseRequired)
	}
}

// TestEndDeviceControlCopyResponseRequiredDeepCopy asserts the same
// deep-copy contract for the DRLC EndDeviceControl subtype.
func TestEndDeviceControlCopyResponseRequiredDeepCopy(t *testing.T) {
	t.Parallel()

	mask := uint8(0x03)
	ctrl := sep2.EndDeviceControl{}
	ctrl.MRID = "EDC-COPY"
	ctrl.ResponseRequired = &mask

	copied := ctrl.Copy()
	if copied.ResponseRequired == nil {
		t.Fatalf("copy ResponseRequired = nil, want non-nil clone")
	}
	if copied.ResponseRequired == ctrl.ResponseRequired {
		t.Errorf("copy ResponseRequired shares pointer with original")
	}
	*copied.ResponseRequired = 0xFF
	if *ctrl.ResponseRequired != 0x03 {
		t.Errorf("original mutated to %#x", *ctrl.ResponseRequired)
	}
}

// TestTextMessageCopyResponseRequiredDeepCopy asserts deep-copy on the
// Messaging function set's TextMessage subtype.
func TestTextMessageCopyResponseRequiredDeepCopy(t *testing.T) {
	t.Parallel()

	mask := uint8(0x05)
	tm := sep2.TextMessage{}
	tm.MRID = "TM-COPY"
	tm.ResponseRequired = &mask

	copied := tm.Copy()
	if copied.ResponseRequired == nil {
		t.Fatalf("copy ResponseRequired = nil")
	}
	if copied.ResponseRequired == tm.ResponseRequired {
		t.Errorf("copy ResponseRequired shares pointer with original")
	}
	*copied.ResponseRequired = 0xFF
	if *tm.ResponseRequired != 0x05 {
		t.Errorf("original mutated to %#x", *tm.ResponseRequired)
	}
}

// TestFlowReservationResponseCopyResponseRequiredDeepCopy asserts
// deep-copy on the Flow Reservation function set's
// FlowReservationResponse subtype.
func TestFlowReservationResponseCopyResponseRequiredDeepCopy(t *testing.T) {
	t.Parallel()

	mask := uint8(0x01)
	frp := sep2.FlowReservationResponse{}
	frp.MRID = "FRP-COPY"
	frp.ResponseRequired = &mask

	copied := frp.Copy()
	if copied.ResponseRequired == nil {
		t.Fatalf("copy ResponseRequired = nil")
	}
	if copied.ResponseRequired == frp.ResponseRequired {
		t.Errorf("copy ResponseRequired shares pointer with original")
	}
	*copied.ResponseRequired = 0xFF
	if *frp.ResponseRequired != 0x01 {
		t.Errorf("original mutated to %#x", *frp.ResponseRequired)
	}
}

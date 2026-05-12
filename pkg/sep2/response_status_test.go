// Package sep2_test pins the ResponseStatus* named constants to their
// IEEE 2030.5-2023 §10.10 Table 31 wire values.
//
// IEEE-044a renumbered the constants because the pre-existing values
// were off-by-one against Table 31 (EventReceived was 0 instead of 1,
// EventCancelled was 5 instead of 6). The CSIP server-side test
// `test/csip/core_022_responses_test.go` had to use raw uint8 literals
// 1/2/3/6 to bypass the wrong constants. After IEEE-044a the constants
// match the wire values and that test uses the named constants.
//
// This test exists to prevent a future regression — if anyone changes
// a constant's value, this test fails with a clear pointer back to
// Table 31. Wire values are NORMATIVE and breaking them silently
// corrupts every Response POST on the network.
package sep2_test

import (
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-go/pkg/sep2"
)

func TestResponseStatusWireValues(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		got      uint8
		wantWire uint8
	}{
		{name: "EventReserved", got: sep2.ResponseStatusEventReserved, wantWire: 0},
		{name: "EventReceived", got: sep2.ResponseStatusEventReceived, wantWire: 1},
		{name: "EventStarted", got: sep2.ResponseStatusEventStarted, wantWire: 2},
		{name: "EventCompleted", got: sep2.ResponseStatusEventCompleted, wantWire: 3},
		{name: "OptOut", got: sep2.ResponseStatusOptOut, wantWire: 4},
		{name: "OptIn", got: sep2.ResponseStatusOptIn, wantWire: 5},
		{name: "EventCancelled", got: sep2.ResponseStatusEventCancelled, wantWire: 6},
		{name: "EventSuperseded", got: sep2.ResponseStatusEventSuperseded, wantWire: 7},
		{name: "EventPartialOptOut", got: sep2.ResponseStatusEventPartialOptOut, wantWire: 8},
		{name: "EventPartialOptIn", got: sep2.ResponseStatusEventPartialOptIn, wantWire: 9},
		{name: "EventCompletedNoApply", got: sep2.ResponseStatusEventCompletedNoApply, wantWire: 10},
		{name: "EventAcknowledged", got: sep2.ResponseStatusEventAcknowledged, wantWire: 11},
		{name: "EventCannotBeDisplayed", got: sep2.ResponseStatusEventCannotBeDisplayed, wantWire: 12},
		{name: "EventSupersededAlternateServer", got: sep2.ResponseStatusEventSupersededAlternateServer, wantWire: 13},
		{name: "EventSupersededAlternateProgram", got: sep2.ResponseStatusEventSupersededAlternateProgram, wantWire: 14},
		{name: "EventResumed", got: sep2.ResponseStatusEventResumed, wantWire: 15},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if tc.got != tc.wantWire {
				t.Errorf("ResponseStatus%s = %d, want Table 31 wire value %d", tc.name, tc.got, tc.wantWire)
			}
		})
	}
}

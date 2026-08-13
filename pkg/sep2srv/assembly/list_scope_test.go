package assembly_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/assembly"
)

// Scoped list routes bind to the parent wildcard their pattern declares.
//
// scopedListHandler hardcoded r.PathValue("id"). Two mounts declare no {id}:
// GET /upt/{uptId}/mr and GET /msg/{msgId}/tm. PathValue on an undeclared
// wildcard returns "" instead of failing, so both lists scoped every lookup
// under the empty parent and served an empty list, with a 200, forever, for
// every usage point and every messaging program.
//
// # Why every test below seeds TWO parents
//
// The obvious test seeds one parent and asserts its list is non-empty. That
// test passes against the fix AND against a handler that ignores scoping and
// returns every member across every parent, which is the strictly worse
// failure: an empty list discloses nothing, a cross-parent list discloses
// another program's messages or another usage point's readings. So each test
// seeds two parents with distinguishable members and asserts BOTH directions:
// each list carries its own member, and does not carry the other's. Only the
// pair separates "correctly scoped" from "not scoped at all".
//
// The third case, a parent that legitimately has no members, is asserted
// separately: the defect was a list that is ALWAYS empty, and a fix that turned
// a genuinely empty list into a 404 or a 500 would trade one wrong answer for
// another.

// TestTextMessageList_ScopeBindsToTheMessagingProgramInThePath asserts the
// {msgId}-scoped list serves its own program's messages and no other's.
func TestTextMessageList_ScopeBindsToTheMessagingProgramInThePath(t *testing.T) {
	t.Parallel()

	// tmServer is reused rather than duplicated: it is the same fully wired
	// router with the same stores, and the messaging family is one of the two
	// shapes this card fixes.
	srv, _ := tmServer(t)

	oneLoc := postTextMessage(t, srv, "m1", "for program one only")
	twoLoc := postTextMessage(t, srv, "m2", "for program two only")

	cases := []struct {
		msgID       string
		wantBody    string
		wantHref    string
		foreignBody string
	}{
		{msgID: "m1", wantBody: "for program one only", wantHref: oneLoc, foreignBody: "for program two only"},
		{msgID: "m2", wantBody: "for program two only", wantHref: twoLoc, foreignBody: "for program one only"},
	}

	for _, tc := range cases {
		t.Run(tc.msgID, func(t *testing.T) {
			resp, err := http.Get(srv.URL + "/msg/" + tc.msgID + "/tm")
			if err != nil {
				t.Fatalf("GET /msg/%s/tm: %v", tc.msgID, err)
			}
			if resp.StatusCode != http.StatusOK {
				resp.Body.Close()
				t.Fatalf("GET /msg/%s/tm status = %d, want 200", tc.msgID, resp.StatusCode)
			}

			var list sep2.TextMessageList
			decodeXML(t, resp, &list)

			if len(list.TextMessage) != 1 {
				t.Fatalf("GET /msg/%s/tm returned %d members, want exactly 1: 0 means the list is scoped by the empty parent, more than 1 means it is not scoped at all",
					tc.msgID, len(list.TextMessage))
			}
			got := list.TextMessage[0]
			if got.TextBody != tc.wantBody {
				t.Errorf("member textMessage = %q, want %q", got.TextBody, tc.wantBody)
			}
			if got.TextBody == tc.foreignBody {
				t.Errorf("GET /msg/%s/tm served another program's message %q", tc.msgID, got.TextBody)
			}
			if got.Href != tc.wantHref {
				t.Errorf("member href = %q, want %q (the href the POST minted under this program)", got.Href, tc.wantHref)
			}
			if !strings.HasPrefix(got.Href, "/msg/"+tc.msgID+"/") {
				t.Errorf("member href %q is not under /msg/%s/", got.Href, tc.msgID)
			}
			if want := uint32(1); list.All != want || list.Results != want {
				t.Errorf("all=%d results=%d, want %d and %d: the counts must agree with the scoped member set",
					list.All, list.Results, want, want)
			}
		})
	}
}

// TestTextMessageList_EmptyProgramIsAnEmptyList: a program with no messages
// still answers 200 with a well-formed empty list. The defect was a list that
// is always empty; a genuinely empty list is a correct answer and must not
// become an error.
func TestTextMessageList_EmptyProgramIsAnEmptyList(t *testing.T) {
	t.Parallel()

	srv, _ := tmServer(t)
	// A sibling program HAS a message, so this assertion is not vacuous: it
	// distinguishes "this program is empty" from "the store is empty".
	postTextMessage(t, srv, "m1", "somewhere else entirely")

	resp, err := http.Get(srv.URL + "/msg/m-empty/tm")
	if err != nil {
		t.Fatalf("GET /msg/m-empty/tm: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		t.Fatalf("status = %d, want 200: an empty list is a valid answer, not an error", resp.StatusCode)
	}

	var list sep2.TextMessageList
	decodeXML(t, resp, &list)

	if len(list.TextMessage) != 0 {
		t.Fatalf("got %d members under a program nothing was posted to, want 0", len(list.TextMessage))
	}
	if list.All != 0 || list.Results != 0 {
		t.Errorf("all=%d results=%d, want 0 and 0", list.All, list.Results)
	}
}

// seedMeterReading stores one MeterReading under a usage point. Server-side
// MeterReadings have no POST route in this package (the mirror family writes
// MirrorMeterReadings to a different store), so the store is seeded directly.
func seedMeterReading(t *testing.T, stores *assembly.Stores, uptID, mrID, description string) {
	t.Helper()

	mr := sep2.MeterReading{
		SubscribableResource: sep2.SubscribableResource{
			Resource: sep2.Resource{Href: "/upt/" + uptID + "/mr/" + mrID},
		},
		MRID:        mrID,
		Description: description,
	}
	if err := stores.MeterReadings.Create(context.Background(), uptID, mrID, mr); err != nil {
		t.Fatalf("seeding MeterReading %s under %s: %v", mrID, uptID, err)
	}
}

// TestMeterReadingList_ScopeBindsToTheUsagePointInThePath asserts the
// {uptId}-scoped list serves its own usage point's readings and no other's.
//
// This is the metering half of the same defect. It matters more than the
// messaging half: a MeterReading list leaking across usage points is one
// customer's metering data served under another customer's usage point.
func TestMeterReadingList_ScopeBindsToTheUsagePointInThePath(t *testing.T) {
	t.Parallel()

	srv, stores := tmServer(t)
	seedMeterReading(t, stores, "u1", "mr1", "usage point one, feeder A")
	seedMeterReading(t, stores, "u2", "mr2", "usage point two, feeder B")

	cases := []struct {
		uptID    string
		wantMRID string
		wantDesc string
		foreign  string
	}{
		{uptID: "u1", wantMRID: "mr1", wantDesc: "usage point one, feeder A", foreign: "mr2"},
		{uptID: "u2", wantMRID: "mr2", wantDesc: "usage point two, feeder B", foreign: "mr1"},
	}

	for _, tc := range cases {
		t.Run(tc.uptID, func(t *testing.T) {
			resp, err := http.Get(srv.URL + "/upt/" + tc.uptID + "/mr")
			if err != nil {
				t.Fatalf("GET /upt/%s/mr: %v", tc.uptID, err)
			}
			if resp.StatusCode != http.StatusOK {
				resp.Body.Close()
				t.Fatalf("GET /upt/%s/mr status = %d, want 200", tc.uptID, resp.StatusCode)
			}

			var list sep2.MeterReadingList
			decodeXML(t, resp, &list)

			if len(list.MeterReading) != 1 {
				t.Fatalf("GET /upt/%s/mr returned %d members, want exactly 1: 0 means the list is scoped by the empty parent, more than 1 means it is not scoped at all",
					tc.uptID, len(list.MeterReading))
			}
			got := list.MeterReading[0]
			if got.MRID != tc.wantMRID {
				t.Errorf("member mRID = %q, want %q", got.MRID, tc.wantMRID)
			}
			if got.MRID == tc.foreign {
				t.Errorf("GET /upt/%s/mr served another usage point's reading %q", tc.uptID, got.MRID)
			}
			if got.Description != tc.wantDesc {
				t.Errorf("member description = %q, want %q", got.Description, tc.wantDesc)
			}
			if want := "/upt/" + tc.uptID + "/mr/" + tc.wantMRID; got.Href != want {
				t.Errorf("member href = %q, want %q", got.Href, want)
			}
			if want := uint32(1); list.All != want || list.Results != want {
				t.Errorf("all=%d results=%d, want %d and %d", list.All, list.Results, want, want)
			}
		})
	}
}

// TestMeterReadingList_EmptyUsagePointIsAnEmptyList: the metering half of the
// genuinely-empty case, with a seeded sibling so the assertion is not vacuous.
func TestMeterReadingList_EmptyUsagePointIsAnEmptyList(t *testing.T) {
	t.Parallel()

	srv, stores := tmServer(t)
	seedMeterReading(t, stores, "u1", "mr1", "somewhere else entirely")

	resp, err := http.Get(srv.URL + "/upt/u-empty/mr")
	if err != nil {
		t.Fatalf("GET /upt/u-empty/mr: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		t.Fatalf("status = %d, want 200: an empty list is a valid answer, not an error", resp.StatusCode)
	}

	var list sep2.MeterReadingList
	decodeXML(t, resp, &list)

	if len(list.MeterReading) != 0 {
		t.Fatalf("got %d members under a usage point nothing was seeded to, want 0", len(list.MeterReading))
	}
	if list.All != 0 || list.Results != 0 {
		t.Errorf("all=%d results=%d, want 0 and 0", list.All, list.Results)
	}
}

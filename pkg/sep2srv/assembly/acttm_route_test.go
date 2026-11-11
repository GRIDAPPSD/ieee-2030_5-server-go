package assembly_test

import (
	"context"
	"encoding/xml"
	"net/http"
	"slices"
	"testing"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/assembly"
)

// seedMessagingFixture stores two programs; m1 holds messages spanning every
// status and m2 holds one active message that must never reach m1's list. It
// returns the ids of m1's active messages.
func seedMessagingFixture(t *testing.T, stores *assembly.Stores) []string {
	t.Helper()
	ctx := context.Background()
	now := time.Now().Unix()

	for _, id := range []string{"m1", "m2"} {
		prog := sep2.MessagingProgram{Primacy: 1}
		prog.Href = "/msg/" + id
		prog.TextMessageListLink = &sep2.ListLink{Href: "/msg/" + id + "/tm"}
		if err := stores.MessagingPrograms.Create(ctx, id, prog); err != nil {
			t.Fatalf("seed program %s: %v", id, err)
		}
	}
	put := func(prog, id string, start int64, dur uint32, status *sep2.EventStatus) {
		m := sep2.TextMessage{TextBody: id}
		m.Href = "/msg/" + prog + "/tm/" + id
		m.CreationTime = start - 100
		m.Interval = &sep2.DateTimeInterval{Start: start, Duration: dur}
		m.EventStatus = status
		if err := stores.TextMessages.Create(ctx, prog, id, m); err != nil {
			t.Fatalf("seed message %s: %v", id, err)
		}
	}
	put("m1", "A2", now-100, 3600, nil)
	put("m1", "A1", now, 3600, nil)
	put("m1", "ENDING", now-600, 600, nil)
	put("m1", "SCHEDULED", now+3600, 600, nil)
	put("m1", "CANCELLED", now-200, 3600, &sep2.EventStatus{CurrentStatus: sep2.EventStatusCancelled})
	put("m1", "SUPERSEDED", now-200, 3600, &sep2.EventStatus{CurrentStatus: sep2.EventStatusSuperseded})
	put("m2", "OTHER", now-100, 3600, nil)
	return []string{"A1", "A2"}
}

func decodeTextMessageList(t *testing.T, raw []byte) sep2.TextMessageList {
	t.Helper()
	var list sep2.TextMessageList
	if err := xml.Unmarshal(raw, &list); err != nil {
		t.Fatalf("decode TextMessageList: %v; body=%s", err, raw)
	}
	return list
}

func tmIDs(list sep2.TextMessageList) []string {
	var ids []string
	for _, m := range list.TextMessage {
		ids = append(ids, m.TextBody)
	}
	return ids
}

// TestActiveTextMessageList_HoldsExactlyTheActiveMessages pins the list at
// the boundaries: starting now is in, ending now is out, and a cancelled or
// superseded message is out, as are another program's messages.
func TestActiveTextMessageList_HoldsExactlyTheActiveMessages(t *testing.T) {
	t.Parallel()
	srv, stores := tmServer(t)
	want := seedMessagingFixture(t, stores)

	status, raw := getBody(t, srv, "/msg/m1/acttm")
	if status != http.StatusOK {
		t.Fatalf("GET /msg/m1/acttm status = %d, want 200; body=%s", status, raw)
	}
	list := decodeTextMessageList(t, raw)
	if got := tmIDs(list); !slices.Equal(got, want) {
		t.Errorf("active messages = %v, want %v", got, want)
	}
	if list.Href != "/msg/m1/acttm" {
		t.Errorf("list href = %q, want /msg/m1/acttm", list.Href)
	}
	if list.All != 2 || list.Results != 2 {
		t.Errorf("all=%d results=%d, want 2 and 2", list.All, list.Results)
	}
}

// TestActiveTextMessageList_PagesTheFilteredCollection proves start and limit
// cut the active members, not the whole store.
func TestActiveTextMessageList_PagesTheFilteredCollection(t *testing.T) {
	t.Parallel()
	srv, stores := tmServer(t)
	seedMessagingFixture(t, stores)

	status, raw := getBody(t, srv, "/msg/m1/acttm?s=1&l=5")
	if status != http.StatusOK {
		t.Fatalf("status = %d; body=%s", status, raw)
	}
	list := decodeTextMessageList(t, raw)
	if got := tmIDs(list); !slices.Equal(got, []string{"A2"}) || list.All != 2 || list.Results != 1 {
		t.Errorf("page = %v all=%d results=%d, want [A2] 2 1", got, list.All, list.Results)
	}
}

// TestActiveTextMessageList_EmptyProgramIsAnEmptyList: a program with no
// active message is a 200 with all=0, not a 404.
func TestActiveTextMessageList_EmptyProgramIsAnEmptyList(t *testing.T) {
	t.Parallel()
	srv, stores := tmServer(t)
	seedMessagingFixture(t, stores)
	prog := sep2.MessagingProgram{Primacy: 1}
	prog.Href = "/msg/m3"
	if err := stores.MessagingPrograms.Create(context.Background(), "m3", prog); err != nil {
		t.Fatal(err)
	}

	status, raw := getBody(t, srv, "/msg/m3/acttm")
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", status, raw)
	}
	if list := decodeTextMessageList(t, raw); list.All != 0 || list.Results != 0 || len(list.TextMessage) != 0 {
		t.Errorf("all=%d results=%d members=%d, want an empty list", list.All, list.Results, len(list.TextMessage))
	}
}

// TestMessagingProgram_AdvertisesActiveTextMessageListLink proves the link is
// served on the instance and on the list, with all counting the active
// messages the link's own list serves, and that the link resolves.
func TestMessagingProgram_AdvertisesActiveTextMessageListLink(t *testing.T) {
	t.Parallel()
	srv, stores := tmServer(t)
	seedMessagingFixture(t, stores)

	status, raw := getBody(t, srv, "/msg/m1")
	if status != http.StatusOK {
		t.Fatalf("GET /msg/m1 status = %d; body=%s", status, raw)
	}
	var prog sep2.MessagingProgram
	if err := xml.Unmarshal(raw, &prog); err != nil {
		t.Fatalf("decode: %v; body=%s", err, raw)
	}
	if prog.ActiveTextMessageListLink == nil {
		t.Fatalf("ActiveTextMessageListLink absent; body=%s", raw)
	}
	if got := prog.ActiveTextMessageListLink; got.Href != "/msg/m1/acttm" || got.All != 2 {
		t.Errorf("link = %+v, want href /msg/m1/acttm all 2", got)
	}
	if prog.TextMessageListLink == nil || prog.TextMessageListLink.Href != "/msg/m1/tm" {
		t.Errorf("TextMessageListLink = %+v, want it unchanged", prog.TextMessageListLink)
	}

	_, raw = getBody(t, srv, "/msg")
	var progs sep2.MessagingProgramList
	if err := xml.Unmarshal(raw, &progs); err != nil {
		t.Fatalf("decode list: %v; body=%s", err, raw)
	}
	counts := map[string]uint32{}
	for _, p := range progs.MessagingProgram {
		if p.ActiveTextMessageListLink != nil {
			counts[p.Href] = p.ActiveTextMessageListLink.All
		}
	}
	if counts["/msg/m1"] != 2 || counts["/msg/m2"] != 1 || len(counts) != 2 {
		t.Errorf("per-program active counts = %v, want m1=2 m2=1", counts)
	}

	status, _ = getBody(t, srv, prog.ActiveTextMessageListLink.Href)
	if status != http.StatusOK {
		t.Errorf("GET %s status = %d, want 200", prog.ActiveTextMessageListLink.Href, status)
	}
}

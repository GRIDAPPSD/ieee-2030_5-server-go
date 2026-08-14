package messaging_test

import (
	"bytes"
	"context"
	"encoding/xml"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/messaging"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

// TestHandlePostTextMessage_CarriesCreationTime asserts the stored TextMessage
// gets a server-assigned creationTime.
//
// TextMessage embeds RandomizableEvent, so it inherits Event's required
// creationTime, and this handler is a core construct path: it is one of the two
// places core mints an Event-derived resource. The field has no omitempty, so an
// unset value serves a parseable <creationTime>0</creationTime> rather than
// vanishing, which makes the gap quiet instead of loud. A client resolving two
// overlapping equal-primacy messages compares creationTime to pick the newer
// one; with both at 0 neither wins and the incoming one is discarded.
//
// The assertion is on the value, not on element presence: presence alone passes
// against a handler with no producer at all.
func TestHandlePostTextMessage_CarriesCreationTime(t *testing.T) {
	t.Parallel()
	tmStore := memory.NewScopedStore[sep2.TextMessage]()

	mux := http.NewServeMux()
	mux.HandleFunc("POST /msg/{msgId}/tm", messaging.HandlePostTextMessage(tmStore))

	// A stale client-supplied creationTime must not survive: creationTime is
	// the instant the SERVER created the event, so the server owns it the same
	// way it owns href.
	const forgedTime = int64(1)
	tm := sep2.TextMessage{
		TextBody: "Alert: high voltage",
		Priority: sep2.PriorityCritical,
	}
	tm.CreationTime = forgedTime
	tm.Href = "/msg/VICTIM/tm/forged"
	body, err := xml.Marshal(&tm)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}

	before := time.Now().Unix()
	req := httptest.NewRequest(http.MethodPost, "/msg/msg1/tm", bytes.NewReader(body))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	after := time.Now().Unix()

	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201, body: %s", w.Code, w.Body.String())
	}

	stored, err := tmStore.List(context.Background(), "msg1", store.ListOptions{Limit: 10})
	if err != nil {
		t.Fatalf("list stored text messages: %v", err)
	}
	if len(stored.Items) != 1 {
		t.Fatalf("stored text message count = %d, want 1", len(stored.Items))
	}
	got := stored.Items[0]

	if got.CreationTime == 0 {
		t.Errorf("stored TextMessage CreationTime = 0; a client comparing creationTime to resolve supersession discards the incoming event in both directions")
	}
	if got.CreationTime == forgedTime {
		t.Errorf("stored TextMessage CreationTime = %d, client-supplied value persisted verbatim", got.CreationTime)
	}
	if got.CreationTime < before || got.CreationTime > after {
		t.Errorf("stored TextMessage CreationTime = %d, want server clock in [%d, %d]", got.CreationTime, before, after)
	}
	if !strings.HasPrefix(got.Href, "/msg/msg1/tm/") {
		t.Errorf("stored TextMessage Href = %q, want prefix /msg/msg1/tm/ (server-synthesized)", got.Href)
	}
	// Client-owned payload survives: the server overrides its own fields only.
	if got.TextBody != "Alert: high voltage" {
		t.Errorf("stored TextMessage TextBody = %q, want preserved", got.TextBody)
	}
	if got.Priority != sep2.PriorityCritical {
		t.Errorf("stored TextMessage Priority = %d, want %d preserved", got.Priority, sep2.PriorityCritical)
	}

	// Wire level: the bytes a client parses must not carry the zero value.
	served, err := xml.Marshal(&got)
	if err != nil {
		t.Fatalf("marshal stored text message: %v", err)
	}
	if strings.Contains(string(served), "<creationTime>0</creationTime>") {
		t.Errorf("served TextMessage carries <creationTime>0</creationTime>:\n%s", served)
	}
}

package flow_reservation_test

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/flow_reservation"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

const (
	senderLFDI  = "ABCDEF0123456789ABCDEF0123456789ABCDEF01"
	foreignLFDI = "2222222222222222222222222222222222222222"
)

// ownOnly admits only senderLFDI and records the LFDI it was asked about.
func ownOnly(asked *string) flow_reservation.ResponseSenderAuthorizer {
	return func(_ *http.Request, lfdi string) (bool, string, error) {
		*asked = lfdi
		return lfdi == senderLFDI, senderLFDI, nil
	}
}

func postDERControlResponse(t *testing.T, authorize flow_reservation.ResponseSenderAuthorizer, target, lfdi string) (*httptest.ResponseRecorder, *memory.ScopedStore[sep2.Response]) {
	t.Helper()
	rspStore := memory.NewScopedStore[sep2.Response]()
	mux := http.NewServeMux()
	mux.HandleFunc("POST /rsps/{rspsId}/rsp", flow_reservation.HandlePostResponse(rspStore, authorize))
	st := uint8(1)
	body, err := xml.Marshal(&sep2.DERControlResponse{Response: sep2.Response{EndDeviceLFDI: lfdi, Status: &st, Subject: "0123456789ABCDEF0123456789ABCDEF"}})
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodPost, target, bytes.NewReader(body)))
	return w, rspStore
}

func storedResponses(t *testing.T, rspStore *memory.ScopedStore[sep2.Response]) []sep2.Response {
	t.Helper()
	var all []sep2.Response
	parents, err := rspStore.Parents(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range parents {
		page, err := rspStore.List(context.Background(), p, store.ListOptions{Unbounded: true})
		if err != nil {
			t.Fatal(err)
		}
		all = append(all, page.Items...)
	}
	return all
}

// hexBinary collapses whitespace, so a padded LFDI is the same value; a
// value of the wrong length or with a non-hex character is refused.
func TestPostResponseLFDIForm(t *testing.T) {
	cases := []struct {
		name       string
		lfdi       string
		wantStatus int
	}{
		{"surrounding whitespace is trimmed", " \n\t" + strings.ToLower(senderLFDI) + "\r\n ", http.StatusCreated},
		{"39 hex digits", senderLFDI[:39], http.StatusBadRequest},
		{"41 hex digits", senderLFDI + "A", http.StatusBadRequest},
		{"40 characters with one non-hex", senderLFDI[:39] + "G", http.StatusBadRequest},
		{"40 characters with one lower-case non-hex", strings.ToLower(senderLFDI[:39]) + "g", http.StatusBadRequest},
		{"inner space", senderLFDI[:20] + " " + senderLFDI[21:], http.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var asked string
			w, rspStore := postDERControlResponse(t, ownOnly(&asked), "/rsps/1/rsp", tc.lfdi)
			if w.Code != tc.wantStatus {
				t.Fatalf("status = %d body %q, want %d", w.Code, w.Body.String(), tc.wantStatus)
			}
			got := storedResponses(t, rspStore)
			if tc.wantStatus != http.StatusCreated {
				if len(got) != 0 || asked != "" {
					t.Fatalf("stored %d, authorizer asked %q; want nothing stored and no check", len(got), asked)
				}
				return
			}
			if len(got) != 1 || got[0].EndDeviceLFDI != senderLFDI || asked != senderLFDI {
				t.Fatalf("stored %+v, asked %q; want the canonical LFDI", got, asked)
			}
		})
	}
}

// A check that cannot complete answers 500, and a missing authorizer
// refuses every Response that names a device; neither stores anything.
func TestPostResponseAuthorizerOutcomes(t *testing.T) {
	failing := func(*http.Request, string) (bool, string, error) {
		return false, senderLFDI, errors.New("management store unavailable")
	}
	for _, tc := range []struct {
		name       string
		authorize  flow_reservation.ResponseSenderAuthorizer
		wantStatus int
	}{
		{"authorizer error", failing, http.StatusInternalServerError},
		{"nil authorizer", nil, http.StatusForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w, rspStore := postDERControlResponse(t, tc.authorize, "/rsps/1/rsp", senderLFDI)
			if w.Code != tc.wantStatus {
				t.Fatalf("status = %d body %q, want %d", w.Code, w.Body.String(), tc.wantStatus)
			}
			if got := storedResponses(t, rspStore); len(got) != 0 {
				t.Fatalf("stored %+v, want nothing", got)
			}
		})
	}
}

// The refusal writes one log line naming the sender and the refused LFDI,
// with the request path quoted so an encoded newline cannot start a line.
// Not parallel: it swaps the process-wide log output.
func TestPostResponseRefusalLogLine(t *testing.T) {
	var buf bytes.Buffer
	prevOut, prevFlags := log.Writer(), log.Flags()
	log.SetFlags(0)
	log.SetOutput(&buf)
	t.Cleanup(func() {
		log.SetOutput(prevOut)
		log.SetFlags(prevFlags)
	})

	var asked string
	w, _ := postDERControlResponse(t, ownOnly(&asked), "/rsps/x%0Aforged-line/rsp", foreignLFDI)
	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", w.Code)
	}
	out := buf.String()
	if n := strings.Count(out, "\n"); n != 1 {
		t.Fatalf("got %d log lines, want 1:\n%s", n, out)
	}
	for _, want := range []string{"sender " + senderLFDI, "endDeviceLFDI " + foreignLFDI, `"/rsps/x\nforged-line/rsp"`} {
		if !strings.Contains(out, want) {
			t.Errorf("log line misses %q: %s", want, out)
		}
	}
}

// deleteResponse stores one Response naming lfdi and DELETEs it through
// HandleDeleteResponse with authorize.
func deleteResponse(t *testing.T, authorize flow_reservation.ResponseSenderAuthorizer, lfdi string) (*httptest.ResponseRecorder, *memory.ScopedStore[sep2.Response]) {
	t.Helper()
	rspStore := memory.NewScopedStore[sep2.Response]()
	if err := rspStore.Create(context.Background(), "1", "r", sep2.Response{EndDeviceLFDI: lfdi}); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("DELETE /rsps/{rspsId}/rsp/{rspId}", flow_reservation.HandleDeleteResponse(rspStore, authorize))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodDelete, "/rsps/1/rsp/r", nil))
	return w, rspStore
}

// The authorizer is asked about the STORED LFDI, in canonical form. A refusal,
// a failed check and a missing authorizer each leave the Response in place,
// and a Response naming no device is never offered to the authorizer.
func TestDeleteResponseAuthorizerOutcomes(t *testing.T) {
	failing := func(*http.Request, string) (bool, string, error) {
		return false, senderLFDI, errors.New("management store unavailable")
	}
	cases := []struct {
		name       string
		useOwn     bool
		authorize  flow_reservation.ResponseSenderAuthorizer
		stored     string
		wantStatus int
		wantAsked  string
		wantKept   int
	}{
		{"admitted caller deletes", true, nil, strings.ToLower(senderLFDI), http.StatusNoContent, senderLFDI, 0},
		{"authorizer refuses", true, nil, foreignLFDI, http.StatusForbidden, foreignLFDI, 1},
		{"authorizer error", false, failing, senderLFDI, http.StatusInternalServerError, "", 1},
		{"nil authorizer", false, nil, senderLFDI, http.StatusForbidden, "", 1},
		{"unnamed device is never offered", true, nil, "", http.StatusForbidden, "", 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var asked string
			authorize := tc.authorize
			if tc.useOwn {
				authorize = ownOnly(&asked)
			}
			w, rspStore := deleteResponse(t, authorize, tc.stored)
			if w.Code != tc.wantStatus || asked != tc.wantAsked {
				t.Fatalf("status %d asked %q, want %d and %q", w.Code, asked, tc.wantStatus, tc.wantAsked)
			}
			if got := storedResponses(t, rspStore); len(got) != tc.wantKept {
				t.Errorf("stored %d Responses, want %d", len(got), tc.wantKept)
			}
		})
	}
}

// faultStore fails the named operations and otherwise is the memory store.
type faultStore struct {
	*memory.ScopedStore[sep2.Response]
	deleteErr error
	// beforeDelete runs ahead of the inner Delete, to remove the row the way
	// a concurrent DELETE would.
	beforeDelete func()
}

func (f *faultStore) Delete(ctx context.Context, parentID, id string) error {
	if f.beforeDelete != nil {
		f.beforeDelete()
	}
	if f.deleteErr != nil {
		return f.deleteErr
	}
	return f.ScopedStore.Delete(ctx, parentID, id)
}

// A store that fails the delete answers 500 and keeps the Response; a row
// that vanishes between the read and the delete answers 404. Neither is 204.
func TestDeleteResponseStoreFailure(t *testing.T) {
	admit := func(*http.Request, string) (bool, string, error) { return true, senderLFDI, nil }
	run := func(t *testing.T, mutate func(*faultStore)) (int, *memory.ScopedStore[sep2.Response]) {
		t.Helper()
		inner := memory.NewScopedStore[sep2.Response]()
		if err := inner.Create(context.Background(), "1", "r", sep2.Response{EndDeviceLFDI: senderLFDI}); err != nil {
			t.Fatal(err)
		}
		fs := &faultStore{ScopedStore: inner}
		mutate(fs)
		mux := http.NewServeMux()
		mux.HandleFunc("DELETE /rsps/{rspsId}/rsp/{rspId}", flow_reservation.HandleDeleteResponse(fs, admit))
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(http.MethodDelete, "/rsps/1/rsp/r", nil))
		return w.Code, inner
	}

	t.Run("a failed delete is 500 and the Response stays", func(t *testing.T) {
		var buf bytes.Buffer
		prevOut, prevFlags := log.Writer(), log.Flags()
		log.SetFlags(0)
		log.SetOutput(&buf)
		defer func() { log.SetOutput(prevOut); log.SetFlags(prevFlags) }()
		code, inner := run(t, func(f *faultStore) { f.deleteErr = errors.New("disk full") })
		if code != http.StatusInternalServerError {
			t.Errorf("status = %d, want 500", code)
		}
		if got := storedResponses(t, inner); len(got) != 1 {
			t.Errorf("stored %d Responses, want the 1 that stayed", len(got))
		}
		if !strings.Contains(buf.String(), "disk full") {
			t.Errorf("log = %q, want the store error", buf.String())
		}
	})

	t.Run("a row gone before the delete is 404", func(t *testing.T) {
		code, inner := run(t, func(f *faultStore) {
			f.beforeDelete = func() {
				if err := f.ScopedStore.Delete(context.Background(), "1", "r"); err != nil {
					t.Error(err)
				}
			}
			f.deleteErr = store.ErrNotFound
		})
		if code != http.StatusNotFound {
			t.Errorf("status = %d, want 404", code)
		}
		if got := storedResponses(t, inner); len(got) != 0 {
			t.Errorf("stored %d Responses, want 0", len(got))
		}
	})
}

// A refusal is logged with its cause, on POST and on DELETE alike, and a
// stored LFDI that is not canonical hex is refused 403 as naming no device.
// Not parallel: it swaps the process-wide log output.
func TestResponseRefusalLogNamesCause(t *testing.T) {
	var buf bytes.Buffer
	prevOut, prevFlags := log.Writer(), log.Flags()
	log.SetFlags(0)
	log.SetOutput(&buf)
	t.Cleanup(func() {
		log.SetOutput(prevOut)
		log.SetFlags(prevFlags)
	})

	var asked string
	for _, tc := range []struct {
		name      string
		do        func() *httptest.ResponseRecorder
		wantCause string
	}{
		{"POST caller not admitted", func() *httptest.ResponseRecorder {
			w, _ := postDERControlResponse(t, ownOnly(&asked), "/rsps/1/rsp", foreignLFDI)
			return w
		}, "caller not admitted"},
		{"POST no identity configured", func() *httptest.ResponseRecorder {
			w, _ := postDERControlResponse(t, nil, "/rsps/1/rsp", senderLFDI)
			return w
		}, "no identity configured"},
		{"DELETE caller not admitted", func() *httptest.ResponseRecorder {
			w, _ := deleteResponse(t, ownOnly(&asked), foreignLFDI)
			return w
		}, "caller not admitted"},
		{"DELETE no identity configured", func() *httptest.ResponseRecorder {
			w, _ := deleteResponse(t, nil, senderLFDI)
			return w
		}, "no identity configured"},
		{"DELETE Response names no device", func() *httptest.ResponseRecorder {
			w, _ := deleteResponse(t, ownOnly(&asked), "")
			return w
		}, "Response names no device"},
		{"DELETE stored LFDI not canonical", func() *httptest.ResponseRecorder {
			w, _ := deleteResponse(t, ownOnly(&asked), "not-an-lfdi")
			return w
		}, "Response names no device"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			buf.Reset()
			asked = ""
			if w := tc.do(); w.Code != http.StatusForbidden {
				t.Fatalf("status = %d, want 403", w.Code)
			}
			out := buf.String()
			if n := strings.Count(out, "\n"); n != 1 {
				t.Fatalf("got %d log lines, want 1:\n%s", n, out)
			}
			if !strings.Contains(out, tc.wantCause) {
				t.Errorf("log line misses cause %q: %s", tc.wantCause, out)
			}
			if asked == "not-an-lfdi" {
				t.Errorf("authorizer was offered a non-canonical LFDI")
			}
		})
	}
}

// Tests for the csiptest package. These exercise the chained-GET helper
// against an in-process httptest.NewTLSServer stub, NOT the spec server
// — the helper is transport-agnostic and these tests confirm that
// invariant. The spec-server integration smoke lives in
// test/csip/handshake_test.go (which also consumes GetDeviceCapability
// to prove the helper is wired into at least one real test).
package csiptest_test

import (
	"context"
	"encoding/xml"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/test/csip/csiptest"
)

// stubDcap returns a minimally-valid DeviceCapability XML body with an
// EndDeviceListLink pointing at the given href. Used by both the
// GetDeviceCapability happy-path test and the WalkLink chain test.
func stubDcap(t *testing.T, edevHref string) []byte {
	t.Helper()
	dcap := sep2.DeviceCapability{
		Resource: sep2.Resource{Href: "/dcap"},
		EndDeviceListLink: &sep2.ListLink{
			Href: edevHref,
			All:  1,
		},
	}
	body, err := xml.Marshal(&dcap)
	if err != nil {
		t.Fatalf("marshal stub dcap: %v", err)
	}
	return body
}

// stubEdevList returns a minimal EndDeviceList with one EndDevice
// carrying the given sFDI/lFDI.
func stubEdevList(t *testing.T, sfdi, lfdi string) []byte {
	t.Helper()
	list := sep2.EndDeviceList{
		ListResource: sep2.ListResource{
			All:     1,
			Results: 1,
		},
		EndDevice: []sep2.EndDevice{{
			SFDI: sfdi,
			LFDI: lfdi,
		}},
	}
	body, err := xml.Marshal(&list)
	if err != nil {
		t.Fatalf("marshal stub edev list: %v", err)
	}
	return body
}

func TestClient_GetDeviceCapability_HappyPath(t *testing.T) {
	t.Parallel()

	mux := http.NewServeMux()
	mux.HandleFunc("/dcap", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/sep+xml")
		_, _ = w.Write(stubDcap(t, "/edev"))
	})
	srv := httptest.NewTLSServer(mux)
	t.Cleanup(srv.Close)

	client := csiptest.NewClient(srv.Client(), srv.URL)

	dcap, err := client.GetDeviceCapability(context.Background())
	if err != nil {
		t.Fatalf("GetDeviceCapability: %v", err)
	}
	if dcap.Href != "/dcap" {
		t.Errorf("dcap.Href = %q, want /dcap", dcap.Href)
	}
	if dcap.EndDeviceListLink == nil || dcap.EndDeviceListLink.Href != "/edev" {
		t.Errorf("dcap.EndDeviceListLink = %+v, want Href=/edev", dcap.EndDeviceListLink)
	}
}

func TestClient_WalkLink_HappyPath(t *testing.T) {
	t.Parallel()

	const (
		wantSFDI = "123456789012"
		wantLFDI = "0123456789ABCDEF0123456789ABCDEF01234567"
	)
	mux := http.NewServeMux()
	mux.HandleFunc("/dcap", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(stubDcap(t, "/edev"))
	})
	mux.HandleFunc("/edev", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(stubEdevList(t, wantSFDI, wantLFDI))
	})
	srv := httptest.NewTLSServer(mux)
	t.Cleanup(srv.Close)

	client := csiptest.NewClient(srv.Client(), srv.URL)
	ctx := context.Background()

	dcap, err := client.GetDeviceCapability(ctx)
	if err != nil {
		t.Fatalf("GetDeviceCapability: %v", err)
	}
	if dcap.EndDeviceListLink == nil {
		t.Fatal("dcap.EndDeviceListLink = nil; stub should have advertised /edev")
	}

	// ListLink → Link conversion at the call site, as documented.
	var list sep2.EndDeviceList
	if err := client.WalkLink(ctx, sep2.Link{Href: dcap.EndDeviceListLink.Href}, &list); err != nil {
		t.Fatalf("WalkLink(/edev): %v", err)
	}
	if len(list.EndDevice) != 1 {
		t.Fatalf("len(EndDevice) = %d, want 1", len(list.EndDevice))
	}
	if list.EndDevice[0].SFDI != wantSFDI {
		t.Errorf("EndDevice[0].SFDI = %q, want %q", list.EndDevice[0].SFDI, wantSFDI)
	}
	if list.EndDevice[0].LFDI != wantLFDI {
		t.Errorf("EndDevice[0].LFDI = %q, want %q", list.EndDevice[0].LFDI, wantLFDI)
	}
}

func TestClient_WalkLink_404(t *testing.T) {
	t.Parallel()

	mux := http.NewServeMux()
	mux.HandleFunc("/edev", func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	})
	srv := httptest.NewTLSServer(mux)
	t.Cleanup(srv.Close)

	client := csiptest.NewClient(srv.Client(), srv.URL)

	var list sep2.EndDeviceList
	err := client.WalkLink(context.Background(), sep2.Link{Href: "/edev"}, &list)
	if err == nil {
		t.Fatal("WalkLink: want error for 404, got nil")
	}
	var statusErr *csiptest.UnexpectedStatusError
	if !errors.As(err, &statusErr) {
		t.Fatalf("WalkLink error = %v, want *UnexpectedStatusError", err)
	}
	if statusErr.Status != http.StatusNotFound {
		t.Errorf("status = %d, want %d", statusErr.Status, http.StatusNotFound)
	}
}

func TestClient_WalkLink_NonXMLBody(t *testing.T) {
	t.Parallel()

	mux := http.NewServeMux()
	mux.HandleFunc("/edev", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("not xml"))
	})
	srv := httptest.NewTLSServer(mux)
	t.Cleanup(srv.Close)

	client := csiptest.NewClient(srv.Client(), srv.URL)

	var list sep2.EndDeviceList
	err := client.WalkLink(context.Background(), sep2.Link{Href: "/edev"}, &list)
	if err == nil {
		t.Fatal("WalkLink: want unmarshal error for non-XML body, got nil")
	}
	// Confirm WalkLink reached the unmarshal step (vs. transport /
	// status / read failure) by checking the wrapping prefix. The
	// stdlib's xml decoder returns either io.EOF or *xml.SyntaxError
	// depending on what the malformed body looks like — both flow
	// through the same wrap path, so we assert on the prefix instead
	// of the leaf type.
	if !strings.Contains(err.Error(), "unmarshal body for") {
		t.Fatalf("WalkLink error = %v, want wrapped unmarshal error", err)
	}
}

func TestClient_WalkLink_EmptyHref_NoDial(t *testing.T) {
	t.Parallel()

	// Counter-backed handler proves WalkLink does NOT touch the wire
	// when Href is empty. If the empty-Href guard regresses, the
	// counter increments and the test fails.
	var hits int64
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&hits, 1)
		w.WriteHeader(http.StatusOK)
	})
	srv := httptest.NewTLSServer(mux)
	t.Cleanup(srv.Close)

	client := csiptest.NewClient(srv.Client(), srv.URL)

	var list sep2.EndDeviceList
	err := client.WalkLink(context.Background(), sep2.Link{Href: ""}, &list)
	if !errors.Is(err, csiptest.ErrEmptyLink) {
		t.Fatalf("WalkLink(empty) = %v, want ErrEmptyLink", err)
	}
	if got := atomic.LoadInt64(&hits); got != 0 {
		t.Errorf("server got %d hits, want 0 (empty link must not dial)", got)
	}
}

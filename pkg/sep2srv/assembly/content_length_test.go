package assembly_test

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/assembly"
)

// seedMirrorUsagePoints inserts n MirrorUsagePoint records whose combined
// XML comfortably exceeds net/http's 2048-byte pre-chunking buffer
// (bufferBeforeChunkingSize, net/http/server.go), so GET /mup?l=<n> is a
// realistic stand-in for the /mup response that broke every EPRI client
// (#613).
func seedMirrorUsagePoints(t *testing.T, stores *assembly.Stores, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("mup-%03d", i)
		mup := sep2.MirrorUsagePoint{
			MRID:        fmt.Sprintf("%024X", i),
			Description: "content-length coverage fixture, padded so each entry contributes real bytes",
			DeviceLFDI:  testLFDI,
		}
		if err := stores.MirrorUsagePoints.Create(context.Background(), id, mup); err != nil {
			t.Fatalf("seed MirrorUsagePoint %s: %v", id, err)
		}
	}
}

// TestAssembly_ListOverChunkThresholdIsLengthFramed is item 2 of the #613
// brief: a protocol response over 2048 bytes must carry Content-Length and
// no Transfer-Encoding. It is written to prove RED against the unwrapped
// router first: seed enough MirrorUsagePoint entries that the raw list body
// exceeds the threshold, request it over a real net/http round trip (a
// ResponseRecorder never exercises net/http's chunking decision), and assert
// on what the client actually received.
func TestAssembly_ListOverChunkThresholdIsLengthFramed(t *testing.T) {
	t.Parallel()
	stores := testStores()
	seedMirrorUsagePoints(t, stores, 40)

	handler, _ := assembly.BuildProtocolRouter(
		assembly.RouterConfig{TZOffset: -28800},
		stores,
		testAuthPolicy(),
		"serverSFDI", "serverLFDI",
		nil,
	)
	srv := httptest.NewServer(handler)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/mup?l=100")
	if err != nil {
		t.Fatalf("GET /mup: %v", err)
	}
	body, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		t.Fatalf("read body: %v", err)
	}

	// Control: this must be over the threshold, or the assertions below
	// never exercise net/http's chunking decision at all.
	if len(body) <= 2048 {
		t.Fatalf("fixture body is %d bytes, want > 2048; the seed does not exercise the chunking path", len(body))
	}

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /mup: want 200, got %d", resp.StatusCode)
	}
	if len(resp.TransferEncoding) != 0 {
		t.Errorf("Transfer-Encoding = %v, want none (body %d bytes)", resp.TransferEncoding, len(body))
	}
	if resp.ContentLength != int64(len(body)) {
		t.Errorf("Content-Length = %d, want %d (actual body size)", resp.ContentLength, len(body))
	}

	// data-invariants Rule 1: assert the actual field values the buffering
	// carried through, not just that the byte count matches. Buffering and
	// re-writing the body is exactly the kind of change that could silently
	// truncate or reorder it while still reporting the right length.
	var list sep2.MirrorUsagePointList
	if err := xml.Unmarshal(body, &list); err != nil {
		t.Fatalf("decode MirrorUsagePointList: %v\nbody: %s", err, body)
	}
	if len(list.MirrorUsagePoint) != 40 {
		t.Fatalf("got %d MirrorUsagePoint entries, want 40", len(list.MirrorUsagePoint))
	}
	if got := list.MirrorUsagePoint[0].MRID; got != fmt.Sprintf("%024X", 0) {
		t.Errorf("entry 0 MRID = %q, want %q", got, fmt.Sprintf("%024X", 0))
	}
	if got := list.MirrorUsagePoint[39].MRID; got != fmt.Sprintf("%024X", 39) {
		t.Errorf("entry 39 MRID = %q, want %q", got, fmt.Sprintf("%024X", 39))
	}
	for i, mup := range list.MirrorUsagePoint {
		if mup.DeviceLFDI != testLFDI {
			t.Errorf("entry %d DeviceLFDI = %q, want %q", i, mup.DeviceLFDI, testLFDI)
		}
	}
}

// TestAssembly_ContentLengthFramingAcrossPaths is item 3: the fix must hold
// for a list, a single resource, and an error response, not only the list
// path that broke. Every case here asserts the response as a client
// receives it over the wire (READ BY THE CONSUMER'S PATH): Content-Length
// present and equal to the body actually read, and no Transfer-Encoding,
// whenever the response carries a body at all.
func TestAssembly_ContentLengthFramingAcrossPaths(t *testing.T) {
	t.Parallel()
	stores := testStores()
	seedMirrorUsagePoints(t, stores, 40)

	handler, _ := assembly.BuildProtocolRouter(
		assembly.RouterConfig{TZOffset: -28800},
		stores,
		testAuthPolicy(),
		"serverSFDI", "serverLFDI",
		nil,
	)
	srv := httptest.NewServer(handler)
	defer srv.Close()

	cases := []struct {
		name       string
		method     string
		path       string
		wantStatus int
	}{
		{"list over threshold", http.MethodGet, "/mup?l=100", http.StatusOK},
		{"single resource under threshold", http.MethodGet, "/dcap", http.StatusOK},
		{"error response", http.MethodGet, "/edev/does-not-exist", http.StatusNotFound},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req, err := http.NewRequest(tc.method, srv.URL+tc.path, nil)
			if err != nil {
				t.Fatalf("build request: %v", err)
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("%s %s: %v", tc.method, tc.path, err)
			}
			body, err := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			if err != nil {
				t.Fatalf("read body: %v", err)
			}

			if resp.StatusCode != tc.wantStatus {
				t.Fatalf("status = %d, want %d; body = %s", resp.StatusCode, tc.wantStatus, body)
			}
			if len(resp.TransferEncoding) != 0 {
				t.Errorf("Transfer-Encoding = %v, want none", resp.TransferEncoding)
			}
			if len(body) > 0 && resp.ContentLength != int64(len(body)) {
				t.Errorf("Content-Length = %d, want %d (actual body size)", resp.ContentLength, len(body))
			}
		})
	}
}

// TestAssembly_NoContentStaysHeaderless proves the fix does not paper a
// Content-Length onto a 204, which would break the "every other header is
// unchanged" invariant: net/http never sends Content-Length for a status
// bodyAllowedForStatus rejects (net/http/transfer.go), and the fix must not
// add one where none was sent before.
func TestAssembly_NoContentStaysHeaderless(t *testing.T) {
	t.Parallel()
	stores := testStores()

	edev := sep2.EndDevice{LFDI: testLFDI}
	if err := stores.EndDevices.Create(context.Background(), "delete-me", edev); err != nil {
		t.Fatalf("seed EndDevice: %v", err)
	}

	handler, _ := assembly.BuildProtocolRouter(
		assembly.RouterConfig{TZOffset: -28800},
		stores,
		testAuthPolicy(),
		"serverSFDI", "serverLFDI",
		nil,
	)
	srv := httptest.NewServer(handler)
	defer srv.Close()

	req, err := http.NewRequest(http.MethodDelete, srv.URL+"/edev/delete-me", nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("DELETE /edev/delete-me: %v", err)
	}
	body, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		t.Fatalf("read body: %v", err)
	}

	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("status = %d, want 204; body = %s", resp.StatusCode, body)
	}
	if len(body) != 0 {
		t.Fatalf("body = %q, want empty", body)
	}
	if cl := resp.Header.Get("Content-Length"); cl != "" {
		t.Errorf("Content-Length = %q, want unset on a 204", cl)
	}
}

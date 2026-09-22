package sep2capture

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
)

// TestHandlerDownloadBytesEqualWhatARealClientSentAndReceived is
// acceptance 4: exact bytes through a real Attach'd server, read back
// through the handler's two download routes.
//
// Mutant (handler.go, handleExchangeDirection): swapping ex.Request.Bytes
// for ex.Response.Bytes on the request route makes this RED: the
// downloaded request bytes would equal the response instead.
func TestHandlerDownloadBytesEqualWhatARealClientSentAndReceived(t *testing.T) {
	m := newMaterial(t)

	const respBody = "traffic-tab-download-acceptance-body"
	addr, st := startCaptureServerWithStore(t, m, okHandler(respBody))

	conn := rawDial(t, m, addr)
	reqBytes := []byte("GET /x HTTP/1.1\r\nHost: example\r\nConnection: close\r\n\r\n")
	if _, err := conn.Write(reqBytes); err != nil {
		t.Fatalf("write request: %v", err)
	}
	respBytes := readRawHTTPMessage(t, conn)

	// waitQueueDrained polls inFlightBytes, which is 0 until something has
	// been handed to Record; the exchange only reaches Record later, off
	// the ConnState hook, so that wait can pass by finding nothing queued
	// yet rather than by finding the queue empty after draining (PR 620
	// review: this raced its own precondition and was CI's actual
	// failure). waitForExchangeCountAnyClient polls the observable result
	// instead.
	sums := waitForExchangeCountAnyClient(t, st, 1)
	id := sums[0].ID

	ts := httptest.NewServer(st.Handler())
	defer ts.Close()

	gotReq := downloadBody(t, ts.URL, id, "request")
	if !bytes.Equal(gotReq, reqBytes) {
		t.Errorf("downloaded request bytes: got %q, want %q", gotReq, reqBytes)
	}

	gotResp := downloadBody(t, ts.URL, id, "response")
	if !bytes.Equal(gotResp, respBytes) {
		t.Errorf("downloaded response bytes: got %q, want %q", gotResp, respBytes)
	}
}

// TestHandlerDownloadHeaders proves the download routes' three required
// headers (Q7 item 4: octet-stream, attachment, nosniff), which the exact
// byte comparison above does not check.
//
// Mutant (handler.go, handleExchangeDirection): dropping the
// X-Content-Type-Options header makes this RED.
func TestHandlerDownloadHeaders(t *testing.T) {
	st := newTestStore(t)
	st.Record(makeExchange(1, 1, "client-a", 32, 32))
	waitQueueDrained(t, st)

	ts := httptest.NewServer(st.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/exchanges/1/request")
	if err != nil {
		t.Fatalf("GET /exchanges/1/request: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, resp.Body)

	if got := resp.Header.Get("Content-Type"); got != "application/octet-stream" {
		t.Errorf("Content-Type: got %q, want application/octet-stream", got)
	}
	if got := resp.Header.Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("X-Content-Type-Options: got %q, want nosniff", got)
	}
	if got := resp.Header.Get("Content-Disposition"); got == "" || !bytes.Contains([]byte(got), []byte("attachment")) {
		t.Errorf("Content-Disposition: got %q, want it to contain attachment", got)
	}
}

func downloadBody(t testing.TB, base string, id uint64, direction string) []byte {
	t.Helper()
	resp, err := http.Get(base + "/exchanges/" + strconv.FormatUint(id, 10) + "/" + direction)
	if err != nil {
		t.Fatalf("GET /exchanges/%d/%s: %v", id, direction, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /exchanges/%d/%s status: got %d, want 200", id, direction, resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return body
}

package assembly_test

import (
	"log"
	"net/http"
	"strings"
	"testing"
)

// TestDERInstance_PUTInvalidXMLDoesNotLeakDecoderDetail pins the 400 path
// convention (#360) at a route mounted through the real protocol router,
// not just against a bare handler: the decoder's own complaint, which can
// quote attacker-supplied content, is a fixed body to the client and the
// detail only to the operator-facing log.
//
// This test cannot run in parallel with its siblings in this package: the log
// redirection is process-wide, and Go finishes every sequential top-level test
// before releasing the parallel ones, so this is the only place a
// process-wide capture is read without a sibling suite writing into it.
func TestDERInstance_PUTInvalidXMLDoesNotLeakDecoderDetail(t *testing.T) {
	const marker = "MARKERXYZ360LEAK"

	buf := &logProbeSafeBuffer{}
	prevFlags, prevOut := log.Flags(), log.Writer()
	log.SetFlags(0)
	log.SetOutput(buf)
	t.Cleanup(func() {
		log.SetOutput(prevOut)
		log.SetFlags(prevFlags)
	})

	srv, stores := derInstanceServer(t)
	seedDER(t, stores, "7", "3")
	path := derHref("7", "3")

	req, err := http.NewRequest(http.MethodPut, srv.URL+path, strings.NewReader("<"+marker+">bar</"+marker+">"))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PUT: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
	body := readBody(t, resp)
	if strings.Contains(string(body), marker) {
		t.Errorf("body = %q; the decoder detail reached the client", body)
	}
	if logged := buf.String(); !strings.Contains(logged, marker) {
		t.Errorf("log = %q; the decoder detail did not reach the operator", logged)
	}
}

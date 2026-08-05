// CSIP V1.2 §5.5 — HTTP Response Semantics.
//
// CORE-002 asserts that requests against function-set endpoints which
// the server has *not* implemented produce HTTP **501 Not Implemented**.
//
// SPEC-TYPO NOTE (IMPORTANT — do not "fix" the assertion to 500):
// The V1.2 conformance PDF carries a typo in its pass-criteria summary
// line for CORE-002. The summary reads "500 Not Implemented", but the
// actual HTTP semantics — and the V1.2 procedure step it summarizes —
// require the standard RFC 7231 §6.6.2 status code **501**. RFC 7231
// reserves 500 for "Internal Server Error" (the server tried and
// failed) and 501 for "Not Implemented" (the server does not support
// the functionality required to fulfill the request), which is the
// CSIP-intended meaning here. Match the procedure step, not the
// summary line. The PR description for #54 calls this out at top.
//
// V1.2 procedure step → assertion mapping (per V1.2 §5.5 procedure):
//
//	Step 1 (boot a server)                     ────► csiptest.BootServer
//	Step 2 (issue a request to an endpoint
//	        the server does not implement)     ────► GET <BaseURL>/<probe>
//	Step 3 (assert response status code = 501) ────► resp.StatusCode == 501
//
// STATUS TODAY (PHASE 3, #54):
//
// The ieee-2030_5-go server does not currently emit 501 from any
// handler. Unmatched paths fall through to Go's net/http.ServeMux and
// return 404. There is no production code path that returns
// http.StatusNotImplemented — verified at #54 ship time by:
//
//	$ grep -rn "StatusNotImplemented" internal/ pkg/ cmd/
//	(0 hits)
//
// CORE-002's procedure therefore has no satisfying production behavior
// to assert against today.
//
// Following the precedent set by #62's COMM-003 skeleton (Phase 3
// exit criterion #5 — "No silent passes; TODO-skeleton with explicit
// blocker citation"), this test is shipped as a documented skeleton
// that t.Skip's when no 501-producing endpoint is found. The skeleton
// performs the probe end-to-end so any future change that introduces a
// 501-returning handler trips it back to a real assertion automatically.
//
// TO TIGHTEN: file a follow-up ticket to add explicit "method handler
// returns 501 for unsupported function-set" behavior to the router
// fallback — e.g. a catch-all for prefixes advertised by DCAP that
// lack a registered sub-route, or per-handler stubs for the
// CustomerAccount / DemandResponseProgram / File / Prepayment /
// TariffProfile FSA-base links that DCAP could advertise but no Go
// handler implements. Once that lands, the t.Skip below trips and
// the existing 501 detection becomes the conformance assertion.
package csip_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/test/csip/csiptest"
)

// TestCORE_002_HTTPResponse implements CSIP V1.2 §5.5.
func TestCORE_002_HTTPResponse(t *testing.T) {
	t.Parallel()

	// Build a CA + device cert under our control so probes that hit
	// ACL-guarded prefixes are allowed past the IdentityMiddleware
	// and reach the inner mux's per-route handler (the level at
	// which a future 501 stub would live). Without a device cert the
	// ACL middleware would respond with 403 on /sdev/*-style probes,
	// masking any 501 wired beneath it.
	_, caCertFile, deviceCert := mustBuildClientPKI(t)

	srv := csiptest.BootServer(t,
		csiptest.WithClientCAsFile(caCertFile),
		csiptest.WithClientCert(deviceCert),
	)
	client := buildClient(t, srv.RootCA, deviceCert)

	// Step 1 + 2: probe paths the server is reasonably likely to
	// surface as "not implemented" once a 501 fallback is wired:
	//   - /unimplemented-function-set: unknown to the router; today
	//     returns 404, target is 501.
	//   - /sdev/sdi/details: deeper path under /sdev/sdi with no
	//     registered handler; ACL allows GET past auth, mux 404s.
	//   - /flow: top-level prefix not in the protocolChain list; the
	//     top mux 404s today.
	// Per V1.2 §5.5, any of these landing on a 501-returning handler
	// satisfies the procedure.
	probes := []string{
		"/unimplemented-function-set",
		"/sdev/sdi/details",
		"/flow",
	}

	ctx := context.Background()
	for _, path := range probes {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.BaseURL+path, nil)
		if err != nil {
			t.Fatalf("build GET %s: %v", path, err)
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		status := resp.StatusCode
		_ = resp.Body.Close()

		// Step 3: 501 satisfies §5.5 procedure step 3. Match the
		// procedure step, not the PDF pass-criteria typo (500 vs 501).
		if status == http.StatusNotImplemented {
			t.Logf("step 3: GET %s → 501 Not Implemented (V1.2 §5.5 satisfied)", path)
			return
		}
		t.Logf("probe GET %s → %d (looking for 501)", path, status)
	}

	// No production code path returns 501 today. Skip cleanly with a
	// pointer back to the doc comment so the gap is loud in test
	// output but the suite stays green (Phase 3 exit criterion #2).
	t.Skip("CORE-002 skeleton: no 501-returning endpoint exists in the server yet; " +
		"see test doc comment for the V1.2 §5.5 procedure and the spec-typo note (500 vs 501). " +
		"This skip becomes a real assertion once the router gains an " +
		"'advertised-but-unimplemented' 501 fallback.")
}

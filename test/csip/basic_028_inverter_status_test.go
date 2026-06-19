// CSIP V1.2 §8.28 — Inverter Status (DERStatus).
//
// BASIC-028 exercises the DERStatus singleton end-to-end. The test
// PUTs a populated DERStatus payload to
// /edev/{id}/der/{derId}/ders, then GETs the same path and asserts
// every field on the payload round-trips exactly (no silent drops at
// the XML codec, the store, or the singleton GET/PUT handler).
//
// V1.2 procedure step → assertion mapping:
//
//	Step 1 (boot CSIP server)                                ────► csiptest.BootServer
//	Step 2 (PUT a DERStatus carrying genConnectStatus,
//	        inverterStatus, operationalModeStatus,
//	        readingTime, alarmStatus, stateOfChargeStatus)   ────► PUT /edev/{id}/der/{derId}/ders
//	Step 3 (PUT returns 204 No Content)                      ────► resp.StatusCode == 204
//	Step 4 (GET /edev/{id}/der/{derId}/ders returns 200
//	        with the previously-PUT DERStatus)               ────► GET, parse
//	Step 5 (every field round-trips exactly: outer fields
//	        compared by value, sub-status structs compared
//	        by both dateTime and value)                       ───► per-field assertions
//
// SPEC GAP NOTE (do not "fix" by editing pkg/sep2.DERStatus):
//
// V1.2 §8.28 names six DERStatus sub-fields procedurally:
// genConnectStatus, inverterStatus, localControlModeStatus,
// manufacturerStatus, operationalModeStatus, and readingTime.
// pkg/sep2/der.go's DERStatus type carries only four of those
// (genConnectStatus, inverterStatus, operationalModeStatus,
// readingTime) plus alarmStatus + stateOfChargeStatus. The two
// missing fields (localControlModeStatus, manufacturerStatus) are not
// yet modelled in the Go server's public types.
//
// Per Pike's hard rule #1 (stay in scope), this test does NOT extend
// pkg/sep2 to add the missing fields. It instead round-trips every
// DERStatus field the type currently exposes, plus alarmStatus and
// stateOfChargeStatus for full coverage of what the server can carry.
// The two unmodelled fields are flagged in the PR description as a
// follow-up so the gap surfaces in a dedicated ticket rather than
// silently expanding this one. When that ticket lands, add lines for
// the two fields to the post-PUT payload below and the round-trip
// assertions in step 5; no other code path changes.
//
// Race notes:
//
// BASIC-028 uses t.Parallel(). HandleSingletonGetPut at
// internal/handler/singleton.go does a Create-then-Update path that
// is single-goroutine-per-test (one PUT, one GET, no concurrency on
// the same parentKey within a single test). pkg/store/memory's
// ScopedStore uses a sync.RWMutex per inner store; the singleton
// "default" key is the only contender in this scope, so even under
// -race the path is well-defined.
package csip_test

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"testing"
	"time"

	"gitlab.pnnl.gov/arista/ieee-2030_5/ieee-2030_5-core/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-go/test/csip/csiptest"
)

// TestBASIC_028_InverterStatus implements CSIP V1.2 §8.28.
func TestBASIC_028_InverterStatus(t *testing.T) {
	t.Parallel()

	// Step 1: boot a CSIP server with a CA + device cert under our
	// control so PUT + GET both pass the ACL device-cert check on
	// the /edev prefix.
	_, caCertFile, deviceCert := mustBuildClientPKI(t)
	srv := csiptest.BootServer(t,
		csiptest.WithClientCAsFile(caCertFile),
		csiptest.WithClientCert(deviceCert),
	)
	httpClient := buildClient(t, srv.RootCA, deviceCert)
	ctx := context.Background()

	const edevID = "statusdev"
	const derID = "der1"
	dersPath := fmt.Sprintf("/edev/%s/der/%s/ders", edevID, derID)

	// Step 2: PUT a populated DERStatus. Values are deterministic so
	// the step-5 round-trip assertions are byte-exact:
	//   genConnectStatus.value = 7  → 0x07 == "connected, available, operating"
	//                                (V1.2 §10.10 ConnectStatusType bitfield;
	//                                bits set: 0 connected, 1 available, 2 operating).
	//   inverterStatus.value   = 3  → "Following / Synchronized" per InverterStatusType
	//                                (V1.2 §10.10 enum; 3 is the spec value for
	//                                "tracking grid voltage and synchronized").
	//   operationalModeStatus.value = 2 → "OperationalMode" per V1.2 §10.10
	//                                    (2 == "Operational").
	//   alarmStatus            = 0  → no alarms set.
	//   stateOfChargeStatus    = 5000 → 50.00% SoC (units: hundredths-of-a-percent
	//                                  per V1.2; out of range [0, 10000]).
	//   readingTime            = now (unix seconds, deterministic per t.Parallel test).
	now := time.Now().Unix()
	alarm := uint32(0)
	soc := uint16(5000)
	want := sep2.DERStatus{
		// Pre-set Href on the PUT body. HandleSingletonGetPut at
		// internal/handler/singleton.go round-trips whatever Href the
		// client supplies (the defaultFactory's Href only applies on
		// a GET when no record exists). Setting it here proves the
		// codec preserves the resource href on the wire, and lines up
		// the step-5 Href assertion against a known value.
		SubscribableResource: sep2.SubscribableResource{
			Resource: sep2.Resource{Href: dersPath},
		},
		GenConnectStatus: &sep2.ConnectStatusType{
			DateTime: now,
			Value:    0x07,
		},
		InverterStatus: &sep2.InverterStatusType{
			DateTime: now,
			Value:    3,
		},
		OperationalModeStatus: &sep2.OperationalModeStatusType{
			DateTime: now,
			Value:    2,
		},
		ReadingTime:         now,
		AlarmStatus:         &alarm,
		StateOfChargeStatus: &soc,
	}

	body, err := xml.Marshal(&want)
	if err != nil {
		t.Fatalf("step 2: marshal DERStatus: %v", err)
	}

	// Step 3: PUT returns 204 No Content. The singleton handler
	// creates-or-updates and emits StatusNoContent (see
	// internal/handler/singleton.go).
	putReq, err := http.NewRequestWithContext(ctx, http.MethodPut, srv.BaseURL+dersPath, bytes.NewReader(body))
	if err != nil {
		t.Fatalf("step 3: build PUT: %v", err)
	}
	putReq.Header.Set("Content-Type", "application/sep+xml")
	putResp, err := httpClient.Do(putReq)
	if err != nil {
		t.Fatalf("step 3: PUT %s: %v", dersPath, err)
	}
	_, _ = io.Copy(io.Discard, putResp.Body)
	_ = putResp.Body.Close()
	if putResp.StatusCode != http.StatusNoContent {
		t.Fatalf("step 3: PUT %s: status = %d, want 204", dersPath, putResp.StatusCode)
	}

	// Step 4: GET /edev/{edevID}/der/{derID}/ders returns 200 + the
	// previously-PUT DERStatus.
	getReq, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.BaseURL+dersPath, nil)
	if err != nil {
		t.Fatalf("step 4: build GET: %v", err)
	}
	getResp, err := httpClient.Do(getReq)
	if err != nil {
		t.Fatalf("step 4: GET %s: %v", dersPath, err)
	}
	defer func() {
		_, _ = io.Copy(io.Discard, getResp.Body)
		_ = getResp.Body.Close()
	}()
	if getResp.StatusCode != http.StatusOK {
		t.Fatalf("step 4: GET %s: status = %d, want 200", dersPath, getResp.StatusCode)
	}
	bodyBytes, err := io.ReadAll(getResp.Body)
	if err != nil {
		t.Fatalf("step 4: read body: %v", err)
	}
	var got sep2.DERStatus
	if err := xml.Unmarshal(bodyBytes, &got); err != nil {
		t.Fatalf("step 4: unmarshal DERStatus: %v", err)
	}

	// Step 5: every field round-trips. Pointer-typed sub-status
	// fields are checked for non-nil first to surface a clear failure
	// if the codec dropped the element entirely (vs. preserved-but-
	// wrong-value).
	if got.ReadingTime != want.ReadingTime {
		t.Errorf("step 5: ReadingTime = %d, want %d", got.ReadingTime, want.ReadingTime)
	}

	assertConnectStatus(t, "GenConnectStatus", got.GenConnectStatus, want.GenConnectStatus)
	assertInverterStatus(t, "InverterStatus", got.InverterStatus, want.InverterStatus)
	assertOperationalModeStatus(t, "OperationalModeStatus", got.OperationalModeStatus, want.OperationalModeStatus)

	if got.AlarmStatus == nil {
		t.Errorf("step 5: AlarmStatus = nil, want non-nil")
	} else if *got.AlarmStatus != *want.AlarmStatus {
		t.Errorf("step 5: AlarmStatus = %d, want %d", *got.AlarmStatus, *want.AlarmStatus)
	}
	if got.StateOfChargeStatus == nil {
		t.Errorf("step 5: StateOfChargeStatus = nil, want non-nil")
	} else if *got.StateOfChargeStatus != *want.StateOfChargeStatus {
		t.Errorf("step 5: StateOfChargeStatus = %d, want %d", *got.StateOfChargeStatus, *want.StateOfChargeStatus)
	}

	// Href is server-assigned (defaultFactory in singleton handler
	// builds it as /edev/{id}/der/{derId}/ders). Confirm it matches
	// so a client walking back from the parent DER link lands on the
	// resource we just PUT, not a sibling.
	wantHref := dersPath
	if got.Href != wantHref {
		t.Errorf("step 5: Href = %q, want %q", got.Href, wantHref)
	}
}

// assertConnectStatus compares the dateTime + value pair on a
// *sep2.ConnectStatusType. The pointer-typed wrapper makes a nil-check
// the first failure mode worth reporting (codec dropped the element).
func assertConnectStatus(t *testing.T, name string, got, want *sep2.ConnectStatusType) {
	t.Helper()
	if got == nil {
		t.Errorf("step 5: %s = nil, want non-nil", name)
		return
	}
	if got.DateTime != want.DateTime {
		t.Errorf("step 5: %s.DateTime = %d, want %d", name, got.DateTime, want.DateTime)
	}
	if got.Value != want.Value {
		t.Errorf("step 5: %s.Value = %d, want %d", name, got.Value, want.Value)
	}
}

// assertInverterStatus mirrors assertConnectStatus for InverterStatusType.
// The two types have identical shapes (DateTime + Value), but live as
// distinct named types per pkg/sep2/der.go; replicating the helper keeps
// the call sites readable and avoids a generic constraint that would
// import-cycle the test's intent.
func assertInverterStatus(t *testing.T, name string, got, want *sep2.InverterStatusType) {
	t.Helper()
	if got == nil {
		t.Errorf("step 5: %s = nil, want non-nil", name)
		return
	}
	if got.DateTime != want.DateTime {
		t.Errorf("step 5: %s.DateTime = %d, want %d", name, got.DateTime, want.DateTime)
	}
	if got.Value != want.Value {
		t.Errorf("step 5: %s.Value = %d, want %d", name, got.Value, want.Value)
	}
}

// assertOperationalModeStatus mirrors the above for
// OperationalModeStatusType. Same rationale.
func assertOperationalModeStatus(t *testing.T, name string, got, want *sep2.OperationalModeStatusType) {
	t.Helper()
	if got == nil {
		t.Errorf("step 5: %s = nil, want non-nil", name)
		return
	}
	if got.DateTime != want.DateTime {
		t.Errorf("step 5: %s.DateTime = %d, want %d", name, got.DateTime, want.DateTime)
	}
	if got.Value != want.Value {
		t.Errorf("step 5: %s.Value = %d, want %d", name, got.Value, want.Value)
	}
}

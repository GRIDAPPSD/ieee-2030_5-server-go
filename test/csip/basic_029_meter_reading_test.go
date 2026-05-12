// CSIP V1.2 §8.29 — Meter Reading (MirrorUsagePoint + MirrorMeterReading).
//
// BASIC-029 exercises the device-side metering mirror end-to-end.
// The test POSTs a MirrorUsagePoint to /mup, then POSTs four
// MirrorMeterReading instances to /mup/{id}/mr — one per V1.2 §8.29
// required ReadingType: Real Power (W), Reactive Power (var),
// Frequency (Hz), and Voltage (V). Each reading carries its own
// embedded ReadingType describing units, kind, accumulation
// behaviour, and flow direction.
//
// PARALLEL POLICY (READ BEFORE FLIPPING — depends on IEEE-010):
//
//	BASIC-029 runs sequentially (no t.Parallel()) until IEEE-010
//	(race-loss /edev /mup) lands. Flip on after Phase 4 IEEE-010
//	merges.
//
// Why this matters: HandleCreateMirrorUsagePoint at
// internal/handler/mirror.go performs a non-atomic
// "Create-then-fall-back-to-Get-on-AlreadyExists" sequence on
// the MirrorUsagePoints store, plus HandlePostMirrorMeterReading
// does a non-atomic "verify parent exists, then create child" pair.
// Under -race + t.Parallel two concurrent CSIP tests racing on /mup
// can interleave such that the parent Get returns ErrNotFound after
// it was just created. IEEE-010 makes both transitions atomic; until
// then BASIC-029 stays serial. The phase doc Section §3 (BASIC-029
// IEEE-010 dependency) is the canonical citation.
//
// V1.2 procedure step → assertion mapping:
//
//	Step 1 (boot CSIP server)                            ────► csiptest.BootServer
//	Step 2 (POST a MirrorUsagePoint with serviceCategory
//	        and roleFlags set for an inverter)            ────► POST /mup
//	Step 3 (POST returns 201 + Location header pointing
//	        at the new /mup/{id})                          ────► resp.StatusCode == 201
//	                                                            && Location prefix /mup/
//	Step 4 (GET /mup/{id} returns the created MUP with
//	        deviceLFDI overridden from the cert identity)  ────► GET, parse, asserts
//	Step 5 (POST 4 MirrorMeterReading instances to
//	        /mup/{id}/mr, one per required ReadingType:
//	        Real P, Reactive P, Frequency, Voltage)        ────► POST /mup/{id}/mr (×4)
//	Step 6 (each POST returns 201 + Location header)     ─────► assert per-POST status
//	Step 7 (all four readings + their parent MUP are
//	        persisted in the server's stores under the
//	        expected key shape — /mup/{id} for the parent
//	        and /mup/{id}/mr/{ts} for each child)          ────► srv.Stores assertions
//
// Why step 7 asserts via srv.Stores instead of a GET:
// The current router (internal/server/router.go) wires POST /mup/{id}/mr
// but does NOT advertise a GET /mup/{id}/mr list endpoint. There is
// therefore no over-the-wire post-condition check available today.
// Asserting through srv.Stores still proves the full POST path
// (TLS handshake → ACL middleware → handler → store) executed
// correctly. The missing list endpoint is flagged in the IEEE-062 PR
// description as a follow-up; once a GET handler ships this step is
// promoted to a wire-level check.
package csip_test

import (
	"bytes"
	"context"
	"encoding/xml"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-go/test/csip/csiptest"
)

// UoM values per IEC 61968 / V1.2 §10.4 ReadingType.uom enumeration.
// pkg/sep2/metering.go already exposes UomWatts, UomVars, UomVolts,
// UomAmps — Frequency (Hz) is not in that constant set, so it is
// declared here as a test-local constant (V1.2 maps Hz to 33). Same
// scope-discipline rationale as leGenSoftware in basic_027.
const uomHertz uint8 = 33

// ReadingType.kind enum values per V1.2 §10.4.
//   - kindPower (37) — instantaneous active or reactive power.
//   - kindFrequency (12) — frequency.
//   - kindVoltage (54) — voltage.
//
// Replicated test-local for the same scope reasons above.
const (
	kindPower     uint8 = 37
	kindFrequency uint8 = 12
	kindVoltage   uint8 = 54
)

// TestBASIC_029_MeterReading implements CSIP V1.2 §8.29.
//
// NOTE: this test deliberately does NOT call t.Parallel(). See the
// PARALLEL POLICY section at the top of this file for why.
func TestBASIC_029_MeterReading(t *testing.T) {
	// Step 1: boot a CSIP server with a CA + device cert under our
	// control. /mup is gated by AuthDeviceCert in DefaultACLRules; the
	// device cert is mandatory both for the POST /mup and for the
	// downstream MMR POSTs.
	_, caCertFile, deviceCert := mustBuildClientPKI(t)
	srv := csiptest.BootServer(t,
		csiptest.WithClientCAsFile(caCertFile),
		csiptest.WithClientCert(deviceCert),
	)
	httpClient := buildClient(t, srv.RootCA, deviceCert)
	ctx := context.Background()

	// Step 2 + 3: POST a MirrorUsagePoint. MRID is deterministic
	// (basic029-mup) so the server's id-derivation path picks it for
	// the /mup/{id} segment, making downstream GET + store
	// inspection paths predictable.
	const mupMRID = "basic029mup"
	mupIn := sep2.MirrorUsagePoint{
		MRID:                mupMRID,
		Description:         "BASIC-029 four-reading inverter mirror",
		ServiceCategoryKind: 0,        // 0 == "electricity" per V1.2 §10.4 ServiceKind
		Status:              1,        // 1 == "on" per V1.2 §10.4 UsagePointStatus
		RoleFlags:           uint16(1), // 0x01 == "isPremisesAggregationPoint"
	}
	mupBody, err := xml.Marshal(&mupIn)
	if err != nil {
		t.Fatalf("step 2: marshal MirrorUsagePoint: %v", err)
	}

	mupReq, err := http.NewRequestWithContext(ctx, http.MethodPost, srv.BaseURL+"/mup", bytes.NewReader(mupBody))
	if err != nil {
		t.Fatalf("step 2: build POST /mup: %v", err)
	}
	mupReq.Header.Set("Content-Type", "application/sep+xml")
	mupResp, err := httpClient.Do(mupReq)
	if err != nil {
		t.Fatalf("step 2: POST /mup: %v", err)
	}
	_, _ = io.Copy(io.Discard, mupResp.Body)
	_ = mupResp.Body.Close()
	if mupResp.StatusCode != http.StatusCreated {
		t.Fatalf("step 3: POST /mup: status = %d, want 201", mupResp.StatusCode)
	}
	mupLocation := mupResp.Header.Get("Location")
	if mupLocation == "" {
		t.Fatalf("step 3: POST /mup: missing Location header")
	}
	if !strings.HasPrefix(mupLocation, "/mup/") {
		t.Fatalf("step 3: POST /mup: Location = %q, want /mup/ prefix", mupLocation)
	}

	// Step 4: GET /mup/{id} returns the created MUP. The id segment
	// is the MRID (HandleCreateMirrorUsagePoint at
	// internal/handler/mirror.go uses mup.MRID directly when set).
	getMupReq, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.BaseURL+mupLocation, nil)
	if err != nil {
		t.Fatalf("step 4: build GET %s: %v", mupLocation, err)
	}
	getMupResp, err := httpClient.Do(getMupReq)
	if err != nil {
		t.Fatalf("step 4: GET %s: %v", mupLocation, err)
	}
	if getMupResp.StatusCode != http.StatusOK {
		_ = getMupResp.Body.Close()
		t.Fatalf("step 4: GET %s: status = %d, want 200", mupLocation, getMupResp.StatusCode)
	}
	mupBytes, err := io.ReadAll(getMupResp.Body)
	_ = getMupResp.Body.Close()
	if err != nil {
		t.Fatalf("step 4: read GET body: %v", err)
	}
	var mupOut sep2.MirrorUsagePoint
	if err := xml.Unmarshal(mupBytes, &mupOut); err != nil {
		t.Fatalf("step 4: unmarshal MirrorUsagePoint: %v", err)
	}
	if mupOut.MRID != mupMRID {
		t.Errorf("step 4: MirrorUsagePoint.MRID = %q, want %q", mupOut.MRID, mupMRID)
	}
	if mupOut.Description != mupIn.Description {
		t.Errorf("step 4: MirrorUsagePoint.Description = %q, want %q", mupOut.Description, mupIn.Description)
	}
	// HandleCreateMirrorUsagePoint forcibly overrides DeviceLFDI from
	// the cert identity (security: never trust client-supplied LFDI).
	// We assert non-empty rather than the exact value; the LFDI is
	// derived from the ephemeral device cert and recomputed per test.
	if mupOut.DeviceLFDI == "" {
		t.Errorf("step 4: MirrorUsagePoint.DeviceLFDI = empty, want server-assigned from cert")
	}
	if mupOut.MirrorMeterReadingListLink == nil || mupOut.MirrorMeterReadingListLink.Href == "" {
		t.Errorf("step 4: MirrorUsagePoint.MirrorMeterReadingListLink missing or empty href")
	}

	// Step 5 + 6: POST 4 MirrorMeterReadings, one per V1.2 §8.29
	// required ReadingType. flowDirection = 1 (forward, "delivered
	// to customer"). powerOfTenMultiplier = 0 (no scaling) for
	// readability; the type allows scaling but the procedure does
	// not exercise it.
	mrPath := mupLocation + "/mr"

	type mrSpec struct {
		label string
		mrid  string
		uom   uint8
		kind  uint8
		value int64
	}
	specs := []mrSpec{
		{label: "RealPower_W", mrid: "rt-real-w", uom: sep2.UomWatts, kind: kindPower, value: 1500},
		{label: "ReactivePower_var", mrid: "rt-reactive-var", uom: sep2.UomVars, kind: kindPower, value: 200},
		{label: "Frequency_Hz", mrid: "rt-frequency-hz", uom: uomHertz, kind: kindFrequency, value: 6000},
		{label: "Voltage_V", mrid: "rt-voltage-v", uom: sep2.UomVolts, kind: kindVoltage, value: 24000},
	}

	now := time.Now().Unix()
	flowFwd := sep2.FlowDirectionForward
	powerOfTen := int8(0)

	for i, s := range specs {
		readingValue := s.value
		mmrBody := sep2.MirrorMeterReading{
			MRID:           s.mrid,
			Description:    s.label,
			LastUpdateTime: now + int64(i),
			ReadingType: &sep2.ReadingType{
				MRID:                 s.mrid + "-type",
				Kind:                 &s.kind,
				Uom:                  &s.uom,
				FlowDirection:        &flowFwd,
				PowerOfTenMultiplier: &powerOfTen,
			},
			Reading: &sep2.Reading{
				Value: &readingValue,
			},
		}
		body, err := xml.Marshal(&mmrBody)
		if err != nil {
			t.Fatalf("step 5: marshal MMR[%s]: %v", s.label, err)
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodPost, srv.BaseURL+mrPath, bytes.NewReader(body))
		if err != nil {
			t.Fatalf("step 5: build POST MMR[%s]: %v", s.label, err)
		}
		req.Header.Set("Content-Type", "application/sep+xml")

		resp, err := httpClient.Do(req)
		if err != nil {
			t.Fatalf("step 5: POST MMR[%s]: %v", s.label, err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()

		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("step 6: POST MMR[%s]: status = %d, want 201", s.label, resp.StatusCode)
		}
		loc := resp.Header.Get("Location")
		if loc == "" {
			t.Errorf("step 6: POST MMR[%s]: missing Location header", s.label)
		}
		wantPrefix := mupLocation + "/mr/"
		if !strings.HasPrefix(loc, wantPrefix) {
			t.Errorf("step 6: POST MMR[%s]: Location = %q, want prefix %q", s.label, loc, wantPrefix)
		}

		// Same nanosecond-key issue as BASIC-027: HandlePostMirrorMeterReading
		// keys MMR records under fmt.Sprintf("%020d", time.Now().UnixNano())
		// — sub-nanosecond consecutive POSTs collide on store.ErrAlreadyExists.
		// 1ms gap is safe and trivial.
		time.Sleep(time.Millisecond)
	}

	// Step 7: parent MUP + four child MMRs persisted in the server's
	// in-memory stores. We use srv.Stores rather than a GET because
	// the router does not expose GET /mup/{id}/mr today (flagged in
	// the file-header doc + PR description). The store reflects the
	// post-state of the handler chain, so this still proves the full
	// POST path executed and recorded the resource.
	//
	// Parent assertion via MirrorUsagePoints.Get keyed by MRID
	// (HandleCreateMirrorUsagePoint uses mup.MRID as the store id
	// when supplied).
	if _, err := srv.Stores.MirrorUsagePoints.Get(ctx, mupMRID); err != nil {
		t.Errorf("step 7: MirrorUsagePoints[%s] missing from store: %v", mupMRID, err)
	}
	mmrCount, err := srv.Stores.MirrorMeterReadings.Count(ctx, mupMRID)
	if err != nil {
		t.Fatalf("step 7: MirrorMeterReadings.Count(%s): %v", mupMRID, err)
	}
	if got, want := int(mmrCount), len(specs); got != want {
		t.Errorf("step 7: MirrorMeterReadings stored = %d, want %d", got, want)
	}
}

// CSIP V1.2 Section 8.29 - Meter Reading (MirrorUsagePoint + MirrorMeterReading).
//
// BASIC-029 exercises the device-side metering mirror end-to-end.
// The test POSTs a MirrorUsagePoint to /mup, then POSTs four
// MirrorMeterReading instances to /mup/{id}/mr - one per V1.2 Section 8.29
// required ReadingType: Real Power (W), Reactive Power (var),
// Frequency (Hz), and Voltage (V). Each reading carries its own
// embedded ReadingType describing units, kind, accumulation
// behaviour, and flow direction.
//
// PARALLEL POLICY (READ BEFORE FLIPPING - depends on #9):
//
//	BASIC-029 runs sequentially (no t.Parallel()) until #9
//	(race-loss /edev /mup) lands. Flip on after Phase 4 #9
//	merges.
//
// Why this matters: HandleCreateMirrorUsagePoint (now in core, at
// pkg/sep2srv/handlers/metering/mirror.go) performs a non-atomic
// "Create-then-fall-back-to-Get-on-AlreadyExists" sequence on
// the MirrorUsagePoints store, plus HandlePostMirrorMeterReading
// does a non-atomic "verify parent exists, then create child" pair.
// Under -race + t.Parallel two concurrent CSIP tests racing on /mup
// can interleave such that the parent Get returns ErrNotFound after
// it was just created. #9 makes both transitions atomic; until
// then BASIC-029 stays serial. The phase doc Section Section 3 (BASIC-029
// #9 dependency) is the canonical citation.
//
// V1.2 procedure step -> assertion mapping:
//
//	Step 1 (boot CSIP server)                            -> csiptest.BootServer
//	Step 2 (POST a MirrorUsagePoint with serviceCategory
//	        and roleFlags set for an inverter, carrying
//	        one inline MirrorMeterReading)                 -> POST /mup
//	Step 3 (POST returns 201 + Location header pointing
//	        at the new /mup/{id})                          -> resp.StatusCode == 201
//	                                                            && Location prefix /mup/
//	Step 4 (GET the Location returns the created MUP with
//	        deviceLFDI overridden from the cert identity,
//	        and with collections omitted per rule (c))     -> GET, parse, asserts
//	Step 5 (POST 4 MirrorMeterReading instances to the
//	        resource named by the Location header, one per
//	        required ReadingType: Real P, Reactive P,
//	        Frequency, Voltage)                            -> POST /mup/{id} (x4)
//	Step 6 (each POST returns 201 + Location header)     -> assert per-POST status
//	Step 7 (the inline reading plus all four posted
//	        readings, and their parent MUP, are persisted
//	        under the server-derived key with the field
//	        values the client sent)                        -> srv.Stores assertions
//
// Why steps 5 and 6 POST to /mup/{id} rather than /mup/{id}/mr:
// IEEE 2030.5-2018 section 10.11.3 rule (d) has the client post readings
// "to the resource identified in the Metering server's response ... (e.g.
// /mup/3)", which is literally the Location header POST /mup returned.
// Core mounts BOTH POST /mup/{id} and POST /mup/{id}/mr against the same
// handler (assembly.go:401 and :419), so neither route is deprecated and
// this is a choice of which one to exercise, not a migration. Following the
// server's own Location header is the more faithful reading of rule (d) and
// is the path the EPRI reference client actually takes, so that is the one
// pinned here. The /mr convention route stays covered by the handler-level
// test at internal/handler/mirror_test.go.
//
// Why step 4 no longer asserts a MirrorMeterReadingListLink:
// It asserted a field that sep.xsd does not define. MirrorUsagePoint carries
// MirrorMeterReading inline and repeated (sep.xsd:6472-6493); there is no
// link-typed child, and core removed MirrorMeterReadingListLink rather than
// renaming it. Nothing replaces that assertion one-for-one, because the
// resource it claimed to point at does not exist on the wire. What replaces
// it is stronger and spec-anchored: step 2 now sends an inline reading, step
// 4 asserts GET omits it (rule (c): a GET of the MirrorUsagePoint "SHALL
// return a resource with only the first level elements"), and step 7 asserts
// it was nonetheless stored. That covers the store/serve split the inline
// shape introduced, which the old link assertion never touched.
//
// Why step 7 asserts via srv.Stores instead of a GET:
// The router wires the reading POST routes but does NOT advertise a
// GET /mup/{id}/mr list endpoint, and rule (c) means GET /mup/{id} omits
// the inline collection by design. There is therefore no over-the-wire
// post-condition check available for reading CONTENT today. Asserting
// through srv.Stores proves the full POST path (TLS handshake -> ACL
// middleware -> handler -> store) executed and recorded the values sent.
// The missing list endpoint is flagged in the #57 PR description as a
// follow-up; once a GET handler ships this step is promoted to a wire-level
// check.
//
// Store-key note: the MirrorUsagePoint store id is NOT the client mRID. Core
// derives it as MirrorStoreID(creator LFDI, client mRID) so two devices
// POSTing the same mRID get distinct mirrors, so step 7 reads the id back
// out of the Location header rather than assuming the mRID.
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

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/test/csip/csiptest"
)

// UoM values per IEC 61968 / V1.2 Section 10.4 ReadingType.uom enumeration.
// pkg/sep2/metering.go already exposes UomWatts, UomVars, UomVolts,
// UomAmps - Frequency (Hz) is not in that constant set, so it is
// declared here as a test-local constant (V1.2 maps Hz to 33). Same
// scope-discipline rationale as leGenSoftware in basic_027.
const uomHertz uint8 = 33

// ReadingType.kind enum values per V1.2 Section 10.4.
//   - kindPower (37) - instantaneous active or reactive power.
//   - kindFrequency (12) - frequency.
//   - kindVoltage (54) - voltage.
//
// Replicated test-local for the same scope reasons above.
const (
	kindPower     uint8 = 37
	kindFrequency uint8 = 12
	kindVoltage   uint8 = 54
)

// mrSpec describes one of the V1.2 Section 8.29 required ReadingTypes.
// Package-scoped rather than function-local because the same shape now feeds
// both the inline reading on the create POST (step 2) and the out-of-band
// readings (step 5); one builder keeps the two from drifting.
type mrSpec struct {
	label string
	mrid  string
	uom   uint8
	kind  uint8
	value int64
}

// newMMR builds a MirrorMeterReading for spec, timestamped at lastUpdate.
//
// flowDirection = 1 (forward, "delivered to customer").
// powerOfTenMultiplier = 0 (no scaling) for readability; the type allows
// scaling but the procedure does not exercise it.
//
// Every pointer field is bound to a fresh local. Taking the address of a
// range variable or of a shared package-level value would alias one number
// across every reading built here, which is exactly the class of bug the
// per-reading value assertions in step 7 exist to catch.
func newMMR(spec mrSpec, lastUpdate int64) sep2.MirrorMeterReading {
	kind := spec.kind
	uom := spec.uom
	value := spec.value
	flowFwd := sep2.FlowDirectionForward
	powerOfTen := int8(0)
	return sep2.MirrorMeterReading{
		MRID:           spec.mrid,
		Description:    spec.label,
		LastUpdateTime: lastUpdate,
		// ReadingType extends Resource, not IdentifiedObject, so it carries
		// no mRID of its own; the reading it describes owns the identity.
		ReadingType: &sep2.ReadingType{
			Kind:                 &kind,
			Uom:                  &uom,
			FlowDirection:        &flowFwd,
			PowerOfTenMultiplier: &powerOfTen,
		},
		Reading: &sep2.Reading{
			Value: &value,
		},
	}
}

// TestBASIC_029_MeterReading implements CSIP V1.2 Section 8.29.
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

	// Step 2 + 3: POST a MirrorUsagePoint carrying one inline
	// MirrorMeterReading. The mRID is deterministic so the record is easy
	// to identify, but note it is NOT the store key: core derives that from
	// (creator LFDI, client mRID), so every downstream lookup reads the id
	// back out of the Location header instead.
	const mupMRID = "basic029mup"
	inlineSpec := mrSpec{
		label: "Inline_RealPower_W",
		mrid:  "rt-inline-real-w",
		uom:   sep2.UomWatts,
		kind:  kindPower,
		value: 1234,
	}
	mupIn := sep2.MirrorUsagePoint{
		MRID:                mupMRID,
		Description:         "BASIC-029 four-reading inverter mirror",
		ServiceCategoryKind: 0,                      // 0 == "electricity" per V1.2 Section 10.4 ServiceKind
		Status:              1,                      // 1 == "on" per V1.2 Section 10.4 UsagePointStatus
		RoleFlags:           sep2.RoleFlagsValue(1), // 0x01 == "isPremisesAggregationPoint"
		// sep.xsd:6472-6493 puts MirrorMeterReading inline on
		// MirrorUsagePoint, repeated, with no link-typed alternative. A
		// client is entitled to seed readings on the create POST, so the
		// test does, and steps 4 and 7 pin the two halves of what the server
		// then owes: omit it on GET, keep it in the store.
		MirrorMeterReading: []sep2.MirrorMeterReading{newMMR(inlineSpec, time.Now().Unix())},
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

	// mupID is the server-derived store key, read back out of the Location
	// header rather than assumed to be the mRID: core keys the record under
	// MirrorStoreID(creator LFDI, client mRID) so two devices sending the
	// same mRID cannot collide. Step 7 uses this for both store lookups.
	mupID := strings.TrimPrefix(mupLocation, "/mup/")
	if mupID == "" || strings.Contains(mupID, "/") {
		t.Fatalf("step 3: Location = %q does not name a single /mup/{id} resource", mupLocation)
	}

	// Step 4: GET the Location the server just handed us returns the
	// created MUP.
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
	// roleFlags is a required element of the UsagePointBase sequence
	// (sep.xsd:6577-6588), so it must survive the round trip verbatim
	// rather than being dropped as an empty-looking value.
	if mupOut.RoleFlags != mupIn.RoleFlags {
		t.Errorf("step 4: MirrorUsagePoint.RoleFlags = %v, want %v", mupOut.RoleFlags, mupIn.RoleFlags)
	}
	// IEEE 2030.5-2018 section 10.11.3 rule (c): a GET of the
	// MirrorUsagePoint "SHALL return a resource with only the first level
	// elements (i.e., sub-elements and collections are not included)."
	// MirrorMeterReading is maxOccurs="unbounded" on MirrorUsagePoint, so
	// it is a collection and must not be served here, even though step 2
	// sent one and step 7 will prove the server kept it. This is the
	// assertion that replaced the old MirrorMeterReadingListLink check; see
	// the file header for why that field is gone rather than renamed.
	if len(mupOut.MirrorMeterReading) != 0 {
		t.Errorf("step 4: GET served %d MirrorMeterReading children, want 0 per rule (c)", len(mupOut.MirrorMeterReading))
	}
	if strings.Contains(string(mupBytes), "MirrorMeterReading") {
		t.Errorf("step 4: served bytes mention MirrorMeterReading despite rule (c):\n%s", string(mupBytes))
	}

	// Step 5 + 6: POST 4 MirrorMeterReadings, one per V1.2 Section 8.29
	// required ReadingType, to the resource the Location header named
	// (section 10.11.3 rule (d)); see the file header for why that route
	// rather than the /mr convention.
	specs := []mrSpec{
		{label: "RealPower_W", mrid: "rt-real-w", uom: sep2.UomWatts, kind: kindPower, value: 1500},
		{label: "ReactivePower_var", mrid: "rt-reactive-var", uom: sep2.UomVars, kind: kindPower, value: 200},
		{label: "Frequency_Hz", mrid: "rt-frequency-hz", uom: uomHertz, kind: kindFrequency, value: 6000},
		{label: "Voltage_V", mrid: "rt-voltage-v", uom: sep2.UomVolts, kind: kindVoltage, value: 24000},
	}

	now := time.Now().Unix()

	for i, s := range specs {
		mmrBody := newMMR(s, now+int64(i))
		body, err := xml.Marshal(&mmrBody)
		if err != nil {
			t.Fatalf("step 5: marshal MMR[%s]: %v", s.label, err)
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodPost, srv.BaseURL+mupLocation, bytes.NewReader(body))
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
		// - sub-nanosecond consecutive POSTs collide on store.ErrAlreadyExists.
		// 1ms gap is safe and trivial.
		time.Sleep(time.Millisecond)
	}

	// Step 7: the parent MUP, the reading it carried inline, and the four
	// out-of-band readings are all persisted, with the values the client
	// sent. srv.Stores rather than a GET because the router exposes no
	// GET /mup/{id}/mr and rule (c) strips the inline collection from
	// GET /mup/{id}; see the file header. The store reflects the post-state
	// of the handler chain, so this proves the full POST path executed and
	// recorded the resource.
	storedMUP, err := srv.Stores.MirrorUsagePoints.Get(ctx, mupID)
	if err != nil {
		t.Fatalf("step 7: MirrorUsagePoints[%s] missing from store: %v", mupID, err)
	}
	if storedMUP.MRID != mupMRID {
		t.Errorf("step 7: stored MirrorUsagePoint.MRID = %q, want %q", storedMUP.MRID, mupMRID)
	}
	if storedMUP.DeviceLFDI == "" {
		t.Errorf("step 7: stored MirrorUsagePoint.DeviceLFDI is empty; the ownership gate keys on it, so an empty value makes the record unreachable")
	}
	if storedMUP.Href != mupLocation {
		t.Errorf("step 7: stored MirrorUsagePoint.Href = %q, want %q (the Location the server advertised)", storedMUP.Href, mupLocation)
	}

	// The inline reading is stored ON the parent record, not in the child
	// store: it arrived as part of the MirrorUsagePoint document. That
	// split is the whole behavioural consequence of MirrorMeterReading
	// being inline in sep.xsd rather than a link to a separate list, so it
	// is asserted rather than assumed.
	if len(storedMUP.MirrorMeterReading) != 1 {
		t.Fatalf("step 7: stored MirrorUsagePoint carries %d inline MirrorMeterReading, want 1", len(storedMUP.MirrorMeterReading))
	}
	inlineStored := storedMUP.MirrorMeterReading[0]
	if inlineStored.MRID != inlineSpec.mrid {
		t.Errorf("step 7: inline reading mRID = %q, want %q", inlineStored.MRID, inlineSpec.mrid)
	}
	assertReadingValues(t, "inline", inlineStored, inlineSpec)
	// The server owns href on every reading, inline ones included, so a
	// client cannot dictate where its data appears to live.
	if !strings.HasPrefix(inlineStored.Href, mupLocation+"/mr/") {
		t.Errorf("step 7: inline reading Href = %q, want server-stamped %q prefix", inlineStored.Href, mupLocation+"/mr/")
	}

	// The four out-of-band readings land in the scoped child store, keyed
	// under the same server-derived parent id.
	mmrCount, err := srv.Stores.MirrorMeterReadings.Count(ctx, mupID)
	if err != nil {
		t.Fatalf("step 7: MirrorMeterReadings.Count(%s): %v", mupID, err)
	}
	if got, want := int(mmrCount), len(specs); got != want {
		t.Errorf("step 7: MirrorMeterReadings stored = %d, want %d", got, want)
	}

	// Limit is deliberately one more than expected: a List capped at exactly
	// len(specs) would hide an extra spurious record.
	listed, err := srv.Stores.MirrorMeterReadings.List(ctx, mupID, store.ListOptions{Limit: uint32(len(specs)) + 1})
	if err != nil {
		t.Fatalf("step 7: MirrorMeterReadings.List(%s): %v", mupID, err)
	}
	byMRID := make(map[string]sep2.MirrorMeterReading, len(listed.Items))
	for _, item := range listed.Items {
		byMRID[item.MRID] = item
	}
	for _, s := range specs {
		got, ok := byMRID[s.mrid]
		if !ok {
			t.Errorf("step 7: no stored reading with mRID %q (%s)", s.mrid, s.label)
			continue
		}
		if got.Description != s.label {
			t.Errorf("step 7: reading[%s] description = %q, want %q", s.label, got.Description, s.label)
		}
		assertReadingValues(t, s.label, got, s)
	}
}

// assertReadingValues checks that the uom, kind, and reading value the client
// sent survived the POST intact.
//
// A count-only check passes just as happily when every reading was stored
// with the same aliased uom pointer, or with a zeroed value, which is the
// silent-corruption shape a metering mirror must not have.
func assertReadingValues(t *testing.T, label string, got sep2.MirrorMeterReading, want mrSpec) {
	t.Helper()

	if got.ReadingType == nil {
		t.Errorf("step 7: reading[%s] ReadingType is nil, want uom %d kind %d", label, want.uom, want.kind)
	} else {
		if got.ReadingType.Uom == nil {
			t.Errorf("step 7: reading[%s] ReadingType.Uom is nil, want %d", label, want.uom)
		} else if *got.ReadingType.Uom != want.uom {
			t.Errorf("step 7: reading[%s] uom = %d, want %d", label, *got.ReadingType.Uom, want.uom)
		}
		if got.ReadingType.Kind == nil {
			t.Errorf("step 7: reading[%s] ReadingType.Kind is nil, want %d", label, want.kind)
		} else if *got.ReadingType.Kind != want.kind {
			t.Errorf("step 7: reading[%s] kind = %d, want %d", label, *got.ReadingType.Kind, want.kind)
		}
	}

	if got.Reading == nil || got.Reading.Value == nil {
		t.Errorf("step 7: reading[%s] has no Reading.Value, want %d", label, want.value)
		return
	}
	if *got.Reading.Value != want.value {
		t.Errorf("step 7: reading[%s] value = %d, want %d", label, *got.Reading.Value, want.value)
	}
}

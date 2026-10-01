package handler_test

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/auth"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/certs"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/commitment"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/commitment/sources"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/dercontrol"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/flowreservation"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/handler"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

// #670: admin write routes for flow reservations, against the real queue,
// commitment ledger, DER control issuer and answer store.

const (
	frwEdev    = "4"
	frwManaged = "7"
	frwProgram = "/edev/7/fsa/fsa1/derp/derp1"
	frwKey     = "the-key"
)

type frwFixture struct {
	*frFixture
	t        *testing.T
	programs *memory.ScopedStore[sep2.DERProgram]
	answers  *memory.ScopedStore[flowreservation.AnswerRecord]
	ledger   *commitment.Ledger
	issuer   *dercontrol.Issuer
	queue    *flowreservation.Queue
	h        *handler.AdminFlowReservationHandler
	logs     *bytes.Buffer
	base     int64
}

func newFRWFixture(t *testing.T) *frwFixture {
	t.Helper()
	ctx := context.Background()
	f := &frwFixture{
		frFixture: newFRFixture(t),
		t:         t,
		programs:  memory.NewScopedStore[sep2.DERProgram](),
		answers:   memory.NewScopedStore[flowreservation.AnswerRecord](),
		logs:      &bytes.Buffer{},
		base:      time.Now().Add(time.Hour).Unix(),
	}
	f.device(frwManaged, frManagedLFDI)
	if err := f.pairs.Assign(ctx, frAggLFDI, frManagedLFDI); err != nil {
		t.Fatal(err)
	}
	if err := f.programs.Create(ctx, frwManaged, "derp1", sep2.DERProgram{
		DERControlListLink: &sep2.ListLink{Href: frwProgram + "/derc"},
	}); err != nil {
		t.Fatal(err)
	}
	pen := uint32(0x40732001)
	issuer, err := dercontrol.NewIssuer(f.programs, f.ctrls, f.ctrlLcs, dercontrol.Config{PEN: &pen})
	if err != nil {
		t.Fatal(err)
	}
	f.issuer = issuer
	f.ledger = sources.NewLedger(f.devices, f.pairs, f.frps, f.lcs, f.ctrls, f.ctrlLcs)
	fleets := commitment.Resolver{Devices: f.devices, Managers: f.pairs}
	f.queue = flowreservation.NewQueue(f.frqs, f.frps, flowreservation.NewLedgerGate(f.ledger, fleets), flowreservation.Config{Deadline: time.Hour}, &pen)
	t.Cleanup(f.queue.Close)
	answers := flowreservation.NewAnswers(f.answers)
	f.queue.RecordAnswers(answers)
	writers := sources.NewWriters(issuer, f.lcs)

	h := f.handler()
	h.Now = nil
	h.Attributions = answers
	h.CancelRecorder = answers
	h.Queue = f.queue
	h.Revise = flowreservation.ReviseDeps{
		FRQ: f.frqs, FRP: f.frps, Ledger: f.ledger, Writers: writers, PEN: &pen, Answers: answers,
		Replace: func(edevID string, frp sep2.FlowReservationResponse) (commitment.Replacement, error) {
			return sources.NewReplacement(f.frps, edevID, frp)
		},
	}
	h.Canceller = flowreservation.NewCanceller(f.frqs, f.frps, f.queue, f.ledger, writers)
	h.Logger = slog.New(slog.NewTextHandler(f.logs, nil))
	f.h = h
	return f
}

// router serves the handler behind the admin auth middleware, so a request
// carries the admission path a deployment would see.
func (f *frwFixture) router() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/derms/flow-reservations/{edevId}/{frqId}", f.h.HandleGet())
	mux.HandleFunc("POST /api/derms/flow-reservations/{edevId}/{frqId}/answer", f.h.HandleAnswer())
	mux.HandleFunc("POST /api/derms/flow-reservations/{edevId}/{frqId}/revise", f.h.HandleRevise())
	mux.HandleFunc("POST /api/derms/flow-reservations/{edevId}/{frqId}/cancel", f.h.HandleCancel())
	return auth.AdminAuthMiddleware(frwKey, nil, nil)(mux)
}

func (f *frwFixture) post(op, frqID, body string) *httptest.ResponseRecorder {
	f.t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/derms/flow-reservations/"+frwEdev+"/"+frqID+"/"+op, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+frwKey)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	f.router().ServeHTTP(rec, req)
	return rec
}

// pending stores a request for [base, base+3600) asking 10000 Wh of
// charging at 4000 W.
func (f *frwFixture) pending(id, mrid string) {
	f.t.Helper()
	f.request(frqSpec{
		edev: frwEdev, id: id, mrid: mrid, created: time.Now().Unix(),
		interval: &sep2.DateTimeInterval{Start: f.base, Duration: 3600},
		energy:   &sep2.SignedRealEnergy{Value: 10000},
		power:    &sep2.ActivePower{Value: 4000},
	})
}

func (f *frwFixture) granted(id, mrid string) sep2.FlowReservationResponse {
	f.t.Helper()
	f.pending(id, mrid)
	frp, err := f.queue.Answer(context.Background(), frwEdev, id, flowreservation.Decision{By: flowreservation.Attribution{Kind: flowreservation.KindOperator}})
	if err != nil {
		f.t.Fatal(err)
	}
	return frp
}

// execute issues a 600 s targetW control on the managed device that
// executes grant, through the issuer under the fleet lock.
func (f *frwFixture) execute(grant sep2.FlowReservationResponse, start int64, target int16) dercontrol.Result {
	f.t.Helper()
	ctx := context.Background()
	req := dercontrol.CreateRequest{
		DERProgramHref: frwProgram, Type: dercontrol.TargetW, TargetW: &sep2.ActivePower{Value: target},
		Start: &start, DurationSeconds: 600, ExecutesGrant: grant.MRID,
	}
	var res dercontrol.Result
	if err := f.ledger.Within(ctx, []string{frAggLFDI}, func(v commitment.View) error {
		var err error
		res, err = f.issuer.IssueInFleet(ctx, req, dercontrol.Fleet{Key: frAggLFDI, Reach: 1, Check: func(ctx context.Context, p dercontrol.Proposal) error {
			return v.CheckControl(ctx, commitment.Proposal{FleetKey: p.FleetKey, Window: p.Window, GrantMRID: p.GrantMRID, TargetW: p.TargetW, Reach: p.Reach, Supersedes: p.Supersedes})
		}})
		return err
	}); err != nil {
		f.t.Fatal(err)
	}
	return res
}

func (f *frwFixture) response(id string) sep2.FlowReservationResponse {
	f.t.Helper()
	frp, err := f.frps.Get(context.Background(), frwEdev, id)
	if err != nil {
		f.t.Fatalf("response %s: %v", id, err)
	}
	return frp
}

func (f *frwFixture) answerRecord(id string) flowreservation.AnswerRecord {
	f.t.Helper()
	rec, err := f.answers.Get(context.Background(), frwEdev, id)
	if err != nil {
		f.t.Fatalf("answer record %s: %v", id, err)
	}
	return rec
}

func decodeView(t *testing.T, rec *httptest.ResponseRecorder) handler.FlowReservationView {
	t.Helper()
	var v handler.FlowReservationView
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatalf("reply is not a FlowReservationView: %v: %s", err, rec.Body.String())
	}
	return v
}

type frwRefusal struct {
	Error string `json:"error"`
	Code  string `json:"code"`
	MRID  string `json:"mRID"`
	FrqID string `json:"frqId"`
}

func decodeRefusal(t *testing.T, rec *httptest.ResponseRecorder) frwRefusal {
	t.Helper()
	var r frwRefusal
	if err := json.Unmarshal(rec.Body.Bytes(), &r); err != nil {
		t.Fatalf("refusal body: %v: %s", err, rec.Body.String())
	}
	return r
}

func TestFRWrite_AnswerGrantAsAskedStoresTheResponseAndItsAuthor(t *testing.T) {
	f := newFRWFixture(t)
	f.pending("frq-1", "REQ-1")

	rec := f.post("answer", "frq-1", `{"decision":"grant"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201: %s", rec.Code, rec.Body.String())
	}
	stored := f.response("frq-1")
	if stored.Subject != "REQ-1" || stored.Href != "/edev/4/frp/frq-1" || stored.MRID == "" {
		t.Errorf("stored subject %q href %q mRID %q", stored.Subject, stored.Href, stored.MRID)
	}
	if stored.Interval == nil || *stored.Interval != (sep2.DateTimeInterval{Start: f.base, Duration: 3600}) {
		t.Errorf("stored interval = %+v, want the requested window", stored.Interval)
	}
	if stored.EnergyAvailable == nil || *stored.EnergyAvailable != (sep2.SignedRealEnergy{Value: 10000}) ||
		stored.PowerAvailable == nil || *stored.PowerAvailable != (sep2.ActivePower{Value: 4000}) {
		t.Errorf("stored energy %+v power %+v, want 10000 and 4000", stored.EnergyAvailable, stored.PowerAvailable)
	}

	got := f.answerRecord("frq-1")
	want := flowreservation.AnswerRecord{Action: flowreservation.ActionAnswer, By: flowreservation.Attribution{
		Kind: flowreservation.KindOperator, Admission: auth.AdmissionPathBearer, Principal: flowreservation.PrincipalAdminKey, At: got.By.At,
	}}
	if got.Action != want.Action || got.By != want.By || got.CancelledBy != nil || got.By.At == 0 {
		t.Errorf("answer record = %+v, want %+v with a non-zero At", got, want)
	}

	v := decodeView(t, rec)
	if v.State != "granted" || v.Tip == nil || v.Tip.MRID != stored.MRID || len(v.Responses) != 1 {
		t.Fatalf("view state %q tip %+v responses %d, want granted with the stored response", v.State, v.Tip, len(v.Responses))
	}
	if a := v.Tip.AnsweredBy; a.Kind != "operator" || a.Admission == nil || *a.Admission != "bearer" || a.Principal == nil || *a.Principal != "admin-key" || a.At != got.By.At {
		t.Errorf("answeredBy = %+v, want operator bearer admin-key at %d", a, got.By.At)
	}
	if v.Tip.CancelledBy != nil {
		t.Errorf("cancelledBy = %+v, want null", v.Tip.CancelledBy)
	}
}

// The page sends magnitudes; the server applies the request's own sign, so
// a discharging request is granted a discharge.
func TestFRWrite_AnswerAdjustedTakesTheRequestsSign(t *testing.T) {
	f := newFRWFixture(t)
	f.request(frqSpec{
		edev: frwEdev, id: "frq-1", mrid: "REQ-1", created: time.Now().Unix(),
		interval: &sep2.DateTimeInterval{Start: f.base, Duration: 3600},
		energy:   &sep2.SignedRealEnergy{Value: -10000},
		power:    &sep2.ActivePower{Value: -4000},
	})
	body := `{"decision":"grant","interval":{"start":` + itoa(f.base+600) + `,"duration":1800},"energy":{"value":5000,"multiplier":0},"power":{"value":2000,"multiplier":0}}`
	rec := f.post("answer", "frq-1", body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201: %s", rec.Code, rec.Body.String())
	}
	stored := f.response("frq-1")
	if *stored.Interval != (sep2.DateTimeInterval{Start: f.base + 600, Duration: 1800}) {
		t.Errorf("interval = %+v, want start base+600 duration 1800", stored.Interval)
	}
	if stored.EnergyAvailable.Value != -5000 || stored.PowerAvailable.Value != -2000 {
		t.Errorf("energy %d power %d, want -5000 and -2000 (the request's sign)", stored.EnergyAvailable.Value, stored.PowerAvailable.Value)
	}
}

func TestFRWrite_AnswerDenyStoresADenial(t *testing.T) {
	f := newFRWFixture(t)
	f.pending("frq-1", "REQ-1")
	rec := f.post("answer", "frq-1", `{"decision":"deny"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201: %s", rec.Code, rec.Body.String())
	}
	stored := f.response("frq-1")
	if stored.Interval == nil || stored.Interval.Duration != 0 || stored.Interval.Start != f.base ||
		stored.EnergyAvailable.Value != 0 || stored.PowerAvailable.Value != 0 {
		t.Errorf("denial interval %+v energy %+v power %+v, want duration 0 at the start and zero quantities", stored.Interval, stored.EnergyAvailable, stored.PowerAvailable)
	}
	if v := decodeView(t, rec); v.State != "denied" || v.Tip.AnsweredBy.Kind != "operator" {
		t.Errorf("view state %q answeredBy %+v, want denied by an operator", v.State, v.Tip.AnsweredBy)
	}
}

func adminCertificate(t *testing.T) *x509.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	type policyInformation struct{ PolicyIdentifier asn1.ObjectIdentifier }
	policies, err := asn1.Marshal([]policyInformation{{certs.OIDPolicyAdmin}})
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:    big.NewInt(7),
		NotBefore:       time.Now().Add(-time.Hour),
		NotAfter:        time.Now().Add(time.Hour),
		ExtraExtensions: []pkix.Extension{{Id: certs.OIDCertificatePolicies, Value: policies}},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	if !certs.HasPolicyOID(cert, certs.OIDPolicyAdmin) {
		t.Fatal("setup: the test certificate carries no admin policy OID")
	}
	return cert
}

// An mTLS admission records the verified certificate's SHA-256; Bearer and
// every other admission record the shared admin key (the other test).
func TestFRWrite_MTLSAnswerRecordsTheCertificateFingerprint(t *testing.T) {
	f := newFRWFixture(t)
	f.pending("frq-1", "REQ-1")
	cert := adminCertificate(t)
	req := httptest.NewRequest(http.MethodPost, "/api/derms/flow-reservations/4/frq-1/answer", strings.NewReader(`{"decision":"grant"}`))
	req.Header.Set("Content-Type", "application/json")
	req.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{cert}}
	rec := httptest.NewRecorder()
	f.router().ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201: %s", rec.Code, rec.Body.String())
	}
	sum := sha256.Sum256(cert.Raw)
	wantPrincipal := "cert:" + hex.EncodeToString(sum[:])
	got := f.answerRecord("frq-1")
	if got.By.Kind != "operator" || got.By.Admission != "mtls" || got.By.Principal != wantPrincipal {
		t.Errorf("answer record by = %+v, want operator mtls %s", got.By, wantPrincipal)
	}
	if a := decodeView(t, rec).Tip.AnsweredBy; a.Principal == nil || *a.Principal != wantPrincipal || *a.Admission != "mtls" {
		t.Errorf("view answeredBy = %+v, want principal %s", a, wantPrincipal)
	}
}

// #670 criterion 2: a revision cancels the previous response and creates a
// newer one with the same subject, and the executions that still fit move.
func TestFRWrite_ReviseCancelsTheOldResponseAndCreatesTheNewOne(t *testing.T) {
	f := newFRWFixture(t)
	ctx := context.Background()
	old := f.granted("frq-1", "REQ-1")
	exec := f.execute(old, f.base, -2000)
	oldBytes, err := json.Marshal(old)
	if err != nil {
		t.Fatal(err)
	}

	rec := f.post("revise", "frq-1", `{"decision":"grant","interval":{"start":`+itoa(f.base)+`,"duration":1800},"reason":"feeder limit"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201: %s", rec.Code, rec.Body.String())
	}

	afterOld, err := json.Marshal(f.response("frq-1"))
	if err != nil {
		t.Fatal(err)
	}
	if string(afterOld) != string(oldBytes) {
		t.Errorf("old response edited:\nbefore %s\nafter  %s", oldBytes, afterOld)
	}
	lc, err := f.lcs.Get(ctx, frwEdev, "frq-1")
	if err != nil || lc.CancelledAt == nil || lc.CancelReason != "feeder limit" {
		t.Errorf("old lifecycle = %+v (%v), want cancelled with reason %q", lc, err, "feeder limit")
	}

	nu := f.response("frq-1-r1")
	if nu.Subject != old.Subject || nu.MRID == old.MRID || nu.MRID == "" || nu.CreationTime <= old.CreationTime || nu.Href != "/edev/4/frp/frq-1-r1" {
		t.Errorf("revision subject %q mRID %q creationTime %d href %q; old subject %q mRID %q creationTime %d",
			nu.Subject, nu.MRID, nu.CreationTime, nu.Href, old.Subject, old.MRID, old.CreationTime)
	}
	if *nu.Interval != (sep2.DateTimeInterval{Start: f.base, Duration: 1800}) || nu.EnergyAvailable.Value != 10000 || nu.PowerAvailable.Value != 4000 {
		t.Errorf("revision interval %+v energy %+v power %+v", nu.Interval, nu.EnergyAvailable, nu.PowerAvailable)
	}
	if _, err := f.lcs.Get(ctx, frwEdev, "frq-1-r1"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("revision lifecycle: err = %v, want none (live)", err)
	}
	execLc, err := f.ctrlLcs.Get(ctx, exec.Scope.Key(), exec.ID)
	if err != nil || execLc.GrantMRID != nu.MRID || execLc.CancelledAt != nil {
		t.Errorf("execution lifecycle = %+v (%v), want moved to %s and live", execLc, err, nu.MRID)
	}

	oldRec, newRec := f.answerRecord("frq-1"), f.answerRecord("frq-1-r1")
	if oldRec.CancelledBy == nil || oldRec.CancelledBy.Kind != "operator" || oldRec.CancelledBy.Admission != "bearer" {
		t.Errorf("old record cancelledBy = %+v, want the reviser", oldRec.CancelledBy)
	}
	if newRec.Action != flowreservation.ActionRevise || newRec.By.Kind != "operator" || newRec.By.Principal != "admin-key" {
		t.Errorf("revision record = %+v, want revise by the operator", newRec)
	}

	v := decodeView(t, rec)
	if len(v.Responses) != 2 || v.Tip == nil || v.Tip.MRID != nu.MRID || v.State != "granted" {
		t.Fatalf("view responses %d tip %+v state %q, want two with the revision as tip", len(v.Responses), v.Tip, v.State)
	}
	if c := v.Responses[0].CancelledBy; c == nil || c.Kind != "operator" {
		t.Errorf("old response cancelledBy = %+v, want operator", c)
	}
	if len(v.Tip.Executions) != 1 || v.Tip.Executions[0].MRID != exec.Control.MRID {
		t.Errorf("tip executions = %+v, want the moved control", v.Tip.Executions)
	}
}

// #670 criterion 3: cancelling a grant cancels the controls executing it in
// the same write.
func TestFRWrite_CancelCancelsTheGrantAndItsExecutions(t *testing.T) {
	f := newFRWFixture(t)
	ctx := context.Background()
	grant := f.granted("frq-1", "REQ-1")
	e1 := f.execute(grant, f.base, -2000)
	e2 := f.execute(grant, f.base+600, -3000)

	rec := f.post("cancel", "frq-1", `{"reason":"operator stop"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	lc, err := f.lcs.Get(ctx, frwEdev, "frq-1")
	if err != nil || lc.CancelledAt == nil || lc.CancelReason != "operator stop" {
		t.Errorf("grant lifecycle = %+v (%v), want cancelled with the reason", lc, err)
	}
	for _, e := range []dercontrol.Result{e1, e2} {
		clc, err := f.ctrlLcs.Get(ctx, e.Scope.Key(), e.ID)
		if err != nil || clc.CancelledAt == nil {
			t.Errorf("execution %s lifecycle = %+v (%v), want cancelled", e.Control.MRID, clc, err)
		}
	}
	if frq, err := f.frqs.Get(ctx, frwEdev, "frq-1"); err != nil || frq.RequestStatus.RequestStatus != 0 {
		t.Errorf("request status = %+v (%v), want unchanged: the operator cancels the grant, not the request", frq.RequestStatus, err)
	}
	got := f.answerRecord("frq-1")
	if got.CancelledBy == nil || got.CancelledBy.Kind != "operator" || got.CancelledBy.Principal != "admin-key" || got.By.Kind != "operator" {
		t.Errorf("answer record = %+v, want answered and cancelled by an operator", got)
	}
	v := decodeView(t, rec)
	if v.State != "cancelled" || v.Tip.CancelledBy == nil || v.Tip.CancelledBy.Kind != "operator" || v.Tip.CancelReason == nil || *v.Tip.CancelReason != "operator stop" {
		t.Errorf("view state %q tip cancelledBy %+v reason %v, want cancelled by an operator", v.State, v.Tip.CancelledBy, v.Tip.CancelReason)
	}
	for _, e := range v.Tip.Executions {
		if e.EventStatus == nil || e.EventStatus.Status != "cancelled" {
			t.Errorf("execution %s status %+v, want cancelled", e.MRID, e.EventStatus)
		}
	}
}

// A revise whose rollback could not relink its controls leaves the old grant
// live beside the revision. A retry names the old grant plainly, and a cancel
// clears both (the PR 770 edge).
func TestFRWrite_EarlierLiveAnswerIsNamedAndCancelClearsIt(t *testing.T) {
	f := newFRWFixture(t)
	ctx := context.Background()
	old := f.granted("frq-1", "REQ-1")
	f.response("frq-1")
	stray := old
	stray.MRID = "STRAY-REVISION"
	stray.CreationTime = old.CreationTime + 1
	stray.Href = "/edev/4/frp/frq-1-r1"
	if err := f.frps.Create(ctx, frwEdev, "frq-1-r1", stray); err != nil {
		t.Fatal(err)
	}

	rec := f.post("revise", "frq-1", `{"decision":"grant","interval":{"start":`+itoa(f.base)+`,"duration":1800}}`)
	r := decodeRefusal(t, rec)
	if rec.Code != http.StatusConflict || r.Code != "fleet_window_committed" || r.MRID != old.MRID || !strings.Contains(r.Error, "earlier answer") || r.FrqID != "frq-1" {
		t.Fatalf("retry revise = %d %+v, want 409 fleet_window_committed naming %s as an earlier answer", rec.Code, r, old.MRID)
	}

	rec = f.post("cancel", "frq-1", `{}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("cancel status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	for _, id := range []string{"frq-1", "frq-1-r1"} {
		lc, err := f.lcs.Get(ctx, frwEdev, id)
		if err != nil || lc.CancelledAt == nil {
			t.Errorf("%s lifecycle = %+v (%v), want cancelled", id, lc, err)
		}
		if rec := f.answerRecord(id); rec.CancelledBy == nil || rec.CancelledBy.Kind != "operator" {
			t.Errorf("%s cancelledBy = %+v, want operator", id, rec.CancelledBy)
		}
	}
}

func TestFRWrite_ReasonIsCountedInCodePoints(t *testing.T) {
	f := newFRWFixture(t)
	ctx := context.Background()
	f.granted("frq-1", "REQ-1")
	// U+00E9 is two octets in UTF-8: 192 of them are 384 octets, within the
	// limit the page counts in characters.
	reason := strings.Repeat("\u00e9", 192)
	body, err := json.Marshal(map[string]any{"reason": reason})
	if err != nil {
		t.Fatal(err)
	}
	if rec := f.post("cancel", "frq-1", string(body)); rec.Code != http.StatusOK {
		t.Fatalf("192 code points: status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	// Accepted as 192 characters, stored reduced to 192 octets (String192).
	if lc, err := f.lcs.Get(ctx, frwEdev, "frq-1"); err != nil || lc.CancelReason != strings.Repeat("\u00e9", 96) {
		t.Errorf("stored reason has %d octets (%v), want the first 96 characters, 192 octets", len(lc.CancelReason), err)
	}

	f.granted("frq-2", "REQ-2")
	body, err = json.Marshal(map[string]any{"reason": reason + "x"})
	if err != nil {
		t.Fatal(err)
	}
	rec := f.post("cancel", "frq-2", string(body))
	if r := decodeRefusal(t, rec); rec.Code != http.StatusBadRequest || r.Code != "reason_too_long" {
		t.Errorf("193 code points: %d %+v, want 400 reason_too_long", rec.Code, r)
	}
}

// One line per write, and nothing a caller typed can split it or forge its
// event key; the reason is never logged.
func TestFRWrite_LogLineHoldsNoReasonAndNoInjectedLine(t *testing.T) {
	f := newFRWFixture(t)
	f.granted("frq-1", "REQ-1")
	body, err := json.Marshal(map[string]any{"decision": "grant", "reason": "SECRETREASON event=forged", "interval": map[string]int64{"start": f.base, "duration": 1800}})
	if err != nil {
		t.Fatal(err)
	}
	rec := f.post("revise", "frq-1", string(body))
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	assertOneSafeLine(t, f.logs.String(), "event=flow_reservation_revised")
	if strings.Contains(f.logs.String(), "SECRETREASON") {
		t.Errorf("the reason reached the log: %q", f.logs.String())
	}

	f.logs.Reset()
	req := httptest.NewRequest(http.MethodPost, "/api/derms/flow-reservations/4/frq-1%0D%0Aevent=forged/answer", strings.NewReader(`{"decision":"grant"}`))
	req.Header.Set("Authorization", "Bearer "+frwKey)
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	f.router().ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("injected id: status = %d, want 404: %s", rec.Code, rec.Body.String())
	}
	assertOneSafeLine(t, f.logs.String(), "event=flow_reservation_answer_refused")
}

func assertOneSafeLine(t *testing.T, logs, event string) {
	t.Helper()
	if n := strings.Count(logs, "\n"); n != 1 || !strings.HasSuffix(logs, "\n") {
		t.Fatalf("log = %q, want exactly one line", logs)
	}
	if strings.Contains(logs, "\r") {
		t.Errorf("log line holds a CR: %q", logs)
	}
	if n := strings.Count(logs, "event="); n != 1 || !strings.Contains(logs, event) {
		t.Errorf("log line has %d event= keys, want only %s: %q", n, event, logs)
	}
}

// The done-when of the write slice: the fallback's response reads
// answeredBy.kind deadline_fallback.
func TestFRWrite_FallbackAnswerReadsAsTheFallback(t *testing.T) {
	f := newFRWFixture(t)
	q := flowreservation.NewQueue(f.frqs, f.frps, flowreservation.NewLedgerGate(f.ledger, commitment.Resolver{Devices: f.devices, Managers: f.pairs}),
		flowreservation.Config{Deadline: 20 * time.Millisecond}, nil)
	t.Cleanup(q.Close)
	q.RecordAnswers(flowreservation.NewAnswers(f.answers))
	f.pending("frq-1", "REQ-1")
	frq, err := f.frqs.Get(context.Background(), frwEdev, "frq-1")
	if err != nil {
		t.Fatal(err)
	}
	q.Submit(frwEdev, "frq-1", frq, frq.CreationTime)

	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := f.frps.Get(context.Background(), frwEdev, "frq-1"); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the fallback stored no response within 5 s")
		}
		time.Sleep(10 * time.Millisecond)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/derms/flow-reservations/4/frq-1", nil)
	req.Header.Set("Authorization", "Bearer "+frwKey)
	rec := httptest.NewRecorder()
	f.router().ServeHTTP(rec, req)
	v := decodeView(t, rec)
	if v.Tip == nil || v.Tip.AnsweredBy.Kind != "deadline_fallback" || v.Tip.AnsweredBy.Admission != nil || v.Tip.AnsweredBy.Principal != nil {
		t.Errorf("fallback answeredBy = %+v, want kind deadline_fallback with no admission or principal", v.Tip)
	}
}

type failingCancelRecorder struct{}

func (failingCancelRecorder) RecordCancel(context.Context, string, string, flowreservation.Attribution) error {
	return errors.New("answer store down")
}

// The cancel record is written after the cancel committed: when it cannot be
// written the cancel still stands, the view says unrecorded, and the line is
// an ERROR.
func TestFRWrite_CancelRecordFailureLeavesTheCancelAndReadsUnrecorded(t *testing.T) {
	f := newFRWFixture(t)
	f.granted("frq-1", "REQ-1")
	f.h.CancelRecorder = failingCancelRecorder{}
	rec := f.post("cancel", "frq-1", `{}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	v := decodeView(t, rec)
	if v.State != "cancelled" || v.Tip.CancelledBy == nil || v.Tip.CancelledBy.Kind != "unrecorded" {
		t.Errorf("view state %q cancelledBy %+v, want cancelled and unrecorded", v.State, v.Tip.CancelledBy)
	}
	if !strings.Contains(f.logs.String(), "level=ERROR") || !strings.Contains(f.logs.String(), "cancel_record_failed=frq-1") {
		t.Errorf("log = %q, want one ERROR line naming the unrecorded cancel", f.logs.String())
	}
}

type failingRequests struct {
	handler.FlowReservationRequestReader
}

func (failingRequests) Get(context.Context, string, string) (sep2.FlowReservationRequest, error) {
	return sep2.FlowReservationRequest{}, errors.New("request store down")
}

// fakeCanceller answers CancelGrants with a fixed result, for the outcomes
// the real canceller reaches only through a race.
type fakeCanceller struct {
	cancelled []flowreservation.CancelledGrant
	err       error
}

func (c fakeCanceller) CancelGrants(context.Context, string, string, string) ([]flowreservation.CancelledGrant, error) {
	return c.cancelled, c.err
}

// frwSnapshot is every store a refused write must leave as it was, read
// field by field through JSON.
func (f *frwFixture) snapshot() string {
	f.t.Helper()
	ctx := context.Background()
	out := map[string]any{}
	add := func(name string, parents []string, list func(string) (any, error)) {
		for _, p := range parents {
			items, err := list(p)
			if err != nil {
				f.t.Fatal(err)
			}
			out[name+"/"+p] = items
		}
	}
	parents := func(s interface {
		Parents(context.Context) ([]string, error)
	}) []string {
		ps, err := s.Parents(ctx)
		if err != nil {
			f.t.Fatal(err)
		}
		return ps
	}
	all := store.ListOptions{Unbounded: true}
	add("requests", parents(f.frqs), func(p string) (any, error) { r, err := f.frqs.List(ctx, p, all); return r.Items, err })
	add("responses", parents(f.frps), func(p string) (any, error) { r, err := f.frps.List(ctx, p, all); return r.Items, err })
	add("lifecycles", parents(f.lcs), func(p string) (any, error) { r, err := f.lcs.List(ctx, p, all); return r.Items, err })
	add("answers", parents(f.answers), func(p string) (any, error) { r, err := f.answers.List(ctx, p, all); return r.Items, err })
	add("controlLifecycles", parents(f.ctrlLcs), func(p string) (any, error) { r, err := f.ctrlLcs.List(ctx, p, all); return r.Items, err })
	b, err := json.Marshal(out)
	if err != nil {
		f.t.Fatal(err)
	}
	return string(b)
}

func itoa(v int64) string { return big.NewInt(v).String() }

// #670 criterion 4 and every row of the refusal table: each refusal answers
// its code and stores nothing in the request, response, lifecycle, answer or
// control lifecycle stores.
func TestFRWrite_EveryRefusalStoresNothing(t *testing.T) {
	type tc struct {
		name, op, frqID string
		body            func(f *frwFixture) string
		setup           func(f *frwFixture) (wantMRID string)
		status          int
		code            string
		wantFrqID       bool
	}
	lit := func(s string) func(*frwFixture) string { return func(*frwFixture) string { return s } }
	pendingOne := func(f *frwFixture) string { f.pending("frq-1", "REQ-1"); return "" }
	grantedOne := func(f *frwFixture) string { f.granted("frq-1", "REQ-1"); return "" }
	// The EndDevice loses its LFDI after the grant, so it belongs to no
	// fleet and the ledger cannot reach the grant.
	fleetless := func(f *frwFixture) string {
		g := f.granted("frq-1", "REQ-1")
		if err := f.devices.Update(context.Background(), frwEdev, sep2.EndDevice{}); err != nil {
			f.t.Fatal(err)
		}
		return g.MRID
	}
	withExec := func(f *frwFixture) string {
		g := f.granted("frq-1", "REQ-1")
		return f.execute(g, f.base, -2000).Control.MRID
	}
	cases := []tc{
		{name: "body_format", op: "answer", body: lit(`not json`), setup: pendingOne, status: 400, code: "body_format"},
		{name: "body_format trailing data", op: "answer", body: lit(`{"decision":"grant"}{}`), setup: pendingOne, status: 400, code: "body_format"},
		{name: "unknown_field", op: "answer", body: lit(`{"decision":"grant","priority":1}`), setup: pendingOne, status: 400, code: "unknown_field"},
		{name: "decision_invalid", op: "answer", body: lit(`{"decision":"maybe"}`), setup: pendingOne, status: 400, code: "decision_invalid", wantFrqID: true},
		{name: "decision_invalid absent", op: "revise", body: lit(`{}`), setup: grantedOne, status: 400, code: "decision_invalid", wantFrqID: true},
		{name: "value_negative", op: "answer", body: lit(`{"decision":"grant","energy":{"value":-1}}`), setup: pendingOne, status: 400, code: "value_negative", wantFrqID: true},
		{name: "reason_too_long", op: "revise", body: lit(`{"decision":"deny","reason":"` + strings.Repeat("r", 193) + `"}`), setup: grantedOne, status: 400, code: "reason_too_long"},
		{name: "reason_not_allowed", op: "answer", body: lit(`{"decision":"grant","reason":"why"}`), setup: pendingOne, status: 400, code: "reason_not_allowed"},
		{name: "interval_outside_window", op: "answer", body: func(f *frwFixture) string {
			return `{"decision":"grant","interval":{"start":` + itoa(f.base-60) + `,"duration":600}}`
		}, setup: pendingOne, status: 400, code: "interval_outside_window", wantFrqID: true},
		{name: "no_requested_window", op: "answer", body: func(f *frwFixture) string {
			return `{"decision":"grant","interval":{"start":` + itoa(f.base) + `,"duration":600}}`
		}, setup: func(f *frwFixture) string {
			f.request(frqSpec{edev: frwEdev, id: "frq-1", mrid: "REQ-1", created: time.Now().Unix(), energy: &sep2.SignedRealEnergy{Value: 100}})
			return ""
		}, status: 400, code: "no_requested_window", wantFrqID: true},
		{name: "no_requested_energy", op: "answer", body: lit(`{"decision":"grant","energy":{"value":5}}`), setup: func(f *frwFixture) string {
			f.request(frqSpec{edev: frwEdev, id: "frq-1", mrid: "REQ-1", created: time.Now().Unix(), interval: &sep2.DateTimeInterval{Start: f.base, Duration: 600}, power: &sep2.ActivePower{Value: 10}})
			return ""
		}, status: 400, code: "no_requested_energy", wantFrqID: true},
		{name: "no_requested_power", op: "answer", body: lit(`{"decision":"grant","power":{"value":5}}`), setup: func(f *frwFixture) string {
			f.request(frqSpec{edev: frwEdev, id: "frq-1", mrid: "REQ-1", created: time.Now().Unix(), interval: &sep2.DateTimeInterval{Start: f.base, Duration: 600}, energy: &sep2.SignedRealEnergy{Value: 10}})
			return ""
		}, status: 400, code: "no_requested_power", wantFrqID: true},
		{name: "energy_exceeds_request", op: "answer", body: lit(`{"decision":"grant","energy":{"value":10001}}`), setup: pendingOne, status: 400, code: "energy_exceeds_request", wantFrqID: true},
		{name: "power_exceeds_request", op: "answer", body: lit(`{"decision":"grant","power":{"value":4001}}`), setup: pendingOne, status: 400, code: "power_exceeds_request", wantFrqID: true},
		{name: "grant_zero_duration", op: "answer", body: func(f *frwFixture) string {
			return `{"decision":"grant","interval":{"start":` + itoa(f.base) + `,"duration":0}}`
		}, setup: pendingOne, status: 400, code: "grant_zero_duration", wantFrqID: true},
		{name: "request_not_found", op: "answer", frqID: "frq-none", body: lit(`{"decision":"grant"}`), setup: pendingOne, status: 404, code: "request_not_found", wantFrqID: true},
		{name: "request_not_found on revise", op: "revise", frqID: "frq-none", body: lit(`{"decision":"deny"}`), setup: pendingOne, status: 404, code: "request_not_found", wantFrqID: true},
		{name: "already_answered", op: "answer", body: lit(`{"decision":"deny"}`), setup: func(f *frwFixture) string { return f.granted("frq-1", "REQ-1").MRID }, status: 409, code: "already_answered", wantFrqID: true},
		{name: "request_cancelled on answer", op: "answer", body: lit(`{"decision":"grant"}`), setup: func(f *frwFixture) string {
			f.request(frqSpec{edev: frwEdev, id: "frq-1", mrid: "REQ-1", created: time.Now().Unix(), cancelled: true, interval: &sep2.DateTimeInterval{Start: f.base, Duration: 600}})
			return ""
		}, status: 409, code: "request_cancelled", wantFrqID: true},
		{name: "request_cancelled on revise", op: "revise", body: lit(`{"decision":"deny"}`), setup: func(f *frwFixture) string {
			f.granted("frq-1", "REQ-1")
			frq, err := f.frqs.Get(context.Background(), frwEdev, "frq-1")
			if err != nil {
				t.Fatal(err)
			}
			frq.RequestStatus = sep2.RequestStatus{RequestStatus: sep2.RequestStatusCancelled}
			if err := f.frqs.Update(context.Background(), frwEdev, "frq-1", frq); err != nil {
				t.Fatal(err)
			}
			return ""
		}, status: 409, code: "request_cancelled", wantFrqID: true},
		{name: "not_answered on revise", op: "revise", body: lit(`{"decision":"deny"}`), setup: pendingOne, status: 409, code: "not_answered", wantFrqID: true},
		{name: "not_answered on cancel", op: "cancel", body: lit(`{}`), setup: pendingOne, status: 409, code: "not_answered", wantFrqID: true},
		{name: "grant_not_live: denial", op: "cancel", body: lit(`{}`), setup: func(f *frwFixture) string {
			f.pending("frq-1", "REQ-1")
			frp, err := f.queue.Answer(context.Background(), frwEdev, "frq-1", flowreservation.Decision{Kind: flowreservation.Deny})
			if err != nil {
				t.Fatal(err)
			}
			return frp.MRID
		}, status: 409, code: "grant_not_live", wantFrqID: true},
		{name: "grant_not_live: cancelled", op: "revise", body: lit(`{"decision":"deny"}`), setup: func(f *frwFixture) string {
			g := f.granted("frq-1", "REQ-1")
			at := time.Now().Unix()
			if err := f.lcs.Create(context.Background(), frwEdev, "frq-1", dercontrol.LifecycleRecord{CancelledAt: &at, CancelReason: "earlier"}); err != nil {
				t.Fatal(err)
			}
			return g.MRID
		}, status: 409, code: "grant_not_live", wantFrqID: true},
		{name: "grant_not_live: ended", op: "cancel", body: lit(`{}`), setup: func(f *frwFixture) string {
			f.request(frqSpec{edev: frwEdev, id: "frq-1", mrid: "REQ-1", created: time.Now().Unix() - 7200,
				interval: &sep2.DateTimeInterval{Start: time.Now().Unix() - 7200, Duration: 600}, energy: &sep2.SignedRealEnergy{Value: 10}, power: &sep2.ActivePower{Value: 10}})
			g, err := f.queue.Answer(context.Background(), frwEdev, "frq-1", flowreservation.Decision{})
			if err != nil {
				t.Fatal(err)
			}
			return g.MRID
		}, status: 409, code: "grant_not_live", wantFrqID: true},
		{name: "fleet_window_committed", op: "answer", frqID: "frq-2", body: lit(`{"decision":"grant"}`), setup: func(f *frwFixture) string {
			g := f.granted("frq-1", "REQ-1")
			f.pending("frq-2", "REQ-2")
			return g.MRID
		}, status: 409, code: "fleet_window_committed", wantFrqID: true},
		{name: "execution_outside_interval", op: "revise", body: func(f *frwFixture) string {
			return `{"decision":"grant","interval":{"start":` + itoa(f.base+1800) + `,"duration":1800}}`
		}, setup: withExec, status: 409, code: "execution_outside_interval", wantFrqID: true},
		{name: "execution_exceeds_power", op: "revise", body: lit(`{"decision":"grant","power":{"value":1000}}`), setup: withExec, status: 409, code: "execution_exceeds_power", wantFrqID: true},
		{name: "execution_exceeds_energy", op: "revise", body: lit(`{"decision":"grant","energy":{"value":100}}`), setup: withExec, status: 409, code: "execution_exceeds_energy", wantFrqID: true},
		{name: "execution_reverses_grant", op: "revise", body: lit(`{"decision":"grant","power":{"value":3000}}`), setup: func(f *frwFixture) string {
			g := f.granted("frq-1", "REQ-1")
			f.control(ctrlSpec{scope: frwManaged + "/fsa1/derp1", id: "rev", mrid: "REVERSING", grant: g.MRID, created: time.Now().Unix(),
				window: sep2.DateTimeInterval{Start: f.base, Duration: 600}, target: 2000, reach: 1})
			return "REVERSING"
		}, status: 409, code: "execution_reverses_grant", wantFrqID: true},
		{name: "internal", op: "answer", body: lit(`{"decision":"grant"}`), setup: func(f *frwFixture) string {
			f.pending("frq-1", "REQ-1")
			f.h.Requests = failingRequests{f.frqs}
			return ""
		}, status: 500, code: "internal"},
		{name: "not_configured answer", op: "answer", body: lit(`{"decision":"grant"}`), setup: func(f *frwFixture) string {
			f.pending("frq-1", "REQ-1")
			f.h.Queue = nil
			return ""
		}, status: 503, code: "not_configured"},
		{name: "not_configured revise", op: "revise", body: lit(`{"decision":"deny"}`), setup: func(f *frwFixture) string {
			f.granted("frq-1", "REQ-1")
			f.h.Revise.Ledger = nil
			return ""
		}, status: 503, code: "not_configured"},
		{name: "not_configured cancel", op: "cancel", body: lit(`{}`), setup: func(f *frwFixture) string {
			f.granted("frq-1", "REQ-1")
			f.h.Canceller = nil
			return ""
		}, status: 503, code: "not_configured"},
		{name: "grant_unresolved on cancel", op: "cancel", body: lit(`{}`), setup: fleetless, status: 409, code: "grant_unresolved", wantFrqID: true},
		{name: "grant_unresolved on revise", op: "revise", body: lit(`{"decision":"deny"}`), setup: fleetless, status: 409, code: "grant_unresolved", wantFrqID: true},
		{name: "chain_moving", op: "cancel", body: lit(`{}`), setup: func(f *frwFixture) string {
			f.granted("frq-1", "REQ-1")
			f.h.Canceller = fakeCanceller{err: flowreservation.ErrChainMoving}
			return ""
		}, status: 409, code: "chain_moving", wantFrqID: true},
		{name: "multiplier_out_of_range energy", op: "answer", body: lit(`{"decision":"grant","energy":{"value":1,"multiplier":10}}`), setup: pendingOne, status: 400, code: "multiplier_out_of_range", wantFrqID: true},
		{name: "multiplier_out_of_range power", op: "revise", body: lit(`{"decision":"grant","power":{"value":1,"multiplier":-10}}`), setup: grantedOne, status: 400, code: "multiplier_out_of_range", wantFrqID: true},
		{name: "reason_invalid NUL", op: "cancel", body: lit(`{"reason":"stop\u0000now"}`), setup: grantedOne, status: 400, code: "reason_invalid"},
		{name: "reason_invalid CR", op: "cancel", body: lit(`{"reason":"stop\rnow"}`), setup: grantedOne, status: 400, code: "reason_invalid"},
		{name: "reason_invalid bidi override", op: "revise", body: lit(`{"decision":"deny","reason":"stop\u202enow"}`), setup: grantedOne, status: 400, code: "reason_invalid"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFRWFixture(t)
			wantMRID := c.setup(f)
			frqID := c.frqID
			if frqID == "" {
				frqID = "frq-1"
			}
			before := f.snapshot()
			f.logs.Reset()
			rec := f.post(c.op, frqID, c.body(f))
			r := decodeRefusal(t, rec)
			if rec.Code != c.status || r.Code != c.code {
				t.Fatalf("%s: %d %+v, want %d %s", c.op, rec.Code, r, c.status, c.code)
			}
			if r.MRID != wantMRID {
				t.Errorf("mRID = %q, want %q", r.MRID, wantMRID)
			}
			if wantFrq := map[bool]string{true: frqID}[c.wantFrqID]; r.FrqID != wantFrq {
				t.Errorf("frqId = %q, want %q", r.FrqID, wantFrq)
			}
			if r.Error == "" {
				t.Error("refusal carries no error text")
			}
			if after := f.snapshot(); after != before {
				t.Errorf("a refused %s stored something:\nbefore %s\nafter  %s", c.op, before, after)
			}
			assertOneSafeLine(t, f.logs.String(), "event=flow_reservation_"+c.op+"_")
		})
	}
}

// lockProbe counts notifications and how many ran while the fleet's
// commitment lock was still held.
type lockProbe struct {
	ledger *commitment.Ledger
	mu     sync.Mutex
	calls  int
	held   int
}

func (p *lockProbe) Notify(ctx context.Context, _ string, _ uint8) {
	done := make(chan struct{})
	go func() {
		_ = p.ledger.Within(context.WithoutCancel(ctx), []string{frAggLFDI}, func(commitment.View) error { return nil })
		close(done)
	}()
	held := false
	select {
	case <-done:
	case <-time.After(300 * time.Millisecond):
		// The probe goroutine finishes once the holder releases the lock.
		held = true
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls++
	if held {
		p.held++
	}
}

func (p *lockProbe) counts() (calls, held int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls, p.held
}

// A revise and a cancel each notify the device's response list once, after
// the fleet lock is released.
func TestFRWrite_ReviseAndCancelNotifyOnlyAfterTheFleetLockIsReleased(t *testing.T) {
	f := newFRWFixture(t)
	probe := &lockProbe{ledger: f.ledger}
	f.h.Notifier = probe
	f.h.Revise.Writers = flowreservation.NotifyingWriters(f.h.Revise.Writers, probe)
	f.h.Canceller = flowreservation.NewCanceller(f.frqs, f.frps, f.queue, f.ledger, sources.NewWriters(f.issuer, f.lcs), flowreservation.WithNotifier(probe))
	f.granted("frq-1", "REQ-1")

	if rec := f.post("revise", "frq-1", `{"decision":"grant","interval":{"start":`+itoa(f.base)+`,"duration":1800}}`); rec.Code != http.StatusCreated {
		t.Fatalf("revise status = %d: %s", rec.Code, rec.Body.String())
	}
	if calls, held := probe.counts(); calls != 1 || held != 0 {
		t.Errorf("after revise: notifications %d (%d under the lock), want 1 and 0", calls, held)
	}
	if rec := f.post("cancel", "frq-1", `{}`); rec.Code != http.StatusOK {
		t.Fatalf("cancel status = %d: %s", rec.Code, rec.Body.String())
	}
	if calls, held := probe.counts(); calls != 2 || held != 0 {
		t.Errorf("after cancel: notifications %d (%d under the lock), want 2 and 0", calls, held)
	}
}

// A reason within 192 characters is accepted as the page counts it and
// stored reduced to 192 octets, the String192 storage bound the DER control
// cancel route holds its own reason to, on the grant and on its controls.
func TestFRWrite_ReasonIsStoredInAtMost192Octets(t *testing.T) {
	f := newFRWFixture(t)
	ctx := context.Background()
	g := f.granted("frq-1", "REQ-1")
	exec := f.execute(g, f.base, -2000)
	// U+65E5 is three octets: 100 of them are 300 octets.
	reason := strings.Repeat("\u65e5", 100)
	body, err := json.Marshal(map[string]any{"reason": reason})
	if err != nil {
		t.Fatal(err)
	}
	if rec := f.post("cancel", "frq-1", string(body)); rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	want := strings.Repeat("\u65e5", 64)
	lc, err := f.lcs.Get(ctx, frwEdev, "frq-1")
	if err != nil || lc.CancelReason != want {
		t.Errorf("grant reason = %q (%d octets, %v), want the first 64 characters, 192 octets", lc.CancelReason, len(lc.CancelReason), err)
	}
	clc, err := f.ctrlLcs.Get(ctx, exec.Scope.Key(), exec.ID)
	if err != nil || clc.CancelReason != want {
		t.Errorf("control reason = %q (%d octets, %v), want the same 192 octets", clc.CancelReason, len(clc.CancelReason), err)
	}
}

func TestFRWrite_MultiplierNineIsAccepted(t *testing.T) {
	f := newFRWFixture(t)
	f.pending("frq-1", "REQ-1")
	rec := f.post("answer", "frq-1", `{"decision":"grant","power":{"value":0,"multiplier":-9}}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201: %s", rec.Code, rec.Body.String())
	}
	if p := f.response("frq-1").PowerAvailable; p == nil || p.Multiplier != -9 {
		t.Errorf("power = %+v, want multiplier -9", p)
	}
}

type notFoundGrants struct{}

func (notFoundGrants) MarkCancelled(context.Context, commitment.Grant, string, int64) error {
	return fmt.Errorf("lifecycle: %w", store.ErrNotFound)
}

// A failed revise whose undo also failed may have left part of the change in
// the stores. Its cause wraps NotFound, and it is still a 500 logged as an
// undo failure, never a 404.
func TestFRWrite_UndoFailureIsInternalWhateverItsCauseWraps(t *testing.T) {
	f := newFRWFixture(t)
	f.granted("frq-1", "REQ-1")
	f.h.Revise.Writers.Grants = notFoundGrants{}
	f.h.Revise.Replace = func(edevID string, frp sep2.FlowReservationResponse) (commitment.Replacement, error) {
		rep, err := sources.NewReplacement(f.frps, edevID, frp)
		rep.Delete = func(context.Context) error { return errors.New("delete refused") }
		return rep, err
	}
	rec := f.post("revise", "frq-1", `{"decision":"grant","interval":{"start":`+itoa(f.base)+`,"duration":1800}}`)
	r := decodeRefusal(t, rec)
	if rec.Code != http.StatusInternalServerError || r.Code != "internal" {
		t.Fatalf("revise with a failed undo = %d %+v, want 500 internal", rec.Code, r)
	}
	if logs := f.logs.String(); !strings.Contains(logs, "level=ERROR") || !strings.Contains(logs, "cause=undo_failed") {
		t.Errorf("log = %q, want one ERROR line with cause=undo_failed", logs)
	}
}

// overlayFRP is the response store with one extra response the ledger's own
// store does not hold, so the ledger never resolves that chain member.
type overlayFRP struct {
	*memory.ScopedStore[sep2.FlowReservationResponse]
	id  string
	frp sep2.FlowReservationResponse
}

func (o overlayFRP) Get(ctx context.Context, parentID, id string) (sep2.FlowReservationResponse, error) {
	if parentID == frwEdev && id == o.id {
		return o.frp, nil
	}
	return o.ScopedStore.Get(ctx, parentID, id)
}

// A cancel that settles part of the chain answers with every mRID it
// cancelled and every one it could not reach, never a bare write-failed, and
// what it cancelled is recorded.
func TestFRWrite_PartialCancelNamesWhatWasAndWasNotCancelled(t *testing.T) {
	f := newFRWFixture(t)
	ctx := context.Background()
	g := f.granted("frq-1", "REQ-1")
	ghost := g
	ghost.MRID = "GHOST-REVISION"
	ghost.Href = "/edev/4/frp/frq-1-r1"
	ghost.CreationTime = g.CreationTime + 1
	f.h.Canceller = flowreservation.NewCanceller(f.frqs, overlayFRP{ScopedStore: f.frps, id: "frq-1-r1", frp: ghost}, f.queue, f.ledger, sources.NewWriters(f.issuer, f.lcs))

	rec := f.post("cancel", "frq-1", `{}`)
	r := decodeRefusal(t, rec)
	if rec.Code != http.StatusInternalServerError || r.Code != "cancel_partial" || r.FrqID != "frq-1" {
		t.Fatalf("partial cancel = %d %+v, want 500 cancel_partial", rec.Code, r)
	}
	var full struct {
		Cancelled  []string `json:"cancelled"`
		Unresolved []string `json:"unresolved"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &full); err != nil {
		t.Fatal(err)
	}
	if len(full.Cancelled) != 1 || full.Cancelled[0] != g.MRID || len(full.Unresolved) != 1 || full.Unresolved[0] != "GHOST-REVISION" {
		t.Errorf("cancelled %v unresolved %v, want [%s] and [GHOST-REVISION]", full.Cancelled, full.Unresolved, g.MRID)
	}
	if lc, err := f.lcs.Get(ctx, frwEdev, "frq-1"); err != nil || lc.CancelledAt == nil {
		t.Errorf("grant lifecycle = %+v (%v), want cancelled", lc, err)
	}
	if got := f.answerRecord("frq-1"); got.CancelledBy == nil || got.CancelledBy.Kind != "operator" {
		t.Errorf("cancelledBy = %+v, want the operator", got.CancelledBy)
	}
	if logs := f.logs.String(); !strings.Contains(logs, "write_committed=partial") || !strings.Contains(logs, "unresolved_mrids=GHOST-REVISION") {
		t.Errorf("log = %q, want the partial write and the unresolved mRID named", logs)
	}
}

// A write that committed but whose view cannot be read says so, rather than
// that the write failed.
func TestFRWrite_ViewFailureAfterACommitSaysTheWriteStands(t *testing.T) {
	f := newFRWFixture(t)
	f.pending("frq-1", "REQ-1")
	f.h.Lifecycles = failingLifecycles{}
	rec := f.post("answer", "frq-1", `{"decision":"grant"}`)
	r := decodeRefusal(t, rec)
	if rec.Code != http.StatusInternalServerError || r.Code != "view_after_write" || r.FrqID != "frq-1" {
		t.Fatalf("answer with an unreadable view = %d %+v, want 500 view_after_write", rec.Code, r)
	}
	f.response("frq-1")
}

type failingLifecycles struct{}

func (failingLifecycles) Get(context.Context, string, string) (dercontrol.LifecycleRecord, error) {
	return dercontrol.LifecycleRecord{}, errors.New("lifecycle store down")
}

// An answer over a record a crash left behind replaces it (design 5.7).
func TestFRWrite_AnswerReplacesAnOrphanRecord(t *testing.T) {
	f := newFRWFixture(t)
	f.pending("frq-1", "REQ-1")
	orphan := flowreservation.AnswerRecord{Action: flowreservation.ActionAnswer, By: flowreservation.Attribution{Kind: flowreservation.KindDeadlineFallback, At: 5},
		CancelledBy: &flowreservation.Attribution{Kind: flowreservation.KindClient, At: 6}}
	if err := f.answers.Create(context.Background(), frwEdev, "frq-1", orphan); err != nil {
		t.Fatal(err)
	}
	if rec := f.post("answer", "frq-1", `{"decision":"grant"}`); rec.Code != http.StatusCreated {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	got := f.answerRecord("frq-1")
	if got.By.Kind != "operator" || got.By.Principal != "admin-key" || got.CancelledBy != nil {
		t.Errorf("record = %+v, want the operator's answer with no cancel", got)
	}
}

func nonAdminCertificate(t *testing.T) *x509.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(9), NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour)}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return cert
}

// A Bearer caller that also presents a verified certificate without the
// admin policy is admitted by the key, so the key is what is recorded.
func TestFRWrite_BearerWithACertificateRecordsTheAdminKey(t *testing.T) {
	f := newFRWFixture(t)
	f.pending("frq-1", "REQ-1")
	req := httptest.NewRequest(http.MethodPost, "/api/derms/flow-reservations/4/frq-1/answer", strings.NewReader(`{"decision":"grant"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+frwKey)
	req.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{nonAdminCertificate(t)}}
	rec := httptest.NewRecorder()
	f.router().ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	if got := f.answerRecord("frq-1"); got.By.Admission != "bearer" || got.By.Principal != "admin-key" {
		t.Errorf("by = %+v, want bearer admin-key", got.By)
	}
}

// After a failed undo leaves an earlier grant live and the tip has ended,
// the cancel the refusal text points to still reaches the earlier grant.
func TestFRWrite_EarlierLiveGrantIsCancellableAfterTheTipEnded(t *testing.T) {
	f := newFRWFixture(t)
	ctx := context.Background()
	now := time.Now().Unix()
	f.request(frqSpec{edev: frwEdev, id: "frq-1", mrid: "REQ-1", created: now - 900,
		interval: &sep2.DateTimeInterval{Start: now - 600, Duration: 3600}, energy: &sep2.SignedRealEnergy{Value: 100}, power: &sep2.ActivePower{Value: 100}})
	old, err := f.queue.Answer(ctx, frwEdev, "frq-1", flowreservation.Decision{})
	if err != nil {
		t.Fatal(err)
	}
	tip := old
	tip.MRID = "ENDED-REVISION"
	tip.CreationTime = old.CreationTime + 1
	tip.Href = "/edev/4/frp/frq-1-r1"
	tip.Interval = &sep2.DateTimeInterval{Start: now - 600, Duration: 300}
	if err := f.frps.Create(ctx, frwEdev, "frq-1-r1", tip); err != nil {
		t.Fatal(err)
	}
	rec := f.post("cancel", "frq-1", `{}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("cancel = %d %s, want 200", rec.Code, rec.Body.String())
	}
	if lc, err := f.lcs.Get(ctx, frwEdev, "frq-1"); err != nil || lc.CancelledAt == nil {
		t.Errorf("earlier grant lifecycle = %+v (%v), want cancelled", lc, err)
	}
}

// A revise refused by another request's grant is the plain window refusal,
// not the earlier-answer one.
func TestFRWrite_ReviseRefusedByAnotherRequestsGrant(t *testing.T) {
	f := newFRWFixture(t)
	other := f.granted("frq-1", "REQ-1")
	f.request(frqSpec{edev: frwEdev, id: "frq-2", mrid: "REQ-2", created: time.Now().Unix(),
		interval: &sep2.DateTimeInterval{Start: f.base, Duration: 7200}, energy: &sep2.SignedRealEnergy{Value: 100}, power: &sep2.ActivePower{Value: 100}})
	if _, err := f.queue.Answer(context.Background(), frwEdev, "frq-2", flowreservation.Decision{Interval: &sep2.DateTimeInterval{Start: f.base + 3600, Duration: 3600}}); err != nil {
		t.Fatal(err)
	}
	before := f.snapshot()
	rec := f.post("revise", "frq-2", `{"decision":"grant","interval":{"start":`+itoa(f.base)+`,"duration":3600}}`)
	r := decodeRefusal(t, rec)
	if rec.Code != http.StatusConflict || r.Code != "fleet_window_committed" || r.MRID != other.MRID {
		t.Fatalf("revise = %d %+v, want 409 fleet_window_committed naming %s", rec.Code, r, other.MRID)
	}
	if strings.Contains(r.Error, "earlier answer") {
		t.Errorf("error = %q, want the plain window text: the conflict is another request's grant", r.Error)
	}
	if after := f.snapshot(); after != before {
		t.Errorf("refused revise stored something")
	}
}

// A reason whose 192-octet cut lands inside a character is stored as the
// longest whole-character prefix, valid UTF-8, on the grant and on its
// controls alike.
func TestFRWrite_ReasonCutInsideACharacterKeepsWholeCharacters(t *testing.T) {
	f := newFRWFixture(t)
	ctx := context.Background()
	g := f.granted("frq-1", "REQ-1")
	exec := f.execute(g, f.base, -2000)
	// One octet then three-octet characters: octet 192 falls inside the 64th.
	reason := "a" + strings.Repeat("\u65e5", 100)
	body, err := json.Marshal(map[string]any{"reason": reason})
	if err != nil {
		t.Fatal(err)
	}
	if rec := f.post("cancel", "frq-1", string(body)); rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	want := "a" + strings.Repeat("\u65e5", 63)
	lc, err := f.lcs.Get(ctx, frwEdev, "frq-1")
	if err != nil || lc.CancelReason != want || !utf8.ValidString(lc.CancelReason) {
		t.Errorf("grant reason: %d octets, valid %v (%v), want the %d-octet whole-character prefix", len(lc.CancelReason), utf8.ValidString(lc.CancelReason), err, len(want))
	}
	clc, err := f.ctrlLcs.Get(ctx, exec.Scope.Key(), exec.ID)
	if err != nil || clc.CancelReason != want || !utf8.ValidString(clc.CancelReason) {
		t.Errorf("control reason: %d octets, valid %v (%v), want the %d-octet whole-character prefix", len(clc.CancelReason), utf8.ValidString(clc.CancelReason), err, len(want))
	}
}

// The multiplier bound is -9..9 inclusive at both ends.
func TestFRWrite_MultiplierNineIsTheUpperBound(t *testing.T) {
	f := newFRWFixture(t)
	f.pending("frq-1", "REQ-1")
	f.pending("frq-2", "REQ-2")
	rec := f.post("answer", "frq-2", `{"decision":"grant","power":{"value":0,"multiplier":10}}`)
	if r := decodeRefusal(t, rec); rec.Code != http.StatusBadRequest || r.Code != "multiplier_out_of_range" {
		t.Errorf("multiplier 10 = %d %+v, want 400 multiplier_out_of_range", rec.Code, r)
	}
	rec = f.post("answer", "frq-1", `{"decision":"grant","power":{"value":0,"multiplier":9}}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("multiplier 9: status = %d, want 201: %s", rec.Code, rec.Body.String())
	}
	if p := f.response("frq-1").PowerAvailable; p == nil || p.Multiplier != 9 {
		t.Errorf("power = %+v, want multiplier 9", p)
	}
}

type fakeAnswerer struct{ err error }

func (a fakeAnswerer) Answer(context.Context, string, string, flowreservation.Decision) (sep2.FlowReservationResponse, error) {
	return sep2.FlowReservationResponse{}, a.err
}

// A failed undo or commitment check is the server's failure even when its
// cause wraps a refusal's sentinel: answer and cancel give 500, never a 4xx.
func TestFRWrite_InternalFailureIsNeverAnsweredAsARefusal(t *testing.T) {
	cases := []struct {
		name, op, body, cause string
		set                   func(f *frwFixture)
	}{
		{"answer: check wrapping not found", "answer", `{"decision":"grant"}`, "commitment_check", func(f *frwFixture) {
			f.pending("frq-1", "REQ-1")
			f.h.Queue = fakeAnswerer{fmt.Errorf("%w: %w", flowreservation.ErrCommitmentCheck, store.ErrNotFound)}
		}},
		{"answer: undo wrapping already answered", "answer", `{"decision":"grant"}`, "undo_failed", func(f *frwFixture) {
			f.pending("frq-1", "REQ-1")
			f.h.Queue = fakeAnswerer{fmt.Errorf("%w: %w", commitment.ErrUndo, flowreservation.ErrAlreadyAnswered)}
		}},
		{"cancel: undo wrapping not found", "cancel", `{}`, "undo_failed", func(f *frwFixture) {
			f.granted("frq-1", "REQ-1")
			f.h.Canceller = fakeCanceller{err: fmt.Errorf("%w: %w", commitment.ErrUndo, store.ErrNotFound)}
		}},
		{"cancel: check wrapping chain moving", "cancel", `{}`, "commitment_check", func(f *frwFixture) {
			f.granted("frq-1", "REQ-1")
			f.h.Canceller = fakeCanceller{err: fmt.Errorf("%w: %w", flowreservation.ErrCommitmentCheck, flowreservation.ErrChainMoving)}
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFRWFixture(t)
			c.set(f)
			f.logs.Reset()
			rec := f.post(c.op, "frq-1", c.body)
			if r := decodeRefusal(t, rec); rec.Code != http.StatusInternalServerError || r.Code != "internal" {
				t.Fatalf("%s = %d %+v, want 500 internal", c.op, rec.Code, r)
			}
			if logs := f.logs.String(); !strings.Contains(logs, "level=ERROR") || !strings.Contains(logs, "cause="+c.cause) {
				t.Errorf("log = %q, want an ERROR line with cause=%s", logs, c.cause)
			}
		})
	}
}

// A response exists, so the answer still gets 409, but an answer record the
// queue could not take back is the server's fault and is logged at ERROR
// with its own cause.
func TestFRWrite_AnswerTakeBackFailureIs409LoggedAtError(t *testing.T) {
	f := newFRWFixture(t)
	f.pending("frq-1", "REQ-1")
	f.h.Queue = fakeAnswerer{fmt.Errorf("%w: %w", flowreservation.ErrAlreadyAnswered, flowreservation.ErrAnswerRecordTakeBack)}
	f.logs.Reset()
	rec := f.post("answer", "frq-1", `{"decision":"grant"}`)
	if r := decodeRefusal(t, rec); rec.Code != http.StatusConflict || r.Code != "already_answered" {
		t.Fatalf("answer = %d %+v, want 409 already_answered", rec.Code, r)
	}
	logs := f.logs.String()
	assertOneSafeLine(t, logs, "event=flow_reservation_answer_refused")
	if !strings.Contains(logs, "level=ERROR") || !strings.Contains(logs, "cause=answer_record_take_back") {
		t.Errorf("log = %q, want an ERROR line with cause=answer_record_take_back", logs)
	}
}

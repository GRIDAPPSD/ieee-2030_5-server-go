package dercontrol

import (
	"context"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/sep2time"
)

// Acceptance criterion 8: cancel records the cancellation time and optional
// reason, keeps the control stored, and refuses a control that is already
// cancelled, superseded or ended. Cancelling a control does not reinstate
// one it superseded.

func mustIssueScheduled(t *testing.T, h *testHarness, scope [3]string) Result {
	t.Helper()
	start := sep2time.Now().Unix() + 1000
	res, err := h.issuer.Issue(context.Background(), CreateRequest{
		DERProgramHref:  programHref(scope[0], scope[1], scope[2]),
		Type:            Connect,
		Start:           &start,
		DurationSeconds: 1000,
	})
	if err != nil {
		t.Fatalf("issue scheduled control: %v", err)
	}
	return res
}

func TestCancel_RecordsTimeAndReason_KeepsControlStored(t *testing.T) {
	h := newHarness(t, Config{PEN: testPEN(1)})
	h.seedProgram(t, "dev1", "p1", controlListHref("dev1", "0", "p1"))
	res := mustIssueScheduled(t, h, [3]string{"dev1", "0", "p1"})

	before := sep2time.Now().Unix()
	lc, err := h.issuer.Cancel(context.Background(), res.Scope, res.ID, "operator stop")
	after := sep2time.Now().Unix()
	if err != nil {
		t.Fatalf("Cancel() error = %v", err)
	}
	if lc.CancelledAt == nil || *lc.CancelledAt < before || *lc.CancelledAt > after {
		t.Fatalf("CancelledAt = %v, want between %d and %d", lc.CancelledAt, before, after)
	}
	if lc.CancelReason != "operator stop" {
		t.Fatalf("CancelReason = %q, want %q", lc.CancelReason, "operator stop")
	}

	stored, err := h.controls.Get(context.Background(), "dev1/0/p1", res.ID)
	if err != nil {
		t.Fatalf("control was removed by Cancel: %v", err)
	}
	if stored.MRID != res.Control.MRID {
		t.Fatalf("stored.MRID = %q, want %q (control must stay exactly as issued)", stored.MRID, res.Control.MRID)
	}
}

func TestCancel_RefusesAlreadyCancelled(t *testing.T) {
	h := newHarness(t, Config{PEN: testPEN(1)})
	h.seedProgram(t, "dev1", "p1", controlListHref("dev1", "0", "p1"))
	res := mustIssueScheduled(t, h, [3]string{"dev1", "0", "p1"})

	if _, err := h.issuer.Cancel(context.Background(), res.Scope, res.ID, ""); err != nil {
		t.Fatalf("first Cancel() error = %v", err)
	}
	_, err := h.issuer.Cancel(context.Background(), res.Scope, res.ID, "")
	assertRefusal(t, err, RefusalAlreadyCancelled)
}

func TestCancel_RefusesAlreadySuperseded(t *testing.T) {
	h := newHarness(t, Config{PEN: testPEN(1)})
	h.seedProgram(t, "dev1", "p1", controlListHref("dev1", "0", "p1"))
	res := mustIssueScheduled(t, h, [3]string{"dev1", "0", "p1"})

	// Drive the lifecycle record directly to "superseded, effective in the
	// past" rather than relying on Issue's supersede path (covered in
	// supersede_test.go) plus a wall-clock wait: this isolates Cancel's own
	// refusal logic from timing.
	scopeKey := "dev1/0/p1"
	past := sep2time.Now().Unix() - 10
	if err := h.lifecycles.Update(context.Background(), scopeKey, res.ID, LifecycleRecord{
		SupersededAt: &past,
		SupersededBy: "DEADBEEF00000000000000000000001",
	}); err != nil {
		t.Fatalf("seed superseded lifecycle: %v", err)
	}

	_, err := h.issuer.Cancel(context.Background(), res.Scope, res.ID, "")
	assertRefusal(t, err, RefusalAlreadySuperseded)
}

func TestCancel_AllowedBeforeSupersedeTakesEffect(t *testing.T) {
	h := newHarness(t, Config{PEN: testPEN(1)})
	h.seedProgram(t, "dev1", "p1", controlListHref("dev1", "0", "p1"))
	res := mustIssueScheduled(t, h, [3]string{"dev1", "0", "p1"})

	// SupersededAt is set but still in the future: the design's derived
	// status keeps this control Scheduled/Active until then, so Cancel must
	// still succeed.
	scopeKey := "dev1/0/p1"
	future := sep2time.Now().Unix() + 10000
	if err := h.lifecycles.Update(context.Background(), scopeKey, res.ID, LifecycleRecord{
		SupersededAt: &future,
		SupersededBy: "DEADBEEF00000000000000000000001",
	}); err != nil {
		t.Fatalf("seed future-superseded lifecycle: %v", err)
	}

	if _, err := h.issuer.Cancel(context.Background(), res.Scope, res.ID, "still cancellable"); err != nil {
		t.Fatalf("Cancel() error = %v, want accepted before the supersede takes effect", err)
	}
}

func TestCancel_RefusesEnded(t *testing.T) {
	h := newHarness(t, Config{PEN: testPEN(1)})
	h.seedProgram(t, "dev1", "p1", controlListHref("dev1", "0", "p1"))
	res := mustIssueScheduled(t, h, [3]string{"dev1", "0", "p1"})

	// Rewrite the stored interval to one that has already ended, isolating
	// Cancel's "ended" refusal from a real wall-clock wait.
	scopeKey := "dev1/0/p1"
	ended := res.Control
	ended.Interval = &sep2.DateTimeInterval{Start: sep2time.Now().Unix() - 2000, Duration: 1000}
	if err := h.controls.Update(context.Background(), scopeKey, res.ID, ended); err != nil {
		t.Fatalf("seed ended interval: %v", err)
	}

	_, err := h.issuer.Cancel(context.Background(), res.Scope, res.ID, "")
	assertRefusal(t, err, RefusalEnded)
}

func TestCancel_RefusesUnknownControl(t *testing.T) {
	h := newHarness(t, Config{PEN: testPEN(1)})
	h.seedProgram(t, "dev1", "p1", controlListHref("dev1", "0", "p1"))

	_, err := h.issuer.Cancel(context.Background(), Scope{EndDeviceID: "dev1", FSAID: "0", DERProgramID: "p1"}, "no-such-id", "")
	assertRefusal(t, err, RefusalControlNotFound)
}

// TestCancel_RefusesAtExactSupersedeInstant proves the supersede refusal
// boundary is inclusive: the existing already-superseded test uses
// now-10, never the exact instant, so a mutant changing supersededAsOf's
// ">=" to ">" would survive without this.
func TestCancel_RefusesAtExactSupersedeInstant(t *testing.T) {
	h := newHarness(t, Config{PEN: testPEN(1)})
	h.seedProgram(t, "dev1", "p1", controlListHref("dev1", "0", "p1"))
	res := mustIssueScheduled(t, h, [3]string{"dev1", "0", "p1"})

	scopeKey := "dev1/0/p1"
	at := sep2time.Now().Unix()
	seeded := LifecycleRecord{SupersededAt: &at, SupersededBy: "DEADBEEF00000000000000000000001"}
	if err := h.lifecycles.Update(context.Background(), scopeKey, res.ID, seeded); err != nil {
		t.Fatalf("seed superseded-at-now lifecycle: %v", err)
	}

	_, err := h.issuer.Cancel(context.Background(), res.Scope, res.ID, "")
	assertRefusal(t, err, RefusalAlreadySuperseded)

	after, err := h.lifecycles.Get(context.Background(), scopeKey, res.ID)
	if err != nil {
		t.Fatalf("load lifecycle after refused Cancel: %v", err)
	}
	if after.SupersededAt == nil || *after.SupersededAt != at || after.SupersededBy != seeded.SupersededBy {
		t.Fatalf("lifecycle changed after refused Cancel: got %+v, want SupersededAt=%d SupersededBy=%q", after, at, seeded.SupersededBy)
	}
}

// TestCancel_RefusesAtExactEndInstant proves the "ended" refusal boundary
// is inclusive: the existing refused-ended test sets an end 1000 seconds
// in the past, never the exact instant, so a mutant changing the end
// comparison's ">=" to ">" would survive without this.
func TestCancel_RefusesAtExactEndInstant(t *testing.T) {
	h := newHarness(t, Config{PEN: testPEN(1)})
	h.seedProgram(t, "dev1", "p1", controlListHref("dev1", "0", "p1"))
	res := mustIssueScheduled(t, h, [3]string{"dev1", "0", "p1"})

	scopeKey := "dev1/0/p1"
	now := sep2time.Now().Unix()
	ended := res.Control
	ended.Interval = &sep2.DateTimeInterval{Start: now - 500, Duration: 500} // ends exactly now
	if err := h.controls.Update(context.Background(), scopeKey, res.ID, ended); err != nil {
		t.Fatalf("seed exact-end interval: %v", err)
	}

	_, err := h.issuer.Cancel(context.Background(), res.Scope, res.ID, "")
	assertRefusal(t, err, RefusalEnded)

	afterLC, err := h.lifecycles.Get(context.Background(), scopeKey, res.ID)
	if err != nil {
		t.Fatalf("load lifecycle after refused Cancel: %v", err)
	}
	if afterLC.CancelledAt != nil {
		t.Fatalf("lifecycle CancelledAt = %v after refused Cancel, want nil", afterLC.CancelledAt)
	}
}

// TestCancel_RefusalLeavesControlAndLifecycleUnchanged proves a refused
// Cancel call writes nothing: a mutant that writes CancelledAt to the
// lifecycle store before the already-cancelled check runs would silently
// overwrite the first cancellation's recorded time and reason.
func TestCancel_RefusalLeavesControlAndLifecycleUnchanged(t *testing.T) {
	h := newHarness(t, Config{PEN: testPEN(1)})
	h.seedProgram(t, "dev1", "p1", controlListHref("dev1", "0", "p1"))
	res := mustIssueScheduled(t, h, [3]string{"dev1", "0", "p1"})

	if _, err := h.issuer.Cancel(context.Background(), res.Scope, res.ID, "first"); err != nil {
		t.Fatalf("first Cancel() error = %v", err)
	}

	scopeKey := "dev1/0/p1"
	beforeLC, err := h.lifecycles.Get(context.Background(), scopeKey, res.ID)
	if err != nil {
		t.Fatalf("load lifecycle after first Cancel: %v", err)
	}
	beforeCtrl, err := h.controls.Get(context.Background(), scopeKey, res.ID)
	if err != nil {
		t.Fatalf("load control after first Cancel: %v", err)
	}

	_, err = h.issuer.Cancel(context.Background(), res.Scope, res.ID, "second")
	assertRefusal(t, err, RefusalAlreadyCancelled)

	afterLC, err := h.lifecycles.Get(context.Background(), scopeKey, res.ID)
	if err != nil {
		t.Fatalf("load lifecycle after refused Cancel: %v", err)
	}
	if afterLC.CancelledAt == nil || *afterLC.CancelledAt != *beforeLC.CancelledAt || afterLC.CancelReason != beforeLC.CancelReason {
		t.Fatalf("lifecycle changed after refused Cancel: before = %+v, after = %+v", beforeLC, afterLC)
	}
	afterCtrl, err := h.controls.Get(context.Background(), scopeKey, res.ID)
	if err != nil {
		t.Fatalf("load control after refused Cancel: %v", err)
	}
	if afterCtrl.MRID != beforeCtrl.MRID {
		t.Fatalf("control changed after refused Cancel: before MRID = %q, after = %q", beforeCtrl.MRID, afterCtrl.MRID)
	}
}

func TestCancel_DoesNotReinstateSupersededControl(t *testing.T) {
	h := newHarness(t, Config{PEN: testPEN(1)})
	h.seedProgram(t, "dev1", "p1", controlListHref("dev1", "0", "p1"))

	start := sep2time.Now().Unix() + 1000
	c, err := issueWith(t, h, func(r *CreateRequest) {
		r.Type = Connect
		r.Start = &start
		r.DurationSeconds = 1000
	})
	if err != nil {
		t.Fatalf("issue C: %v", err)
	}
	nStart := start + 500
	n, err := issueWith(t, h, func(r *CreateRequest) {
		r.Type = Disconnect
		r.Start = &nStart
		r.DurationSeconds = 1000
	})
	if err != nil {
		t.Fatalf("issue N (supersedes C): %v", err)
	}
	if len(n.Supersedes) != 1 {
		t.Fatalf("test setup: N did not supersede C, Supersedes = %v", n.Supersedes)
	}

	if _, err := h.issuer.Cancel(context.Background(), n.Scope, n.ID, "cancel N"); err != nil {
		t.Fatalf("Cancel(N) error = %v", err)
	}

	lc, err := h.lifecycles.Get(context.Background(), "dev1/0/p1", c.ID)
	if err != nil {
		t.Fatalf("load C's lifecycle: %v", err)
	}
	if lc.SupersededAt == nil || *lc.SupersededAt != nStart {
		t.Fatalf("C.SupersededAt = %v, want still %d (cancelling N must not reinstate C)", lc.SupersededAt, nStart)
	}
	if lc.SupersededBy != n.Control.MRID {
		t.Fatalf("C.SupersededBy = %q, want still %q", lc.SupersededBy, n.Control.MRID)
	}
}

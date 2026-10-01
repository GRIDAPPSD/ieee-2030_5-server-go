package dercontrol

import (
	"context"
	"errors"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/sep2time"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

// Cancel and Relink return the record they read alongside a plain error
// that wrote nothing. These pin that the returned record equals the stored
// one, field by field, for each such return.

// cancelOnGet cancels the caller's context once the lifecycle record has
// been read, so Cancel reaches its ctx check after both reads succeed.
type cancelOnGet struct {
	*memory.ScopedStore[LifecycleRecord]
	cancel context.CancelFunc
}

func (s cancelOnGet) Get(ctx context.Context, parentID, id string) (LifecycleRecord, error) {
	rec, err := s.ScopedStore.Get(ctx, parentID, id)
	s.cancel()
	return rec, err
}

// linkedRecord is a lifecycle record with every field Cancel and Relink
// leave alone set, so a returned zero or partial record cannot pass.
func linkedRecord() LifecycleRecord {
	return LifecycleRecord{GrantMRID: "AAAA", FleetKey: "LFDI-AGG", Reach: 3}
}

func assertRecordAsRead(t *testing.T, got LifecycleRecord, lifecycles lifecycleStore, id string) {
	t.Helper()
	stored, err := lifecycles.Get(context.Background(), linkScopeKey, id)
	if err != nil {
		t.Fatalf("load lifecycle: %v", err)
	}
	want := linkedRecord()
	if got.GrantMRID != want.GrantMRID || got.FleetKey != want.FleetKey || got.Reach != want.Reach || got.CancelledAt != nil || got.SupersededAt != nil {
		t.Fatalf("returned record = %+v, want %+v as read", got, want)
	}
	if stored.GrantMRID != got.GrantMRID || stored.FleetKey != got.FleetKey || stored.Reach != got.Reach || stored.CancelledAt != nil {
		t.Fatalf("stored record = %+v, want it unchanged and equal to the returned %+v", stored, got)
	}
}

func seedLinkedControl(t *testing.T, controls *memory.ScopedStore[sep2.DERControl], lifecycles lifecycleStore, id string, interval *sep2.DateTimeInterval) {
	t.Helper()
	var ctrl sep2.DERControl
	ctrl.Interval = interval
	ctrl.Href = controlListHref("dev1", "0", "p1") + "/" + id
	if err := controls.Create(context.Background(), linkScopeKey, id, ctrl); err != nil {
		t.Fatalf("seed control: %v", err)
	}
	if err := lifecycles.Create(context.Background(), linkScopeKey, id, linkedRecord()); err != nil {
		t.Fatalf("seed lifecycle: %v", err)
	}
}

func TestCancel_ContextDoneAfterReadsReturnsRecordAsRead(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	lifecycles := cancelOnGet{ScopedStore: memory.NewScopedStore[LifecycleRecord](), cancel: cancel}
	controls := memory.NewScopedStore[sep2.DERControl]()
	issuer, err := NewIssuer(memory.NewScopedStore[sep2.DERProgram](), controls, lifecycles, Config{PEN: testPEN(1)})
	if err != nil {
		t.Fatalf("NewIssuer() error = %v", err)
	}
	start := sep2time.Now().Unix() + 1000
	seedLinkedControl(t, controls, lifecycles.ScopedStore, "c1", &sep2.DateTimeInterval{Start: start, Duration: 600})

	got, err := issuer.Cancel(ctx, Scope{EndDeviceID: "dev1", FSAID: "0", DERProgramID: "p1"}, "c1", "stop")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Cancel() error = %v, want context.Canceled", err)
	}
	assertRecordAsRead(t, got, lifecycles.ScopedStore, "c1")
}

func TestCancel_NoIntervalReturnsRecordAsRead(t *testing.T) {
	h := newLinkHarness(t)
	seedLinkedControl(t, h.controls, h.lifecycles, "c1", nil)

	got, err := h.issuer.Cancel(context.Background(), Scope{EndDeviceID: "dev1", FSAID: "0", DERProgramID: "p1"}, "c1", "stop")
	var refusal *RefusalError
	if err == nil || errors.As(err, &refusal) {
		t.Fatalf("Cancel() error = %v, want a plain error", err)
	}
	assertRecordAsRead(t, got, h.lifecycles, "c1")
}

func TestRelink_NotExecutionReturnsRecordAsRead(t *testing.T) {
	h := newLinkHarness(t)
	start := sep2time.Now().Unix() + 1000
	seedLinkedControl(t, h.controls, h.lifecycles, "c1", &sep2.DateTimeInterval{Start: start, Duration: 600})

	got, err := h.issuer.Relink(context.Background(), Scope{EndDeviceID: "dev1", FSAID: "0", DERProgramID: "p1"}, "c1", "")
	if !errors.Is(err, ErrNotExecution) {
		t.Fatalf("Relink() error = %v, want ErrNotExecution", err)
	}
	assertRecordAsRead(t, got, h.lifecycles, "c1")
}

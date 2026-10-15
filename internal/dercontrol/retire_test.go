package dercontrol

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/sep2time"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
)

// #922: a program is removed only while none of its scopes holds a live
// control, and its control records are kept.

func retireRemoved(t *testing.T, h *testHarness, scopes []Scope) (bool, error) {
	t.Helper()
	ran := false
	err := h.issuer.Retire(context.Background(), scopes, func(context.Context) error {
		ran = true
		return nil
	})
	return ran, err
}

func TestRetire_RefusesAScheduledControlAndRunsAfterCancel(t *testing.T) {
	h := newHarness(t, Config{PEN: testPEN(1)})
	h.seedProgram(t, "dev1", "p1", controlListHref("dev1", "0", "p1"))
	res := mustIssueScheduled(t, h, [3]string{"dev1", "0", "p1"})

	ran, err := retireRemoved(t, h, []Scope{res.Scope})
	var live *LiveControlsError
	if !errors.As(err, &live) || ran {
		t.Fatalf("Retire = %v, removed %v; want *LiveControlsError and no removal", err, ran)
	}
	if !slices.Equal(live.Controls, []string{res.Href}) {
		t.Errorf("live controls = %v, want [%s]", live.Controls, res.Href)
	}

	if _, err := h.issuer.Cancel(context.Background(), res.Scope, res.ID, ""); err != nil {
		t.Fatal(err)
	}
	if ran, err := retireRemoved(t, h, []Scope{res.Scope}); err != nil || !ran {
		t.Fatalf("Retire after cancel = %v, removed %v; want nil and a removal", err, ran)
	}
	if c, err := h.controls.Get(context.Background(), res.Scope.Key(), res.ID); err != nil || c.MRID != res.Control.MRID {
		t.Errorf("control after Retire = %+v, %v; want it kept", c, err)
	}
	if lc, err := h.lifecycles.Get(context.Background(), res.Scope.Key(), res.ID); err != nil || lc.CancelledAt == nil {
		t.Errorf("lifecycle after Retire = %+v, %v; want the cancelled record kept", lc, err)
	}
}

func TestRetire_JudgesEachControl(t *testing.T) {
	now := sep2time.Now().Unix()
	past, future := now-10, now+1000
	for _, tc := range []struct {
		name      string
		interval  *sep2.DateTimeInterval
		lifecycle *LifecycleRecord
		status    *sep2.EventStatus
		live      bool
	}{
		{"scheduled", &sep2.DateTimeInterval{Start: future, Duration: 60}, &LifecycleRecord{}, nil, true},
		{"active", &sep2.DateTimeInterval{Start: past, Duration: 600}, &LifecycleRecord{}, nil, true},
		{"ended", &sep2.DateTimeInterval{Start: now - 600, Duration: 60}, &LifecycleRecord{}, nil, false},
		{"cancelled", &sep2.DateTimeInterval{Start: future, Duration: 60}, &LifecycleRecord{CancelledAt: &past}, nil, false},
		{"superseded", &sep2.DateTimeInterval{Start: past, Duration: 600}, &LifecycleRecord{SupersededAt: &past}, nil, false},
		{"superseded later", &sep2.DateTimeInterval{Start: past, Duration: 600}, &LifecycleRecord{SupersededAt: &future}, nil, true},
		{"not issued here, scheduled", &sep2.DateTimeInterval{Start: future, Duration: 60}, nil, nil, true},
		{"not issued here, cancelled", &sep2.DateTimeInterval{Start: future, Duration: 60}, nil, &sep2.EventStatus{CurrentStatus: sep2.EventStatusCancelled}, false},
		{"not issued here, cancelled with randomization", &sep2.DateTimeInterval{Start: future, Duration: 60}, nil, &sep2.EventStatus{CurrentStatus: 3}, false},
		{"not issued here, superseded", &sep2.DateTimeInterval{Start: future, Duration: 60}, nil, &sep2.EventStatus{CurrentStatus: sep2.EventStatusSuperseded}, false},
		{"not issued here, active", &sep2.DateTimeInterval{Start: past, Duration: 600}, nil, &sep2.EventStatus{CurrentStatus: sep2.EventStatusActive}, true},
		{"no interval", nil, nil, nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, Config{PEN: testPEN(1)})
			scope := Scope{EndDeviceID: "dev1", FSAID: "0", DERProgramID: "p1"}
			c := sep2.DERControl{}
			c.Href = controlListHref("dev1", "0", "p1") + "/c1"
			c.Interval = tc.interval
			c.EventStatus = tc.status
			if err := h.controls.Create(context.Background(), scope.Key(), "c1", c); err != nil {
				t.Fatal(err)
			}
			if tc.lifecycle != nil {
				if err := h.lifecycles.Create(context.Background(), scope.Key(), "c1", *tc.lifecycle); err != nil {
					t.Fatal(err)
				}
			}
			ran, err := retireRemoved(t, h, []Scope{scope})
			var live *LiveControlsError
			if got := errors.As(err, &live); got != tc.live || ran == tc.live {
				t.Errorf("Retire = %v, removed %v; want live %v", err, ran, tc.live)
			}
		})
	}
}

func TestRetire_ALiveControlInAnyScopeRefusesAll(t *testing.T) {
	h := newHarness(t, Config{PEN: testPEN(1)})
	h.seedProgram(t, "dev2", "p1", controlListHref("dev2", "0", "p1"))
	res := mustIssueScheduled(t, h, [3]string{"dev2", "0", "p1"})
	quiet := Scope{EndDeviceID: "dev1", FSAID: "0", DERProgramID: "p1"}
	if ran, err := retireRemoved(t, h, []Scope{quiet, res.Scope, quiet}); err == nil || ran {
		t.Fatalf("Retire = %v, removed %v; want a refusal from the second scope", err, ran)
	}
}

// gatedPrograms pauses after the first read of one program until gate
// closes, so the read it returns is stale by then.
type gatedPrograms struct {
	programStore
	derp    string
	gate    chan struct{}
	entered chan struct{}
	gets    int
}

func (g *gatedPrograms) Get(ctx context.Context, parentID, id string) (sep2.DERProgram, error) {
	p, err := g.programStore.Get(ctx, parentID, id)
	if id == g.derp {
		g.gets++
		if g.gets == 1 {
			close(g.entered)
			<-g.gate
		}
	}
	return p, err
}

// An Issue that read the program before a Retire removed it must not store
// a control the Retire's check never saw.
func TestIssue_RefusesAProgramRetiredAfterItsFirstRead(t *testing.T) {
	h := newHarness(t, Config{PEN: testPEN(1)})
	h.seedProgram(t, "dev1", "p1", controlListHref("dev1", "0", "p1"))
	g := &gatedPrograms{programStore: h.programs, derp: "p1", gate: make(chan struct{}), entered: make(chan struct{})}
	h.issuer.programs = g

	issued := make(chan error, 1)
	go func() {
		start := sep2time.Now().Unix() + 1000
		_, err := h.issuer.Issue(context.Background(), CreateRequest{
			DERProgramHref: programHref("dev1", "0", "p1"), Type: Connect, Start: &start, DurationSeconds: 1000,
		})
		issued <- err
	}()
	<-g.entered
	scope := Scope{EndDeviceID: "dev1", FSAID: "0", DERProgramID: "p1"}
	if err := h.issuer.Retire(context.Background(), []Scope{scope}, func(ctx context.Context) error {
		return h.programs.Delete(ctx, "dev1", "p1")
	}); err != nil {
		t.Fatalf("Retire: %v", err)
	}
	close(g.gate)
	assertRefusal(t, <-issued, RefusalProgramNotFound)
	if list, err := h.controls.List(context.Background(), scope.Key(), store.ListOptions{Unbounded: true}); err != nil || len(list.Items) != 0 {
		t.Errorf("controls after the refused Issue = %+v, %v; want none", list.Items, err)
	}
}

// A control is live up to, and not at, the second its interval ends.
func TestLiveControls_IntervalEndSecond(t *testing.T) {
	const now = int64(1_800_000_000)
	for _, tc := range []struct {
		end  int64
		live bool
	}{{now + 1, true}, {now, false}} {
		h := newHarness(t, Config{PEN: testPEN(1)})
		c := sep2.DERControl{}
		c.Interval = &sep2.DateTimeInterval{Start: tc.end - 60, Duration: 60}
		c.Href = controlListHref("dev1", "0", "p1") + "/c1"
		if err := h.controls.Create(context.Background(), "dev1/0/p1", "c1", c); err != nil {
			t.Fatal(err)
		}
		live, err := h.issuer.liveControls(context.Background(), "dev1/0/p1", now)
		if err != nil || (len(live) == 1) != tc.live {
			t.Errorf("end %d at now %d: live = %v, %v; want live %v", tc.end, now, live, err, tc.live)
		}
	}
}

// gatedControls pauses the first List of one scope until gate closes. Issue
// lists a scope while holding its lock, after its program re-read.
type gatedControls struct {
	controlStore
	scope   string
	gate    chan struct{}
	entered chan struct{}
	once    sync.Once
}

func (g *gatedControls) List(ctx context.Context, parentID string, opts store.ListOptions) (store.ListResult[sep2.DERControl], error) {
	if parentID == g.scope {
		first := false
		g.once.Do(func() { first = true })
		if first {
			close(g.entered)
			<-g.gate
		}
	}
	return g.controlStore.List(ctx, parentID, opts)
}

// A Retire that starts while an Issue holds the scope waits for it, and then
// sees the control the Issue stored.
func TestRetire_WaitsForAnIssueHoldingTheScope(t *testing.T) {
	h := newHarness(t, Config{PEN: testPEN(1)})
	h.seedProgram(t, "dev1", "p1", controlListHref("dev1", "0", "p1"))
	scope := Scope{EndDeviceID: "dev1", FSAID: "0", DERProgramID: "p1"}
	g := &gatedControls{controlStore: h.controls, scope: scope.Key(), gate: make(chan struct{}), entered: make(chan struct{})}
	h.issuer.controls = g

	issued := make(chan error, 1)
	go func() {
		start := sep2time.Now().Unix() + 1000
		_, err := h.issuer.Issue(context.Background(), CreateRequest{
			DERProgramHref: programHref("dev1", "0", "p1"), Type: Connect, Start: &start, DurationSeconds: 1000,
		})
		issued <- err
	}()
	<-g.entered

	removed := false
	retired := make(chan error, 1)
	go func() {
		retired <- h.issuer.Retire(context.Background(), []Scope{scope}, func(context.Context) error {
			removed = true
			return nil
		})
	}()
	select {
	case err := <-retired:
		t.Fatalf("Retire returned %v while Issue held the scope", err)
	case <-time.After(100 * time.Millisecond):
	}
	close(g.gate)
	if err := <-issued; err != nil {
		t.Fatalf("Issue: %v", err)
	}
	var live *LiveControlsError
	if err := <-retired; !errors.As(err, &live) || removed {
		t.Fatalf("Retire = %v, removed %v; want *LiveControlsError and no removal", err, removed)
	}
}

package dercontrol

import (
	"context"
	"errors"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/derhref"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

const stateNow = int64(1_800_000_000)

func TestControlState(t *testing.T) {
	past, future := stateNow-10, stateNow+1000
	iv := func(start, dur int64) *sep2.DateTimeInterval {
		return &sep2.DateTimeInterval{Start: start, Duration: uint32(dur)}
	}
	status := func(v uint8) *sep2.EventStatus { return &sep2.EventStatus{CurrentStatus: v} }
	for _, tc := range []struct {
		name     string
		interval *sep2.DateTimeInterval
		created  int64
		status   *sep2.EventStatus
		lc       *LifecycleRecord
		want     State
	}{
		{"interval contains now", iv(past, 600), 0, nil, &LifecycleRecord{}, InForce},
		{"last second of the interval", iv(stateNow-59, 60), 0, nil, &LifecycleRecord{}, InForce},
		{"first second after the interval", iv(stateNow-60, 60), 0, nil, &LifecycleRecord{}, Retired},
		{"scheduled", iv(future, 60), 0, nil, &LifecycleRecord{}, Scheduled},
		{"creationTime after start is the effective start", iv(past, 600), future, nil, &LifecycleRecord{}, Scheduled},
		{"ended 2018 control whose status says Active", iv(1_530_000_000, 60), 0, status(sep2.EventStatusActive), &LifecycleRecord{}, Retired},
		{"cancelled", iv(past, 600), 0, nil, &LifecycleRecord{CancelledAt: &past}, Retired},
		{"cancelled before it starts", iv(future, 60), 0, nil, &LifecycleRecord{CancelledAt: &past}, Retired},
		{"superseded", iv(past, 600), 0, nil, &LifecycleRecord{SupersededAt: &past}, Retired},
		{"superseded later is still in force", iv(past, 600), 0, nil, &LifecycleRecord{SupersededAt: &future}, InForce},
		{"no record, own status Active", iv(past, 600), 0, status(sep2.EventStatusActive), nil, InForce},
		{"no record, own status Cancelled", iv(past, 600), 0, status(sep2.EventStatusCancelled), nil, Retired},
		{"no record, own status Superseded", iv(past, 600), 0, status(sep2.EventStatusSuperseded), nil, Retired},
		{"no record, no status", iv(past, 600), 0, nil, nil, InForce},
		{"no interval", nil, 0, nil, &LifecycleRecord{}, Retired},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := sep2.DERControl{}
			c.Interval, c.EventStatus, c.CreationTime = tc.interval, tc.status, tc.created
			lc := LifecycleRecord{}
			if tc.lc != nil {
				lc = *tc.lc
			}
			if got := ControlState(c, lc, tc.lc != nil, stateNow); got != tc.want {
				t.Errorf("ControlState = %d, want %d", got, tc.want)
			}
		})
	}
}

func putControl(t *testing.T, h *testHarness, edev string, id string, iv *sep2.DateTimeInterval, lc *LifecycleRecord) {
	t.Helper()
	c := sep2.DERControl{}
	c.Href = controlListHref(edev, "0", "p1") + "/" + id
	c.Interval = iv
	key := edev + "/0/p1"
	if err := h.controls.Create(context.Background(), key, id, c); err != nil {
		t.Fatal(err)
	}
	if lc != nil {
		if err := h.lifecycles.Create(context.Background(), key, id, *lc); err != nil {
			t.Fatal(err)
		}
	}
}

func TestInForceScopes_ReadsEachControlState(t *testing.T) {
	past, future := stateNow-10, stateNow+1000
	for _, tc := range []struct {
		name string
		iv   *sep2.DateTimeInterval
		lc   *LifecycleRecord
		want bool
	}{
		{"in force", &sep2.DateTimeInterval{Start: past, Duration: 600}, &LifecycleRecord{}, true},
		{"scheduled", &sep2.DateTimeInterval{Start: future, Duration: 60}, &LifecycleRecord{}, false},
		{"ended", &sep2.DateTimeInterval{Start: stateNow - 600, Duration: 60}, &LifecycleRecord{}, false},
		{"cancelled", &sep2.DateTimeInterval{Start: past, Duration: 600}, &LifecycleRecord{CancelledAt: &past}, false},
		{"superseded", &sep2.DateTimeInterval{Start: past, Duration: 600}, &LifecycleRecord{SupersededAt: &past}, false},
		{"ended 2018 control", &sep2.DateTimeInterval{Start: 1_530_000_000, Duration: 60}, &LifecycleRecord{}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, Config{PEN: testPEN(1)})
			putControl(t, h, "dev1", "c1", tc.iv, tc.lc)
			got, err := InForceScopes(context.Background(), h.controls, h.lifecycles, "dev1", stateNow)
			if err != nil || got != tc.want {
				t.Errorf("InForceScopes = %v, %v; want %v, nil", got, err, tc.want)
			}
		})
	}
}

func TestInForceScopes_OnlyTheDevicesOwnScopes(t *testing.T) {
	h := newHarness(t, Config{PEN: testPEN(1)})
	live := &sep2.DateTimeInterval{Start: stateNow - 10, Duration: 600}
	putControl(t, h, "dev10", "c1", live, &LifecycleRecord{})
	for edev, want := range map[string]bool{"dev1": false, "dev10": true, "dev": false} {
		got, err := InForceScopes(context.Background(), h.controls, h.lifecycles, edev, stateNow)
		if err != nil || got != want {
			t.Errorf("%s: InForceScopes = %v, %v; want %v", edev, got, err, want)
		}
	}
}

func TestInForceScopes_AnyScopeOfTheDeviceCounts(t *testing.T) {
	h := newHarness(t, Config{PEN: testPEN(1)})
	ended := &sep2.DateTimeInterval{Start: stateNow - 600, Duration: 60}
	putControl(t, h, "dev1", "old", ended, &LifecycleRecord{})
	c := sep2.DERControl{}
	c.Href = controlListHref("dev1", "7", "p2") + "/live"
	c.Interval = &sep2.DateTimeInterval{Start: stateNow - 10, Duration: 600}
	if err := h.controls.Create(context.Background(), "dev1/7/p2", "live", c); err != nil {
		t.Fatal(err)
	}
	got, err := InForceScopes(context.Background(), h.controls, h.lifecycles, "dev1", stateNow)
	if err != nil || !got {
		t.Errorf("InForceScopes = %v, %v; want true from the second scope (no lifecycle record, no status)", got, err)
	}
}

func TestInForceScopes_FSAControlReadsTrueOnEachCopy(t *testing.T) {
	controls := memory.NewScopedStore[sep2.DERControl]()
	issuer, _, lifecycles, targets := acrossHarness(t, controls, "d1", "d2")
	recs := &recordLog{ids: map[string]sep2.DERControl{}}
	res, err := issuer.IssueAcross(context.Background(), maxLimReq(4000), targets, recs.write)
	if err != nil {
		t.Fatalf("IssueAcross: %v", err)
	}
	at := res.Control.CreationTime + 1
	for edev, want := range map[string]bool{"d1": true, "d2": true, "d3": false} {
		got, err := InForceScopes(context.Background(), controls, lifecycles, edev, at)
		if err != nil || got != want {
			t.Errorf("%s: InForceScopes = %v, %v; want %v", edev, got, err, want)
		}
	}
	// Before its effective start the same copies are scheduled, not in force.
	if got, err := InForceScopes(context.Background(), controls, lifecycles, "d1", res.Control.CreationTime-1); err != nil || got {
		t.Errorf("before start: InForceScopes = %v, %v; want false", got, err)
	}
}

type stateFailControls struct {
	store.ScopedReader[sep2.DERControl]
	parentsErr, listErr error
}

func (f stateFailControls) Parents(ctx context.Context) ([]string, error) {
	if f.parentsErr != nil {
		return nil, f.parentsErr
	}
	return f.ScopedReader.Parents(ctx)
}

func (f stateFailControls) List(ctx context.Context, p string, o store.ListOptions) (store.ListResult[sep2.DERControl], error) {
	if f.listErr != nil {
		return store.ListResult[sep2.DERControl]{}, f.listErr
	}
	return f.ScopedReader.List(ctx, p, o)
}

type stateFailLifecycles struct{ err error }

func (f stateFailLifecycles) Get(context.Context, string, string) (LifecycleRecord, error) {
	return LifecycleRecord{}, f.err
}

func TestInForceScopes_ReadFailureIsAnErrorNeverFalse(t *testing.T) {
	boom := errors.New("boom")
	h := newHarness(t, Config{PEN: testPEN(1)})
	putControl(t, h, "dev1", "c1", &sep2.DateTimeInterval{Start: stateNow - 10, Duration: 600}, &LifecycleRecord{})
	for _, tc := range []struct {
		name string
		c    store.ScopedReader[sep2.DERControl]
		lc   lifecycleReader
	}{
		{"parents", stateFailControls{h.controls, boom, nil}, h.lifecycles},
		{"list", stateFailControls{h.controls, nil, boom}, h.lifecycles},
		{"lifecycle", h.controls, stateFailLifecycles{boom}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := InForceScopes(context.Background(), tc.c, tc.lc, "dev1", stateNow)
			if !errors.Is(err, boom) || got {
				t.Errorf("InForceScopes = %v, %v; want false and an error wrapping the read failure", got, err)
			}
		})
	}
}

// A control that is over is judged from its own interval, so an unreadable
// lifecycle record cannot fail Retire or the dashboard read for it.
type getFailLifecycles struct{ lifecycleStore }

func (getFailLifecycles) Get(context.Context, string, string) (LifecycleRecord, error) {
	return LifecycleRecord{}, errors.New("lifecycle backend down")
}

func TestEndedControl_NeverReadsItsLifecycle(t *testing.T) {
	for name, iv := range map[string]*sep2.DateTimeInterval{
		"ended":       {Start: stateNow - 600, Duration: 60},
		"no interval": nil,
	} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t, Config{PEN: testPEN(1)})
			putControl(t, h, "dev1", "c1", iv, nil)

			got, err := InForceScopes(context.Background(), h.controls, getFailLifecycles{h.lifecycles}, "dev1", stateNow)
			if err != nil || got {
				t.Errorf("InForceScopes = %v, %v; want false, nil", got, err)
			}

			issuer, err := NewIssuer(h.programs, h.controls, getFailLifecycles{h.lifecycles}, Config{PEN: testPEN(1)})
			if err != nil {
				t.Fatal(err)
			}
			live, err := issuer.liveControls(context.Background(), "dev1/0/p1", stateNow)
			if err != nil || len(live) != 0 {
				t.Errorf("liveControls = %v, %v; want none, nil", live, err)
			}
		})
	}
}

// LiveCount counts scheduled and in-force controls of every scope of one
// device, and not ended, cancelled or superseded ones, by ControlState.
func TestLiveCount(t *testing.T) {
	ctx := context.Background()
	controls := memory.NewScopedStore[sep2.DERControl]()
	lifecycles := memory.NewScopedStore[LifecycleRecord]()
	now := stateNow
	past := now - 10
	add := func(key, id string, start int64, lc *LifecycleRecord) {
		t.Helper()
		c := sep2.DERControl{}
		c.MRID, c.CreationTime = id, now-100
		edev, fsa, derp, _ := derhref.SplitScopeKey(key)
		c.Href = controlListHref(edev, fsa, derp) + "/" + id
		c.Interval = &sep2.DateTimeInterval{Start: start, Duration: 600}
		if err := controls.Create(ctx, key, id, c); err != nil {
			t.Fatal(err)
		}
		if lc != nil {
			if err := lifecycles.Create(ctx, key, id, *lc); err != nil {
				t.Fatal(err)
			}
		}
	}
	add("d1/f/p", "scheduled", now+1000, &LifecycleRecord{})
	add("d1/g/q", "inforce", now-100, &LifecycleRecord{})
	add("d1/f/p", "noRecord", now+2000, nil)
	add("d1/f/p", "ended", now-5000, &LifecycleRecord{})
	add("d1/f/p", "cancelled", now+3000, &LifecycleRecord{CancelledAt: &past})
	add("d1/f/p", "superseded", now-200, &LifecycleRecord{SupersededAt: &past, SupersededBy: "X"})
	add("d2/f/p", "otherDevice", now+1000, &LifecycleRecord{})
	n, err := LiveCount(ctx, controls, lifecycles, "d1", now)
	if err != nil || n != 3 {
		t.Errorf("LiveCount = %d, %v; want 3: scheduled, in force and one with no record", n, err)
	}
}

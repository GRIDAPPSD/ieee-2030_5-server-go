package dercontrol

import (
	"context"
	"testing"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

// Acceptance criterion 4's bounds are meaningless if a misconfigured Issuer
// silently accepts them: NewIssuer validates before any request is issued,
// and a PEN of 0 (IANA-reserved) is treated the same as an unconfigured PEN.

func newConfigTestStores() (*memory.ScopedStore[sep2.DERProgram], *memory.ScopedStore[sep2.DERControl], *memory.ScopedStore[LifecycleRecord]) {
	return memory.NewScopedStore[sep2.DERProgram](),
		memory.NewScopedStore[sep2.DERControl](),
		memory.NewScopedStore[LifecycleRecord]()
}

func TestNewIssuer_AcceptsZeroConfig(t *testing.T) {
	programs, controls, lifecycles := newConfigTestStores()
	if _, err := NewIssuer(programs, controls, lifecycles, Config{PEN: testPEN(1)}); err != nil {
		t.Fatalf("NewIssuer() error = %v, want the zero Config to take package defaults and validate", err)
	}
}

func TestNewIssuer_RefusesNegativeDuration(t *testing.T) {
	programs, controls, lifecycles := newConfigTestStores()
	_, err := NewIssuer(programs, controls, lifecycles, Config{MinDuration: -1 * time.Second, MaxDuration: time.Hour})
	if err == nil {
		t.Fatalf("NewIssuer() error = nil, want an error for a negative MinDuration")
	}
}

func TestNewIssuer_RefusesSubSecondDuration(t *testing.T) {
	programs, controls, lifecycles := newConfigTestStores()
	// 500ms would truncate to 0 seconds at every comparison Issue makes
	// (int64(d/time.Second)), silently accepting a zero-length request.
	_, err := NewIssuer(programs, controls, lifecycles, Config{MinDuration: 500 * time.Millisecond, MaxDuration: time.Hour})
	if err == nil {
		t.Fatalf("NewIssuer() error = nil, want an error for a sub-second MinDuration")
	}
}

func TestNewIssuer_RefusesMinDurationAboveMaxDuration(t *testing.T) {
	programs, controls, lifecycles := newConfigTestStores()
	_, err := NewIssuer(programs, controls, lifecycles, Config{MinDuration: 2 * time.Hour, MaxDuration: time.Hour})
	if err == nil {
		t.Fatalf("NewIssuer() error = nil, want an error when MinDuration exceeds MaxDuration")
	}
}

func TestNewIssuer_TreatsPENZeroAsNotConfigured(t *testing.T) {
	programs, controls, lifecycles := newConfigTestStores()
	zero := uint32(0)
	issuer, err := NewIssuer(programs, controls, lifecycles, Config{PEN: &zero})
	if err != nil {
		t.Fatalf("NewIssuer() error = %v", err)
	}
	h := &testHarness{issuer: issuer, programs: programs, controls: controls, lifecycles: lifecycles}
	h.seedProgram(t, "dev1", "p1", controlListHref("dev1", "0", "p1"))

	_, err = h.issuer.Issue(context.Background(), CreateRequest{
		DERProgramHref:  programHref("dev1", "0", "p1"),
		Type:            Connect,
		DurationSeconds: 3600,
	})
	assertRefusal(t, err, RefusalPENNotConfigured)
	assertNoNewControl(t, h)
}

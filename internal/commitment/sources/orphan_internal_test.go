package sources

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/commitment"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
)

type goneFleets struct{}

func (goneFleets) FleetOf(_ context.Context, id string) (string, error) {
	return "", fmt.Errorf("resolving fleet of EndDevice %s: %w", id, store.ErrNotFound)
}

func TestFleetOrGone_LogsAnOrphanOnce(t *testing.T) {
	t.Parallel()
	var lines []string
	o := orphans{fleets: goneFleets{}, what: "things", logf: func(f string, a ...any) { lines = append(lines, fmt.Sprintf(f, a...)) }}
	for range 3 {
		fleet, err := o.fleetOrGone(context.Background(), "gone")
		if fleet != "" || err != nil {
			t.Fatalf("fleetOrGone = %q, %v; want \"\", nil", fleet, err)
		}
	}
	if len(lines) != 1 || !strings.Contains(lines[0], "things under EndDevice gone") {
		t.Fatalf("logged %q for one orphan, want one line naming it", lines)
	}
}

type fleetErr struct{ err error }

func (f fleetErr) FleetOf(context.Context, string) (string, error) { return "", f.err }

// With skipNoLFDI a device with no LFDI is skipped like a gone one; without
// it, and for any other resolver failure, the scan refuses.
func TestFleetOrGone_NoLFDIIsSkippedOtherErrorsRefuse(t *testing.T) {
	t.Parallel()
	var lines []string
	logf := func(f string, a ...any) { lines = append(lines, fmt.Sprintf(f, a...)) }
	noLFDI := fleetErr{fmt.Errorf("x: %w", commitment.ErrNoLFDI)}
	if _, err := (&orphans{fleets: noLFDI, what: "things", logf: logf}).fleetOrGone(context.Background(), "blank"); !errors.Is(err, commitment.ErrNoLFDI) {
		t.Errorf("fleetOrGone(no LFDI, not skipped) = %v, want ErrNoLFDI", err)
	}
	o := orphans{fleets: noLFDI, what: "things", logf: logf, skipNoLFDI: true}
	if fleet, err := o.fleetOrGone(context.Background(), "blank"); fleet != "" || err != nil {
		t.Fatalf("fleetOrGone(no LFDI) = %q, %v; want \"\", nil", fleet, err)
	}
	if len(lines) != 1 || !strings.Contains(lines[0], "EndDevice blank sit under a device with no LFDI") {
		t.Errorf("logged %q", lines)
	}

	boom := errors.New("store down")
	o = orphans{fleets: fleetErr{boom}, what: "things", logf: logf, skipNoLFDI: true}
	if _, err := o.fleetOrGone(context.Background(), "x"); !errors.Is(err, boom) {
		t.Errorf("fleetOrGone(store error) = %v, want it returned", err)
	}
}

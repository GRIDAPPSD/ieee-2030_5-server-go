package sources

import (
	"context"
	"fmt"
	"strings"
	"testing"

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

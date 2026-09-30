package sources

import (
	"context"
	"fmt"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
)

type goneFleets struct{}

func (goneFleets) FleetOf(_ context.Context, id string) (string, error) {
	return "", fmt.Errorf("resolving fleet of EndDevice %s: %w", id, store.ErrNotFound)
}

func TestResolve_LogsAnOrphanOnce(t *testing.T) {
	t.Parallel()
	var lines []string
	c := &Controls{fleets: goneFleets{}, logf: func(f string, a ...any) { lines = append(lines, fmt.Sprintf(f, a...)) }}
	for range 3 {
		fleet, err := c.resolve(context.Background(), "gone")
		if fleet != "" || err != nil {
			t.Fatalf("resolve = %q, %v; want \"\", nil", fleet, err)
		}
	}
	if len(lines) != 1 {
		t.Fatalf("logged %d lines for one orphan, want 1: %q", len(lines), lines)
	}
}

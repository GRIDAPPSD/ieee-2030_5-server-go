package flowreservation

import (
	"context"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
)

// PermissiveGate reads every window as free. It is for tests whose subject
// is not the commitment rule; production always wires a LedgerGate.
type PermissiveGate struct{}

// Grant implements Gate by calling write at once.
func (PermissiveGate) Grant(ctx context.Context, _ string, _ *sep2.DateTimeInterval, _ string, write func(ctx context.Context) error) error {
	return write(ctx)
}

package devicelog

import (
	"context"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/identity"
	sep2lfdi "github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/lfdi"
)

// Actor returns the canonical LFDI of the caller id reports on ctx, the key
// of a device's log file. It is false when there is no identity or it is not
// an LFDI; each caller chooses whether to refuse or drop.
func Actor(ctx context.Context, id identity.Func) (string, bool) {
	lfdi, _, ok := id(ctx)
	return ActorFromLFDI(lfdi, ok)
}

// ActorFromLFDI is Actor for a caller that has already read the identity.
func ActorFromLFDI(lfdi string, ok bool) (string, bool) {
	if !ok {
		return "", false
	}
	return sep2lfdi.Canonical(lfdi)
}

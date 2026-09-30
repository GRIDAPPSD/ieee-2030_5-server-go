package flowreservation

import "context"

// CommitmentChecker reports whether a fleet already holds a live commitment
// (a grant or a plain dispatch) overlapping a window. #714 owns the real
// rule: one commitment per fleet window, keyed by the aggregator's LFDI.
//
// fleetKey here is the EndDevice id the request was posted under (the
// {id} path value), not yet resolved to the aggregator's LFDI; #714's own
// resolver does that. Until #714 lands, Queue defaults to
// PermissiveCommitmentChecker, so the deadline fallback grants as asked for
// an unanswered, uncontested request exactly as D1/D2 specify; #714 plugs
// in by implementing this interface, with no caller change.
type CommitmentChecker interface {
	Committed(ctx context.Context, fleetKey string, start int64, duration uint32) (bool, error)
}

// PermissiveCommitmentChecker always reports a fleet's window as free. It is
// the default CommitmentChecker until #714 builds the real commitment
// ledger.
type PermissiveCommitmentChecker struct{}

// Committed always returns false, nil: see the type doc comment.
func (PermissiveCommitmentChecker) Committed(_ context.Context, _ string, _ int64, _ uint32) (bool, error) {
	return false, nil
}

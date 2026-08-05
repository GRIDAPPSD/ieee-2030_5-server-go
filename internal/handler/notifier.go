package handler

import "context"

// ResourceNotifier dispatches subscription notifications for a resource
// href. Defined here at the consumer (Pike rule: interfaces at the
// consumer, not at the producer) so the handler can take nil from tests
// that do not exercise the notification pipeline. The production
// implementation is *coresub.Manager.
//
// Moved from edev.go to this file in Phase 3 so the interface survives
// the deletion of the protocol handler files while admin and mutation
// callers (assembly_seam.go, test_mutations.go, csiptest/server.go) keep
// their existing import paths.
type ResourceNotifier interface {
	Notify(ctx context.Context, resourceHref string, status uint8)
}

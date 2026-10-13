package adminplane

import (
	"context"
	"log/slog"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/commitment"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/commitment/sources"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/flowreservation"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

// FlowReservationConfig is the one place the flow reservation queue's
// Config is built. The queue runs under it, and the admin read API computes
// deadlineAt under it, so a configured deadline moves both together. Zero
// takes the queue's default.
func FlowReservationConfig(deadline time.Duration) flowreservation.Config {
	return flowreservation.Config{Deadline: deadline}
}

// FlowReservationAnswers is the answer store as the queue and the admin API
// use it, or nil when none is wired.
func FlowReservationAnswers(s *Stores) *flowreservation.Answers {
	if store.IsAbsent(s.FlowReservationAnswers) {
		return nil
	}
	return flowreservation.NewAnswers(s.FlowReservationAnswers)
}

// FlowReservationResponses is the response store with its lifecycle and
// answer records removed alongside it, the form the assembly serves and
// recovery deletes through, so an EndDevice delete leaves neither behind.
func FlowReservationResponses(s *Stores) store.ScopedStore[sep2.FlowReservationResponse] {
	withLifecycles := memory.WithDependents(s.FlowReservationResponses, s.FlowReservationResponseLifecycles)
	return memory.WithDependents(withLifecycles, s.FlowReservationAnswers)
}

// recoveryWriters are the writers a repair cancels grants through: the
// process's issuer and cancel marks, notifying as a live cancel does. Without
// an issuer it returns the zero Writers, and a repair that needs them ends the
// pass with an error (requireRepairDeps), which Run never meets because it
// always sets one.
func recoveryWriters(stores *Stores, notifier flowreservation.Notifier) commitment.Writers {
	if stores.DERControlIssuer == nil {
		return commitment.Writers{}
	}
	return flowreservation.NotifyingWriters(
		sources.NewWriters(stores.DERControlIssuer, stores.FlowReservationResponseLifecycles), notifier)
}

// RecoverFlowReservations is the startup pass over stored flow reservation
// requests (#762). A request it could not repair is logged at error level by
// Recover, once per request, and does not stop the boot: it is retried at the
// next start. Only a failure of the pass itself returns an error.
func RecoverFlowReservations(ctx context.Context, stores *Stores, queue *flowreservation.Queue, notifier flowreservation.Notifier, logger *slog.Logger, now time.Time) (flowreservation.RecoverCounts, error) {
	writers := recoveryWriters(stores, notifier)
	return flowreservation.Recover(ctx, flowreservation.RecoverDeps{
		FRQ:        stores.FlowReservationRequests,
		FRP:        FlowReservationResponses(stores),
		Lifecycles: stores.FlowReservationResponseLifecycles,
		Queue:      queue,
		Ledger:     stores.CommitmentLedger,
		Writers:    writers,
		Notifier:   notifier,
		Log:        logger,
	}, now)
}

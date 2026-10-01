package flowreservation

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/commitment"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
)

// RetentionInterval is the period of the sweep that follows the one at boot.
const RetentionInterval = 60 * time.Second

// ErrIncompleteRetention is returned by Sweep when a required dependency is
// missing.
var ErrIncompleteRetention = errors.New("flowreservation: Retention needs FRQ, FRP, Lifecycles, Ledger, Fleets and a positive Grace")

// RetentionFRQ is the part of the request store the sweep reads and deletes.
type RetentionFRQ interface {
	RecoverFRQ
	Get(ctx context.Context, parentID, id string) (sep2.FlowReservationRequest, error)
	Delete(ctx context.Context, parentID, id string) error
}

// RetentionFRP is the part of the response store the sweep reads and deletes.
type RetentionFRP interface {
	FRPReader
	Parents(ctx context.Context) ([]string, error)
	List(ctx context.Context, parentID string, opts store.ListOptions) (store.ListResult[sep2.FlowReservationResponse], error)
	Delete(ctx context.Context, parentID, id string) error
}

// RecordDeleter is a store keyed like the responses: the response lifecycle
// records, or the answer records.
type RecordDeleter interface {
	Delete(ctx context.Context, parentID, id string) error
}

// Retention removes flow reservation requests whose responses have ended
// (IEEE 2030.5-2023 10.9.3.1), together with their responses and the
// lifecycle and answer records keyed like those responses.
type Retention struct {
	FRQ        RetentionFRQ
	FRP        RetentionFRP
	Lifecycles RecordDeleter
	// Answers is optional; nil means no answer store is wired.
	Answers RecordDeleter
	Ledger  *commitment.Ledger
	Fleets  FleetResolver
	// Grace is added to a chain's end. It is required: the default and its
	// bounds belong to the setting (internal/config), and a zero grace would
	// remove a chain the instant it ends.
	Grace time.Duration
	// Notifier, when set, is told once per EndDevice whose response list
	// lost a member, after every lock is released.
	Notifier Notifier
	// Log takes the per-request failures and the summary; nil takes
	// slog.Default().
	Log *slog.Logger
}

func (r *Retention) logger() *slog.Logger {
	if r.Log != nil {
		return r.Log
	}
	return slog.Default()
}

// endsAt is when one response stops mattering: the end of its interval, or
// its creation when that is later or it has no interval. A cancelled response
// keeps its original end, since a Cancelled event stays readable for the
// duration of the original event.
func endsAt(frp sep2.FlowReservationResponse) int64 {
	if frp.Interval == nil {
		return frp.CreationTime
	}
	end := int64(math.MaxInt64)
	if d := int64(frp.Interval.Duration); frp.Interval.Start <= math.MaxInt64-d {
		end = frp.Interval.Start + d
	}
	return max(end, frp.CreationTime)
}

// ended reports whether every response of chain ended at least grace before
// now. Reading every member rather than the tip alone keeps a revised-away
// response whose original window outlasts its revision readable until that
// window ends too. An empty chain never ends: an unanswered request is kept.
func ended(chain []sep2.FlowReservationResponse, grace time.Duration, now time.Time) bool {
	if len(chain) == 0 {
		return false
	}
	g := int64(grace / time.Second)
	for _, frp := range chain {
		e := endsAt(frp)
		if e > math.MaxInt64-g || e+g > now.Unix() {
			return false
		}
	}
	return true
}

// chainBase splits a response id into the request id it was stored under and
// its position in the chain: 0 for the request's own id, k for "-rk". It is
// RevisionID read backwards.
func chainBase(id string) (base string, k int) {
	if i := strings.LastIndex(id, "-r"); i > 0 {
		n := id[i+len("-r"):]
		if v, err := strconv.Atoi(n); err == nil && v > 0 && n == strconv.Itoa(v) {
			return id[:i], v
		}
	}
	return id, 0
}

// Sweep removes every request whose chain has ended, then every response
// whose request no longer exists (what a crash between two deletes leaves).
// It returns the number of requests removed. A failure to enumerate the
// requests or responses is returned, after the rest of the pass has run; a
// request whose chain, fleet or delete fails is logged, left as it is, and
// retried by the next sweep.
//
// Per request, the chain is read again under its fleet lock, so a revision
// or a cancel cannot interleave, and deleted in an order whose every crash
// prefix the next sweep finishes and Recover leaves alone:
//
//   - each revision, newest first, then its lifecycle and answer records;
//   - the request;
//   - the first response, then its lifecycle and answer records.
//
// A response always goes before its lifecycle record, which carries its
// cancel mark. The first response outlives the request because Recover reads
// a request with no response as pending and would answer it again.
func (r *Retention) Sweep(ctx context.Context, now time.Time) (int, error) {
	if r.FRQ == nil || r.FRP == nil || r.Lifecycles == nil || r.Ledger == nil || r.Fleets == nil || r.Grace <= 0 {
		return 0, ErrIncompleteRetention
	}
	var (
		removed  int
		orphans  int
		failures []error
		passErrs []error
		touched  []string
	)
	notifyLater := func(edevID string) {
		if !slices.Contains(touched, edevID) {
			touched = append(touched, edevID)
		}
	}
	// Runs on every return, after every lock is released, so what was
	// already removed is announced and what was skipped is logged.
	defer func() {
		hook := notifyHook{n: r.Notifier}
		for _, e := range touched {
			hook.fire(ctx, e)
		}
		for _, f := range failures {
			r.logger().Warn("flowreservation: retention: left for the next sweep", "err", f)
		}
		if removed > 0 || orphans > 0 || len(failures) > 0 {
			r.logger().Info("flowreservation: retention swept",
				"requestsRemoved", removed, "orphanResponsesRemoved", orphans, "skipped", len(failures))
		}
	}()

	// A request store that cannot be listed stops the request pass only:
	// the orphan pass reads the response store and can still run.
	parents, err := r.FRQ.Parents(ctx)
	if err != nil {
		passErrs = append(passErrs, fmt.Errorf("flowreservation: retention: list request owners: %w", err))
	}
	for _, edevID := range parents {
		page, err := r.FRQ.List(ctx, edevID, store.ListOptions{Unbounded: true})
		if err != nil {
			passErrs = append(passErrs, fmt.Errorf("flowreservation: retention: list requests of %s: %w", edevID, err))
			continue
		}
		for _, frq := range page.Items {
			if err := ctx.Err(); err != nil {
				return removed, err
			}
			frqID, ok := requestID(edevID, frq.Href)
			if !ok {
				failures = append(failures, fmt.Errorf("request href %q is not under /edev/%s/frq/", frq.Href, edevID))
				continue
			}
			gone, err := r.retire(ctx, edevID, frqID, now)
			if gone {
				removed++
			}
			if gone || err != nil {
				notifyLater(edevID)
			}
			if err != nil {
				failures = append(failures, fmt.Errorf("request %s/%s: %w", edevID, frqID, err))
			}
		}
	}

	n, errs, err := r.sweepOrphans(ctx, notifyLater)
	orphans = n
	failures = append(failures, errs...)
	if err != nil {
		passErrs = append(passErrs, err)
	}
	return removed, errors.Join(passErrs...)
}

// retire removes one request and its chain when the chain has ended. The
// first read is unlocked so a live chain costs no lock; the decision is taken
// again on a fresh read under the fleet lock.
func (r *Retention) retire(ctx context.Context, edevID, frqID string, now time.Time) (bool, error) {
	chain, err := ChainOf(ctx, r.FRP, edevID, frqID)
	if err != nil {
		return false, err
	}
	if !ended(chain, r.Grace, now) {
		return false, nil
	}
	gone := false
	err = r.underFleetLock(ctx, edevID, func() error {
		if _, err := r.FRQ.Get(ctx, edevID, frqID); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return nil
			}
			return fmt.Errorf("get request: %w", err)
		}
		chain, err := ChainOf(ctx, r.FRP, edevID, frqID)
		if err != nil {
			return err
		}
		if !ended(chain, r.Grace, now) {
			return nil
		}
		if err := r.deleteChain(ctx, edevID, frqID, len(chain)); err != nil {
			return err
		}
		gone = true
		return nil
	})
	return gone, err
}

// deleteChain deletes a request whose chain holds n responses, in Sweep's
// order. It first clears the records keyed one past the tip, which a crash
// in an earlier sweep or an interrupted answer can leave without a response.
func (r *Retention) deleteChain(ctx context.Context, edevID, frqID string, n int) error {
	ids := make([]string, n)
	ids[0] = frqID
	for i := 1; i < n; i++ {
		ids[i] = RevisionID(ids[i-1])
	}
	if err := r.deleteRecords(ctx, edevID, RevisionID(ids[n-1])); err != nil {
		return err
	}
	for i := n - 1; i >= 1; i-- {
		if err := r.deleteMember(ctx, edevID, ids[i]); err != nil {
			return err
		}
	}
	if err := ignoreNotFound(r.FRQ.Delete(ctx, edevID, frqID)); err != nil {
		return fmt.Errorf("delete request: %w", err)
	}
	return r.deleteMember(ctx, edevID, frqID)
}

// deleteMember deletes one response and then the records keyed like it.
func (r *Retention) deleteMember(ctx context.Context, edevID, id string) error {
	if err := ignoreNotFound(r.FRP.Delete(ctx, edevID, id)); err != nil {
		return fmt.Errorf("delete response %s: %w", id, err)
	}
	return r.deleteRecords(ctx, edevID, id)
}

func (r *Retention) deleteRecords(ctx context.Context, edevID, id string) error {
	if err := ignoreNotFound(r.Lifecycles.Delete(ctx, edevID, id)); err != nil {
		return fmt.Errorf("delete lifecycle record %s: %w", id, err)
	}
	if r.Answers == nil {
		return nil
	}
	if err := ignoreNotFound(r.Answers.Delete(ctx, edevID, id)); err != nil {
		return fmt.Errorf("delete answer record %s: %w", id, err)
	}
	return nil
}

func ignoreNotFound(err error) error {
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	return err
}

// sweepOrphans removes the responses whose request no longer exists, newest
// first, each before its lifecycle and answer records. Only an id that is a
// chain id of a missing request is touched, so a live response and its
// cancel mark are never reached: a live response's request always exists.
// The lifecycle and answer records of a response already gone cannot be
// found here, since no store lists its ids; a crash between those deletes
// leaves them unread under an id no later request reuses.
func (r *Retention) sweepOrphans(ctx context.Context, touched func(string)) (int, []error, error) {
	parents, err := r.FRP.Parents(ctx)
	if err != nil {
		return 0, nil, fmt.Errorf("flowreservation: retention: list response owners: %w", err)
	}
	var (
		removed  int
		failures []error
	)
	for _, edevID := range parents {
		page, err := r.FRP.List(ctx, edevID, store.ListOptions{Unbounded: true})
		if err != nil {
			return removed, failures, fmt.Errorf("flowreservation: retention: list responses of %s: %w", edevID, err)
		}
		chains := map[string][]string{}
		var bases []string
		for _, frp := range page.Items {
			id, ok := ResponseID(edevID, frp.Href)
			if !ok {
				failures = append(failures, fmt.Errorf("response href %q is not under /edev/%s/frp/", frp.Href, edevID))
				continue
			}
			base, _ := chainBase(id)
			if _, seen := chains[base]; !seen {
				bases = append(bases, base)
			}
			chains[base] = append(chains[base], id)
		}
		for _, base := range bases {
			if err := ctx.Err(); err != nil {
				return removed, failures, err
			}
			n, err := r.removeOrphanChain(ctx, edevID, base, chains[base])
			removed += n
			if n > 0 || err != nil {
				touched(edevID)
			}
			if err != nil {
				failures = append(failures, fmt.Errorf("orphan responses of %s/%s: %w", edevID, base, err))
			}
		}
	}
	return removed, failures, nil
}

// removeOrphanChain deletes ids when the request base is gone, under
// underFleetLock.
func (r *Retention) removeOrphanChain(ctx context.Context, edevID, base string, ids []string) (int, error) {
	if exists, err := r.requestExists(ctx, edevID, base); err != nil || exists {
		return 0, err
	}
	slices.SortFunc(ids, func(a, b string) int {
		_, ka := chainBase(a)
		_, kb := chainBase(b)
		return kb - ka
	})
	removed := 0
	run := func(ctx context.Context) error {
		if exists, err := r.requestExists(ctx, edevID, base); err != nil || exists {
			return err
		}
		for _, id := range ids {
			if err := r.deleteMember(ctx, edevID, id); err != nil {
				return err
			}
			removed++
		}
		return nil
	}
	err := r.underFleetLock(ctx, edevID, func() error { return run(ctx) })
	return removed, err
}

// underFleetLock runs fn under the fleet lock of edevID. A device that no
// longer exists, or has no LFDI, has no fleet: every writer that could touch
// its records (a grant, a revision, a cancel) resolves the same fleet and is
// refused, so fn runs unlocked rather than being skipped and logged on every
// sweep. Any other resolver error skips fn.
func (r *Retention) underFleetLock(ctx context.Context, edevID string, fn func() error) error {
	fleet, err := r.Fleets.FleetOf(ctx, edevID)
	switch {
	case err == nil:
		return r.Ledger.Within(ctx, []string{fleet}, func(commitment.View) error { return fn() })
	case errors.Is(err, store.ErrNotFound) || errors.Is(err, commitment.ErrNoLFDI):
		return fn()
	default:
		return fmt.Errorf("fleet: %w", err)
	}
}

func (r *Retention) requestExists(ctx context.Context, edevID, frqID string) (bool, error) {
	_, err := r.FRQ.Get(ctx, edevID, frqID)
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, store.ErrNotFound):
		return false, nil
	default:
		return false, fmt.Errorf("get request: %w", err)
	}
}

// Start runs Sweep every interval, with the time from now, until ctx ends or
// the returned stop is called. An interval of zero or less takes
// RetentionInterval. stop cancels a sweep in progress, waits for it to
// return, and is safe to call more than once.
func (r *Retention) Start(ctx context.Context, interval time.Duration, now func() time.Time) (stop func()) {
	if interval <= 0 {
		interval = RetentionInterval
	}
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if _, err := r.Sweep(ctx, now()); err != nil && ctx.Err() == nil {
					r.logger().Error("flowreservation: retention sweep failed, retried at the next tick", "err", err)
				}
			}
		}
	}()
	var once sync.Once
	return func() {
		once.Do(func() {
			cancel()
			<-done
		})
	}
}

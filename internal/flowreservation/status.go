package flowreservation

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/dercontrol"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/sep2time"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
)

// deriveEventStatus derives a FlowReservationResponse's EventStatus by
// calling dercontrol.DeriveStatus (#564), the one state machine issue
// #666's "status follows the DER control derivation rule" criterion asks
// for. lc is the response's lifecycle record, which carries its cancel
// mark (#714); a response is never superseded, so that branch never fires.
//
// It also carries #564's deliberate 2018 policy: DeriveStatus takes no
// duration or end, so it never returns Complete (a 2023-only value this
// server does not serve on any event-derived resource). A finished grant
// and a zero-duration denial whose start has passed both read Active,
// exactly as an ended DERControl does. That is accepted rather than worked
// around: inventing a Complete-based denial status here would leave two
// different status vocabularies for one server.
func deriveEventStatus(start, creationTime, now int64, lc dercontrol.LifecycleRecord) sep2.EventStatus {
	return dercontrol.DeriveStatus(now, creationTime, start, lc)
}

// responseHref is the href build stores a response under: the response
// shares its request's id.
func responseHref(edevID, frqID string) string {
	return "/edev/" + edevID + "/frp/" + frqID
}

// ResponseID recovers the store id from a response's own href, under the
// EndDevice it is stored beneath. ok is false for any other shape.
func ResponseID(edevID, href string) (string, bool) {
	id, ok := strings.CutPrefix(href, "/edev/"+edevID+"/frp/")
	if !ok || id == "" || strings.Contains(id, "/") {
		return "", false
	}
	return id, true
}

// LifecycleReader is the read-only subset of a response lifecycle store the
// serve-time derivation needs.
type LifecycleReader interface {
	Get(ctx context.Context, parentID, id string) (dercontrol.LifecycleRecord, error)
}

// DerivedStatusResponseStore decorates a FlowReservationResponse store so a
// response with a lifecycle record is served with the EventStatus derived
// from it; a response without one is served exactly as stored. It
// decorates Get and List only; every write delegates unchanged, so the
// stored response is never rewritten.
type DerivedStatusResponseStore struct {
	responses  store.ScopedStore[sep2.FlowReservationResponse]
	lifecycles LifecycleReader
}

// NewDerivedStatusResponseStore decorates responses with lifecycles.
func NewDerivedStatusResponseStore(responses store.ScopedStore[sep2.FlowReservationResponse], lifecycles LifecycleReader) *DerivedStatusResponseStore {
	return &DerivedStatusResponseStore{responses: responses, lifecycles: lifecycles}
}

var _ store.ScopedStore[sep2.FlowReservationResponse] = (*DerivedStatusResponseStore)(nil)

// Get returns the response with its EventStatus derived. A lifecycle lookup
// failure other than "no record" fails the read: serving the stored status
// would show a cancelled grant as live.
func (s *DerivedStatusResponseStore) Get(ctx context.Context, parentID, id string) (sep2.FlowReservationResponse, error) {
	frp, err := s.responses.Get(ctx, parentID, id)
	if err != nil {
		return frp, err
	}
	if err := s.derive(ctx, sep2time.Now().Unix(), parentID, id, &frp); err != nil {
		return sep2.FlowReservationResponse{}, err
	}
	return frp, nil
}

// List returns a page with each member's EventStatus derived against one
// instant, and fails whole on a lookup failure, as Get does.
func (s *DerivedStatusResponseStore) List(ctx context.Context, parentID string, opts store.ListOptions) (store.ListResult[sep2.FlowReservationResponse], error) {
	result, err := s.responses.List(ctx, parentID, opts)
	if err != nil {
		return result, err
	}
	now := sep2time.Now().Unix()
	for i := range result.Items {
		id, ok := ResponseID(parentID, result.Items[i].Href)
		if !ok {
			return store.ListResult[sep2.FlowReservationResponse]{}, fmt.Errorf("flowreservation: response href %q under %s has no store id", result.Items[i].Href, parentID)
		}
		if err := s.derive(ctx, now, parentID, id, &result.Items[i]); err != nil {
			return store.ListResult[sep2.FlowReservationResponse]{}, err
		}
	}
	return result, nil
}

func (s *DerivedStatusResponseStore) derive(ctx context.Context, now int64, parentID, id string, frp *sep2.FlowReservationResponse) error {
	lc, err := s.lifecycles.Get(ctx, parentID, id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil
		}
		return fmt.Errorf("flowreservation: loading lifecycle record for %s/%s: %w", parentID, id, err)
	}
	var start int64
	if frp.Interval != nil {
		start = frp.Interval.Start
	}
	status := deriveEventStatus(start, frp.CreationTime, now, lc)
	frp.EventStatus = &status
	return nil
}

func (s *DerivedStatusResponseStore) Count(ctx context.Context, parentID string) (uint32, error) {
	return s.responses.Count(ctx, parentID)
}

func (s *DerivedStatusResponseStore) HasParent(ctx context.Context, parentID string) (bool, error) {
	return s.responses.HasParent(ctx, parentID)
}

func (s *DerivedStatusResponseStore) Parents(ctx context.Context) ([]string, error) {
	return s.responses.Parents(ctx)
}

func (s *DerivedStatusResponseStore) Create(ctx context.Context, parentID, id string, resource sep2.FlowReservationResponse) error {
	return s.responses.Create(ctx, parentID, id, resource)
}

func (s *DerivedStatusResponseStore) Update(ctx context.Context, parentID, id string, resource sep2.FlowReservationResponse) error {
	return s.responses.Update(ctx, parentID, id, resource)
}

func (s *DerivedStatusResponseStore) Delete(ctx context.Context, parentID, id string) error {
	return s.responses.Delete(ctx, parentID, id)
}

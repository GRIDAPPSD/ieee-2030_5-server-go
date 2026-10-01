package flowreservation

import (
	"context"
	"errors"
	"fmt"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
)

// FRPReader is the part of the response store a chain walk needs.
type FRPReader interface {
	Get(ctx context.Context, parentID, id string) (sep2.FlowReservationResponse, error)
}

// ChainOf returns the responses to one request, oldest first: the one stored
// under frqID, then each revision RevisionID names in turn. The last is the
// tip, the response the next revision replaces. Normally only the tip is
// live; a revision whose rollback failed can leave an older one live too, so
// a caller that cancels must not assume it. A request with no response yet
// has an empty chain and no error.
//
// The first missing id ends the walk, so a gap would hide every later
// member. No write path makes one: the queue creates the first id, a
// revision creates the id after the tip under the fleet lock, and deletes
// remove a whole request's responses or the newest first. A read error other
// than not-found is returned, never read as the end.
func ChainOf(ctx context.Context, frp FRPReader, edevID, frqID string) ([]sep2.FlowReservationResponse, error) {
	var chain []sep2.FlowReservationResponse
	id := frqID
	for {
		r, err := frp.Get(ctx, edevID, id)
		if errors.Is(err, store.ErrNotFound) {
			return chain, nil
		}
		if err != nil {
			return nil, fmt.Errorf("flowreservation: get FlowReservationResponse %s/%s: %w", edevID, id, err)
		}
		chain = append(chain, r)
		id = RevisionID(id)
	}
}

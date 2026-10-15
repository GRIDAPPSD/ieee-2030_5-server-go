package dercontrol

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/derhref"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/sep2time"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
)

// LiveControlsError refuses a Retire because a scope still holds a control
// that is neither cancelled nor finished. Controls lists their hrefs, sorted.
type LiveControlsError struct {
	Controls []string
}

func (e *LiveControlsError) Error() string {
	return "dercontrol: live controls: " + strings.Join(e.Controls, ", ")
}

// Retire runs remove while every scope is locked against Issue and Cancel,
// and only when none of them holds a live control: one not cancelled, not
// superseded, and whose interval has not ended. A live control refuses with
// *LiveControlsError and remove does not run. Retire writes nothing itself,
// so every control record is kept.
func (i *Issuer) Retire(ctx context.Context, scopes []Scope, remove func(context.Context) error) error {
	keys := make([]string, 0, len(scopes))
	for _, s := range scopes {
		keys = append(keys, scopeKeyOf(s))
	}
	// Sorted, so two Retires over overlapping scopes cannot deadlock; Issue
	// and Cancel each hold one scope lock at a time.
	slices.Sort(keys)
	keys = slices.Compact(keys)
	for _, k := range keys {
		defer i.lockScope(k)()
	}

	now := sep2time.Now().Unix()
	var live []string
	for _, k := range keys {
		hrefs, err := i.liveControls(ctx, k, now)
		if err != nil {
			return err
		}
		live = append(live, hrefs...)
	}
	if len(live) > 0 {
		slices.Sort(live)
		return &LiveControlsError{Controls: live}
	}
	return remove(ctx)
}

func (i *Issuer) liveControls(ctx context.Context, scopeKey string, now int64) ([]string, error) {
	list, err := i.controls.List(ctx, scopeKey, store.ListOptions{Unbounded: true})
	if err != nil {
		return nil, fmt.Errorf("dercontrol: list scope %s: %w", scopeKey, err)
	}
	var live []string
	for _, c := range list.Items {
		// An event with no interval never runs; the supersede scan skips
		// one for the same reason. A boot fixture control has none.
		if c.Interval == nil || now >= c.Interval.Start+int64(c.Interval.Duration) {
			continue
		}
		lc, err := i.lifecycles.Get(ctx, scopeKey, controlIDOf(c.Href))
		switch {
		case err == nil:
			if lc.cancelled() || lc.supersededAsOf(now) {
				continue
			}
		case errors.Is(err, store.ErrNotFound):
			// A control this package did not issue carries its own status.
			if c.EventStatus != nil && c.EventStatus.CurrentStatus >= sep2.EventStatusCancelled {
				continue
			}
		default:
			return nil, fmt.Errorf("dercontrol: load lifecycle of %s: %w", c.Href, err)
		}
		live = append(live, c.Href)
	}
	return live, nil
}

// controlIDOf is the store id of a control at href, or "" for an href of
// another shape, which no lifecycle record is kept under.
func controlIDOf(href string) string {
	id, _ := derhref.ControlID(href)
	return id
}

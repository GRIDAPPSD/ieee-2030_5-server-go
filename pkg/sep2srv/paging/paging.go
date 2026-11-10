package paging

import (
	"net/url"
	"slices"
	"strconv"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
)

const (
	DefaultLimit uint32 = 10
	MaxLimit     uint32 = 255
)

// Params holds parsed paging query parameters per spec section 4.6.2.
type Params struct {
	Start uint32 // s: first ordinal position (0-based, default 0)
	Limit uint32 // l: max items to return (default 10, max 255)
	After string // a: return items with keys after this value
}

// ParseQuery extracts paging parameters from URL query values.
// Invalid values are silently replaced with defaults per spec.
func ParseQuery(q url.Values) Params {
	p := Params{
		Start: 0,
		Limit: DefaultLimit,
	}

	if s := q.Get("s"); s != "" {
		if v, err := strconv.ParseUint(s, 10, 32); err == nil {
			p.Start = uint32(v)
		}
	}

	if l := q.Get("l"); l != "" {
		if v, err := strconv.ParseUint(l, 10, 32); err == nil {
			p.Limit = uint32(v)
			if p.Limit > MaxLimit {
				p.Limit = MaxLimit
			}
		}
	}

	p.After = q.Get("a")

	return p
}

// ToListOptions converts paging Params to store.ListOptions.
func (p Params) ToListOptions() store.ListOptions {
	return store.ListOptions{
		Start: p.Start,
		Limit: p.Limit,
		After: p.After,
	}
}

// Slice returns the page of items that o selects, with the stores' own List
// semantics: a keeps the items whose key sorts strictly after it, s skips that
// many of the rest, and l caps what remains, so an l of 0 selects nothing. items
// must already be in ascending key order. The result is nil when the page is
// empty. Offsets are compared as uint64 and used as slice indices unconverted,
// so a large s or l cannot wrap.
func Slice[T any](items []T, key func(T) string, o store.ListOptions) []T {
	if o.After != "" {
		i := slices.IndexFunc(items, func(it T) bool { return key(it) > o.After })
		if i < 0 {
			return nil
		}
		items = items[i:]
	}
	if o.Limit == 0 || uint64(o.Start) >= uint64(len(items)) {
		return nil
	}
	items = items[o.Start:]
	if uint64(o.Limit) < uint64(len(items)) {
		items = items[:o.Limit]
	}
	return items
}

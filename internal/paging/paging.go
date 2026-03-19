package paging

import (
	"net/url"
	"strconv"

	"github.com/craig8/ieee-2030_5-go/pkg/store"
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

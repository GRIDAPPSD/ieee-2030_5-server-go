package paging_test

import (
	"fmt"
	"net/url"
	"strconv"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-go/internal/paging"
	"pgregory.net/rapid"
)

func TestParseQueryDefaults(t *testing.T) {
	p := paging.ParseQuery(url.Values{})
	if p.Start != 0 {
		t.Errorf("Start = %d, want 0", p.Start)
	}
	if p.Limit != paging.DefaultLimit {
		t.Errorf("Limit = %d, want %d", p.Limit, paging.DefaultLimit)
	}
	if p.After != "" {
		t.Errorf("After = %q, want empty", p.After)
	}
}

func TestParseQueryValues(t *testing.T) {
	q := url.Values{"s": {"5"}, "l": {"20"}, "a": {"1604963587"}}
	p := paging.ParseQuery(q)

	if p.Start != 5 {
		t.Errorf("Start = %d, want 5", p.Start)
	}
	if p.Limit != 20 {
		t.Errorf("Limit = %d, want 20", p.Limit)
	}
	if p.After != "1604963587" {
		t.Errorf("After = %q, want 1604963587", p.After)
	}
}

func TestParseQueryLimitCap(t *testing.T) {
	q := url.Values{"l": {"9999"}}
	p := paging.ParseQuery(q)

	if p.Limit != paging.MaxLimit {
		t.Errorf("Limit = %d, want max %d", p.Limit, paging.MaxLimit)
	}
}

func TestParseQueryInvalidValues(t *testing.T) {
	q := url.Values{"s": {"abc"}, "l": {"-1"}}
	p := paging.ParseQuery(q)

	if p.Start != 0 {
		t.Errorf("invalid start should default to 0, got %d", p.Start)
	}
	if p.Limit != paging.DefaultLimit {
		t.Errorf("invalid limit should default, got %d", p.Limit)
	}
}

func TestParseQueryLimitZero(t *testing.T) {
	q := url.Values{"l": {"0"}}
	p := paging.ParseQuery(q)

	if p.Limit != 0 {
		t.Errorf("Limit = %d, want 0 (explicit zero)", p.Limit)
	}
}

func TestToListOptions(t *testing.T) {
	p := paging.Params{Start: 3, Limit: 7, After: "test"}
	opts := p.ToListOptions()

	if opts.Start != 3 || opts.Limit != 7 || opts.After != "test" {
		t.Errorf("ToListOptions = %+v", opts)
	}
}

// TestPropLimitClamp is a property test (plan-4, IEEE-117).
//
// Property A (clamp): for any numeric value of the "l" query parameter,
// ParseQuery returns Params.Limit in [1, MaxLimit]. Never zero, never above
// 255, never negative.
//
// The generator explicitly produces numeric uint32 values — including zero —
// as the "l" key so that the full clamping contract is exercised. The
// all-random-string variant (below) covers the non-numeric input space.
//
// IEEE 2030.5 §8.2 / SEP2 spec section 4.6.2 define the `l` (limit) paging
// parameter. The spec is silent on whether `l=0` is valid; a client requesting
// zero items is a degenerate request. We choose to clamp to DefaultLimit
// (same as other invalid inputs) rather than returning zero — returning zero
// would cause a server to emit an empty list that looks like "no records" to
// a well-behaved client, which is incorrect behaviour for a populated resource.
func TestPropLimitClamp(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		// Generate a uint32 in [0, MaxLimit+10] so we exercise values below,
		// within, and above the valid range. Zero is included in the draw.
		v := rapid.Uint32Range(0, uint32(paging.MaxLimit)+10).Draw(rt, "l")
		q := url.Values{"l": {fmt.Sprintf("%d", v)}}

		p := paging.ParseQuery(q)

		if p.Limit < 1 {
			rt.Fatalf("Limit %d < 1 for l=%d", p.Limit, v)
		}
		if p.Limit > paging.MaxLimit {
			rt.Fatalf("Limit %d > MaxLimit %d for l=%d", p.Limit, paging.MaxLimit, v)
		}
	})
}

// TestPropDefaulting is a property test (plan-4, IEEE-117).
//
// Property B (defaulting): for any url.Values that contains no "s", "l", or
// "a" key, ParseQuery returns the documented defaults exactly:
//
//	Start = 0, Limit = DefaultLimit, After = "".
func TestPropDefaulting(t *testing.T) {
	// Build random keys that are never "s", "l", or "a".
	nonPagingKey := rapid.Custom(func(rt *rapid.T) string {
		s := rapid.StringN(1, 8, -1).Draw(rt, "s")
		for s == "s" || s == "l" || s == "a" {
			s = rapid.StringN(1, 8, -1).Draw(rt, "s")
		}
		return s
	})

	rapid.Check(t, func(rt *rapid.T) {
		n := rapid.IntRange(0, 4).Draw(rt, "n")
		q := make(url.Values, n)
		for i := range n {
			k := nonPagingKey.Draw(rt, fmt.Sprintf("key%d", i))
			v := rapid.StringN(0, 20, -1).Draw(rt, fmt.Sprintf("val%d", i))
			q.Set(k, v)
		}

		p := paging.ParseQuery(q)

		if p.Start != 0 {
			rt.Fatalf("Start = %d, want 0; input %v", p.Start, q)
		}
		if p.Limit != paging.DefaultLimit {
			rt.Fatalf("Limit = %d, want DefaultLimit %d; input %v", p.Limit, paging.DefaultLimit, q)
		}
		if p.After != "" {
			rt.Fatalf("After = %q, want empty; input %v", p.After, q)
		}
	})
}

// TestPropInvalidInputInvariance is a property test (plan-4, IEEE-117).
//
// Property C (invalid-input invariance): for any non-numeric or out-of-range
// value for "s" or "l", ParseQuery falls back to defaults — never panics,
// never returns an error to the caller.
func TestPropInvalidInputInvariance(t *testing.T) {
	// Generate strings that are definitely not valid uint32 decimal strings.
	nonNumeric := rapid.Custom(func(rt *rapid.T) string {
		// Produce a string that contains at least one non-ASCII-digit character.
		prefix := rapid.StringN(0, 5, -1).Draw(rt, "prefix")
		bad := rapid.Rune().Filter(func(r rune) bool { return r < '0' || r > '9' }).Draw(rt, "bad")
		suffix := rapid.StringN(0, 5, -1).Draw(rt, "suffix")
		return prefix + string(bad) + suffix
	})

	rapid.Check(t, func(rt *rapid.T) {
		q := url.Values{}
		// Optionally inject a bad "s" value.
		if rapid.Bool().Draw(rt, "badS") {
			q.Set("s", nonNumeric.Draw(rt, "sVal"))
		}
		// Optionally inject a bad "l" value.
		if rapid.Bool().Draw(rt, "badL") {
			q.Set("l", nonNumeric.Draw(rt, "lVal"))
		}
		// At least one bad key must be present (otherwise this degenerates to
		// the defaults test, which is fine but adds noise here).
		if len(q) == 0 {
			q.Set("l", nonNumeric.Draw(rt, "lVal2"))
		}

		// Must not panic. ParseQuery has no error return by design (silent
		// fallback per spec section 4.6.2).
		p := paging.ParseQuery(q)

		// Bad "s" must fall back to 0.
		if q.Get("s") != "" {
			if _, err := strconv.ParseUint(q.Get("s"), 10, 32); err != nil {
				if p.Start != 0 {
					rt.Fatalf("bad s %q: Start = %d, want 0", q.Get("s"), p.Start)
				}
			}
		}
		// Bad "l" must fall back to DefaultLimit.
		if q.Get("l") != "" {
			if _, err := strconv.ParseUint(q.Get("l"), 10, 32); err != nil {
				if p.Limit != paging.DefaultLimit {
					rt.Fatalf("bad l %q: Limit = %d, want DefaultLimit %d", q.Get("l"), p.Limit, paging.DefaultLimit)
				}
			}
		}
	})
}

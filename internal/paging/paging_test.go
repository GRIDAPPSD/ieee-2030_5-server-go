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
	// l=0 is a spec-defined edge case per CSIP V1.2 §5.6: a client may request
	// zero items to observe the All count without fetching any payload. The
	// server returns zero items and sets Results=0 while All still reflects the
	// full store count. ParseQuery must not clamp l=0 to DefaultLimit.
	q := url.Values{"l": {"0"}}
	p := paging.ParseQuery(q)

	if p.Limit != 0 {
		t.Errorf("l=0: Limit = %d, want 0 (spec-valid count-peek per CSIP V1.2 §5.6)", p.Limit)
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
// ParseQuery returns Params.Limit in [0, MaxLimit]. Never above 255, never
// negative (uint32 rules out negative; MaxLimit caps the upper bound).
//
// Note: l=0 is a spec-defined edge case per CSIP V1.2 §5.6 (count-peek: zero
// items returned, All still set). The lower bound here is 0, not 1.
// TestPropLimitZeroSpecEdge below documents the l=0 contract separately.
//
// plan-4 plan.md predicted a Limit=0 defect here; it did NOT surface because
// the implementation correctly passes through l=0 as per spec. The CSIP suite
// test TestCORE_004_ListHandling/limit_zero_returns_empty confirms this.
func TestPropLimitClamp(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		// Generate a uint32 in [0, MaxLimit+10] so we exercise values at,
		// within, and above the valid range. Zero is included in the draw.
		v := rapid.Uint32Range(0, uint32(paging.MaxLimit)+10).Draw(rt, "l")
		q := url.Values{"l": {fmt.Sprintf("%d", v)}}

		p := paging.ParseQuery(q)

		// Limit must never exceed MaxLimit (upper clamp is always active).
		if p.Limit > paging.MaxLimit {
			rt.Fatalf("Limit %d > MaxLimit %d for l=%d", p.Limit, paging.MaxLimit, v)
		}
		// For valid in-range inputs [0, MaxLimit], the parsed value must be
		// preserved exactly (including zero).
		if v <= uint32(paging.MaxLimit) && p.Limit != v {
			rt.Fatalf("Limit %d ≠ input %d (in-range, no clamping expected)", p.Limit, v)
		}
		// For over-range inputs (v > MaxLimit), Limit must equal MaxLimit.
		if v > uint32(paging.MaxLimit) && p.Limit != paging.MaxLimit {
			rt.Fatalf("Limit %d ≠ MaxLimit %d for over-range l=%d", p.Limit, paging.MaxLimit, v)
		}
	})
}

// TestPropLimitZeroSpecEdge documents the l=0 spec contract as a standalone
// property. CSIP V1.2 §5.6 defines l=0 as a "count-peek": the server returns
// zero items in the body but sets All to the full store count. ParseQuery must
// pass through Limit=0 without clamping.
func TestPropLimitZeroSpecEdge(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		// The "l" key is always "0"; other keys are random noise.
		q := url.Values{"l": {"0"}}
		n := rapid.IntRange(0, 3).Draw(rt, "n")
		for i := range n {
			k := rapid.StringN(1, 4, -1).Draw(rt, fmt.Sprintf("key%d", i))
			if k == "l" {
				continue // don't override the "l" key we just set
			}
			q.Set(k, rapid.StringN(0, 10, -1).Draw(rt, fmt.Sprintf("val%d", i)))
		}

		p := paging.ParseQuery(q)

		if p.Limit != 0 {
			rt.Fatalf("l=0: Limit = %d, want 0 (spec-valid count-peek per CSIP V1.2 §5.6)", p.Limit)
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

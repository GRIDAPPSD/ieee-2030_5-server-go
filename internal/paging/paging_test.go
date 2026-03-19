package paging_test

import (
	"net/url"
	"testing"

	"github.com/craig8/ieee-2030_5-go/internal/paging"
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

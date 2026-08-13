package store_test

import (
	"errors"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
)

// TestListOptionsZeroValueRequestsNoItems pins the meaning of the zero value.
//
// Adding the unbounded representation must not have moved it. Every existing
// caller that builds ListOptions without naming a field, and every caller that
// fills only the wire's s/l/a, depends on the zero value continuing to mean
// "no items, ascending by id", not "everything".
func TestListOptionsZeroValueRequestsNoItems(t *testing.T) {
	t.Parallel()

	var opts store.ListOptions

	if opts.Limit != 0 {
		t.Errorf("zero ListOptions Limit = %d, want 0", opts.Limit)
	}
	if opts.Unbounded {
		t.Error("zero ListOptions Unbounded = true, want false: " +
			"a caller that says nothing about paging must not receive the whole collection")
	}
	if opts.Start != 0 || opts.After != "" || opts.Sort != store.SortByIDAsc {
		t.Errorf("zero ListOptions = %+v, want Start 0, empty After, SortByIDAsc", opts)
	}
	if err := opts.Validate(); err != nil {
		t.Errorf("zero ListOptions Validate = %v, want nil", err)
	}
}

func TestListOptionsValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		opts    store.ListOptions
		wantErr error
	}{
		{
			// l=0 is a conformant request for the count without the contents.
			name: "limit zero alone is a valid request for no items",
			opts: store.ListOptions{Limit: 0},
		},
		{
			name: "an ordinary page is valid",
			opts: store.ListOptions{Start: 5, Limit: 10, After: "k", Sort: store.SortByIDDesc},
		},
		{
			name: "unbounded alone is valid",
			opts: store.ListOptions{Unbounded: true},
		},
		{
			name: "unbounded composes with start and after",
			opts: store.ListOptions{Start: 5, After: "k", Unbounded: true, Sort: store.SortByIDDesc},
		},
		{
			// Refused rather than resolved: honouring either field would be a
			// silently different answer from the one the caller wrote.
			name:    "unbounded with a page size is refused",
			opts:    store.ListOptions{Limit: 10, Unbounded: true},
			wantErr: store.ErrInvalidListOptions,
		},
		{
			name:    "unbounded with a page size of one is refused",
			opts:    store.ListOptions{Limit: 1, Unbounded: true},
			wantErr: store.ErrInvalidListOptions,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			err := tc.opts.Validate()
			if tc.wantErr == nil {
				if err != nil {
					t.Fatalf("Validate(%+v) = %v, want nil", tc.opts, err)
				}
				return
			}
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("Validate(%+v) = %v, want %v", tc.opts, err, tc.wantErr)
			}
		})
	}
}

// TestInvalidListOptionsIsNotAnotherSentinel keeps the new error out of the
// paths the other three drive. A caller matching on ErrNotFound must not see a
// caller bug as a 404, and vice versa.
func TestInvalidListOptionsIsNotAnotherSentinel(t *testing.T) {
	t.Parallel()

	err := store.ListOptions{Limit: 3, Unbounded: true}.Validate()
	if err == nil {
		t.Fatal("Validate on contradictory options = nil, want an error")
	}
	for _, other := range []error{store.ErrNotFound, store.ErrAlreadyExists, store.ErrUnsupportedSort} {
		if errors.Is(err, other) {
			t.Errorf("ErrInvalidListOptions matches %v, want them distinct", other)
		}
	}
}

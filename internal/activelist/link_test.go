package activelist_test

import (
	"context"
	"errors"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/activelist"
)

type fakeCounter struct {
	n   uint32
	err error
}

func (f fakeCounter) Count(context.Context, string) (uint32, error) { return f.n, f.err }

func derive(href string, ok bool) func() (string, bool) {
	return func() (string, bool) { return href, ok }
}

func TestStampLink(t *testing.T) {
	t.Parallel()
	boom := errors.New("boom")
	cases := []struct {
		name     string
		counter  fakeCounter
		link     *sep2.ListLink
		derive   func() (string, bool)
		wantErr  bool
		wantLink *sep2.ListLink
	}{
		{"count error clears a stored link", fakeCounter{err: boom}, &sep2.ListLink{Href: "/stored", All: 9}, derive("/derived", true), true, nil},
		{"count error leaves an absent link absent", fakeCounter{err: boom}, nil, derive("/derived", true), true, nil},
		{"stored href is kept, all recomputed", fakeCounter{n: 4}, &sep2.ListLink{Href: "/stored", All: 9}, derive("/derived", true), false, &sep2.ListLink{Href: "/stored", All: 4}},
		{"absent link is derived", fakeCounter{n: 2}, nil, derive("/derived", true), false, &sep2.ListLink{Href: "/derived", All: 2}},
		{"absent link with no derivable href stays absent", fakeCounter{n: 2}, nil, derive("", false), false, nil},
	}
	for _, tc := range cases {
		link := tc.link
		err := activelist.StampLink(context.Background(), tc.counter, "scope", &link, tc.derive)
		if (err != nil) != tc.wantErr {
			t.Errorf("%s: err = %v, wantErr %v", tc.name, err, tc.wantErr)
		}
		switch {
		case tc.wantLink == nil && link != nil:
			t.Errorf("%s: link = %+v, want nil", tc.name, *link)
		case tc.wantLink != nil && (link == nil || *link != *tc.wantLink):
			t.Errorf("%s: link = %+v, want %+v", tc.name, link, *tc.wantLink)
		}
	}
}

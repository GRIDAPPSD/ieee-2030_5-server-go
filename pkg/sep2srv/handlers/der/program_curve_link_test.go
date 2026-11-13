package der_test

import (
	"context"
	"errors"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	coredel "github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/der"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

type fixedCount struct {
	n   uint32
	err error
}

func (c fixedCount) Count(context.Context) (uint32, error) { return c.n, c.err }

type fixedParentCount struct{}

func (fixedParentCount) Count(context.Context, string) (uint32, error) { return 0, nil }

// The DERCurveListLink stamp serves a link only for a program that has an
// href to extend, no stored link, and a curve count that succeeded; zero
// curves is still a served link with all=0.
func TestProgramCurveListLinkStamp(t *testing.T) {
	t.Parallel()
	const href = "/edev/1/fsa/1/derp/1"
	stored := &sep2.ListLink{Href: "/dc", All: 9}
	cases := []struct {
		name     string
		counter  fixedCount
		wired    bool
		href     string
		link     *sep2.ListLink
		wantLink *sep2.ListLink
	}{
		{"three curves", fixedCount{n: 3}, true, href, nil, &sep2.ListLink{Href: href + "/dc", All: 3}},
		{"zero curves still links with all 0", fixedCount{}, true, href, nil, &sep2.ListLink{Href: href + "/dc", All: 0}},
		{"a stored link is left as stored", fixedCount{n: 3}, true, href, stored, stored},
		{"a program with no href gets no link", fixedCount{n: 3}, true, "", nil, nil},
		{"a failed count serves no link", fixedCount{err: errors.New("boom")}, true, href, nil, nil},
		{"curves not wired serves no link", fixedCount{n: 3}, false, href, nil, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			programs := memory.NewScopedStore[sep2.DERProgram]()
			p := sep2.DERProgram{DERCurveListLink: tc.link}
			p.Href = tc.href
			if err := programs.Create(ctx, "1", "1", p); err != nil {
				t.Fatalf("seed: %v", err)
			}
			counted := coredel.NewDERControlCountedProgramStore(programs, fixedParentCount{})
			if tc.wired {
				counted.WithCurveList(tc.counter)
			}
			check := func(op string, got sep2.DERProgram) {
				switch {
				case tc.wantLink == nil && got.DERCurveListLink != nil:
					t.Errorf("%s: DERCurveListLink = %+v, want none", op, got.DERCurveListLink)
				case tc.wantLink != nil && (got.DERCurveListLink == nil || *got.DERCurveListLink != *tc.wantLink):
					t.Errorf("%s: DERCurveListLink = %+v, want %+v", op, got.DERCurveListLink, *tc.wantLink)
				}
			}
			got, err := counted.Get(ctx, "1", "1")
			if err != nil {
				t.Fatalf("Get: %v", err)
			}
			check("Get", got)
			res, err := counted.List(ctx, "1", store.ListOptions{Unbounded: true})
			if err != nil || len(res.Items) != 1 {
				t.Fatalf("List = %+v, %v; want the one program", res, err)
			}
			check("List", res.Items[0])
		})
	}
}

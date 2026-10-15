package csip_test

import (
	"context"
	"strings"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/test/csip/csiptest"
)

// wantProgram names one DERProgram an FSA's DERProgramList must hold.
type wantProgram struct {
	mrid    string
	primacy uint8
}

// assertFSAProgramList walks fsa's DERProgramListLink and asserts the list
// holds exactly want, in Table 50 order, every member under that FSA's own
// path, with All equal to the count the FSA's link advertises. Each FSA lists
// only its own programs (CSIP IG v2 5.2.3), so a member of another FSA here is
// a scope leak. Each member's own href is then followed, the "GETs the
// DERProgram" step of the procedures, and must serve the same program.
func assertFSAProgramList(t *testing.T, ctx context.Context, c *csiptest.Client, fsa sep2.FunctionSetAssignments, want []wantProgram) []sep2.DERProgram {
	t.Helper()
	if fsa.DERProgramListLink == nil || fsa.DERProgramListLink.Href == "" {
		t.Fatalf("[%s] FSA has no DERProgramListLink", fsa.MRID)
	}
	listHref := fsa.DERProgramListLink.Href
	var list sep2.DERProgramList
	if err := c.WalkLink(ctx, sep2.Link{Href: listHref + "?l=255"}, &list); err != nil {
		t.Fatalf("[%s] walk DERProgramList %s: %v", fsa.MRID, listHref, err)
	}
	if list.All != uint32(len(want)) || list.Results != uint32(len(want)) || len(list.DERProgram) != len(want) {
		t.Fatalf("[%s] DERProgramList all=%d results=%d len=%d, want %d of its own programs %v",
			fsa.MRID, list.All, list.Results, len(list.DERProgram), len(want), want)
	}
	if list.All != fsa.DERProgramListLink.All {
		t.Errorf("[%s] DERProgramList.All = %d, but the FSA's DERProgramListLink advertises all=%d",
			fsa.MRID, list.All, fsa.DERProgramListLink.All)
	}
	for i, w := range want {
		got := list.DERProgram[i]
		if got.MRID != w.mrid || got.Primacy != w.primacy {
			t.Errorf("[%s] DERProgram[%d] = mRID %q primacy %d, want mRID %q primacy %d",
				fsa.MRID, i, got.MRID, got.Primacy, w.mrid, w.primacy)
		}
		if !strings.HasPrefix(got.Href, listHref+"/") {
			t.Errorf("[%s] DERProgram[%d].Href = %q, want a member of %s", fsa.MRID, i, got.Href, listHref)
			continue
		}
		var member sep2.DERProgram
		if err := c.WalkLink(ctx, sep2.Link{Href: got.Href}, &member); err != nil {
			t.Errorf("[%s] GET DERProgram %s: %v", fsa.MRID, got.Href, err)
			continue
		}
		if member.MRID != got.MRID || member.Primacy != got.Primacy || member.Href != got.Href {
			t.Errorf("[%s] GET %s = mRID %q primacy %d href %q, want the list member mRID %q primacy %d",
				fsa.MRID, got.Href, member.MRID, member.Primacy, member.Href, got.MRID, got.Primacy)
		}
	}
	return list.DERProgram
}

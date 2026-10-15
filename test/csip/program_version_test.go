//go:build csip_test_hooks

// DERProgram version over the wire (#919): the primacy mutation goes through
// the one edit function, so each real change raises the served version and an
// unchanged write does not.

package csip_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
)

func TestDERProgramVersion_ServedAndIncrementedByEdit(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	srv := bootAggregatorTopology(t)
	client := srv.Client()
	const programHref = "/edev/1/fsa/0/derp/0"
	const listHref = "/edev/1/fsa/0/derp?l=255"

	servedVersion := func() (single *uint16, list *uint16) {
		t.Helper()
		var p sep2.DERProgram
		if err := client.WalkLink(ctx, sep2.Link{Href: programHref}, &p); err != nil {
			t.Fatalf("GET %s: %v", programHref, err)
		}
		var l sep2.DERProgramList
		if err := client.WalkLink(ctx, sep2.Link{Href: listHref}, &l); err != nil {
			t.Fatalf("GET %s: %v", listHref, err)
		}
		if len(l.DERProgram) != 1 {
			t.Fatalf("GET %s: %d programs, want 1", listHref, len(l.DERProgram))
		}
		return p.Version, l.DERProgram[0].Version
	}
	setPrimacy := func(primacy uint8) {
		t.Helper()
		status, body := postMutationJSON(t, srv, mutateDERProgPrimacy, map[string]any{
			"end_device_id": "1", "program_id": "0", "primacy": primacy,
		})
		if status != http.StatusNoContent {
			t.Fatalf("primacy %d: status %d: %s", primacy, status, body)
		}
	}
	check := func(step string, want uint16) {
		t.Helper()
		single, list := servedVersion()
		for name, got := range map[string]*uint16{"single": single, "list": list} {
			if got == nil || *got != want {
				t.Errorf("%s, %s route: version = %v, want %d", step, name, got, want)
			}
		}
	}

	if single, list := servedVersion(); single != nil || list != nil {
		t.Fatalf("fixture program already carries a version: single=%v list=%v", single, list)
	}
	setPrimacy(20)
	check("first edit", 1)
	setPrimacy(21)
	check("second edit", 2)
	setPrimacy(21)
	check("unchanged write", 2)
}

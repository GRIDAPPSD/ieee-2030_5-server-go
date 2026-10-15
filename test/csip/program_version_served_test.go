// The version stored on a DERProgram reaches the wire on both program routes
// in a default build (#919); the tagged test covers incrementing it.

package csip_test

import (
	"context"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
)

func TestDERProgramVersion_StoredValueIsServedOnListAndSingleRoutes(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	srv := bootAggregatorTopology(t)

	prog, err := srv.Stores.DERPrograms.Get(ctx, "1", "0")
	if err != nil {
		t.Fatal(err)
	}
	v := uint16(42)
	prog.Version = &v
	if err := srv.Stores.DERPrograms.Update(ctx, "1", "0", prog); err != nil {
		t.Fatal(err)
	}

	var single sep2.DERProgram
	if err := srv.Client().WalkLink(ctx, sep2.Link{Href: "/edev/1/fsa/0/derp/0"}, &single); err != nil {
		t.Fatal(err)
	}
	var list sep2.DERProgramList
	if err := srv.Client().WalkLink(ctx, sep2.Link{Href: "/edev/1/fsa/0/derp?l=255"}, &list); err != nil {
		t.Fatal(err)
	}
	if single.Version == nil || *single.Version != 42 {
		t.Errorf("single route version = %v, want 42", single.Version)
	}
	if len(list.DERProgram) != 1 || list.DERProgram[0].Version == nil || *list.DERProgram[0].Version != 42 {
		t.Errorf("list route programs = %+v, want one with version 42", list.DERProgram)
	}
}

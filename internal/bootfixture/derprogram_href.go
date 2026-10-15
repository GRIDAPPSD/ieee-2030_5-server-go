package bootfixture

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/derhref"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/der"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
)

// ErrDERProgramWithoutFSA reports stored DERPrograms whose href names no FSA.
var ErrDERProgramWithoutFSA = errors.New("stored DERProgram href names no FSA")

// CheckDERProgramHrefs refuses a store holding a DERProgram whose href names
// no FSA, naming every one. Each FSA lists only the programs its segment of
// their href names (#916), so such a program would be served under no FSA;
// a program the boot fixture names has already been moved to the fixture's
// FSA by Reconcile, and any left here have no record of their FSA.
func CheckDERProgramHrefs(ctx context.Context, programs store.ScopedReader[sep2.DERProgram]) error {
	parents, err := programs.Parents(ctx)
	if err != nil {
		return fmt.Errorf("bootfixture: list DERProgram parents: %w", err)
	}
	var bad []string
	for _, edev := range parents {
		res, err := programs.List(ctx, edev, store.ListOptions{Unbounded: true})
		if err != nil {
			return fmt.Errorf("bootfixture: list DERPrograms of %q: %w", edev, err)
		}
		for _, p := range res.Items {
			if _, _, _, ok := derhref.Program(p.Href); !ok {
				bad = append(bad, fmt.Sprintf("edev=%q mRID=%q href=%q", edev, p.MRID, p.Href))
			}
		}
	}
	if len(bad) == 0 {
		return nil
	}
	return fmt.Errorf("bootfixture: %w: %s; name each program in the boot fixture with its fsa_id, or delete it",
		ErrDERProgramWithoutFSA, strings.Join(bad, "; "))
}

// migratedProgram returns prog with its href, and every link under it,
// moved to the canonical href of program id in FSA fsaID.
func migratedProgram(prog sep2.DERProgram, edev, fsaID, id string) sep2.DERProgram {
	oldHref := prog.Href
	newHref := der.DERProgramHref(edev, fsaID, id)
	restamp := func(href string) string {
		if rest, ok := strings.CutPrefix(href, oldHref+"/"); ok && oldHref != "" {
			return newHref + "/" + rest
		}
		return href
	}
	prog.Href = newHref
	if prog.DefaultDERControlLink != nil {
		prog.DefaultDERControlLink.Href = restamp(prog.DefaultDERControlLink.Href)
	}
	for _, l := range []*sep2.ListLink{prog.ActiveDERControlListLink, prog.DERControlListLink, prog.DERCurveListLink} {
		if l != nil {
			l.Href = restamp(l.Href)
		}
	}
	return prog
}

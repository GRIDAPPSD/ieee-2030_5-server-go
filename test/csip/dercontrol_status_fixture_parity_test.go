// Fixture-wide proof for PR 726 (#564) fix round 1: acceptance criterion 2
// asks for a byte comparison over every control in every CSIP fixture, and
// criterion 4 the same for DERControlListLink.all over every fixture
// program. Both compare the decorated store (pkg/sep2srv/handlers/der)
// against the plain, undecorated one, directly at the store level: no
// server is booted, since the property under test is the decorator's own
// output, not the wire encoding a booted server would add on top.
package csip_test

import (
	"context"
	"encoding/xml"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/dercontrol"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/derhref"
	coreder "github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/der"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/test/csip/csiptest"
)

// fixtureYAMLFiles lists every fixture, sorted, so the run order is
// deterministic. A pattern that cannot match (an empty or missing
// directory) is a hard failure here, not a silent 0: this test's whole
// premise is that every fixture stands in for a specific, non-zero
// denominator.
func fixtureYAMLFiles(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir("fixtures")
	if err != nil {
		t.Fatalf("read fixtures dir: %v", err)
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if filepath.Ext(e.Name()) == ".yaml" {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	if len(names) == 0 {
		t.Fatal("fixtures dir has no .yaml files; the fixture-wide checks below would silently check nothing")
	}
	return names
}

// newParityTarget builds a fresh, independent set of in-memory stores for
// one fixture load: no state carries between fixtures or between this and
// any other test in the package.
func newParityTarget() *csiptest.Target {
	return &csiptest.Target{
		EndDevices:         memory.NewEndDeviceStore(),
		FSAs:               memory.NewScopedStore[sep2.FunctionSetAssignments](),
		DERPrograms:        memory.NewScopedStore[sep2.DERProgram](),
		DERControls:        memory.NewScopedStore[sep2.DERControl](),
		DefaultDERControls: memory.NewScopedStore[sep2.DefaultDERControl](),
		DERCurves:          memory.NewStore[sep2.DERCurve](),
		EndDeviceManagers:  memory.NewEndDeviceManagementStore(),
	}
}

func marshalOrFatal(t *testing.T, label string, ctrl *sep2.DERControl) []byte {
	t.Helper()
	b, err := xml.Marshal(ctrl)
	if err != nil {
		t.Fatalf("marshal %s: %v", label, err)
	}
	return b
}

// TestDERControlEventStatusByteParityAcrossCSIPFixtures is acceptance
// criterion 2's fixture-wide proof: every control loaded from every CSIP
// fixture has no lifecycle record, so the decorated store's Get and List
// must serve it byte-identical to the plain store, for every control.
//
// A single synthetic control (the original test at commit deedd49) cannot
// stand for this: it proves the decorator behaves on one shape of input
// the author chose, not on the 29 real ones the CSIP suite actually
// exercises. This test also proves the comparison can fail: after the
// byte-identical pass, it adds a lifecycle record to the first control of
// each fixture that has one and asserts the bytes then DIFFER, the same
// direction PROVE THE CHECK CAN FAIL requires of a zero.
func TestDERControlEventStatusByteParityAcrossCSIPFixtures(t *testing.T) {
	names := fixtureYAMLFiles(t)
	totalControls := 0
	perturbedFixtures := 0

	for _, name := range names {
		name := name
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			target := newParityTarget()
			if err := csiptest.Load(ctx, target, filepath.Join("fixtures", name)); err != nil {
				t.Fatalf("load: %v", err)
			}

			lifecycles := memory.NewScopedStore[dercontrol.LifecycleRecord]()
			decorated := coreder.NewDerivedStatusControlStore(target.DERControls, lifecycles)

			scopes, err := target.DERControls.Parents(ctx)
			if err != nil {
				t.Fatalf("DERControls.Parents: %v", err)
			}

			fixtureControls := 0
			var firstScope, firstID string
			for _, scope := range scopes {
				undecoratedList, err := target.DERControls.List(ctx, scope, store.ListOptions{Unbounded: true})
				if err != nil {
					t.Fatalf("List(%s) undecorated: %v", scope, err)
				}
				decoratedList, err := decorated.List(ctx, scope, store.ListOptions{Unbounded: true})
				if err != nil {
					t.Fatalf("List(%s) decorated: %v", scope, err)
				}
				if len(decoratedList.Items) != len(undecoratedList.Items) {
					t.Fatalf("List(%s): decorated served %d items, undecorated %d", scope, len(decoratedList.Items), len(undecoratedList.Items))
				}

				for i := range undecoratedList.Items {
					ctrl := undecoratedList.Items[i]
					id, ok := derhref.ControlID(ctrl.Href)
					if !ok {
						t.Fatalf("control href %q does not parse to a store id", ctrl.Href)
					}
					fixtureControls++
					totalControls++
					// A control with no interval can never derive a status
					// (there is no start to derive one from): the issuer
					// always sets one, so only a fixture control not shaped
					// like an admin-issued one can lack it. Only such a
					// control can be perturbed below.
					if firstScope == "" && ctrl.Interval != nil {
						firstScope, firstID = scope, id
					}

					wantListBytes := marshalOrFatal(t, "undecorated list member "+id, &undecoratedList.Items[i])
					gotListBytes := marshalOrFatal(t, "decorated list member "+id, &decoratedList.Items[i])
					if string(wantListBytes) != string(gotListBytes) {
						t.Errorf("List(%s)[%s]: decorated bytes differ from undecorated with no lifecycle record\nundecorated: %s\ndecorated:   %s", scope, id, wantListBytes, gotListBytes)
					}

					gotGet, err := decorated.Get(ctx, scope, id)
					if err != nil {
						t.Fatalf("Get(%s,%s) decorated: %v", scope, id, err)
					}
					wantGetBytes := marshalOrFatal(t, "undecorated get "+id, &ctrl)
					gotGetBytes := marshalOrFatal(t, "decorated get "+id, &gotGet)
					if string(wantGetBytes) != string(gotGetBytes) {
						t.Errorf("Get(%s,%s): decorated bytes differ from undecorated with no lifecycle record\nundecorated: %s\ndecorated:   %s", scope, id, wantGetBytes, gotGetBytes)
					}
				}
			}

			t.Logf("%s: %d controls, 0 byte differences (no lifecycle records)", name, fixtureControls)
			if firstScope == "" {
				// No control in this fixture carries an interval, so none
				// is eligible for derivation at all; nothing to perturb.
				return
			}

			// Control: prove this comparison can fail. Add a lifecycle
			// record to the first control found and require the bytes to
			// now diverge.
			cancelledAt := int64(1)
			if err := lifecycles.Create(ctx, firstScope, firstID, dercontrol.LifecycleRecord{CancelledAt: &cancelledAt}); err != nil {
				t.Fatalf("seed perturbing lifecycle record for %s/%s: %v", firstScope, firstID, err)
			}
			undecorated, err := target.DERControls.Get(ctx, firstScope, firstID)
			if err != nil {
				t.Fatalf("Get(%s,%s) undecorated after perturb: %v", firstScope, firstID, err)
			}
			gotDecorated, err := decorated.Get(ctx, firstScope, firstID)
			if err != nil {
				t.Fatalf("Get(%s,%s) decorated after perturb: %v", firstScope, firstID, err)
			}
			wantBytes := marshalOrFatal(t, "undecorated after perturb", &undecorated)
			gotBytes := marshalOrFatal(t, "decorated after perturb", &gotDecorated)
			if string(wantBytes) == string(gotBytes) {
				t.Errorf("after adding a lifecycle record to %s/%s, decorated bytes still equal undecorated; this comparison cannot fail", firstScope, firstID)
			} else {
				perturbedFixtures++
			}
		})
	}

	t.Logf("checked %d controls across %d fixtures; %d fixtures had at least one control and were perturbed to prove the check can fail", totalControls, len(names), perturbedFixtures)
}

// TestDERProgramControlListLinkAllAcrossCSIPFixtures is acceptance
// criterion 4's fixture-wide proof: every DERProgram loaded from every
// CSIP fixture that advertises a DERControlListLink must serve .All as the
// live count of controls in the scope the link names, cross-checked
// against an independent Count call, not merely against this decorator's
// own List path.
func TestDERProgramControlListLinkAllAcrossCSIPFixtures(t *testing.T) {
	names := fixtureYAMLFiles(t)
	totalPrograms := 0
	agreedWithStoredValue := 0

	for _, name := range names {
		name := name
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			target := newParityTarget()
			if err := csiptest.Load(ctx, target, filepath.Join("fixtures", name)); err != nil {
				t.Fatalf("load: %v", err)
			}

			decorated := coreder.NewDERControlCountedProgramStore(target.DERPrograms, target.DERControls)

			deviceIDs, err := target.DERPrograms.Parents(ctx)
			if err != nil {
				t.Fatalf("DERPrograms.Parents: %v", err)
			}

			fixturePrograms := 0
			for _, devID := range deviceIDs {
				undecorated, err := target.DERPrograms.List(ctx, devID, store.ListOptions{Unbounded: true})
				if err != nil {
					t.Fatalf("DERPrograms.List(%s) undecorated: %v", devID, err)
				}
				decoratedList, err := decorated.List(ctx, devID, store.ListOptions{Unbounded: true})
				if err != nil {
					t.Fatalf("DERPrograms.List(%s) decorated: %v", devID, err)
				}
				if len(decoratedList.Items) != len(undecorated.Items) {
					t.Fatalf("DERPrograms.List(%s): decorated served %d items, undecorated %d", devID, len(decoratedList.Items), len(undecorated.Items))
				}

				for i, prog := range undecorated.Items {
					if prog.DERControlListLink == nil {
						continue
					}
					fixturePrograms++
					totalPrograms++

					scopeKey, ok := derhref.ControlListScope(prog.DERControlListLink.Href)
					if !ok {
						t.Fatalf("program %q DERControlListLink.Href %q does not parse", prog.Href, prog.DERControlListLink.Href)
					}
					wantCount, err := target.DERControls.Count(ctx, scopeKey)
					if err != nil {
						t.Fatalf("DERControls.Count(%s): %v", scopeKey, err)
					}
					got := decoratedList.Items[i].DERControlListLink.All
					if got != wantCount {
						t.Errorf("program %s: DERControlListLink.All = %d, want %d (independently counted)", prog.Href, got, wantCount)
					}
					if got == prog.DERControlListLink.All {
						agreedWithStoredValue++
					}
				}
			}
			t.Logf("%s: %d programs with a DERControlListLink checked", name, fixturePrograms)
		})
	}

	t.Logf("checked %d programs across %d fixtures; %d had a live count equal to their fixture-stored all value", totalPrograms, len(names), agreedWithStoredValue)
}

// TestDERProgramControlListLinkAllChangesWhenAControlIsAdded is the "prove
// it can fail" control for the fixture-wide check above: a decorator whose
// .All always happened to equal the fixture-stored value (a tautology, not
// a live count) would still pass every case in that test. This adds a
// second control to BASIC-017's lone program and requires .All to move
// from 1 to 2.
func TestDERProgramControlListLinkAllChangesWhenAControlIsAdded(t *testing.T) {
	ctx := context.Background()
	target := newParityTarget()
	if err := csiptest.Load(ctx, target, filepath.Join("fixtures", "basic-017-1derp-0dderc-1derc.yaml")); err != nil {
		t.Fatalf("load: %v", err)
	}
	decorated := coreder.NewDERControlCountedProgramStore(target.DERPrograms, target.DERControls)

	before, err := decorated.Get(ctx, "0", "0")
	if err != nil {
		t.Fatalf("Get before: %v", err)
	}
	if before.DERControlListLink == nil || before.DERControlListLink.All != 1 {
		t.Fatalf("before adding a control: DERControlListLink = %+v, want All=1 (premise of this test)", before.DERControlListLink)
	}

	extra := sep2.DERControl{DERControlBase: &sep2.DERControlBase{OpModTargetW: &sep2.ActivePower{Value: 1}}}
	extra.Href = "/edev/0/fsa/0/derp/0/derc/extra"
	extra.Interval = &sep2.DateTimeInterval{Start: 1700000200, Duration: 60}
	if err := target.DERControls.Create(ctx, "0/0/0", "extra", extra); err != nil {
		t.Fatalf("add extra control: %v", err)
	}

	after, err := decorated.Get(ctx, "0", "0")
	if err != nil {
		t.Fatalf("Get after: %v", err)
	}
	if after.DERControlListLink == nil || after.DERControlListLink.All != 2 {
		t.Fatalf("after adding a control: DERControlListLink = %+v, want All=2", after.DERControlListLink)
	}
}

package bootfixture

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

// ErrIdentityConflict reports a fixture EndDevice, not yet in the seed record,
// whose id a persisted EndDevice of a different LFDI holds, or whose LFDI or
// SFDI another persisted EndDevice holds.
var ErrIdentityConflict = errors.New("boot fixture EndDevice conflicts with a persisted EndDevice")

// Logf receives the line logged for each fixture record Reconcile skips.
type Logf func(format string, args ...any)

// Reconcile applies the fixture at fixturePath as a seed that yields to
// persisted state (#352). seedPath is the seed record: the EndDevice and
// DERProgram records a fixture has already created or adopted in this
// data_dir. Each of those is applied at most once, so edits and deletes made
// between boots survive. The in-memory kinds load on every boot unless their
// EndDevice or DERProgram was skipped.
//
// An empty seedPath means no data_dir, and Reconcile is exactly Load.
//
// Every action is planned before any write, so a conflict or an untrusted
// seed record fails with the stores and the seed record untouched. The seed
// record is written last: a crash before it leaves records the next boot
// adopts, where the opposite order would mark records seeded that never
// reached a store. A nil logf logs through the standard logger.
func Reconcile(ctx context.Context, target *Target, fixturePath, seedPath string, logf Logf) error {
	if seedPath == "" {
		return Load(ctx, target, fixturePath)
	}
	if logf == nil {
		logf = log.Printf
	}
	spec, err := readSpec(target, fixturePath)
	if err != nil {
		return err
	}
	// Throwaway stores run the fixture's own checks exactly as Load does
	// (required ids, duplicates, unknown parents) without writing to target.
	if err := applySpec(ctx, scratchTarget(), spec); err != nil {
		return fmt.Errorf("bootfixture: apply fixture %s: %w", fixturePath, err)
	}
	seeded, err := readSeedRecord(seedPath)
	if err != nil {
		return fmt.Errorf("bootfixture: %w", err)
	}
	plan, err := planReconcile(ctx, target, spec, seeded)
	if err != nil {
		return fmt.Errorf("bootfixture: reconcile fixture %s: %w", fixturePath, err)
	}
	for _, line := range plan.skips {
		logf("boot fixture: skipping %s", line)
	}
	if err := plan.apply(ctx, target); err != nil {
		return fmt.Errorf("bootfixture: apply fixture %s: %w", fixturePath, err)
	}
	if plan.seedChanged {
		if err := writeSeedRecord(seedPath, plan.seeded); err != nil {
			return fmt.Errorf("bootfixture: %w", err)
		}
	}
	return nil
}

func scratchTarget() *Target {
	return &Target{
		EndDevices:         memory.NewEndDeviceStore(),
		FSAs:               memory.NewScopedStore[sep2.FunctionSetAssignments](),
		DERPrograms:        memory.NewScopedStore[sep2.DERProgram](),
		DERControls:        memory.NewScopedStore[sep2.DERControl](),
		DefaultDERControls: memory.NewScopedStore[sep2.DefaultDERControl](),
		DERCurves:          memory.NewStore[sep2.DERCurve](),
	}
}

// indexed keeps a fixture record's position so errors name the entry.
type indexed[T any] struct {
	i    int
	spec T
}

// reconcilePlan holds the records to create and the seed record to write.
type reconcilePlan struct {
	endDevices         []indexed[EndDeviceSpec]
	fsas               []indexed[FSASpec]
	derPrograms        []indexed[DERProgramSpec]
	defaultDERControls []indexed[DefaultDERControlSpec]
	derControls        []indexed[DERControlSpec]
	derCurves          []indexed[DERCurveSpec]

	seeded      map[seedKey]struct{}
	seedChanged bool
	skips       []string
}

func (p *reconcilePlan) record(key seedKey) {
	if _, ok := p.seeded[key]; ok {
		return
	}
	p.seeded[key] = struct{}{}
	p.seedChanged = true
}

func (p *reconcilePlan) skip(format string, args ...any) {
	p.skips = append(p.skips, fmt.Sprintf(format, args...))
}

// planReconcile decides every record's action without writing.
func planReconcile(ctx context.Context, target *Target, spec *Spec, seeded map[seedKey]struct{}) (*reconcilePlan, error) {
	p := &reconcilePlan{seeded: seeded}
	skippedEndDevices := make(map[string]struct{})
	skippedPrograms := make(map[[2]string]struct{})

	for i, e := range spec.EndDevices {
		key := seedKey{Kind: kindEndDevice, ID: e.ID}
		_, wasSeeded := seeded[key]
		stored, err := target.EndDevices.Get(ctx, e.ID)
		switch {
		case err == nil:
			switch {
			case strings.EqualFold(stored.LFDI, e.LFDI): // same identity: keep, or adopt if unseeded
				if !wasSeeded {
					if err := checkIdentifiersFree(ctx, target.EndDevices, i, e, &stored); err != nil {
						return nil, err
					}
				}
				p.record(key)
			case wasSeeded: // deleted, then the id was reallocated
				skippedEndDevices[e.ID] = struct{}{}
				p.skip("EndDevice id=%q: id now held by another identity", e.ID)
			default:
				return nil, fmt.Errorf("end_devices[%d] (id=%q): %w: a persisted EndDevice with a different LFDI holds this id",
					i, e.ID, ErrIdentityConflict)
			}
		case errors.Is(err, store.ErrNotFound):
			if wasSeeded {
				skippedEndDevices[e.ID] = struct{}{}
				p.skip("EndDevice id=%q: deleted since seeded", e.ID)
				continue
			}
			if err := checkIdentifiersFree(ctx, target.EndDevices, i, e, nil); err != nil {
				return nil, err
			}
			p.endDevices = append(p.endDevices, indexed[EndDeviceSpec]{i, e})
			p.record(key)
		default:
			return nil, fmt.Errorf("end_devices[%d] (id=%q): read persisted EndDevice: %w", i, e.ID, err)
		}
	}

	for i, d := range spec.DERPrograms {
		key := seedKey{Kind: kindDERProgram, Parent: d.EndDeviceID, ID: d.ID}
		if _, skipped := skippedEndDevices[d.EndDeviceID]; skipped {
			// Recorded so a reused parent id never receives this program later.
			p.record(key)
			skippedPrograms[[2]string{d.EndDeviceID, d.ID}] = struct{}{}
			p.skip("DERProgram edev=%q id=%q: parent EndDevice skipped", d.EndDeviceID, d.ID)
			continue
		}
		_, wasSeeded := seeded[key]
		_, err := target.DERPrograms.Get(ctx, d.EndDeviceID, d.ID)
		switch {
		case err == nil: // keep, or adopt if unseeded
			p.record(key)
		case !errors.Is(err, store.ErrNotFound):
			return nil, fmt.Errorf("der_programs[%d] (id=%q): read persisted DERProgram: %w", i, d.ID, err)
		case wasSeeded:
			skippedPrograms[[2]string{d.EndDeviceID, d.ID}] = struct{}{}
			p.skip("DERProgram edev=%q id=%q: deleted since seeded", d.EndDeviceID, d.ID)
		default:
			p.derPrograms = append(p.derPrograms, indexed[DERProgramSpec]{i, d})
			p.record(key)
		}
	}

	underSkipped := func(edev, derp string) bool {
		if _, ok := skippedEndDevices[edev]; ok {
			return true
		}
		_, ok := skippedPrograms[[2]string{edev, derp}]
		return ok
	}
	for i, f := range spec.FSAs {
		if _, ok := skippedEndDevices[f.EndDeviceID]; !ok {
			p.fsas = append(p.fsas, indexed[FSASpec]{i, f})
		}
	}
	for i, d := range spec.DefaultDERControls {
		if !underSkipped(d.EndDeviceID, d.DERProgramID) {
			p.defaultDERControls = append(p.defaultDERControls, indexed[DefaultDERControlSpec]{i, d})
		}
	}
	for i, c := range spec.DERControls {
		if !underSkipped(c.EndDeviceID, c.DERProgramID) {
			p.derControls = append(p.derControls, indexed[DERControlSpec]{i, c})
		}
	}
	for i, c := range spec.DERCurves {
		p.derCurves = append(p.derCurves, indexed[DERCurveSpec]{i, c})
	}
	return p, nil
}

// checkIdentifiersFree fails when a persisted EndDevice other than self holds
// the fixture record's LFDI or SFDI. Creating the record would repoint the
// store's LFDI or SFDI index at it, and adopting it would leave two records
// with one identity. self is the stored record under the fixture id, or nil.
func checkIdentifiersFree(ctx context.Context, s store.EndDeviceStore, i int, e EndDeviceSpec, self *sep2.EndDevice) error {
	holder, which, err := endDeviceHoldingIdentifier(ctx, s, e, self)
	if err != nil {
		return fmt.Errorf("end_devices[%d] (id=%q): look up persisted identifiers: %w", i, e.ID, err)
	}
	if which != "" {
		return fmt.Errorf("end_devices[%d] (id=%q): %w: its %s is held by persisted EndDevice %q",
			i, e.ID, ErrIdentityConflict, which, holder.Href)
	}
	return nil
}

// endDeviceHoldingIdentifier returns the first persisted EndDevice other than
// self whose LFDI matches e's ignoring case, or whose SFDI equals e's, and
// names which matched. It scans rather than calling GetByLFDI or GetBySFDI:
// the LFDI match ignores case, and each index keeps one id per value, so it
// can hide a second holder. List returns no store ids, so self is skipped by
// matching it once on href, LFDI and SFDI; when nothing matches self, every
// holder counts.
func endDeviceHoldingIdentifier(ctx context.Context, s store.EndDeviceStore, e EndDeviceSpec, self *sep2.EndDevice) (sep2.EndDevice, string, error) {
	if e.LFDI == "" && e.SFDI == "" {
		return sep2.EndDevice{}, "", nil
	}
	list, err := s.List(ctx, store.ListOptions{Unbounded: true})
	if err != nil {
		return sep2.EndDevice{}, "", err
	}
	selfSkipped := self == nil
	for _, dev := range list.Items {
		if !selfSkipped && dev.Href == self.Href && dev.LFDI == self.LFDI && dev.SFDI == self.SFDI {
			selfSkipped = true
			continue
		}
		switch {
		case e.LFDI != "" && strings.EqualFold(dev.LFDI, e.LFDI):
			return dev, "LFDI", nil
		case e.SFDI != "" && dev.SFDI == e.SFDI:
			return dev, "SFDI", nil
		}
	}
	return sep2.EndDevice{}, "", nil
}

func (p *reconcilePlan) apply(ctx context.Context, target *Target) error {
	for _, r := range p.endDevices {
		if err := createEndDevice(ctx, target, r.i, r.spec); err != nil {
			return err
		}
	}
	for _, r := range p.fsas {
		if err := createFSA(ctx, target, r.i, r.spec); err != nil {
			return err
		}
	}
	for _, r := range p.derPrograms {
		if err := createDERProgram(ctx, target, r.i, r.spec); err != nil {
			return err
		}
	}
	for _, r := range p.defaultDERControls {
		if err := createDefaultDERControl(ctx, target, r.i, r.spec); err != nil {
			return err
		}
	}
	for _, r := range p.derControls {
		if err := createDERControl(ctx, target, r.i, r.spec); err != nil {
			return err
		}
	}
	for _, r := range p.derCurves {
		if err := createDERCurve(ctx, target, r.i, r.spec); err != nil {
			return err
		}
	}
	return nil
}

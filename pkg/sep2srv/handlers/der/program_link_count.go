package der

import (
	"context"
	"log"
	"strings"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/derhref"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
)

// DERControlCounter is the read-only subset of a DERControl store this
// package needs to recompute a served DERProgram's DERControlListLink.all.
// Defined at this consumer per the project's Go standard.
type DERControlCounter interface {
	Count(ctx context.Context, parentID string) (uint32, error)
}

// DERControlCountedProgramStore decorates a DERProgram store so every served
// program's DERControlListLink.all is the LIVE count of controls in the
// scope the link names, rather than whatever value was stored (a boot
// fixture, or a stale count from before a control was added or cancelled
// after boot; cancelling leaves a control stored, so it still counts).
//
// It decorates GET and LIST only; every other method delegates straight to
// programs, unchanged.
type DERControlCountedProgramStore struct {
	programs store.ScopedStore[sep2.DERProgram]
	controls DERControlCounter
}

// NewDERControlCountedProgramStore decorates programs with controls.
func NewDERControlCountedProgramStore(programs store.ScopedStore[sep2.DERProgram], controls DERControlCounter) *DERControlCountedProgramStore {
	return &DERControlCountedProgramStore{programs: programs, controls: controls}
}

var _ store.ScopedStore[sep2.DERProgram] = (*DERControlCountedProgramStore)(nil)

func (s *DERControlCountedProgramStore) Get(ctx context.Context, parentID, id string) (sep2.DERProgram, error) {
	p, err := s.programs.Get(ctx, parentID, id)
	if err != nil {
		return p, err
	}
	s.stampCount(ctx, &p)
	return p, nil
}

func (s *DERControlCountedProgramStore) List(ctx context.Context, parentID string, opts store.ListOptions) (store.ListResult[sep2.DERProgram], error) {
	result, err := s.programs.List(ctx, parentID, opts)
	if err != nil {
		return result, err
	}
	for i := range result.Items {
		s.stampCount(ctx, &result.Items[i])
	}
	return result, nil
}

// stampCount overwrites p.DERControlListLink.All with the live count of
// controls in the scope the link names, parsed from the link's own href
// exactly as the issuer resolves it (internal/dercontrol). A program with no
// link, a link whose href does not parse to the expected shape, or a count
// that fails is left with its stored value and logged: this is a display
// derivation, not a security boundary, so it degrades rather than failing
// the whole request, the same direction [FillAbsentDERLinks] takes.
func (s *DERControlCountedProgramStore) stampCount(ctx context.Context, p *sep2.DERProgram) {
	if p.DERControlListLink == nil {
		return
	}
	scopeKey, ok := controlListScopeKey(p.DERControlListLink.Href)
	if !ok {
		return
	}
	n, err := s.controls.Count(ctx, scopeKey)
	if err != nil {
		log.Printf("der: counting DERControls for %q: %v; serving the stored DERControlListLink.all", p.DERControlListLink.Href, err)
		return
	}
	p.DERControlListLink.All = n
}

// controlListScopeKey pulls the composite (edev, fsa, derp) scope key out of
// a DERControlListLink href ("/edev/{id}/fsa/{fsaId}/derp/{derpId}/derc"),
// the same shape and the same key format [scopedListHandlerDeep] scopes the
// control store by. ok is false for any other shape.
func controlListScopeKey(href string) (string, bool) {
	trimmed, ok := strings.CutSuffix(strings.TrimSpace(href), "/derc")
	if !ok {
		return "", false
	}
	edev, fsa, derp, ok := derhref.Program(trimmed)
	if !ok {
		return "", false
	}
	return edev + "/" + fsa + "/" + derp, true
}

func (s *DERControlCountedProgramStore) Count(ctx context.Context, parentID string) (uint32, error) {
	return s.programs.Count(ctx, parentID)
}

func (s *DERControlCountedProgramStore) HasParent(ctx context.Context, parentID string) (bool, error) {
	return s.programs.HasParent(ctx, parentID)
}

func (s *DERControlCountedProgramStore) Parents(ctx context.Context) ([]string, error) {
	return s.programs.Parents(ctx)
}

func (s *DERControlCountedProgramStore) Create(ctx context.Context, parentID, id string, resource sep2.DERProgram) error {
	return s.programs.Create(ctx, parentID, id, resource)
}

func (s *DERControlCountedProgramStore) Update(ctx context.Context, parentID, id string, resource sep2.DERProgram) error {
	return s.programs.Update(ctx, parentID, id, resource)
}

func (s *DERControlCountedProgramStore) Delete(ctx context.Context, parentID, id string) error {
	return s.programs.Delete(ctx, parentID, id)
}

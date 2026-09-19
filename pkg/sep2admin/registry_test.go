package sep2admin

import (
	"context"
	"errors"
	"fmt"
	"os"
	"reflect"
	"slices"
	"testing"
)

func noopView(_ context.Context) (Descriptor, error) {
	return Descriptor{Version: CurrentDescriptorVersion}, nil
}

// graftPanel returns a valid graft-band Panel with the given ID and rank,
// so a table test that varies one field can start from a value that would
// otherwise register cleanly.
func graftPanel(id string, rank int) Panel {
	return Panel{
		ID:                id,
		Label:             id,
		Placement:         ExtensionSlot(rank),
		DescriptorVersion: CurrentDescriptorVersion,
		View:              noopView,
	}
}

func TestRegistryExportedMethodSet(t *testing.T) {
	typ := reflect.TypeOf((*Registry)(nil)).Elem()
	got := make([]string, typ.NumMethod())
	for i := range got {
		got[i] = typ.Method(i).Name
	}
	// reflect sorts an interface's methods lexicographically by name.
	want := []string{"Freeze", "Register", "SetTheme"}
	if !slices.Equal(got, want) {
		t.Fatalf("Registry method set = %v, want %v: INV-1 (no Remove, Replace, Hide, or mutable order) holds only if the type never offers them", got, want)
	}
}

func TestRegisterRejectsZeroPlacement(t *testing.T) {
	r := NewRegistry()
	p := graftPanel("graft", 1)
	p.Placement = Placement{}

	err := r.Register(p)
	if !errors.Is(err, ErrZeroPlacement) {
		t.Fatalf("Register with a zero-value Placement: err = %v, want ErrZeroPlacement", err)
	}
}

func TestRegisterRejectsDuplicateIDAndKeepsTheFirst(t *testing.T) {
	r := &registry{panels: make(map[string]registeredPanel)}

	first := graftPanel("dup", 1)
	first.Label = "First"
	if err := r.Register(first); err != nil {
		t.Fatalf("Register(first): %v", err)
	}

	second := graftPanel("dup", 2)
	second.Label = "Second"
	if err := r.Register(second); !errors.Is(err, ErrDuplicateID) {
		t.Fatalf("Register(second, same ID): err = %v, want ErrDuplicateID", err)
	}

	core := Panel{ID: "core", Label: "Core", Placement: corePlacement(1), DescriptorVersion: CurrentDescriptorVersion, View: noopView}
	if err := r.Register(core); err != nil {
		t.Fatalf("Register(core): %v", err)
	}

	out, err := r.Freeze()
	if err != nil {
		t.Fatalf("Freeze: %v", err)
	}
	i := slices.IndexFunc(out, func(p Panel) bool { return p.ID == "dup" })
	if i == -1 {
		t.Fatal("frozen panels do not contain \"dup\"")
	}
	if out[i].Label != "First" {
		t.Fatalf("surviving panel Label = %q, want %q: the first registration must survive unchanged", out[i].Label, "First")
	}
}

func TestValidateID(t *testing.T) {
	longID := ""
	for range 64 {
		longID += "a"
	}

	cases := []struct {
		name string
		id   string
	}{
		{"empty", ""},
		{"leading digit", "1panel"},
		{"uppercase", "Panel"},
		{"path traversal segment", "../etc"},
		{"slash", "a/b"},
		{"over-length", longID},
		{"reserved overview", "overview"},
		{"reserved devices", "devices"},
		{"reserved fsas", "fsas"},
		{"reserved control", "control"},
		{"reserved certificates", "certificates"},
		{"reserved ui", "ui"},
		{"reserved api", "api"},
		{"reserved auth", "auth"},
		{"reserved login", "login"},
		{"reserved dashboard", "dashboard"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := NewRegistry()
			p := graftPanel(tc.id, 1)
			err := r.Register(p)
			if !errors.Is(err, ErrInvalidID) {
				t.Fatalf("Register(ID=%q): err = %v, want ErrInvalidID", tc.id, err)
			}
		})
	}
}

func TestValidateIDAcceptsAWellFormedSlug(t *testing.T) {
	r := NewRegistry()
	if err := r.Register(graftPanel("a-well-formed-slug", 1)); err != nil {
		t.Fatalf("Register with a well-formed slug: %v", err)
	}
}

// TestRegisterRejectsNilView pins P3: a nil View registered cleanly and
// then panicked on first invocation, the exact rendering surprise
// CurrentDescriptorVersion's own boot-failure rule forbids. Refusing it at
// Register makes a missing View a boot failure instead.
func TestRegisterRejectsNilView(t *testing.T) {
	r := NewRegistry()
	p := graftPanel("no-view", 1)
	p.View = nil

	if err := r.Register(p); !errors.Is(err, ErrNilView) {
		t.Fatalf("Register with a nil View: err = %v, want ErrNilView", err)
	}
}

func TestRegisterRejectsNonNilAssets(t *testing.T) {
	r := NewRegistry()
	p := graftPanel("has-assets", 1)
	p.Assets = os.DirFS(t.TempDir())

	if err := r.Register(p); !errors.Is(err, ErrAssetsNotImplemented) {
		t.Fatalf("Register with non-nil Assets: err = %v, want ErrAssetsNotImplemented", err)
	}
}

func TestRegisterRejectsUnsupportedDescriptorVersion(t *testing.T) {
	r := NewRegistry()
	p := graftPanel("bad-version", 1)
	p.DescriptorVersion = CurrentDescriptorVersion + 1

	if err := r.Register(p); !errors.Is(err, ErrUnsupportedDescriptorVersion) {
		t.Fatalf("Register with a mismatched DescriptorVersion: err = %v, want ErrUnsupportedDescriptorVersion", err)
	}
}

func TestFreezeRejectsRegistrationsWithNoCorePanel(t *testing.T) {
	r := NewRegistry()
	if err := r.Register(graftPanel("only-graft", 1)); err != nil {
		t.Fatalf("Register: %v", err)
	}

	if _, err := r.Freeze(); !errors.Is(err, ErrCorePanelsMissing) {
		t.Fatalf("Freeze with no core panel: err = %v, want ErrCorePanelsMissing", err)
	}
}

func TestFreezeAndAfterFreezeRefusals(t *testing.T) {
	r := &registry{panels: make(map[string]registeredPanel)}
	core := Panel{ID: "core", Label: "Core", Placement: corePlacement(1), DescriptorVersion: CurrentDescriptorVersion, View: noopView}
	if err := r.Register(core); err != nil {
		t.Fatalf("Register(core): %v", err)
	}

	panels, err := r.Freeze()
	if err != nil {
		t.Fatalf("Freeze: %v", err)
	}
	if len(panels) != 1 || panels[0].ID != "core" {
		t.Fatalf("Freeze() = %v, want a single core panel", panels)
	}

	if err := r.Register(graftPanel("late", 1)); !errors.Is(err, ErrRegistryFrozen) {
		t.Fatalf("Register after Freeze: err = %v, want ErrRegistryFrozen", err)
	}
	if err := r.SetTheme(Theme{}); !errors.Is(err, ErrRegistryFrozen) {
		t.Fatalf("SetTheme after Freeze: err = %v, want ErrRegistryFrozen", err)
	}
	if _, err := r.Freeze(); !errors.Is(err, ErrRegistryFrozen) {
		t.Fatalf("second Freeze: err = %v, want ErrRegistryFrozen", err)
	}
}

// TestFreezeOrderingIsStableAcrossRegistrationOrders is the proof for
// criterion 11: the sort key is (group, Rank, ID), and registration order
// (registeredPanel.seq) is never part of it. Registering the identical
// panel set in several different call orders must produce the identical
// frozen nav sequence.
func TestFreezeOrderingIsStableAcrossRegistrationOrders(t *testing.T) {
	build := func(order []string) []string {
		r := &registry{panels: make(map[string]registeredPanel)}
		byID := map[string]Panel{
			"core":  {ID: "core", Label: "Core", Placement: corePlacement(1), DescriptorVersion: CurrentDescriptorVersion, View: noopView},
			"zeta":  graftPanel("zeta", 1),
			"alpha": graftPanel("alpha", 1),
			"beta":  graftPanel("beta", 2),
		}
		for _, id := range order {
			if err := r.Register(byID[id]); err != nil {
				t.Fatalf("Register(%q): %v", id, err)
			}
		}
		out, err := r.Freeze()
		if err != nil {
			t.Fatalf("Freeze: %v", err)
		}
		ids := make([]string, len(out))
		for i, p := range out {
			ids[i] = p.ID
		}
		return ids
	}

	orders := [][]string{
		{"core", "alpha", "beta", "zeta"},
		{"zeta", "beta", "alpha", "core"},
		{"beta", "core", "zeta", "alpha"},
	}

	want := build(orders[0])
	for _, order := range orders[1:] {
		got := build(order)
		if !slices.Equal(got, want) {
			t.Fatalf("registration order %v produced nav sequence %v, want %v: registration order must not be a sort input", order, got, want)
		}
	}
}

func TestErrDisabledMatchesItself(t *testing.T) {
	if !errors.Is(fmt.Errorf("wrap: %w", ErrDisabled), ErrDisabled) {
		t.Fatal("errors.Is must match a wrapped ErrDisabled: this is the check a caller uses to skip starting a runner")
	}
}

func TestErrDisabledIsDistinctFromEveryConfigurationRefusal(t *testing.T) {
	refusals := []error{
		ErrZeroPlacement, ErrDuplicateID, ErrInvalidID, ErrRegistryFrozen,
		ErrCorePanelsMissing, ErrNilView, ErrAssetsNotImplemented, ErrUnsupportedDescriptorVersion,
	}
	for _, err := range refusals {
		if errors.Is(err, ErrDisabled) || errors.Is(ErrDisabled, err) {
			t.Fatalf("%v must not match ErrDisabled: a caller that treats ErrDisabled as \"skip starting a runner\" must never misread a genuine boot refusal as that", err)
		}
	}
}

package sep2admin

import (
	"errors"
	"net/http"
	"testing"
	"testing/fstest"
)

var noopPage = http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})

// pagePanel is graftPanel served by a Page instead of a View.
func pagePanel(id string, rank int) Panel {
	p := graftPanel(id, rank)
	p.View = nil
	p.Page = noopPage
	return p
}

func TestRegisterAcceptsAPagePanel(t *testing.T) {
	r := NewRegistry()
	if err := r.Register(pagePanel("tool", 1)); err != nil {
		t.Fatalf("Register(page panel): %v", err)
	}
	frozen, err := r.Freeze()
	if err != nil {
		t.Fatalf("Freeze: %v", err)
	}
	last := frozen[len(frozen)-1]
	if last.ID != "tool" || last.Page == nil || last.View != nil {
		t.Fatalf("frozen page panel = {ID:%q Page:%v View:%v}, want tool with its Page and no View", last.ID, last.Page != nil, last.View != nil)
	}
}

func TestRegisterRequiresExactlyOneOfViewAndPage(t *testing.T) {
	both := pagePanel("both", 1)
	both.View = noopView
	if err := NewRegistry().Register(both); !errors.Is(err, ErrViewAndPage) {
		t.Fatalf("Register with View and Page: err = %v, want ErrViewAndPage", err)
	}

	neither := pagePanel("neither", 1)
	neither.Page = nil
	if err := NewRegistry().Register(neither); !errors.Is(err, ErrNilView) {
		t.Fatalf("Register with neither View nor Page: err = %v, want ErrNilView", err)
	}
}

// Every refusal a View panel gets still fires when the panel carries a Page
// instead: the Page is an alternative to View, not a way around the checks.
func TestEveryRefusalStillFiresForAPagePanel(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Panel)
		want   error
	}{
		{"zero placement", func(p *Panel) { p.Placement = Placement{} }, ErrZeroPlacement},
		{"traversal id", func(p *Panel) { p.ID = "../x" }, ErrInvalidID},
		{"core tab id", func(p *Panel) { p.ID = "devices" }, ErrInvalidID},
		{"api-prefixed id", func(p *Panel) { p.ID = "apis" }, ErrInvalidID},
		{"reserved segment", func(p *Panel) { p.ID = "login" }, ErrInvalidID},
		{"the page mount segment", func(p *Panel) { p.ID = "apps" }, ErrInvalidID},
		{"assets", func(p *Panel) { p.Assets = fstest.MapFS{} }, ErrAssetsNotImplemented},
		{"half a picker", func(p *Panel) { p.Picker = &Picker{} }, ErrInvalidPicker},
		{"descriptor version", func(p *Panel) { p.DescriptorVersion = 1 }, ErrUnsupportedDescriptorVersion},
		{"invalid action", func(p *Panel) { p.Actions = []Action{{ID: "Bad ID"}} }, ErrInvalidAction},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := pagePanel("tool", 1)
			tc.mutate(&p)
			if err := NewRegistry().Register(p); !errors.Is(err, tc.want) {
				t.Fatalf("Register: err = %v, want %v", err, tc.want)
			}
		})
	}

	t.Run("duplicate id", func(t *testing.T) {
		r := NewRegistry()
		if err := r.Register(pagePanel("tool", 1)); err != nil {
			t.Fatalf("first Register: %v", err)
		}
		if err := r.Register(pagePanel("tool", 2)); !errors.Is(err, ErrDuplicateID) {
			t.Fatalf("second Register: err = %v, want ErrDuplicateID", err)
		}
	})
	t.Run("frozen", func(t *testing.T) {
		r := NewRegistry()
		if _, err := r.Freeze(); err != nil {
			t.Fatalf("Freeze: %v", err)
		}
		if err := r.Register(pagePanel("tool", 1)); !errors.Is(err, ErrRegistryFrozen) {
			t.Fatalf("Register after Freeze: err = %v, want ErrRegistryFrozen", err)
		}
	})
}

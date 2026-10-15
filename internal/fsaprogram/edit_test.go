package fsaprogram

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	coresingleton "github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/singleton"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

func u16(v uint16) *uint16 { return &v }

func versionOf(v *uint16) int {
	if v == nil {
		return -1
	}
	return int(*v)
}

func seedProgram(t *testing.T, version *uint16) *memory.DERProgramStore {
	t.Helper()
	s := memory.NewDERProgramStore()
	p := sep2.DERProgram{MRID: "P1", Description: "d", Primacy: 1, Version: version}
	if err := s.Create(context.Background(), "e1", "p1", p); err != nil {
		t.Fatal(err)
	}
	return s
}

func setPrimacy(v uint8) func(*sep2.DERProgram) error {
	return func(p *sep2.DERProgram) error { p.Primacy = v; return nil }
}

func TestEdit_IncrementsFromUnversioned(t *testing.T) {
	ctx := context.Background()
	s := seedProgram(t, nil)
	ref := Ref{DeviceID: "e1", ProgramID: "p1"}
	for want, primacy := range []uint8{5, 6} {
		if err := Edit(ctx, s, ref, setPrimacy(primacy)); err != nil {
			t.Fatal(err)
		}
		got, err := s.Get(ctx, "e1", "p1")
		if err != nil {
			t.Fatal(err)
		}
		if versionOf(got.Version) != want+1 || got.Primacy != primacy {
			t.Errorf("after edit %d: version=%d primacy=%d, want version %d primacy %d",
				want+1, versionOf(got.Version), got.Primacy, want+1, primacy)
		}
	}
}

func TestEdit_UnchangedWriteDoesNotIncrement(t *testing.T) {
	ctx := context.Background()
	s := seedProgram(t, u16(3))
	if err := Edit(ctx, s, Ref{DeviceID: "e1", ProgramID: "p1"}, setPrimacy(1)); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Get(ctx, "e1", "p1")
	if versionOf(got.Version) != 3 {
		t.Errorf("version = %d, want 3", versionOf(got.Version))
	}
	s2 := seedProgram(t, nil)
	if err := Edit(ctx, s2, Ref{DeviceID: "e1", ProgramID: "p1"}, setPrimacy(1)); err != nil {
		t.Fatal(err)
	}
	got, _ = s2.Get(ctx, "e1", "p1")
	if got.Version != nil {
		t.Errorf("unchanged unversioned program gained version %d", *got.Version)
	}
}

// A mutate that edits through a link pointer must still count as a change.
func TestEdit_ChangeThroughLinkPointerIncrements(t *testing.T) {
	ctx := context.Background()
	s := memory.NewDERProgramStore()
	p := sep2.DERProgram{DefaultDERControlLink: &sep2.Link{Href: "/a"}}
	if err := s.Create(ctx, "e1", "p1", p); err != nil {
		t.Fatal(err)
	}
	err := Edit(ctx, s, Ref{DeviceID: "e1", ProgramID: "p1"}, func(p *sep2.DERProgram) error {
		p.DefaultDERControlLink.Href = "/b"
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	got, _ := s.Get(ctx, "e1", "p1")
	if got.DefaultDERControlLink.Href != "/b" || versionOf(got.Version) != 1 {
		t.Errorf("href=%q version=%d, want /b and 1", got.DefaultDERControlLink.Href, versionOf(got.Version))
	}
}

func TestEdit_ExhaustedVersionIsRefusedAndNothingWritten(t *testing.T) {
	ctx := context.Background()
	s := seedProgram(t, u16(65535))
	err := Edit(ctx, s, Ref{DeviceID: "e1", ProgramID: "p1"}, setPrimacy(9))
	if !errors.Is(err, ErrVersionExhausted) {
		t.Fatalf("err = %v, want ErrVersionExhausted", err)
	}
	got, _ := s.Get(ctx, "e1", "p1")
	if got.Primacy != 1 || versionOf(got.Version) != 65535 {
		t.Errorf("program changed: primacy=%d version=%d", got.Primacy, versionOf(got.Version))
	}
	// An unchanged write at the ceiling is not an edit, so it is not refused.
	if err := Edit(ctx, s, Ref{DeviceID: "e1", ProgramID: "p1"}, setPrimacy(1)); err != nil {
		t.Errorf("unchanged write at 65535: %v", err)
	}
}

func TestEdit_MissingProgramAndMutateError(t *testing.T) {
	ctx := context.Background()
	s := seedProgram(t, nil)
	if err := Edit(ctx, s, Ref{DeviceID: "e1", ProgramID: "nope"}, setPrimacy(2)); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("missing program: err = %v, want ErrNotFound", err)
	}
	boom := errors.New("boom")
	err := Edit(ctx, s, Ref{DeviceID: "e1", ProgramID: "p1"}, func(p *sep2.DERProgram) error {
		p.Primacy = 77
		return boom
	})
	if !errors.Is(err, boom) {
		t.Errorf("mutate error: err = %v, want boom", err)
	}
	got, _ := s.Get(ctx, "e1", "p1")
	if got.Primacy != 1 || got.Version != nil {
		t.Errorf("failed mutate was written: primacy=%d version=%v", got.Primacy, got.Version)
	}
}

func TestEdit_ConcurrentEditsEachIncrement(t *testing.T) {
	ctx := context.Background()
	s := seedProgram(t, nil)
	const n = 20
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := Edit(ctx, s, Ref{DeviceID: "e1", ProgramID: "p1"}, setPrimacy(uint8(10+i))); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	got, _ := s.Get(ctx, "e1", "p1")
	if versionOf(got.Version) != n {
		t.Errorf("version = %d, want %d", versionOf(got.Version), n)
	}
}

type defaultFixture struct {
	programs *memory.DERProgramStore
	defaults store.ScopedStore[sep2.DefaultDERControl]
	ref      Ref
}

func seedDefault(t *testing.T, progVersion, defVersion *uint16) defaultFixture {
	t.Helper()
	ctx := context.Background()
	f := defaultFixture{
		programs: seedProgram(t, progVersion),
		defaults: memory.NewScopedStore[sep2.DefaultDERControl](),
		ref:      Ref{DeviceID: "e1", ProgramID: "p1"},
	}
	d := sep2.DefaultDERControl{MRID: "D1", Version: defVersion, SetGradW: u16(100)}
	if err := f.defaults.Create(ctx, "scope", coresingleton.SingletonKey, d); err != nil {
		t.Fatal(err)
	}
	return f
}

func TestEditDefault_IncrementsDefaultAndProgram(t *testing.T) {
	ctx := context.Background()
	f := seedDefault(t, u16(4), nil)
	err := EditDefault(ctx, f.programs, f.defaults, f.ref, "scope", func(d *sep2.DefaultDERControl) error {
		d.SetGradW = u16(200)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	d, _ := f.defaults.Get(ctx, "scope", coresingleton.SingletonKey)
	p, _ := f.programs.Get(ctx, "e1", "p1")
	if *d.SetGradW != 200 || versionOf(d.Version) != 1 {
		t.Errorf("default: setGradW=%d version=%d, want 200 and 1", *d.SetGradW, versionOf(d.Version))
	}
	if versionOf(p.Version) != 5 {
		t.Errorf("program version = %d, want 5", versionOf(p.Version))
	}
}

func TestEditDefault_UnchangedWritesNothing(t *testing.T) {
	ctx := context.Background()
	f := seedDefault(t, u16(4), u16(2))
	err := EditDefault(ctx, f.programs, f.defaults, f.ref, "scope", func(d *sep2.DefaultDERControl) error {
		d.SetGradW = u16(100)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	d, _ := f.defaults.Get(ctx, "scope", coresingleton.SingletonKey)
	p, _ := f.programs.Get(ctx, "e1", "p1")
	if versionOf(d.Version) != 2 || versionOf(p.Version) != 4 {
		t.Errorf("versions = default %d program %d, want 2 and 4", versionOf(d.Version), versionOf(p.Version))
	}
}

func TestEditDefault_ExhaustedEitherSideRefusesBoth(t *testing.T) {
	ctx := context.Background()
	for name, f := range map[string]defaultFixture{
		"default": seedDefault(t, u16(1), u16(65535)),
		"program": seedDefault(t, u16(65535), u16(1)),
	} {
		err := EditDefault(ctx, f.programs, f.defaults, f.ref, "scope", func(d *sep2.DefaultDERControl) error {
			d.SetGradW = u16(300)
			return nil
		})
		if !errors.Is(err, ErrVersionExhausted) {
			t.Errorf("%s at 65535: err = %v, want ErrVersionExhausted", name, err)
		}
		d, _ := f.defaults.Get(ctx, "scope", coresingleton.SingletonKey)
		if *d.SetGradW != 100 {
			t.Errorf("%s at 65535: default was written (setGradW=%d)", name, *d.SetGradW)
		}
	}
}

func TestNextVersion(t *testing.T) {
	for _, tc := range []struct {
		in   *uint16
		want int
		err  bool
	}{
		{nil, 1, false}, {u16(0), 1, false}, {u16(41), 42, false}, {u16(65535), 0, true},
	} {
		got, err := NextVersion(tc.in)
		if (err != nil) != tc.err || (!tc.err && versionOf(got) != tc.want) {
			t.Errorf("NextVersion(%v) = %v, %v", tc.in, got, err)
		}
	}
}

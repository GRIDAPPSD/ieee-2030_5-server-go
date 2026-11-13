package devicelog

import (
	"bytes"
	"context"
	"errors"
	"log"
	"strings"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

type failingResponses struct {
	*memory.ScopedStore[sep2.Response]
}

func (failingResponses) Create(context.Context, string, string, sep2.Response) error {
	return errors.New("boom")
}

func TestResponseStore_NotesByValueAndOnlyOnSuccess(t *testing.T) {
	t.Parallel()
	const up = "0123456789ABCDEF0123456789ABCDEF"
	st := func(v uint8) *uint8 { return &v }
	cases := []struct {
		name string
		rsp  sep2.Response
		want map[string]any
	}{
		{"lowercase subject and status", sep2.Response{Subject: "0123456789abcdef0123456789abcdef", Status: st(3)},
			map[string]any{"subject": up, "rspStatus": 3}},
		{"no status", sep2.Response{Subject: up}, map[string]any{"subject": up}},
		{"non-hex subject", sep2.Response{Subject: "not an mrid", Status: st(2)},
			map[string]any{"subjectInvalid": true, "rspStatus": 2}},
		{"short subject", sep2.Response{Subject: "ABC"}, map[string]any{"subjectInvalid": true}},
		{"other device", sep2.Response{EndDeviceLFDI: lfdiB},
			map[string]any{"target": lfdiB}},
		{"same device as caller", sep2.Response{EndDeviceLFDI: lfdiA}, map[string]any{}},
		{"empty", sep2.Response{}, map[string]any{}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			s := NewResponseStore(memory.NewScopedStore[sep2.Response](), fixedIdentity(lfdiA, true))
			ctx, note := WithNote(context.Background())
			if err := s.Create(ctx, "1", "r", c.rsp); err != nil {
				t.Fatal(err)
			}
			got := note.Fields()
			if len(got) != len(c.want) {
				t.Errorf("fields = %v, want %v", got, c.want)
			}
			for k, v := range c.want {
				if got[k] != v {
					t.Errorf("field %s = %v (%T), want %v", k, got[k], got[k], v)
				}
			}
		})
	}

	t.Run("failed create notes nothing", func(t *testing.T) {
		t.Parallel()
		s := NewResponseStore(failingResponses{memory.NewScopedStore[sep2.Response]()}, fixedIdentity(lfdiA, true))
		ctx, note := WithNote(context.Background())
		if err := s.Create(ctx, "1", "r", sep2.Response{Subject: up, Status: st(3)}); err == nil {
			t.Fatal("want the inner error")
		}
		if got := note.Fields(); len(got) != 0 {
			t.Errorf("fields after failed Create = %v, want none", got)
		}
	})

	t.Run("no identity and no note are safe", func(t *testing.T) {
		t.Parallel()
		inner := memory.NewScopedStore[sep2.Response]()
		if got := NewResponseStore(inner, nil); got != inner {
			t.Error("nil identity should return the inner store")
		}
		s := NewResponseStore(inner, fixedIdentity("", false))
		ctx, note := WithNote(context.Background())
		if err := s.Create(ctx, "1", "r", sep2.Response{EndDeviceLFDI: lfdiB}); err != nil {
			t.Fatal(err)
		}
		if _, has := note.Fields()["target"]; has {
			t.Error("target noted without a caller identity")
		}
		if err := s.Create(context.Background(), "1", "r2", sep2.Response{Subject: up}); err != nil {
			t.Fatal(err)
		}
	})
}

func TestResponseStore_DeleteNotesTheRemovedResponse(t *testing.T) {
	t.Parallel()
	const up = "0123456789ABCDEF0123456789ABCDEF"
	status := uint8(3)

	t.Run("notes the facts of the one removed", func(t *testing.T) {
		t.Parallel()
		inner := memory.NewScopedStore[sep2.Response]()
		s := NewResponseStore(inner, fixedIdentity(lfdiA, true))
		if err := inner.Create(context.Background(), "1", "r", sep2.Response{Subject: up, Status: &status, EndDeviceLFDI: lfdiB}); err != nil {
			t.Fatal(err)
		}
		ctx, note := WithNote(context.Background())
		if err := s.Delete(ctx, "1", "r"); err != nil {
			t.Fatal(err)
		}
		want := map[string]any{"subject": up, "rspStatus": 3, "target": lfdiB}
		got := note.Fields()
		if len(got) != len(want) {
			t.Errorf("fields = %v, want %v", got, want)
		}
		for k, v := range want {
			if got[k] != v {
				t.Errorf("field %s = %v, want %v", k, got[k], v)
			}
		}
		if _, err := inner.Get(context.Background(), "1", "r"); err == nil {
			t.Error("Response still stored after Delete")
		}
	})

	t.Run("a missing id notes nothing and returns the inner error", func(t *testing.T) {
		t.Parallel()
		s := NewResponseStore(memory.NewScopedStore[sep2.Response](), fixedIdentity(lfdiA, true))
		ctx, note := WithNote(context.Background())
		if err := s.Delete(ctx, "1", "nope"); err == nil {
			t.Fatal("want the inner not-found error")
		}
		if got := note.Fields(); len(got) != 0 {
			t.Errorf("fields after failed Delete = %v, want none", got)
		}
	})
}

type failingGet struct {
	*memory.ScopedStore[sep2.Response]
}

func (failingGet) Get(context.Context, string, string) (sep2.Response, error) {
	return sep2.Response{}, errors.New("read boom")
}

// A Response that cannot be read before its delete is still deleted, notes
// no facts, and the read failure is logged rather than lost. Not parallel: it
// swaps the process-wide log output.
func TestResponseStore_DeleteLogsAFailedRead(t *testing.T) {
	var buf bytes.Buffer
	prevOut, prevFlags := log.Writer(), log.Flags()
	log.SetFlags(0)
	log.SetOutput(&buf)
	t.Cleanup(func() {
		log.SetOutput(prevOut)
		log.SetFlags(prevFlags)
	})

	inner := memory.NewScopedStore[sep2.Response]()
	if err := inner.Create(context.Background(), "1", "r", sep2.Response{EndDeviceLFDI: lfdiB}); err != nil {
		t.Fatal(err)
	}
	s := NewResponseStore(failingGet{inner}, fixedIdentity(lfdiA, true))
	ctx, note := WithNote(context.Background())
	if err := s.Delete(ctx, "1", "r"); err != nil {
		t.Fatalf("Delete = %v, want nil", err)
	}
	if got := note.Fields(); len(got) != 0 {
		t.Errorf("fields after an unreadable Response = %v, want none", got)
	}
	if _, err := inner.Get(context.Background(), "1", "r"); err == nil {
		t.Error("Response still stored after Delete")
	}
	if out := buf.String(); !strings.Contains(out, "read boom") {
		t.Errorf("log = %q, want the read failure", out)
	}
}

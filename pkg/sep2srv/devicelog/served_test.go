package devicelog

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
)

// fakeControls answers Get and List from fixed values; every other method is
// the nil embedded interface and would panic, so the decorator is shown to
// call nothing else.
type fakeControls struct {
	store.ScopedStore[sep2.DERControl]
	items []sep2.DERControl
	err   error
}

func (f fakeControls) Get(context.Context, string, string) (sep2.DERControl, error) {
	if f.err != nil {
		return sep2.DERControl{}, f.err
	}
	return f.items[0], nil
}

func (f fakeControls) List(context.Context, string, store.ListOptions) (store.ListResult[sep2.DERControl], error) {
	if f.err != nil {
		return store.ListResult[sep2.DERControl]{}, f.err
	}
	return store.ListResult[sep2.DERControl]{All: uint32(len(f.items)), Results: uint32(len(f.items)), Items: f.items}, nil
}

func ctl(m string) sep2.DERControl {
	var c sep2.DERControl
	c.MRID = m
	return c
}

func hex32(i int) string { return fmt.Sprintf("%032x", i) }

func TestServedStoreNotesCappedMRIDs(t *testing.T) {
	t.Parallel()
	var items []sep2.DERControl
	for i := 0; i < 11; i++ {
		items = append(items, ctl(hex32(i+1)))
	}
	s := NewServedStore[sep2.DERControl](fakeControls{items: items}, func(c sep2.DERControl) string { return c.MRID })
	ctx, note := WithNote(context.Background())
	if _, err := s.List(ctx, "p", store.ListOptions{Unbounded: true}); err != nil {
		t.Fatal(err)
	}
	f := note.Fields()
	want := make([]string, MaxServedMRIDs)
	for i := range want {
		want[i] = fmt.Sprintf("%032X", i+1)
	}
	if !reflect.DeepEqual(f["mrids"], want) {
		t.Errorf("mrids = %v, want the first 8 uppercased: %v", f["mrids"], want)
	}
	if f["mridsMore"] != 3 {
		t.Errorf("mridsMore = %v, want 3", f["mridsMore"])
	}
}

func TestServedStoreNeverWritesMalformedMRIDAsText(t *testing.T) {
	t.Parallel()
	items := []sep2.DERControl{ctl("not-hex"), ctl(hex32(5)), ctl("")}
	s := NewServedStore[sep2.DERControl](fakeControls{items: items}, func(c sep2.DERControl) string { return c.MRID })
	ctx, note := WithNote(context.Background())
	if _, err := s.List(ctx, "p", store.ListOptions{Unbounded: true}); err != nil {
		t.Fatal(err)
	}
	f := note.Fields()
	if !reflect.DeepEqual(f["mrids"], []string{fmt.Sprintf("%032X", 5)}) {
		t.Errorf("mrids = %v, want only the well-formed one", f["mrids"])
	}
	if f["mridsMore"] != 2 {
		t.Errorf("mridsMore = %v, want 2 (the two malformed are still counted as served)", f["mridsMore"])
	}
}

func TestServedStoreGetNotesOneAndFailureNotesNothing(t *testing.T) {
	t.Parallel()
	mridOf := func(c sep2.DERControl) string { return c.MRID }
	ok := NewServedStore[sep2.DERControl](fakeControls{items: []sep2.DERControl{ctl(hex32(9))}}, mridOf)
	ctx, note := WithNote(context.Background())
	if _, err := ok.Get(ctx, "p", "id"); err != nil {
		t.Fatal(err)
	}
	if f := note.Fields(); !reflect.DeepEqual(f["mrids"], []string{fmt.Sprintf("%032X", 9)}) {
		t.Errorf("Get mrids = %v, want one", f["mrids"])
	} else if _, more := f["mridsMore"]; more {
		t.Errorf("Get set mridsMore %v, want absent", f["mridsMore"])
	}

	bad := NewServedStore[sep2.DERControl](fakeControls{err: store.ErrNotFound}, mridOf)
	ctx, note = WithNote(context.Background())
	if _, err := bad.Get(ctx, "p", "id"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("control: Get err = %v, want ErrNotFound", err)
	}
	if _, err := bad.List(ctx, "p", store.ListOptions{}); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("control: List err = %v, want ErrNotFound", err)
	}
	if f := note.Fields(); len(f) != 0 {
		t.Errorf("failed reads left note fields %v, want none", f)
	}
}

func TestServedStoreWithoutNoteIsPlain(t *testing.T) {
	t.Parallel()
	s := NewServedStore[sep2.DERControl](fakeControls{items: []sep2.DERControl{ctl(hex32(1))}}, func(c sep2.DERControl) string { return c.MRID })
	if _, err := s.Get(context.Background(), "p", "id"); err != nil {
		t.Fatalf("Get without a note: %v", err)
	}
}

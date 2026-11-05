package content_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/content"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
)

const fam = "messages"

func program(href, mrid, desc string) string {
	return `<MessagingProgram xmlns="urn:ieee:std:2030.5:ns" href="` + href + `"><mRID>` + mrid +
		`</mRID><description>` + desc + `</description><primacy>2</primacy></MessagingProgram>`
}

func requireProgram(p sep2.MessagingProgram) error {
	if p.Href == "" || p.MRID == "" {
		return errors.New("href and mRID are required")
	}
	return nil
}

func register(t *testing.T, dir string) (*content.Owner, store.ResourceStore[sep2.MessagingProgram]) {
	t.Helper()
	o := content.NewOwner(func(name string) string { return filepath.Join(dir, name+".json") })
	st, err := content.Register(o, content.Spec[sep2.MessagingProgram]{Name: fam, Validate: requireProgram})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	return o, st
}

func TestCreateReplaceDeleteStoreWhatWasSent(t *testing.T) {
	ctx := context.Background()
	o, st := register(t, t.TempDir())

	if err := o.Create(ctx, fam, "p1", []byte(program("/msg/1", "AA01", "first"))); err != nil {
		t.Fatalf("Create: %v", err)
	}
	got, err := st.Get(ctx, "p1")
	if err != nil {
		t.Fatalf("Get after Create: %v", err)
	}
	if got.Href != "/msg/1" || got.MRID != "AA01" || got.Description != "first" || got.Primacy != 2 {
		t.Errorf("stored = %+v, want href /msg/1, mRID AA01, description first, primacy 2", got)
	}

	if err := o.Create(ctx, fam, "p1", []byte(program("/msg/1", "AA01", "dup"))); !errors.Is(err, store.ErrAlreadyExists) {
		t.Errorf("second Create = %v, want ErrAlreadyExists", err)
	}
	if got, _ := st.Get(ctx, "p1"); got.Description != "first" {
		t.Errorf("description after a refused Create = %q, want first", got.Description)
	}

	if err := o.Replace(ctx, fam, "p1", []byte(program("/msg/1", "AA01", "second"))); err != nil {
		t.Fatalf("Replace: %v", err)
	}
	if got, _ := st.Get(ctx, "p1"); got.Description != "second" {
		t.Errorf("description after Replace = %q, want second", got.Description)
	}
	if err := o.Replace(ctx, fam, "absent", []byte(program("/msg/9", "AA09", "x"))); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("Replace of an absent id = %v, want ErrNotFound", err)
	}

	if err := o.Delete(ctx, fam, "p1"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := st.Get(ctx, "p1"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("Get after Delete = %v, want ErrNotFound", err)
	}
	if err := o.Delete(ctx, fam, "p1"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("second Delete = %v, want ErrNotFound", err)
	}
}

func TestRefusedBodiesLeaveTheStoreEmpty(t *testing.T) {
	ctx := context.Background()
	o, st := register(t, t.TempDir())
	ok := program("/msg/1", "AA01", "d")

	cases := []struct {
		name, id, body string
	}{
		{"not xml", "p1", "{not xml"},
		{"wrong root element", "p1", strings.ReplaceAll(ok, "MessagingProgram", "TextMessage")},
		{"missing required field", "p1", program("", "AA01", "d")},
		{"element the type would drop", "p1", strings.Replace(ok, "<primacy>", "<unknownPrice>1</unknownPrice><primacy>", 1)},
		{"id with a slash", "a/b", ok},
		{"empty id", "", ok},
		{"id too long", strings.Repeat("a", 65), ok},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := o.Create(ctx, fam, c.id, []byte(c.body))
			if !errors.Is(err, content.ErrInvalid) {
				t.Fatalf("Create = %v, want ErrInvalid", err)
			}
			if n, _ := st.Count(ctx); n != 0 {
				t.Errorf("store holds %d resources after a refused body, want 0", n)
			}
		})
	}

	if err := o.Create(ctx, "nope", "p1", []byte(ok)); !errors.Is(err, content.ErrUnknownFamily) {
		t.Errorf("Create in an unregistered family = %v, want ErrUnknownFamily", err)
	}
}

func TestContentSurvivesARestart(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	o, _ := register(t, dir)
	for id, mrid := range map[string]string{"p1": "AA01", "p2": "AA02", "p3": "AA03"} {
		if err := o.Create(ctx, fam, id, []byte(program("/msg/"+id, mrid, "d-"+id))); err != nil {
			t.Fatal(err)
		}
	}
	if err := o.Replace(ctx, fam, "p2", []byte(program("/msg/p2", "AA02", "changed"))); err != nil {
		t.Fatal(err)
	}
	if err := o.Delete(ctx, fam, "p3"); err != nil {
		t.Fatal(err)
	}

	_, st := register(t, dir)
	if n, _ := st.Count(ctx); n != 1+1 {
		t.Fatalf("count after restart = %d, want 2", n)
	}
	if got, err := st.Get(ctx, "p1"); err != nil || got.MRID != "AA01" || got.Description != "d-p1" {
		t.Errorf("p1 after restart = %+v, %v", got, err)
	}
	if got, err := st.Get(ctx, "p2"); err != nil || got.Description != "changed" {
		t.Errorf("p2 after restart = %+v, %v; want the replaced description", got, err)
	}
	if _, err := st.Get(ctx, "p3"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("p3 after restart = %v, want ErrNotFound (deleted before the restart)", err)
	}
}

func TestFailedPersistRollsMemoryBack(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	snap := filepath.Join(dir, "snap")
	if err := os.Mkdir(snap, 0o755); err != nil {
		t.Fatal(err)
	}
	o := content.NewOwner(func(name string) string { return filepath.Join(snap, name+".json") })
	st, err := content.Register(o, content.Spec[sep2.MessagingProgram]{Name: fam, Validate: requireProgram})
	if err != nil {
		t.Fatal(err)
	}
	if err := o.Create(ctx, fam, "keep", []byte(program("/msg/k", "AA01", "kept"))); err != nil {
		t.Fatal(err)
	}

	// With the snapshot directory gone every write fails to reach disk.
	if err := os.RemoveAll(snap); err != nil {
		t.Fatal(err)
	}

	err = o.Create(ctx, fam, "new", []byte(program("/msg/n", "AA02", "x")))
	if err == nil || errors.Is(err, store.ErrNotFound) || errors.Is(err, store.ErrAlreadyExists) || errors.Is(err, content.ErrInvalid) {
		t.Fatalf("Create with an unwritable snapshot = %v, want a plain failure", err)
	}
	if _, err := st.Get(ctx, "new"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("Get of the failed Create = %v, want ErrNotFound (rolled back)", err)
	}

	if err := o.Replace(ctx, fam, "keep", []byte(program("/msg/k", "AA01", "lost"))); err == nil {
		t.Fatal("Replace with an unwritable snapshot succeeded")
	}
	if got, _ := st.Get(ctx, "keep"); got.Description != "kept" {
		t.Errorf("description after a failed Replace = %q, want kept", got.Description)
	}

	if err := o.Delete(ctx, fam, "keep"); err == nil {
		t.Fatal("Delete with an unwritable snapshot succeeded")
	}
	if _, err := st.Get(ctx, "keep"); err != nil {
		t.Errorf("Get after a failed Delete = %v, want the record still served", err)
	}
}

func TestCorruptSnapshotRefusesRegistration(t *testing.T) {
	cases := map[string]string{
		"not json":                 "{garbage",
		"unknown version":          `{"version":99,"records":[]}`,
		"record not decodable":     `{"version":1,"records":[{"parent":"all","id":"p1","value":"text"}]}`,
		"record with empty id":     `{"version":1,"records":[{"parent":"all","id":"","value":{}}]}`,
		"records not an array":     `{"version":1,"records":{"a":1}}`,
		"truncated mid-record":     `{"version":1,"records":[{"parent":"all","id":"p1","val`,
		"empty parent and id":      `{"version":1,"records":[{"parent":"","id":"","value":{}}]}`,
		"value of the wrong shape": `{"version":1,"records":[{"parent":"all","id":"p1","value":[1,2]}]}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "content-"+fam+".json"), []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			o := content.NewOwner(func(n string) string { return filepath.Join(dir, n+".json") })
			if _, err := content.Register(o, content.Spec[sep2.MessagingProgram]{Name: fam}); err == nil {
				t.Fatal("Register over a corrupt snapshot succeeded; boot would serve the family with its content missing")
			}
			if o.Has(fam) {
				t.Error("a family whose snapshot did not load is registered")
			}
		})
	}
}

func TestRegisterRefusesBadNamesAndDuplicates(t *testing.T) {
	o := content.NewOwner(nil)
	spec := content.Spec[sep2.MessagingProgram]{Name: fam}
	if _, err := content.Register(o, spec); err != nil {
		t.Fatal(err)
	}
	if _, err := content.Register(o, spec); err == nil {
		t.Error("registering a family twice succeeded")
	}
	for _, name := range []string{"", "Upper", "a/b", "../x", "1abc", strings.Repeat("a", 41)} {
		if _, err := content.Register(o, content.Spec[sep2.MessagingProgram]{Name: name}); err == nil {
			t.Errorf("Register accepted the family name %q", name)
		}
	}
}

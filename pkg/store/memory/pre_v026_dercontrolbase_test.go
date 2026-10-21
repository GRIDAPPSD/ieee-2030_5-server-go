package memory_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

// currentBase carries every DERControlBase mode core v0.26.0 changed, plus
// one it did not, so a reload can be checked value by value.
func currentBase() sep2.DERControlBase {
	maxLim := sep2.PerCent(5000)
	return sep2.DERControlBase{
		OpModVoltVar:   &sep2.DERCurveLink{Href: "/dc/3"},
		OpModFixedVar:  &sep2.FixedVar{RefType: 2, Value: 5000},
		OpModFreqDroop: &sep2.FreqDroopType{DBOF: 1, DBUF: 2, KOF: 3, KUF: 4, OpenLoopTms: 5},
		OpModMaxLimW:   &maxLim,
	}
}

// Each fragment pair is a mode as v0.26.0 writes it and as v0.25.0 wrote
// it, so an old-shape snapshot is the server's own file with one mode put
// back in the old form.
var preV026Shapes = []struct {
	name, current, old, field string
}{
	{"curve as a number", `"OpModVoltVar":{"Href":"/dc/3"}`, `"OpModVoltVar":3`, "OpModVoltVar"},
	{"fixed var as ReactivePower", `"OpModFixedVar":{"RefType":2,"Value":5000}`, `"OpModFixedVar":{"Multiplier":0,"Value":500}`, "OpModFixedVar"},
	{"freq droop as a number", `"OpModFreqDroop":{"DBOF":1,"DBUF":2,"KOF":3,"KUF":4,"OpenLoopTms":5}`, `"OpModFreqDroop":55`, "OpModFreqDroop"},
}

type baseStore struct {
	name string
	// write stores two records holding base, the second with id "b".
	write func(t *testing.T, path string, base sep2.DERControlBase)
	// reload reopens path and returns each stored base in write order.
	reload func(path string) ([]sep2.DERControlBase, error)
	// field is the path the refusal names for the second record's mode.
	field string
	// record is how the refusal names the second record.
	record string
}

func baseStores() []baseStore {
	ctx := context.Background()
	return []baseStore{
		{
			name: "DERControl",
			write: func(t *testing.T, path string, base sep2.DERControlBase) {
				s, err := memory.NewDERControlStoreWithPersistence(path)
				if err != nil {
					t.Fatal(err)
				}
				for _, id := range []string{"a", "b"} {
					c := sep2.DERControl{DERControlBase: &base}
					c.MRID = "MRID-" + id
					if err := s.Create(ctx, "e1/f1/p1", id, c); err != nil {
						t.Fatal(err)
					}
				}
			},
			reload: func(path string) ([]sep2.DERControlBase, error) {
				s, err := memory.NewDERControlStoreWithPersistence(path)
				if err != nil {
					return nil, err
				}
				var out []sep2.DERControlBase
				for _, id := range []string{"a", "b"} {
					c, err := s.Get(ctx, "e1/f1/p1", id)
					if err != nil {
						return nil, err
					}
					out = append(out, *c.DERControlBase)
				}
				return out, nil
			},
			field:  "control.DERControlBase.",
			record: "decode record 1 (e1/f1/p1/b)",
		},
		{
			name: "DefaultDERControl",
			write: func(t *testing.T, path string, base sep2.DERControlBase) {
				s, err := memory.NewPersistentScopedStore[sep2.DefaultDERControl](path, "DefaultDERControl")
				if err != nil {
					t.Fatal(err)
				}
				for _, parent := range []string{"e1/f1/a", "e1/f1/b"} {
					if err := s.Create(ctx, parent, "dderc", sep2.DefaultDERControl{DERControlBase: &base}); err != nil {
						t.Fatal(err)
					}
				}
			},
			reload: func(path string) ([]sep2.DERControlBase, error) {
				s, err := memory.NewPersistentScopedStore[sep2.DefaultDERControl](path, "DefaultDERControl")
				if err != nil {
					return nil, err
				}
				var out []sep2.DERControlBase
				for _, parent := range []string{"e1/f1/a", "e1/f1/b"} {
					d, err := s.Get(ctx, parent, "dderc")
					if err != nil {
						return nil, err
					}
					out = append(out, *d.DERControlBase)
				}
				return out, nil
			},
			field:  "value.DERControlBase.",
			record: "decode record 1 (e1/f1/b/dderc)",
		},
		{
			name: "AdminFSA template and control",
			write: func(t *testing.T, path string, base sep2.DERControlBase) {
				s, err := memory.NewAdminFSAStoreWithPersistence(path)
				if err != nil {
					t.Fatal(err)
				}
				for _, id := range []string{"a", "b"} {
					if err := s.Create(ctx, id, sep2.FunctionSetAssignments{MRID: "FSA-" + id}); err != nil {
						t.Fatal(err)
					}
					if err := s.CreateTemplate(ctx, id, memory.ProgramTemplate{ID: "t", MRID: "T-" + id, DefaultControlBase: base}); err != nil {
						t.Fatal(err)
					}
					if err := s.CreateFSAControl(ctx, id, memory.FSAControl{TemplateID: "t", ID: "c", MRID: "C-" + id, Base: base}); err != nil {
						t.Fatal(err)
					}
				}
			},
			reload: func(path string) ([]sep2.DERControlBase, error) {
				s, err := memory.NewAdminFSAStoreWithPersistence(path)
				if err != nil {
					return nil, err
				}
				var out []sep2.DERControlBase
				for _, id := range []string{"a", "b"} {
					tmpl, err := s.Template(ctx, id, "t")
					if err != nil {
						return nil, err
					}
					c, err := s.FSAControl(ctx, id, "t", "c")
					if err != nil {
						return nil, err
					}
					out = append(out, tmpl.DefaultControlBase, c.Base)
				}
				return out, nil
			},
			// The FSA record's controls sort before its templates.
			field:  "controls[0].derControlBase.",
			record: "decode record 1 (b)",
		},
	}
}

// TestCurrentDERControlBaseReloads is the control for the refusal test: the
// file the server writes reloads with every changed mode's value intact.
func TestCurrentDERControlBaseReloads(t *testing.T) {
	t.Parallel()
	for _, st := range baseStores() {
		t.Run(st.name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), "snapshot.json")
			st.write(t, path, currentBase())
			got, err := st.reload(path)
			if err != nil {
				t.Fatalf("reload: %v", err)
			}
			for i, b := range got {
				if !reflect.DeepEqual(b, currentBase()) {
					t.Errorf("base %d = %+v, want %+v", i, b, currentBase())
				}
			}
		})
	}
}

// TestPreV026DERControlBaseRefused pins the stored-data decision for core
// v0.26.0: a snapshot holding a DERControlBase mode in the v0.25.0 shape
// fails the load, naming the file, the record and the mode, and the file
// is left as it was. Nothing is dropped, zeroed or half loaded.
func TestPreV026DERControlBaseRefused(t *testing.T) {
	t.Parallel()
	for _, st := range baseStores() {
		for _, shape := range preV026Shapes {
			t.Run(st.name+"/"+shape.name, func(t *testing.T) {
				t.Parallel()
				path := filepath.Join(t.TempDir(), "snapshot.json")
				st.write(t, path, currentBase())
				raw, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				// Only the last occurrence, so the first record stays
				// readable and the refusal must name the second.
				at := bytes.LastIndex(raw, []byte(shape.current))
				if at < 0 {
					t.Fatalf("snapshot does not hold %s:\n%s", shape.current, raw)
				}
				old := append(append(append([]byte{}, raw[:at]...), shape.old...), raw[at+len(shape.current):]...)
				if err := os.WriteFile(path, old, 0o600); err != nil {
					t.Fatal(err)
				}

				got, err := st.reload(path)
				if err == nil {
					t.Fatalf("old-shape snapshot loaded: %+v", got)
				}
				for _, want := range []string{path, st.record, st.field + shape.field, "before core v0.26.0"} {
					if !strings.Contains(err.Error(), want) {
						t.Errorf("error %q does not name %q", err, want)
					}
				}
				after, readErr := os.ReadFile(path)
				if readErr != nil || !bytes.Equal(after, old) {
					t.Errorf("refused load changed the snapshot (read error %v)", readErr)
				}
			})
		}
	}
}

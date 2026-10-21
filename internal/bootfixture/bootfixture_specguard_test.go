package bootfixture

import (
	"reflect"
	"slices"
	"strings"
	"testing"
)

func yamlKey(f reflect.StructField) string {
	key, _, _ := strings.Cut(f.Tag.Get("yaml"), ",")
	return key
}

// TestSpecStringFieldsAreRead fails when DERControlBaseSpec gains a *string
// curve field that BuildDERControlBase does not read, which would load the
// fixture value and silently drop it.
func TestSpecStringFieldsAreRead(t *testing.T) {
	t.Parallel()

	typ := reflect.TypeFor[DERControlBaseSpec]()
	checked := 0
	for i := range typ.NumField() {
		f := typ.Field(i)
		if f.Type != reflect.TypeFor[*string]() {
			continue
		}
		checked++
		var spec DERControlBaseSpec
		id := "c1"
		reflect.ValueOf(&spec).Elem().Field(i).Set(reflect.ValueOf(&id))
		base, err := BuildDERControlBase(spec)
		if err != nil {
			t.Errorf("%s: BuildDERControlBase: %v", yamlKey(f), err)
			continue
		}
		if reflect.DeepEqual(base, reflect.Zero(reflect.TypeOf(base)).Interface()) {
			t.Errorf("%s is set on the spec but BuildDERControlBase ignores it", yamlKey(f))
		}
	}
	if checked == 0 {
		t.Fatal("no *string field found on DERControlBaseSpec: the guard checked nothing")
	}
}

// TestSpecKeysCoverSpecFields fails when a field the fixture spec supports
// has no specKeys entry, since its refusals would lose their key prefix.
func TestSpecKeysCoverSpecFields(t *testing.T) {
	t.Parallel()

	keys := make([]string, 0, len(specKeys))
	for _, k := range specKeys {
		keys = append(keys, k)
	}
	typ := reflect.TypeFor[DERControlBaseSpec]()
	for i := range typ.NumField() {
		if k := yamlKey(typ.Field(i)); !slices.Contains(keys, k) {
			t.Errorf("spec field %s has no specKeys entry", k)
		}
	}
	for name, k := range specKeys {
		if k == "" {
			t.Errorf("specKeys[%s] is empty", name)
		}
	}
}

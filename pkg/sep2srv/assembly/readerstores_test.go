package assembly_test

import (
	"fmt"
	"reflect"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/assembly"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

// allowedReaderMethods names every method a reader interface on
// ReaderStores may declare. This is an ALLOWLIST, not a denylist of write
// names someone predicted: the property under test is "no method that
// mutates is reachable through a chain of INTERFACE-typed return values",
// so an unrecognized method name fails the same way a known write method
// would. A denylist only refuses the writes someone thought to name. The
// recursion stops at the first non-interface return type (see
// assertReaderOnly): a field whose Get returns a concrete write-capable
// type is outside this check's reach.
var allowedReaderMethods = map[string]bool{
	"Get": true, "List": true, "Count": true,
	"HasParent": true, "Parents": true,
	"GetBySFDI": true, "GetByLFDI": true,
	"ManagerOf": true, "ManagedBy": true,
}

// errorType is the built-in error interface: it is the return type of
// every method here, and its own Error() string method carries no store
// access, so it is exempt from the allowlist and from recursion rather
// than requiring "Error" as an allowed method name.
var errorType = reflect.TypeOf((*error)(nil)).Elem()

// assertReaderOnly fails t for any method on typ not in
// allowedReaderMethods, and recurses into every method's INTERFACE-typed
// return types. Return types count: a field whose otherwise-allowed method
// hands back a write-capable interface (a method returning a
// store.ResourceStore, say) is the same defect as a field carrying a write
// method directly, one call further away. visited stops the recursion
// revisiting a type already checked, guarding against a self-referential
// return type.
//
// The recursion stops at typ.Kind() != reflect.Interface: a method
// returning a concrete struct, pointer, slice or scalar is not followed
// into its own fields or element type. Nothing on ReaderStores trips this
// today, because every reader method returns a sep2 value, a scalar, a
// slice, or an interface already covered by this allowlist.
func assertReaderOnly(t *testing.T, typ reflect.Type, path string, visited map[reflect.Type]bool) {
	t.Helper()
	if typ.Kind() != reflect.Interface || typ == errorType || visited[typ] {
		return
	}
	visited[typ] = true

	for m := 0; m < typ.NumMethod(); m++ {
		method := typ.Method(m)
		if !allowedReaderMethods[method.Name] {
			t.Errorf("%s declares %s, not on the reader allowlist: a write or unexpected method is reachable", path, method.Name)
			continue
		}
		for r := 0; r < method.Type.NumOut(); r++ {
			out := method.Type.Out(r)
			assertReaderOnly(t, out, fmt.Sprintf("%s.%s()'s return value (%s)", path, method.Name, out), visited)
		}
	}
}

// TestReaderStoresFieldsAreInterfacesWithOnlyAllowedReaderMethods is the
// exported symbol set assertion criterion 6 asks for: every field is an
// interface (so a caller cannot reach a concrete type's wider method set
// without an explicit, visible type assertion), every method on it is on
// allowedReaderMethods, and no method's return type reaches a mutating
// method either.
func TestReaderStoresFieldsAreInterfacesWithOnlyAllowedReaderMethods(t *testing.T) {
	t.Parallel()

	typ := reflect.TypeOf(assembly.ReaderStores{})
	for i := 0; i < typ.NumField(); i++ {
		f := typ.Field(i)
		if f.Type.Kind() != reflect.Interface {
			t.Errorf("ReaderStores.%s is a %s, not an interface: it can carry exported methods no reader interface declares", f.Name, f.Type.Kind())
			continue
		}
		assertReaderOnly(t, f.Type, "ReaderStores."+f.Name, map[reflect.Type]bool{})
	}
}

// TestNewReaderStoresOmitsAdminAndSubscriptionPlanes pins the exclusion
// ReaderStores' own doc comment states: AdminFSAs and Subscriptions are not
// mirrored, so their exported SnapshotForTesting/RestoreForTesting/
// AttachProgram/etc. methods are not reachable through the read handle at
// all, concrete type included. This is a struct literal, not a method
// lookup: if a future field addition ever assigns one of those concrete
// types to ReaderStores, this fails to compile before it fails to test.
func TestNewReaderStoresOmitsAdminAndSubscriptionPlanes(t *testing.T) {
	t.Parallel()

	full := &assembly.Stores{
		EndDevices:        memory.NewEndDeviceStore(),
		EndDeviceManagers: memory.NewEndDeviceManagementStore(),
		AdminFSAs:         memory.NewAdminFSAStore(),
		Subscriptions:     memory.NewSubscriptionStore(),
	}
	reader := assembly.NewReaderStores(full)

	rv := reflect.ValueOf(*reader)
	rt := rv.Type()
	for i := 0; i < rt.NumField(); i++ {
		name := rt.Field(i).Name
		if name == "AdminFSAs" || name == "Subscriptions" {
			t.Errorf("ReaderStores unexpectedly carries a %s field; its own doc comment says this plane is not mirrored", name)
		}
	}

	// Establish the assertion has teeth: the concrete types really do carry
	// the forbidden methods, on the write handle, so their absence above is
	// a narrowing and not an accident of an already-empty method set.
	adminFSAsType := reflect.TypeOf(full.AdminFSAs)
	if _, ok := adminFSAsType.MethodByName("AttachProgram"); !ok {
		t.Fatal("control failed: *memory.AdminFSAStore no longer has AttachProgram; the exclusion this test proves has nothing to prove")
	}
	subsType := reflect.TypeOf(full.Subscriptions)
	if _, ok := subsType.MethodByName("RestoreForTesting"); !ok {
		t.Fatal("control failed: *memory.SubscriptionStore no longer has RestoreForTesting; the exclusion this test proves has nothing to prove")
	}
}

// readerStoresExcludedFields names every Stores field with no counterpart
// on ReaderStores, matching ReaderStores' own doc comment. A field named
// here must NOT appear on ReaderStores; every other Stores field MUST.
var readerStoresExcludedFields = map[string]string{
	"EndDeviceIndexes":   "a URL addressing allocator, not resource data",
	"RegistrationPolicy": "provisioning config, not a store",
	"AdminFSAs":          "a bespoke admin plane with no reader/writer split",
	"Subscriptions":      "a bespoke notification plane with no reader/writer split",
}

// TestNewReaderStoresWiresEveryMirroredField fails when a field is added to
// Stores and NewReaderStores does not mirror it onto ReaderStores (or does
// not carry a reason in readerStoresExcludedFields), and when a mirrored
// field exists on ReaderStores but NewReaderStores leaves it unset. It is
// the read-side counterpart to TestTestStoresWiresEveryStoresField
// (stores_wiring_test.go), which only covers the write side.
func TestNewReaderStoresWiresEveryMirroredField(t *testing.T) {
	t.Parallel()

	full := testStores()
	reader := assembly.NewReaderStores(full)

	writeType := reflect.TypeOf(*full)
	readVal := reflect.ValueOf(*reader)
	readType := readVal.Type()

	onRead := map[string]bool{}
	for i := 0; i < readType.NumField(); i++ {
		onRead[readType.Field(i).Name] = true
	}

	for i := 0; i < writeType.NumField(); i++ {
		name := writeType.Field(i).Name
		if reason, excluded := readerStoresExcludedFields[name]; excluded {
			if onRead[name] {
				t.Errorf("ReaderStores carries a %s field, which readerStoresExcludedFields says is deliberately left out (%s)", name, reason)
			}
			continue
		}
		field := readVal.FieldByName(name)
		if !field.IsValid() {
			t.Errorf("Stores.%s has no counterpart on ReaderStores: mirror it in NewReaderStores, or add it to readerStoresExcludedFields with a reason", name)
			continue
		}
		if field.IsZero() {
			t.Errorf("ReaderStores.%s is nil after NewReaderStores: the field exists but NewReaderStores does not set it", name)
		}
	}

	for name := range onRead {
		if _, onWrite := writeType.FieldByName(name); !onWrite {
			t.Errorf("ReaderStores.%s has no counterpart on Stores", name)
		}
	}

	for name := range readerStoresExcludedFields {
		if _, onWrite := writeType.FieldByName(name); !onWrite {
			t.Errorf("readerStoresExcludedFields names Stores.%s, which does not exist", name)
		}
	}
}

// writeMutatingMethods names every mutating method any Stores field's
// interface declares, across every family (Create/Update/Delete for
// [store.ResourceStore] and [store.ScopedStore], Assign/Unassign for
// [store.EndDeviceManagementStore]).
var writeMutatingMethods = []string{"Create", "Update", "Delete", "Assign", "Unassign"}

// TestNewReaderStoresRefusesWriteMethodsOnEveryField is the DYNAMIC
// counterpart to TestReaderStoresFieldsAreInterfacesWithOnlyAllowedReaderMethods.
// That test reads ReaderStores' struct definition: the field's DECLARED
// type, which stays a reader interface whether or not NewReaderStores
// actually wraps the value it puts there. This test reads the VALUE
// NewReaderStores returns: a caller holding it can attempt a type
// assertion to the write interface, or find a mutating method by
// reflection, and both must fail on every field, not on the declared type.
//
// Stripping every AsReader/AsScopedReader/AsEndDeviceReader/
// AsEndDeviceManagementReader wrap back to plain assignment leaves the
// struct definition, and so the first test, unchanged; this one goes red,
// because the dynamic value is now the write-capable store itself.
func TestNewReaderStoresRefusesWriteMethodsOnEveryField(t *testing.T) {
	t.Parallel()

	full := testStores()
	reader := assembly.NewReaderStores(full)

	writeVal := reflect.ValueOf(*full)
	writeType := writeVal.Type()
	readVal := reflect.ValueOf(*reader)
	readType := readVal.Type()

	checked := 0
	for i := 0; i < readType.NumField(); i++ {
		name := readType.Field(i).Name
		writeField, ok := writeType.FieldByName(name)
		if !ok {
			t.Errorf("ReaderStores.%s has no Stores counterpart", name)
			continue
		}
		writeFieldVal := writeVal.FieldByName(name)
		if !writeFieldVal.IsValid() || writeFieldVal.IsZero() {
			t.Errorf("Stores.%s is nil in testStores(): this field cannot serve as a control for its reader counterpart", name)
			continue
		}

		// Control: the write side must both implement its own declared
		// interface (trivially true) and expose at least one mutating
		// method by reflection, or a field with no writer at all could pass
		// the assertion below by accident.
		writeDyn := reflect.ValueOf(writeFieldVal.Interface())
		if !writeDyn.Type().Implements(writeField.Type) {
			t.Fatalf("control failed: Stores.%s's dynamic type %s does not implement %s", name, writeDyn.Type(), writeField.Type)
		}
		foundWrite := ""
		for _, m := range writeMutatingMethods {
			if writeDyn.MethodByName(m).IsValid() {
				foundWrite = m
				break
			}
		}
		if foundWrite == "" {
			t.Fatalf("control failed: Stores.%s (dynamic type %s) exposes none of %v; it cannot prove this check has teeth", name, writeDyn.Type(), writeMutatingMethods)
		}
		checked++

		// Assertion: the DYNAMIC value behind ReaderStores.<name> admits
		// neither escape.
		readDyn := reflect.ValueOf(readVal.Field(i).Interface())
		if readDyn.Type().Implements(writeField.Type) {
			t.Errorf("ReaderStores.%s's dynamic type %s implements %s: a type assertion to the write interface would succeed", name, readDyn.Type(), writeField.Type)
		}
		for _, m := range writeMutatingMethods {
			if readDyn.MethodByName(m).IsValid() {
				t.Errorf("ReaderStores.%s (dynamic type %s) exposes %s by reflection: the wrapping was dropped or bypassed", name, readDyn.Type(), m)
			}
		}
	}
	if checked != readType.NumField() {
		t.Fatalf("checked %d of %d ReaderStores fields; every field must go through the control and the assertion", checked, readType.NumField())
	}
}


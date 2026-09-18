package assembly_test

import (
	"reflect"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/assembly"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

// forbiddenOnReader names methods a write, a test-mutation hook, or an
// admin-plane action would carry. None may be reachable through any field
// type on ReaderStores: criterion 6 of issue #343, "no ACL internals,
// dashboard, login surface or test-mutation hooks are exported. Assert by
// inspecting the exported symbol set."
var forbiddenOnReader = map[string]bool{
	"Create": true, "Update": true, "Delete": true,
	"Assign": true, "Unassign": true,
	"Remove": true, "Replace": true, "Hide": true,
	"SnapshotForTesting": true, "RestoreForTesting": true, "LoadFromFile": true,
	"AttachProgram": true, "DetachProgram": true,
	"AssignDevice": true, "UnassignDevice": true,
}

// TestReaderStoresFieldsAreInterfacesWithNoWriteMethod is the exported
// symbol set assertion criterion 6 asks for: every field is an interface
// (so a caller cannot reach a concrete type's wider method set without an
// explicit, visible type assertion), and none of the interface's own
// methods is one of forbiddenOnReader.
func TestReaderStoresFieldsAreInterfacesWithNoWriteMethod(t *testing.T) {
	t.Parallel()

	typ := reflect.TypeOf(assembly.ReaderStores{})
	for i := 0; i < typ.NumField(); i++ {
		f := typ.Field(i)
		if f.Type.Kind() != reflect.Interface {
			t.Errorf("ReaderStores.%s is a %s, not an interface: it can carry exported methods no reader interface declares", f.Name, f.Type.Kind())
			continue
		}
		for m := 0; m < f.Type.NumMethod(); m++ {
			name := f.Type.Method(m).Name
			if forbiddenOnReader[name] {
				t.Errorf("ReaderStores.%s exposes %s, a write or test-mutation method", f.Name, name)
			}
		}
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

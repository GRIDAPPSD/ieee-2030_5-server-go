package memory_test

import (
	"reflect"
	"slices"
	"sort"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

// managementReaders and managementMutators pin
// EndDeviceManagementStore's exported method set, name and signature,
// split by whether the method mutates managerOf/managedBy: a tripwire
// on that set, not a proof of routing. It catches a method added or
// removed regardless of file, lvalue shape, or local alias, but says
// nothing about whether a mutator calls mutate (enddevicemanagement.go).
// A new mutator needs its own subtest in
// TestManagementPersistence_WriteFailureLeavesMemoryAndDiskUnchanged
// (enddevicemanagement_persistence_test.go); adding it here alone is
// not enough.
var managementReaders = []string{
	"ManagedBy func(*memory.EndDeviceManagementStore, context.Context, string) ([]string, error)",
	"ManagerOf func(*memory.EndDeviceManagementStore, context.Context, string) (string, error)",
}

var managementMutators = []string{
	"Assign func(*memory.EndDeviceManagementStore, context.Context, string, string) error",
	"RekeyManaged func(*memory.EndDeviceManagementStore, context.Context, string, string) error",
	"RekeyManager func(*memory.EndDeviceManagementStore, context.Context, string, string) error",
	"Unassign func(*memory.EndDeviceManagementStore, context.Context, string) error",
}

// TestEndDeviceManagementStoreHasExactMethodSet fails when a method is
// added, removed, or its signature changes. A newly added method must be
// classified above as a reader or a mutator; if it mutates, route it
// through mutate and give it a subtest in
// TestManagementPersistence_WriteFailureLeavesMemoryAndDiskUnchanged.
func TestEndDeviceManagementStoreHasExactMethodSet(t *testing.T) {
	typ := reflect.TypeOf((*memory.EndDeviceManagementStore)(nil))
	got := make([]string, typ.NumMethod())
	for i := range got {
		m := typ.Method(i)
		got[i] = m.Name + " " + m.Type.String()
	}
	sort.Strings(got)

	want := slices.Concat(managementReaders, managementMutators)
	sort.Strings(want)

	if !reflect.DeepEqual(got, want) {
		t.Errorf("EndDeviceManagementStore method set changed.\ngot:  %#v\nwant: %#v", got, want)
	}
}

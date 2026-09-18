package store_test

import (
	"reflect"
	"sort"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
)

// methodSignatures returns "Name func(...)" for every method on typ,
// sorted so two calls compare deterministically regardless of
// reflect's own method ordering.
func methodSignatures(typ reflect.Type) []string {
	out := make([]string, typ.NumMethod())
	for i := range out {
		m := typ.Method(i)
		out[i] = m.Name + " " + m.Type.String()
	}
	sort.Strings(out)
	return out
}

// TestDecomposedEndDeviceInterfacesHaveExactMethodSets pins the four
// interfaces PR #604 (issue #343) split from the flat EndDeviceStore and
// EndDeviceManagementStore contract, name AND signature, both ways.
//
// contract.go's compile-time assertions (`_ store.EndDeviceReader =
// (*EndDeviceStore)(nil)`) only fail when a method is ADDED to the
// interface and an implementation does not have it; an implementation
// that already has MORE methods than the interface declares still
// compiles if a method is quietly REMOVED from the interface, so nothing
// there notices a narrowing of a published contract. reflect.DeepEqual on
// the sorted signature list catches both directions.
func TestDecomposedEndDeviceInterfacesHaveExactMethodSets(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		typ  reflect.Type
		want []string
	}{
		{
			name: "EndDeviceReader",
			typ:  reflect.TypeOf((*store.EndDeviceReader)(nil)).Elem(),
			want: []string{
				"Count func(context.Context) (uint32, error)",
				"Get func(context.Context, string) (sep2.EndDevice, error)",
				"GetByLFDI func(context.Context, string) (sep2.EndDevice, error)",
				"GetBySFDI func(context.Context, string) (sep2.EndDevice, error)",
				"List func(context.Context, store.ListOptions) (store.ListResult[github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2.EndDevice], error)",
			},
		},
		{
			name: "EndDeviceStore",
			typ:  reflect.TypeOf((*store.EndDeviceStore)(nil)).Elem(),
			want: []string{
				"Count func(context.Context) (uint32, error)",
				"Create func(context.Context, string, sep2.EndDevice) error",
				"Delete func(context.Context, string) error",
				"Get func(context.Context, string) (sep2.EndDevice, error)",
				"GetByLFDI func(context.Context, string) (sep2.EndDevice, error)",
				"GetBySFDI func(context.Context, string) (sep2.EndDevice, error)",
				"List func(context.Context, store.ListOptions) (store.ListResult[github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2.EndDevice], error)",
				"Update func(context.Context, string, sep2.EndDevice) error",
			},
		},
		{
			name: "EndDeviceManagementReader",
			typ:  reflect.TypeOf((*store.EndDeviceManagementReader)(nil)).Elem(),
			want: []string{
				"ManagedBy func(context.Context, string) ([]string, error)",
				"ManagerOf func(context.Context, string) (string, error)",
			},
		},
		{
			name: "EndDeviceManagementStore",
			typ:  reflect.TypeOf((*store.EndDeviceManagementStore)(nil)).Elem(),
			want: []string{
				"Assign func(context.Context, string, string) error",
				"ManagedBy func(context.Context, string) ([]string, error)",
				"ManagerOf func(context.Context, string) (string, error)",
				"Unassign func(context.Context, string) error",
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := methodSignatures(tc.typ)
			want := append([]string(nil), tc.want...)
			sort.Strings(want)
			if !reflect.DeepEqual(got, want) {
				t.Errorf("store.%s method set changed.\ngot:  %#v\nwant: %#v", tc.name, got, want)
			}
		})
	}
}

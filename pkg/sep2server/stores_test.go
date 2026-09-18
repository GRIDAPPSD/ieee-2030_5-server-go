package sep2server

import (
	"context"
	"reflect"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/assembly"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
)

// TestNewStoresPopulatesEveryField asserts every nilable field on the store
// set is non-nil.
//
// This has teeth because core treats a nil store field as "skip those routes"
// rather than as an error: a forgotten field does not crash, it silently
// serves a smaller protocol surface. Reflection rather than a hand-written
// list, so a field added to assembly.Stores by a future core bump that
// NewStores does not fill fails HERE, at construction, instead of as a missing
// route somebody notices in the field.
//
// RegistrationPolicy is the one field deliberately left zero; see NewStores.
func TestNewStoresPopulatesEveryField(t *testing.T) {
	t.Parallel()

	stores := NewStores()
	if stores == nil {
		t.Fatal("NewStores returned nil")
	}

	val := reflect.ValueOf(stores).Elem()
	typ := val.Type()
	for i := 0; i < val.NumField(); i++ {
		name := typ.Field(i).Name
		if name == "RegistrationPolicy" {
			// Fail-closed by design: no PIN resolver means no Registration
			// at all, rather than one carrying an invented PIN.
			continue
		}
		field := val.Field(i)
		switch field.Kind() {
		case reflect.Pointer, reflect.Interface, reflect.Map, reflect.Slice, reflect.Chan, reflect.Func:
			if field.IsNil() {
				t.Errorf("assembly.Stores.%s is nil after NewStores: core would silently skip the routes it backs", name)
			}
		}
	}
}

// TestNewStoresAreIndependent asserts two calls hand back separate storage, so
// one embedded server's fleet cannot appear inside another's. A shared
// package-level default would make two servers in one process leak devices
// into each other, which is exactly the sort of thing that only surfaces in a
// multi-server test.
func TestNewStoresAreIndependent(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	first, second := NewStores(), NewStores()

	dev := sep2.EndDevice{}
	if err := first.EndDevices.Create(ctx, "dev-1", dev); err != nil {
		t.Fatalf("create in first: %v", err)
	}

	if _, err := second.EndDevices.Get(ctx, "dev-1"); err == nil {
		t.Fatal("a device created in one store set is visible in another: NewStores is sharing state")
	}

	count, err := second.EndDevices.Count(ctx)
	if err != nil {
		t.Fatalf("count in second: %v", err)
	}
	if count != 0 {
		t.Errorf("second store set holds %d devices, want 0", count)
	}
}

// TestReadOnlyNarrowingIsAvailable is the separability proof for the
// read-only privilege split: a future narrowed accessor beside
// Server.Stores, which hands back the write handle. The claim that the
// two are separable is only worth anything if the narrowing actually
// type-checks against the handle this package ships, so this asserts it
// does rather than asserting it in a comment.
//
// It also pins the shape that narrowing lands as: reader interfaces from
// core's store package, taken from the same underlying stores, so the
// read view cannot drift from what the protocol handlers are writing.
// Nothing is exported here, so the accessor's name and grouping stay open.
func TestReadOnlyNarrowingIsAvailable(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	stores := NewStores()

	// The narrowing a future read-only accessor performs: concrete
	// write-capable stores assigned to read-only interfaces. This
	// assignment failing to compile is the signal that the accessor
	// needs a different shape.
	var (
		devices store.ResourceReader[sep2.EndDevice]        = stores.EndDevices
		status  store.ScopedReader[sep2.DERStatus]          = stores.DERStatuses
		mups    store.ResourceReader[sep2.MirrorUsagePoint] = stores.MirrorUsagePoints
	)

	// A read through the narrowed view must observe a write made through the
	// write handle: the two are views onto one store, not two stores.
	if err := stores.EndDevices.Create(ctx, "dev-1", sep2.EndDevice{}); err != nil {
		t.Fatalf("create through the write handle: %v", err)
	}
	if _, err := devices.Get(ctx, "dev-1"); err != nil {
		t.Errorf("the narrowed reader does not see a write made through the write handle: %v", err)
	}

	if _, err := status.Count(ctx, "dev-1"); err != nil {
		t.Errorf("scoped reader Count: %v", err)
	}
	if _, err := mups.Count(ctx); err != nil {
		t.Errorf("resource reader Count: %v", err)
	}
}

// TestNewReaderStoresSeesWritesThroughTheWriteHandle is criterion 1 and the
// core of criterion 3: assembly.NewReaderStores hands back a second,
// reader-typed accessor over the SAME stores, not a copy. A write made
// through the write handle (the bridge's seeding path) must be visible
// through the read handle (its telemetry path) without any further action.
func TestNewReaderStoresSeesWritesThroughTheWriteHandle(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	writeHandle := NewStores()
	readHandle := assembly.NewReaderStores(writeHandle)

	// SFDI is the distinguishing field: a Get that returned a zero-valued
	// EndDevice for a present id would leave a bare err == nil check green,
	// per data-invariants.md ("tests must assert field values, not just
	// non-crash"). The ManagerOf assertion below is the model this follows.
	if err := writeHandle.EndDevices.Create(ctx, "dev-1", sep2.EndDevice{SFDI: "seeded-sfdi-1"}); err != nil {
		t.Fatalf("seed through the write handle: %v", err)
	}
	if got, err := readHandle.EndDevices.Get(ctx, "dev-1"); err != nil || got.SFDI != "seeded-sfdi-1" {
		t.Errorf("the read handle does not see a device seeded through the write handle: got SFDI %q, err %v", got.SFDI, err)
	}

	if err := writeHandle.EndDeviceManagers.Assign(ctx, "MGR", "DEV"); err != nil {
		t.Fatalf("assign through the write handle: %v", err)
	}
	if got, err := readHandle.EndDeviceManagers.ManagerOf(ctx, "DEV"); err != nil || got != "MGR" {
		t.Errorf("the read handle does not see a management pair assigned through the write handle: got %q, err %v", got, err)
	}

	if err := writeHandle.DERs.Create(ctx, "dev-1", "der-1", sep2.DER{}); err != nil {
		t.Fatalf("create a scoped resource through the write handle: %v", err)
	}
	if count, err := readHandle.DERs.Count(ctx, "dev-1"); err != nil || count != 1 {
		t.Errorf("the read handle does not see a scoped resource created through the write handle: count=%d, err=%v", count, err)
	}
}

package memory_test

import (
	"context"
	"errors"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/storetest"
)

// The device-keyed four: Configuration, DeviceStatus, PowerStatus and
// FunctionSetAssignments, the first slice of
// GRIDAPPSD/ieee-2030_5-server-go#721. Each is scoped under the EndDevice id
// the same way flow reservation and LogEvent records are, so a device's
// records surviving a DELETE would be inherited by whichever device the key
// is next allocated to.

// deviceKeyedFixture builds the four scoped stores and the decorator under
// test, plus a seeded EndDevice at id "1" so Delete has something to cascade
// from.
func deviceKeyedFixture(t *testing.T) (
	s *memory.DeviceKeyedCascadeEndDeviceStore,
	configurations store.ScopedStore[sep2.Configuration],
	deviceStatuses store.ScopedStore[sep2.DeviceStatus],
	powerStatuses store.ScopedStore[sep2.PowerStatus],
	fsas store.ScopedStore[sep2.FunctionSetAssignments],
) {
	t.Helper()
	configurations = memory.NewScopedStore[sep2.Configuration]()
	deviceStatuses = memory.NewScopedStore[sep2.DeviceStatus]()
	powerStatuses = memory.NewScopedStore[sep2.PowerStatus]()
	fsas = memory.NewScopedStore[sep2.FunctionSetAssignments]()
	s = memory.NewDeviceKeyedCascadeEndDeviceStore(memory.NewEndDeviceStore(), configurations, deviceStatuses, powerStatuses, fsas)

	dev := sep2.EndDevice{SFDI: "1111111111", LFDI: "AAAA"}
	dev.Href = "/edev/1"
	if err := s.Create(context.Background(), "1", dev); err != nil {
		t.Fatalf("seed EndDevice: %v", err)
	}
	return s, configurations, deviceStatuses, powerStatuses, fsas
}

// TestDeviceKeyedCascadeEndDeviceStore_DeleteCascadesEachFamily pins the
// acceptance criterion directly: after a successful DELETE, none of the four
// record kinds is readable under the dead key. One subtest per kind, per the
// card, table-driven since the four cases differ only in which store and
// singleton key they seed.
func TestDeviceKeyedCascadeEndDeviceStore_DeleteCascadesEachFamily(t *testing.T) {
	t.Parallel()

	const singletonKey = "default" // coresingleton.SingletonKey; not imported to keep this package's dependency graph flat.

	cases := []struct {
		name  string
		seed  func(ctx context.Context, configurations store.ScopedStore[sep2.Configuration], deviceStatuses store.ScopedStore[sep2.DeviceStatus], powerStatuses store.ScopedStore[sep2.PowerStatus], fsas store.ScopedStore[sep2.FunctionSetAssignments]) error
		count func(ctx context.Context, configurations store.ScopedStore[sep2.Configuration], deviceStatuses store.ScopedStore[sep2.DeviceStatus], powerStatuses store.ScopedStore[sep2.PowerStatus], fsas store.ScopedStore[sep2.FunctionSetAssignments]) (uint32, error)
	}{
		{
			name: "Configuration",
			seed: func(ctx context.Context, c store.ScopedStore[sep2.Configuration], _ store.ScopedStore[sep2.DeviceStatus], _ store.ScopedStore[sep2.PowerStatus], _ store.ScopedStore[sep2.FunctionSetAssignments]) error {
				return c.Create(ctx, "1", singletonKey, sep2.Configuration{})
			},
			count: func(ctx context.Context, c store.ScopedStore[sep2.Configuration], _ store.ScopedStore[sep2.DeviceStatus], _ store.ScopedStore[sep2.PowerStatus], _ store.ScopedStore[sep2.FunctionSetAssignments]) (uint32, error) {
				return c.Count(ctx, "1")
			},
		},
		{
			name: "DeviceStatus",
			seed: func(ctx context.Context, _ store.ScopedStore[sep2.Configuration], d store.ScopedStore[sep2.DeviceStatus], _ store.ScopedStore[sep2.PowerStatus], _ store.ScopedStore[sep2.FunctionSetAssignments]) error {
				return d.Create(ctx, "1", singletonKey, sep2.DeviceStatus{})
			},
			count: func(ctx context.Context, _ store.ScopedStore[sep2.Configuration], d store.ScopedStore[sep2.DeviceStatus], _ store.ScopedStore[sep2.PowerStatus], _ store.ScopedStore[sep2.FunctionSetAssignments]) (uint32, error) {
				return d.Count(ctx, "1")
			},
		},
		{
			name: "PowerStatus",
			seed: func(ctx context.Context, _ store.ScopedStore[sep2.Configuration], _ store.ScopedStore[sep2.DeviceStatus], p store.ScopedStore[sep2.PowerStatus], _ store.ScopedStore[sep2.FunctionSetAssignments]) error {
				return p.Create(ctx, "1", singletonKey, sep2.PowerStatus{})
			},
			count: func(ctx context.Context, _ store.ScopedStore[sep2.Configuration], _ store.ScopedStore[sep2.DeviceStatus], p store.ScopedStore[sep2.PowerStatus], _ store.ScopedStore[sep2.FunctionSetAssignments]) (uint32, error) {
				return p.Count(ctx, "1")
			},
		},
		{
			name: "FunctionSetAssignments",
			seed: func(ctx context.Context, _ store.ScopedStore[sep2.Configuration], _ store.ScopedStore[sep2.DeviceStatus], _ store.ScopedStore[sep2.PowerStatus], f store.ScopedStore[sep2.FunctionSetAssignments]) error {
				return f.Create(ctx, "1", "fsa-1", sep2.FunctionSetAssignments{})
			},
			count: func(ctx context.Context, _ store.ScopedStore[sep2.Configuration], _ store.ScopedStore[sep2.DeviceStatus], _ store.ScopedStore[sep2.PowerStatus], f store.ScopedStore[sep2.FunctionSetAssignments]) (uint32, error) {
				return f.Count(ctx, "1")
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			s, configurations, deviceStatuses, powerStatuses, fsas := deviceKeyedFixture(t)
			ctx := context.Background()

			if err := tc.seed(ctx, configurations, deviceStatuses, powerStatuses, fsas); err != nil {
				t.Fatalf("seed %s: %v", tc.name, err)
			}

			// Control: the record exists before the delete, so a zero count
			// below means the cascade ran rather than nothing having been
			// seeded.
			if n, err := tc.count(ctx, configurations, deviceStatuses, powerStatuses, fsas); err != nil || n != 1 {
				t.Fatalf("control: %s under %q = %d, %v, want 1, nil", tc.name, "1", n, err)
			}

			if err := s.Delete(ctx, "1"); err != nil {
				t.Fatalf("Delete: %v", err)
			}

			if n, err := tc.count(ctx, configurations, deviceStatuses, powerStatuses, fsas); err != nil || n != 0 {
				t.Errorf("%s under the dead key %q = %d, %v, want 0, nil", tc.name, "1", n, err)
			}
		})
	}
}

// TestDeviceKeyedCascadeEndDeviceStore_DeleteFailsClosedWhenOneFamilyCannotCascade
// pins the probe-first rule GRIDAPPSD/ieee-2030_5-server-go#701 added: a
// family that cannot cascade is refused before ANY layer removes anything,
// so a sibling family's records that could have cascaded cleanly must not be
// removed ahead of the failure being discovered.
func TestDeviceKeyedCascadeEndDeviceStore_DeleteFailsClosedWhenOneFamilyCannotCascade(t *testing.T) {
	t.Parallel()

	configurations := memory.NewScopedStore[sep2.Configuration]()
	fault := &storetest.Fault{}
	deviceStatuses := storetest.NewFaultyScopedStore[sep2.DeviceStatus](memory.NewScopedStore[sep2.DeviceStatus](), fault)
	powerStatuses := memory.NewScopedStore[sep2.PowerStatus]()
	fsas := memory.NewScopedStore[sep2.FunctionSetAssignments]()

	inner := memory.NewEndDeviceStore()
	s := memory.NewDeviceKeyedCascadeEndDeviceStore(inner, configurations, deviceStatuses, powerStatuses, fsas)
	ctx := context.Background()

	dev := sep2.EndDevice{SFDI: "1111111111", LFDI: "AAAA"}
	dev.Href = "/edev/1"
	if err := s.Create(ctx, "1", dev); err != nil {
		t.Fatalf("seed EndDevice: %v", err)
	}
	if err := configurations.Create(ctx, "1", "default", sep2.Configuration{}); err != nil {
		t.Fatalf("seed configuration: %v", err)
	}

	fault.Arm(storetest.ErrBackendUnavailable)

	if err := s.Delete(ctx, "1"); err == nil {
		t.Fatal("Delete succeeded while DeviceStatuses could not cascade; want an error and everything left in place")
	}

	if _, err := inner.Get(ctx, "1"); err != nil {
		t.Errorf("device was removed despite the failed cascade: Get(%q) = %v, want the device still present", "1", err)
	}
	fault.Disarm()
	if n, err := configurations.Count(ctx, "1"); err != nil || n != 1 {
		t.Errorf("configurations under %q = %d, %v, want 1, nil: a family that could have cascaded cleanly "+
			"must not run ahead of a sibling family's incapacity being discovered", "1", n, err)
	}
}

// TestNewDeviceKeyedCascadeEndDeviceStore_RejectsANilStore asserts the
// mis-wiring fails at construction, not at request time inside net/http's
// per-request recover.
func TestNewDeviceKeyedCascadeEndDeviceStore_RejectsANilStore(t *testing.T) {
	t.Parallel()

	defer func() {
		if recover() == nil {
			t.Error("a nil decorated store must panic at construction")
		}
	}()
	memory.NewDeviceKeyedCascadeEndDeviceStore(nil,
		memory.NewScopedStore[sep2.Configuration](),
		memory.NewScopedStore[sep2.DeviceStatus](),
		memory.NewScopedStore[sep2.PowerStatus](),
		memory.NewScopedStore[sep2.FunctionSetAssignments](),
	)
}

// TestDeviceKeyedCascadeEndDeviceStore_SkipsAFamilyThatIsNotWired asserts a
// nil argument for one family (the shape a deployment that never mounts
// that family's routes produces, per assembly.go's per-family gate) is
// simply skipped rather than treated as a cascade failure: there is nothing
// stored under an unwired family for Delete to remove.
func TestDeviceKeyedCascadeEndDeviceStore_SkipsAFamilyThatIsNotWired(t *testing.T) {
	t.Parallel()

	configurations := memory.NewScopedStore[sep2.Configuration]()
	inner := memory.NewEndDeviceStore()
	// DeviceStatuses, PowerStatuses and FSAs are all nil: only Configurations
	// is wired.
	s := memory.NewDeviceKeyedCascadeEndDeviceStore(inner, configurations, nil, nil, nil)
	ctx := context.Background()

	dev := sep2.EndDevice{SFDI: "1111111111", LFDI: "AAAA"}
	dev.Href = "/edev/1"
	if err := s.Create(ctx, "1", dev); err != nil {
		t.Fatalf("seed EndDevice: %v", err)
	}
	if err := configurations.Create(ctx, "1", "default", sep2.Configuration{}); err != nil {
		t.Fatalf("seed configuration: %v", err)
	}

	if err := s.Delete(ctx, "1"); err != nil {
		t.Fatalf("Delete: %v, want the wired family cascaded and the unwired ones skipped without error", err)
	}
	if n, err := configurations.Count(ctx, "1"); err != nil || n != 0 {
		t.Errorf("configurations under the dead key %q = %d, %v, want 0, nil", "1", n, err)
	}
	if _, err := inner.Get(ctx, "1"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("device Get after Delete = %v, want ErrNotFound", err)
	}
}

package memory_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/storetest"
)

// The device-keyed four: Configuration, DeviceStatus, PowerStatus and
// FunctionSetAssignments, plus the admin-plane FSA assignment link, the
// first slice of GRIDAPPSD/ieee-2030_5-server-go#721. Each of the four is
// scoped under the EndDevice id the same way flow reservation and LogEvent
// records are, so a device's records surviving a DELETE would be inherited
// by whichever device the key is next allocated to; the admin link is keyed
// by device id in a separate store and survives the same way if not cleared.

// deviceKeyedFixture builds the four scoped stores, the admin FSA store, and
// the decorator under test, plus a seeded EndDevice at id "1" so Delete has
// something to cascade from.
func deviceKeyedFixture(t *testing.T) (
	s *memory.DeviceKeyedCascadeEndDeviceStore,
	configurations store.ScopedStore[sep2.Configuration],
	deviceStatuses store.ScopedStore[sep2.DeviceStatus],
	powerStatuses store.ScopedStore[sep2.PowerStatus],
	fsas store.ScopedStore[sep2.FunctionSetAssignments],
	adminFSAs *memory.AdminFSAStore,
) {
	t.Helper()
	configurations = memory.NewScopedStore[sep2.Configuration]()
	deviceStatuses = memory.NewScopedStore[sep2.DeviceStatus]()
	powerStatuses = memory.NewScopedStore[sep2.PowerStatus]()
	fsas = memory.NewScopedStore[sep2.FunctionSetAssignments]()
	adminFSAs = memory.NewAdminFSAStore()
	s = memory.NewDeviceKeyedCascadeEndDeviceStore(memory.NewEndDeviceStore(), configurations, deviceStatuses, powerStatuses, fsas, adminFSAs)

	dev := sep2.EndDevice{SFDI: "1111111111", LFDI: "AAAA"}
	dev.Href = "/edev/1"
	if err := s.Create(context.Background(), "1", dev); err != nil {
		t.Fatalf("seed EndDevice: %v", err)
	}
	return s, configurations, deviceStatuses, powerStatuses, fsas, adminFSAs
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

			s, configurations, deviceStatuses, powerStatuses, fsas, _ := deviceKeyedFixture(t)
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

// TestDeviceKeyedCascadeEndDeviceStore_DeleteClearsAdminFSAAssignment pins
// the admin-plane half of GRIDAPPSD/ieee-2030_5-server-go#721:
// HandleAssignDeviceFSA writes an admin-plane device -> FSA link in the same
// act as the scoped FunctionSetAssignments record the table above already
// covers, and the link must not outlive its device either. Left behind, the
// admin topology keeps naming a device that is gone, and re-assigning the
// key's new occupant fails as a duplicate.
func TestDeviceKeyedCascadeEndDeviceStore_DeleteClearsAdminFSAAssignment(t *testing.T) {
	t.Parallel()

	s, _, _, _, fsas, adminFSAs := deviceKeyedFixture(t)
	ctx := context.Background()

	if err := adminFSAs.Create(ctx, "fsa-1", sep2.FunctionSetAssignments{}); err != nil {
		t.Fatalf("seed admin FSA: %v", err)
	}
	if err := adminFSAs.AssignDevice(ctx, "fsa-1", "1"); err != nil {
		t.Fatalf("assign device: %v", err)
	}
	if err := fsas.Create(ctx, "1", "fsa-1", sep2.FunctionSetAssignments{}); err != nil {
		t.Fatalf("seed the scoped FSA record the assign handler materializes alongside the admin link: %v", err)
	}

	// Control: the assignment is really there before the delete.
	if devs := adminFSAs.Devices(ctx, "fsa-1"); len(devs) != 1 || devs[0] != "1" {
		t.Fatalf("control: adminFSAs.Devices(fsa-1) = %v, want [1]", devs)
	}

	if err := s.Delete(ctx, "1"); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	if devs := adminFSAs.Devices(ctx, "fsa-1"); len(devs) != 0 {
		t.Errorf("adminFSAs.Devices(fsa-1) after delete = %v, want none: the admin topology still names the dead device", devs)
	}

	// A device registered at the reused key starts with no FSA of its own.
	newDev := sep2.EndDevice{SFDI: "2222222222", LFDI: "BBBB"}
	newDev.Href = "/edev/1"
	if err := s.Create(ctx, "1", newDev); err != nil {
		t.Fatalf("recreate device at the reused key: %v", err)
	}
	if got := adminFSAs.FSAsForDevice(ctx, "1"); len(got) != 0 {
		t.Errorf("the new device at the reused key is already assigned %v, want none", got)
	}

	// Re-assigning the key now succeeds instead of answering ErrAlreadyExists,
	// since the stale link was cleared rather than merely left unread.
	if err := adminFSAs.AssignDevice(ctx, "fsa-1", "1"); err != nil {
		t.Errorf("re-assign after delete = %v, want nil: the stale link must not block a fresh assignment", err)
	}
}

// deleteParentFailingFSAs wraps a real ScopedStore[FunctionSetAssignments]
// so its probe (HasParent, delegated to the real store) succeeds while its
// actual cascade call always fails: the state a probe cannot predict, since
// "A probe passing is not a guarantee the delete that follows will succeed"
// (cascade.go, probeScopedParent's own doc).
type deleteParentFailingFSAs struct {
	store.ScopedStore[sep2.FunctionSetAssignments]
}

var errDeleteParentFailed = errors.New("test: DeleteParent failed")

func (deleteParentFailingFSAs) DeleteParent(context.Context, string) (uint32, error) {
	return 0, errDeleteParentFailed
}

// TestDeviceKeyedCascadeEndDeviceStore_DeleteLeavesTheAdminLinkUntouchedWhenAScopedDeleteFailsAfterItsProbePassed
// pins GRIDAPPSD/ieee-2030_5-server-go#721: the admin FSA link is unassigned
// LAST among this layer's own mutations, after every scoped family, so a
// family's own delete call failing post-probe (the FSA family itself, here)
// never lets the admin link disappear while the device keeps serving the
// program the family's delete never actually removed. A retried DELETE
// finishes the job: nothing about the admin link changed, so it converges
// the same way TestDeviceKeyedCascadeEndDeviceStore_DeleteClearsAdminFSAAssignment
// already pins for the clean path.
func TestDeviceKeyedCascadeEndDeviceStore_DeleteLeavesTheAdminLinkUntouchedWhenAScopedDeleteFailsAfterItsProbePassed(t *testing.T) {
	t.Parallel()

	inner := memory.NewScopedStore[sep2.FunctionSetAssignments]()
	ctx := context.Background()
	if err := inner.Create(ctx, "1", "fsa-1", sep2.FunctionSetAssignments{}); err != nil {
		t.Fatalf("seed the scoped FSA record: %v", err)
	}
	fsas := deleteParentFailingFSAs{ScopedStore: inner}

	configurations := memory.NewScopedStore[sep2.Configuration]()
	deviceStatuses := memory.NewScopedStore[sep2.DeviceStatus]()
	powerStatuses := memory.NewScopedStore[sep2.PowerStatus]()
	adminFSAs := memory.NewAdminFSAStore()
	if err := adminFSAs.Create(ctx, "fsa-1", sep2.FunctionSetAssignments{}); err != nil {
		t.Fatalf("seed admin FSA: %v", err)
	}
	if err := adminFSAs.AssignDevice(ctx, "fsa-1", "1"); err != nil {
		t.Fatalf("assign device: %v", err)
	}

	devs := memory.NewEndDeviceStore()
	s := memory.NewDeviceKeyedCascadeEndDeviceStore(devs, configurations, deviceStatuses, powerStatuses, fsas, adminFSAs)
	dev := sep2.EndDevice{SFDI: "1111111111", LFDI: "AAAA"}
	dev.Href = "/edev/1"
	if err := s.Create(ctx, "1", dev); err != nil {
		t.Fatalf("seed EndDevice: %v", err)
	}

	if err := s.Delete(ctx, "1"); err == nil {
		t.Fatal("Delete succeeded while the FSA family's DeleteParent failed; want an error")
	}

	if _, err := devs.Get(ctx, "1"); err != nil {
		t.Errorf("device was removed despite the failed cascade: Get(1) = %v, want the device still present", err)
	}
	if got := adminFSAs.Devices(ctx, "fsa-1"); len(got) != 1 || got[0] != "1" {
		t.Errorf("adminFSAs.Devices(fsa-1) after the failed cascade = %v, want [1]: the admin link must not be "+
			"cleared ahead of the scoped record it is paired with actually being removed", got)
	}
}

// TestDeviceKeyedCascadeEndDeviceStore_DeleteReportsAnUnassignDeviceFailure
// pins the other direction of GRIDAPPSD/ieee-2030_5-server-go#721: when
// clearing the admin link genuinely fails (a durable AdminFSAStore whose
// snapshot directory is read-only, here, the same reproduction
// TestAdminFSAPersistence_UnassignDeviceRollsBackMemoryWhenPersistFails
// uses at the AdminFSAStore layer), Delete must report that failure rather
// than swallow it, and must not proceed to remove the device.
func TestDeviceKeyedCascadeEndDeviceStore_DeleteReportsAnUnassignDeviceFailure(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	dir := t.TempDir()
	adminFSAs, err := memory.NewAdminFSAStoreWithPersistence(filepath.Join(dir, "fsas.json"))
	if err != nil {
		t.Fatalf("NewAdminFSAStoreWithPersistence: %v", err)
	}
	if err := adminFSAs.Create(ctx, "fsa-1", sep2.FunctionSetAssignments{}); err != nil {
		t.Fatalf("seed admin FSA: %v", err)
	}
	if err := adminFSAs.AssignDevice(ctx, "fsa-1", "1"); err != nil {
		t.Fatalf("assign device: %v", err)
	}

	configurations := memory.NewScopedStore[sep2.Configuration]()
	deviceStatuses := memory.NewScopedStore[sep2.DeviceStatus]()
	powerStatuses := memory.NewScopedStore[sep2.PowerStatus]()
	fsas := memory.NewScopedStore[sep2.FunctionSetAssignments]()
	if err := fsas.Create(ctx, "1", "fsa-1", sep2.FunctionSetAssignments{}); err != nil {
		t.Fatalf("seed the scoped FSA record: %v", err)
	}

	devs := memory.NewEndDeviceStore()
	s := memory.NewDeviceKeyedCascadeEndDeviceStore(devs, configurations, deviceStatuses, powerStatuses, fsas, adminFSAs)
	dev := sep2.EndDevice{SFDI: "1111111111", LFDI: "AAAA"}
	dev.Href = "/edev/1"
	if err := s.Create(ctx, "1", dev); err != nil {
		t.Fatalf("seed EndDevice: %v", err)
	}

	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatalf("chmod the snapshot directory read-only: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	if err := s.Delete(ctx, "1"); err == nil {
		t.Fatal("Delete succeeded while UnassignDevice could not persist; want the error reported")
	}

	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatalf("restore write access: %v", err)
	}
	if _, err := devs.Get(ctx, "1"); err != nil {
		t.Errorf("device was removed despite the failed unassign: Get(1) = %v, want the device still present", err)
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
	s := memory.NewDeviceKeyedCascadeEndDeviceStore(inner, configurations, deviceStatuses, powerStatuses, fsas, nil)
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

// TestDeviceKeyedCascadeEndDeviceStore_DeleteLeavesEveryFamilyUntouchedWhenAnyOneCannotCascade
// is the same probe-first guarantee, composed the way assembly.go actually
// wires it: RegisteredEndDeviceStore, then LogEventLinkedEndDeviceStore,
// then this decorator, with an admin FSA link assigned too. probeInner is
// what reaches the two inner layers' own probes; faulting either of them, or
// any of this layer's own four families, must refuse the delete before
// anything anywhere in the chain is mutated. An admin FSA link is seeded in
// every case because it is unassigned first among this layer's own
// mutations (it has no probe of its own): a probe branch that stopped
// checking its own family would still let that unassign run ahead of the
// family's own (still-failing) delete call, which is what the "every
// family, not just the faulted one" assertions below are built to catch.
func TestDeviceKeyedCascadeEndDeviceStore_DeleteLeavesEveryFamilyUntouchedWhenAnyOneCannotCascade(t *testing.T) {
	t.Parallel()

	cases := []string{"Registrations", "LogEvents", "Configurations", "DeviceStatuses", "PowerStatuses", "FunctionSetAssignments"}

	for _, faulted := range cases {
		t.Run(faulted, func(t *testing.T) {
			t.Parallel()

			fault := &storetest.Fault{}

			innerRegs := memory.NewRegistrationStore()
			var regs store.ResourceStore[sep2.Registration] = innerRegs
			innerEvents := memory.NewScopedStore[sep2.LogEvent]()
			var events store.ScopedStore[sep2.LogEvent] = innerEvents
			innerConfigurations := memory.NewScopedStore[sep2.Configuration]()
			var configurations store.ScopedStore[sep2.Configuration] = innerConfigurations
			innerDeviceStatuses := memory.NewScopedStore[sep2.DeviceStatus]()
			var deviceStatuses store.ScopedStore[sep2.DeviceStatus] = innerDeviceStatuses
			innerPowerStatuses := memory.NewScopedStore[sep2.PowerStatus]()
			var powerStatuses store.ScopedStore[sep2.PowerStatus] = innerPowerStatuses
			innerFSAs := memory.NewScopedStore[sep2.FunctionSetAssignments]()
			var fsas store.ScopedStore[sep2.FunctionSetAssignments] = innerFSAs

			switch faulted {
			case "Registrations":
				regs = storetest.NewFaultyResourceStore[sep2.Registration](innerRegs, fault)
			case "LogEvents":
				events = storetest.NewFaultyScopedStore[sep2.LogEvent](innerEvents, fault)
			case "Configurations":
				configurations = storetest.NewFaultyScopedStore[sep2.Configuration](innerConfigurations, fault)
			case "DeviceStatuses":
				deviceStatuses = storetest.NewFaultyScopedStore[sep2.DeviceStatus](innerDeviceStatuses, fault)
			case "PowerStatuses":
				powerStatuses = storetest.NewFaultyScopedStore[sep2.PowerStatus](innerPowerStatuses, fault)
			case "FunctionSetAssignments":
				fsas = storetest.NewFaultyScopedStore[sep2.FunctionSetAssignments](innerFSAs, fault)
			}

			devs := memory.NewEndDeviceStore()
			bound := memory.NewRegisteredEndDeviceStore(devs, regs, memory.RegistrationPolicy{
				PIN: func(string) (uint32, bool) { return bindingFixturePIN, true },
			})
			linked := memory.NewLogEventLinkedEndDeviceStore(bound, events)
			adminFSAs := memory.NewAdminFSAStore()
			s := memory.NewDeviceKeyedCascadeEndDeviceStore(linked, configurations, deviceStatuses, powerStatuses, fsas, adminFSAs)
			ctx := context.Background()

			dev := sep2.EndDevice{SFDI: "1111111111", LFDI: "AAAA"}
			dev.Href = "/edev/1"
			if err := s.Create(ctx, "1", dev); err != nil {
				t.Fatalf("seed EndDevice: %v", err)
			}
			if err := innerEvents.Create(ctx, "1", "evt-1", sep2.LogEvent{LogEventID: 1}); err != nil {
				t.Fatalf("seed log event: %v", err)
			}
			if err := innerConfigurations.Create(ctx, "1", "default", sep2.Configuration{}); err != nil {
				t.Fatalf("seed configuration: %v", err)
			}
			if err := innerDeviceStatuses.Create(ctx, "1", "default", sep2.DeviceStatus{}); err != nil {
				t.Fatalf("seed device status: %v", err)
			}
			if err := innerPowerStatuses.Create(ctx, "1", "default", sep2.PowerStatus{}); err != nil {
				t.Fatalf("seed power status: %v", err)
			}
			if err := innerFSAs.Create(ctx, "1", "fsa-1", sep2.FunctionSetAssignments{}); err != nil {
				t.Fatalf("seed function set assignment: %v", err)
			}
			if err := adminFSAs.Create(ctx, "fsa-1", sep2.FunctionSetAssignments{}); err != nil {
				t.Fatalf("seed admin FSA: %v", err)
			}
			if err := adminFSAs.AssignDevice(ctx, "fsa-1", "1"); err != nil {
				t.Fatalf("assign device: %v", err)
			}

			// Control: everything is present before the fault is armed.
			if _, err := innerRegs.Get(ctx, "1"); err != nil {
				t.Fatalf("control: registration for 1: %v", err)
			}
			if devs := adminFSAs.Devices(ctx, "fsa-1"); len(devs) != 1 {
				t.Fatalf("control: adminFSAs.Devices(fsa-1) = %v, want [1]", devs)
			}

			fault.Arm(storetest.ErrBackendUnavailable)

			if err := s.Delete(ctx, "1"); err == nil {
				t.Fatalf("Delete succeeded while %s could not cascade; want an error and everything left in place", faulted)
			}

			fault.Disarm()

			if _, err := devs.Get(ctx, "1"); err != nil {
				t.Errorf("device was removed despite the refused delete (%s faulted): %v", faulted, err)
			}
			if _, err := innerRegs.Get(ctx, "1"); err != nil {
				t.Errorf("registration was removed despite the refused delete (%s faulted): %v", faulted, err)
			}
			if n, err := innerEvents.Count(ctx, "1"); err != nil || n != 1 {
				t.Errorf("log events under 1 (%s faulted) = %d, %v, want 1, nil", faulted, n, err)
			}
			if n, err := innerConfigurations.Count(ctx, "1"); err != nil || n != 1 {
				t.Errorf("configurations under 1 (%s faulted) = %d, %v, want 1, nil", faulted, n, err)
			}
			if n, err := innerDeviceStatuses.Count(ctx, "1"); err != nil || n != 1 {
				t.Errorf("device statuses under 1 (%s faulted) = %d, %v, want 1, nil", faulted, n, err)
			}
			if n, err := innerPowerStatuses.Count(ctx, "1"); err != nil || n != 1 {
				t.Errorf("power statuses under 1 (%s faulted) = %d, %v, want 1, nil", faulted, n, err)
			}
			if n, err := innerFSAs.Count(ctx, "1"); err != nil || n != 1 {
				t.Errorf("function set assignments under 1 (%s faulted) = %d, %v, want 1, nil", faulted, n, err)
			}
			if devs := adminFSAs.Devices(ctx, "fsa-1"); len(devs) != 1 {
				t.Errorf("adminFSAs.Devices(fsa-1) after the refused delete (%s faulted) = %v, want [1]: "+
					"the admin link must not be unassigned ahead of a family's own incapacity being discovered", faulted, devs)
			}
		})
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
		memory.NewAdminFSAStore(),
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
	// DeviceStatuses, PowerStatuses, FSAs and AdminFSAs are all nil: only
	// Configurations is wired.
	s := memory.NewDeviceKeyedCascadeEndDeviceStore(inner, configurations, nil, nil, nil, nil)
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

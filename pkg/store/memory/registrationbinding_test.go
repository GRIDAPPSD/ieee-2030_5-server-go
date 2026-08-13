package memory_test

import (
	"context"
	"errors"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

// The EndDevice-Registration coupling at the store layer. The HTTP-level
// acceptance lives in pkg/sep2srv/assembly/registration_binding_test.go;
// this file pins the
// invariant on the type that owns it, so a future edit that breaks the
// coupling fails next to the code rather than three packages away.
//
// No test here seeds a Registration before asserting one exists. That is the
// point: a seeded Registration would make every assertion below pass whether
// or not the binding couples anything.

// bindingFixturePIN is an obvious stand-in, not a secret. It is never
// logged and appears only in test code.
const bindingFixturePIN uint32 = 123455

const (
	bindingLFDI            = "0BA1C3D4E5F60718293A4B5C6D7E8F9012345678"
	bindingUnprovisionedID = "F0E1D2C3B4A5968778695A4B3C2D1E0F87654321"
)

func bindingUnderTest(t *testing.T) (*memory.RegisteredEndDeviceStore, *memory.RegistrationStore) {
	t.Helper()
	regs := memory.NewRegistrationStore()
	bound := memory.NewRegisteredEndDeviceStore(memory.NewEndDeviceStore(), regs, memory.RegistrationPolicy{
		PIN: func(lfdi string) (uint32, bool) {
			if lfdi == bindingUnprovisionedID || lfdi == "" {
				return 0, false
			}
			return bindingFixturePIN, true
		},
	})
	return bound, regs
}

func deviceFixture(key, lfdi string) sep2.EndDevice {
	enabled := true
	dev := sep2.EndDevice{Enabled: &enabled, LFDI: lfdi, SFDI: "1122334455667788"}
	dev.Href = "/edev/" + key
	return dev
}

// TestRegisteredEndDeviceStore_CreateWritesBothHalves asserts the field
// values of the Registration the binding wrote, not merely that a record
// appeared. A record with a zero pIN would satisfy "it exists" and would be
// useless to a client, which is the assertion strength [[data-invariants]]
// Rule 1 requires for anything that goes on the wire.
func TestRegisteredEndDeviceStore_CreateWritesBothHalves(t *testing.T) {
	t.Parallel()

	bound, regs := bindingUnderTest(t)
	memory.SetRegistrationClockForTest(bound, func() int64 { return 1500000000 })
	ctx := context.Background()

	if err := bound.Create(ctx, "3", deviceFixture("3", bindingLFDI)); err != nil {
		t.Fatalf("Create: %v", err)
	}

	reg, err := regs.Get(ctx, "3")
	if err != nil {
		t.Fatalf("no Registration exists under the EndDevice's key: %v", err)
	}
	if reg.Href != "/edev/3/rg" {
		t.Errorf("Registration.Href = %q, want %q", reg.Href, "/edev/3/rg")
	}
	if reg.PIN != bindingFixturePIN {
		t.Error("Registration.pIN is not the value the policy supplied")
	}
	if reg.PollRate != memory.DefaultRegistrationPollRate {
		t.Errorf("Registration.pollRate = %d, want %d", reg.PollRate, memory.DefaultRegistrationPollRate)
	}
	if reg.DateTimeRegistered != 1500000000 {
		t.Errorf("Registration.dateTimeRegistered = %d, want 1500000000", reg.DateTimeRegistered)
	}

	dev, err := bound.Get(ctx, "3")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if dev.RegistrationLink == nil {
		t.Fatal("stored EndDevice carries no RegistrationLink")
	}
	if dev.RegistrationLink.Href != "/edev/3/rg" {
		t.Errorf("RegistrationLink.Href = %q, want %q", dev.RegistrationLink.Href, "/edev/3/rg")
	}
}

// TestRegisteredEndDeviceStore_UnprovisionedDeviceGetsNeitherHalf pins the
// fail-closed branch: no pIN means no Registration AND no link, never a link
// pointing at a resource that will 404.
func TestRegisteredEndDeviceStore_UnprovisionedDeviceGetsNeitherHalf(t *testing.T) {
	t.Parallel()

	bound, regs := bindingUnderTest(t)
	ctx := context.Background()

	// A client-supplied link on the inbound record must not survive: whether
	// the server holds a Registration is not the client's to assert.
	dev := deviceFixture("5", bindingUnprovisionedID)
	dev.RegistrationLink = &sep2.Link{Href: "/edev/5/rg"}
	if err := bound.Create(ctx, "5", dev); err != nil {
		t.Fatalf("Create: %v", err)
	}

	if _, err := regs.Get(ctx, "5"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("Registrations.Get = %v, want ErrNotFound: an unprovisioned device must have no Registration", err)
	}
	got, err := bound.Get(ctx, "5")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.RegistrationLink != nil {
		t.Errorf("unprovisioned EndDevice advertises %q; 2018 section 4.4 p.19 forbids a link to an unimplemented resource", got.RegistrationLink.Href)
	}
}

// TestRegisteredEndDeviceStore_NilPolicyProvisionsNothing asserts the zero
// RegistrationPolicy is inert rather than defaulted. A binding that invented
// a pIN would be handing out a shared secret nobody chose.
func TestRegisteredEndDeviceStore_NilPolicyProvisionsNothing(t *testing.T) {
	t.Parallel()

	regs := memory.NewRegistrationStore()
	bound := memory.NewRegisteredEndDeviceStore(memory.NewEndDeviceStore(), regs, memory.RegistrationPolicy{})
	ctx := context.Background()

	if err := bound.Create(ctx, "1", deviceFixture("1", bindingLFDI)); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := regs.Get(ctx, "1"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("Registrations.Get = %v, want ErrNotFound: a zero policy must provision nothing", err)
	}
	dev, err := bound.Get(ctx, "1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if dev.RegistrationLink != nil {
		t.Errorf("a zero-policy binding advertised %q", dev.RegistrationLink.Href)
	}
}

// TestRegisteredEndDeviceStore_ListDerivesEachEntry asserts the list a client
// walks to discover devices carries the same truth the device resource does.
// A link stripped from one and left in the other is still a link a client
// follows into a 404.
func TestRegisteredEndDeviceStore_ListDerivesEachEntry(t *testing.T) {
	t.Parallel()

	bound, regs := bindingUnderTest(t)
	ctx := context.Background()

	if err := bound.Create(ctx, "1", deviceFixture("1", bindingLFDI)); err != nil {
		t.Fatalf("Create device 1: %v", err)
	}
	if err := bound.Create(ctx, "2", deviceFixture("2", bindingUnprovisionedID)); err != nil {
		t.Fatalf("Create device 2: %v", err)
	}

	result, err := bound.List(ctx, store.ListOptions{Unbounded: true})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(result.Items) != 2 {
		t.Fatalf("List returned %d devices, want 2", len(result.Items))
	}
	if result.Items[0].RegistrationLink == nil {
		t.Error("the provisioned device is listed without its RegistrationLink")
	}
	if result.Items[1].RegistrationLink != nil {
		t.Errorf("the unprovisioned device is listed with %q", result.Items[1].RegistrationLink.Href)
	}

	// Remove the record behind the surviving link and the list must stop
	// advertising it, without the EndDevice record itself being touched.
	if err := regs.Delete(ctx, "1"); err != nil {
		t.Fatalf("Delete Registration: %v", err)
	}
	result, err = bound.List(ctx, store.ListOptions{Unbounded: true})
	if err != nil {
		t.Fatalf("List after removal: %v", err)
	}
	if result.Items[0].RegistrationLink != nil {
		t.Errorf("the list still advertises %q after the record behind it was removed", result.Items[0].RegistrationLink.Href)
	}
}

// TestRegisteredEndDeviceStore_DeleteRemovesBothHalves asserts a Registration
// does not outlive its EndDevice. A surviving record would be served to
// whichever device the key is next allocated to, which hands one device
// another's pIN.
func TestRegisteredEndDeviceStore_DeleteRemovesBothHalves(t *testing.T) {
	t.Parallel()

	bound, regs := bindingUnderTest(t)
	ctx := context.Background()

	if err := bound.Create(ctx, "1", deviceFixture("1", bindingLFDI)); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := bound.Delete(ctx, "1"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := regs.Get(ctx, "1"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("Registrations.Get = %v, want ErrNotFound: the Registration outlived its EndDevice", err)
	}
}

// TestRegisteredEndDeviceStore_CreateReplacesAStaleRecordUnderTheKey covers
// the key-reuse case directly. Index keys are reallocated across boots
// without persistence, so a record left under a key belongs to a device that
// no longer exists, and serving it to the key's new occupant would disclose
// another device's pIN.
func TestRegisteredEndDeviceStore_CreateReplacesAStaleRecordUnderTheKey(t *testing.T) {
	t.Parallel()

	bound, regs := bindingUnderTest(t)
	memory.SetRegistrationClockForTest(bound, func() int64 { return 1500000000 })
	ctx := context.Background()

	stale := sep2.Registration{DateTimeRegistered: 1, PIN: 999999, PollRate: 60}
	stale.Href = "/edev/1/rg"
	if err := regs.Create(ctx, "1", stale); err != nil {
		t.Fatalf("place a stale record under the key: %v", err)
	}

	if err := bound.Create(ctx, "1", deviceFixture("1", bindingLFDI)); err != nil {
		t.Fatalf("Create: %v", err)
	}

	got, err := regs.Get(ctx, "1")
	if err != nil {
		t.Fatalf("Registrations.Get: %v", err)
	}
	if got.PIN != bindingFixturePIN {
		t.Error("the stale record survived: the new device would be served the previous occupant's pIN")
	}
	if got.DateTimeRegistered != 1500000000 {
		t.Errorf("Registration.dateTimeRegistered = %d, want the new registration's time", got.DateTimeRegistered)
	}
}

// TestRegisteredEndDeviceStore_UpdateDoesNotProvision asserts Update never
// brings a Registration into being. Update is a client-driven PUT: letting
// it provision would let a device register itself by editing its own record,
// bypassing whatever the operator configured.
func TestRegisteredEndDeviceStore_UpdateDoesNotProvision(t *testing.T) {
	t.Parallel()

	bound, regs := bindingUnderTest(t)
	ctx := context.Background()

	if err := bound.Create(ctx, "1", deviceFixture("1", bindingUnprovisionedID)); err != nil {
		t.Fatalf("Create: %v", err)
	}

	// The client PUTs a record naming a different LFDI, one the policy WOULD
	// provision, plus a forged link.
	updated := deviceFixture("1", bindingLFDI)
	updated.RegistrationLink = &sep2.Link{Href: "/edev/1/rg"}
	if err := bound.Update(ctx, "1", updated); err != nil {
		t.Fatalf("Update: %v", err)
	}

	if _, err := regs.Get(ctx, "1"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("Registrations.Get = %v, want ErrNotFound: a PUT provisioned a Registration", err)
	}
	got, err := bound.Get(ctx, "1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.RegistrationLink != nil {
		t.Errorf("the forged RegistrationLink survived the update as %q", got.RegistrationLink.Href)
	}
}

// TestRegisteredEndDeviceStore_GetByIdentityDerivesTheLink covers the read
// paths that hand back a device without its store key. They recover the key
// from the device's own Href, so they need their own coverage: a derivation
// that silently failed there would strip a valid link on the POST
// already-registered path.
func TestRegisteredEndDeviceStore_GetByIdentityDerivesTheLink(t *testing.T) {
	t.Parallel()

	bound, _ := bindingUnderTest(t)
	ctx := context.Background()

	dev := deviceFixture("9", bindingLFDI)
	if err := bound.Create(ctx, "9", dev); err != nil {
		t.Fatalf("Create: %v", err)
	}

	byLFDI, err := bound.GetByLFDI(ctx, bindingLFDI)
	if err != nil {
		t.Fatalf("GetByLFDI: %v", err)
	}
	if byLFDI.RegistrationLink == nil || byLFDI.RegistrationLink.Href != "/edev/9/rg" {
		t.Errorf("GetByLFDI did not derive the RegistrationLink: %+v", byLFDI.RegistrationLink)
	}

	bySFDI, err := bound.GetBySFDI(ctx, dev.SFDI)
	if err != nil {
		t.Fatalf("GetBySFDI: %v", err)
	}
	if bySFDI.RegistrationLink == nil || bySFDI.RegistrationLink.Href != "/edev/9/rg" {
		t.Errorf("GetBySFDI did not derive the RegistrationLink: %+v", bySFDI.RegistrationLink)
	}
}

// TestRegisteredEndDeviceStore_MalformedHrefStripsTheLink pins the
// fail-closed direction of the key recovery. A device whose Href does not
// follow the "/edev/{key}" addressing invariant yields no key, so the
// binding cannot know whether a Registration exists and must not claim one.
func TestRegisteredEndDeviceStore_MalformedHrefStripsTheLink(t *testing.T) {
	t.Parallel()

	bound, _ := bindingUnderTest(t)
	ctx := context.Background()

	dev := deviceFixture("1", bindingLFDI)
	dev.Href = "/somewhere/else"
	if err := bound.Create(ctx, "1", dev); err != nil {
		t.Fatalf("Create: %v", err)
	}

	result, err := bound.List(ctx, store.ListOptions{Unbounded: true})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(result.Items) != 1 {
		t.Fatalf("List returned %d devices, want 1", len(result.Items))
	}
	if result.Items[0].RegistrationLink != nil {
		t.Errorf("a device with an off-scheme href was listed advertising %q", result.Items[0].RegistrationLink.Href)
	}
}

// TestRegisteredEndDeviceStore_NilCollaboratorPanics asserts the binding
// refuses to exist with only one half. Failing at construction, once at
// assembly time, is loud; a nil half discovered at request time would be a
// silent 500 inside net/http's per-request recover.
func TestRegisteredEndDeviceStore_NilCollaboratorPanics(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		call func()
	}{
		{"nil EndDeviceStore", func() {
			memory.NewRegisteredEndDeviceStore(nil, memory.NewRegistrationStore(), memory.RegistrationPolicy{})
		}},
		{"nil RegistrationStore", func() {
			memory.NewRegisteredEndDeviceStore(memory.NewEndDeviceStore(), nil, memory.RegistrationPolicy{})
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Error("construction did not panic")
				}
			}()
			tc.call()
		})
	}
}

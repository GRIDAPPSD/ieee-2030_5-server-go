package memory

import (
	"context"
	"errors"
	"fmt"
	"log"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/logsafe"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
)

// Mounting the DER function set and advertising it are one act, as for the
// LogEvent function set in logeventbinding.go.
//
// Before this file NO production path assigned EndDevice.DERListLink. The
// only assignments were the boot fixture and the CSIP loader, so a device
// that registered itself through POST /edev was served an EndDevice with no
// way to reach /edev/{id}/der. A client that walks links (EndDevice ->
// DERListLink -> DERList -> DER -> DERStatusLink) never found anywhere to
// PUT its DERStatus, DERCapability, DERSettings or DERAvailability.
//
// Advertising the list is half of it. Nothing in this server created a DER
// instance either: POST on the DERList is not offered (#480), and every DER
// is meant to be server-seeded. So the same decorator seeds one DER, at
// [DefaultDERID], under each EndDevice it creates, and [SeedDefaultDERs]
// does the same at boot for devices loaded from disk, since the DER stores
// are memory-only and start empty on every restart.

// DefaultDERID is the id of the DER instance this server seeds under each
// EndDevice. It is "1" to share a keyspace with the embedding bridge, which
// seeds one DER per EndDevice at /der/1 (#480).
const DefaultDERID = "1"

// DERListHref is the canonical resource URL of the DERList belonging to the
// EndDevice stored under key, per sep_wadl.xml and 2018 A.4.10.
func DERListHref(key string) string { return "/edev/" + key + "/der" }

// SeedDefaultDER creates the DER at [DefaultDERID] under the EndDevice key
// unless that device already has a DER of any id. A device with DERs of its
// own (a fixture, an embedder's seed, a client PUT) is left as it is.
func SeedDefaultDER(ctx context.Context, ders store.ScopedStore[sep2.DER], key string) error {
	n, err := ders.Count(ctx, key)
	if err != nil {
		return fmt.Errorf("count DERs under %q: %w", key, err)
	}
	if n > 0 {
		return nil
	}
	der := sep2.DER{}
	der.Href = DERListHref(key) + "/" + DefaultDERID
	if err := ders.Create(ctx, key, DefaultDERID, der); err != nil && !errors.Is(err, store.ErrAlreadyExists) {
		return fmt.Errorf("seed DER %s under %q: %w", DefaultDERID, key, err)
	}
	return nil
}

// SeedDefaultDERs runs [SeedDefaultDER] for every stored EndDevice. The
// server calls it once at boot, after persisted and fixture devices are
// loaded.
func SeedDefaultDERs(ctx context.Context, devs store.EndDeviceStore, ders store.ScopedStore[sep2.DER]) error {
	if store.IsAbsent(devs) || store.IsAbsent(ders) {
		return nil
	}
	result, err := devs.List(ctx, store.ListOptions{Unbounded: true})
	if err != nil {
		return fmt.Errorf("list EndDevices to seed DERs: %w", err)
	}
	for _, dev := range result.Items {
		key, ok := keyFromEndDeviceHref(dev.Href)
		if !ok {
			continue
		}
		if err := SeedDefaultDER(ctx, ders, key); err != nil {
			return err
		}
	}
	return nil
}

// DERLinkedEndDeviceStore decorates any [store.EndDeviceStore] so every
// EndDevice it serves advertises its DERList, and every EndDevice it creates
// has a DER to report against.
//
// As with [LogEventLinkedEndDeviceStore], the link is DERIVED on the way out
// from the device's store key rather than served as stored, so a link a
// client tried to install through PUT /edev/{id} is not served back. Its
// all attribute is the live count of DERs under the device.
type DERLinkedEndDeviceStore struct {
	devs store.EndDeviceStore
	ders store.ScopedStore[sep2.DER]
}

var (
	_ store.EndDeviceStore = (*DERLinkedEndDeviceStore)(nil)
	_ deleteProber         = (*DERLinkedEndDeviceStore)(nil)
)

// NewDERLinkedEndDeviceStore decorates devs. Construct it only when the DER
// routes are mounted, for the reason given at
// [NewLogEventLinkedEndDeviceStore]. Both arguments are required.
func NewDERLinkedEndDeviceStore(devs store.EndDeviceStore, ders store.ScopedStore[sep2.DER]) *DERLinkedEndDeviceStore {
	if store.IsAbsent(devs) {
		panic("memory: NewDERLinkedEndDeviceStore: devs (EndDeviceStore) must not be nil")
	}
	if store.IsAbsent(ders) {
		panic("memory: NewDERLinkedEndDeviceStore: ders (ScopedStore) must not be nil")
	}
	return &DERLinkedEndDeviceStore{devs: devs, ders: ders}
}

// Create stores the device and seeds its DER. A seed failure is logged, not
// returned: the device is already stored, refusing the registration would
// not undo that, and [SeedDefaultDERs] repairs it on the next boot.
func (s *DERLinkedEndDeviceStore) Create(ctx context.Context, id string, device sep2.EndDevice) error {
	device.DERListLink = nil
	if err := s.devs.Create(ctx, id, device); err != nil {
		return err
	}
	if err := SeedDefaultDER(ctx, s.ders, id); err != nil {
		log.Printf("memory: EndDevice %q created without a DER: %s", logsafe.Str(id), logsafe.Err(err))
	}
	return nil
}

// Update replaces the stored device. The link is not stored; reads derive it.
func (s *DERLinkedEndDeviceStore) Update(ctx context.Context, id string, device sep2.EndDevice) error {
	device.DERListLink = nil
	return s.devs.Update(ctx, id, device)
}

// probeDelete has nothing of its own to check: the DER cascade belongs to
// [DeviceKeyedCascadeEndDeviceStore]. It passes the probe down the chain.
func (s *DERLinkedEndDeviceStore) probeDelete(ctx context.Context, id string) error {
	return probeInner(ctx, s.devs, id)
}

// Delete delegates; the DER records are cascaded by the layer that owns them.
func (s *DERLinkedEndDeviceStore) Delete(ctx context.Context, id string) error {
	return s.devs.Delete(ctx, id)
}

// Get returns the device stored under id with its link derived from id.
func (s *DERLinkedEndDeviceStore) Get(ctx context.Context, id string) (sep2.EndDevice, error) {
	device, err := s.devs.Get(ctx, id)
	if err != nil {
		return sep2.EndDevice{}, err
	}
	return s.derive(ctx, device, id), nil
}

// GetBySFDI returns the device with the given SFDI, link-derived.
func (s *DERLinkedEndDeviceStore) GetBySFDI(ctx context.Context, sfdi string) (sep2.EndDevice, error) {
	device, err := s.devs.GetBySFDI(ctx, sfdi)
	if err != nil {
		return sep2.EndDevice{}, err
	}
	return s.deriveByHref(ctx, device), nil
}

// GetByLFDI returns the device with the given LFDI, link-derived.
func (s *DERLinkedEndDeviceStore) GetByLFDI(ctx context.Context, lfdi string) (sep2.EndDevice, error) {
	device, err := s.devs.GetByLFDI(ctx, lfdi)
	if err != nil {
		return sep2.EndDevice{}, err
	}
	return s.deriveByHref(ctx, device), nil
}

// List returns a page of devices, each link-derived.
func (s *DERLinkedEndDeviceStore) List(ctx context.Context, opts store.ListOptions) (store.ListResult[sep2.EndDevice], error) {
	result, err := s.devs.List(ctx, opts)
	if err != nil {
		return store.ListResult[sep2.EndDevice]{}, err
	}
	for i := range result.Items {
		result.Items[i] = s.deriveByHref(ctx, result.Items[i])
	}
	return result, nil
}

// Count delegates.
func (s *DERLinkedEndDeviceStore) Count(ctx context.Context) (uint32, error) {
	return s.devs.Count(ctx)
}

// derive sets the link for the device stored under key. A count that cannot
// be read leaves all unset rather than failing the read: the href is still
// right, and all is optional.
func (s *DERLinkedEndDeviceStore) derive(ctx context.Context, device sep2.EndDevice, key string) sep2.EndDevice {
	link := &sep2.ListLink{Href: DERListHref(key)}
	if n, err := s.ders.Count(ctx, key); err == nil {
		link.All = n
	} else {
		log.Printf("memory: DERListLink for %q served without all: %s", logsafe.Str(key), logsafe.Err(err))
	}
	device.DERListLink = link
	return device
}

// deriveByHref recovers the key from the device's own Href, and strips the
// link when it cannot, the fail-closed direction argued at
// [LogEventLinkedEndDeviceStore.deriveByHref].
func (s *DERLinkedEndDeviceStore) deriveByHref(ctx context.Context, device sep2.EndDevice) sep2.EndDevice {
	key, ok := keyFromEndDeviceHref(device.Href)
	if !ok {
		device.DERListLink = nil
		return device
	}
	return s.derive(ctx, device, key)
}

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
// Before this file no production path assigned EndDevice.DERListLink; only
// the boot fixture and the CSIP loader did. A device that registered itself
// through POST /edev was served no way to reach /edev/{id}/der, so a client
// walking links (EndDevice, DERListLink, DERList, DER, DERStatusLink) found
// nowhere to PUT its DERStatus, DERCapability, DERSettings or DERAvailability.
//
// Advertising the list is half of it. Nothing in this server created a DER
// instance either, because POST on the DERList is not offered (#480). CSIP IG
// Figure 10 shows a DERListLink on every EndDevice, and the CTP
// pre-registration lists the DERList and the DER singletons, so the same
// decorator seeds one DER under each EndDevice it creates and [SeedDefaultDERs]
// does so at boot, since the DER stores are memory-only.

// DefaultDERID is the id of the DER this server seeds under each EndDevice.
// It is "1", the id the embedding bridge uses for the DER it seeds. The bridge
// writes through the raw stores, not through this decorator (gridappsd-ieee-
// 2030_5-go internal/sep2embed/seed.go), so the two share a keyspace and
// nothing else.
const DefaultDERID = "1"

// DERListHref is the canonical resource URL of the DERList belonging to the
// EndDevice stored under key.
func DERListHref(key string) string { return "/edev/" + key + "/der" }

// SeedDefaultDER creates the DER at [DefaultDERID] under the EndDevice key
// unless that device already has a DER of any id. A concurrent create of the
// same DER is not an error: the outcome both callers wanted holds.
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

// SeedDefaultDERs runs [SeedDefaultDER] for every stored EndDevice. The server
// calls it once at boot, after persisted and fixture devices are loaded, and
// it is safe to run again.
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
// The link is derived on the read path and never stored, because its all
// attribute is the live DER count under the device. The DER records are
// cascaded on Delete by [DeviceKeyedCascadeEndDeviceStore], not here.
type DERLinkedEndDeviceStore struct {
	linkedEndDevices
	ders store.ScopedStore[sep2.DER]
}

var (
	_ store.EndDeviceStore = (*DERLinkedEndDeviceStore)(nil)
	_ deleteProber         = (*DERLinkedEndDeviceStore)(nil)
)

// derLinks is the DERListLink half of the shared decorator.
type derLinks struct {
	ders store.ScopedStore[sep2.DER]
}

// derive sets the link. A count that cannot be read leaves all unset rather
// than failing the read: the href is right and all is optional.
func (l derLinks) derive(ctx context.Context, d *sep2.EndDevice, key string) {
	link := &sep2.ListLink{Href: DERListHref(key)}
	if n, err := l.ders.Count(ctx, key); err == nil {
		link.All = n
	} else {
		log.Printf("memory: DERListLink for %q served without all: %s", logsafe.Str(key), logsafe.Err(err))
	}
	d.DERListLink = link
}

func (derLinks) forWrite(_ context.Context, d *sep2.EndDevice, _ string) { d.DERListLink = nil }

func (derLinks) strip(d *sep2.EndDevice) bool {
	had := d.DERListLink != nil
	d.DERListLink = nil
	return had
}

func (derLinks) linkNames() string { return "DERListLink" }

// NewDERLinkedEndDeviceStore decorates devs. Construct it only when the DER
// routes are mounted, for the reason given at [NewLogEventLinkedEndDeviceStore].
// Both arguments are required.
func NewDERLinkedEndDeviceStore(devs store.EndDeviceStore, ders store.ScopedStore[sep2.DER]) *DERLinkedEndDeviceStore {
	if store.IsAbsent(devs) {
		panic("memory: NewDERLinkedEndDeviceStore: devs (EndDeviceStore) must not be nil")
	}
	if store.IsAbsent(ders) {
		panic("memory: NewDERLinkedEndDeviceStore: ders (ScopedStore) must not be nil")
	}
	return &DERLinkedEndDeviceStore{linkedEndDevices: linkedEndDevices{devs: devs, fields: derLinks{ders: ders}}, ders: ders}
}

// Create stores the device and seeds its DER. A seed failure is logged, not
// returned: the device is already stored, refusing the registration would not
// undo that, and [SeedDefaultDERs] repairs it on the next boot.
func (s *DERLinkedEndDeviceStore) Create(ctx context.Context, id string, device sep2.EndDevice) error {
	if err := s.linkedEndDevices.Create(ctx, id, device); err != nil {
		return err
	}
	if err := SeedDefaultDER(ctx, s.ders, id); err != nil {
		log.Printf("memory: EndDevice %q created without a DER: %s", logsafe.Str(id), logsafe.Err(err))
	}
	return nil
}

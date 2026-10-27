package memory

import (
	"context"
	"log"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/logsafe"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
)

// endDeviceLinkFields is the part of an EndDevice link decorator that differs
// from its siblings: which link fields it owns and how it fills them. The
// delegation around it, which is identical for every such decorator, lives
// once in [linkedEndDevices].
type endDeviceLinkFields interface {
	// derive sets the owned links for the device stored under key, or clears
	// them when the decorator is the unserved arm of its function set.
	derive(ctx context.Context, device *sep2.EndDevice, key string)

	// forWrite prepares device for storage under key, discarding whatever
	// link the caller supplied.
	forWrite(ctx context.Context, device *sep2.EndDevice, key string)

	// strip clears every owned link and reports whether any was present.
	strip(device *sep2.EndDevice) bool

	// names the owned links in the log line a strip produces.
	linkNames() string
}

// linkedEndDevices is the one implementation of the EndDevice pass-through
// the link decorators share: Create, Update, Get, GetBySFDI, GetByLFDI, List
// and Count, plus a Delete that only delegates.
//
// The link is DERIVED on the way out from the device's store key, never
// served as stored, so a value a client installed through PUT /edev/{id} is
// not served back. Reads that return a device without its key recover it from
// the device's own Href; when that fails the links are STRIPPED rather than
// guessed, the fail-closed direction: a missing link is recoverable by
// re-reading the device under its canonical href, a link derived from a
// malformed href points at a list this server may not serve. A strip that
// discards a link is logged, because it means a device was stored off the
// /edev/{key} addressing scheme the rest of the package relies on.
//
// A decorator embeds this and overrides Delete and probeDelete when it
// cascades records of its own.
type linkedEndDevices struct {
	devs   store.EndDeviceStore
	fields endDeviceLinkFields
}

// Create stores the device with its links prepared for key.
func (s *linkedEndDevices) Create(ctx context.Context, id string, device sep2.EndDevice) error {
	s.fields.forWrite(ctx, &device, id)
	return s.devs.Create(ctx, id, device)
}

// Update replaces the stored device. A client's PUT body cannot install a link
// of its own choosing.
func (s *linkedEndDevices) Update(ctx context.Context, id string, device sep2.EndDevice) error {
	s.fields.forWrite(ctx, &device, id)
	return s.devs.Update(ctx, id, device)
}

// probeDelete passes the probe down the chain; a decorator with records of its
// own overrides it. See [deleteProber].
func (s *linkedEndDevices) probeDelete(ctx context.Context, id string) error {
	return probeInner(ctx, s.devs, id)
}

// Delete delegates; a decorator that owns records beneath the device overrides
// it with a cascade.
func (s *linkedEndDevices) Delete(ctx context.Context, id string) error {
	return s.devs.Delete(ctx, id)
}

// Get returns the device stored under id with its links derived from id.
func (s *linkedEndDevices) Get(ctx context.Context, id string) (sep2.EndDevice, error) {
	device, err := s.devs.Get(ctx, id)
	if err != nil {
		return sep2.EndDevice{}, err
	}
	s.fields.derive(ctx, &device, id)
	return device, nil
}

// GetBySFDI returns the device with the given SFDI, link-derived.
func (s *linkedEndDevices) GetBySFDI(ctx context.Context, sfdi string) (sep2.EndDevice, error) {
	device, err := s.devs.GetBySFDI(ctx, sfdi)
	if err != nil {
		return sep2.EndDevice{}, err
	}
	return s.deriveByHref(ctx, device), nil
}

// GetByLFDI returns the device with the given LFDI, link-derived.
func (s *linkedEndDevices) GetByLFDI(ctx context.Context, lfdi string) (sep2.EndDevice, error) {
	device, err := s.devs.GetByLFDI(ctx, lfdi)
	if err != nil {
		return sep2.EndDevice{}, err
	}
	return s.deriveByHref(ctx, device), nil
}

// List returns a page of devices, each link-derived.
func (s *linkedEndDevices) List(ctx context.Context, opts store.ListOptions) (store.ListResult[sep2.EndDevice], error) {
	result, err := s.devs.List(ctx, opts)
	if err != nil {
		return store.ListResult[sep2.EndDevice]{}, err
	}
	for i := range result.Items {
		result.Items[i] = s.deriveByHref(ctx, result.Items[i])
	}
	return result, nil
}

// Count delegates: links change what a device advertises, not how many
// devices exist.
func (s *linkedEndDevices) Count(ctx context.Context) (uint32, error) {
	return s.devs.Count(ctx)
}

func (s *linkedEndDevices) deriveByHref(ctx context.Context, device sep2.EndDevice) sep2.EndDevice {
	key, ok := keyFromEndDeviceHref(device.Href)
	if !ok {
		if s.fields.strip(&device) {
			log.Printf("memory: EndDevice href %q does not follow the /edev/{key} addressing invariant; serving it without %s", logsafe.Str(device.Href), logsafe.Str(s.fields.linkNames()))
		}
		return device
	}
	s.fields.derive(ctx, &device, key)
	return device
}

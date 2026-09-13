package memory

import (
	"encoding/json"
	"fmt"
	"log"
	"maps"
	"math"
	"strconv"
	"sync"
)

// EndDeviceIndex assigns and resolves the opaque, server-chosen index that
// identifies an EndDevice in resource URLs: the "3" in "/edev/3/rg".
//
// # Why an index rather than the LFDI
//
// Both forms are spec-legal. IEEE 2030.5 paths are server-chosen and
// discovered through links, the WADL writes the segment as {id1}, and the
// standard's own example instance uses href="/edev/3/reg". The index form is
// chosen here for four reasons, in priority order:
//
//  1. Certificate rotation. The LFDI is SHA-256 over the device certificate,
//     so rotating a cert (which real deployments do on expiry) changes the
//     LFDI. With the LFDI in the path, rotation changes every URL for that
//     device and invalidates every stored link and cached href. An index
//     assigned against a rotation-independent device key survives rotation.
//     See "Choosing a device key" below: this property is only delivered if
//     the caller supplies a key that does NOT derive from the certificate.
//  2. It keeps a certificate fingerprint out of access logs, reverse-proxy
//     logs, browser history, and Referer headers.
//  3. It is 40 hex characters shorter per path segment, on a protocol partly
//     aimed at constrained devices.
//  4. It matches the WADL and the standard's examples.
//
// # Identity versus addressing
//
// This type changes ADDRESSING only. It is not an identity mechanism and must
// never be used as one. A device's identity remains its certificate-derived
// LFDI, stored on the EndDevice record; the ownership gate and the ACL
// continue to compare that stored LFDI against the LFDI derived from the
// caller's TLS certificate. The index only selects WHICH record a request
// addresses; authorization then resolves through that record to the LFDI. An
// index is public, guessable, and carries no authority: possession of an
// index proves nothing.
//
// Because indices CAN move (see the stability section), a stale URL held by a
// client may address a different device than it did when the client learned
// it. That case is handled by the ownership gate, which denies it on the
// caller's certificate rather than serving it. Do not weaken that gate on the
// reasoning that indices are stable: stability is a property of one
// configuration, the gate has to hold in all of them.
//
// # Choosing a device key
//
// Allocate is keyed by a caller-supplied deviceKey, which should be the most
// durable identity the caller has for the physical device:
//
//   - A consumer that provisions from an external asset model should pass
//     that model's stable identifier (for the GridAPPS-D bridge, the CIM
//     mRID). Such a key is independent of the certificate, so the index
//     survives certificate rotation, which is reason 1 above.
//   - A consumer with nothing but the certificate (core's own POST /edev
//     self-registration path, where the device is known only by its cert)
//     must pass the LFDI. That is correct and safe, but note the honest
//     limitation: a device that self-registers under a rotated certificate
//     presents a new LFDI, is an unknown key, and receives a NEW index.
//     Rotation survival for self-registering devices requires out-of-band
//     provisioning that carries a stable key across the rotation; it cannot
//     be synthesized from the certificate alone.
//
// # What stability is actually guaranteed
//
// Stability comes from persistence, not from allocation order, and the
// guarantee differs by constructor. Be precise about which one is in use:
//
//   - NewEndDeviceIndexWithPersistence: every assignment is flushed to disk
//     before Allocate returns it and reloaded on the next boot, so a device
//     keeps its index across restart AND across fleet insertions and
//     deletions. Indices are never reused, including across restart, so no
//     stored link can come to address a different device. This is the
//     stronger property and the correct production configuration.
//   - NewEndDeviceIndex: assignments live for the process lifetime only. A
//     restart re-addresses the fleet; the same device can return under a
//     different index, and an index can return pointing at a different
//     device. This is acceptable only where client and server are known to
//     start fresh together, so no client holds a URL from a previous run.
//
// Neither constructor makes the ownership gate optional. A fleet change, a
// re-provisioning, or a migration can reshuffle addressing under either one.
//
// # Determinism within a run
//
// Indices are handed out in call order, so a caller that bulk-provisions MUST
// iterate in a deterministic order (sorted by device key, never Go map
// iteration order, which is randomized per process). Otherwise the same fleet
// produces different URLs on every run for no reason, which makes an
// end-to-end run irreproducible.
//
// EndDeviceIndex is safe for concurrent use.
type EndDeviceIndex struct {
	mu      sync.Mutex
	byKey   map[string]string // device key -> index
	byIndex map[string]string // index -> device key
	next    uint64
	path    string // "" means in-memory only
}

// edevIndexRecord is the on-disk shape of a single assignment. It is written
// inside the shared snapshot envelope (see persistence.go), so it inherits
// the same version gate and atomic-rename durability contract as every other
// persistent store in this package.
//
// Future work introduces an operator-editable per-device provisioning
// record for registration PINs. A device's index belongs in THAT record
// rather than in this parallel file once it exists: two files keyed by the
// same device can disagree, and the index and the PIN are both per-device
// provisioning facts with the same lifetime. Fold this in when that record
// lands.
type edevIndexRecord struct {
	DeviceKey string `json:"device_key"`
	Index     string `json:"index"`
}

// firstIndex is the first index handed out on a cold boot. Numbering starts
// at 1 rather than 0 so that no device is addressed as "/edev/0", which reads
// like a sentinel or an uninitialized value in a log line.
const firstIndex = 1

// NewEndDeviceIndex returns an in-memory-only allocator. Assignments do not
// survive the process. See "What stability is actually guaranteed" on
// EndDeviceIndex for when that is acceptable.
func NewEndDeviceIndex() *EndDeviceIndex {
	return &EndDeviceIndex{
		byKey:   make(map[string]string),
		byIndex: make(map[string]string),
		next:    firstIndex,
	}
}

// NewEndDeviceIndexWithPersistence returns an allocator backed by a JSON
// snapshot at path. An empty path is equivalent to NewEndDeviceIndex.
//
// A missing file is a cold boot and is not an error. A corrupt file, an
// unknown schema version, or a snapshot containing a duplicate device key or
// duplicate index returns an error and no allocator: the caller decides
// whether to rebuild or fail, and is never handed a partially-loaded map that
// would quietly hand out an index already in use.
func NewEndDeviceIndexWithPersistence(path string) (*EndDeviceIndex, error) {
	x := NewEndDeviceIndex()
	if path == "" {
		return x, nil
	}
	if err := x.loadFromFile(path); err != nil {
		return nil, fmt.Errorf("enddevice index persistence: load %q: %w", path, err)
	}
	x.path = path
	return x, nil
}

// loadFromFile rehydrates assignments from path. No lock is taken: this runs
// during construction, before the allocator is published.
func (x *EndDeviceIndex) loadFromFile(path string) error {
	env, err := readSnapshotEnvelope(path)
	if err != nil {
		return err
	}
	if env == nil || len(env.Records) == 0 {
		return nil // cold boot
	}
	var records []edevIndexRecord
	if err := json.Unmarshal(env.Records, &records); err != nil {
		return fmt.Errorf("decode records: %w", err)
	}

	highest := uint64(0)
	for _, r := range records {
		if r.DeviceKey == "" || r.Index == "" {
			return fmt.Errorf("blank device key or index in snapshot")
		}
		if _, dup := x.byKey[r.DeviceKey]; dup {
			return fmt.Errorf("duplicate device key %q in snapshot", r.DeviceKey)
		}
		if _, dup := x.byIndex[r.Index]; dup {
			// Two device keys sharing one index would make the index
			// ambiguous, so refuse rather than pick a winner: a wrong winner
			// routes one device's URLs at another device.
			return fmt.Errorf("duplicate index %q in snapshot", r.Index)
		}
		n, err := strconv.ParseUint(r.Index, 10, 64)
		if err != nil {
			return fmt.Errorf("non-numeric index %q for device key %q in snapshot", r.Index, r.DeviceKey)
		}
		// Reject non-canonical decimal (leading zeros, e.g. "01") before it
		// enters byIndex. persistLocked looks entries up by the canonical
		// string it derives from the numeric range, so a non-canonical entry
		// would silently vanish from the very next snapshot write: the
		// record is not overwritten, it is dropped, and the device becomes
		// unreachable at the URL it was actually given out under.
		if canonical := strconv.FormatUint(n, 10); canonical != r.Index {
			return fmt.Errorf("non-canonical index %q (want %q) for device key %q in snapshot", r.Index, canonical, r.DeviceKey)
		}
		// firstIndex=1 is the numbering floor; "0" reads as a sentinel or an
		// uninitialized value at the wire (see firstIndex's own comment), so
		// a snapshot claiming index 0 is corrupt, not merely unusual.
		if n < firstIndex {
			return fmt.Errorf("index %q for device key %q in snapshot is below firstIndex %d", r.Index, r.DeviceKey, firstIndex)
		}
		x.byKey[r.DeviceKey] = r.Index
		x.byIndex[r.Index] = r.DeviceKey
		if n > highest {
			highest = n
		}
	}
	// Resume above the highest index ever recorded, so a restart never
	// reissues a number a client may still be holding.
	x.next = highest + 1
	return nil
}

// Allocate returns the index for deviceKey, assigning a new one if the key
// has not been seen. It is idempotent: the same key always yields the same
// index for the life of the allocator's durable state.
//
// A blank deviceKey is rejected rather than defaulted. Synthesizing an index
// for an unidentified device would let two distinct devices collide onto one
// URL, so this fails closed.
//
// When persistence is configured, the new assignment is flushed to disk
// BEFORE it is returned. If that flush fails the assignment is rolled back out
// of memory and the error is returned, so the server never serves an index it
// failed to durably record. The counter is deliberately NOT rolled back: a gap
// in the numbering is harmless, whereas reissuing a number that a
// partially-completed write may have recorded is not.
func (x *EndDeviceIndex) Allocate(deviceKey string) (string, error) {
	if deviceKey == "" {
		return "", fmt.Errorf("enddevice index: device key is required")
	}

	x.mu.Lock()
	defer x.mu.Unlock()

	if idx, ok := x.byKey[deviceKey]; ok {
		return idx, nil
	}

	// x.next is a uint64; incrementing it at MaxUint64 wraps to 0, which is
	// below firstIndex and would eventually walk back into ids already
	// occupied. Refuse rather than hand out a number that silently wraps.
	if x.next == math.MaxUint64 {
		return "", fmt.Errorf("enddevice index: allocator exhausted at max uint64")
	}

	idx := strconv.FormatUint(x.next, 10)
	x.next++
	x.byKey[deviceKey] = idx
	x.byIndex[idx] = deviceKey

	if err := x.persistLocked(); err != nil {
		delete(x.byKey, deviceKey)
		delete(x.byIndex, idx)
		return "", err
	}
	return idx, nil
}

// IndexFor returns the index already assigned to deviceKey. The bool reports
// presence; IndexFor never assigns.
func (x *EndDeviceIndex) IndexFor(deviceKey string) (string, bool) {
	x.mu.Lock()
	defer x.mu.Unlock()
	idx, ok := x.byKey[deviceKey]
	return idx, ok
}

// DeviceKey returns the device key an index was assigned to. The bool reports
// presence.
//
// This resolves ADDRESSING only. A caller must not treat a hit as evidence
// that the requester is that device: see the "Identity versus addressing"
// section on EndDeviceIndex.
func (x *EndDeviceIndex) DeviceKey(index string) (string, bool) {
	x.mu.Lock()
	defer x.mu.Unlock()
	key, ok := x.byIndex[index]
	return key, ok
}

// Assignments returns an independent copy of the device-key-to-index map, for
// operator tooling and tests. Mutating the result does not affect the
// allocator.
func (x *EndDeviceIndex) Assignments() map[string]string {
	x.mu.Lock()
	defer x.mu.Unlock()
	return maps.Clone(x.byKey)
}

// persistLocked writes the current assignments to disk. Caller holds x.mu.
// No-op when no path is configured.
func (x *EndDeviceIndex) persistLocked() error {
	if x.path == "" {
		return nil
	}
	// Emit in numeric index order so the snapshot is byte-stable across writes
	// with an unchanged assignment set, which keeps backups small and makes an
	// unexpected change visible in a diff.
	records := make([]edevIndexRecord, 0, len(x.byKey))
	for n := uint64(firstIndex); n < x.next; n++ {
		idx := strconv.FormatUint(n, 10)
		key, ok := x.byIndex[idx]
		if !ok {
			continue // gap left by a rolled-back allocation
		}
		records = append(records, edevIndexRecord{DeviceKey: key, Index: idx})
	}
	if err := writeSnapshotEnvelope(x.path, records); err != nil {
		return fmt.Errorf("enddevice index persistence: %w", err)
	}
	return nil
}

// NewEndDeviceIndexFromStore returns an in-memory index pre-seeded with an
// assignment for every device already in s, keyed by each device's LFDI and
// addressed by the id it is already stored under, with the counter resumed
// above the highest id found.
//
// This is what keeps allocation from colliding with a persisted
// EndDeviceStore after a restart (GRIDAPPSD/ieee-2030_5-server-go#443): the
// store's records outlive this type's own in-memory state, so an unseeded
// NewEndDeviceIndex would reissue an id a persisted device already occupies.
// Call this in place of NewEndDeviceIndex wherever the index sits in front
// of a store that may already hold records, whether or not the index itself
// is also configured with a persistence path.
//
// A record whose id is not one of this allocator's own canonical decimal
// ids, or whose LFDI is blank, is not entered into byKey: Allocate could
// never look it up by that key regardless. Its numeric id, if it has one,
// still raises the counter floor, so the slot is never reissued.
//
// When two records share one LFDI (GRIDAPPSD/ieee-2030_5-server-go#446),
// only the FIRST one reached in the store's own snapshot order is entered
// into byKey; every later one with that LFDI is skipped. Snapshot order is
// the store's sorted key order (lexicographic on the id STRING, not
// creation order or numeric value: "10" sorts before "9"), so which of the
// two records the LFDI resolves to is not the one that registered first.
// Both records carry the caller's own LFDI regardless of which wins, so no
// cross-identity record becomes reachable this way.
//
// A skipped record is logged, once, as a count rather than by id or LFDI:
// this is a boot-time summary for an operator, and it must not put a device
// identifier in the log any more than the request-time paths do.
func NewEndDeviceIndexFromStore(s *EndDeviceStore) *EndDeviceIndex {
	x := NewEndDeviceIndex()
	highest := uint64(firstIndex - 1)
	skipped := 0
	for _, r := range s.snapshotEndDevices() {
		n, err := strconv.ParseUint(r.ID, 10, 64)
		if err != nil || strconv.FormatUint(n, 10) != r.ID || n < firstIndex {
			skipped++
			continue
		}
		if n > highest {
			highest = n
		}
		if r.Device.LFDI == "" {
			skipped++
			continue
		}
		if _, dup := x.byKey[r.Device.LFDI]; dup {
			skipped++
			continue
		}
		x.byKey[r.Device.LFDI] = r.ID
		x.byIndex[r.ID] = r.Device.LFDI
	}
	if skipped > 0 {
		log.Printf("enddevice index: seeding from the store skipped %d record(s): non-canonical id, blank LFDI, or LFDI shared with another record", skipped)
	}
	// highest+1 wraps to 0 when a stored id is math.MaxUint64. Pin x.next at
	// MaxUint64 instead, so Allocate's own overflow guard refuses cleanly
	// rather than the wrap happening here, silently, before Allocate ever runs.
	if highest == math.MaxUint64 {
		x.next = math.MaxUint64
	} else {
		x.next = highest + 1
	}
	return x
}

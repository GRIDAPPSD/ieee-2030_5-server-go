// Package enddevice provides IEEE 2030.5 EndDevice resource handlers for
// GET/POST/PUT/DELETE /edev and /edev/{id}. Ported from the reference
// server's internal/handler/edev.go; auth touch points replaced by the
// injected AuthPolicy seam.
package enddevice

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/identity"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2/encoding"
	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2tls"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/logsafe"
	lfdiform "github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/lfdi"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/srverr"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
)

// EndDeviceListHref is the resource href that EndDeviceList subscribers
// register against per IEEE 2030.5 section 11.1. Used by the DELETE handler
// to identify the subscription fan-out target when an EndDevice is removed
// (CSIP V1.2 MAINT-002 step 5).
const EndDeviceListHref = "/edev"

// deviceIndexConflictMessage is the client-visible body for the 409 answered
// when the record already occupying an ALLOCATED INDEX belongs to a
// different certificate identity than the caller's
// (GRIDAPPSD/ieee-2030_5-server-go#443).
const deviceIndexConflictMessage = "device index conflict"

// deviceSFDIConflictMessage is the client-visible body for the 409 answered
// when a record found by SFDI belongs to a different certificate identity
// than the caller's. Kept distinct from deviceIndexConflictMessage: the two
// refusals are raised by different lookups (GetBySFDI versus the index
// allocator) and an operator grepping logs or client-support tickets needs to
// tell them apart.
const deviceSFDIConflictMessage = "device SFDI conflict"

// conflictLogPrefix begins every line logged when POST /edev refuses a
// request rather than disclose another identity's EndDevice. It mirrors
// srverr's "route + reason" log shape without importing srverr's 500-only
// semantics into this 409 path.
const conflictLogPrefix = "sep2srv: 409 on "

// deviceLFDIConflictMessage is the client-visible body for the 409 answered
// when a manager's POST names an LFDI that a stored record already holds in a
// different letter case.
const deviceLFDIConflictMessage = "device LFDI conflict"

// notManagedMessage is the client-visible body for the 403 answered when a
// POST names an lFDI other than the caller's that the caller does not manage,
// or when in-band management is switched off.
const notManagedMessage = "lFDI is not managed by this certificate"

// inBandOffMessage is the client-visible body for the 403 answered when the
// server option refuses manager registration, so a real manager can tell it
// from a missing pair.
const inBandOffMessage = "in-band management is off"

// managedLogPrefix begins the line logged for each manager-registered device,
// naming the caller and the managed LFDI so the add is attributable.
const managedLogPrefix = "sep2srv: manager registered a managed device: "

// managedDeleteLogPrefix begins the line logged for each manager-deleted
// device, so the erasure is attributable to the caller.
const managedDeleteLogPrefix = "sep2srv: manager deleted a managed device: "

// logConflict records that a POST /edev was refused, naming the route and
// the kind of conflict, and nothing about either identity involved.
func logConflict(r *http.Request, kind string) {
	log.Printf("%s%s: %s", conflictLogPrefix, logsafe.Str(srverr.Route(r)), logsafe.Str(kind))
}

// ResourceNotifier dispatches subscription notifications for a resource
// href. It is the minimal surface HandleDeleteEndDevice needs from the
// subscription package and is defined here at the consumer (Pike rule:
// interfaces at the consumer, not at the producer). The production
// implementation is *subscription.Manager.
type ResourceNotifier interface {
	Notify(ctx context.Context, resourceHref string, status uint8)
}

// IdentityFunc extracts the authenticated device identity from the request
// context. Returns the LFDI, SFDI, and ok=true when identity is present.
// Replaces the direct auth.GetIdentity call in the original edev.go.
// The server wires auth.GetIdentity; tests supply a fixed identity.
type IdentityFunc = identity.Func

// SFDIPrefixFunc derives the EndDevice id prefix from an SFDI string.
// Replaces auth.ExtractSFDIPrefix (the short-SFDI guard,
// GRIDAPPSD/ieee-2030_5-server-go#13). The server wires
// auth.ExtractSFDIPrefix; tests supply a trivial truncation.
//
// Its RETURN VALUE is no longer used to address the device: resource URLs
// now carry the opaque index allocated by EndDeviceIndexer (see below). It
// is still called, and its error still rejects the registration, because
// the guard is a validity check on the SFDI itself and dropping the call
// would silently drop that check along with the addressing change.
type SFDIPrefixFunc func(sfdi string) (string, error)

// EndDeviceIndexer allocates the opaque, server-chosen index that identifies
// a device in resource URLs: the "3" in "/edev/3/rg". Declared here at the
// consumer; *memory.EndDeviceIndex is the production implementation.
//
// deviceKey is the most durable identity the caller has for the device. On
// this self-registration path the device is known only by its certificate,
// so the LFDI is the only key available. That means a device presenting a
// ROTATED certificate is an unknown key and receives a new index; surviving
// rotation requires out-of-band provisioning under a certificate-independent
// key, which this path by construction does not have. See the
// memory.EndDeviceIndex doc comment for the full discussion.
type EndDeviceIndexer interface {
	Allocate(deviceKey string) (string, error)
}

// BuildEndDeviceList constructs an EndDeviceList from store results.
func BuildEndDeviceList(href string, result store.ListResult[sep2.EndDevice], pollRate uint32) sep2.EndDeviceList {
	return sep2.EndDeviceList{
		ListResource: sep2.ListResource{
			SubscribableResource: sep2.SubscribableResource{
				Resource: sep2.Resource{Href: href},
			},
			All:      result.All,
			Results:  result.Results,
			PollRate: pollRate,
		},
		EndDevice: result.Items,
	}
}

// HandleEndDevice returns a handler for GET /edev/{id}.
func HandleEndDevice(s store.EndDeviceStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			encoding.MethodNotAllowed(w, "GET, HEAD")
			return
		}

		id := r.PathValue("id")
		if id == "" {
			http.Error(w, "device id required", http.StatusBadRequest)
			return
		}

		dev, err := s.Get(r.Context(), id)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				http.Error(w, "not found", http.StatusNotFound)
				return
			}
			srverr.Internal(w, r, err)
			return
		}

		encoding.WriteXML(w, http.StatusOK, &dev)
	}
}

// HandleCreateEndDevice returns a handler for POST /edev.
//
// It creates a new EndDevice, setting identity from the injected
// IdentityFunc, validating the SFDI through SFDIPrefixFunc (the short-SFDI
// guard), and addressing the device by the opaque index allocated from idx.
//
// Identity and addressing are separate concerns here and must stay separate.
// The stored EndDevice keeps the certificate-derived LFDI and SFDI, which is
// what every ownership check compares against; the index only decides which
// URL the record is served under.
//
// RegistrationLink is NOT stamped here and is not this handler's to decide.
// Whether a device may advertise a Registration depends on
// whether the server holds one, which only the store knows. Pass a
// *memory.RegisteredEndDeviceStore for s to get the coupled behavior; with
// any other store no device carries a RegistrationLink, which is what 2018
// section 4.4 p.19 requires when the function set is not populated.
func HandleCreateEndDevice(s store.EndDeviceStore, idx EndDeviceIndexer, identity IdentityFunc, sfdiPrefix SFDIPrefixFunc, opts ...CreateOption) http.HandlerFunc {
	var cfg createConfig
	for _, o := range opts {
		if o != nil {
			o(&cfg)
		}
	}
	// idx is a required collaborator: every code path below that reaches
	// registration calls idx.Allocate. A nil idx would panic on the first
	// POST /edev, inside net/http's per-request recover, which turns a
	// mis-wired server into a silent 500 (or a bare connection reset) for
	// every caller instead of a loud failure at boot. Fail here, at
	// construction, which happens once when the router is assembled: this
	// is the exported package's own wiring point, independent of whatever a
	// given assembler layers on top of it. The production assembler
	// (pkg/sep2srv/assembly) already resolves a nil Stores.EndDeviceIndexes
	// to a substitute before it ever calls this constructor, so that path
	// never trips this panic; this check is for any other caller of this
	// exported constructor that passes nil directly.
	//
	// The guard asks store.IsAbsent rather than comparing against nil: idx
	// is an interface parameter, and an interface holding a nil concrete
	// pointer is not equal to nil, so a plain
	// comparison misses exactly the caller this comment already names: any
	// other caller of this exported constructor that passes a typed nil
	// rather than the untyped literal.
	if store.IsAbsent(idx) {
		panic("enddevice: HandleCreateEndDevice: idx (EndDeviceIndexer) must not be nil")
	}
	var mu sync.Mutex
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			encoding.MethodNotAllowed(w, "POST")
			return
		}

		lfdi, sfdi, ok := identity(r.Context())
		if !ok {
			http.Error(w, "identity required", http.StatusForbidden)
			return
		}

		// Read body (optional: client may POST with minimal or empty body)
		var dev sep2.EndDevice
		body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if err != nil {
			http.Error(w, "read body failed", http.StatusBadRequest)
			return
		}
		if len(body) > 0 {
			if err := xml.Unmarshal(body, &dev); err != nil {
				srverr.BadRequestMessage(w, r, "invalid XML", err)
				return
			}
		}

		// Resolve whose identity the record carries. It is the certificate's
		// unless the caller is the manager of the lFDI the body names.
		target, ok := resolveTarget(w, r, &cfg, dev, lfdi, sfdi)
		if !ok {
			return
		}
		callerLFDI := lfdi
		managed := target.lfdi != callerLFDI
		lfdi, sfdi = target.lfdi, target.sfdi

		// Never trust a client-supplied SFDI/LFDI beyond what resolveTarget
		// accepted.
		dev.SFDI = sfdi
		dev.LFDI = lfdi
		dev.ChangedTime = time.Now().Unix()
		enabled := true
		dev.Enabled = &enabled

		// The checks below and the Create at the end are one step: two
		// concurrent POSTs for colliding identities would otherwise both pass
		// the lookups and both create. The store enforces uniqueness on the id
		// only, and changing that would change every other writer of the
		// store (admin creates, boot seeding), so the handler serializes its
		// own check-and-create. The lock is released before the response is
		// written.
		mu.Lock()
		unlock := sync.OnceFunc(mu.Unlock)
		defer unlock()

		// A minted record must not duplicate or shadow an LFDI the store
		// already holds, whatever its SFDI or letter case: the same LFDI
		// returns the record, another case of it is someone else's.
		if managed {
			held, exact, found, err := findByLFDIFold(r.Context(), s, lfdi)
			switch {
			case err != nil:
				srverr.Internal(w, r, fmt.Errorf("the registration lookup by LFDI failed, so no device was provisioned: %w", err))
				return
			case found && exact:
				unlock()
				w.Header().Set("Location", held.Href)
				encoding.WriteXML(w, http.StatusOK, &held)
				return
			case found:
				logConflict(r, "lfdi conflict")
				http.Error(w, deviceLFDIConflictMessage, http.StatusConflict)
				return
			}
		}

		// Check if already registered.
		//
		// A lookup that did not COMPLETE is not evidence that the device is
		// unregistered, and the two must not be collapsed here, because the
		// branch this guards is a WRITE. Falling through on a transient failure
		// provisions a second EndDevice for a device that may already have one,
		// under a second URL index allocated to the same LFDI: duplicate fleet
		// state that nothing downstream can distinguish from a genuine second
		// device, plus a re-addressing that strands the path a client was
		// already given. A 500 costs the client a retry; the write costs
		// corruption no later read can detect.
		existing, err := s.GetBySFDI(r.Context(), sfdi)
		switch {
		case err == nil:
			if existing.LFDI != lfdi {
				// SFDI collision: IEEE 2030.5's SFDI is a short
				// checksum-including derivative, not a collision-safe hash,
				// so two different certificates can share one even though
				// their LFDIs differ. Same disclosure rule as the index
				// collision below: answer without the other identity's record.
				logConflict(r, "sfdi conflict")
				http.Error(w, deviceSFDIConflictMessage, http.StatusConflict)
				return
			}
			// Already exists under this identity: return 200 with existing device
			unlock()
			w.Header().Set("Location", existing.Href)
			encoding.WriteXML(w, http.StatusOK, &existing)
			return
		case !errors.Is(err, store.ErrNotFound):
			srverr.Internal(w, r, fmt.Errorf("the registration lookup by SFDI failed, so no device was provisioned: %w", err))
			return
		}

		// Short-SFDI guard: reject a malformed or too-short SFDI before the
		// device is admitted. The returned prefix is intentionally discarded;
		// it used to be the device id, and addressing now comes from idx.
		if _, err := sfdiPrefix(sfdi); err != nil {
			srverr.InternalMessage(w, r, "invalid device identity", err)
			return
		}

		// Address the device by an opaque server-chosen index. The LFDI is
		// the only device key this path has (the device is known solely by
		// its certificate), so a rotated certificate yields a new index here.
		id, err := idx.Allocate(lfdi)
		if err != nil {
			srverr.Internal(w, r, fmt.Errorf("allocate a device index: %w", err))
			return
		}
		dev.Href = store.DeviceHref(id)
		dev.FunctionSetAssignmentsListLink = &sep2.ListLink{Href: fmt.Sprintf("/edev/%s/fsa", id)}

		// RegistrationLink is deliberately NOT stamped here.
		// It used to be, unconditionally, while nothing ever wrote a
		// Registration record, so every device advertised a resource that
		// answered 404. The link now comes from the store, which stamps it
		// only when it wrote the Registration to go with it, and that is
		// why the response below is re-read rather than serving the local
		// copy: this handler no longer knows which links the device has.

		if cfg.beforeCreate != nil {
			if err := sweepVacantID(r.Context(), s, cfg.beforeCreate, id); err != nil {
				srverr.Internal(w, r, err)
				return
			}
		}

		if err := s.Create(r.Context(), id, dev); err != nil {
			if errors.Is(err, store.ErrAlreadyExists) {
				// Race condition: another goroutine registered something at
				// this id. If the record was deleted between Create and Get,
				// return 5xx rather than a zero-value 200 (silent data loss).
				existing, getErr := s.Get(r.Context(), id)
				if getErr != nil {
					srverr.InternalMessage(w, r, "registration race", fmt.Errorf("re-read after ErrAlreadyExists: %w", getErr))
					return
				}
				if existing.LFDI != lfdi {
					// idx.Allocate gave this caller an id another identity
					// already holds (GRIDAPPSD/ieee-2030_5-server-go#443).
					// idx.Allocate is idempotent per key, so retrying under
					// the same certificate reproduces this every time; 409
					// tells the client that, unlike a 500, which reads as
					// transient. Never disclose the occupying record.
					logConflict(r, "index conflict")
					http.Error(w, deviceIndexConflictMessage, http.StatusConflict)
					return
				}
				// Same identity raced its own registration: serve the record
				// it already has.
				unlock()
				w.Header().Set("Location", existing.Href)
				encoding.WriteXML(w, http.StatusOK, &existing)
				return
			}
			srverr.Internal(w, r, err)
			return
		}

		// Serve what the store holds, not what was handed to it. A store
		// that couples the EndDevice to its Registration decides at Create
		// time whether the device may advertise a RegistrationLink, and a
		// 201 body built from the local copy would report links the server
		// will not serve. A read that fails here is a real error: the
		// record was just written, so its absence means the store is not
		// answering, and a zero-value 200 would be silent data loss.
		created, err := s.Get(r.Context(), id)
		if err != nil {
			srverr.Internal(w, r, fmt.Errorf("re-read after create: %w", err))
			return
		}

		if managed {
			log.Printf("%s%s: caller %s, managed %s", managedLogPrefix, logsafe.Str(srverr.Route(r)), logsafe.Str(callerLFDI), logsafe.Str(lfdi))
		}

		unlock()
		w.Header().Set("Location", created.Href)
		encoding.WriteXML(w, http.StatusCreated, &created)
	}
}

// ResolveClaimedLFDI canonicalizes a client-claimed LFDI (a MirrorUsagePoint
// deviceLFDI or an EndDevice lFDI), or falls back to fallback when the client
// left it absent. It is the one owner of that rule: POST /edev and the /mup
// handlers both call it.
//
// raw is trimmed of surrounding whitespace before anything else. sep.xsd
// types deviceLFDI as xs:hexBinary, whose whiteSpace facet is "collapse":
// leading and trailing whitespace is stripped before the value is compared,
// so " 00AABB...\n" is schema-valid and names the same value as
// "00AABB...". Go's encoding/xml has no XSD facet awareness and hands back
// the text node verbatim, so this server has to apply the facet itself
// rather than refuse a document the standard accepts.
//
// IEEE 2030.5-2023 UsagePointBase.deviceLFDI SHALL be present when mirroring
// (extraction line 12584-12585), but tolerating absence keeps every existing
// client that posts only an mRID working: absence is not itself malformed. A
// claim that is whitespace only collapses to empty under the same facet, so
// it is treated exactly like an absent element, not like a malformed one.
//
// ok is false when the trimmed claim is non-empty but cannot be a valid
// HexBinary160 value (see lfdiform.CanonicalPadded); the caller answers 400 in
// that case, never a silent fallback to fallback, because a malformed claim
// is a client error, not an absent one.
func ResolveClaimedLFDI(raw, fallback string) (device string, ok bool) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return fallback, true
	}
	return lfdiform.CanonicalPadded(trimmed)
}

// createTarget is the identity a POST /edev record is stored under.
type createTarget struct{ lfdi, sfdi string }

// resolveTarget decides whose identity a POST /edev stores, and answers the
// refusal itself when there is none. A body with no lFDI, or one that names
// the caller's own LFDI once resolved by ResolveClaimedLFDI, registers the
// caller as it always has. Any other canonical lFDI is accepted only when the
// caller is its current manager (CurrentManagerOwns) and in-band management is
// on; the sFDI is then derived, and a body sFDI that differs from the derived
// one is refused rather than stored.
func resolveTarget(w http.ResponseWriter, r *http.Request, cfg *createConfig, dev sep2.EndDevice, callerLFDI, callerSFDI string) (createTarget, bool) {
	self := createTarget{callerLFDI, callerSFDI}
	claimed, ok := ResolveClaimedLFDI(dev.LFDI, callerLFDI)
	if !ok {
		cfg.refuse(w, r, callerLFDI, http.StatusBadRequest, "invalid-lfdi", "invalid lFDI")
		return createTarget{}, false
	}
	if claimed == callerLFDI {
		return self, true
	}
	manages := false
	if !cfg.inBand.off && !store.IsAbsent(cfg.inBand.managers) {
		var err error
		manages, err = CurrentManagerOwns(r.Context(), cfg.inBand.managers, claimed, callerLFDI)
		if err != nil {
			srverr.Internal(w, r, fmt.Errorf("the management lookup failed, so no device was provisioned: %w", err))
			return createTarget{}, false
		}
	}
	if !manages {
		cfg.inBand.refuseForbidden(w, r, callerLFDI)
		return createTarget{}, false
	}
	derived, err := sep2tls.SFDIFromLFDI(claimed)
	if err != nil {
		cfg.refuse(w, r, callerLFDI, http.StatusBadRequest, "invalid-lfdi", "invalid lFDI")
		return createTarget{}, false
	}
	if dev.SFDI != "" && dev.SFDI != derived {
		cfg.refuse(w, r, callerLFDI, http.StatusBadRequest, "sfdi-mismatch", "sFDI does not match the lFDI")
		return createTarget{}, false
	}
	return createTarget{claimed, derived}, true
}

// refuse answers a POST /edev refusal and reports it to the bounded refusal
// log when one is wired; the same limiter that bounds the ownership gate's
// denial lines then bounds these.
func (c *createConfig) refuse(w http.ResponseWriter, r *http.Request, caller string, status int, reason, msg string) {
	c.inBand.refuse(w, r, caller, status, reason, msg)
}

// inBandManagement is the one owner of the in-band switch, the management
// store and the refusal log, shared by POST and DELETE.
type inBandManagement struct {
	managers store.EndDeviceManagementReader
	off      bool
	refusals func(r *http.Request, caller, reason string)
}

const (
	reasonInBandOff  = "in-band-off"
	reasonNotManaged = "not-manager"
)

func (m inBandManagement) refuse(w http.ResponseWriter, r *http.Request, caller string, status int, reason, msg string) {
	if m.refusals != nil {
		m.refusals(r, caller, reason)
	}
	http.Error(w, msg, status)
}

// refuseForbidden answers the 403 a manager gets when in-band management is
// off or the device is not its to manage.
func (m inBandManagement) refuseForbidden(w http.ResponseWriter, r *http.Request, caller string) {
	if m.off {
		m.refuse(w, r, caller, http.StatusForbidden, reasonInBandOff, inBandOffMessage)
		return
	}
	m.refuse(w, r, caller, http.StatusForbidden, reasonNotManaged, notManagedMessage)
}

// InBandManagement configures a manager's in-band add and delete of the
// devices it manages. One value feeds both [HandleCreateEndDevice] (through
// ForCreate) and [HandleDeleteEndDevice] (through ForDelete), so the switch,
// the store and the refusal log cannot differ between them.
type InBandManagement struct {
	// Managers is the management store; absent means a manager is refused.
	Managers store.EndDeviceManagementStore
	// Off refuses every manager add and delete with 403.
	Off bool
	// Refusals, when set, receives each refusal with the caller's LFDI and a
	// short reason; it owns the bounding of what it writes.
	Refusals func(r *http.Request, caller, reason string)
}

func (m InBandManagement) config() inBandManagement {
	return inBandManagement{managers: m.Managers, off: m.Off, refusals: m.Refusals}
}

// ForCreate is the option for [HandleCreateEndDevice].
func (m InBandManagement) ForCreate() CreateOption {
	return func(c *createConfig) { c.inBand = m.config() }
}

// ForDelete is the option for [HandleDeleteEndDevice]: a manager the
// ownership gate admitted (see [WithDelegatedAccess]) releases the pair and
// deletes the record.
func (m InBandManagement) ForDelete() DeleteOption {
	return func(c *deleteConfig) {
		c.inBand = m.config()
		if !store.IsAbsent(m.Managers) {
			c.release = m.Managers.Release
		}
	}
}

// findByLFDIFold looks for a record whose LFDI equals lfdi ignoring case. It
// reports the record and whether its LFDI is exactly lfdi. The exact index is
// tried first, then the whole list is scanned, because the store indexes only
// the exact string and a record added by an operator or an older version may
// hold any letter case.
func findByLFDIFold(ctx context.Context, s store.EndDeviceReader, lfdi string) (found sep2.EndDevice, exact, ok bool, err error) {
	dev, state, err := RecordFor(ctx, s, lfdi)
	if err != nil {
		return sep2.EndDevice{}, false, false, err
	}
	if state == RecordPresent {
		return dev, true, true, nil
	}
	all, err := s.List(ctx, store.ListOptions{Unbounded: true})
	if err != nil {
		return sep2.EndDevice{}, false, false, err
	}
	for _, d := range all.Items {
		if strings.EqualFold(d.LFDI, lfdi) {
			return d, d.LFDI == lfdi, true, nil
		}
	}
	return sep2.EndDevice{}, false, false, nil
}

// HandleUpdateEndDevice returns a handler for PUT /edev/{id}.
//
// The body replaces the record except for its identity: the stored LFDI and
// SFDI are kept whatever the body carries, whether present, empty, absent or
// different. Identity is the certificate's, fixed at registration, and the
// ownership gate, GET /edev and the store's LFDI and SFDI indexes all resolve
// by it, so a client body must not move it. A differing value is ignored
// rather than refused, so a client that omits the optional lFDI, or PUTs back
// the document it fetched, is not turned away.
func HandleUpdateEndDevice(s store.EndDeviceStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			encoding.MethodNotAllowed(w, "PUT")
			return
		}

		id := r.PathValue("id")
		if id == "" {
			http.Error(w, "device id required", http.StatusBadRequest)
			return
		}

		body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if err != nil {
			http.Error(w, "read body failed", http.StatusBadRequest)
			return
		}

		var dev sep2.EndDevice
		if err := xml.Unmarshal(body, &dev); err != nil {
			srverr.BadRequestMessage(w, r, "invalid XML", err)
			return
		}

		current, err := s.Get(r.Context(), id)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				http.Error(w, "not found", http.StatusNotFound)
				return
			}
			srverr.Internal(w, r, err)
			return
		}

		dev.Href = store.DeviceHref(id)
		dev.LFDI, dev.SFDI = current.LFDI, current.SFDI
		dev.ChangedTime = time.Now().Unix()

		if err := s.Update(r.Context(), id, dev); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				http.Error(w, "not found", http.StatusNotFound)
				return
			}
			srverr.Internal(w, r, err)
			return
		}

		w.WriteHeader(http.StatusNoContent)
	}
}

// CreateOption configures [HandleCreateEndDevice].
type CreateOption func(*createConfig)

type createConfig struct {
	beforeCreate func(ctx context.Context, id string) error
	inBand       inBandManagement
}

// WithRefusalLog sends each 403 and 400 a POST /edev refuses on the caller's
// identity claim to fn, with the caller's LFDI and a short reason. fn owns the
// bounding of what it writes; without it these refusals are answered and not
// logged.
func WithRefusalLog(fn func(r *http.Request, caller, reason string)) CreateOption {
	return func(c *createConfig) { c.inBand.refusals = fn }
}

// WithManagers lets a caller register the EndDevices it manages: a POST whose
// body names another lFDI is accepted when managers says the caller is that
// LFDI's current manager. Without it, such a POST answers 403.
func WithManagers(managers store.EndDeviceManagementReader) CreateOption {
	return func(c *createConfig) { c.inBand.managers = managers }
}

// WithInBandManagementOff refuses every manager registration with 403, the
// server option that leaves device registration to the device or an operator.
func WithInBandManagementOff() CreateOption {
	return func(c *createConfig) { c.inBand.off = true }
}

// WithBeforeCreate runs fn with the allocated id just before a new device is
// stored there, but only when no device occupies that id. The index is reused
// for a known LFDI, so this is where a cleanup a failed DELETE left undone is
// finished before the new record can inherit it.
func WithBeforeCreate(fn func(ctx context.Context, id string) error) CreateOption {
	return func(c *createConfig) { c.beforeCreate = fn }
}

// sweepVacantID calls fn for id when s holds no device there. A device that
// is present means a live record owns the id, and its subscriptions are not
// stale.
func sweepVacantID(ctx context.Context, s store.EndDeviceStore, fn func(context.Context, string) error, id string) error {
	_, err := s.Get(ctx, id)
	switch {
	case err == nil:
		return nil
	case errors.Is(err, store.ErrNotFound):
		return fn(ctx, id)
	default:
		return fmt.Errorf("check whether %q is vacant: %w", id, err)
	}
}

// releaseManagedPair drops manager's pair for the record at id before the
// record is deleted, and reports whether the delete may go on. Releasing
// first fails closed: if the delete then fails, the manager has lost the grant
// and an operator can reach the orphaned record, where the reverse order would
// leave a pair no retry could reach (the gate answers 404 on a missing
// record). ErrNotFound from Release means the grant that admitted the request
// is gone, so nothing is deleted.
func (c *deleteConfig) releaseManagedPair(w http.ResponseWriter, r *http.Request, s store.EndDeviceStore, manager, id string) bool {
	if c.inBand.off {
		c.inBand.refuseForbidden(w, r, manager)
		return false
	}
	if c.release == nil {
		srverr.Internal(w, r, fmt.Errorf("enddevice: a manager delete of %q has no management store to release the pair in", id))
		return false
	}
	dev, err := s.Get(r.Context(), id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			http.Error(w, "not found", http.StatusNotFound)
			return false
		}
		srverr.Internal(w, r, fmt.Errorf("read EndDevice %q before releasing its pair: %w", id, err))
		return false
	}
	if err := c.release(r.Context(), manager, dev.LFDI); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			c.inBand.refuse(w, r, manager, http.StatusForbidden, reasonNotManaged, notManagedMessage)
			return false
		}
		srverr.Internal(w, r, fmt.Errorf("release the management pair of EndDevice %q for manager %s: %w", id, logsafe.Str(manager), err))
		return false
	}
	log.Printf("%s%s: manager %s released the pair of EndDevice %s", managedDeleteLogPrefix, logsafe.Str(srverr.Route(r)), logsafe.Str(manager), logsafe.Str(id))
	return true
}

// DeleteOption configures [HandleDeleteEndDevice].
type DeleteOption func(*deleteConfig)

type deleteConfig struct {
	afterNotify func(ctx context.Context, id string) error
	inBand      inBandManagement
	release     func(ctx context.Context, manager, managed string) error
}

type delegatedAccessKey struct{}

// WithDelegatedAccess records on ctx that an /edev/{id} request was admitted
// because manager is the record's current manager, not because the caller owns
// it. Only the ownership gate sets it; [DelegatedBy] reads it.
func WithDelegatedAccess(ctx context.Context, manager string) context.Context {
	return context.WithValue(ctx, delegatedAccessKey{}, manager)
}

// DelegatedBy returns the manager LFDI recorded by [WithDelegatedAccess], or
// "" when the request was admitted as the device itself.
func DelegatedBy(ctx context.Context) string {
	manager, _ := ctx.Value(delegatedAccessKey{}).(string)
	return manager
}

// WithAfterNotify runs fn with the deleted device's id after the device is
// removed and the EndDeviceList notification is queued. A failure answers 500;
// the device is already gone, and a retried DELETE answers 404.
func WithAfterNotify(fn func(ctx context.Context, id string) error) DeleteOption {
	return func(c *deleteConfig) { c.afterNotify = fn }
}

// recipientPreparer is satisfied by *subscription.Manager, and by an adapter
// that forwards to one.
type recipientPreparer interface {
	PrepareNotify(ctx context.Context, resourceHref string) func(status uint8)
}

// PrepareDeleted readies the notice to /edev/{id} subscribers that the
// EndDevice is gone, NotificationStatusCanceledDeleted (IEEE 2030.5-2023
// status 4), and returns the func that sends it. It is the one owner of that
// notice: the DELETE route and the out-of-band test hook both use it.
//
// Call it BEFORE the delete and the returned func only after the delete
// succeeded. The router's subscriber check refuses a subscriber whose
// EndDevice, or whose managed record, is already gone, so deciding the
// recipients after the delete would reach nobody; deciding them before and
// sending after reaches those who could read the record, and a failed delete
// sends nothing. A notifier without PrepareNotify notifies at send time. A
// nil n sends nothing.
func PrepareDeleted(ctx context.Context, n ResourceNotifier, id string) (send func()) {
	if n == nil {
		return func() {}
	}
	href := store.DeviceHref(id)
	if p, ok := n.(recipientPreparer); ok {
		deliver := p.PrepareNotify(ctx, href)
		return func() { deliver(sep2.NotificationStatusCanceledDeleted) }
	}
	return func() { n.Notify(ctx, href, sep2.NotificationStatusCanceledDeleted) }
}

// HandleDeleteEndDevice returns a handler for DELETE /edev/{id}. CSIP V1.2
// MAINT-002 step 5 mandates that on successful deletion the server fires a
// Notification on the EndDeviceList subscription (SubscribedResource =
// "/edev") with NotificationStatusDefault: the list changed, no subscription ended.
// Subscribers of /edev/{id} itself get [PrepareDeleted]'s notice. The notifier is optional:
// passing nil disables notification fan-out (useful for tests that don't
// exercise the subscription path).
//
// A [DeleteOption] adds work after the notification is queued. Subscriptions
// are removed there rather than inside the store's Delete because Notify looks
// up its subscribers, the deleted device's own among them, and removing them
// first would drop the notification MAINT-002 requires.
func HandleDeleteEndDevice(s store.EndDeviceStore, n ResourceNotifier, opts ...DeleteOption) http.HandlerFunc {
	var cfg deleteConfig
	for _, o := range opts {
		if o != nil {
			o(&cfg)
		}
	}
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			encoding.MethodNotAllowed(w, "DELETE")
			return
		}

		id := r.PathValue("id")
		if id == "" {
			http.Error(w, "device id required", http.StatusBadRequest)
			return
		}

		sendDeleted := PrepareDeleted(r.Context(), n, id)

		manager := DelegatedBy(r.Context())
		if manager != "" && !cfg.releaseManagedPair(w, r, s, manager, id) {
			return
		}

		if err := s.Delete(r.Context(), id); err != nil {
			if manager != "" {
				log.Printf("%s%s: manager %s lost its pair but the delete of EndDevice %s failed: %s", managedDeleteLogPrefix, logsafe.Str(srverr.Route(r)), logsafe.Str(manager), logsafe.Str(id), logsafe.Str(err.Error()))
			}
			if errors.Is(err, store.ErrNotFound) {
				http.Error(w, "not found", http.StatusNotFound)
				return
			}
			srverr.Internal(w, r, err)
			return
		}
		if manager != "" {
			log.Printf("%s%s: caller %s, id %s", managedDeleteLogPrefix, logsafe.Str(srverr.Route(r)), logsafe.Str(manager), logsafe.Str(id))
		}

		// MAINT-002 step 5: fan out a removal notification to every
		// EndDeviceList subscriber. The Manager enqueues onto a bounded
		// queue and returns immediately, so this stays non-blocking.
		if n != nil {
			n.Notify(r.Context(), EndDeviceListHref, sep2.NotificationStatusDefault)
		}
		sendDeleted()

		if cfg.afterNotify != nil {
			if err := cfg.afterNotify(r.Context(), id); err != nil {
				srverr.Internal(w, r, err)
				return
			}
		}

		w.WriteHeader(http.StatusNoContent)
	}
}

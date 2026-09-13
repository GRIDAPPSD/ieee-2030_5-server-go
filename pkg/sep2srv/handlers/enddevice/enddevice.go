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
	"io"
	"log"
	"net/http"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2/encoding"
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

// logConflict records that a POST /edev was refused, naming the route and
// the kind of conflict, and nothing about either identity involved.
func logConflict(r *http.Request, kind string) {
	log.Printf("%s%s: %s", conflictLogPrefix, srverr.Route(r), kind)
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
type IdentityFunc func(ctx context.Context) (lfdi, sfdi string, ok bool)

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
func HandleCreateEndDevice(s store.EndDeviceStore, idx EndDeviceIndexer, identity IdentityFunc, sfdiPrefix SFDIPrefixFunc) http.HandlerFunc {
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
				http.Error(w, "invalid XML: "+err.Error(), http.StatusBadRequest)
				return
			}
		}

		// Override identity from cert (never trust client-supplied SFDI/LFDI)
		dev.SFDI = sfdi
		dev.LFDI = lfdi
		dev.ChangedTime = time.Now().Unix()
		enabled := true
		dev.Enabled = &enabled

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
		dev.Href = "/edev/" + id
		dev.FunctionSetAssignmentsListLink = &sep2.ListLink{Href: fmt.Sprintf("/edev/%s/fsa", id)}

		// RegistrationLink is deliberately NOT stamped here.
		// It used to be, unconditionally, while nothing ever wrote a
		// Registration record, so every device advertised a resource that
		// answered 404. The link now comes from the store, which stamps it
		// only when it wrote the Registration to go with it, and that is
		// why the response below is re-read rather than serving the local
		// copy: this handler no longer knows which links the device has.

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

		w.Header().Set("Location", created.Href)
		encoding.WriteXML(w, http.StatusCreated, &created)
	}
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
			http.Error(w, "invalid XML: "+err.Error(), http.StatusBadRequest)
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

		dev.Href = "/edev/" + id
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

// HandleDeleteEndDevice returns a handler for DELETE /edev/{id}. CSIP V1.2
// MAINT-002 step 5 mandates that on successful deletion the server fires a
// Notification on the EndDeviceList subscription (SubscribedResource =
// "/edev") with NotificationStatusRemoved. The notifier is optional:
// passing nil disables notification fan-out (useful for tests that don't
// exercise the subscription path).
func HandleDeleteEndDevice(s store.EndDeviceStore, n ResourceNotifier) http.HandlerFunc {
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

		if err := s.Delete(r.Context(), id); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				http.Error(w, "not found", http.StatusNotFound)
				return
			}
			srverr.Internal(w, r, err)
			return
		}

		// MAINT-002 step 5: fan out a removal notification to every
		// EndDeviceList subscriber. The Manager enqueues onto a bounded
		// queue and returns immediately, so this stays non-blocking.
		if n != nil {
			n.Notify(r.Context(), EndDeviceListHref, sep2.NotificationStatusRemoved)
		}

		w.WriteHeader(http.StatusNoContent)
	}
}

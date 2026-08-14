package metering

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2/encoding"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/srverr"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
)

// LFDIProvider extracts a device LFDI from the request context.
// Returns the LFDI string and a boolean indicating whether an identity
// was present. Server passes auth.GetIdentity-derived closure; tests
// inject directly. The server-side auth package stays out of core by
// threading this callback through at construction time (same pattern
// as the subscription manager's NotificationObserver from Phase D1).
type LFDIProvider func(ctx context.Context) (lfdi string, ok bool)

// PostRateProvider resolves the server's preferred postRate, in seconds, for
// the client identified by lfdi, and reports whether the server has an
// opinion at all. A false second return means "no configured rate": the
// handler then leaves whatever postRate the client supplied untouched,
// including none.
//
// sep.xsd:6485-6487 documents MirrorUsagePoint.postRate as "how often
// mirrored data should be POSTed, in seconds. A client MAY indicate a
// preferred postRate when POSTing MirrorUsagePoint. A server MAY add or
// modify postRate to indicate its preferred posting rate." Both verbs are
// explicit, so a server that is configured with a rate is entitled to
// overwrite a client's preference, not merely to fill an absent one. That is
// what HandleCreateMirrorUsagePoint does, and it is the reason this is a
// resolver rather than a plain "default if absent" value.
//
// The rate is keyed on the CREATING client's LFDI, the same identity
// HandleCreateMirrorUsagePoint stamps into MirrorUsagePoint.DeviceLFDI. That
// makes a per-device rate policy a pure consumer-side concern: a consumer
// that today answers one fleet-wide value for every LFDI can later answer a
// per-device value with no change to this package or to any call site here.
//
// Distinct from LFDIProvider above: that one reads identity off the request
// context, this one maps an already-resolved identity to a policy value. A
// nil PostRateProvider is safe and means the server has no opinion for any
// client.
type PostRateProvider func(lfdi string) (rate uint32, ok bool)

// stampMirrorMeterReading assigns the server-owned fields on a
// MirrorMeterReading and returns the store id it derived them from.
//
// href and lastUpdateTime are the server's to assign, never the client's.
// MirrorMeterReading embeds Resource, so href arrives as a client-settable
// attribute, and lastUpdateTime is a plain client-writable element. /mup is
// not /edev-scoped, so no ownership middleware constrains what a client may
// claim there, and GET /mup echoes stored records back to every reader: a
// verbatim client href lets one device plant a path that points at another
// device's reading namespace, and a verbatim lastUpdateTime lets it forge the
// age of a reading others consume.
//
// Both the inline MirrorUsagePoint.MirrorMeterReading path and the
// out-of-band POST /mup/{id}/mr path route through here so the two endpoints
// cannot drift into minting different href shapes for the same resource kind.
//
// The zero-padded fixed-width nanosecond id is load-bearing rather than
// cosmetic: the store orders keys lexicographically, so fixed-width digits
// make lexical order match chronological order. lastUpdateTime is derived
// from the same instant as the id rather than from a second clock read, so a
// record's timestamp and its ordering key can never disagree.
func stampMirrorMeterReading(mmr *sep2.MirrorMeterReading, parentID string, nanos int64) string {
	id := fmt.Sprintf("%020d", nanos)
	mmr.Href = fmt.Sprintf("/mup/%s/mr/%s", parentID, id)
	mmr.LastUpdateTime = time.Unix(0, nanos).Unix()
	return id
}

// stampServerOwnedMirrorFields overwrites every field of a submitted
// MirrorUsagePoint that belongs to the server rather than to the client, and it
// is the ONE place that decides what "server-owned" means for this resource.
//
// POST /mup and PUT /mup/{id} both call it, on purpose. They are the two ways a
// client writes a whole MirrorUsagePoint, and a field one of them stamps while
// the other takes from the body is a field a client can set simply by choosing
// the other verb. Sharing the function makes that class of gap unreachable
// rather than merely absent today: adding a server-owned field here covers both
// routes, and there is no second stamping site to forget.
//
// id is the store key the record is written under, and the caller has already
// established that it is the key this (lfdi, mRID) pair resolves to. This
// function does not re-derive it, because on the PUT path the comparison
// between the derived key and the requested one is a REJECTION decision, not a
// stamping one, and folding it in here would let a mismatch be silently
// corrected into a rename.
//
// The fields, and why each is the server's:
//
//   - DeviceLFDI is an identity claim, so a client must never be able to assert
//     one. It is taken from the caller's certificate identity, which for a PUT
//     is the same value the rule (e) gate already compared against the stored
//     record, so the record cannot be handed to a device the gate did not
//     authorise.
//
//   - PostRate is a rate the SERVER is being asked to absorb. sep.xsd:6485-6487
//     grants the server both verbs, "add or modify", so a configured server
//     value wins over a client preference; see PostRateProvider. This is not
//     the same kind of override as DeviceLFDI: the client posting into the
//     mirror does not know the server's ingest budget, and a mirror whose rate
//     the server did not agree to is a rate the server cannot plan for. Applying
//     it on the overwrite paths as well as on create is deliberate. If the stamp
//     applied only to create, a client that lost the rate war on its first POST
//     could win it back by re-POSTing or PUTting the same mRID, since both write
//     the new data over the existing record; the overwrite would persist an
//     un-stamped client value and silently revert the server's configured rate.
//     An ingest-budget policy the server cannot make survive a rewrite is not a
//     policy it can rely on.
//
//     A nil provider, or one that reports no configured rate, is a no-op:
//     PostRate keeps whatever the client submitted in THIS request, not whatever
//     was previously stored, consistent with rule (a)(4)'s full write-over
//     semantics. That keeps an unconfigured server byte-for-byte identical to
//     its pre-change behaviour instead of silently zeroing a stated preference.
//
//   - Href is minted by MirrorHref from the store key, so the stored self href,
//     the Location header on 201 and the Location header on 204 cannot drift
//     into three different strings for one resource.
//
//   - Each inline MirrorMeterReading's href and lastUpdateTime, matching what
//     HandlePostMirrorMeterReading does for the out-of-band route. Without this
//     a client's href and lastUpdateTime are stored and re-served verbatim on
//     GET /mup, which is unscoped.
//
// The per-element nanos offset comes from one clock read plus the index rather
// than a fresh time.Now() per element: on a coarse monotonic clock repeated
// reads inside a loop can return the same nanosecond, which would mint
// colliding hrefs for distinct readings. Distinct ids are required because they
// are the store's ordering keys.
//
// No response body observes the stamp directly: 201 carries none (rule (a)(3))
// and 204 carries none (rule (a)(4)), so a client learns its actual postRate
// only from a follow-up GET /mup/{id}. The stamp still has to happen before
// storage, because GET only ever echoes what was persisted.
func stampServerOwnedMirrorFields(mup *sep2.MirrorUsagePoint, id, lfdi string, postRateProvider PostRateProvider) {
	mup.DeviceLFDI = lfdi

	if postRateProvider != nil {
		if rate, ok := postRateProvider(lfdi); ok {
			// Bind to a fresh local: taking the address of the per-request
			// `rate` is fine, while pointing at any shared policy storage would
			// alias one value across every mirror.
			r := rate
			mup.PostRate = &r
		}
	}

	mup.Href = MirrorHref(id)

	baseNanos := time.Now().UnixNano()
	for i := range mup.MirrorMeterReading {
		stampMirrorMeterReading(&mup.MirrorMeterReading[i], id, baseNanos+int64(i))
	}
}

// stripMirrorMeterReadings returns a copy of mup with MirrorMeterReading
// omitted, for serving on GET.
//
// IEEE 2030.5-2018 section 10.11.3 rule (c): a GET of the MirrorUsagePoint
// "SHALL return a resource with only the first level elements (i.e.,
// sub-elements and collections are not included)." MirrorMeterReading is
// minOccurs="0" maxOccurs="unbounded" on MirrorUsagePoint (sep.xsd:6472),
// so it is a collection, not a first-level scalar, and omitting it on
// GET is spec-mandated, not merely a workaround for the ReadingType/mRID
// defect. This is a serving-only change: storage is untouched, so a POST
// still stores whatever MirrorMeterReading children the client submitted.
//
// s.Get and s.List (see pkg/store/memory) already return independent
// copies, so mutating the returned value's slice field here does not
// affect the stored record.
func stripMirrorMeterReadings(mup sep2.MirrorUsagePoint) sep2.MirrorUsagePoint {
	mup.MirrorMeterReading = nil
	return mup
}

// authorizeMirrorOwner resolves the caller's certificate-derived LFDI and
// confirms the caller is the client that CREATED the MirrorUsagePoint at id.
//
// IEEE 2030.5-2018 section 10.11.3 rule (e): "The Metering Mirror server
// SHOULD only accept POSTs to a given MirrorUsagePoint from the client that
// created the mirror."
//
// The scope key is the CREATOR, not the device whose readings the mirror
// describes. HandleCreateMirrorUsagePoint stamps MirrorUsagePoint.DeviceLFDI
// from the caller's identity, overriding whatever the client claimed in the
// body, so the stored DeviceLFDI IS the creator's identity and comparing the
// caller against it implements rule (e) directly rather than by proxy.
//
// This composes with CSIP aggregators, which is the case a naive
// one-mirror-per-certificate rule would break. An aggregator acting for many
// DERs legitimately creates many mirrors; each is stamped with the
// aggregator's own LFDI at creation, so the aggregator retains access to all
// of them. Nothing here assumes a single mirror per certificate.
//
// The check lives in core rather than in a consumer's middleware because core
// owns protocol behavior: /mup is not /edev-scoped, so no path-shaped ACL rule
// a server writes can express "the creator of this record", which is a fact
// only the stored resource carries. An authorization rule enforced solely in a
// consumer is a rule core cannot guarantee, and that is exactly the shape that
// lets one route drift out of compliance with its sibling.
//
// Fails closed on every indeterminate case: a nil provider, no identity, an
// empty caller LFDI, or a stored record carrying no DeviceLFDI. A mirror with
// no recorded creator has nobody who can claim it, so it accepts no writes and
// serves no reads; treating an empty stored DeviceLFDI as "matches anyone"
// would be the unsafe fallback that converts a missing value into open access.
//
// Denial status is 403, not 404, on both the write and the read path:
//
//   - 403 is "authenticated but not authorized", which is precisely the
//     condition here, and it matches the ownership precedent already set by
//     registration.go for GET /edev/{id}/rg.
//   - 404 would be the choice if denial had to avoid confirming the resource
//     exists, but it buys nothing today: GET /mup is a server-wide list that
//     already enumerates every MirrorUsagePoint to every authenticated client
//     (deliberately, per 6.2.3.1). Masking existence on one route while the
//     adjacent list discloses it is theater. If the list is ever scoped per
//     client, revisit 404 for the read path at the same time so the two stay
//     consistent.
//
// On denial the response body is a fixed plain-text string. It never echoes
// the request body, the stored record, or either LFDI, so a probe learns
// nothing beyond "denied".
//
// On success the already-fetched record is returned, so callers need not
// re-read the store, together with the CALLER's certificate-derived LFDI.
//
// Returning the caller LFDI is what lets PUT derive server-owned fields from
// the certificate without asking the provider a second time. Two reads of the
// same provider inside one request are two chances to disagree, and the value
// this gate compared against the stored record is by definition the one the
// rest of the request must use: any other value would be authorised by a check
// that never saw it.
func authorizeMirrorOwner(
	w http.ResponseWriter,
	r *http.Request,
	s store.ResourceStore[sep2.MirrorUsagePoint],
	lfdiProvider LFDIProvider,
	id string,
) (sep2.MirrorUsagePoint, string, bool) {
	var zero sep2.MirrorUsagePoint

	if lfdiProvider == nil {
		// A consumer that wired no identity source gets a closed door, not a
		// nil-func panic and not an open one.
		log.Printf("mup: nil LFDIProvider, denying (path=%s)", r.URL.Path)
		http.Error(w, "identity required", http.StatusForbidden)
		return zero, "", false
	}

	lfdi, ok := lfdiProvider(r.Context())
	if !ok || lfdi == "" {
		http.Error(w, "identity required", http.StatusForbidden)
		return zero, "", false
	}

	mup, err := s.Get(r.Context(), id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			http.Error(w, "mirror usage point not found", http.StatusNotFound)
			return zero, "", false
		}
		srverr.Internal(w, r, fmt.Errorf("the ownership lookup failed: %w", err))
		return zero, "", false
	}

	if mup.DeviceLFDI == "" || mup.DeviceLFDI != lfdi {
		http.Error(w, "forbidden", http.StatusForbidden)
		return zero, "", false
	}

	return mup, lfdi, true
}

// BuildMirrorUsagePointList constructs a MirrorUsagePointList from store results.
func BuildMirrorUsagePointList(href string, result store.ListResult[sep2.MirrorUsagePoint], pollRate uint32) sep2.MirrorUsagePointList {
	items := make([]sep2.MirrorUsagePoint, len(result.Items))
	for i, mup := range result.Items {
		items[i] = stripMirrorMeterReadings(mup)
	}
	return sep2.MirrorUsagePointList{
		ListResource: sep2.ListResource{
			SubscribableResource: sep2.SubscribableResource{
				Resource: sep2.Resource{Href: href},
			},
			All:      result.All,
			Results:  result.Results,
			PollRate: pollRate,
		},
		MirrorUsagePoint: items,
	}
}

// mirrorIDBytes is the length in bytes of the derived MirrorUsagePoint id,
// rendered as 2*mirrorIDBytes uppercase hex characters. 16 bytes matches the
// width of a sep2 hexBinary128 mRID, so a derived id is indistinguishable in
// shape from the identifiers already carried on this resource, and 128 bits of
// a SHA-256 digest keeps accidental collision between two owners' ids beyond
// any realistic device population.
const mirrorIDBytes = 16

// MirrorStoreID derives the server-assigned MirrorUsagePoint id from the
// identity of the client that creates it and the mRID that client supplied.
//
// MirrorUsagePoint identity is per owning device, not global. The mRID on a
// POSTed MirrorUsagePoint is client-supplied and nothing coordinates it across
// devices: observed in the field, nine devices holding nine distinct
// certificates POSTed only three distinct mRIDs between them. Keying the store
// on that mRID alone made the second device to use a given mRID collide with
// the first device's record, and the create path answered the collision with
// 200 plus a Location pointing at the FIRST device's MirrorUsagePoint. The
// second device would then post its readings into another device's resource.
// That is an ownership-isolation defect independent of the rule (e) gate: the
// gate compares a caller against a stored DeviceLFDI, and on the create path
// the collision happens before any such comparison is reachable.
//
// Deriving rather than scoping the store is deliberate. /mup carries no owner
// path segment, so a URL must identify exactly one resource server-wide:
//
//   - GET /mup/{id} resolves from the path alone. If two owners shared a URL,
//     the same URL would mean different resources to different callers.
//   - The MirrorMeterReading ScopedStore is keyed by the {id} path segment, so
//     two owners sharing an id would share a readings namespace, which is the
//     original defect moved one level down rather than fixed.
//   - GET /mup is a deliberately server-wide list over one flat store
//     (section 6.2.3.1), which a per-owner ScopedStore cannot serve: it has no
//     cross-parent iteration.
//
// Hashing rather than concatenating owner and mRID also bounds the URI:
//
//   - The output is always 32 characters, so Location is 37 characters
//     whatever the client sends. A client-supplied mRID is unvalidated in
//     length and charset (sep2.MirrorUsagePoint.MRID is a plain string), so
//     using it verbatim let a client size and shape the URI we hand back: a
//     4000-character mRID produced a 4005-character Location, and an mRID of
//     "../../etc/passwd" produced a Location with path traversal in it. The
//     EPRI reference client parses Location into a 127-byte buffer with no
//     length guard and dereferences the result unguarded, so an over-long or
//     unparseable Location crashes it.
//   - The owner LFDI stays out of the URL. GET /mup hands every mirror's href
//     to every authenticated client, so a concatenated id would publish which
//     certificate created which mirror in the path itself.
//
// The digest input is length-prefixed rather than separator-joined so no pair
// of (owner, mRID) values can be framed two ways into the same digest,
// whatever characters either string contains.
//
// The derivation is deterministic, which is what preserves the idempotent
// re-POST behavior: the same device POSTing the same mRID lands on the same
// id, takes the ErrAlreadyExists branch, and is served its own record.
func MirrorStoreID(ownerLFDI, clientMRID string) string {
	h := sha256.New()
	_, _ = io.WriteString(h, strconv.Itoa(len(ownerLFDI)))
	_, _ = io.WriteString(h, ":")
	_, _ = io.WriteString(h, ownerLFDI)
	_, _ = io.WriteString(h, strconv.Itoa(len(clientMRID)))
	_, _ = io.WriteString(h, ":")
	_, _ = io.WriteString(h, clientMRID)
	sum := h.Sum(nil)
	return strings.ToUpper(hex.EncodeToString(sum[:mirrorIDBytes]))
}

// MirrorHref returns the canonical MirrorUsagePoint href for a store id.
// One function mints the href so the stored Resource.Href, the Location header
// on 201, and the Location header on the ErrAlreadyExists branch cannot drift
// into three different strings for one resource.
func MirrorHref(id string) string {
	return "/mup/" + id
}

// HandleCreateMirrorUsagePoint returns a handler for POST /mup.
// Inverters create MirrorUsagePoints to register for metering data reporting.
// lfdiProvider extracts the client LFDI from the request context; the server
// passes a closure over auth.GetIdentity so that the auth package does not
// become a dependency of core.
//
// The created resource's id is derived from the caller's certificate identity
// and its mRID together (see MirrorStoreID), so two devices POSTing the same
// mRID each get their own MirrorUsagePoint and neither is ever handed the
// other's Location.
//
// postRateProvider supplies the server's preferred MirrorUsagePoint.postRate
// for the creating client (see PostRateProvider). Nil, or a provider that
// answers false, leaves the client's own postRate exactly as submitted, so a
// server that configures no rate behaves precisely as it did before this
// parameter existed. The stamp applies identically on the create path and on
// the rule (a)(4) overwrite path a re-POST of the same mRID takes; see the
// stamping site below for why the overwrite path is not exempted.
func HandleCreateMirrorUsagePoint(s store.ResourceStore[sep2.MirrorUsagePoint], lfdiProvider LFDIProvider, postRateProvider PostRateProvider) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			encoding.MethodNotAllowed(w, "POST")
			return
		}

		lfdi, ok := lfdiProvider(r.Context())
		if !ok || lfdi == "" {
			// An empty-but-present identity would key every such caller onto
			// one derived id, collapsing them back into the shared-record
			// defect this handler exists to prevent. Fail closed instead.
			http.Error(w, "identity required", http.StatusForbidden)
			return
		}

		body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if err != nil {
			http.Error(w, "read body failed", http.StatusBadRequest)
			return
		}

		var mup sep2.MirrorUsagePoint
		if err := xml.Unmarshal(body, &mup); err != nil {
			http.Error(w, "invalid XML: "+err.Error(), http.StatusBadRequest)
			return
		}

		// IEEE 2030.5-2018 section 10.11.3 rule (a)(1): the POST "SHALL
		// contain at least... the MirrorUsagePoint mRID". IdentifiedObject.mRID
		// (sep.xsd:5331) is minOccurs="1", inherited by MirrorUsagePoint via
		// UsagePointBase (sep.xsd:6472-6477, 6571-6576); the EPRI reference
		// client's own generated schema table (se_schema.c:428) carries
		// .min=1 on the MirrorUsagePoint mRID entry directly, not merely by
		// inheritance. A client that omits mRID has by definition given the
		// server nothing to dedupe future POSTs against (rule (a)(4) can
		// never be satisfied for it) and no identity it can later
		// re-address (rule (c) and the mandatory PUT/DELETE /mup/{id}
		// presume an addressable resource), so this is refused outright
		// rather than papered over with a synthetic per-request key.
		if mup.MRID == "" {
			http.Error(w, "MirrorUsagePoint mRID is required", http.StatusBadRequest)
			return
		}

		// The resource identity is (creating device, client mRID).
		id := MirrorStoreID(lfdi, mup.MRID)

		// Every server-owned field is stamped in one place, shared with PUT.
		// The stamp runs once here, on the same mup value the ErrAlreadyExists
		// branch below reuses verbatim for its s.Update call, so it lands on
		// BOTH the create path and the rule (a)(4) overwrite path a re-POST of
		// the same mRID takes. See stampServerOwnedMirrorFields for why each
		// field is server-owned and why the overwrite path is not exempted.
		stampServerOwnedMirrorFields(&mup, id, lfdi, postRateProvider)

		// sep.xsd carries MirrorMeterReading inline on MirrorUsagePoint
		// (sep2.MirrorUsagePoint.MirrorMeterReading), not via a link
		// element; the schema has no MirrorMeterReadingListLink type at
		// all. The POST /mup/{id}/mr endpoint below remains the
		// out-of-band route clients use to add readings; its target
		// path is a fixed convention documented on MirrorUsagePoint,
		// not carried in the resource body.

		if err := s.Create(r.Context(), id, mup); err != nil {
			if errors.Is(err, store.ErrAlreadyExists) {
				// The caller already holds a mirror under this mRID, or
				// another goroutine created it for the same caller between
				// this Create and the Get below. If the record was deleted in
				// that window, return 5xx rather than a zero-value 200
				// (silent data loss).
				existing, getErr := s.Get(r.Context(), id)
				if getErr != nil {
					srverr.InternalMessage(w, r, "registration race", fmt.Errorf("re-read after ErrAlreadyExists: %w", getErr))
					return
				}

				// Ownership is re-checked rather than inferred from the
				// derivation. Per-owner keying already means only this
				// caller's own record can occupy this id, but the store is
				// also writable by a consumer that seeds it directly, and
				// handing a caller a resource it does not own is precisely
				// the defect being fixed. Two independent barriers, not one.
				if existing.DeviceLFDI == "" || existing.DeviceLFDI != lfdi {
					log.Printf("mup: create collided with a record owned by another device (id=%q)", id)
					http.Error(w, "forbidden", http.StatusForbidden)
					return
				}

				// IEEE 2030.5-2018 section 10.11.3 rule (a)(4): "...the new
				// data SHALL be written over the existing MirrorUsagePoint."
				// mup already carries this POST's data with every
				// server-owned field stamped the same way the create path
				// stamps it: DeviceLFDI from the caller's certificate (not
				// the body, set above), Href derived from the same id this
				// (owner, mRID) pair always resolves to, and any inline
				// MirrorMeterReading elements already re-stamped with
				// fresh server-owned href/lastUpdateTime values. Persisting
				// mup as-is overwrites every other field verbatim from what
				// the client just sent, mRID and Description included.
				//
				// This is a full write-over, not a merge: an inline
				// MirrorMeterReading this POST omits is cleared from the
				// stored record, matching "written over" rather than
				// "append". Rule (a)(4) does not describe a partial-update
				// mode, and a merge would leave stale inline readings that
				// this POST deliberately dropped served back on the next
				// GET.
				//
				// The out-of-band POST /mup/{id}/mr route persists into
				// mmrStore, a separate collection this s.Update call never
				// touches. Rule (a)(4)'s "written over" language is scoped
				// to the MirrorUsagePoint resource itself, and neither
				// source cited for this fix speaks to the out-of-band
				// readings collection; rather than silently discard data
				// outside the rule's stated scope, this overwrite leaves
				// mmrStore untouched.
				if err := s.Update(r.Context(), id, mup); err != nil {
					srverr.Internal(w, r, fmt.Errorf("overwrite an existing mirror: %w", err))
					return
				}

				// Location is minted from the id just resolved, never
				// echoed from storage. An empty Location is not merely
				// wrong: the EPRI client takes strlen of it with no guard
				// and dereferences the NULL that its failed URI parse
				// returns.
				//
				// No representation is written on 204: the EPRI client's
				// se_receive (se_connection.c) schema-parses ANY response
				// body ahead of process_response regardless of status code,
				// so a 204 carrying content is both non-conformant and a
				// needless parse surface the reference client never reads.
				w.Header().Set("Location", mup.Href)
				w.WriteHeader(http.StatusNoContent)
				return
			}
			srverr.Internal(w, r, err)
			return
		}

		// IEEE 2030.5-2018 section 10.11.3 rule (a)(3) mandates only that
		// the Location header carry the new MirrorUsagePoint URI on 201;
		// it does not require a representation in the body, and the text
		// is identical in the 2018 and 2023 editions. The EPRI client's
		// process_response (retrieve.c) never reads a POST response body
		// via se_body(): for HTTP_POST it reads the Location header and
		// issues a fresh GET to fetch the created resource. Independent
		// of that, se_receive (se_connection.c) attempts to schema-parse
		// ANY response body before process_response is even reached, POST
		// included, so serving a body here re-creates a second parse
		// point the client does not use for anything. Dropping the body
		// removes that surface entirely rather than relying on item 1 and
		// item 2 to keep it well-formed forever.
		w.Header().Set("Location", mup.Href)
		w.WriteHeader(http.StatusCreated)
	}
}

// HandleMirrorUsagePoint returns a handler for GET /mup/{id}.
//
// Scoped to the creating client, same rule as the POST routes: see
// authorizeMirrorOwner. Rule (e) governs POSTs only, and the WADL marks this
// GET Optional (section 4.2 item (c) makes those modes normative), so scoping
// it costs nothing in conformance while metering readings are customer data
// that an unscoped GET hands to any authenticated peer.
//
// Rule (c) still holds on the owner's own record: the response carries only
// first-level elements, MirrorMeterReading children stripped.
func HandleMirrorUsagePoint(s store.ResourceStore[sep2.MirrorUsagePoint], lfdiProvider LFDIProvider) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			encoding.MethodNotAllowed(w, "GET, HEAD")
			return
		}

		mup, _, ok := authorizeMirrorOwner(w, r, s, lfdiProvider, r.PathValue("id"))
		if !ok {
			return
		}

		stripped := stripMirrorMeterReadings(mup)
		encoding.WriteXML(w, http.StatusOK, &stripped)
	}
}

// HandlePutMirrorUsagePoint returns a handler for PUT /mup/{id}.
//
// PUT on the MirrorUsagePoint is mode M in the WADL (sep_wadl.xml:2303), and
// until this handler existed a client following the Location header POST /mup
// handed it, using a method the standard marks Mandatory, was refused. That is
// the advertised-but-unserved class in its strongest form: the server named the
// URI itself.
//
// # The ordering is the security property
//
// The section 10.11.3 rule (e) ownership gate runs FIRST, before a single byte
// of the request body is read. That ordering is not an optimisation. If the
// body were parsed first, an unauthorised caller would learn which bodies parse,
// which mRIDs resolve to this resource, and which do not, from the differing
// status codes and messages a parse produces: 400 for malformed XML, 400 for a
// missing mRID, 409 for an mRID belonging elsewhere. Every one of those is a
// distinguishable answer to a question the caller has no standing to ask. With
// the gate first, a denied caller's response is a function of its identity
// alone, so every body it can construct yields the identical response, which is
// what the tests assert rather than merely that both are refused.
//
// # Why a differing mRID is a rejection and not a rename
//
// MirrorStoreID derives the store key from the owner and the mRID TOGETHER (see
// MirrorStoreID for why), so the mRID is part of this resource's identity, not a
// mutable attribute of it. A PUT whose body carries a different mRID therefore
// does not describe an edit to the resource at {id}: it describes a DIFFERENT
// resource, living at a different key.
//
// Honouring it as a rename would be data corruption rather than a 4xx. The
// record at {id} would be left behind with nothing addressing it, since its key
// no longer matches the mRID it now claims to hold and no future request from
// the owner can derive that key again; and a second record would appear at the
// new key, so one mirror would have become two, one of them unreachable. The
// readings scoped under the old id would be stranded under a parent nothing can
// name. Refusing with 409 leaves the store exactly as it was, which the tests
// assert directly rather than inferring from the status code.
//
// The client that genuinely wants a mirror under a new mRID already has the
// route for it: POST /mup mints one and returns its Location, rule (a)(3).
//
// The response is 204 with a Location header, matching rule (a)(4)'s treatment
// of a write-over of an existing MirrorUsagePoint. No representation is served:
// the EPRI reference client's se_receive schema-parses ANY response body ahead
// of process_response regardless of status, so a body on a 204 is both
// non-conformant and a parse surface no client reads.
func HandlePutMirrorUsagePoint(
	s store.ResourceStore[sep2.MirrorUsagePoint],
	lfdiProvider LFDIProvider,
	postRateProvider PostRateProvider,
) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			encoding.MethodNotAllowed(w, "PUT")
			return
		}

		id := r.PathValue("id")

		// Rule (e), BEFORE the body. See the ordering note above: everything
		// below this line is reachable only by the client that created the
		// mirror, so no branch below can be used as an oracle by anyone else.
		_, lfdi, ok := authorizeMirrorOwner(w, r, s, lfdiProvider, id)
		if !ok {
			return
		}

		body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if err != nil {
			http.Error(w, "read body failed", http.StatusBadRequest)
			return
		}

		var mup sep2.MirrorUsagePoint
		if err := xml.Unmarshal(body, &mup); err != nil {
			http.Error(w, "invalid XML: "+err.Error(), http.StatusBadRequest)
			return
		}

		// mRID is minOccurs="1" on IdentifiedObject (sep.xsd:5331), inherited
		// by MirrorUsagePoint, and here it is load-bearing beyond that: without
		// it there is nothing to compare against the resource's own identity,
		// so the rename check below could not run at all. Refused rather than
		// treated as "unchanged", because inferring the mRID from the stored
		// record would make an absent mRID silently mean whatever the server
		// already had, and a client could then never tell a full replacement
		// from a partial one.
		if mup.MRID == "" {
			http.Error(w, "MirrorUsagePoint mRID is required", http.StatusBadRequest)
			return
		}

		// The identity check. Nothing has been written at this point and
		// nothing is written on this branch: the store is left byte-for-byte as
		// it was, which is the property that distinguishes a refusal from a
		// half-applied rename.
		//
		// The message names neither the submitted mRID nor the stored one. The
		// caller owns this resource, so it could learn both by other means, but
		// echoing request content into an error body is how a reflected value
		// ends up somewhere it was not expected.
		if derived := MirrorStoreID(lfdi, mup.MRID); derived != id {
			http.Error(w, "MirrorUsagePoint mRID does not identify this resource", http.StatusConflict)
			return
		}

		// Identical stamping to the create path, by construction: one function,
		// both routes. DeviceLFDI comes from the certificate identity the gate
		// above already matched against the stored record, never from the body.
		stampServerOwnedMirrorFields(&mup, id, lfdi, postRateProvider)

		// A full write-over, not a merge, matching rule (a)(4). An inline
		// MirrorMeterReading this PUT omits is cleared from the stored record.
		// The out-of-band readings in mmrStore are a separate collection and are
		// deliberately untouched, exactly as the POST overwrite path leaves
		// them: rule (a)(4)'s "written over" language is scoped to the
		// MirrorUsagePoint resource, and discarding data outside that scope on a
		// PUT would be a silent deletion the client did not ask for.
		if err := s.Update(r.Context(), id, mup); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				// The record was deleted between the gate's Get and this
				// Update. Reported as 404 rather than recreated: Update never
				// creates, and re-creating here would resurrect a mirror whose
				// owner had just retired it.
				log.Printf("mup: PUT lost a race with a delete for id=%q", id)
				http.Error(w, "mirror usage point not found", http.StatusNotFound)
				return
			}
			srverr.Internal(w, r, err)
			return
		}

		w.Header().Set("Location", mup.Href)
		w.WriteHeader(http.StatusNoContent)
	}
}

// HandleDeleteMirrorUsagePoint returns a handler for DELETE /mup/{id}.
//
// DELETE on the MirrorUsagePoint is mode M in the WADL (sep_wadl.xml:2323) and
// declares a sep:MirrorUsagePoint response representation (sep_wadl.xml:2325),
// so the deleted record is served back rather than answered with a bare 204.
// The served record is STRIPPED of its MirrorMeterReading children, per section
// 10.11.3 rule (c): a MirrorUsagePoint representation carries only first-level
// elements. The same stripping the GET path applies is applied here, through the
// same function, so the document a client receives at deletion is the same shape
// as the one it received while the mirror was alive.
//
// The record served is the one captured by the ownership gate BEFORE the delete,
// which is the only correct source: reading it back afterwards would find
// nothing, and reconstructing it from the request would serve the client its own
// input rather than what the server actually held.
//
// # The cascade
//
// MirrorMeterReadings are scoped under the MirrorUsagePoint id. Deleting the
// parent without them leaves a collection that nothing can address through the
// protocol, because /mup/{id}/mr/{}'s only route to it is through an id that no
// longer resolves, while the readings themselves stay resident. Worse, the ids
// are derived from (owner, mRID), so an owner that re-creates a mirror under the
// SAME mRID lands on the SAME key and would inherit the previous mirror's
// readings as if they were its own: a device would be served metering data it
// never posted, under a resource it believes it just created.
//
// The children go first, then the parent. On a failure to remove the children
// the parent is left in place and the request fails, so the client sees a
// resource that still exists and can retry; the reverse order would answer a
// partial failure with a deleted parent and surviving orphans, which is the
// exact state this cascade exists to prevent and which no client could detect.
//
// A nil readings store means the readings collection was never wired at all, so
// there is nothing that could be orphaned and the cascade is vacuous rather than
// skipped. It is not treated as an error, because a consumer that serves the
// MirrorUsagePoint function set without the out-of-band readings route is a
// deployment choice, not an indeterminate check.
//
// A readings store that IS wired but cannot cascade is the opposite case and is
// treated as an error: see [parentCascader].

// parentCascader removes a whole parent collection in one operation and reports
// how many resources went with it.
//
// It is declared HERE, at the consumer, rather than in pkg/store, because it is
// not part of the store contract and should not become part of it by accident.
// The contract addresses resources as (parent, id) pairs and offers no key
// enumeration, so a cascade cannot be expressed as a loop over it: there is no
// way to ask which ids live under a parent, and a loop would not be atomic even
// if there were. That makes bulk parent removal a capability an implementation
// either has or does not, which is exactly what an optional interface asserted
// at the point of use is for.
//
// An implementation that does not provide it is not silently tolerated. See the
// fail-closed branch in [HandleDeleteMirrorUsagePoint]: a store that cannot
// cascade blocks the parent delete rather than completing it and orphaning the
// children.
type parentCascader interface {
	DeleteParent(ctx context.Context, parentID string) (uint32, error)
}

func HandleDeleteMirrorUsagePoint(
	s store.ResourceStore[sep2.MirrorUsagePoint],
	mmrStore store.ScopedStore[sep2.MirrorMeterReading],
	lfdiProvider LFDIProvider,
) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			encoding.MethodNotAllowed(w, "DELETE")
			return
		}

		id := r.PathValue("id")

		// Rule (e), before anything is removed. A DELETE has no body to leak
		// through, but the gate is still first for the same reason the read and
		// write paths put it first: no branch of this handler is reachable by a
		// caller that does not own the record.
		mup, _, ok := authorizeMirrorOwner(w, r, s, lfdiProvider, id)
		if !ok {
			return
		}

		if !store.IsAbsent(mmrStore) {
			cascader, ok := mmrStore.(parentCascader)
			if !ok {
				// Fail closed, and leave the parent in place. The readings
				// store is wired, so a collection under this id may exist and
				// would be orphaned by a delete this handler cannot follow
				// with a cascade. Answering 204 here would report a clean
				// deletion while leaving exactly the state the cascade exists
				// to prevent, and the client could not tell.
				srverr.Internal(w, r, fmt.Errorf("the readings store (%T) cannot cascade a parent delete, "+
					"so the MirrorUsagePoint is left in place", mmrStore))
				return
			}
			removed, err := cascader.DeleteParent(r.Context(), id)
			if err != nil {
				// The parent is deliberately still here. Surfacing the failure
				// with the resource intact is recoverable; deleting it anyway
				// would leave orphans no client could see or clean up.
				srverr.Internal(w, r, fmt.Errorf("the cascade delete of the readings under this parent failed, "+
					"so the MirrorUsagePoint is left in place: %w", err))
				return
			}
			if removed > 0 {
				log.Printf("mup: DELETE id=%q cascaded to %d MirrorMeterReading(s)", id, removed)
			}
		}

		if err := s.Delete(r.Context(), id); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				// Another request deleted it between the gate's Get and here.
				// The cascade above has already run, which is what that other
				// request would have done too, so the end state is the intended
				// one and only the reporting differs.
				log.Printf("mup: DELETE lost a race with another delete for id=%q", id)
				http.Error(w, "mirror usage point not found", http.StatusNotFound)
				return
			}
			srverr.Internal(w, r, err)
			return
		}

		stripped := stripMirrorMeterReadings(mup)
		encoding.WriteXML(w, http.StatusOK, &stripped)
	}
}

// sep2Namespace is the IEEE 2030.5 XML namespace a request document must
// carry on its root element.
//
// Requiring it is not new strictness. Every sep2 struct in this package tags
// XMLName with this namespace, and encoding/xml rejects a namespace-less
// document against such a tag ("expected element <X> in name space ... but
// have no name space"), so the single-reading path already refused documents
// without it. Naming the constant here keeps the explicit root-element check
// below exactly as strict as the unmarshal it dispatches to, rather than
// letting the two disagree about what a valid document is.
const sep2Namespace = "urn:ieee:std:2030.5:ns"

// errNoRootElement reports a body that parsed but contained no element at all
// (empty, or comments and processing instructions only).
var errNoRootElement = errors.New("document has no root element")

// errEmptyReadingList reports a MirrorMeterReadingList carrying no
// MirrorMeterReading children.
var errEmptyReadingList = errors.New("MirrorMeterReadingList contains no MirrorMeterReading")

// rootElementName returns the qualified name of body's root element.
//
// It reads tokens rather than unmarshalling so the caller can decide which
// type to decode into BEFORE any decode is attempted. The name is read from
// the document itself, so no struct tag has to be trusted to be the thing
// that rejects a mismatch.
func rootElementName(body []byte) (xml.Name, error) {
	dec := xml.NewDecoder(bytes.NewReader(body))
	for {
		tok, err := dec.Token()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return xml.Name{}, errNoRootElement
			}
			return xml.Name{}, err
		}
		if se, ok := tok.(xml.StartElement); ok {
			return se.Name, nil
		}
	}
}

// decodeMirrorMeterReadings decodes a POST body into the readings it carries.
//
// IEEE 2030.5-2018 Annex A.4.4.11 lists TWO request representations for POST
// on /mup/{id1}: MirrorMeterReading and MirrorMeterReadingList. Both are
// accepted here and both yield the same []MirrorMeterReading, so a batching
// client and a one-at-a-time client take an identical path through stamping,
// ownership, and storage. A batch is not a way around any rule that applies
// to a single reading.
//
// Discrimination is by explicit root-element name, decided before any
// unmarshal is attempted. That ordering is the point. The obvious alternative,
// "unmarshal into one type and fall back to the other on error", is unsafe as
// a general technique because encoding/xml is happy to produce a zero value
// from a document it did not really match: a list decoded as a single would be
// an empty single, which stores nothing and looks like success. Here the type
// is chosen from the document's own root name and nothing else, so a
// MirrorMeterReadingList is never handed to the single-reading decoder and a
// MirrorMeterReading is never handed to the list decoder. A root that is
// neither is refused outright rather than defaulting to either.
//
// (encoding/xml's XMLName tag matching would in fact catch a crossed decode
// today, because both sep2 types tag XMLName with a fixed element name and a
// mismatch is an error rather than a zero value. That is a second barrier, not
// the first one: it holds only as long as nobody drops or loosens an XMLName
// tag, and it is asserted directly in the tests. The dispatch above does not
// depend on it.)
//
// An empty MirrorMeterReadingList is an error, not a no-op success. A 201 is
// a claim that something was created at Location, and an empty batch creates
// nothing to point Location at. Serving 201 with an empty Location is
// specifically harmful: the EPRI reference client takes strlen of the header
// with no guard and dereferences the result of its failed URI parse.
//
// Error messages never echo client-supplied element names or namespaces back
// in the response body, so a probe learns only that its document was the wrong
// shape.
func decodeMirrorMeterReadings(body []byte) ([]sep2.MirrorMeterReading, error) {
	root, err := rootElementName(body)
	if err != nil {
		return nil, fmt.Errorf("invalid XML: %w", err)
	}

	if root.Space != sep2Namespace {
		return nil, errors.New("invalid XML: root element is not in the IEEE 2030.5 name space")
	}

	switch root.Local {
	case "MirrorMeterReading":
		var mmr sep2.MirrorMeterReading
		if err := xml.Unmarshal(body, &mmr); err != nil {
			return nil, fmt.Errorf("invalid XML: %w", err)
		}
		return []sep2.MirrorMeterReading{mmr}, nil

	case "MirrorMeterReadingList":
		var list sep2.MirrorMeterReadingList
		if err := xml.Unmarshal(body, &list); err != nil {
			return nil, fmt.Errorf("invalid XML: %w", err)
		}
		if len(list.MirrorMeterReading) == 0 {
			return nil, errEmptyReadingList
		}
		return list.MirrorMeterReading, nil

	default:
		return nil, errors.New("invalid XML: expected a MirrorMeterReading or MirrorMeterReadingList root element")
	}
}

// HandlePostMirrorMeterReading returns a handler for POST /mup/{id}/mr and,
// mounted identically, POST /mup/{id}. Inverters POST metering data (power,
// energy, etc.) to this endpoint.
//
// Both routes share this one handler so the ownership gate, the href shape,
// and the server-owned field stamping cannot drift between them. Enforcing
// rule (e) on one route and not its sibling is the failure shape this
// arrangement exists to prevent.
//
// Ownership: the caller's LFDI must equal the parent MirrorUsagePoint's stored
// DeviceLFDI, which is the LFDI of the client that created the mirror. See
// authorizeMirrorOwner for the rule, the CSIP aggregator case, and the choice
// of 403 over 404. The gate runs before the body is read, so it covers a batch
// exactly as it covers a single reading: an unauthorized caller's payload is
// never parsed, and no member of its batch is ever stamped or stored.
//
// The body may be a single MirrorMeterReading or a MirrorMeterReadingList,
// both of which Annex A.4.4.11 lists as request representations for POST on
// /mup/{id1}. See decodeMirrorMeterReadings for how the two are told apart.
// Because this one handler serves both mounted routes, /mup/{id}/mr accepts
// the list form too; splitting the body grammar between the two routes would
// reintroduce exactly the drift that sharing the handler exists to prevent.
func HandlePostMirrorMeterReading(
	mupStore store.ResourceStore[sep2.MirrorUsagePoint],
	mmrStore store.ScopedStore[sep2.MirrorMeterReading],
	lfdiProvider LFDIProvider,
) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			encoding.MethodNotAllowed(w, "POST")
			return
		}

		parentID := r.PathValue("id")

		// Ownership gate: resolves identity, confirms the parent exists, and
		// confirms the caller created it. Runs before the body is read so an
		// unauthorized caller's payload is never parsed, let alone stored.
		if _, _, ok := authorizeMirrorOwner(w, r, mupStore, lfdiProvider, parentID); !ok {
			return
		}

		body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if err != nil {
			http.Error(w, "read body failed", http.StatusBadRequest)
			return
		}

		readings, err := decodeMirrorMeterReadings(body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		// Generate time-based IDs for sorted ordering, and override the
		// client-supplied href and lastUpdateTime on EVERY reading. Shared
		// with the inline MirrorUsagePoint.MirrorMeterReading path so all
		// three sites mint the same href shape.
		//
		// One clock read plus the index, not a fresh time.Now() per element:
		// on a coarse monotonic clock repeated reads inside a loop can return
		// the same nanosecond, which would mint colliding ids for distinct
		// readings. The id is the store's ordering key, so distinct ids are
		// required, and deriving lastUpdateTime from the same instant keeps a
		// record's timestamp and its ordering key from disagreeing.
		baseNanos := time.Now().UnixNano()
		ids := make([]string, len(readings))
		for i := range readings {
			ids[i] = stampMirrorMeterReading(&readings[i], parentID, baseNanos+int64(i))
		}

		// All-or-nothing: a batch either lands whole or lands not at all.
		// A half-stored batch is a state no client can describe: the response
		// says 500 while some readings are queryable and some are not, and
		// nothing in the reply says which. The store exposes no transaction,
		// so this is a compensating rollback rather than an atomic write:
		// every reading THIS request created is deleted before the error is
		// returned. A rollback delete that itself fails is logged rather than
		// swallowed, because at that point the invariant is genuinely broken
		// and the operator needs to know.
		for i := range readings {
			if err := mmrStore.Create(r.Context(), parentID, ids[i], readings[i]); err != nil {
				rollbackErr := fmt.Errorf("store reading %d of %d: %w", i+1, len(readings), err)
				for _, done := range ids[:i] {
					if delErr := mmrStore.Delete(r.Context(), parentID, done); delErr != nil {
						// Reported through the same 500 line rather than a
						// separate one, so that the fact the batch is
						// partially stored cannot be read without the failure
						// that caused it.
						rollbackErr = fmt.Errorf("%w; and the rollback of an already-stored reading failed, "+
							"so the batch is partially stored: %w", rollbackErr, delErr)
					}
				}
				srverr.Internal(w, r, rollbackErr)
				return
			}
		}

		// readings is never empty: the single form yields exactly one and the
		// list form rejects an empty list, so Location always names a resource
		// this request actually created. For a batch it names the first, which
		// is both deterministic and the earliest in the id ordering the store
		// sorts by.
		w.Header().Set("Location", readings[0].Href)
		w.WriteHeader(http.StatusCreated)
	}
}

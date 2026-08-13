package metering

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2/encoding"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/srverr"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
)

// BuildUsagePointList constructs a UsagePointList.
func BuildUsagePointList(href string, result store.ListResult[sep2.UsagePoint], pollRate uint32) sep2.UsagePointList {
	return sep2.UsagePointList{
		ListResource: sep2.ListResource{
			SubscribableResource: sep2.SubscribableResource{
				Resource: sep2.Resource{Href: href},
			},
			All:      result.All,
			Results:  result.Results,
			PollRate: pollRate,
		},
		UsagePoint: result.Items,
	}
}

// BuildMeterReadingList constructs a MeterReadingList.
func BuildMeterReadingList(href string, result store.ListResult[sep2.MeterReading], pollRate uint32) sep2.MeterReadingList {
	return sep2.MeterReadingList{
		ListResource: sep2.ListResource{
			SubscribableResource: sep2.SubscribableResource{
				Resource: sep2.Resource{Href: href},
			},
			All:      result.All,
			Results:  result.Results,
			PollRate: pollRate,
		},
		MeterReading: result.Items,
	}
}

// BuildReadingList constructs a ReadingList.
func BuildReadingList(href string, result store.ListResult[sep2.Reading], pollRate uint32) sep2.ReadingList {
	return sep2.ReadingList{
		ListResource: sep2.ListResource{
			SubscribableResource: sep2.SubscribableResource{
				Resource: sep2.Resource{Href: href},
			},
			All:      result.All,
			Results:  result.Results,
			PollRate: pollRate,
		},
		Reading: result.Items,
	}
}

// HandleUsagePoint returns a handler for GET /upt/{uptId}.
func HandleUsagePoint(uptStore store.ResourceStore[sep2.UsagePoint]) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			encoding.MethodNotAllowed(w, "GET, HEAD")
			return
		}
		id := r.PathValue("uptId")
		upt, err := uptStore.Get(r.Context(), id)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				http.Error(w, "not found", http.StatusNotFound)
				return
			}
			srverr.Internal(w, r, err)
			return
		}
		encoding.WriteXML(w, http.StatusOK, &upt)
	}
}

// usagePointIDBytes is the length in bytes of a derived UsagePoint id,
// rendered as 2*usagePointIDBytes uppercase hex characters. 16 bytes matches
// the width of a sep2 hexBinary128 mRID, so a derived id is indistinguishable
// in shape from the identifiers already carried on this resource, and it makes
// "/upt/" + id a constant 37 characters, the same bound MirrorHref carries.
const usagePointIDBytes = 16

// Derivation domains. Each names a distinct id space, so a value hashed as one
// kind of id can never coincide with another kind's id.
const (
	usagePointIDDomain          = "upt"
	anonymousUsagePointIDDomain = "upt-anon"
)

// boundedID renders a fixed-width, bounded identifier from a domain tag and a
// value.
//
// Every part is length-prefixed, so no two (domain, value) pairs can frame
// into the same digest whatever characters either string contains.
func boundedID(domain, value string) string {
	h := sha256.New()
	for _, part := range []string{domain, value} {
		_, _ = io.WriteString(h, strconv.Itoa(len(part)))
		_, _ = io.WriteString(h, ":")
		_, _ = io.WriteString(h, part)
	}
	sum := h.Sum(nil)
	return strings.ToUpper(hex.EncodeToString(sum[:usagePointIDBytes]))
}

// UsagePointStoreID derives the server-assigned UsagePoint id from the
// client-supplied mRID.
//
// The reasoning is the URI-bounding half of MirrorStoreID's (see mirror.go),
// and only that half. sep2.UsagePoint.MRID is a plain string, unvalidated in
// length and charset, and POST /upt put it straight into the store key, into
// Resource.Href, into MeterReadingListLink.Href, and into the Location header.
// A 4000-character mRID therefore produced a 4005-character Location, and an
// mRID of "../../etc/passwd" produced a Location with path traversal in it.
// The EPRI reference client parses Location into a 127-byte buffer with no
// length guard and dereferences the result unguarded, so an over-long or
// unparseable Location crashes it. Hashing makes the output always 32
// characters and always hex, whatever the client sends.
//
// The other half of MirrorStoreID, scoping the id to the creating device's
// LFDI, has no counterpart here and is deliberately NOT invented. On /mup the
// owner is a real, stored fact: HandleCreateMirrorUsagePoint stamps
// MirrorUsagePoint.deviceLFDI from the caller's certificate, and rule (e)
// names the creating client as the scope for later POSTs. sep2.UsagePoint has
// no deviceLFDI or any other owner field (sep.xsd UsagePoint extends
// UsagePointBase extends IdentifiedObject: mRID, description, version,
// roleFlags, serviceCategoryKind, status), Annex A.4.4.1 and A.4.4.2 define
// /upt and /upt/{id1} with no owner path segment, and POST /upt is Optional
// rather than Mandatory. So what identity scopes a UsagePoint is not
// established by the standard, and a (owner, mRID) derivation here would
// invent an ownership model the resource does not have: it would put an
// unenforced owner into the store key while GET /upt/{uptId} still resolves
// from the path alone with no ownership check at all. Bounding is a fix;
// half an ownership model is a new defect. If multi-tenant /upt is ever
// specified, the owner half gets added here and to the read path together.
//
// The derivation is deterministic, which preserves the idempotent re-POST
// behavior: the same mRID lands on the same id, takes the ErrAlreadyExists
// branch, and is served the existing record.
func UsagePointStoreID(clientMRID string) string {
	return boundedID(usagePointIDDomain, clientMRID)
}

// UsagePointHref returns the canonical UsagePoint href for a store id.
// One function mints the href so the stored Resource.Href, the Location header
// on 201, and the Location header on the ErrAlreadyExists branch cannot drift
// into three different strings for one resource.
func UsagePointHref(id string) string {
	return "/upt/" + id
}

// HandleCreateUsagePoint returns a handler for POST /upt.
//
// Annex A.4.4.1 marks POST on /upt Optional. We support it, so it has to be
// correct; nothing obliges us to support it at all.
//
// The created resource's id is derived from the client's mRID (see
// UsagePointStoreID) rather than being that mRID, so the URI this handler
// mints and hands back is bounded in length and charset no matter what the
// client sends. There is no owner half to that derivation, and none is
// invented: see UsagePointStoreID for why.
//
// The store is taken as the store.ResourceStore interface rather than the
// concrete *memory.Store, matching HandleCreateMirrorUsagePoint. *memory.Store
// satisfies it, so every existing call site is unchanged; what it buys is that
// the ErrAlreadyExists-then-Get-fails path can be driven in a test at all, and
// that path is precisely the one whose swallowed error this change fixes.
func HandleCreateUsagePoint(uptStore store.ResourceStore[sep2.UsagePoint]) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			encoding.MethodNotAllowed(w, "POST")
			return
		}

		body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if err != nil {
			http.Error(w, "read body failed", http.StatusBadRequest)
			return
		}

		var upt sep2.UsagePoint
		if err := xml.Unmarshal(body, &upt); err != nil {
			http.Error(w, "invalid XML: "+err.Error(), http.StatusBadRequest)
			return
		}

		// A POST carrying no mRID gets a per-request id rather than a derived
		// one. Hashing the empty string would collapse every mRID-less POST
		// onto a single key, which is a collision this handler introduced
		// rather than a duplicate the client asked for. The anonymous domain
		// tag keeps that id space disjoint from the mRID-derived one, so no
		// client-supplied mRID can be chosen to land on a synthesized id.
		id := UsagePointStoreID(upt.MRID)
		if upt.MRID == "" {
			id = boundedID(anonymousUsagePointIDDomain, strconv.FormatInt(time.Now().UnixNano(), 10))
		}

		// Server-minted, never echoed from the client's body. The client's own
		// Resource.Href, if it sent one, is overwritten here.
		upt.Href = UsagePointHref(id)
		upt.MeterReadingListLink = &sep2.ListLink{Href: upt.Href + "/mr"}

		if err := uptStore.Create(r.Context(), id, upt); err != nil {
			if errors.Is(err, store.ErrAlreadyExists) {
				// The record was deleted between the Create above and this
				// Get, or the store failed. Either way there is no existing
				// resource to serve. The previous code discarded this error
				// and served a zero-value UsagePoint with Location: "", which
				// is silent data loss on the wire and, specifically, a
				// header the EPRI reference client strlen()s with no guard
				// before dereferencing its failed URI parse.
				existing, getErr := uptStore.Get(r.Context(), id)
				if getErr != nil {
					srverr.InternalMessage(w, r, "registration race", fmt.Errorf("re-read after ErrAlreadyExists: %w", getErr))
					return
				}

				// Location is minted from the id just resolved, never read
				// back from the stored record: a consumer that seeded the
				// store directly may have written any Href it liked, and that
				// value is not something this handler will hand to a client.
				w.Header().Set("Location", UsagePointHref(id))
				encoding.WriteXML(w, http.StatusOK, &existing)
				return
			}
			srverr.Internal(w, r, err)
			return
		}

		w.Header().Set("Location", upt.Href)
		encoding.WriteXML(w, http.StatusCreated, &upt)
	}
}

// HandleReadingType returns a handler for GET /rt/{id}.
func HandleReadingType(rtStore store.ResourceStore[sep2.ReadingType]) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			encoding.MethodNotAllowed(w, "GET, HEAD")
			return
		}
		id := r.PathValue("id")
		rt, err := rtStore.Get(r.Context(), id)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				http.Error(w, "not found", http.StatusNotFound)
				return
			}
			srverr.Internal(w, r, err)
			return
		}
		encoding.WriteXML(w, http.StatusOK, &rt)
	}
}

// BuildReadingTypeList constructs a ReadingTypeList.
func BuildReadingTypeList(href string, result store.ListResult[sep2.ReadingType], pollRate uint32) sep2.ReadingTypeList {
	return sep2.ReadingTypeList{
		ListResource: sep2.ListResource{
			SubscribableResource: sep2.SubscribableResource{
				Resource: sep2.Resource{Href: href},
			},
			All:      result.All,
			Results:  result.Results,
			PollRate: pollRate,
		},
		ReadingType: result.Items,
	}
}

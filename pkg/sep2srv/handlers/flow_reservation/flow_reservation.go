package flow_reservation

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2/encoding"
	coreresponse "github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/response"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/srverr"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
)

// Submitter is what HandlePostFlowReservationRequest needs to hand a newly
// stored request off for its answer: the deadline fallback (#666) today,
// and the operator's explicit answer once #670 wires an admin route to the
// same call. Satisfied by *internal/flowreservation.Queue; this package
// depends on the interface, not the concrete type, so a test can stub it.
type Submitter interface {
	// Submit enqueues the request stored under (edevID, frqID) for the
	// deadline fallback. createdAt is the Unix-second instant it was
	// stored, the same value written to frq.CreationTime.
	Submit(edevID, frqID string, frq sep2.FlowReservationRequest, createdAt int64)
}

// BuildFlowReservationRequestList constructs a FlowReservationRequestList.
func BuildFlowReservationRequestList(href string, result store.ListResult[sep2.FlowReservationRequest], pollRate uint32) sep2.FlowReservationRequestList {
	return sep2.FlowReservationRequestList{
		ListResource: sep2.ListResource{
			SubscribableResource: sep2.SubscribableResource{
				Resource: sep2.Resource{Href: href},
			},
			All:      result.All,
			Results:  result.Results,
			PollRate: pollRate,
		},
		FlowReservationRequest: result.Items,
	}
}

// BuildFlowReservationResponseList constructs a FlowReservationResponseList.
func BuildFlowReservationResponseList(href string, result store.ListResult[sep2.FlowReservationResponse], pollRate uint32) sep2.FlowReservationResponseList {
	return sep2.FlowReservationResponseList{
		ListResource: sep2.ListResource{
			SubscribableResource: sep2.SubscribableResource{
				Resource: sep2.Resource{Href: href},
			},
			All:      result.All,
			Results:  result.Results,
			PollRate: pollRate,
		},
		FlowReservationResponse: result.Items,
	}
}

// requestStatusPresence decodes only whether RequestStatus and its two
// children were present in the document, and their raw text, using pointer
// fields to a string rather than the typed int64/uint8 sep2 carries.
// sep2.FlowReservationRequest cannot make either distinction on its own:
// RequestStatus is a value there (core flow_reservation.go: a served
// response must always emit the mandatory element), so a request that
// omitted it decodes to the same {0, 0} as one that sent
// <dateTime>0</dateTime><requestStatus>0</requestStatus> (#692). A string
// pointer goes one step further than an int64/uint8 pointer would: per
// encoding/xml's copyValue (GOROOT src/encoding/xml/read.go:639-656), a
// present-but-empty numeric element (self-closed, open-close, or
// comment-only, which all decode to zero-length character data) still
// allocates the pointer and sets it to 0, so an *int64 cannot tell "present,
// empty" from "present, literal 0". A *string can: it holds "" for empty
// content and the literal digits otherwise, with no numeric parsing to erase
// the difference. This type exists to check the document, not the struct.
type requestStatusPresence struct {
	XMLName       xml.Name `xml:"urn:ieee:std:2030.5:ns FlowReservationRequest"`
	RequestStatus *struct {
		DateTime      *string `xml:"dateTime"`
		RequestStatus *string `xml:"requestStatus"`
	} `xml:"RequestStatus"`
}

// requestStatusDateTimeFutureTolerance bounds how far ahead of the server's
// own clock a client's dateTime may be before it stops being explainable by
// clock skew. 2018 S233 / 2023 S240 requires dateTime to be "the time at
// which the status change occurred, not a time in the future or past"; the
// standard's own worst-case device clock accuracy is 60 seconds of drift per
// 24 hours for a device with no user interface (2023 S88), so five minutes
// comfortably covers that plus request latency while still refusing a client
// whose dateTime is wrong by any meaningful amount.
const requestStatusDateTimeFutureTolerance = 5 * time.Minute

// errRequestStatusRequired and its siblings name the missing or invalid
// piece; the handler logs them and sends a fixed client-facing message,
// matching the mRID check above and the #360 no-decoder-detail convention.
var (
	errRequestStatusRequired         = errors.New("RequestStatus is required")
	errRequestStatusDateTimeRequired = errors.New("RequestStatus.dateTime is required")
	errRequestStatusDateTimeEmpty    = errors.New("RequestStatus.dateTime must not be empty")
	errRequestStatusValueRequired    = errors.New("RequestStatus.requestStatus is required")
	errRequestStatusValueEmpty       = errors.New("RequestStatus.requestStatus must not be empty")
	errRequestStatusDateTimeNegative = errors.New("RequestStatus.dateTime must not be negative")
	errRequestStatusDateTimeFuture   = errors.New("RequestStatus.dateTime is too far in the future to be the instant the status changed")
	errRequestStatusValueReserved    = errors.New("RequestStatus.requestStatus is not 0 (Requested) or 1 (Cancelled)")
)

// validateRequestStatus refuses a FlowReservationRequest body whose
// RequestStatus element is missing, incomplete, empty, or carries a value the
// schema does not allow (2018 S233 / 2023 S240 RequestStatus object).
//
// requestStatus is UInt8 with only 0 (Requested) and 1 (Cancelled) defined;
// "All other values reserved". dateTime is TimeType, seconds since the 1970
// epoch, and "SHALL be set to the time at which the status change occurred,
// not a time in the future or past": a negative value predates 1970 and a
// value materially ahead of the server's clock is neither, so both are
// refused outright rather than guessed at.
//
// frq is the same body already decoded by the caller: once presence and
// non-emptiness are established from the raw text above, its parsed
// DateTime and RequestStatus are what the numeric checks run against, so the
// digits are parsed once (by that earlier xml.Unmarshal), not twice.
func validateRequestStatus(body []byte, frq sep2.FlowReservationRequest) error {
	var probe requestStatusPresence
	// Cannot fail: the caller already parsed the same bytes successfully
	// into frq, and every field probe decodes into is a string or a pointer
	// to one, so there is no numeric conversion left to fail on either.
	_ = xml.Unmarshal(body, &probe)

	if probe.RequestStatus == nil {
		return errRequestStatusRequired
	}
	if probe.RequestStatus.DateTime == nil {
		return errRequestStatusDateTimeRequired
	}
	if strings.TrimSpace(*probe.RequestStatus.DateTime) == "" {
		return errRequestStatusDateTimeEmpty
	}
	if probe.RequestStatus.RequestStatus == nil {
		return errRequestStatusValueRequired
	}
	if strings.TrimSpace(*probe.RequestStatus.RequestStatus) == "" {
		return errRequestStatusValueEmpty
	}

	if frq.RequestStatus.DateTime < 0 {
		return errRequestStatusDateTimeNegative
	}
	if frq.RequestStatus.DateTime > time.Now().Add(requestStatusDateTimeFutureTolerance).Unix() {
		return errRequestStatusDateTimeFuture
	}
	switch frq.RequestStatus.RequestStatus {
	case sep2.RequestStatusRequested, sep2.RequestStatusCancelled:
		return nil
	default:
		return errRequestStatusValueReserved
	}
}

// HandlePostFlowReservationRequest returns a handler for POST /edev/{id}/frq.
// pen is RouterConfig.PEN, passed straight through: nil (or the IANA-reserved
// value 0) mints a response mRID with no embedded PEN, per newFRPMRID.
func HandlePostFlowReservationRequest(
	frqStore store.ScopedStore[sep2.FlowReservationRequest],
	queue Submitter,
) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			encoding.MethodNotAllowed(w, "POST")
			return
		}

		edevID := r.PathValue("id")
		body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if err != nil {
			http.Error(w, "read body failed", http.StatusBadRequest)
			return
		}

		var frq sep2.FlowReservationRequest
		if err := xml.Unmarshal(body, &frq); err != nil {
			srverr.BadRequestMessage(w, r, "invalid XML", err)
			return
		}

		// Presence and emptiness are checked against the raw body, not frq:
		// RequestStatus is a value field, so an absent element and a
		// present-but-zero one decode identically (#692). See
		// validateRequestStatus.
		if err := validateRequestStatus(body, frq); err != nil {
			srverr.BadRequestMessage(w, r, "invalid RequestStatus", err)
			return
		}

		// The auto-created response's subject names the request it answers
		// (#665). A request with no mRID, or one that is whitespace only
		// (encoding/xml does not trim element content, so "<mRID> </mRID>"
		// unmarshals to a non-empty string), has no subject to give it, and
		// the client has no acknowledgement path back to a response nobody
		// could address, so it is refused outright rather than stored with a
		// meaningless subject downstream.
		if strings.TrimSpace(frq.MRID) == "" {
			http.Error(w, "FlowReservationRequest mRID is required", http.StatusBadRequest)
			return
		}

		frqID := fmt.Sprintf("frq-%d", time.Now().UnixNano())
		frq.Href = fmt.Sprintf("/edev/%s/frq/%s", edevID, frqID)
		createdAt := time.Now().Unix()
		frq.CreationTime = createdAt

		if err := frqStore.Create(r.Context(), edevID, frqID, frq); err != nil {
			srverr.Internal(w, r, err)
			return
		}

		// #666: no response is built here. The request waits for the
		// operator's answer, or the deadline fallback if none comes; queue
		// (internal/flowreservation.Queue) owns the one code path that ever
		// builds a FlowReservationResponse, so the request's mRID, subject
		// linkage and mint failures all live there instead of being
		// duplicated at this call site.
		queue.Submit(edevID, frqID, frq, createdAt)

		w.Header().Set("Location", frq.Href)
		encoding.WriteXML(w, http.StatusCreated, &frq)
	}
}

// Canceller withdraws the request stored under (edevID, frqID): marks it
// Cancelled with status, then settles its answer. Satisfied by
// *internal/flowreservation.Canceller; this package depends on the
// interface so a test can stub it.
type Canceller interface {
	Cancel(ctx context.Context, edevID, frqID string, status sep2.RequestStatus) error
}

var errRequestFieldChanged = errors.New("a PUT may change only RequestStatus")

// changedField names the first client-owned field of body that differs from
// stored, or "" when none does. href and creationTime are server-stamped,
// so a body that omits or echoes them wrongly changes nothing the client
// owns.
func changedField(stored, body sep2.FlowReservationRequest) string {
	switch {
	case stored.MRID != body.MRID:
		return "mRID"
	case stored.Description != body.Description:
		return "description"
	case !ptrEqual(stored.Version, body.Version):
		return "version"
	case !ptrEqual(stored.EnergyRequested, body.EnergyRequested):
		return "energyRequested"
	case !ptrEqual(stored.PowerRequested, body.PowerRequested):
		return "powerRequested"
	case !ptrEqual(stored.IntervalRequested, body.IntervalRequested):
		return "intervalRequested"
	case !ptrEqual(stored.DurationRequested, body.DurationRequested):
		return "durationRequested"
	}
	return ""
}

func ptrEqual[T comparable](a, b *T) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

// HandlePutFlowReservationRequest returns a handler for PUT
// /edev/{id}/frq/{frqId}, the one way a client withdraws a request
// (10.9.3.1): a body equal to the stored request except RequestStatus.
// Any other difference is 400 and changes nothing, as is an attempt to move
// a Cancelled request back to Requested. A body that leaves the status
// Requested changes nothing and is accepted.
func HandlePutFlowReservationRequest(
	frqStore store.ScopedStore[sep2.FlowReservationRequest],
	canceller Canceller,
) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			encoding.MethodNotAllowed(w, "PUT")
			return
		}

		edevID, frqID := r.PathValue("id"), r.PathValue("frqId")
		body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if err != nil {
			http.Error(w, "read body failed", http.StatusBadRequest)
			return
		}
		var frq sep2.FlowReservationRequest
		if err := xml.Unmarshal(body, &frq); err != nil {
			srverr.BadRequestMessage(w, r, "invalid XML", err)
			return
		}
		if err := validateRequestStatus(body, frq); err != nil {
			srverr.BadRequestMessage(w, r, "invalid RequestStatus", err)
			return
		}

		stored, err := frqStore.Get(r.Context(), edevID, frqID)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				http.Error(w, "not found", http.StatusNotFound)
				return
			}
			srverr.Internal(w, r, err)
			return
		}
		if field := changedField(stored, frq); field != "" {
			srverr.BadRequestMessage(w, r, "only RequestStatus may change", fmt.Errorf("%w: %s differs", errRequestFieldChanged, field))
			return
		}

		if frq.RequestStatus.RequestStatus != sep2.RequestStatusCancelled {
			if stored.RequestStatus.RequestStatus == sep2.RequestStatusCancelled {
				srverr.BadRequestMessage(w, r, "a cancelled request cannot be requested again", errRequestFieldChanged)
				return
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}

		if err := canceller.Cancel(r.Context(), edevID, frqID, frq.RequestStatus); err != nil {
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

// HandlePostResponse returns a handler for POST /rsps/{rspsId}/rsp.
//
// The WADL marks this POST Mandatory and declares six root elements at the
// member path it creates, so the body is decoded with sep2.DecodeResponse
// rather than unmarshalled straight into a sep2.Response. Unmarshalling into
// the base type answered the EPRI reference client's conforming
// <DERControlResponse> with 400, because Response.XMLName is pinned. The
// pin stays: DecodeResponse dispatches on the root element and decodes the
// declared subtype, so the accepted set is exactly the subtypes the WADL
// names.
//
// A Response that names an endDeviceLFDI is stored only when authorize says
// the sender may speak for that device: IEEE 2030.5-2018 6.11.2 grants access
// from the certificate's identity, not from what a body claims. A refusal is
// 403, since the request is well formed and its sender authenticated but not
// authorized for the device it names; nothing is stored. A nil authorize
// refuses every Response that names a device. The LFDI is compared and
// stored in canonical uppercase. A DERControlResponse must name its device,
// because the admin plane attributes it by that LFDI; other Response types
// that name none are stored as before.
func HandlePostResponse(rspStore store.ScopedStore[sep2.Response], authorize ResponseSenderAuthorizer) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			encoding.MethodNotAllowed(w, "POST")
			return
		}

		rspsID := r.PathValue("rspsId")
		body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if err != nil {
			http.Error(w, "read body failed", http.StatusBadRequest)
			return
		}

		rsp, err := sep2.DecodeResponse(body)
		if err != nil {
			srverr.BadRequestMessage(w, r, "invalid XML", err)
			return
		}

		if rsp.EndDeviceLFDI == "" {
			if responseRoot(body) == "DERControlResponse" {
				http.Error(w, "DERControlResponse endDeviceLFDI is required", http.StatusBadRequest)
				return
			}
		} else {
			lfdi, ok := canonicalLFDI(rsp.EndDeviceLFDI)
			if !ok {
				http.Error(w, "endDeviceLFDI must be 40 hexadecimal digits", http.StatusBadRequest)
				return
			}
			allowed, sender := false, ""
			if authorize != nil {
				allowed, sender, err = authorize(r, lfdi)
				if err != nil {
					srverr.Internal(w, r, err)
					return
				}
			}
			if !allowed {
				// lfdi is canonical hex and sender a certificate identity;
				// the path is quoted because it is decoded request text.
				log.Printf("rsps: refused a Response for endDeviceLFDI %s from sender %s: neither the sender nor a device it manages (%s %q)", lfdi, sender, r.Method, r.URL.Path)
				http.Error(w, "endDeviceLFDI is not the sender or a device it manages", http.StatusForbidden)
				return
			}
			rsp.EndDeviceLFDI = lfdi
		}

		id := fmt.Sprintf("rsp-%d", time.Now().UnixNano())
		rsp.Href = coreresponse.MemberHref(rspsID, id)
		rsp.CreatedDateTime = time.Now().Unix()

		if err := rspStore.Create(r.Context(), rspsID, id, rsp); err != nil {
			srverr.Internal(w, r, err)
			return
		}

		w.Header().Set("Location", rsp.Href)
		w.WriteHeader(http.StatusCreated)
	}
}

// ResponseSenderAuthorizer reports whether the sender of r may post a
// Response naming endDeviceLFDI (canonical uppercase): the sender's own
// device, or one it manages. sender is the sender's LFDI for the refusal
// log, or "" when the request carries no identity. A non-nil error means
// the check could not complete, and the POST answers 500.
type ResponseSenderAuthorizer func(r *http.Request, endDeviceLFDI string) (allowed bool, sender string, err error)

// canonicalLFDI returns s uppercased when it is 40 hex digits (HexBinary160).
// hexBinary collapses surrounding whitespace and is case-insensitive, and
// identities are compared uppercase.
func canonicalLFDI(s string) (string, bool) {
	s = strings.Trim(s, " \t\r\n")
	if len(s) != 40 {
		return "", false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F') {
			return "", false
		}
	}
	return strings.ToUpper(s), true
}

// responseRoot returns the local name of body's root element, or "" when it
// has none. body has already decoded, so this only names which subtype.
func responseRoot(body []byte) string {
	dec := xml.NewDecoder(bytes.NewReader(body))
	for {
		tok, err := dec.Token()
		if err != nil {
			return ""
		}
		if se, ok := tok.(xml.StartElement); ok {
			return se.Name.Local
		}
	}
}

// BuildResponseList constructs a ResponseList.
func BuildResponseList(href string, result store.ListResult[sep2.Response], pollRate uint32) sep2.ResponseList {
	return sep2.ResponseList{
		ListResource: sep2.ListResource{
			SubscribableResource: sep2.SubscribableResource{
				Resource: sep2.Resource{Href: href},
			},
			All:      result.All,
			Results:  result.Results,
			PollRate: pollRate,
		},
		Response: result.Items,
	}
}

// BuildResponseSetList constructs a ResponseSetList.
func BuildResponseSetList(href string, result store.ListResult[sep2.ResponseSet], pollRate uint32) sep2.ResponseSetList {
	return sep2.ResponseSetList{
		ListResource: sep2.ListResource{
			SubscribableResource: sep2.SubscribableResource{
				Resource: sep2.Resource{Href: href},
			},
			All:      result.All,
			Results:  result.Results,
			PollRate: pollRate,
		},
		ResponseSet: result.Items,
	}
}

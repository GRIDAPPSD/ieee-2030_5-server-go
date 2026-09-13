// Package der provides IEEE 2030.5 DER resource handlers: DERList,
// DERProgramList, DERControlList, DERCurveList, DER singleton sub-resources
// (DERCapability, DERSettings, DERStatus, DERAvailability), and
// DefaultDERControl. Ported verbatim from the reference server's
// internal/handler/der.go (no auth touch points; import paths rewritten
// to core).
package der

import (
	"fmt"
	"net/http"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2/encoding"
	coreresponse "github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/response"
	coresingleton "github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/singleton"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
)

// DefaultResponseRequired is the responseRequired bitmap this server stamps on
// a DERControl that does not carry one.
//
// Bits per IEEE 2030.5 Table 32: bit 0 message received, bit 1 specific
// response, bit 2 response on transition. 0x07 sets all three, which is the
// literal value CSIP CTP CORE-022 names in its setup and repeats in BASIC-004
// through BASIC-015. It is a HexBinary8, so it reaches the wire as "07" and
// not as a decimal 7.
const DefaultResponseRequired sep2.HexBinary8 = 0x07

// StampResponseRequest fills in the RespondableResource fields on a DERControl
// about to be served, so a conforming client knows a response is wanted and
// where to send it.
//
// # Why the server does this rather than whoever created the control
//
// replyTo has to name a URI THIS server routes, and the response function set
// is core's, not a consumer's: a consumer that wrote its own replyTo would be
// guessing at a path shape it does not own. Doing it on the way out also means
// no consumer has to change to become certification-conformant, and that the
// list route and the single-resource route cannot drift, since both call this.
//
// # Why it is a default and not an override
//
// A control that already carries either field keeps it. responseRequired is a
// pointer precisely so a server can say "explicitly none" (a stored 0x00)
// distinguishably from "unset", and collapsing the two would make that policy
// unexpressible. The base standard is conditional here ("If a response is
// desired to an event, then the event SHALL provide, in the replyTo field, a
// URI"), so a consumer that wants no response is not misconfigured; it just
// has to say so.
func StampResponseRequest(ctrl *sep2.DERControl) {
	if ctrl == nil {
		return
	}
	if ctrl.ReplyTo == "" {
		ctrl.ReplyTo = coreresponse.ListHref(coreresponse.DefaultSetID)
	}
	if ctrl.ResponseRequired == nil {
		v := DefaultResponseRequired
		ctrl.ResponseRequired = &v
	}
}

// BuildDERList constructs a DERList from store results.
func BuildDERList(href string, result store.ListResult[sep2.DER], pollRate uint32) sep2.DERList {
	return sep2.DERList{
		ListResource: sep2.ListResource{
			SubscribableResource: sep2.SubscribableResource{
				Resource: sep2.Resource{Href: href},
			},
			All:      result.All,
			Results:  result.Results,
			PollRate: pollRate,
		},
		DER: result.Items,
	}
}

// BuildDERProgramList constructs a DERProgramList from store results.
func BuildDERProgramList(href string, result store.ListResult[sep2.DERProgram], pollRate uint32) sep2.DERProgramList {
	return sep2.DERProgramList{
		ListResource: sep2.ListResource{
			SubscribableResource: sep2.SubscribableResource{
				Resource: sep2.Resource{Href: href},
			},
			All:      result.All,
			Results:  result.Results,
			PollRate: pollRate,
		},
		DERProgram: result.Items,
	}
}

// DERProgramHref returns the canonical href for a single DERProgram member,
// matching the only route this package mounts a DERProgram under: GET
// /edev/{id}/fsa/{fsaId}/derp/{derpId}. A DERProgram's own href is
// server-assigned, so every site that builds one (seeding code, admin
// creation, bootfixture and conformance-test data) MUST call this instead of
// hand-rolling the path. The FSA segment was once found dropped in three
// independently-written call sites, which is exactly the drift a single
// shared builder exists to prevent.
func DERProgramHref(edevID, fsaID, derpID string) string {
	return fmt.Sprintf("/edev/%s/fsa/%s/derp/%s", edevID, fsaID, derpID)
}

// BuildDERControlList constructs a DERControlList from store results.
//
// Every member is stamped with the response request (see StampResponseRequest)
// on the way out. The store hands back copies, so this mutates the served
// document and never the stored control.
func BuildDERControlList(href string, result store.ListResult[sep2.DERControl], pollRate uint32) sep2.DERControlList {
	for i := range result.Items {
		StampResponseRequest(&result.Items[i])
	}
	return sep2.DERControlList{
		ListResource: sep2.ListResource{
			SubscribableResource: sep2.SubscribableResource{
				Resource: sep2.Resource{Href: href},
			},
			All:      result.All,
			Results:  result.Results,
			PollRate: pollRate,
		},
		DERControl: result.Items,
	}
}

// BuildDERCurveList constructs a DERCurveList from store results.
func BuildDERCurveList(href string, result store.ListResult[sep2.DERCurve], pollRate uint32) sep2.DERCurveList {
	return sep2.DERCurveList{
		ListResource: sep2.ListResource{
			SubscribableResource: sep2.SubscribableResource{
				Resource: sep2.Resource{Href: href},
			},
			All:      result.All,
			Results:  result.Results,
			PollRate: pollRate,
		},
		DERCurve: result.Items,
	}
}

// DERSingletonHandlers creates all DER singleton GET/PUT handlers
// for DERCapability, DERSettings, DERStatus, DERAvailability.
func DERSingletonHandlers(
	caps store.ScopedStore[sep2.DERCapability],
	settings store.ScopedStore[sep2.DERSettings],
	statuses store.ScopedStore[sep2.DERStatus],
	avails store.ScopedStore[sep2.DERAvailability],
) (dercap, derg, ders, dera http.HandlerFunc) {

	derParentKey := func(r *http.Request) string {
		return r.PathValue("id") + "/" + r.PathValue("derId")
	}

	dercap = coresingleton.HandleSingletonGetPut[sep2.DERCapability](caps, derParentKey,
		func(r *http.Request) sep2.DERCapability {
			return sep2.DERCapability{Resource: sep2.Resource{
				Href: fmt.Sprintf("/edev/%s/der/%s/dercap", r.PathValue("id"), r.PathValue("derId")),
			}}
		})

	derg = coresingleton.HandleSingletonGetPut[sep2.DERSettings](settings, derParentKey,
		func(r *http.Request) sep2.DERSettings {
			s := sep2.DERSettings{}
			s.Href = fmt.Sprintf("/edev/%s/der/%s/derg", r.PathValue("id"), r.PathValue("derId"))
			return s
		})

	ders = coresingleton.HandleSingletonGetPut[sep2.DERStatus](statuses, derParentKey,
		func(r *http.Request) sep2.DERStatus {
			s := sep2.DERStatus{}
			s.Href = fmt.Sprintf("/edev/%s/der/%s/ders", r.PathValue("id"), r.PathValue("derId"))
			return s
		})

	dera = coresingleton.HandleSingletonGetPut[sep2.DERAvailability](avails, derParentKey,
		func(r *http.Request) sep2.DERAvailability {
			s := sep2.DERAvailability{}
			s.Href = fmt.Sprintf("/edev/%s/der/%s/dera", r.PathValue("id"), r.PathValue("derId"))
			return s
		})

	return
}

// DefaultDERControlHandler creates a handler for GET/HEAD on DefaultDERControl.
//
// PUT is refused with 405 rather than routed to the generic singleton upsert:
// unlike DERCapability/DERSettings/DERStatus/DERAvailability, which a device
// reports about itself, DefaultDERControl is utility-set (CSIP: the server
// sets it, clients monitor it), so no protocol caller -- owner, manager, or
// anyone else -- may write it (#456). The refusal is enforced here rather
// than only by which methods assembly.go mounts, so re-mounting PUT later
// cannot silently reopen the write.
func DefaultDERControlHandler(ddercStore store.ScopedStore[sep2.DefaultDERControl]) http.HandlerFunc {
	readOnly := coresingleton.HandleSingletonGetPut[sep2.DefaultDERControl](ddercStore,
		func(r *http.Request) string {
			return r.PathValue("id") + "/" + r.PathValue("fsaId") + "/" + r.PathValue("derpId")
		},
		func(r *http.Request) sep2.DefaultDERControl {
			s := sep2.DefaultDERControl{}
			s.Href = fmt.Sprintf("/edev/%s/fsa/%s/derp/%s/dderc",
				r.PathValue("id"), r.PathValue("fsaId"), r.PathValue("derpId"))
			return s
		})

	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet || r.Method == http.MethodHead {
			readOnly(w, r)
			return
		}
		encoding.MethodNotAllowed(w, "GET, HEAD")
	}
}

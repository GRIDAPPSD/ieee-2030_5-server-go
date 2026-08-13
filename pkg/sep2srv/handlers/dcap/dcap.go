package dcap

import (
	"net/http"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2/encoding"
)

// HandleDeviceCapability returns a handler for GET /dcap.
// DeviceCapability is the mandatory entry point for IEEE 2030.5 servers.
// Element order matches XSD (FunctionSetAssignmentsBase first, then extensions).
//
// DERProgramListLink is deliberately omitted. It previously pointed at
// "/dc", but "/dc" is the global DERCurve list (GET /dc in
// assembly.go registerDERRoutes, serving DERCurveList): a client following
// the advertised DERProgramListLink got a DERCurveList back, a bare
// violation of IEEE 2030.5-2018 section 4.4 on the resource every client
// fetches first. Core has no top-level DERProgramList resource to link to:
// DERProgram is served only nested under FunctionSetAssignments
// (GET /edev/{id}/fsa/{fsaId}/derp in assembly.go), because DERProgram
// hrefs are server-assigned and FSA-scoped, and serving the list there is
// the correct design, not a gap. Section 4.4: "If a function set is not
// implemented, Link elements to resources in that function set SHALL NOT
// be included." Omitting the link is the conformant choice; pointing it at
// a different resource type, or inventing a top-level route to satisfy the
// link, are not.
func HandleDeviceCapability() http.HandlerFunc {
	dcap := sep2.DeviceCapability{
		Resource: sep2.Resource{Href: "/dcap"},
		PollRate: 900,

		// FunctionSetAssignmentsBase elements (XSD order)
		MessagingProgramListLink: &sep2.ListLink{Href: "/msg"},
		ResponseSetListLink:      &sep2.ListLink{Href: "/rsps"},
		TimeLink:                 &sep2.Link{Href: "/tm"},
		UsagePointListLink:       &sep2.ListLink{Href: "/upt"},

		// DeviceCapability extension elements (XSD order)
		EndDeviceListLink:        &sep2.ListLink{Href: "/edev"},
		MirrorUsagePointListLink: &sep2.ListLink{Href: "/mup"},
		SelfDeviceLink:           &sep2.Link{Href: "/sdev"},
	}

	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			encoding.MethodNotAllowed(w, "GET, HEAD")
			return
		}
		encoding.WriteXML(w, http.StatusOK, &dcap)
	}
}

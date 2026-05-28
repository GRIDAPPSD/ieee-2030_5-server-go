package handler

import (
	"net/http"

	"github.com/GRIDAPPSD/ieee-2030_5-go/internal/encoding"
	"github.com/GRIDAPPSD/ieee-2030_5-go/pkg/sep2"
)

// HandleDeviceCapability returns a handler for GET /dcap.
// DeviceCapability is the mandatory entry point for IEEE 2030.5 servers.
// Element order matches XSD (FunctionSetAssignmentsBase first, then extensions).
func HandleDeviceCapability() http.HandlerFunc {
	dcap := sep2.DeviceCapability{
		Resource: sep2.Resource{Href: "/dcap"},
		PollRate: 900,

		// FunctionSetAssignmentsBase elements (XSD order)
		DERProgramListLink:       &sep2.ListLink{Href: "/dc"},
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

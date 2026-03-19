package handler

import (
	"net/http"

	"github.com/craig8/ieee-2030_5-go/internal/encoding"
	"github.com/craig8/ieee-2030_5-go/pkg/sep2"
)

// HandleDeviceCapability returns a handler for GET /dcap.
// DeviceCapability is the mandatory entry point for IEEE 2030.5 servers.
func HandleDeviceCapability() http.HandlerFunc {
	dcap := sep2.DeviceCapability{
		Resource: sep2.Resource{Href: "/dcap"},
		PollRate: 900,
		TimeLink: &sep2.Link{Href: "/tm"},
		EndDeviceListLink: &sep2.ListLink{
			Href: "/edev",
		},
		SelfDeviceLink:           &sep2.Link{Href: "/sdev"},
		MirrorUsagePointListLink: &sep2.ListLink{Href: "/mup"},
		DERProgramListLink:       &sep2.ListLink{Href: "/dc"},
	}

	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			encoding.MethodNotAllowed(w, "GET, HEAD")
			return
		}
		encoding.WriteXML(w, http.StatusOK, &dcap)
	}
}

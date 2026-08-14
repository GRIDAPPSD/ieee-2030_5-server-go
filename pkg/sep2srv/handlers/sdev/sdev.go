package sdev

import (
	"net/http"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2/encoding"
)

// HandleSelfDevice returns a handler for GET /sdev.
// SelfDevice represents the server's own identity.
func HandleSelfDevice(serverSFDI, serverLFDI string) http.HandlerFunc {
	sd := sep2.SelfDevice{
		SubscribableResource: sep2.SubscribableResource{
			Resource: sep2.Resource{Href: "/sdev"},
		},
		SFDI:                  serverSFDI,
		LFDI:                  serverLFDI,
		DeviceInformationLink: &sep2.Link{Href: "/sdev/sdi"},
	}

	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			encoding.MethodNotAllowed(w, "GET, HEAD")
			return
		}
		encoding.WriteXML(w, http.StatusOK, &sd)
	}
}

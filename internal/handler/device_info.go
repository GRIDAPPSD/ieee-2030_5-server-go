package handler

import (
	"net/http"

	"github.com/craig8/ieee-2030_5-go/internal/encoding"
	"github.com/craig8/ieee-2030_5-go/pkg/sep2"
)

// HandleDeviceInformation returns a handler for GET /sdev/sdi or /edev/{id}/di.
func HandleDeviceInformation(serverLFDI string) http.HandlerFunc {
	di := sep2.DeviceInformation{
		LFDI:    serverLFDI,
		MfModel: "IEEE 2030.5 Go Server",
		SwVer:   "0.1.0",
	}
	di.Href = "/sdev/sdi"

	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			encoding.MethodNotAllowed(w, "GET, HEAD")
			return
		}
		encoding.WriteXML(w, http.StatusOK, &di)
	}
}

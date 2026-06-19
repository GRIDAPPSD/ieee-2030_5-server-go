package handler

import (
	"fmt"
	"net/http"

	"gitlab.pnnl.gov/arista/ieee-2030_5/ieee-2030_5-core/pkg/sep2"
	"gitlab.pnnl.gov/arista/ieee-2030_5/ieee-2030_5-core/pkg/store/memory"
)

// HandlePowerStatus returns a singleton GET/PUT handler for /edev/{id}/ps.
func HandlePowerStatus(psStore *memory.ScopedStore[sep2.PowerStatus]) http.HandlerFunc {
	return HandleSingletonGetPut[sep2.PowerStatus](psStore,
		func(r *http.Request) string { return r.PathValue("id") },
		func(r *http.Request) sep2.PowerStatus {
			ps := sep2.PowerStatus{}
			ps.Href = fmt.Sprintf("/edev/%s/ps", r.PathValue("id"))
			return ps
		})
}

package power_status

import (
	"fmt"
	"net/http"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/singleton"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
)

// HandlePowerStatus returns a singleton GET/PUT handler for /edev/{id}/ps.
func HandlePowerStatus(psStore store.ScopedStore[sep2.PowerStatus]) http.HandlerFunc {
	return singleton.HandleSingletonGetPut[sep2.PowerStatus](psStore,
		func(r *http.Request) string { return r.PathValue("id") },
		func(r *http.Request) sep2.PowerStatus {
			ps := sep2.PowerStatus{}
			ps.Href = fmt.Sprintf("/edev/%s/ps", r.PathValue("id"))
			return ps
		})
}

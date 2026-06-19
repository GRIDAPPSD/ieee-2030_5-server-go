package handler

import (
	"fmt"
	"net/http"

	"gitlab.pnnl.gov/arista/ieee-2030_5/ieee-2030_5-core/pkg/sep2"
	"gitlab.pnnl.gov/arista/ieee-2030_5/ieee-2030_5-core/pkg/store"
	"gitlab.pnnl.gov/arista/ieee-2030_5/ieee-2030_5-core/pkg/store/memory"
)

// HandleConfiguration returns a singleton GET/PUT handler for /edev/{id}/cfg.
func HandleConfiguration(cfgStore *memory.ScopedStore[sep2.Configuration]) http.HandlerFunc {
	return HandleSingletonGetPut[sep2.Configuration](cfgStore,
		func(r *http.Request) string { return r.PathValue("id") },
		func(r *http.Request) sep2.Configuration {
			c := sep2.Configuration{}
			c.Href = fmt.Sprintf("/edev/%s/cfg", r.PathValue("id"))
			return c
		})
}

// Ensure store.Copier constraint is satisfied
var _ store.Copier[sep2.Configuration] = sep2.Configuration{}

package configuration

import (
	"fmt"
	"net/http"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/singleton"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
)

// HandleConfiguration returns a singleton GET/PUT handler for /edev/{id}/cfg.
func HandleConfiguration(cfgStore store.ScopedStore[sep2.Configuration]) http.HandlerFunc {
	return singleton.HandleSingletonGetPut[sep2.Configuration](cfgStore,
		func(r *http.Request) string { return r.PathValue("id") },
		func(r *http.Request) sep2.Configuration {
			c := sep2.Configuration{}
			c.Href = fmt.Sprintf("/edev/%s/cfg", r.PathValue("id"))
			return c
		})
}

// Ensure store.Copier constraint is satisfied
var _ store.Copier[sep2.Configuration] = sep2.Configuration{}

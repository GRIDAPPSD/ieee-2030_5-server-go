package server

import (
	"context"
	"strings"

	"github.com/GRIDAPPSD/ieee-2030_5-go/internal/handler"
	"github.com/GRIDAPPSD/ieee-2030_5-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-go/pkg/store/memory"
)

// IEEE-096 wiring: build the *handler.AdminFSAHandler from the server's
// stores plus a tiny adapter that turns the existing DERProgram scoped
// store into a DERProgramHrefValidator (the program href shape is
// `/edev/{id}/fsa/{fsaId}/derp/{derpId}` — we parse to (edev, derp) and
// probe the store).

// derProgramHrefValidator adapts *memory.ScopedStore[sep2.DERProgram] to
// handler.DERProgramHrefValidator. It is defined at the consumer (server
// package) per the Pike rule rather than baked into the handler package.
type derProgramHrefValidator struct {
	programs *memory.ScopedStore[sep2.DERProgram]
}

// HasProgram returns true if href resolves to an existing DERProgram.
// Spec-shape href: /edev/{id}/fsa/{fsaId}/derp/{derpId}. Anything else
// falls through as "not found" — the admin endpoint will surface 404.
func (v *derProgramHrefValidator) HasProgram(ctx context.Context, href string) bool {
	if v == nil || v.programs == nil {
		return false
	}
	edevID, derpID, ok := parseProgramHref(href)
	if !ok {
		return false
	}
	if _, err := v.programs.Get(ctx, edevID, derpID); err != nil {
		return false
	}
	return true
}

// parseProgramHref pulls (edevID, derpID) out of
// /edev/{id}/fsa/{fsaId}/derp/{derpId}. Returns ok=false on any malformed
// input — no partial matches.
func parseProgramHref(href string) (string, string, bool) {
	href = strings.TrimSpace(href)
	parts := strings.Split(strings.TrimPrefix(href, "/"), "/")
	// Expect: edev / {id} / fsa / {fsaId} / derp / {derpId}
	if len(parts) != 6 {
		return "", "", false
	}
	if parts[0] != "edev" || parts[2] != "fsa" || parts[4] != "derp" {
		return "", "", false
	}
	if parts[1] == "" || parts[5] == "" {
		return "", "", false
	}
	return parts[1], parts[5], true
}

// newAdminFSAHandler builds the handler from the server's stores. Returns
// nil when AdminFSAs is unset so the caller can decide to skip route
// registration entirely.
func newAdminFSAHandler(stores *Stores) *handler.AdminFSAHandler {
	if stores == nil || stores.AdminFSAs == nil {
		return nil
	}
	return &handler.AdminFSAHandler{
		AdminFSAs:   stores.AdminFSAs,
		DeviceFSAs:  stores.FSAs,
		EndDevices:  stores.EndDevices,
		DERPrograms: &derProgramHrefValidator{programs: stores.DERPrograms},
	}
}

package dercontrol

import (
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/derhref"
)

// parseProgramHref pulls (edev, fsa, derp) out of
// "/edev/{id}/fsa/{fsaId}/derp/{derpId}". ok is false on any other shape.
func parseProgramHref(href string) (edev, fsa, derp string, ok bool) {
	return derhref.Program(href)
}

// parseControlListHref pulls (edev, fsa, derp) out of
// "/edev/{id}/fsa/{fsaId}/derp/{derpId}/derc". See [derhref.ControlList];
// this package's own status-derivation decorator
// (pkg/sep2srv/handlers/der) and the DERControlListLink.all decorator call
// the same shared function directly, so the parse logic exists in exactly
// one place.
func parseControlListHref(href string) (edev, fsa, derp string, ok bool) {
	return derhref.ControlList(href)
}

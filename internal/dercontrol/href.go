package dercontrol

import (
	"strings"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/derhref"
)

// parseProgramHref pulls (edev, fsa, derp) out of
// "/edev/{id}/fsa/{fsaId}/derp/{derpId}". ok is false on any other shape.
func parseProgramHref(href string) (edev, fsa, derp string, ok bool) {
	return derhref.Program(href)
}

// parseControlListHref pulls (edev, fsa, derp) out of
// "/edev/{id}/fsa/{fsaId}/derp/{derpId}/derc". A href missing the "/derc"
// suffix is not a control list link and is refused: it may be the program's
// own href, which no device could have followed to reach a control list.
func parseControlListHref(href string) (edev, fsa, derp string, ok bool) {
	trimmed, ok := strings.CutSuffix(strings.TrimSpace(href), "/derc")
	if !ok {
		return "", "", "", false
	}
	return derhref.Program(trimmed)
}

package dercontrol

import "strings"

// parseProgramHref pulls (edev, fsa, derp) out of
// "/edev/{id}/fsa/{fsaId}/derp/{derpId}". ok is false on any other shape.
//
// Duplicated from internal/server's identical parser (der control lookups
// need the fsa segment too, which that one discards) rather than imported:
// this package must not depend on internal/server, since the admin handler
// in internal/server is the future caller of this package, not the other
// way around.
func parseProgramHref(href string) (edev, fsa, derp string, ok bool) {
	parts, ok := splitHref(href, "edev", "fsa", "derp")
	if !ok {
		return "", "", "", false
	}
	return parts[0], parts[1], parts[2], true
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
	return parseProgramHref(trimmed)
}

// splitHref matches "/seg0/{v0}/seg1/{v1}/seg2/{v2}" against the given
// literal segment names and returns (v0, v1, v2). Every literal and every
// value must be non-empty.
func splitHref(href string, seg0, seg1, seg2 string) ([3]string, bool) {
	href = strings.TrimSpace(href)
	parts := strings.Split(strings.TrimPrefix(href, "/"), "/")
	if len(parts) != 6 {
		return [3]string{}, false
	}
	if parts[0] != seg0 || parts[2] != seg1 || parts[4] != seg2 {
		return [3]string{}, false
	}
	if parts[1] == "" || parts[3] == "" || parts[5] == "" {
		return [3]string{}, false
	}
	return [3]string{parts[1], parts[3], parts[5]}, true
}

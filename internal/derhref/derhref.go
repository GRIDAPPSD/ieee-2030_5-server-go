// Package derhref parses the DER program href shape
// "/edev/{id}/fsa/{fsaId}/derp/{derpId}" shared by internal/dercontrol and
// internal/server, so the split-and-match logic lives in exactly one place
// rather than two copies that can drift (#563).
package derhref

import "strings"

// Program pulls (edev, fsa, derp) out of
// "/edev/{id}/fsa/{fsaId}/derp/{derpId}". ok is false on any other shape.
func Program(href string) (edev, fsa, derp string, ok bool) {
	parts, ok := Split(href, "edev", "fsa", "derp")
	if !ok {
		return "", "", "", false
	}
	return parts[0], parts[1], parts[2], true
}

// Split matches "/seg0/{v0}/seg1/{v1}/seg2/{v2}" against the given literal
// segment names and returns (v0, v1, v2). Every literal and every value
// must be non-empty.
func Split(href string, seg0, seg1, seg2 string) ([3]string, bool) {
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

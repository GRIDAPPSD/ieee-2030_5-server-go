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

// ControlID recovers the store id from a DERControl's own href
// ("/edev/.../derc/<id>"), the one shape the issuer (internal/dercontrol),
// the boot fixture and the CSIP loader all build. ok is false for any other
// shape, which callers treat as "cannot key a lookup on it" rather than
// guessing.
func ControlID(href string) (string, bool) {
	const marker = "/derc/"
	idx := strings.LastIndex(href, marker)
	if idx < 0 {
		return "", false
	}
	id := href[idx+len(marker):]
	if id == "" {
		return "", false
	}
	return id, true
}

// ControlList pulls (edev, fsa, derp) out of a DERControlListLink href
// ("/edev/{id}/fsa/{fsaId}/derp/{derpId}/derc"). A href missing the "/derc"
// suffix is not a control list link and is refused: it may be the
// program's own href, which no device could have followed to reach a
// control list.
func ControlList(href string) (edev, fsa, derp string, ok bool) {
	trimmed, ok := strings.CutSuffix(strings.TrimSpace(href), "/derc")
	if !ok {
		return "", "", "", false
	}
	return Program(trimmed)
}

// ControlListScope is [ControlList] composed into the single scope key
// string the DERControl store is keyed by ("edev/fsa/derp"), the same join
// [internal/dercontrol.Scope]'s own key uses.
func ControlListScope(href string) (string, bool) {
	edev, fsa, derp, ok := ControlList(href)
	if !ok {
		return "", false
	}
	return edev + "/" + fsa + "/" + derp, true
}

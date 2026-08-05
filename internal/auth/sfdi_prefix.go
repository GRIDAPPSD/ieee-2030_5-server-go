package auth

import "fmt"

// ExtractSFDIPrefix returns the first 8 characters of an SFDI string as a
// device-ID prefix. An SFDI is at minimum 8 characters long; if the supplied
// string is shorter, a non-nil error is returned and the prefix is empty.
//
// Both AutoRegistrationMiddleware (registration.go) and HandleCreateEndDevice
// (handler/edev.go) need this slice. Centralising the bounds check here
// prevents a panic at `sfdi[:8]` on any truncated or synthetic SFDI value
// (test fixture, future format change). See #13.
func ExtractSFDIPrefix(sfdi string) (string, error) {
	if len(sfdi) < 8 {
		return "", fmt.Errorf("SFDI %q too short: need ≥8 chars, got %d", sfdi, len(sfdi))
	}
	return sfdi[:8], nil
}

// extractSFDIPrefix is the package-internal alias so registration.go can call
// the same implementation without qualifying the package name. Tests inside
// package auth call extractSFDIPrefix to exercise the unexported surface.
func extractSFDIPrefix(sfdi string) (string, error) { return ExtractSFDIPrefix(sfdi) }

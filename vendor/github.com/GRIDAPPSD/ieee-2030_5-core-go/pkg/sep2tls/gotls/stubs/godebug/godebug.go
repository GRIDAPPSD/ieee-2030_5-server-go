// Package godebug stubs internal/godebug for the crypto/tls fork.
package godebug

// Setting represents a GODEBUG setting.
type Setting struct {
	name string
}

// New creates a new Setting. In the stub, all settings return empty values.
func New(name string) *Setting {
	return &Setting{name: name}
}

// Value returns the current value of the setting. Stub always returns "".
func (s *Setting) Value() string {
	return ""
}

// IncNonDefault is a no-op in the stub.
func (s *Setting) IncNonDefault() {}

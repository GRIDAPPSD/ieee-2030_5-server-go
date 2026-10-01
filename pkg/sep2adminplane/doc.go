// Package sep2adminplane serves this server's admin UI and admin API from
// inside another program.
//
// [New] builds the plane from the same function the standalone server's Run
// uses, so an embedder and the server serve one code path. The plane is an
// [net/http.Handler]; listening, TLS and the listener's lifetime are the
// embedder's.
//
// Two defaults differ from the standalone server, and both fail closed:
// [Config.LoopbackBypass] is off, so a loopback request needs a credential
// like any other, and [Config.ControlWrites] is off, so the DER control and
// flow reservation write routes are not mounted.
//
// # Stability
//
// This package is a sibling of pkg/sep2admin and carries its weaker
// promise, not pkg/sep2server's: the admin plane is this repository's
// fastest-changing surface. The [Plane] method set and the [Config] field
// names are kept; the route list behind [Plane.Patterns] is not.
package sep2adminplane

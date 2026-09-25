// Package sep2server is the embeddable surface of this IEEE 2030.5 server.
//
// Everything else in this repository lives under cmd/ and internal/, and
// internal/ is unimportable from outside the module. That left no surface for
// an in-process consumer to graft onto, so the only way to embed a 2030.5
// server was to wrap the core assembly directly and re-implement the routing,
// auth and store wiring this repository already owns. Two such wrappers
// existed and diverged. This package exists so there is one.
//
// # The surface
//
// Four things, and deliberately no more:
//
//  1. Construction. [Config] carries the stores, the auth policy, the rate
//     providers, the TLS material and the subscription notifier.
//  2. Store handles. [Server.Stores] hands back the handle a consumer seeds
//     through and injects control through. See the note on [Server.Stores]
//     for the read-only half of the privilege split.
//  3. The router as an [net/http.Handler]. [Server.Handler] and
//     [BuildHandler] let a consumer compose alongside the protocol surface
//     without reaching inside it.
//  4. Lifecycle. [New] binds, [Server.Run] serves and drains gracefully.
//
// # What is deliberately NOT here
//
// The ACL internals, the admin dashboard's serving logic, the operator
// login surface and the build-tagged test-mutation hooks are this
// server's own deployment concerns, not a contract for embedders. They
// stay in internal/. The admin dashboard's built UI assets and its
// extension contract live in the sibling packages pkg/adminui/web and
// pkg/sep2admin, each of which carries a weaker stability promise than
// this one. [DefaultAuthPolicy] is the one door onto the ACL, and it is
// a composed policy value rather than the rules behind it: an embedder
// can adopt this server's enforcement, but it cannot reach in and
// reshape it.
//
// Anything exported here is a contract this repository keeps stable. That is
// the reason the list above is short.
package sep2server

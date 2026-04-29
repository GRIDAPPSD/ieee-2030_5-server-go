# TODO — Deferred Findings

Items discovered during the `security/admin-auth-ticket` branch work and the
golangci-lint cleanup pass that followed. They were intentionally deferred —
fixing them was outside the scope of those changes.

## HIGH — Server identity computed after router construction

**File:** `internal/server/server.go:99-100`

In GCM mode, `serverSFDI` and `serverLFDI` are derived from the server
certificate *after* `NewRouter(...)` is called on line 64 with empty strings.
Result: server-identity-dependent endpoints see empty SFDI/LFDI in the GCM
cipher path.

Currently suppressed with `//nolint:ineffassign` plus a TODO comment so the
lint pass stays clean. The real fix is to move TLS configuration and identity
derivation above the `NewRouter` call so the router receives non-empty values
from the start.

Severity: HIGH — affects correctness of any endpoint that consumes server
SFDI/LFDI under GCM mode.

## LOW — Dead EXI encoder stub

**File:** `internal/encoding/exi_stub.go`

`exiStubEncoder` was entirely unused. The lint pass added a compile-time
interface assertion (`var _ Encoder = (*exiStubEncoder)(nil)`) so the file
documents the intended EXI API shape rather than sitting as fully dead code.

Decision needed:
- If EXI encoding is no longer planned, delete the file.
- If it is planned, implement against the asserted interface.

## Behavior change to be aware of

**File:** `internal/handler/flow_reservation.go:94`

The lint cleanup uncovered a swallowed error: `frpStore.Create` failures were
being ignored and the handler returned `201 Created` regardless of outcome.
Now returns `500 Internal Server Error` on store failure. Strictly an
improvement, but documenting it here so anyone diffing against an older copy
of this branch knows where the behavior changed.

## Repository status notes

- No git remote is currently configured. This branch lives only on the local
  checkout until it is moved to its destination repository.
- No CI is configured. `make test`, `make vet`, and `make lint` are the only
  signal — all currently green on this branch.
- The `docs/2030.5-*/` IEEE vendor packages and `certs/` PKI material are
  gitignored. A fresh clone needs them obtained out-of-band before `make run`
  works.

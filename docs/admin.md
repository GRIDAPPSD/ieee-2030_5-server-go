# Admin surface

The admin surface is the operator-facing side of the server: a dashboard,
a login form, a cert-management API, an EndDevice registration assistant,
and an FSA management plane. It runs on its own listener with its own TLS
posture, separately from the SEP2 protocol listener. For the protocol
side see [`2030_5.md`](2030_5.md). For the listener TLS posture matrix
(plain HTTP behind Caddy, direct HTTPS with operator cert, self-signed
fallback) see [`admin-listener.md`](admin-listener.md).

## Reaching the admin surface

The admin listener binds `SEP2_ADMIN_LISTEN` (preferred) or the
deprecated `SEP2_ADMIN_ADDR` alias. Empty disables the admin surface
entirely. The Make targets bind it on `:8444`:

```
SEP2_ADMIN_ADDR=:8444
SEP2_ADMIN_KEY=admin
```

`SEP2_ADMIN_KEY` is the Bearer token the login form validates against
(constant-time compare).

| Profile | Command |
|---|---|
| Default (admin enabled, no mDNS) | `make run` |
| CCM-8 + admin | `make run-ccm` |
| CCM-8 + admin + mDNS | `make run-full` |

The browser entry point is `https://localhost:8444/login`. Submit
`SEP2_ADMIN_KEY` as the `key` field; on success the server issues a
short-lived `admin_ticket` cookie and redirects to `/`, the dashboard.

## Auth model

`AdminAuthMiddleware`
([`internal/auth/admin.go`](../internal/auth/admin.go)) gates everything
behind the login routes. Four paths are checked in order:

1. **mTLS** — peer cert with the IEEE 2030.5 admin policy OID
   `1.3.6.1.4.1.40732.2.5` (matched by
   [`certs.HasPolicyOID`](../internal/certs/)).
2. **Bearer** — `Authorization: Bearer <SEP2_ADMIN_KEY>`. Disabled when
   `SEP2_ADMIN_KEY` is empty.
3. **Query-param ticket** — `?ticket=<value>` redeemed against the
   `TicketStore`. One-time use. Used by browser SSE / EventSource
   clients that cannot send `Authorization` headers.
4. **Cookie ticket** — the `admin_ticket` cookie redeemed against the
   `TicketStore`. On success the middleware issues a fresh ticket and
   re-sets the cookie so multi-request page navigation works under the
   one-time-use semantics. Cookie is `HttpOnly`, `Secure`,
   `SameSite=Strict`, `Path=/` — see `NewAdminTicketCookie` in
   `admin.go`.

Tickets are random 32-byte hex strings issued by
[`internal/auth/ticket.go`](../internal/auth/ticket.go). The store is
single-host in-memory, protected by a mutex, and purges expired
entries lazily on `Issue`. The TTL is configured at server bootstrap.

ACL on the **protocol** listener
([`internal/auth/acl.go`](../internal/auth/acl.go)) is a separate
concern: per-prefix method bitmaps and auth-type bitmaps, longest-prefix
match. It does not gate the admin surface.

## Admin features

Status vocabulary: **Complete / In Progress / Planned**. "Complete"
means the route is wired, the handler returns a useful response, and
the dashboard form (where one exists) submits to it.

| Feature | Status | Description | Source |
|---|---|---|---|
| Login form | Complete | `GET /login` renders the form; `POST /auth/login` validates the key and issues the cookie. | [`internal/server/login.go`](../internal/server/login.go), [`internal/server/login_html.go`](../internal/server/login_html.go) |
| Dashboard page | Complete | `GET /` renders the operator dashboard. | [`internal/server/dashboard.go`](../internal/server/dashboard.go), [`internal/server/dashboard_html.go`](../internal/server/dashboard_html.go) |
| Live dashboard data | Complete | `GET /dashboard/data` returns JSON; `GET /dashboard/events` is the SSE stream pushing 5-second updates. | [`internal/server/dashboard.go`](../internal/server/dashboard.go) |
| Auth ticket exchange | Complete | `POST /auth/ticket` exchanges a valid admin session for a one-time-use ticket. Used by SSE clients. | [`internal/server/admin_router.go`](../internal/server/admin_router.go), [`internal/auth/ticket.go`](../internal/auth/ticket.go) |
| Cert management API | Complete | `GET /api/certs/ca` (download CA), `POST /api/certs/server`, `POST /api/certs/device`. The dashboard's "Certificate Management" form posts to `/api/certs/device`. | [`internal/handler/admin_certs.go`](../internal/handler/admin_certs.go) |
| Cert info parser | Complete | `POST /api/certs/info` parses a pasted PEM cert and returns SFDI + LFDI. Used by the "Add End Device" form to auto-fill identity. | [`internal/handler/admin_register.go`](../internal/handler/admin_register.go) |
| EndDevice registration | Complete | `POST /api/devices` creates an EndDevice + Registration with a PIN (IEEE-095). `GET /api/devices/by-lfdi/{lfdi}` looks up by LFDI. Both wired into the dashboard. | [`internal/handler/admin_register.go`](../internal/handler/admin_register.go) |
| FSA management | Complete | Create/list/get/delete admin FSA templates, attach/detach DERPrograms, assign/unassign devices, plus a topology endpoint for the dashboard tree (IEEE-096). | [`internal/handler/admin_fsa.go`](../internal/handler/admin_fsa.go), [`internal/server/admin_fsa_wiring.go`](../internal/server/admin_fsa_wiring.go) |
| Topology view | Complete | `GET /api/topology` returns the SY → FD → SP → DEV tree the dashboard renders. | [`internal/handler/admin_topology.go`](../internal/handler/admin_topology.go) |
| DER control submit form | In Progress | The "Send DER Control" form is rendered in [`dashboard_html.go`](../internal/server/dashboard_html.go), but the JS submit is currently a stub (`// TODO: POST to /api/der/controls when admin DER API is wired`). The admin DER API itself is not implemented. | [`internal/server/dashboard_html.go`](../internal/server/dashboard_html.go) |

## mTLS cert flow for the admin path

`make certs` issues four artifacts into `certs/`:

| File | Generated by | Purpose |
|---|---|---|
| `certs/ca.crt`, `certs/ca.key` | `sep2server certs generate-ca` | Root CA used to sign the other three. Trusted by both listeners. |
| `certs/server.crt`, `certs/server.key` | `sep2server certs generate-server` | Server leaf for the SEP2 listener (`SEP2_CERT`, `SEP2_KEY`). |
| `certs/admin.crt`, `certs/admin.key` | `sep2server certs generate-admin` | Operator client cert. Carries the admin policy OID `1.3.6.1.4.1.40732.2.5` so `AdminAuthMiddleware` Path A admits it. |
| `certs/device.crt`, `certs/device.key` | `sep2server certs generate-device` | Sample device cert. CSIP-compliant when `--hw-serial` and `--hw-type` are supplied (see [`csip.md`](csip.md)). |

To use the admin cert against an HTTPS admin listener:

```bash
curl --cacert certs/ca.crt \
     --cert  certs/admin.crt \
     --key   certs/admin.key \
     https://localhost:8444/api/certs/ca
```

The cert generation entry points live in
[`cmd/sep2server/cmd_certs.go`](../cmd/sep2server/cmd_certs.go); the
underlying generation is in [`internal/certs/`](../internal/certs/).

## Listener TLS posture

The admin listener can run as plain HTTP (intended for Caddy in front),
direct HTTPS with operator-supplied cert/key, or direct HTTPS with a
self-signed fallback. The full posture matrix and the
`SEP2_ADMIN_TLS` / `SEP2_ADMIN_CERT` / `SEP2_ADMIN_KEY_FILE` knobs are
documented in [`admin-listener.md`](admin-listener.md). When HTTPS is
on, `ClientAuth` is `VerifyClientCertIfGiven`: a browser without a
client cert still completes the handshake and authenticates via Bearer
or cookie.

## End-to-end tests

The Playwright suite under [`e2e/`](../e2e/) drives the dashboard.
`make test-e2e` runs it (requires `npm install` in `e2e/` first).
Putting the suite under CI is tracked under
[#211 (IEEE-115)](https://github.com/GRIDAPPSD/ieee-2030_5-go/issues/211).

## Cross-references

- [README](../README.md)
- [`2030_5.md`](2030_5.md) — protocol surface
- [`csip.md`](csip.md) — CSIP V1.2 profile
- [`admin-listener.md`](admin-listener.md) — admin listener TLS posture
- [`caddy-admin.example.conf`](caddy-admin.example.conf) — example
  Caddy fronting config

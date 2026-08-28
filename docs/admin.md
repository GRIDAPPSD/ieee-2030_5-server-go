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

```bash
SEP2_ADMIN_LISTEN=:8444
SEP2_ADMIN_KEY="$(openssl rand -hex 32)"   # bearer secret; do NOT ship "admin"
```

`SEP2_ADMIN_KEY` is the Bearer token the login form validates against
(constant-time compare). It is distinct from `SEP2_ADMIN_KEY_FILE`,
which is the admin listener's TLS private key path — see
[`admin-listener.md`](admin-listener.md). Any non-loopback deployment
MUST set `SEP2_ADMIN_KEY` to a high-entropy value; the literal string
`admin` is fine for `make run` on localhost only.

| Profile | Command |
|---|---|
| Default (admin enabled, no mDNS) | `make run` |
| CCM-8 + admin | `make run-ccm` |
| CCM-8 + admin + mDNS | `make run-full` |

The Make targets above leave `SEP2_ADMIN_TLS` unset, so the admin
listener serves **plain HTTP** on `:8444` (Caddy mode). Two browser
entry points are supported, depending on which posture you want:

- **HTTP, default — #246 loopback bypass.** Hit
  `http://localhost:8444/` directly. No `/login`, no Bearer token, no
  client cert needed: `AdminAuthMiddleware` admits any request from a
  loopback address that carries no reverse-proxy forwarded header.
  Caddy in front injects `X-Forwarded-For` by default, so the bypass
  declines for production traffic and the normal auth chain runs.
- **HTTPS, opt-in.** Set `SEP2_ADMIN_TLS=true` (and optionally
  `SEP2_ADMIN_CERT` / `SEP2_ADMIN_KEY_FILE`), then hit
  `https://localhost:8444/login`. Submit `SEP2_ADMIN_KEY` as the
  `key` field; on success the server issues a short-lived
  `admin_ticket` cookie and redirects to `/`, the dashboard. The
  loopback bypass still applies, but you can also use Bearer or mTLS
  if you want to exercise the post-bypass chain. See
  [`admin-listener.md`](admin-listener.md) for the full TLS posture
  matrix.

## Auth model

`AdminAuthMiddleware`
([`internal/auth/admin.go`](../internal/auth/admin.go)) gates everything
behind the login routes. Five paths are checked in order; **any single
path that succeeds admits the request** (this is fallback ordering, not
defense in depth):

0. **Loopback bypass (#246)** — RemoteAddr is a loopback address
   (127.0.0.0/8 or `::1`) AND the request carries no reverse-proxy
   forwarded header (`X-Forwarded-For`, `X-Forwarded-Host`,
   `X-Forwarded-Proto`, RFC 7239 `Forwarded`). Local-developer
   ergonomic path: `make run` on localhost has no working credentials
   by default, and a reverse proxy in front injects `X-Forwarded-*` so
   the bypass declines automatically for production traffic. Every
   admission is logged.
1. **[mTLS](glossary.md)** — peer cert with the IEEE 2030.5 admin policy OID
   `1.3.6.1.4.1.40732.2.5` (matched by
   [`certs.HasPolicyOID`](../internal/certs/oids.go)).
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
| Login form | Complete | `GET /login` renders the form; `POST /auth/login` validates the key and issues the cookie. The dashboard shows the same form in place of the panels when its first authenticated read returns 401, so an expired session does not present an empty page. | [`internal/server/login.go`](../internal/server/login.go), [`internal/server/login_html.go`](../internal/server/login_html.go), [`frontend/src/panels/LoginPanel.svelte`](../internal/server/web/frontend/src/panels/LoginPanel.svelte) |
| Dashboard page | Complete | `GET /` renders the operator dashboard: the embedded Svelte admin UI, or the pre-Svelte page with `SEP2_ADMIN_LEGACY_DASHBOARD=true`. | [`internal/server/dashboard.go`](../internal/server/dashboard.go), [`frontend/src/routes/Dashboard.svelte`](../internal/server/web/frontend/src/routes/Dashboard.svelte) |
| Live dashboard data | Complete | `GET /dashboard/data` returns JSON; `GET /dashboard/events` is the SSE stream pushing 5-second updates. | [`internal/server/dashboard.go`](../internal/server/dashboard.go), [`frontend/src/lib/dashboard.ts`](../internal/server/web/frontend/src/lib/dashboard.ts) |
| Auth ticket exchange | Complete | `POST /auth/ticket` exchanges a valid admin session for a one-time-use ticket. Used by SSE clients. | [`internal/server/admin_router.go`](../internal/server/admin_router.go), [`internal/auth/ticket.go`](../internal/auth/ticket.go), [`frontend/src/lib/dashboard.ts`](../internal/server/web/frontend/src/lib/dashboard.ts) |
| Cert management API | Complete | `GET /api/certs/ca` (download CA), `POST /api/certs/server`, `POST /api/certs/device`. The dashboard's "Certificate Management" card reaches the CA download and the device cert. `POST /api/certs/server` is CLI and curl only by design: it returns a server private key, which a browser panel has no business receiving. | [`internal/handler/admin_certs.go`](../internal/handler/admin_certs.go), [`frontend/src/panels/CertPanel.svelte`](../internal/server/web/frontend/src/panels/CertPanel.svelte) |
| Cert info parser | Complete | `POST /api/certs/info` parses a pasted PEM cert and returns SFDI + LFDI. Used by the "Add End Device" form to auto-fill identity. | [`internal/handler/admin_register.go`](../internal/handler/admin_register.go), [`frontend/src/panels/AddDevice.svelte`](../internal/server/web/frontend/src/panels/AddDevice.svelte) |
| EndDevice registration | Complete | `POST /api/devices` creates an EndDevice + Registration with a PIN (#159). `GET /api/devices/by-lfdi/{lfdi}` looks up by LFDI. Both wired into the dashboard. | [`internal/handler/admin_register.go`](../internal/handler/admin_register.go), [`frontend/src/panels/AddDevice.svelte`](../internal/server/web/frontend/src/panels/AddDevice.svelte), [`frontend/src/panels/LookupDevice.svelte`](../internal/server/web/frontend/src/panels/LookupDevice.svelte) |
| FSA management | Complete | Create/list/get/delete admin FSA templates, attach/detach DERPrograms, assign/unassign devices, plus a topology endpoint for the dashboard tree (#163). Create is the Create FSA card, attach/detach and delete are the tree's per-FSA controls, assign is the device table, unassign is the FSA template table. | [`internal/handler/admin_fsa.go`](../internal/handler/admin_fsa.go), [`internal/server/admin_fsa_wiring.go`](../internal/server/admin_fsa_wiring.go), [`frontend/src/panels/CreateFsa.svelte`](../internal/server/web/frontend/src/panels/CreateFsa.svelte), [`frontend/src/panels/FsaNode.svelte`](../internal/server/web/frontend/src/panels/FsaNode.svelte), [`frontend/src/panels/FsaCatalog.svelte`](../internal/server/web/frontend/src/panels/FsaCatalog.svelte) |
| Topology view | Complete | `GET /api/topology` returns the SY / FD / SP / DEV tree the dashboard renders. | [`internal/handler/admin_topology.go`](../internal/handler/admin_topology.go), [`frontend/src/panels/TopologyTree.svelte`](../internal/server/web/frontend/src/panels/TopologyTree.svelte) |
| DER control submit form | In Progress | The "Send DER Control" form renders and validates, but its submit is still a stub: it echoes the selection and sends nothing. The blocker is a missing backend route (`POST /api/der/controls`), not the UI. | [`frontend/src/panels/DerControl.svelte`](../internal/server/web/frontend/src/panels/DerControl.svelte) |

## The dashboard is an embedded Svelte app

`GET /` serves a Svelte single-page app compiled into the binary
(`internal/server/web/frontend/` builds into `internal/server/web/dist/`,
embedded by `internal/server/web/embed.go`). It is also served at `/ui/`.
Node and npm are build-time-only: a fresh `go build` uses the committed
bundle. Rebuild with `make ui-build` after any change under `frontend/`;
`make ui-check` fails the build if the committed bundle is stale.

The chart library is bundled, not fetched. The previous page pulled it
from a public CDN, which failed silently on any deployment without
outbound internet access: an authenticated admin page, a blank panel, and
no server-side signal. `e2e/chart_offline.spec.ts` asserts the chart still
draws with every external origin blocked.

Panels live in `frontend/src/panels/`, one per dashboard card, each with a
sibling `*.svelte.test.ts`. `frontend/src/lib/api.ts` is the only place
that calls `fetch`. Element ids from the previous markup are preserved:
see [`admin-ui-selectors.md`](admin-ui-selectors.md).

### Certificate downloads

`GET /api/certs/ca` answers with JSON carrying the PEM in a `certPEM`
field, not with a PEM body, so the panel extracts that field and saves the
file. A plain link to the route would save the JSON envelope under a
`.crt` name and a device trust store would reject it.

A device cert request needs the manufacturer PEN OID (`hwType`) as well as
the hardware serial: the route rejects a request without it, since the OID
is part of the CSIP HardwareModuleName SAN and the server cannot invent
one. The issued certificate and key are offered as file downloads and are
not stored on the server; the key is never rendered into the page.

`POST /api/certs/server` has no button. It mints a server private key, and
a panel that receives one without delivering it would put key material
through the devtools network log, the JS heap, and any HAR attached to a
bug report, for no benefit. Use the CLI (`sep2server certs
generate-server`) or curl, where the key is written to a file.

### Rolling back to the previous page

```bash
SEP2_ADMIN_LEGACY_DASHBOARD=true   # GET / serves the pre-Svelte page
```

The pre-Svelte dashboard is still compiled in
(`internal/server/dashboard_html.go`) and this flag serves it at `GET /`,
so a page that breaks an operator's workflow does not need a binary
downgrade. That page no longer loads a chart library at all, so it renders
every panel except the activity chart. `/ui/` always serves the Svelte UI
regardless of the flag.

## mTLS cert flow for the admin path

`make certs` issues four artifacts into `certs/`:

| File | Generated by | Purpose |
|---|---|---|
| `certs/ca.crt`, `certs/ca.key` | `sep2server certs generate-ca` | Root CA used to sign the other three. Trusted by both listeners. |
| `certs/server.crt`, `certs/server.key` | `sep2server certs generate-server` | Server leaf for the SEP2 listener (`SEP2_CERT`, `SEP2_KEY`). |
| `certs/admin.crt`, `certs/admin.key` | `sep2server certs generate-admin` | Operator client cert. Carries the admin policy OID `1.3.6.1.4.1.40732.2.5` so `AdminAuthMiddleware` Path A admits it. |
| `certs/device.crt`, `certs/device.key` | `sep2server certs generate-device` | Sample device cert. CSIP-compliant when `--hw-serial` and `--hw-type` are supplied (see [`csip.md`](csip.md)). |

Two equivalent curl forms hit the cert API. Pick the one that matches
your listener posture:

```bash
# Plain-HTTP admin listener (default for `make run` / `make run-full`).
# #246 loopback bypass admits the request — no Bearer needed.
curl http://localhost:8444/api/certs/ca

# HTTPS admin listener (after `SEP2_ADMIN_TLS=true`). The bypass still
# applies, but mTLS exercises Path A explicitly.
curl --cacert certs/ca.crt \
     --cert  certs/admin.crt \
     --key   certs/admin.key \
     https://localhost:8444/api/certs/ca

# HTTPS admin listener with Bearer (no client cert needed):
curl https://localhost:8444/api/certs/ca \
     --cacert certs/ca.crt \
     -H "Authorization: Bearer $SEP2_ADMIN_KEY"
```

If you hit `error:0A0000C6:SSL routines::packet length too long`, the
listener is in plain-HTTP mode and you sent it TLS bytes — drop the
`https://` or set `SEP2_ADMIN_TLS=true`.

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
[#211](https://github.com/GRIDAPPSD/ieee-2030_5-go/issues/211).

## Cross-references

- [README](../README.md)
- [`admin-ui-selectors.md`](admin-ui-selectors.md) - dashboard selector map
- [`2030_5.md`](2030_5.md) — protocol surface
- [`csip.md`](csip.md) — CSIP V1.2 profile
- [`admin-listener.md`](admin-listener.md) — admin listener TLS posture
- [`glossary.md`](glossary.md) — acronyms and protocol terms
- [`caddy-admin.example.conf`](caddy-admin.example.conf) — example
  Caddy fronting config

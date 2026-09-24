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
which is the admin listener's TLS private key path - see
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

- **HTTP, default - #246 loopback bypass.** Hit
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

Both entry points above assume the browser is on the machine running the
server. To reach the UI from a different machine, follow
[Opening the admin UI in a browser from another machine](#opening-the-admin-ui-in-a-browser-from-another-machine)
below: that path is refused by default and needs three settings the two
above do not.

## Opening the admin UI in a browser from another machine

This is the whole path from a running server to a rendered dashboard, for
an operator whose only tool is a browser. Steps 1 to 5 need no terminal,
no `curl`, no browser extension, no proxy and no devtools override. The
commands in "Server-side configuration" start the server; nothing after
that runs on a command line.

### Server-side configuration

A default admin listener is loopback-only, so it is unreachable from
another machine and the server refuses a non-loopback bind unless you
opt in. Four settings make the browser path work, and the fifth is the
credential:

```bash
export SEP2_ADMIN_LISTEN=0.0.0.0:8444        # or a specific interface IP
export SEP2_ADMIN_ALLOW_NON_LOOPBACK=true    # without this the server does NOT start
export SEP2_ADMIN_TLS=true                   # the session cookie is Secure; see below
export SEP2_ADMIN_ALLOWED_HOSTS=admin.example.com,192.168.1.10
export SEP2_ADMIN_KEY="$(openssl rand -hex 32)"
sep2server serve
```

- `SEP2_ADMIN_ALLOW_NON_LOOPBACK=true` is mandatory for any bind that is
  not loopback. Without it startup fails with an error and opens no
  socket at all, so a missing opt-in looks like a server that will not
  run rather than a UI that will not load. See
  [`admin-listener.md`](admin-listener.md#non-loopback-bind-is-refused-not-warned-365).
- `SEP2_ADMIN_TLS=true` matters because the login cookie is set `Secure`
  and a browser discards a `Secure` cookie that arrives over plain HTTP
  from a non-loopback origin. Serving plain HTTP here breaks the login
  in a way that looks like a server bug; the server warns about it at
  startup. The supported alternative is to terminate TLS in a reverse
  proxy and set `SEP2_ADMIN_BEHIND_PROXY=true`, and that proxy must
  inject `X-Forwarded-For` (or RFC 7239 `Forwarded`): stock nginx does
  not, and without those headers every relayed request looks
  loopback-local and is admitted with no credential at all.
- `SEP2_ADMIN_ALLOWED_HOSTS` must contain the hostname or IP the
  operator types in the address bar. The built-in allowlist covers
  `localhost`, `127.0.0.1`, `::1` and `ieee2030-5.local` only, so a
  request that arrives with `Host: 192.168.1.10:8444` is rejected before
  the login form is reached.
- `SEP2_ADMIN_KEY` is what the operator types into the form. Use a
  high-entropy value: this listener is now reachable from the network.

With `SEP2_ADMIN_TLS=true` and no `SEP2_ADMIN_CERT` / `SEP2_ADMIN_KEY_FILE`,
the server generates a self-signed certificate at startup, and the browser
shows a certificate warning on first visit (step 1 below). Supply an
operator-issued cert to avoid it.

### The five steps in the browser

1. **Open `https://<host>:8444/`**: the address of the admin listener,
   using a hostname or IP that is in `SEP2_ADMIN_ALLOWED_HOSTS`. With a
   self-signed certificate the browser interrupts with a certificate
   warning; accept it for this host and continue. Accepting that warning
   means the server's identity is unverified for this visit, so anyone
   positioned on the network path can read the key you type at step 3:
   use self-signed only on a network you trust, and an operator-issued
   cert everywhere else.
2. **The sign-in page appears.** The server answers an unauthenticated
   page request with a redirect to `/login`, so this is what loads even
   though you typed `/`. The page is a single dark card headed
   **IEEE 2030.5 Admin**, with the line "Sign in to access the server
   dashboard.", one masked field labelled **ADMIN KEY**, and a **Sign in**
   button.
3. **Type the `SEP2_ADMIN_KEY` value into the Admin Key field** and press
   **Sign in**. Nothing else is entered anywhere: there is no username,
   and no header or token to paste.
4. **The dashboard renders.** A successful sign-in returns you to `/` and
   the admin UI loads: a top bar reading **IEEE 2030.5 Server Admin** with
   live TLS / Uptime / Devices readouts, then the panel grid (Overview,
   Server Info, Certificate Management, Send DER Control, Add End Device,
   Lookup Device, Create FSA, FSA templates, the topology tree, the device
   table, and the activity chart). The Uptime and Devices readouts tick,
   which is the stream working as well as the page.
5. **Keep navigating.** The session is a cookie the browser holds, and it
   is not consumed by use, so moving between views and reloading the page
   do not ask for the key again. The session ends on an idle timeout or at
   its absolute lifetime, whichever comes first, and then step 2 appears
   again.

A wrong key re-renders the same page with **Invalid admin key.** below the
button and issues no session, so pressing Sign in again with the correct
value is all that is needed.

### When it does not work

The two failures worth knowing before you start are the first and third
rows: both look like a broken server rather than a configuration gap.

| What you see | Cause | Fix |
|---|---|---|
| The server exits at startup: `admin listener refuses to bind non-loopback address "0.0.0.0:8444"` | Non-loopback bind with no opt-in. No socket was opened. | Set `SEP2_ADMIN_ALLOW_NON_LOOPBACK=true`, or use a bare `:8444` for a loopback-only admin plane. |
| The server exits at startup: `SEP2_ADMIN_KEY is set to whitespace only` | The key is a space, tab or newline. Whitespace is a typo, not a way to disable Bearer auth. | Set a non-blank key, or leave `SEP2_ADMIN_KEY` unset to disable Bearer auth deliberately. |
| Sign in appears to succeed, then every page shows the sign-in form again | The admin listener is plain HTTP on a non-loopback address, so the browser discarded the `Secure` session cookie. The server logged `WARNING: admin listener is plain HTTP on non-loopback address ...` at startup: check the boot log before suspecting the login. | Set `SEP2_ADMIN_TLS=true`, or terminate TLS in a proxy and set `SEP2_ADMIN_BEHIND_PROXY=true`. |
| `421 Misdirected Request` | The `Host` header is not on the allowlist. The gate runs before authentication, so this is not a credential problem and the sign-in page is never reached. The boot log names the allowlist it compared against. | Add the hostname or IP you typed to `SEP2_ADMIN_ALLOWED_HOSTS`. |
| `400 Bad Request: missing Host header` | An HTTP/1.1 request arrived with no `Host`. No browser does this; a hand-built client or a misconfigured proxy does. | Send a `Host` header. |
| The browser reports the connection is not private, with no way past it | A self-signed certificate the browser will not accept for this host. | Accept the exception for this host, or supply an operator-issued cert via `SEP2_ADMIN_CERT` / `SEP2_ADMIN_KEY_FILE`. |
| `error:0A0000C6:SSL routines::packet length too long` in the server log | TLS bytes reached a plain-HTTP listener: the URL says `https://` and `SEP2_ADMIN_TLS` is false. | Set `SEP2_ADMIN_TLS=true`, or use `http://`. |
| A raw `{"error":"admin authentication required"}` page | You navigated straight to an `/api/...` or `/dashboard/...` URL. Those are answered for code, not for a browser, so they return the status rather than the sign-in page. | Open `/` and sign in first. |

## Auth model

`AdminAuthMiddleware`
([`internal/auth/admin.go`](../internal/auth/admin.go)) gates everything
behind the login routes. Five paths are checked in order; **any single
path that succeeds admits the request** (this is fallback ordering, not
defense in depth):

0. **Loopback bypass (#246)** - RemoteAddr is a loopback address
   (127.0.0.0/8 or `::1`) AND the request carries no reverse-proxy
   forwarded header (`X-Forwarded-For`, `X-Forwarded-Host`,
   `X-Forwarded-Proto`, RFC 7239 `Forwarded`). Local-developer
   ergonomic path: `make run` on localhost has no working credentials
   by default, and a reverse proxy in front injects `X-Forwarded-*` so
   the bypass declines automatically for production traffic. Every
   admission is logged.

   **Every admin write route refuses this bypass on its own by default
   (#579, #631), with a named exemption list:** the exceptions are the
   end-device and FSA management routes (create, delete, and their
   program and assignment routes), because those routes only create or
   delete configuration rows and never return key material, a captured
   header, or a ticket. A write route not on that list requires a real
   credential (paths 1 to 4 below) even from a loopback address, so a new
   route is protected the moment it exists rather than only once someone
   remembers to add it to a list.

   Two route families are also named directly, regardless of method: the
   certificate routes (`/api/certs/*`, since they mint and return key
   material) and the traffic-capture routes (`/api/traffic/*`, since they
   return captured `Authorization` and `Cookie` header bytes verbatim). Both
   require a real credential on every method, GET reads included.

   On any of these routes, a request the bypass alone would admit is
   refused with a 401, logged at WARN as `admin: sensitive route refused
   bypass-only admission`. This is a credential requirement, not a
   loopback ban - a valid credential presented from a loopback address
   still succeeds on these routes too.
1. **[mTLS](glossary.md)** - peer cert with the IEEE 2030.5 admin policy OID
   `1.3.6.1.4.1.40732.2.5` (matched by
   [`certs.HasPolicyOID`](../internal/certs/oids.go)).
2. **Bearer** - `Authorization: Bearer <SEP2_ADMIN_KEY>`. Disabled when
   `SEP2_ADMIN_KEY` is empty.
3. **Query-param ticket** - `?ticket=<value>` redeemed against the
   `TicketStore`. One-time use. Used by browser SSE / EventSource
   clients that cannot send `Authorization` headers.
4. **Cookie session**: the `admin_ticket` cookie validated against the
   `SessionStore`, a store separate from the query-ticket one. Validation
   slides the session's idle deadline but does NOT consume it, and no
   response re-sets the cookie: one page load is four independent
   authentications (document, script, stylesheet, icon) arriving in
   parallel, so a consuming check or a per-request rotation admits the
   first and refuses the rest. The absolute lifetime is not extendable.
   Cookie is `HttpOnly`, `Secure`, `SameSite=Strict`, `Path=/`; see
   `NewAdminTicketCookie` in `admin.go`.

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
| Dashboard page | Complete | `GET /` renders the operator dashboard: the embedded Svelte admin UI, or the pre-Svelte page with `SEP2_ADMIN_LEGACY_DASHBOARD=true`. | [`internal/server/dashboard.go`](../internal/server/dashboard.go), [`frontend/src/AdminShell.svelte`](../internal/server/web/frontend/src/AdminShell.svelte) |
| Live dashboard data | Complete | `GET /dashboard/data` returns JSON; `GET /dashboard/events` is the SSE stream pushing 5-second updates. | [`internal/server/dashboard.go`](../internal/server/dashboard.go), [`frontend/src/lib/dashboard.ts`](../internal/server/web/frontend/src/lib/dashboard.ts) |
| Auth ticket exchange | Complete | `POST /auth/ticket` exchanges a valid admin session for a one-time-use ticket. Used by SSE clients. | [`internal/server/admin_router.go`](../internal/server/admin_router.go), [`internal/auth/ticket.go`](../internal/auth/ticket.go), [`frontend/src/lib/dashboard.ts`](../internal/server/web/frontend/src/lib/dashboard.ts) |
| Cert management API | Complete | `GET /api/certs/ca` (download CA), `POST /api/certs/server`, `POST /api/certs/device`. The dashboard's "Certificate Management" card reaches the CA download and the device cert. `POST /api/certs/server` is CLI and curl only by design: it returns a server private key, which a browser panel has no business receiving. | [`internal/handler/admin_certs.go`](../internal/handler/admin_certs.go), [`frontend/src/panels/CertPanel.svelte`](../internal/server/web/frontend/src/panels/CertPanel.svelte) |
| Cert info parser | Complete | `POST /api/certs/info` parses a pasted PEM cert and returns SFDI + LFDI. Used by the "Add End Device" form to auto-fill identity. | [`internal/handler/admin_register.go`](../internal/handler/admin_register.go), [`frontend/src/panels/AddDevice.svelte`](../internal/server/web/frontend/src/panels/AddDevice.svelte) |
| EndDevice registration | Complete | `POST /api/devices` creates an EndDevice + Registration with a PIN (#159). `GET /api/devices/by-lfdi/{lfdi}` looks up by LFDI. Both wired into the dashboard. | [`internal/handler/admin_register.go`](../internal/handler/admin_register.go), [`frontend/src/panels/AddDevice.svelte`](../internal/server/web/frontend/src/panels/AddDevice.svelte), [`frontend/src/panels/LookupDevice.svelte`](../internal/server/web/frontend/src/panels/LookupDevice.svelte) |
| FSA management | Complete | Create/list/get/delete admin FSA templates, attach/detach DERPrograms, assign/unassign devices, plus a topology endpoint for the dashboard tree (#163). Create is the Create FSA card, attach/detach and delete are the tree's per-FSA controls, assign is the device table, unassign is the FSA template table. | [`internal/handler/admin_fsa.go`](../internal/handler/admin_fsa.go), [`internal/server/admin_fsa_wiring.go`](../internal/server/admin_fsa_wiring.go), [`frontend/src/panels/CreateFsa.svelte`](../internal/server/web/frontend/src/panels/CreateFsa.svelte), [`frontend/src/panels/FsaNode.svelte`](../internal/server/web/frontend/src/panels/FsaNode.svelte), [`frontend/src/panels/FsaCatalog.svelte`](../internal/server/web/frontend/src/panels/FsaCatalog.svelte) |
| Management-pair provisioning | Complete | `POST /api/management-pairs` creates a (manager LFDI, managed LFDI) pair; `GET /api/management-pairs?manager=` or `?managed=` lists in either direction; `DELETE /api/management-pairs?managed=` removes one; `POST /api/management-pairs/rekey` replaces a manager or managed LFDI across its pairs after a certificate rotation (#440). Every LFDI is normalized to 40 uppercase hex digits and refused only when it is not valid hexBinary. Pairs persist to `<SEP2_DATA_DIR>/enddevicemanagement.json` when a data directory is configured, the same as the other admin-mutated stores. See [`enddevice-access.md`](enddevice-access.md) for what a pair grants. No dashboard panel yet. | [`internal/handler/admin_management.go`](../internal/handler/admin_management.go), [`internal/server/admin_management_wiring.go`](../internal/server/admin_management_wiring.go), [`pkg/store/memory/enddevicemanagement.go`](../pkg/store/memory/enddevicemanagement.go) |
| Topology view | Complete | `GET /api/topology` returns the SY / FD / SP / DEV tree the dashboard renders. | [`internal/handler/admin_topology.go`](../internal/handler/admin_topology.go), [`frontend/src/panels/TopologyTree.svelte`](../internal/server/web/frontend/src/panels/TopologyTree.svelte) |
| DER control submit form | In Progress | The "Send DER Control" form renders and validates, but its submit is still a stub: it echoes the selection and sends nothing. The blocker is a missing backend route (`POST /api/der/controls`), not the UI. | [`frontend/src/panels/DerControl.svelte`](../internal/server/web/frontend/src/panels/DerControl.svelte) |
| Traffic capture API | In Progress | `GET /api/traffic/{clients,exchanges,exchanges/{id},exchanges/{id}/request,exchanges/{id}/response,stream,stats}` (#611): read-only access to recorded protocol exchanges, on the same auth chain as every other admin route (including the ticket path for `GET /api/traffic/stream`). A non-GET method on any of these routes answers 405, not 404. Off unless `SEP2_TRAFFIC_CAPTURE=true` permits it; when permitted, the directory is `SEP2_TRAFFIC_DIR`, else `<SEP2_DATA_DIR>/traffic`. No dashboard panel yet; that is a later PR. | [`pkg/sep2capture/`](../pkg/sep2capture/), [`internal/server/admin_router.go`](../internal/server/admin_router.go) |

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

The body is normalized, not echoed: it is the certificate's own DER,
re-encoded canonically, never the stored CA file's raw bytes. A file with
unusual wrapping, line endings, or a legacy label still loads and comes
back as a standard 64-column `CERTIFICATE` block.

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

Both cert-creation routes decode JSON, so a curl call needs an explicit
`Content-Type` header (#416); a missing or mismatched one is refused with
a 415 naming the type the route accepts:

```bash
curl -X POST http://localhost:8444/api/certs/server \
     -H "Content-Type: application/json" \
     -d '{"hosts":["sep2.example.org"],"commonName":"sep2.example.org"}'

curl -X POST http://localhost:8444/api/certs/device \
     -H "Content-Type: application/json" \
     -d '{"hwSerialNum":"0123456789AB","hwType":"1.3.6.1.4.1.40732.99"}'
```

Each returns `certPEM` and `keyPEM` as JSON string fields; extract them
(for example with `jq -r .keyPEM`) rather than saving the response
envelope itself as a `.crt` or `.key` file.

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
| `certs/ca.crt`, `certs/ca.key` | `sep2server certs generate-ca` | Root CA used to sign the other three (`SEP2_CA`, `SEP2_CA_KEY`). Trusted by both listeners. |
| `certs/server.crt`, `certs/server.key` | `sep2server certs generate-server` | Server leaf for the SEP2 listener (`SEP2_CERT`, `SEP2_KEY`). |
| `certs/admin.crt`, `certs/admin.key` | `sep2server certs generate-admin` | Operator client cert. Carries the admin policy OID `1.3.6.1.4.1.40732.2.5` so `AdminAuthMiddleware` Path A admits it. |
| `certs/device.crt`, `certs/device.key` | `sep2server certs generate-device` | Sample device cert. CSIP-compliant when `--hw-serial` and `--hw-type` are supplied (see [`csip.md`](csip.md)). |

`SEP2_CA` / `SEP2_CA_KEY` can be split into a serving role and a device
role that each sign only their own certificates, with independent key
settings; see operator-guide.md's
[Splitting the serving and device CAs](operator-guide.md#splitting-the-serving-and-device-cas-optional)
section.

Two equivalent curl forms hit the cert API. Pick the one that matches
your listener posture:

```bash
# Plain-HTTP admin listener (default for `make run` / `make run-full`).
# #246 loopback bypass admits the request - no Bearer needed.
curl -f http://localhost:8444/api/certs/ca

# HTTPS admin listener (after `SEP2_ADMIN_TLS=true`). The bypass still
# applies, but mTLS exercises Path A explicitly.
curl -f --cacert certs/ca.crt \
     --cert  certs/admin.crt \
     --key   certs/admin.key \
     https://localhost:8444/api/certs/ca

# HTTPS admin listener with Bearer (no client cert needed):
curl -f https://localhost:8444/api/certs/ca \
     --cacert certs/ca.crt \
     -H "Authorization: Bearer $SEP2_ADMIN_KEY"
```

`-f` makes curl fail on a non-2xx response instead of writing the error
body to stdout (or into a saved file if redirected), where it could be
mistaken for a valid CA.

If you hit `error:0A0000C6:SSL routines::packet length too long`, the
listener is in plain-HTTP mode and you sent it TLS bytes - drop the
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
- [`operator-guide.md`](operator-guide.md) - operator walkthrough of the admin dashboard
- [`admin-ui-selectors.md`](admin-ui-selectors.md) - dashboard selector map
- [`2030_5.md`](2030_5.md) - protocol surface
- [`csip.md`](csip.md) - CSIP V1.2 profile
- [`admin-listener.md`](admin-listener.md) - admin listener TLS posture
- [`glossary.md`](glossary.md) - acronyms and protocol terms
- [`caddy-admin.example.conf`](caddy-admin.example.conf) - example
  Caddy fronting config

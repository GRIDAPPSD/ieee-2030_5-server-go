# Admin Listener

IEEE 2030.5 server runs two listeners. They have different jobs and
intentionally different TLS postures.

## SEP2 protocol listener (`SEP2_ADDR`, default `:443`)

This is the wire spec'd by IEEE 2030.5 / CSIP V1.2. Every connecting peer
is a device or another aggregator and MUST present a certificate. The
listener uses `RequireAnyClientCert` plus a manual chain walk (so we can
acknowledge CSIP's critical `HardwareModuleName` SAN before stdlib chain
validation). No browser can connect here without a client cert. That's the
point.

## Admin listener (`SEP2_ADMIN_LISTEN`, optional)

Operator surface: dashboard, login, cert-paste utility, add-device form, and
the certificate management API. A browser cannot present a client cert by
default, so the admin listener can't share the SEP2 posture. The admin
listener is therefore split off entirely. It runs on its own port and
selects its TLS posture independently. `AdminAuthMiddleware` still gates the
auth model — mTLS (operator cert), Bearer token, query-param ticket, and
the IEEE-095 cookie all keep working.

The `SEP2_ADMIN_ADDR` env var from the pre-IEEE-094 deployment is preserved
as a deprecated alias: empty `SEP2_ADMIN_LISTEN` falls back to it.

### Loopback by default (IEEE-136)

A bare-port value (`:8444`, `:9443`) binds the admin listener to `127.0.0.1`
by default, NOT `0.0.0.0`. This makes the admin surface safe-by-default —
the SEP2 protocol listener admits any self-signed client cert
(`tls.RequireAnyClientCert`), and the admin auth middleware's Path 0
admits loopback requests with no proxy headers, so a `0.0.0.0` admin bind
combined with the loopback bypass would hand the admin API to any network
neighbor (or any co-resident process on a multi-tenant host). Defaulting
bare ports to loopback closes that compound vulnerability.

| Operator input | Effective bind |
|---|---|
| `SEP2_ADMIN_LISTEN=:8444` | `127.0.0.1:8444` (loopback default) |
| `SEP2_ADMIN_LISTEN=127.0.0.1:8444` | `127.0.0.1:8444` (explicit, unchanged) |
| `SEP2_ADMIN_LISTEN=0.0.0.0:8444` | `0.0.0.0:8444` (explicit public, unchanged) |
| `SEP2_ADMIN_LISTEN=192.168.1.5:8444` | `192.168.1.5:8444` (explicit interface, unchanged) |
| `SEP2_ADMIN_LISTEN=` (empty) | admin disabled |

To expose admin off-box, name the bind explicitly: `SEP2_ADMIN_LISTEN=0.0.0.0:8444`
(or a specific interface IP). The `make run`/`run-ccm`/`run-full` dev
targets set `SEP2_ADMIN_ADDR=:8444` and now bind to loopback. To run
those targets with a network-reachable admin, override:
`SEP2_ADMIN_LISTEN=0.0.0.0:8444 make run-ccm`.

## TLS posture matrix

| `SEP2_ADMIN_LISTEN` / `SEP2_ADMIN_ADDR` | `SEP2_ADMIN_TLS` | `SEP2_ADMIN_CERT` / `SEP2_ADMIN_KEY_FILE` | Behavior |
|---|---|---|---|
| empty | — | — | Admin listener disabled (no admin surface) |
| set | `false` (default) | — | Plain HTTP on the admin port — intended for Caddy in front |
| set | `true` | set | HTTPS with operator-supplied cert/key (`VerifyClientCertIfGiven`) |
| set | `true` | empty | HTTPS with a freshly generated self-signed cert (`VerifyClientCertIfGiven`) — operator must trust on first use |

When HTTPS is enabled, `ClientAuth` is `VerifyClientCertIfGiven`. An
operator client that does present a cert and chains to a known CA gets the
mTLS Path A in `AdminAuthMiddleware`. A browser that presents no cert still
completes the TLS handshake and can authenticate via Bearer or cookie.

## Caddy fronting (recommended for production)

Run the admin listener in plain-HTTP mode and put Caddy in front. Caddy
terminates TLS (Let's Encrypt or a private cert), and the upstream is
`localhost:<SEP2_ADMIN_LISTEN port>`. The minimal Caddyfile snippet lives
at `docs/caddy-admin.example.conf`. Bind the admin port to localhost only
(`SEP2_ADMIN_LISTEN=127.0.0.1:9443`) so the plain-HTTP socket is never
exposed off-box.

Caddy injects `X-Forwarded-For` by default, so the IEEE-132 loopback
bypass declines automatically and normal Bearer / cookie / mTLS auth
runs against operator requests routed through Caddy. Proxies that strip
`X-Forwarded-*` would defeat this safety; verify your proxy preserves
the `Forwarded-*` headers (or `Forwarded` per RFC 7239).

### Reverse-proxy XFF requirement (IEEE-137)

`AdminAuthMiddleware` Path 0 admits requests that arrive over loopback
with no proxy headers. That bypass is intentional — it makes a local
operator session usable without juggling Bearer tokens — but it has a
sharp edge when an upstream reverse proxy fronts the admin listener
without injecting `X-Forwarded-For` or RFC 7239 `Forwarded`. In that
configuration the proxy relays public traffic to the loopback admin
socket, the relayed connection looks loopback-local from the listener's
view, and Path 0 admits with no creds.

**nginx's default config does NOT inject `X-Forwarded-*`.** A stock-
nginx admin front-end would silently expose the admin surface and the
cert API to public traffic. Caddy injects `X-Forwarded-For` by default;
HAProxy needs `option forwardfor`; AWS ALB injects automatically;
Traefik injects by default. Verify your proxy.

When the admin listener is bound to a non-loopback address
(`SEP2_ADMIN_LISTEN=0.0.0.0:8444` or any specific interface IP) and
`SEP2_ADMIN_BEHIND_PROXY=true` is NOT set, the server logs a startup
WARNING explaining the requirement. Set `SEP2_ADMIN_BEHIND_PROXY=true`
once the upstream proxy is verified to inject the headers.

For loopback-only admin (the IEEE-136 default for bare-port input),
the warning does not fire — there is no proxy gap to mind because no
public traffic can reach the listener.

If you hit `error:0A0000C6:SSL routines::packet length too long`, the
admin listener is in plain-HTTP mode and you sent it TLS bytes — drop
the `https://` or set `SEP2_ADMIN_TLS=true`.

## Host-header allowlist (DNS-rebinding defense, IEEE-138)

The admin listener wraps every request in a Host-header allowlist before
the auth chain runs. The threat: with `ieee2030-5.local` advertised over
mDNS (IEEE-133) and a loopback admin bind, a DNS-rebinding attacker can
resolve a malicious domain to `127.0.0.1`, lure a browser to a page
hosted at that domain, and have the browser's same-origin requests hit
the admin port. The IEEE-132 loopback bypass admits because RemoteAddr
is loopback. The host gate is the defense-in-depth.

A request whose `Host` header is not on the allowlist receives HTTP 421
Misdirected Request and never reaches the auth middleware. An empty
`Host` on HTTP/1.1 returns 400 (RFC 7230 §5.4 violation).

The static defaults — always installed — are:

- `localhost`
- `127.0.0.1`
- `::1`
- `ieee2030-5.local` (the IEEE-133 mDNS hostname)

Allowlist entries match BOTH the bare host and the host-with-port form,
so an entry of `localhost` matches a `Host: localhost:8444` header from
a browser hitting the default admin bind.

`SEP2_ADMIN_ALLOWED_HOSTS` is a CSV of additional entries appended to
the defaults. There is no opt-out: the local-dev defaults stay
installed even when the env var is set. Use it when fronting the admin
listener with Caddy on a public hostname:

```bash
export SEP2_ADMIN_ALLOWED_HOSTS=admin.example.com,admin.example.lan
```

The protocol listener at `:443` is NOT host-gated: devices cannot be
relied upon to send a particular `Host` header, and the SEP2 wire is
already protected by `RequireAnyClientCert` plus a manual chain walk.
This gate is admin-mux-only.

## Quick start

```bash
# Caddy mode: plain HTTP on the admin port, Caddy terminates externally
export SEP2_ADMIN_LISTEN=127.0.0.1:9443
export SEP2_ADMIN_TLS=false
export SEP2_ADMIN_KEY=<bearer-token>

# Direct HTTPS with self-signed cert (dev / kiosk)
export SEP2_ADMIN_LISTEN=:9443
export SEP2_ADMIN_TLS=true
export SEP2_ADMIN_KEY=<bearer-token>

# Direct HTTPS with operator cert (private CA, no Caddy)
export SEP2_ADMIN_LISTEN=:9443
export SEP2_ADMIN_TLS=true
export SEP2_ADMIN_CERT=/etc/ssl/admin.crt
export SEP2_ADMIN_KEY_FILE=/etc/ssl/admin.key
export SEP2_ADMIN_KEY=<bearer-token>
```

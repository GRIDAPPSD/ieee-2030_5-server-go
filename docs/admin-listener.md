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

If you hit `error:0A0000C6:SSL routines::packet length too long`, the
admin listener is in plain-HTTP mode and you sent it TLS bytes — drop
the `https://` or set `SEP2_ADMIN_TLS=true`.

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

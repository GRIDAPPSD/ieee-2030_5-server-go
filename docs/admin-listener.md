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
auth model: mTLS (operator cert), Bearer token, query-param ticket, and
the #159 cookie all keep working.

The `SEP2_ADMIN_ADDR` env var from the pre-#161 deployment is preserved
as a deprecated alias: empty `SEP2_ADMIN_LISTEN` falls back to it.

### Loopback by default (#268)

A bare-port value (`:8444`, `:9443`) binds the admin listener to `127.0.0.1`
by default, NOT `0.0.0.0`. This makes the admin surface safe-by-default:
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
| `SEP2_ADMIN_LISTEN=0.0.0.0:8444` | `0.0.0.0:8444`, and startup REFUSES without the opt-in below |
| `SEP2_ADMIN_LISTEN=192.168.1.5:8444` | `192.168.1.5:8444`, and startup REFUSES without the opt-in below |
| `SEP2_ADMIN_LISTEN=` (empty) | admin disabled |

### Non-loopback bind is refused, not warned (#365)

Naming an explicit non-loopback bind is not on its own enough. Startup
FAILS with an error and opens no socket unless
`SEP2_ADMIN_ALLOW_NON_LOOPBACK=true` is also set:

```
admin listener refuses to bind non-loopback address "0.0.0.0:8444", which is
reachable from outside this host: set SEP2_ADMIN_ALLOW_NON_LOOPBACK=true to
allow it, or set SEP2_ADMIN_LISTEN to a bare :<port> for a loopback-only
admin plane
```

Anything that is not positively a loopback address counts as non-loopback,
including the unspecified addresses `0.0.0.0` and `[::]` (they bind every
interface), an unresolved hostname, and a malformed address. The check is
answered before `net.Listen` runs, so a refused configuration never has a
listening socket even briefly.

To expose admin off-box, name the bind explicitly AND opt in:

```bash
export SEP2_ADMIN_LISTEN=0.0.0.0:8444
export SEP2_ADMIN_ALLOW_NON_LOOPBACK=true
```

The `make run`/`run-ccm`/`run-full` dev targets set `SEP2_ADMIN_ADDR=:8444`
and bind to loopback, so they are unaffected. To run those targets with a
network-reachable admin, override both:
`SEP2_ADMIN_LISTEN=0.0.0.0:8444 SEP2_ADMIN_ALLOW_NON_LOOPBACK=true make run-ccm`.

The opt-in is one of the settings a browser on another machine needs. For the
full operator path from a running server to a rendered dashboard, including the
Host allowlist and the `Secure` cookie requirement, see
[Opening the admin UI in a browser from another machine](admin.md#opening-the-admin-ui-in-a-browser-from-another-machine).

## TLS posture matrix

| `SEP2_ADMIN_LISTEN` / `SEP2_ADMIN_ADDR` | `SEP2_ADMIN_TLS` | `SEP2_ADMIN_CERT` / `SEP2_ADMIN_KEY_FILE` | Behavior |
|---|---|---|---|
| empty | - | - | Admin listener disabled (no admin surface) |
| set | `false` (default) | - | Plain HTTP on the admin port: intended for Caddy in front |
| set | `true` | set | HTTPS with operator-supplied cert/key (`VerifyClientCertIfGiven`) |
| set | `true` | empty | HTTPS with a freshly generated self-signed cert (`VerifyClientCertIfGiven`); operator must trust on first use |

When HTTPS is enabled, `ClientAuth` is `VerifyClientCertIfGiven`. An
operator client that does present a cert and chains to the admin listener's
client-CA anchor (below) gets the mTLS Path A in `AdminAuthMiddleware`. A
browser that presents no cert still completes the TLS handshake and can
authenticate via Bearer or cookie.

### Admin client-certificate trust anchor (`SEP2_ADMIN_CLIENT_CA`, #624)

The admin listener verifies a presented operator certificate against
`SEP2_ADMIN_CLIENT_CA`. Unset, this defaults to the serving CA
(`SEP2_SERVING_CA`, or `SEP2_CA` where the two are not split): the CA that
signs the operator certificate the certs API mints, so a freshly minted
operator cert works with no extra client-side configuration. Set it to a
PEM file path to trust a different CA instead, or to the literal value
`system` to fall back to the host's root trust store: the behavior before
#624, useful only for a deployment whose operator certificates come from a
public CA rather than this server's own serving CA.

A certificate signed by any CA outside this anchor is refused at the TLS
handshake if the client presents it. Whether a client presents it at all is
up to the client: some honor the server's CertificateRequest hint and omit
a certificate that does not chain to the advertised CAs, connecting
certless instead, but `curl --cert` is not one of them - it sends the
certificate regardless, and the connection fails with a TLS alert
(measured: curl 8.14.1 / OpenSSL 3.5.4, `SSL_read: ... tlsv1 alert unknown
ca`). Either way the admin policy-OID check is never reached: a refused
handshake never gets there, and an omitted certificate reaches it as a
certless connection, the same as a browser that never had one.

## Caddy fronting (recommended for production)

Run the admin listener in plain-HTTP mode and put Caddy in front. Caddy
terminates TLS (Let's Encrypt or a private cert), and the upstream is
`localhost:<SEP2_ADMIN_LISTEN port>`. The minimal Caddyfile snippet lives
at `docs/caddy-admin.example.conf`. Bind the admin port to localhost only
(`SEP2_ADMIN_LISTEN=127.0.0.1:9443`) so the plain-HTTP socket is never
exposed off-box.

Caddy injects `X-Forwarded-For` by default, so the #246 loopback
bypass declines automatically and normal Bearer / cookie / mTLS auth
runs against operator requests routed through Caddy. Proxies that strip
`X-Forwarded-*` would defeat this safety; verify your proxy preserves
the `Forwarded-*` headers (or `Forwarded` per RFC 7239).

### Reverse-proxy XFF requirement (#269)

`AdminAuthMiddleware` Path 0 admits requests that arrive over loopback
with no proxy headers. That bypass is intentional: it makes a local
operator session usable without juggling Bearer tokens, but it has a
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
(`SEP2_ADMIN_LISTEN=0.0.0.0:8444` or any specific interface IP, which also
requires `SEP2_ADMIN_ALLOW_NON_LOOPBACK=true` to start at all) and
`SEP2_ADMIN_BEHIND_PROXY=true` is NOT set, the server logs a startup
WARNING explaining the requirement. Set `SEP2_ADMIN_BEHIND_PROXY=true`
once the upstream proxy is verified to inject the headers.

For loopback-only admin (the #268 default for bare-port input),
the warning does not fire: there is no proxy gap to mind because no
public traffic can reach the listener.

If you hit `error:0A0000C6:SSL routines::packet length too long`, the
admin listener is in plain-HTTP mode and you sent it TLS bytes: drop
the `https://` or set `SEP2_ADMIN_TLS=true`.

### Plain HTTP plus the Secure session cookie (#365)

The browser login flow sets `admin_ticket` with the `Secure` attribute, and a
browser discards a `Secure` cookie that arrives over plain HTTP from a
non-loopback origin. A plain-HTTP admin listener reached directly from another
host therefore cannot complete a login: the POST appears to succeed and every
request after it is unauthenticated.

Startup warns when all three hold: `SEP2_ADMIN_TLS` is false, the bind is
non-loopback, and `SEP2_ADMIN_BEHIND_PROXY` is not set. Either serve HTTPS
directly with `SEP2_ADMIN_TLS=true`, or terminate TLS in an upstream proxy and
set `SEP2_ADMIN_BEHIND_PROXY=true`. Bearer and mTLS clients are unaffected;
only the cookie flow is.

Loopback is exempt: a browser treats a loopback origin as
potentially-trustworthy and keeps the cookie, which is why `make run` works
over plain HTTP.

This failure and the `Host`-allowlist rejection are the two that read as a
server bug rather than a configuration gap; both are in the troubleshooting
table under
[Opening the admin UI in a browser from another machine](admin.md#opening-the-admin-ui-in-a-browser-from-another-machine).

## Admin Bearer key (`SEP2_ADMIN_KEY`, #365)

Three states, deliberately distinguished:

| `SEP2_ADMIN_KEY` | Behavior |
|---|---|
| unset | Bearer auth disabled; mTLS, ticket and cookie paths still work |
| whitespace only (a space, tab, newline, or any mix) | HARD startup error, no socket opens |
| anything with a non-whitespace character | Bearer auth enabled with that value |

Whitespace-only is a startup failure rather than a silent disable. An operator
who typed a space was trying to set a key, and silently disabling Bearer auth
turns that typo into a connection refusal much later, in a place that does not
name the cause. The error names `SEP2_ADMIN_KEY` and never echoes the value.

### The key is matched byte-for-byte

A non-blank key is never trimmed. `SEP2_ADMIN_KEY="s3cret "` is the seven-byte
value including the trailing space, and only a caller presenting those exact
bytes is admitted. Trimming it would change the secret the server accepts
without saying so.

**Do not configure a key with leading or trailing whitespace.** HTTP header
parsing strips surrounding whitespace from a field value, so such a key cannot
be presented byte-for-byte over a real connection and every request will be
refused. Quote your value in shell exports and check it: a trailing space that
survived a copy and paste looks identical to a working key.

The `Bearer` scheme token itself is matched case-insensitively per RFC 7235
section 2.1, so `bearer`, `Bearer` and `BEARER` are equivalent. The key that
follows it is not.

## Host-header allowlist (DNS-rebinding defense, #270)

The admin listener wraps every request in a Host-header allowlist before
the auth chain runs. The threat: with `ieee2030-5.local` advertised over
mDNS (#246) and a loopback admin bind, a DNS-rebinding attacker can
resolve a malicious domain to `127.0.0.1`, lure a browser to a page
hosted at that domain, and have the browser's same-origin requests hit
the admin port. The #246 loopback bypass admits because RemoteAddr
is loopback. The host gate is the defense-in-depth.

A request whose `Host` header is not on the allowlist receives HTTP 421
Misdirected Request and never reaches the auth middleware. An empty
`Host` on HTTP/1.1 returns 400 (RFC 7230 section 5.4 violation).

The static defaults (always installed) are:

- `localhost`
- `127.0.0.1`
- `::1`
- `ieee2030-5.local` (the #246 mDNS hostname)

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

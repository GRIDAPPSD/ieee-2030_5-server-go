# Container runtime (IEEE-177)

How `sep2server` runs as a container in the observability stack, how its certs
are bootstrapped, and how the development and production cert paths share a
single seam so switching between them is a mount change — never a code or image
change.

Builds on IEEE-172 (distroless/static:nonroot Dockerfile, JSON-on-stdout
logging) and IEEE-175 (hardened `.dockerignore`).

## TL;DR

- Entrypoint is the in-binary `serve-init` subcommand (no shell in the image).
- On start it generates a CA + server + admin cert set into `/certs` **only if
  `/certs` has no `server.crt`** — the *gen-only-if-empty seam*.
- `/certs` is a named volume, so the cert (and the LFDI/SFDI identity derived
  from it) persists across restarts. A restart never mints a new identity.
- Logs flow to Loki via the `obs.service=sep2server` Docker label.

## Why a Go subcommand, not a shell entrypoint

The final image is `gcr.io/distroless/static-debian12:nonroot`: no shell, no
`mkdir`, no `chown`, no package manager. A shell `docker-entrypoint.sh` would
have forced a base-image swap to `busybox`/`alpine:nonroot`, adding a shell and
its injection surface to a server that handles cert private keys.

That swap is unnecessary here: cert generation is already pure Go
(`internal/certs`). The `serve-init` subcommand (`cmd/sep2server/cmd_serve_init.go`)
does the gen-only-if-empty check and then falls through to the normal serve
path. Benefits:

- The hardened distroless/static base is preserved (no shell).
- `os.WriteFile`/`os.Chmod` set the exact key mode (0600). A shell `umask`
  dance can only approximate this and is easy to get wrong.
- No new attack surface: one binary, no interpreter.

This is the tradeoff the IEEE-177 card asked to resolve explicitly: we kept
static+nonroot and did cert generation in Go rather than changing the base.

## The `/certs` named volume and nonroot writability

A fresh Docker named volume is root-owned by default. The nonroot user (uid:gid
`65532:65532`, the distroless `nonroot` user) cannot write to a root-owned
mountpoint.

Fix: the image **pre-creates `/certs` owned by 65532 at mode 0700** (see the
`COPY --from=builder --chown=65532:65532 /out/certs /certs` line in the
Dockerfile). When Docker first mounts a fresh named volume over a path that
already exists in the image, it seeds the empty volume from that mountpoint —
**including its ownership**. So the volume comes up owned by 65532 and the
nonroot entrypoint can write into it on first start.

Verified on a live run: every file in the volume is owned `65532:65532`, and
the three `*.key` files are mode `0600` (`-rw-------`), certs `0644`.

> If `/certs` did NOT exist in the image, the volume would be created
> root-owned and the nonroot process would fail to write. Pre-creating it owned
> by 65532 is the load-bearing step.

## Cert handling invariants

- **Private keys are owner-only.** Generated `ca.key`, `server.key`, `admin.key`
  are written at mode 0600 and `os.Chmod`-enforced. Never group/world readable,
  never logged, never baked into an image layer (`.dockerignore` excludes
  `*.pem`/`*.key`/`*.crt`/`testdata/` — IEEE-175).
- **Identity persistence.** The server derives its LFDI/SFDI from its leaf cert
  (`internal/server.deriveServerIdentity`). Persisting `server.crt`/`server.key`
  in the named volume is therefore what keeps the network identity stable. A
  restart reuses the leaf — verified: the server cert SHA-256 fingerprint and
  LFDI are identical before and after `docker compose restart`.
- **No secrets in image/compose/git.** Nothing committed contains a key or
  password. The compose `SEP2_ADMIN_KEY` is intentionally left unset inline;
  supply it via an `env_file` or a secret in a real deployment.

## Development path (implemented)

`docker compose up -d` against `docker-compose.yml`:

1. `serve-init` finds `/certs` empty → generates CA + server + admin into the
   `certs` named volume as the nonroot user, keys at 0600.
2. Server binds `:8443` (CCM-8), derives identity from the generated leaf.
3. Promtail keeps the container (label `obs.service=sep2server`) and ships its
   JSON stdout to Loki under `service="sep2server"`.
4. A restart finds `server.crt` present → **skips generation**, reuses the
   identity.

Query the logs:

```bash
curl -sG 'http://localhost:3100/loki/api/v1/query_range' \
  --data-urlencode 'query={service="sep2server"}'
```

The self-signed dev CA is fine for the lab/observability stack. It is NOT a
production trust anchor.

## Production path (design — the secret-mount drop-in)

The production posture is **externally-managed certs**: a CA you actually trust
issues the server leaf, and the leaf + key are delivered to the container as a
secret mounted at `/certs`. No code or image change is needed — this drops into
the exact same `/certs`-empty seam, because `serve-init` skips generation
whenever `/certs/server.crt` already exists.

### Why no change is needed

`ensureCerts` keys its decision on the presence of `/certs/server.crt`. A
mounted production secret puts that file there *before* the process starts, so
`serve-init` sees a populated `/certs`, skips generation, and serves the
operator's cert verbatim. (Covered by the
`TestEnsureCertsHonorsMountedProdSecret` test, which mounts a foreign-CA leaf
and asserts it is left untouched.)

### Docker Compose secrets sketch

```yaml
services:
  sep2server:
    image: gridappsd/sep2server:<pinned-tag>
    environment:
      SEP2_ADDR: ":8443"
      SEP2_CERT_DIR: "/certs"
      SEP2_CCM: "true"
    secrets:
      - source: sep2_server_cert
        target: /certs/server.crt
        mode: 0444
      - source: sep2_server_key
        target: /certs/server.key
        uid: "65532"
        gid: "65532"
        mode: 0400
      - source: sep2_ca_cert
        target: /certs/ca.crt
        mode: 0444
    user: "65532:65532"
    networks: [obs]
    labels: ["obs.service=sep2server"]

secrets:
  sep2_server_cert:
    file: ./secrets/server.crt        # delivered out-of-band; never committed
  sep2_server_key:
    file: ./secrets/server.key        # 0400, owned 65532; never committed
  sep2_ca_cert:
    file: ./secrets/ca.crt
```

Notes for the prod path:

- **No `ca.key` is mounted.** Production does not need the CA private key on the
  server host; the admin cert-issuing API is simply disabled when `ca.key` is
  absent (the server logs "CA not loaded — admin cert API disabled" and serves
  normally). Keep the CA key in your PKI/HSM, off the server box. If you DO want
  the in-server device-cert API, mount `ca.key` too at 0400/65532 — but prefer
  issuing device certs out-of-band.
- **Key file permission/ownership.** Mount the key at mode 0400 owned by 65532
  so only the server process reads it. Swarm/compose secret `uid`/`gid`/`mode`
  fields handle this; a bind-mount must be pre-set on the host.
- **Stable externally-issued identity.** Because the LFDI/SFDI is derived from
  the leaf, the production identity is whatever your PKI issues. It is stable as
  long as you mount the same leaf; rotation changes it deliberately (below).
- **Persistence.** With a secret-mount you do NOT need the `certs` named volume
  — the secret IS the source of truth and is re-mounted on every start. Drop the
  volume in the prod compose, or keep an empty one; either way generation never
  runs because the secret populates `/certs/server.crt` first.

### Rotation

1. Issue a new server leaf from your CA (new key, same or new identity per
   policy).
2. Replace the secret material (`docker secret` update, or swap the mounted
   files) and restart the service.
3. On restart, `serve-init` sees the new `/certs/server.crt`, skips generation,
   and serves the rotated cert. If the rotation issued a new key, the LFDI/SFDI
   changes deliberately — coordinate this with the EndDevice registrations that
   pin the server identity.

Rotating the dev (generated) cert is the inverse: delete the `certs` volume so
the next start regenerates. Do this only in the lab.

## Listener / network notes

- Protocol listener: `:8443` in-container (8443 avoids needing
  `CAP_NET_BIND_SERVICE` for the `:443` default under nonroot). The compose file
  also `cap_drop: [ALL]` and `no-new-privileges:true`.
- Admin listener: loopback `:8444` inside the container (IEEE-136 default), not
  published. Set `SEP2_ADMIN_KEY` via secret/env_file, never inline, before
  exposing it.
- Network: `observability-stack_obs` is declared `external` — bring the
  observability stack up first so the network exists.

## Files

- `cmd/sep2server/cmd_serve_init.go` — `serve-init` entrypoint + `ensureCerts`.
- `cmd/sep2server/cmd_serve_init_test.go` — seam + key-mode + identity tests.
- `Dockerfile` — `/certs` pre-created owned by 65532; `ENTRYPOINT serve-init`.
- `docker-compose.yml` — dev path service wired into the obs stack.

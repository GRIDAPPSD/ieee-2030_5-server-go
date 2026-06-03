# IEEE-172: multi-stage build for the sep2server IEEE 2030.5 server.
#
# The binary is built statically (CGO_ENABLED=0) so it can run in a
# distroless/static final stage with no libc. The vendored crypto/tls fork
# under internal/tls/gotls/ is pure Go and compiles with a normal go build;
# no special build flags are required for it.
#
# Logs: the server routes the stdlib log package through a slog JSON handler
# to stdout (see cmd/sep2server/logging.go). Run the container under the
# Docker label obs.service=sep2server so Promtail keeps it and maps the
# label to the Loki "service" label.

# ─── Builder ──────────────────────────────────────────────────────
FROM golang:1.26.3 AS builder

WORKDIR /src

# Resolve modules first for a cacheable dependency layer.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# Static, stripped build. Trimpath drops local filesystem paths from the
# binary; -s -w strips the symbol/DWARF tables to shrink the image.
RUN CGO_ENABLED=0 GOOS=linux go build \
        -trimpath -ldflags="-s -w" \
        -o /out/sep2server ./cmd/sep2server/

# IEEE-177: stage a /certs directory owned by the nonroot uid (65532) and
# mode 0700. The final distroless stage has no shell, mkdir, or chown, so we
# create the directory here and COPY it in with ownership preserved. When a
# fresh Docker named volume is first mounted at /certs, Docker seeds the empty
# volume from the image's existing /certs mountpoint — including its ownership
# — so the nonroot entrypoint can write cert material into the volume on first
# start. (A volume mounted over a path that did NOT exist in the image would
# be created root-owned; pre-creating it owned by 65532 is what makes nonroot
# writability work.)
RUN mkdir -p /out/certs && chmod 0700 /out/certs

# ─── Final ────────────────────────────────────────────────────────
# distroless static: no shell, no package manager, runs as nonroot by
# default. Carries CA roots and /etc/passwd for the nonroot user.
FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=builder /out/sep2server /sep2server

# IEEE-177: /certs is the cert volume mountpoint. Pre-created owned by nonroot
# (uid:gid 65532:65532, the distroless `nonroot` user) at 0700 so a fresh
# named volume mounted here inherits that ownership and the nonroot entrypoint
# can write generated certs into it. Keep distroless/static (no shell): the
# cert bootstrap is done IN the Go binary (serve-init), not a shell script,
# which is why the shell-less base is preserved — no busybox/alpine swap, no
# added shell-injection surface, and os.WriteFile sets the exact 0600 key mode.
COPY --from=builder --chown=65532:65532 /out/certs /certs

# Cert paths default off SEP2_CERT_DIR (=/certs). The entrypoint `serve-init`
# generates a CA+server+admin set into /certs ONLY when /certs has no server
# cert (the gen-only-if-empty seam), then serves. Mounting an externally-issued
# cert at /certs bypasses generation with no image change (the prod path).
ENV SEP2_CERT_DIR=/certs

# The protocol listener defaults to :443 and admin to its own port; both are
# configured via SEP2_* env vars at runtime (see cmd/sep2server/main.go).
ENTRYPOINT ["/sep2server"]
CMD ["serve-init"]

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
FROM golang:1.26 AS builder

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

# ─── Final ────────────────────────────────────────────────────────
# distroless static: no shell, no package manager, runs as nonroot by
# default. Carries CA roots and /etc/passwd for the nonroot user.
FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=builder /out/sep2server /sep2server

# The protocol listener defaults to :443 and admin to its own port; both are
# configured via SEP2_* env vars at runtime (see cmd/sep2server/main.go).
ENTRYPOINT ["/sep2server"]
CMD ["serve"]

.PHONY: build test test-cover test-race test-verbose test-e2e \
       test-csip-server \
       test-csip test-csip-hooks test-csip-race test-csip-cover coverage-gate \
       coverage-scope-check \
       lint gofmt-check vet clean run run-ccm run-journald run-ccm-journald \
       run-testdevice run-sunspec certs new-device \
       serve help stress-pretest vendor bump-core ui-build ui-check \
       ci-local ci-local-drift-check

SERVER   := bin/sep2server

# #598: certificates, private keys included, are never written into the
# working tree. This matches the server's own default (SEP2_CERT_DIR, or
# ~/tls) so `make run`/`make certs` and a bare `sep2server serve` agree on
# where the local dev CA and keys live: read SEP2_CERT_DIR when the
# operator set it, the same setting the server itself reads, rather than
# letting the two disagree. Override for a one-off location without
# touching SEP2_CERT_DIR: make certs CERT_DIR=/other/dir.
CERT_DIR ?= $(if $(SEP2_CERT_DIR),$(SEP2_CERT_DIR),$(HOME)/tls)

# --- Build --------------------------------------------------------

build:                    ## Build the server binary
	go build -o $(SERVER) ./cmd/sep2server/

# --- Test ---------------------------------------------------------

test:                     ## Run all Go tests
	go test ./...

# ui-build builds the admin UI's Svelte frontend
# (pkg/adminui/web/frontend/) and writes the static assets into
# pkg/adminui/web/dist/, then rebuilds the Go binary so the freshly
# built assets are embedded via pkg/adminui/web/embed.go's
# "//go:embed all:dist" directive.
#
# Node and npm are needed to RUN this target, but not to run the
# resulting binary: the embedded bundle in dist/ is committed to the
# repo, so `go build ./...` alone (with no Node toolchain at all)
# already succeeds on a fresh checkout using whatever dist/ content is
# currently committed. Run this target only when the frontend source
# under frontend/ has changed and dist/ needs regenerating.
ui-build:                 ## Rebuild the admin UI frontend and re-embed it into the server binary
	cd pkg/adminui/web/frontend && npm ci --ignore-scripts && npm run build
	go build -o $(SERVER) ./cmd/sep2server/

# ui-check rebuilds the frontend into pkg/adminui/web/dist/ and then
# checks that directory for any difference against what is committed, so
# a frontend source change landed WITHOUT a matching `make ui-build` (a
# stale embedded bundle) fails loudly instead of shipping silently.
# `go build ./...` alone cannot catch this: it succeeds against whatever
# dist/ content is on disk, committed or not.
#
# `git status --porcelain`, not `git diff`: Vite content-hashes asset
# filenames, so a source change produces a NEW hashed file under
# dist/assets/ rather than a modified line in an existing tracked file.
# `git diff` only reports changes to already-tracked paths, so a new
# hashed asset would be invisible to it.
#
# Scoped to pkg/adminui/web/dist/ only, so an unrelated dirty file
# elsewhere in the working tree does not produce a false positive. Meant
# to run against a clean checkout (CI's default); running it locally on
# a dirty tree may report drift caused by unrelated uncommitted changes.
ui-check:                 ## Fail if the committed admin UI bundle is stale relative to its source
	cd pkg/adminui/web/frontend && npm ci --ignore-scripts && npm run build
	@if [ -n "$$(git status --porcelain -- pkg/adminui/web/dist/)" ]; then \
	  echo "ui-check: pkg/adminui/web/dist/ is stale."; \
	  echo "The committed build output does not match what the frontend source in"; \
	  echo "pkg/adminui/web/frontend/ currently builds. Run 'make ui-build'"; \
	  echo "and commit the updated dist/ directory."; \
	  git status --porcelain -- pkg/adminui/web/dist/; \
	  exit 1; \
	fi

test-cover:               ## Run tests with coverage report
	go test -coverprofile=coverage.out ./...
	go tool cover -html=coverage.out -o coverage.html
	@echo "Coverage report: coverage.html"

test-race:                ## Run tests with race detector
	go test -race ./...

test-verbose:             ## Run tests with verbose output
	go test -v ./...

test-e2e:                 ## Run Playwright E2E tests (requires npm install in e2e/)
	cd e2e && NODE_TLS_REJECT_UNAUTHORIZED=0 npx playwright test --reporter=list

# --- Certificates ------------------------------------------------

certs:                    ## Generate CA, server, and device certificates
	@# -m 700: CERT_DIR now lives outside the repo (default ~/tls) and
	@# holds a CA private key; -m sets the mode on creation regardless of
	@# umask, so a default umask of 022 cannot leave it world-readable.
	@# A no-op if the directory already exists (#598).
	@mkdir -p -m 700 $(CERT_DIR)
	$(SERVER) certs generate-ca --out $(CERT_DIR)
	$(SERVER) certs generate-server \
		--ca $(CERT_DIR)/ca.crt --ca-key $(CERT_DIR)/ca.key \
		--hosts localhost,127.0.0.1 --out $(CERT_DIR)
	$(SERVER) certs generate-admin \
		--ca $(CERT_DIR)/ca.crt --ca-key $(CERT_DIR)/ca.key \
		--out $(CERT_DIR)
	$(SERVER) certs generate-device \
		--ca $(CERT_DIR)/ca.crt --ca-key $(CERT_DIR)/ca.key \
		--hw-serial INV-001 --hw-type 1.3.6.1.4.1.40732.99 \
		--name device --out $(CERT_DIR)

# new-device: mint an additional CSIP-conformant device certificate signed by the
# existing CA and print the certificate PEM ready to paste into the admin
# dashboard's "Add End Device" textarea. The server derives SFDI/LFDI from the
# pasted PEM; the operator separately supplies PIN and description in the form.
#
# Required:
#   DEVICE_NAME=<slug>  output filename prefix (creates $(CERT_DIR)/<slug>.crt and .key)
#   SERIAL=<string>     hardware serial number for the CSIP section 6.2 HardwareModuleName SAN
#
# Optional:
#   HW_TYPE=<oid>       manufacturer PEN OID (default 1.3.6.1.4.1.40732.99 - same as `make certs`)
#   DEVICE_TYPE=<n>     1=generic (default), 2=mobile, 3=postMfg
#   FORCE=1             overwrite an existing cert/key with the same DEVICE_NAME (default: refuse)
#
# Examples:
#   make new-device DEVICE_NAME=inverter-2 SERIAL=INV-002
#   make new-device DEVICE_NAME=mobile-1 SERIAL=PHONE-XYZ DEVICE_TYPE=2
#   make new-device DEVICE_NAME=device SERIAL=INV-RESPIN FORCE=1
#
# Pre-flight: refuses to overwrite an existing $(CERT_DIR)/<DEVICE_NAME>.crt
# unless FORCE=1 is set, so a typo cannot stomp the device whose key the
# deployed inverter is already running with.
#
# DEVICE_NAME (rather than the more natural NAME) avoids collision with the
# NAME env var that some desktop environments and shells set automatically.
#
# Implementation: the target is a thin dispatcher to scripts/new-device.sh.
# All input validation (regex allowlists for DEVICE_NAME / SERIAL / HW_TYPE,
# device-type enum, FORCE check, CA pre-flight) lives in the script. Make
# does pure text substitution before the shell parses, so anything Make-
# substituted into a shell line is an injection vector. Pushing the values
# through `target-specific export` and reading them in bash with `[[ =~ ]]`
# rejects shell-metachar / path-traversal payloads BEFORE any value reaches
# a command substitution.
new-device: export DEVICE_NAME := $(DEVICE_NAME)
new-device: export SERIAL      := $(SERIAL)
new-device: export HW_TYPE     := $(HW_TYPE)
new-device: export DEVICE_TYPE := $(DEVICE_TYPE)
new-device: export FORCE       := $(FORCE)
new-device: export CERT_DIR    := $(CERT_DIR)
new-device: export SERVER_BIN  := $(SERVER)
new-device:                ## Mint a new device cert (DEVICE_NAME= SERIAL= required); prints PEM for the admin dashboard
	@./scripts/new-device.sh

# --- Run ----------------------------------------------------------

# #268: SEP2_ADMIN_ADDR=:8444 binds admin to 127.0.0.1:8444 by default
# (loopback). #365: an off-box bind ALSO needs an explicit opt-in or
# startup refuses; see docs/admin-listener.md. To reach admin off-box:
#   SEP2_ADMIN_LISTEN=0.0.0.0:8444 SEP2_ADMIN_ALLOW_NON_LOOPBACK=true make run-ccm
# #268 / PR #264: a bare SEP2_METRICS_ADDR=:9100 now resolves to loopback
# (127.0.0.1:9100), so the UNAUTHENTICATED /metrics surface is not exposed
# network-wide by default. A containerized Prometheus scrapes the host via
# host.docker.internal (the docker bridge gateway IP), which is NOT loopback
# and cannot reach a loopback-only bind. The dev run target therefore binds
# the EXPLICIT routable form 0.0.0.0:9100 so the scrape works. This exposes
# /metrics on ALL interfaces and MUST sit behind a host firewall / trusted
# network; the server logs a non-loopback startup WARNING to make that visible.
run: build certs           ## Build, generate certs, and start server (CCM-8, the server's only mode; admin on loopback :8444, metrics on 0.0.0.0:9100)
	SEP2_ADDR=:8443 \
	SEP2_CERT=$(CERT_DIR)/server.crt \
	SEP2_KEY=$(CERT_DIR)/server.key \
	SEP2_CA=$(CERT_DIR)/ca.crt \
	SEP2_CA_KEY=$(CERT_DIR)/ca.key \
	SEP2_ADMIN_ADDR=:8444 \
	SEP2_ADMIN_KEY=admin \
	SEP2_METRICS_ADDR=0.0.0.0:9100 \
	./$(SERVER) serve

run-ccm: run               ## Alias for run (CCM-8 is the server's only mode; kept for muscle memory and existing docs)

# --- journald log shipping -----------------------------
#
# run-journald / run-ccm-journald mirror run / run-ccm but route the
# server's stdout AND stderr (the slog JSON stream) into the systemd
# journal under SYSLOG_IDENTIFIER=sep2server via systemd-cat. The
# observability stack's Promtail journal scrape then ships those entries
# to Loki tagged service=sep2server. The default run / run-ccm targets are
# unchanged and still log to the terminal for interactive dev.
#
# We use the command form `systemd-cat -t <tag> <cmd...>` rather than a
# shell pipe (`<cmd> | systemd-cat`): the command form preserves the
# server's exit status (no pipe masking it), captures both stdout and
# stderr, and avoids a SIGPIPE race on shutdown. The SEP2_* env vars are
# inherited by systemd-cat and passed through to the server child.
#
# Inspect the captured JSON with:   journalctl -t sep2server -o json -f
run-journald: build certs  ## Like run, but ship stdout+stderr to journald as SYSLOG_IDENTIFIER=sep2server
	@# SEP2_ADMIN_KEY=admin is a local-dev default; MUST be overridden for any non-dev/bare-metal deployment
	SEP2_ADDR=:8443 \
	SEP2_CERT=$(CERT_DIR)/server.crt \
	SEP2_KEY=$(CERT_DIR)/server.key \
	SEP2_CA=$(CERT_DIR)/ca.crt \
	SEP2_CA_KEY=$(CERT_DIR)/ca.key \
	SEP2_ADMIN_ADDR=:8444 \
	SEP2_ADMIN_KEY=admin \
	systemd-cat -t sep2server ./$(SERVER) serve

run-ccm-journald: run-journald  ## Alias for run-journald (CCM-8 is the server's only mode; kept for muscle memory and existing docs)

run-full: build certs      ## Start with mDNS + admin dashboard (admin on loopback :8444)
	SEP2_ADDR=:8443 \
	SEP2_CERT=$(CERT_DIR)/server.crt \
	SEP2_KEY=$(CERT_DIR)/server.key \
	SEP2_CA=$(CERT_DIR)/ca.crt \
	SEP2_CA_KEY=$(CERT_DIR)/ca.key \
	SEP2_ADMIN_ADDR=:8444 \
	SEP2_ADMIN_KEY=admin \
	SEP2_MDNS=true \
	./$(SERVER) serve

# CSIP test device demo (#75).
#
# Boots the server bound to 127.0.0.1:8888 (default) with the self-minted
# test device root appended to ClientCAs and a pre-seeded EndDevice
# matching the device's LFDI/SFDI. Server still presents its own
# CA-signed leaf; the test device root only authenticates the test device
# client cert.
#
# The test device cert is CSIP section 6.11-compliant (HardwareModuleName SAN,
# empty Subject, KeyUsage, BasicConstraints). The server may run in
# default (non-strict) or strict mode.
#
# Override the bind via TESTDEVICE_ADDR=host:port.
TESTDEVICE_ADDR ?= 127.0.0.1:8888

run-testdevice: build certs   ## Start server with test device root + EndDevice pre-seed (override TESTDEVICE_ADDR for alternate bind)
	@echo "# Test device profile: binding $(TESTDEVICE_ADDR) (override with TESTDEVICE_ADDR=...)"
	@echo "# Trusted extra client CAs: testdata/csip-pki/testdevice/root_ca.pem"
	@echo "# Pre-seeded EndDevice fixture: test/csip/fixtures/testdevice-edev.yaml"
	@echo "# CSIP section 6.11-compliant cert mode."
	@echo "# Device-side cert (from testdata/csip-pki/testdevice/):"
	@echo "#   --cert testdata/csip-pki/testdevice/device_chain.pem"
	@echo "#   --key  testdata/csip-pki/testdevice/device_key.pem"
	@echo "#   --ca   $(CERT_DIR)/ca.crt     (server CA: what the device must trust)"
	@echo "# Server prints a full connection-details banner at boot (#206)."
	SEP2_ADDR=$(TESTDEVICE_ADDR) \
	SEP2_CERT=$(CERT_DIR)/server.crt \
	SEP2_KEY=$(CERT_DIR)/server.key \
	SEP2_CA=$(CERT_DIR)/ca.crt \
	SEP2_CA_KEY=$(CERT_DIR)/ca.key \
	SEP2_EXTRA_CLIENT_CAS=testdata/csip-pki/testdevice/root_ca.pem \
	SEP2_BOOT_FIXTURE=test/csip/fixtures/testdevice-edev.yaml \
	./$(SERVER) serve

# SunSpec CSIP test PKI profile (#204).
#
# Boots the server in CCM-8 mode on :8443 with the SunSpec CSIP test PKI's
# trust roots loaded as extra client CAs via SEP2_EXTRA_CLIENT_CAS. The
# server still presents its own local-CA-signed leaf - the SunSpec roots
# only authenticate inverter client certs issued under that test PKI.
#
# The default SUNSPEC_ROOTS path lives in the operator's Knowledge
# workspace (gitignored). Override SUNSPEC_ROOTS=... to relocate, or fetch
# the SunSpec CSIP test PKI to that path before running.
#
# Device side (manual): point the inverter at https://localhost:8443 with
# its SunSpec-issued client cert/key (e.g. sunspec/cert.pem + sunspec/key.pem
# from the same test PKI bundle).
SUNSPEC_ROOTS ?= $(HOME)/knowledge/projects/ieee-2030_5-go/artifacts/inputs/csip-test-pki/sunspec/roots.pem

run-sunspec: build certs   ## Start server trusting SunSpec CSIP test PKI roots (override SUNSPEC_ROOTS to relocate)
	@test -f $(SUNSPEC_ROOTS) || (echo "ERROR: SUNSPEC_ROOTS not found at $(SUNSPEC_ROOTS); set SUNSPEC_ROOTS=... or fetch the SunSpec CSIP test PKI to that path" && exit 1)
	@echo "# SunSpec profile: binding :8443 (CCM-8)"
	@echo "# Server cert: local CA chain ($(CERT_DIR)/server.crt)"
	@echo "# Trusted extra client CAs: $(SUNSPEC_ROOTS)"
	@echo "# Device-side cert (CSIP V1.2 SunSpec test PKI):"
	@echo "#   --cert $(subst roots.pem,cert.pem,$(SUNSPEC_ROOTS))"
	@echo "#   --key  $(subst roots.pem,key.pem,$(SUNSPEC_ROOTS))"
	@echo "#   --ca   $(CERT_DIR)/ca.crt   (server's CA - what the device must trust)"
	@echo "# Server prints a full connection-details banner at boot (#206)."
	SEP2_ADDR=:8443 \
	SEP2_CERT=$(CERT_DIR)/server.crt \
	SEP2_KEY=$(CERT_DIR)/server.key \
	SEP2_CA=$(CERT_DIR)/ca.crt \
	SEP2_CA_KEY=$(CERT_DIR)/ca.key \
	SEP2_EXTRA_CLIENT_CAS=$(SUNSPEC_ROOTS) \
	./$(SERVER) serve

serve: run                 ## Alias for run

# --- EPRI Client Interop ------------------------------------------

EPRI_CLIENT := $(HOME)/repos/IEEE-2030.5-Client

test-interop: build certs ## Run interop tests with curl (server must be running)
	./scripts/test-epri-client.sh

build-epri:               ## Build the EPRI C client (requires gcc + OpenSSL)
	cd $(EPRI_CLIENT) && bash build.sh

test-epri: build certs    ## Run EPRI C client against our server (CCM mode, server must be running)
	$(EPRI_CLIENT)/build/client_test lo \
		$(CERT_DIR)/device.crt $(CERT_DIR)/ca.crt \
		https://localhost:8443/dcap edev time

# --- CSIP Conformance Harness ------------------------------------

test-csip-server:         ## Run CSIP server-side conformance harness (requires fixtures, see test/csip/README.md)
	./scripts/test-csip-server.sh

# Client-side CSIP conformance moved with the inverter client to
# ieee-2030_5-client-go; its gate is tracked there (ieee-2030_5-client-go#31),
# not in this repository's Makefile.

# #192 - CSIP harness CI integration. The two targets below shape the
# build-tag matrix axis: `test-csip` runs the suite under the production
# code path (no tag), `test-csip-hooks` runs it with the #27/#28
# mutation + time-advance hooks compiled in. Both target the CSIP harness
# AND `./internal/...` so the standard library code reached by the CSIP
# tests participates in the coverage signal CI captures.
test-csip:                ## Run CSIP suite under the default (production) build tag
	go test ./test/csip/... ./internal/... ./pkg/...

test-csip-hooks:          ## Run CSIP suite + internal + pkg with csip_test_hooks build tag
	go test -tags csip_test_hooks ./test/csip/... ./internal/... ./pkg/...

test-csip-race:           ## Race detector on the CSIP suite with csip_test_hooks tag
	go test -race -tags csip_test_hooks ./test/csip/...

# #193 - CSIP-scoped coverage profile + gate.
#
# Phase 8 Deliverable 3: the coverage gate operates on production code
# reachable from CSIP-mode execution, not on raw ./... (which aggregates
# every first-party package in the module: cmd/ entrypoints, test/stress
# tooling, and handler packages under pkg/sep2srv/handlers/... whose
# coverage comes only from the CSIP suite's cross-package -coverpkg
# attribution and read 0% under a plain per-package `go test ./...` run;
# that mix would always drag the aggregate below policy). The -coverpkg
# list below pins the in-scope packages; the gate floor is enforced by
# scripts/coverage-gate.sh.
#
# Achieved threshold at #192 merge: 79.1% scoped. Gate floored at
# 78% (1pp below for measurement noise) per Phase 8 doc (#193).
# Ratcheted to 80% at #212 merge (post-interop, per workspace TDD rule).
# ./pkg/sep2server/..., the embeddable surface, is listed because the
# protocol listener, the handler assembly and the graceful drain MOVED
# there out of ./internal/server/...; leaving it off would have quietly
# shrunk what the floor measures while the percentage went up.
#
# #357 - scope correction. pkg/sep2srv (the router assembly and every
# protocol handler) and pkg/store (the resource-state contract and its
# in-memory implementation) arrived after Phase 8 and were never added
# here, so the floor stopped measuring most of the module's production
# code while staying green. Both are hand-maintained wildcards, guarded
# by the `coverage-scope-check` target (#387) against silently
# resolving to nothing again. pkg/store/storetest is deliberately left
# out: its own package doc says it exists for testing only and nothing
# in the production path imports it, so it is not production code the
# CSIP suite is meant to cover.
#
# #705 - ./pkg/adminui/... is listed for the same reason as
# ./pkg/sep2server/... above: the embedded admin UI assets MOVED there
# out of ./internal/server/web, which `./internal/server/...` used to
# match by prefix. Leaving it off would have quietly dropped that
# package from the floor while the percentage held steady.
CSIP_COVERPKG := ./test/csip/...,./internal/auth/...,./internal/bootfixture/...,./internal/certs/...,./internal/config/...,./internal/discovery/...,./internal/handler/...,./internal/server/...,./pkg/adminui/...,./pkg/sep2server/...,./pkg/sep2srv/...,./pkg/store,./pkg/store/memory
CSIP_COVER_THRESHOLD ?= 80

# #387 - the -coverpkg list above is hand-maintained; a pattern that stops
# resolving (a package renamed or moved out) is a silent Go warning, not a
# build error, so it is checked here before every profile run.
coverage-scope-check:     ## Fail if any CSIP_COVERPKG entry resolves to zero packages
	./scripts/coverage-scope-check.sh '$(CSIP_COVERPKG)'

test-csip-cover: coverage-scope-check ## Run CSIP suite with scoped coverage profile (writes coverage-csip.out)
	go test -coverprofile=coverage-csip.out -coverpkg='$(CSIP_COVERPKG)' \
	       -tags csip_test_hooks ./test/csip/... ./internal/... ./pkg/...
	@echo "Coverage profile: coverage-csip.out"

coverage-gate:            ## Enforce CSIP coverage floor on coverage-csip.out
	./scripts/coverage-gate.sh coverage-csip.out $(CSIP_COVER_THRESHOLD)

# --- Stress Test ---
#
# Usage examples:
#   make stress-test                              # smoke: 5 clients, 30s, throughput
#   make stress-test CLIENTS=100 DURATION=300     # throughput ramp
#   make stress-test DIM=fanout CLIENTS=10 DURATION=60  # fanout smoke (10 subs, 60s)
#   make stress-test DIM=fanout CLIENTS=0 DURATION=1800 # fanout break-point run (open-ended)
#   make stress-test DIM=soak DURATION=7200       # 2h soak
#   make stress-test DIM=tls CCM=true             # TLS/CCM exhaustion
#
# Sweep subscription workers/queue:
#   SEP2_SUBSCRIPTION_WORKERS=8 SEP2_SUBSCRIPTION_QUEUE_SIZE=512 \
#     make stress-test DIM=fanout CLIENTS=50
#
# Parameters (all optional, defaults shown):
#   DIM=throughput        dimension: throughput|fanout|soak|tls
#   CLIENTS=5             virtual client count (0=open-ended ramp)
#   RAMP_RATE=5           clients added per second
#   DURATION=30           run duration in seconds (0=unlimited, break only)
#   TARGET_HOST=127.0.0.1
#   TARGET_PORT=8443
#   CCM=false             enable CCM-8 cipher
#   SEED=42               deterministic RNG seed
#   SCRAPE=5              Prometheus scrape interval in seconds
#
# Fanout-only parameters:
#   MUTATION_RATE_HZ=20   stress-notify injections per second
#   MUTATION_TOKEN=       pre-set token (auto-generated per run when empty)
#
STRESS_DIM          ?= throughput
STRESS_CLIENTS      ?= 5
STRESS_RAMP         ?= 5
STRESS_DURATION     ?= 30
STRESS_HOST         ?= 127.0.0.1
STRESS_PORT         ?= 8443
STRESS_CCM          ?= false
STRESS_SEED         ?= 42
STRESS_SCRAPE       ?= 5
STRESS_MUTATION_HZ  ?= 20

stress-test: ## Run stress test (smoke: 5 clients, 30s, throughput). See comment above for full usage.
	DIM=$(STRESS_DIM) \
	CLIENTS=$(STRESS_CLIENTS) \
	RAMP_RATE=$(STRESS_RAMP) \
	DURATION=$(STRESS_DURATION) \
	TARGET_HOST=$(STRESS_HOST) \
	TARGET_PORT=$(STRESS_PORT) \
	CCM=$(STRESS_CCM) \
	SEED=$(STRESS_SEED) \
	SCRAPE_INTERVAL=$(STRESS_SCRAPE) \
	MUTATION_RATE_HZ=$(STRESS_MUTATION_HZ) \
	bash test/stress/scripts/stress.sh

# stress-pretest applies the opt-in kernel tuning the connection-heavy
# dimensions need (throughput and tls/ccm). REQUIRED before those two;
# NOT needed for fanout or soak. See test/stress/DESIGN.md.
# NOTE: a make target runs in a child process and cannot set the parent
# shell's ulimit. This target applies the sysctls and PRINTS the exact
# 'ulimit -n 65536' line; run that line in the shell you launch the
# harness from (or 'source test/stress/scripts/pretest-tune.sh' there).
stress-pretest: ## Apply opt-in kernel tuning for stress (sysctls now; run the printed ulimit line in your own shell)
	bash test/stress/scripts/pretest-tune.sh

# --- Code Quality ---

lint:                     ## Run golangci-lint + gofmt drift check
	golangci-lint run ./...
	$(MAKE) gofmt-check

gofmt-check:              ## Verify gofmt drift (excludes vendor/); a file gofmt cannot parse still fails
	@# vendor/ holds go.mod-managed third-party source. The hand-copied
	@# crypto/tls fork this repository once kept locally is gone: it was
	@# replaced by the sep2tls package (pkg/sep2tls) consumed from
	@# ieee-2030_5-core-go, mod-vendored at
	@# vendor/github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2tls/gotls/.
	@out=`gofmt -l . 2>&1` && status=0 || status=$$?; \
		if [ "$$status" -ne 0 ]; then \
			echo "gofmt failed to run (exit $$status):"; \
			echo "$$out"; \
			exit "$$status"; \
		fi; \
		drift=`printf '%s\n' "$$out" | grep -vE '^vendor/' || true`; \
		if [ -n "$$drift" ]; then \
			echo "gofmt drift detected (run: gofmt -w <files>):"; \
			echo "$$drift"; \
			exit 1; \
		fi

vet:                      ## Run go vet
	@# ./... excludes vendor/, and vet only prints diagnostics for the
	@# requested package set (true of any unrequested dependency, not a
	@# vendor-specific rule); it still uses facts from vendor/ packages when
	@# analyzing first-party callers. A vendor path here is a type-check
	@# error that also fails 'make build': never filter it out.
	go vet ./...

# --- Local CI Gate (#410) ------------------------------------------

ci-local:                 ## Run the same gates ci.yml runs, locally (see #410)
	scripts/ci-local/ci-local.sh

ci-local-drift-check:     ## Fail if ci.yml invokes a make target ci-local.sh does not run
	scripts/ci-local/ci-local-drift-check.sh

# --- Dependencies -----------------------------------------------

vendor:                   ## Regenerate vendor/ from go.mod (VENDORED.md)
	go mod vendor

# bump-core: the one supported path for moving the core-go pin by hand, so
# a human doing it manually cannot skip the vendor step the way
# core-freshness.yml itself once could. Vendoring only when vendor/
# already exists mirrors that workflow's own guard: a repo with no vendor
# tree yet must not gain one as a side effect of an unrelated bump.
bump-core:                ## Bump the pinned core-go module, tidy, and re-vendor. Usage: make bump-core [TARGET=v0.15.1] (default: main)
	go get github.com/GRIDAPPSD/ieee-2030_5-core-go@$(if $(TARGET),$(TARGET),main)
	go mod tidy
	@if [ -f vendor/modules.txt ]; then $(MAKE) vendor; fi

# --- Cleanup -----------------------------------------------------

clean:                    ## Remove build artifacts
	rm -rf bin/ coverage.out coverage.html coverage-csip.out sep2server

# --- Help --------------------------------------------------------

help:                     ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*## ' $(MAKEFILE_LIST) | sort | \
		awk 'BEGIN {FS = ":.*## "}; {printf "  \033[36m%-20s\033[0m %s\n", $$1, $$2}'

.DEFAULT_GOAL := help

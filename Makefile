.PHONY: build build-all test test-cover test-race test-verbose test-e2e \
       test-csip-server test-csip-client \
       test-csip test-csip-hooks test-csip-race test-csip-cover coverage-gate \
       lint vet clean run run-ccm run-enphase run-sunspec certs serve help \
       verify-run-inverter-url

SERVER   := bin/sep2server
CLIENT   := bin/inverterclient
CERT_DIR := certs

# ─── Build ────────────────────────────────────────────────────────

build:                    ## Build the server binary
	go build -o $(SERVER) ./cmd/sep2server/

build-all:                ## Build server and inverter client
	go build -o $(SERVER) ./cmd/sep2server/
	go build -o $(CLIENT) ./cmd/inverterclient/

# ─── Test ─────────────────────────────────────────────────────────

test:                     ## Run all Go tests
	go test ./...

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

# ─── Certificates ────────────────────────────────────────────────

certs:                    ## Generate CA, server, and device certificates
	@mkdir -p $(CERT_DIR)
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

# ─── Run ──────────────────────────────────────────────────────────

run: build certs           ## Build, generate certs, and start server (GCM mode)
	SEP2_ADDR=:8443 \
	SEP2_CERT=$(CERT_DIR)/server.crt \
	SEP2_KEY=$(CERT_DIR)/server.key \
	SEP2_CA=$(CERT_DIR)/ca.crt \
	SEP2_CA_KEY=$(CERT_DIR)/ca.key \
	SEP2_ADMIN_ADDR=:8444 \
	SEP2_ADMIN_KEY=admin \
	./$(SERVER) serve

run-ccm: build certs       ## Start server with CCM-8 cipher (spec-compliant)
	SEP2_ADDR=:8443 \
	SEP2_CERT=$(CERT_DIR)/server.crt \
	SEP2_KEY=$(CERT_DIR)/server.key \
	SEP2_CA=$(CERT_DIR)/ca.crt \
	SEP2_CA_KEY=$(CERT_DIR)/ca.key \
	SEP2_ADMIN_ADDR=:8444 \
	SEP2_ADMIN_KEY=admin \
	SEP2_CCM=true \
	./$(SERVER) serve

run-full: build certs      ## Start with CCM + mDNS + admin dashboard
	SEP2_ADDR=:8443 \
	SEP2_CERT=$(CERT_DIR)/server.crt \
	SEP2_KEY=$(CERT_DIR)/server.key \
	SEP2_CA=$(CERT_DIR)/ca.crt \
	SEP2_CA_KEY=$(CERT_DIR)/ca.key \
	SEP2_ADMIN_ADDR=:8444 \
	SEP2_ADMIN_KEY=admin \
	SEP2_CCM=true \
	SEP2_MDNS=true \
	./$(SERVER) serve

# Enphase microinverter demo (IEEE-068).
#
# Boots the server bound to 10.0.0.101:8888 with the Enphase test root
# appended to ClientCAs and a pre-seeded EndDevice matching the device's
# LFDI/SFDI. Server still presents its own SunSpec-CA-signed leaf — the
# Enphase root only authenticates the inverter's client cert.
#
# CSIP §6.11 compliance: the Enphase test leaf is NOT compliant
# (no HardwareModuleName SAN, no Key Usage, no Basic Constraints,
# non-empty Subject). The server must run in non-strict cert verification
# mode (the default). SEP2_CSIP_STRICT=true (IEEE-020) is incompatible
# with this device.
#
# Manual prerequisite: the host's network must reach 10.0.0.101.
# Switching to the Enphase LAN is a manual step performed before running
# this target. Override the bind via ENPHASE_ADDR=host:port for local
# smoke runs (e.g. ENPHASE_ADDR=127.0.0.1:8888).
ENPHASE_ADDR ?= 10.0.0.101:8888

run-enphase: build certs   ## Start server with Enphase root + EndDevice pre-seed (override ENPHASE_ADDR for local smoke)
	@echo "# Enphase profile: binding $(ENPHASE_ADDR) (override with ENPHASE_ADDR=...)"
	@echo "# Trusted extra client CAs: testdata/csip-pki/enphase/Enph_root.pem"
	@echo "# Pre-seeded EndDevice fixture: test/csip/fixtures/enphase-edev.yaml"
	@echo "# Non-strict cert mode (Enphase leaf is CSIP §6.11 non-compliant)"
	@echo "# Device-side cert (Enphase microinverter ships its own client cert):"
	@echo "#   --cert <enphase-device>.crt   (device's vendor-issued client cert)"
	@echo "#   --key  <enphase-device>.key   (device's private key)"
	@echo "#   --ca   $(CERT_DIR)/ca.crt     (server CA — what the device must trust)"
	@echo "# Server prints a full connection-details banner at boot (IEEE-112)."
	SEP2_ADDR=$(ENPHASE_ADDR) \
	SEP2_CERT=$(CERT_DIR)/server.crt \
	SEP2_KEY=$(CERT_DIR)/server.key \
	SEP2_CA=$(CERT_DIR)/ca.crt \
	SEP2_CA_KEY=$(CERT_DIR)/ca.key \
	SEP2_EXTRA_CLIENT_CAS=testdata/csip-pki/enphase/Enph_root.pem \
	SEP2_BOOT_FIXTURE=test/csip/fixtures/enphase-edev.yaml \
	SEP2_CCM=true \
	./$(SERVER) serve

# SunSpec CSIP test PKI profile (IEEE-111).
#
# Boots the server in CCM-8 mode on :8443 with the SunSpec CSIP test PKI's
# trust roots loaded as extra client CAs via SEP2_EXTRA_CLIENT_CAS. The
# server still presents its own local-CA-signed leaf — the SunSpec roots
# only authenticate inverter client certs issued under that test PKI.
#
# The default SUNSPEC_ROOTS path lives in the operator's Knowledge
# workspace (gitignored). Override SUNSPEC_ROOTS=... to relocate, or fetch
# the SunSpec CSIP test PKI to that path before running.
#
# Device side (manual): point the inverter at https://localhost:8443 with
# its SunSpec-issued client cert/key (e.g. sunspec/cert.pem + sunspec/key.pem
# from the same test PKI bundle). For our own inverter simulator, prefer
# `make run-inverter` against the locally-generated device cert instead.
SUNSPEC_ROOTS ?= $(HOME)/knowledge/projects/ieee-2030_5-go/artifacts/inputs/csip-test-pki/sunspec/roots.pem

run-sunspec: build certs   ## Start server trusting SunSpec CSIP test PKI roots (override SUNSPEC_ROOTS to relocate)
	@test -f $(SUNSPEC_ROOTS) || (echo "ERROR: SUNSPEC_ROOTS not found at $(SUNSPEC_ROOTS); set SUNSPEC_ROOTS=... or fetch the SunSpec CSIP test PKI to that path" && exit 1)
	@echo "# SunSpec profile: binding :8443 (CCM-8)"
	@echo "# Server cert: local CA chain ($(CERT_DIR)/server.crt)"
	@echo "# Trusted extra client CAs: $(SUNSPEC_ROOTS)"
	@echo "# Device-side cert (CSIP V1.2 SunSpec test PKI):"
	@echo "#   --cert $(subst roots.pem,cert.pem,$(SUNSPEC_ROOTS))"
	@echo "#   --key  $(subst roots.pem,key.pem,$(SUNSPEC_ROOTS))"
	@echo "#   --ca   $(CERT_DIR)/ca.crt   (server's CA — what the device must trust)"
	@echo "# Server prints a full connection-details banner at boot (IEEE-112)."
	SEP2_ADDR=:8443 \
	SEP2_CERT=$(CERT_DIR)/server.crt \
	SEP2_KEY=$(CERT_DIR)/server.key \
	SEP2_CA=$(CERT_DIR)/ca.crt \
	SEP2_CA_KEY=$(CERT_DIR)/ca.key \
	SEP2_EXTRA_CLIENT_CAS=$(SUNSPEC_ROOTS) \
	SEP2_CCM=true \
	./$(SERVER) serve

serve: run                 ## Alias for run

# ─── Inverter Simulator ──────────────────────────────────────────

SERVER_URL ?= https://localhost:8443

run-inverter: build-all    ## Run inverter simulator (override target with SERVER_URL=...)
	./$(CLIENT) \
		--server $(SERVER_URL) \
		--cert $(CERT_DIR)/device.crt \
		--key $(CERT_DIR)/device.key \
		--ca $(CERT_DIR)/ca.crt \
		--scenario normal \
		--timescale 120 \
		--hmi-port 8080

run-scenario: build-all    ## Run a specific scenario (use SCENARIO=voltvar)
	./$(CLIENT) \
		--server https://localhost:8443 \
		--cert $(CERT_DIR)/device.crt \
		--key $(CERT_DIR)/device.key \
		--ca $(CERT_DIR)/ca.crt \
		--scenario $(or $(SCENARIO),normal) \
		--timescale 120 \
		--hmi-port 8080

list-scenarios: build-all  ## List available inverter test scenarios
	./$(CLIENT) --list-scenarios

verify-run-inverter-url:   ## Smoke-check that run-inverter honors SERVER_URL env override (IEEE-026)
	bash scripts/test-run-inverter-url.sh

# ─── EPRI Client Interop ──────────────────────────────────────────

EPRI_CLIENT := $(HOME)/repos/IEEE-2030.5-Client

test-interop: build certs ## Run interop tests with curl (server must be running)
	./scripts/test-epri-client.sh

build-epri:               ## Build the EPRI C client (requires gcc + OpenSSL)
	cd $(EPRI_CLIENT) && bash build.sh

test-epri: build certs    ## Run EPRI C client against our server (CCM mode, server must be running)
	$(EPRI_CLIENT)/build/client_test lo \
		$(CERT_DIR)/device.crt $(CERT_DIR)/ca.crt \
		https://localhost:8443/dcap edev time

# ─── CSIP Conformance Harness ────────────────────────────────────

test-csip-server:         ## Run CSIP server-side conformance harness (requires fixtures, see test/csip/README.md)
	./scripts/test-csip-server.sh

test-csip-client:         ## CSIP client-side conformance harness (blocked on IEEE-019)
	@echo "# IEEE-019 blocks this target: inverter client must move onto vendored gotls"
	@echo "# stack to negotiate CCM-8 before client-side conformance can be exercised."
	@echo "# See GRIDAPPSD/ieee-2030_5-go#21."

# IEEE-106 — CSIP harness CI integration. The two targets below shape the
# build-tag matrix axis: `test-csip` runs the suite under the production
# code path (no tag), `test-csip-hooks` runs it with the IEEE-024/025
# mutation + time-advance hooks compiled in. Both target the CSIP harness
# AND `./internal/...` so the standard library code reached by the CSIP
# tests participates in the coverage signal CI captures.
test-csip:                ## Run CSIP suite under the default (production) build tag
	go test ./test/csip/... ./internal/...

test-csip-hooks:          ## Run CSIP suite + internal with csip_test_hooks build tag
	go test -tags csip_test_hooks ./test/csip/... ./internal/...

test-csip-race:           ## Race detector on the CSIP suite with csip_test_hooks tag
	go test -race -tags csip_test_hooks ./test/csip/...

# IEEE-107 — CSIP-scoped coverage profile + gate.
#
# Phase 8 Deliverable 3: the coverage gate operates on production code
# reachable from CSIP-mode execution, not on raw ./... (which includes
# the vendored internal/tls/gotls/ fork and would always drag the
# aggregate below policy). The -coverpkg list below pins the in-scope
# packages; the gate floor is enforced by scripts/coverage-gate.sh.
#
# Achieved threshold at IEEE-106 merge: 79.1% scoped. Gate floored at
# 78% (1pp below for measurement noise) per Phase 8 doc (IEEE-107).
# Ratcheted to 80% at IEEE-121 merge (post-interop, per workspace TDD rule).
CSIP_COVERPKG := ./test/csip/...,./internal/auth/...,./internal/bootfixture/...,./internal/certs/...,./internal/config/...,./internal/discovery/...,./internal/encoding/...,./internal/handler/...,./internal/inverter/...,./internal/paging/...,./internal/server/...,./internal/subscription/...,./internal/tls,./internal/tls/ccm
CSIP_COVER_THRESHOLD ?= 80

test-csip-cover:          ## Run CSIP suite with scoped coverage profile (writes coverage-csip.out)
	go test -coverprofile=coverage-csip.out -coverpkg='$(CSIP_COVERPKG)' \
	       -tags csip_test_hooks ./test/csip/... ./internal/...
	@echo "Coverage profile: coverage-csip.out"

coverage-gate:            ## Enforce CSIP coverage floor on coverage-csip.out
	./scripts/coverage-gate.sh coverage-csip.out $(CSIP_COVER_THRESHOLD)

# ─── Code Quality ────────────────────────────────────────────────

lint:                     ## Run golangci-lint
	golangci-lint run ./...

vet:                      ## Run go vet
	go vet ./...

# ─── Cleanup ─────────────────────────────────────────────────────

clean:                    ## Remove build artifacts and generated certs
	rm -rf bin/ coverage.out coverage.html coverage-csip.out $(CERT_DIR)/ sep2server

# ─── Help ────────────────────────────────────────────────────────

help:                     ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*## ' $(MAKEFILE_LIST) | sort | \
		awk 'BEGIN {FS = ":.*## "}; {printf "  \033[36m%-20s\033[0m %s\n", $$1, $$2}'

.DEFAULT_GOAL := help

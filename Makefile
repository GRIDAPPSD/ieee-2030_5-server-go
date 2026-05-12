.PHONY: build build-all test test-cover test-race test-verbose test-e2e \
       lint vet clean run run-ccm certs serve help \
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

# ─── Code Quality ────────────────────────────────────────────────

lint:                     ## Run golangci-lint
	golangci-lint run ./...

vet:                      ## Run go vet
	go vet ./...

# ─── Cleanup ─────────────────────────────────────────────────────

clean:                    ## Remove build artifacts and generated certs
	rm -rf bin/ coverage.out coverage.html $(CERT_DIR)/ sep2server

# ─── Help ────────────────────────────────────────────────────────

help:                     ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*## ' $(MAKEFILE_LIST) | sort | \
		awk 'BEGIN {FS = ":.*## "}; {printf "  \033[36m%-20s\033[0m %s\n", $$1, $$2}'

.DEFAULT_GOAL := help

#!/usr/bin/env bash
# Run the CSIP server-side conformance harness.
#
# This driver boots an in-process spec server in CCM mode inside the Go test
# binary and exercises the mTLS handshake using SunSpec V1.2 test materials
# as the external client. The server is NOT a long-running standalone
# process — there is nothing to tear down because the test owns the listener
# lifecycle via t.Cleanup.
#
# Fixture provisioning:
#   The SunSpec test PKI is gitignored. Point the harness at provisioned
#   fixtures via env vars or drop them under test/csip/fixtures/sunspec/.
#   See test/csip/README.md.
#
# Env contract:
#   CSIP_SUNSPEC_CERT   path to the SunSpec device leaf cert (PEM)
#   CSIP_SUNSPEC_KEY    path to the SunSpec device private key (PEM)
#   CSIP_SUNSPEC_ROOTS  path to the SunSpec trust root bundle (PEM)
#
# When all three are unset AND test/csip/fixtures/sunspec/ is empty, the
# harness skips cleanly with a pointer to test/csip/README.md. This is by
# design — fresh clones must not fail.
#
# Usage:
#   make test-csip-server
#   CSIP_SUNSPEC_CERT=... CSIP_SUNSPEC_KEY=... CSIP_SUNSPEC_ROOTS=... \
#     make test-csip-server

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"

echo "=== CSIP Server-side Conformance Harness ==="
echo "Repo:        ${REPO_ROOT}"
echo "Fixtures:    ${CSIP_SUNSPEC_CERT:-<default: test/csip/fixtures/sunspec/cert.pem>}"
echo "             ${CSIP_SUNSPEC_KEY:-<default: test/csip/fixtures/sunspec/key.pem>}"
echo "             ${CSIP_SUNSPEC_ROOTS:-<default: test/csip/fixtures/sunspec/roots.pem>}"
echo ""

cd "${REPO_ROOT}"
exec go test -v ./test/csip/...

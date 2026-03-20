#!/usr/bin/env bash
# Test script for EPRI IEEE 2030.5 Client against our server.
#
# Prerequisites:
#   1. Build our server: make build
#   2. Generate certs: make certs
#   3. Clone and build EPRI client:
#      git clone https://github.com/epri-dev/IEEE-2030.5-Client.git
#      cd IEEE-2030.5-Client && ./build.sh
#   4. Run this script: ./scripts/test-epri-client.sh [path-to-epri-client]
#
# If the EPRI client is not available, this script tests with curl instead
# to verify the server responds correctly to the same requests.

set -euo pipefail

CERT_DIR="${CERT_DIR:-certs}"
SERVER_URL="${SERVER_URL:-https://localhost:8443}"
EPRI_CLIENT="${1:-}"

echo "=== IEEE 2030.5 Server Interop Test ==="
echo "Server: ${SERVER_URL}"
echo "Certs:  ${CERT_DIR}"
echo ""

# Check certs exist
for f in ca.crt server.crt server.key device.crt device.key; do
  if [ ! -f "${CERT_DIR}/${f}" ]; then
    echo "ERROR: ${CERT_DIR}/${f} not found. Run 'make certs' first."
    exit 1
  fi
done

if [ -n "${EPRI_CLIENT}" ] && [ -x "${EPRI_CLIENT}" ]; then
  echo "Using EPRI client: ${EPRI_CLIENT}"
  echo "TODO: Wire EPRI client_test commands"
  echo ""
else
  echo "EPRI client not found — testing with curl (same TLS/HTTP patterns)"
  echo ""
fi

# Test with curl using our device cert (mutual TLS)
CURL="curl -s --cacert ${CERT_DIR}/ca.crt --cert ${CERT_DIR}/device.crt --key ${CERT_DIR}/device.key"

echo "--- Phase 1: Discovery ---"
echo "GET /dcap"
DCAP=$(${CURL} ${SERVER_URL}/dcap)
echo "${DCAP}" | head -5
echo ""

if echo "${DCAP}" | grep -q "DeviceCapability"; then
  echo "PASS: DeviceCapability returned"
else
  echo "FAIL: DeviceCapability not in response"
  exit 1
fi

echo ""
echo "--- Phase 2: Time ---"
echo "GET /tm"
TIME=$(${CURL} ${SERVER_URL}/tm)
echo "${TIME}" | head -5
echo ""

if echo "${TIME}" | grep -q "currentTime"; then
  echo "PASS: Time resource returned"
else
  echo "FAIL: Time not in response"
  exit 1
fi

echo ""
echo "--- Phase 3: Registration ---"
echo "POST /edev"
EDEV=$(${CURL} -X POST -H "Content-Type: application/sep+xml" \
  -d '<EndDevice xmlns="urn:ieee:std:2030.5:ns"><sFDI>000000000000</sFDI></EndDevice>' \
  ${SERVER_URL}/edev)
echo "${EDEV}" | head -5
echo ""

if echo "${EDEV}" | grep -q "EndDevice"; then
  echo "PASS: EndDevice registration succeeded"
else
  echo "FAIL: EndDevice not in response"
  exit 1
fi

echo ""
echo "--- Phase 4: End Device List ---"
echo "GET /edev"
EDEV_LIST=$(${CURL} ${SERVER_URL}/edev)
echo "${EDEV_LIST}" | head -5
echo ""

if echo "${EDEV_LIST}" | grep -q "EndDeviceList"; then
  echo "PASS: EndDeviceList returned"
else
  echo "FAIL: EndDeviceList not in response"
  exit 1
fi

echo ""
echo "--- Phase 5: Mirror Usage Point ---"
echo "POST /mup"
MUP=$(${CURL} -X POST -H "Content-Type: application/sep+xml" \
  -d '<MirrorUsagePoint xmlns="urn:ieee:std:2030.5:ns"><mRID>interop-test</mRID><description>Interop Test</description><serviceCategoryKind>0</serviceCategoryKind><status>1</status></MirrorUsagePoint>' \
  ${SERVER_URL}/mup)
echo "${MUP}" | head -5
echo ""

if echo "${MUP}" | grep -q "MirrorUsagePoint"; then
  echo "PASS: MirrorUsagePoint created"
else
  echo "FAIL: MirrorUsagePoint not in response"
  exit 1
fi

echo ""
echo "--- Phase 6: TLS Info ---"
echo "Checking TLS connection details..."
echo | openssl s_client -connect "${SERVER_URL#https://}" \
  -cert "${CERT_DIR}/device.crt" -key "${CERT_DIR}/device.key" \
  -CAfile "${CERT_DIR}/ca.crt" 2>/dev/null | grep -E "Protocol|Cipher|Verify"
echo ""

echo "=== All interop tests PASSED ==="

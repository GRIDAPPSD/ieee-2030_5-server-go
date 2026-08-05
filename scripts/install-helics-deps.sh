#!/usr/bin/env bash
# install-helics-deps.sh
#
# Provision HELICS 3 and GridLAB-D (with HELICS support) on a fresh Debian/Ubuntu
# host so the IEEE 2030.5 plan-5 HELICS/GridLAB-D co-simulation work can build.
#
# Owner: #251 (Plan-5, Phase 1 prerequisite).
#
# What this installs:
#   * HELICS 3 (https://github.com/GMLC-TDC/HELICS) at the pinned tag below,
#     cloned to /home/debian/repos/HELICS, built with CMake, installed to
#     /usr/local. Provides libhelics, helics_broker, and the pkg-config file
#     consumed by the in-house cgo wrapper at internal/helics/.
#   * GridLAB-D (https://github.com/gridlab-d/gridlab-d) on branch feature/1478,
#     cloned to /home/debian/repos/gridlab-d, built with -DGLD_USE_HELICS=ON,
#     installed to /usr/local. Provides gridlabd with HELICS support so plan-5
#     models can co-simulate via connection/helics_msg.cpp.
#
# Idempotent: re-running after a clean install short-circuits both halves.
#
# References:
#   * ADR-002, ADR-004 (in-house cgo wrapper against system libhelics)
#
# Constraint update from Craig (2026-06-02):
#   1. GridLAB-D branch is feature/1478 (NOT develop).
#   2. `git submodule update --init --recursive` is mandatory before each
#      cmake configure step. Both HELICS and GridLAB-D ship submodules.

set -euo pipefail

# -----------------------------------------------------------------------------
# Pinned versions
# -----------------------------------------------------------------------------
HELICS_REPO_URL="https://github.com/GMLC-TDC/HELICS.git"
HELICS_TAG="v3.6.1"          # Latest stable HELICS 3 release as of 2026-06-02
HELICS_DIR="/home/debian/repos/HELICS"

GRIDLABD_REPO_URL="https://github.com/gridlab-d/gridlab-d.git"
GRIDLABD_BRANCH="feature/1478"
# 5aef1a5fe5a3df81eb065def89a8aa901c024c5a was the tip of feature/1478 on
# 2026-06-02 when this script was authored. The script tracks the branch
# (HEAD on first clone, fetch on re-runs) so future hosts get current bug
# fixes; the SHA above is the verifiable reference point for this provisioning
# pass.
GRIDLABD_DIR="/home/debian/repos/gridlab-d"

INSTALL_PREFIX="/usr/local"
BUILD_JOBS="$(nproc)"

# Apt packages required to build HELICS 3 and GridLAB-D (with HELICS).
# Keep this list minimal: only what the upstream CMake builds actually need.
# HELICS bundles asio/fmt/spdlog/zmq via submodules, but we install the system
# zeromq dev package as well so HELICS_USE_SYSTEM_ZMQ-style builds also work.
APT_PACKAGES=(
    git
    cmake
    build-essential       # gcc, g++, make
    pkg-config
    libboost-dev          # HELICS uses Boost headers (program_options off by default)
    libzmq3-dev           # HELICS communication core
    libcurl4-openssl-dev  # GridLAB-D HTTP client
    libxerces-c-dev       # GridLAB-D XML parser
    libncurses-dev        # GridLAB-D ccmake / curses UI deps
    libreadline-dev       # GridLAB-D interactive shell
    autoconf              # Some submodule scripts shell out to autoconf
    automake
    libtool
    bison                 # GridLAB-D parser
    flex                  # GridLAB-D scanner
    gettext               # git-submodule status messages (cosmetic)
)

# -----------------------------------------------------------------------------
# Helpers
# -----------------------------------------------------------------------------
log()  { printf '\n[install-helics-deps] %s\n' "$*"; }
warn() { printf '\n[install-helics-deps] WARN: %s\n' "$*" >&2; }
die()  { printf '\n[install-helics-deps] ERROR: %s\n' "$*" >&2; exit 1; }

require_sudo() {
    if ! sudo -n true 2>/dev/null; then
        die "passwordless sudo is required (apt-get + cmake --install need root)"
    fi
}

helics_already_installed() {
    pkg-config --exists helics 2>/dev/null \
        && command -v helics_broker >/dev/null 2>&1 \
        && helics_broker --version >/dev/null 2>&1
}

gridlabd_already_installed_with_helics() {
    if ! command -v gridlabd >/dev/null 2>&1; then
        return 1
    fi
    # GridLAB-D ships HELICS as the `helics_msg` class inside the `connection`
    # module. `gridlabd --version` is module-agnostic, so probe the connection
    # module for the helics_msg class — present iff -DGLD_USE_HELICS=ON found
    # libhelics at configure time. Both the `gridlabd -L connection` output
    # and the linker reference to libhelics on connection.so must agree, so
    # also confirm the .so links against libhelics.
    gridlabd -L connection 2>&1 | grep -q "helics_msg" \
        && ldd /usr/local/lib/connection.so 2>&1 | grep -q "libhelics"
}

# -----------------------------------------------------------------------------
# 1. Apt prerequisites
# -----------------------------------------------------------------------------
install_apt_deps() {
    log "Installing apt build dependencies"
    require_sudo
    sudo apt-get update -y
    sudo apt-get install -y --no-install-recommends "${APT_PACKAGES[@]}"
}

# -----------------------------------------------------------------------------
# 2. HELICS 3
# -----------------------------------------------------------------------------
install_helics() {
    if helics_already_installed; then
        log "HELICS already installed (pkg-config + helics_broker present); skipping"
        return 0
    fi

    log "Cloning HELICS ${HELICS_TAG} into ${HELICS_DIR}"
    if [[ ! -d "${HELICS_DIR}/.git" ]]; then
        git clone "${HELICS_REPO_URL}" "${HELICS_DIR}"
    fi
    local helics_old_head helics_new_head
    helics_old_head="$(git -C "${HELICS_DIR}" rev-parse HEAD 2>/dev/null || echo "")"
    (
        cd "${HELICS_DIR}"
        git fetch --tags origin
        git checkout "${HELICS_TAG}"
        # Mandatory per the #251 constraint update: HELICS pulls
        # asio / zmq / fmtlib / spdlog / etc. as submodules.
        git submodule update --init --recursive
    )
    helics_new_head="$(git -C "${HELICS_DIR}" rev-parse HEAD)"
    # If the source tree advanced (or this is a re-run after a mid-build
    # failure where the install guard didn't trip), nuke the build dir so
    # cmake doesn't incrementally rebuild against mixed old/new sources.
    if [[ -n "${helics_old_head}" && "${helics_old_head}" != "${helics_new_head}" && -d "${HELICS_DIR}/build" ]]; then
        log "HELICS source advanced from ${helics_old_head:0:12}..${helics_new_head:0:12}; removing stale build dir"
        rm -rf "${HELICS_DIR}/build"
    fi

    log "Configuring HELICS build (prefix=${INSTALL_PREFIX})"
    (
        cd "${HELICS_DIR}"
        mkdir -p build
        cd build
        cmake .. \
            -DCMAKE_BUILD_TYPE=Release \
            -DCMAKE_INSTALL_PREFIX="${INSTALL_PREFIX}" \
            -DHELICS_BUILD_APP_LIBRARY=ON \
            -DHELICS_BUILD_APP_EXECUTABLES=ON \
            -DHELICS_BUILD_CXX_SHARED_LIB=ON \
            -DHELICS_BUILD_BENCHMARKS=OFF \
            -DHELICS_BUILD_EXAMPLES=OFF \
            -DHELICS_BUILD_TESTS=OFF \
            -DHELICS_DISABLE_C_SHARED_LIB=OFF
    )

    log "Building HELICS (-j${BUILD_JOBS})"
    cmake --build "${HELICS_DIR}/build" -j"${BUILD_JOBS}"

    log "Installing HELICS to ${INSTALL_PREFIX}"
    require_sudo
    sudo cmake --install "${HELICS_DIR}/build"

    # Refresh the dynamic linker cache so libhelics.so is discoverable for
    # subsequent gridlabd CMake configure calls in the same script invocation.
    sudo ldconfig

    if ! helics_already_installed; then
        die "HELICS install completed but pkg-config / helics_broker still missing"
    fi
    log "HELICS install verified: $(pkg-config --modversion helics)"
}

# -----------------------------------------------------------------------------
# 3. GridLAB-D (feature/1478, HELICS support)
# -----------------------------------------------------------------------------
install_gridlabd() {
    if gridlabd_already_installed_with_helics; then
        log "GridLAB-D already installed with HELICS support; skipping"
        return 0
    fi

    log "Cloning GridLAB-D (${GRIDLABD_BRANCH}) into ${GRIDLABD_DIR}"
    if [[ ! -d "${GRIDLABD_DIR}/.git" ]]; then
        git clone --branch "${GRIDLABD_BRANCH}" "${GRIDLABD_REPO_URL}" "${GRIDLABD_DIR}"
    fi
    local gridlabd_old_head gridlabd_new_head
    gridlabd_old_head="$(git -C "${GRIDLABD_DIR}" rev-parse HEAD 2>/dev/null || echo "")"
    (
        cd "${GRIDLABD_DIR}"
        git fetch origin "${GRIDLABD_BRANCH}"
        git checkout "${GRIDLABD_BRANCH}"
        git pull --ff-only origin "${GRIDLABD_BRANCH}"
        # Mandatory per the #251 constraint update.
        git submodule update --init --recursive
    )
    gridlabd_new_head="$(git -C "${GRIDLABD_DIR}" rev-parse HEAD)"
    # The script tracks feature/1478 (not a fixed SHA), so a re-pull may
    # advance the tip. If the install guard failed (e.g. mid-cmake death
    # on a prior run) AND the source advanced, blow away cmake-build/ so
    # the configure step starts clean against the new source tree.
    if [[ -n "${gridlabd_old_head}" && "${gridlabd_old_head}" != "${gridlabd_new_head}" && -d "${GRIDLABD_DIR}/cmake-build" ]]; then
        log "GridLAB-D source advanced from ${gridlabd_old_head:0:12}..${gridlabd_new_head:0:12}; removing stale cmake-build dir"
        rm -rf "${GRIDLABD_DIR}/cmake-build"
    fi

    log "Configuring GridLAB-D build (prefix=${INSTALL_PREFIX}, HELICS=ON)"
    (
        cd "${GRIDLABD_DIR}"
        mkdir -p cmake-build
        cd cmake-build
        cmake .. \
            -DCMAKE_BUILD_TYPE=Release \
            -DCMAKE_INSTALL_PREFIX="${INSTALL_PREFIX}" \
            -DGLD_USE_HELICS=ON
    )

    log "Building GridLAB-D (-j${BUILD_JOBS})"
    cmake --build "${GRIDLABD_DIR}/cmake-build" -j"${BUILD_JOBS}"

    log "Installing GridLAB-D to ${INSTALL_PREFIX}"
    require_sudo
    sudo cmake --install "${GRIDLABD_DIR}/cmake-build"

    sudo ldconfig

    if ! gridlabd_already_installed_with_helics; then
        die "GridLAB-D install completed but 'gridlabd --version' does not report HELICS support"
    fi
    log "GridLAB-D install verified (HELICS-enabled)"
}

# -----------------------------------------------------------------------------
# Main
# -----------------------------------------------------------------------------
main() {
    log "Starting HELICS + GridLAB-D provisioning"
    install_apt_deps
    install_helics
    install_gridlabd
    log "Provisioning complete."
    log "  pkg-config --modversion helics : $(pkg-config --modversion helics)"
    log "  helics_broker --version        : $(helics_broker --version 2>&1 | head -1)"
    log "  gridlabd --version             : $(gridlabd --version 2>&1 | grep -v '^[[:space:]]*$' | head -1)"
    log "  gridlabd -L connection (HELICS): $(gridlabd -L connection 2>&1 | grep -i 'classes' | head -1)"
    # Print resolved upstream SHAs so a re-run against a moved branch is
    # visible. The header documents 5aef1a5f as the GridLAB-D feature/1478
    # tip on 2026-06-02; the script tracks the branch (not the SHA), so
    # this line is the operator's check that the installed code matches
    # the documented snapshot.
    log "  HELICS HEAD                    : $(git -C "${HELICS_DIR}" rev-parse HEAD 2>/dev/null || echo "(repo missing)")"
    log "  GridLAB-D HEAD                 : $(git -C "${GRIDLABD_DIR}" rev-parse HEAD 2>/dev/null || echo "(repo missing)")"
}

main "$@"

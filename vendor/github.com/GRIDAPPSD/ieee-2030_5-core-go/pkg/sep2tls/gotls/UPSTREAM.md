# Upstream provenance: pkg/sep2tls/gotls

`pkg/sep2tls/gotls` is a fork of the Go standard library's `crypto/tls`,
carrying the mandatory IEEE 2030.5 / CSIP CCM-8 cipher suite that upstream
does not implement. This file records what it was forked from, what changed,
how to reproduce the diff, and how to check upstream security fixes against
it. It describes the tree at this branch only: it makes no statement about
the effect of any pull request that has not merged.

## Upstream base

- Release: **Go 1.22.0**
- Tag: `go1.22.0`
- Commit: `a10e42f219abb9c5bc4e7d86d9464700a42c7d57`
- Tag commit date (from `go.googlesource.com/go`): 2024-02-06

### How the base was identified

The fork carries no version marker, so the base was found by content match
rather than by record. The candidate window was narrowed from the file set
itself before any byte comparison ran:

- The fork has `cache.go` and `quic.go`, both absent before Go 1.21, so the
  base is Go 1.21 or later.
- The fork has neither `defaults.go` nor `ech.go`, both added in Go 1.23
  (encrypted ClientHello support), so the base predates Go 1.23.

That left the Go 1.21.x and Go 1.22.x lines as candidates. Predicted before
comparing: Go 1.22.x, on the reasoning that a fork adding a single cipher
suite is more likely cut from a stable minor release than rebased file by
file across a whole later cycle.

Comparison method: for each of the 20 files the fork shares with upstream
`crypto/tls` (the `FILES` list in `check-upstream.sh`), the fork's copy was
rewritten to swap `package gotls` back to `package tls` and the four local
stub import paths back to their real standard-library paths (see
"Deliberate changes" below), then passed through `gofmt`, then diffed
against each candidate release's copy of the same file.

Diff line counts below are the added-plus-removed content lines `diff -u`
reports, not counting the `---`/`+++` file-header line pair it prints once
per differing file; a wholly blank added or removed line counts as one
content line. Given a raw `diff -u` run saved to `out`, the exact command
is:

```
grep -Ec '^[+-]([^+-]|$)' out
```

| Candidate | Commit | Diff lines | Files differing |
|---|---|---|---|
| go1.21.13 (latest Go 1.21.x) | `8bba868de983dd7bf55fcd121495ba8d6e2734e7` | 221 | 10 of 20 |
| **go1.22.0** | `a10e42f219abb9c5bc4e7d86d9464700a42c7d57` | **0** | 0 of 20 |
| go1.22.12 (latest Go 1.22.x) | `5817e650946aaa0ac28956de96b3f9aa1de4b299` | 4 | 2 of 20 |

Go 1.22.0 is a byte-for-byte match, after `gofmt`, on every one of the 20
shared files.

Before trusting the zero: the same procedure run against go1.21.13 produced
221 changed lines and a nonzero exit. Both tags carry the same 22 non-test
files under `src/crypto/tls` (confirmed by listing both checkouts); the
difference is in file content, not file layout. The check can fail; it did,
against the wrong base.

go1.22.12 is not a byte-for-byte match: it differs from go1.22.0 by 4 lines
in `handshake_client.go` and `handshake_server.go`, where upstream added an
`!needFIPS() &&` guard around a debug counter increment
(`tlsrsakex.IncNonDefault()`) between go1.22.0 and go1.22.12. This is a real,
small upstream content change, not an import-ordering artifact, and it does
not disappear under `gofmt`. It has no effect on the fork's behavior, because
the fork's `needFIPS` always returns `false` (see `notboring.go`), so the
guarded call always runs regardless of the guard's presence. Go 1.22.0
remains the fork's exact, byte-for-byte base; later 1.22.x point releases are
not implied to match it and were not all checked (only go1.22.12, the latest,
was).

## Deliberate changes against the base

| Change | Files | Notes |
|---|---|---|
| Package rename `tls` -> `gotls` | all 20 non-test `.go` files carried from upstream | Required so the fork can be vendored as an ordinary importable package rather than the standard library's `crypto/tls`. |
| Internal package stand-ins | new files `stubs/boring/boring.go`, `stubs/cpu/cpu.go`, `stubs/fipstls/fipstls.go`, `stubs/godebug/godebug.go`; import sites: `stubs/fipstls` in `boring.go`; `stubs/boring` and `stubs/cpu` in `cipher_suites.go`; `stubs/godebug` in `common.go`, `conn.go`, and `handshake_client.go` | Upstream `crypto/tls` imports four packages under `internal/` or `crypto/internal/`, which are not importable outside the standard library: `crypto/internal/boring`, `crypto/internal/boring/fipstls`, `internal/cpu`, `internal/godebug`. Each is replaced by a local stub package of the same name and function signatures, with `Enabled`/`Required`/`HasAES` and friends hardcoded to their disabled/false values and `Setting.Value()` hardcoded to `""`. `notboring.go` is otherwise unmodified upstream code; it already assumes `boring.Enabled == false` at compile time via a build tag pair, which this stand-in preserves logically without reproducing the build-tag split. |
| CCM-8 cipher suite registration | new file `cipher_suites_ccm.go` | Registers `TLS_ECDHE_ECDSA_WITH_AES_128_CCM_8` (`0xC0AE`, RFC 7251), the suite IEEE 2030.5-2018 SEP2 mandates, via an `init()` function that appends to `cipherSuites` and prepends to `cipherSuitesPreferenceOrder` and `cipherSuitesPreferenceOrderNoAES`, so CCM-8 ranks ahead of the GCM suite in both orders (GRIDAPPSD/ieee-2030_5-core-go#136). It does not edit `cipher_suites.go` itself, which is why that file is byte-identical to upstream. The AEAD construction calls the sibling `pkg/sep2tls/ccm` package (a vendored pure-Go RFC 3610 CCM implementation), not a standard-library CCM. |
| Fork-only tests | new files `ccm_check_test.go`, `ccm_raw_test.go`, `cipher_suites_ccm_order_test.go` | Assert the CCM-8 suite is registered, exercise a raw handshake using it, and assert it ranks ahead of GCM in both preference orders. Not part of upstream and not part of the diff. |
| Dropped files | `generate_cert.go`, `fipsonly/fipsonly.go` (both upstream `crypto/tls`) | `generate_cert.go` is a `package main`, `//go:build ignore` standalone certificate-generation tool, not part of the `tls` package's compiled surface. `fipsonly/fipsonly.go` is a separate package (`crypto/tls/fipsonly`) that, when blank-imported, forces FIPS-only TLS configuration; it only exists under `GOEXPERIMENT=boringcrypto` and calls `crypto/internal/boring/fipstls` and `crypto/internal/boring/sig` directly, neither of which the fork stands in for. Neither file is carried into the fork. |

## Known divergences (not yet reconciled with upstream)

- **Legacy ClientHello handling**: `handshake_server.go` matches go1.22.0
  byte-for-byte (see the base comparison above), and go1.22.0's
  `handshake_server.go` does not contain the RFC 8446 Section 4.2.1 branch that
  rejects a pre-TLS-1.3-style ClientHello carrying `legacy_version =
  0x0304` with no `supported_versions` extension. That branch is present at
  go1.25.0 and go1.27.1, and absent at go1.22.0, go1.22.12, go1.23.0,
  go1.23.12, and go1.24.0 (checked directly against each tag's
  `handshake_server.go`). Whether it was backported to a later go1.24.x
  point release is not checked. Since the fork has never been rebased past
  go1.22.0, it does not have this change either way.
- **No AES-NI / hardware AES detection for cipher suite preference**:
  `stubs/cpu/cpu.go` hardcodes `HasAES` (and the other feature flags) to
  `false` on every architecture. `cipher_suites.go` reads this once, to
  compute `hasAESGCMHardwareSupport`, which selects between
  `cipherSuitesPreferenceOrder` and `cipherSuitesPreferenceOrderNoAES` in
  `handshake_client.go` and `handshake_server.go`. So the fork always
  negotiates as if no hardware AES acceleration were present, which can
  affect which cipher suite a handshake settles on. It does not affect
  whether AES itself runs in hardware: the AES primitives come from the
  standard library's `crypto/aes.NewCipher`, which does its own,
  independent CPU feature detection at the point the cipher is constructed.
  This is a property of the stand-in design, not an upstream-fix gap, and
  it holds regardless of which upstream base the fork is rebased to.

## Reproducing the diff and checking for unrecorded files

`check-upstream.sh`, in this directory, is the repeatable check. It needs
network access to `github.com/golang/go` (a read-only mirror of
`go.googlesource.com/go`, bounded to a 90-second clone) and `git`, `gofmt`,
`sed`, `diff`, `sha256sum`, `find`, `awk`, `cut`, `mktemp`, and `timeout` on
`PATH`. Run it from the repository root (the directory containing this
project's `go.mod`):

```
pkg/sep2tls/gotls/check-upstream.sh
```

It does three things:

1. Diffs the 20 shared files (normalized back to upstream package and
   import names, then `gofmt`-formatted) against the recorded upstream tag,
   the same comparison used to establish the base above. A file recorded
   as `patched` is not diffed against upstream this way; instead its
   recorded `upstream_sha256` (see "Recording a deliberate change" below)
   is checked against the live upstream file at the same pinned tag. This
   catches the manifest's recorded value going stale, most often after a
   maintainer bumps `UPSTREAM_TAG`. It does **not** detect a `crypto/tls`
   security release published after the pin while the pin itself stays
   put: the clone always fetches the same recorded `UPSTREAM_TAG` and
   `UPSTREAM_COMMIT`, so there is nothing for it to compare against.
   Picking up such a release is a manual step; see "Checking upstream
   `crypto/tls` security releases against the fork" below.
2. Checks every file listed in `upstream-manifest.sha256` against its
   recorded sha256. That file covers the fork-only files that have no
   upstream counterpart (`cipher_suites_ccm.go`, `ccm_check_test.go`,
   `ccm_raw_test.go`, and the four `stubs/*/*.go` files) and any shared file
   that has been deliberately patched (see "Recording a deliberate change"
   below). A content change to any of these with no matching manifest
   update is reported, as is a manifest line missing a required field.
3. Scans every file or symlink under `pkg/sep2tls/gotls` and reports any
   path that is neither one of the 20 shared files, a manifest entry, nor the check
   script, its `bats` test suite, the manifest, or this document. A new
   file added to the tree without being classified one way or the other is
   reported rather than passing silently.

Exit codes:

| Exit | Meaning |
|---|---|
| 0 | Clean: every shared file matches upstream (or a recorded patch whose upstream side still matches its recorded base hash), every manifest entry matches its recorded hash, no unrecorded file. |
| 2 | Environment: a required tool is missing, the script was not run from the repository root, the upstream clone/checkout could not be produced as recorded (bad tag, commit mismatch, sparse-checkout failure, network failure, an upstream file absent after checkout), the file walk under `pkg/sep2tls/gotls` or the normalization pass (creating the working directory, rewriting a file's package and import paths) could not be completed, a manifest type or upstream-hash lookup failed, or a `patched` manifest entry has no recorded `upstream_sha256`. This is a tooling or setup failure, not evidence of drift, and is never combined with the bits below: it always ends the run by itself. |
| any other nonzero | A bitwise OR of: **1** (drift: a shared file differs from upstream with no recorded patch, a manifest entry's hash no longer matches its recorded content, an expected shared file is missing, a `patched` file's recorded `upstream_sha256` no longer matches upstream, or a manifest line is malformed) and **4** (unrecorded: a file or symlink exists under `pkg/sep2tls/gotls` that this script cannot classify; add it to the `FILES` list in `check-upstream.sh` if it is meant to track an upstream file, or record it in `upstream-manifest.sha256` if it is fork-only). So exit 5 means both drift and an unrecorded file were found in the same run; neither overwrites the other. |
| 130 | Interrupted: SIGINT (Ctrl-C) arrived while cloning upstream; the run exited at once instead of continuing. 128+2, the shell's usual "killed by signal" convention, not a combination of the bits above. |
| 143 | Interrupted: SIGTERM arrived while cloning upstream, for the same reason and the same 128+signal convention. |

A nonzero exit prints one or more messages on stderr naming which check
failed and why; read the message to tell drift, an environment failure,
and an unrecorded file apart, since each calls for a different fix, and a
combined exit code (5) means more than one message is present. 130 and
143 are not part of that bitwise scheme: the run stopped before it could
compute either bit, so neither message appears.

## Running this check automatically

`.github/workflows/gotls-upstream-check.yml` runs `check-upstream.sh` and
the `bats` suite on every pull request that touches `pkg/sep2tls/gotls/`,
and fails the job on a nonzero exit from either. A change to a covered
file that is merged without this job having run (for example, a manifest
edit outside a pull request) is not covered by this automation and
should be verified by hand before relying on it.

## Recording a deliberate change

Three situations call for a manifest update rather than a code change.
`upstream-manifest.sha256`'s header documents the exact column format
(`type<TAB>path<TAB>sha256<TAB>upstream_sha256<TAB>note`).

- **A new fork-only file** (for example, a new stub or a new fork-only
  test): add a `fork-only` line with `sha256sum pkg/sep2tls/gotls/<path>`
  for the `sha256` column, `-` for `upstream_sha256` (fork-only files have
  no upstream counterpart), and a short note of why the file exists.
  Without this, `check-upstream.sh` reports it as unrecorded (exit 4).
  Also add the file to the matching row of the "Deliberate changes" table
  above: the manifest update alone does not keep that table's file lists
  in sync, and `check-upstream.sh` cannot detect a table that has fallen
  behind the manifest.
- **An edit to an existing fork-only file** (for example, PR #143 editing
  `cipher_suites_ccm.go`): re-run `sha256sum` on the changed file and
  replace the `sha256` column on its existing `fork-only` line. Do not add
  a new line and do not touch `upstream_sha256` (still `-`); this is a
  manifest hash update, not a new-file registration, and needs no CVE or
  release note. Without it, `check-upstream.sh` reports the file as
  drifted (exit 1) against its old hash. If the edit also changes behavior
  this document describes elsewhere (for example, whether CCM-8 appends or
  prepends to a preference order), update that description too: a clean
  hash check proves the manifest matches the file, not that the prose
  above still matches the file.
- **A hand-ported fix to a shared file** (see the security-release
  procedure below): after making the change, add or update a `patched`
  line for that file with its current `sha256sum` in the `sha256` column,
  the sha256sum of the SAME file at the fork's recorded `UPSTREAM_TAG`
  (go1.22.0, i.e. the base the fork has not rebased past, not the fixed
  release used only to see the delta) in the `upstream_sha256` column, and
  a note naming the release or advisory the fix came from. Once a shared
  file has a `patched` entry, `check-upstream.sh` stops diffing the fork's
  copy against upstream and instead checks the fork's hash against
  `sha256` and the live upstream file's hash against `upstream_sha256`:
  the first catches the fork's patch changing or reverting, the second
  catches the recorded `upstream_sha256` going stale against the pinned
  base, which in practice means it fires after `UPSTREAM_TAG` is bumped
  without this entry being re-verified. It does not, by itself, detect a
  later `crypto/tls` security release: the live upstream file it compares
  against is always fetched from the same pinned `UPSTREAM_TAG` and
  `UPSTREAM_COMMIT`, which do not change on their own. Watching for and
  hand-porting a security release is the manual procedure below, in
  "Checking upstream `crypto/tls` security releases against the fork".

## Checking upstream `crypto/tls` security releases against the fork

1. Watch the Go security announcements
   (`https://groups.google.com/g/golang-announce`) or the release notes at
   `https://go.dev/doc/devel/release` for any release whose fix list
   mentions `crypto/tls`.
2. For each such release, fetch the affected file(s) from
   `https://go.googlesource.com/go/+/refs/tags/<tag>/src/crypto/tls/<file>`
   (append `?format=TEXT` for a base64 body, or temporarily edit
   `UPSTREAM_TAG` and `UPSTREAM_COMMIT` in a scratch copy of
   `check-upstream.sh` to the fixed release and run it against the current
   base's checkout, to see the security delta directly) and compare the
   affected function against the fork's copy of the same file.
3. If the fix applies to a file the fork carries unmodified from go1.22.0,
   port the fix by hand into the fork file, add a new row to the
   "Deliberate changes" table above naming the CVE or release and the
   file(s) touched, and record the change per "Recording a deliberate
   change" above: `upstream_sha256` is the sha256sum of that file at
   go1.22.0 (the checkout `check-upstream.sh` already produces at its
   recorded `UPSTREAM_TAG`), not at the fixed release used in step 2 to
   view the delta. Do not bump `UPSTREAM_TAG` in `check-upstream.sh` unless
   every shared file has been re-verified against the new base by the same
   procedure used to establish go1.22.0 above; a partial rebase would make
   the recorded base a false claim about files that were not actually
   re-diffed.
4. If the fix applies to `generate_cert.go`, `fipsonly/fipsonly.go`, or any
   other file the fork does not carry, no action is needed here; note it
   was checked and found inapplicable.

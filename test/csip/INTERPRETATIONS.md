# CSIP V1.2 Spec Interpretations

This file documents every spec-interpretation call surfaced during the
Plan-2 wiring of `test/csip/` against SunSpec CSIP V1.2. Section references
below are to `SunSpecCSIPConformanceTestProceduresV1.2.pdf`, 2019-07-24.

Each entry covers:

- **The ambiguity** - the spec wording or section 4 marking that was not
  unambiguous against the server profile we ship.
- **The interpretation** - the call we made.
- **Where implemented** - file + ticket that landed the call.
- **Decision lineage** - date and where the decision was recorded
  (journal entry, PR review thread, phase doc).

Imported into #194 (self-attestation letter) Section 3 (Spec
Interpretations) at attestation time. Reviewed by Dutch + Leon as the
test gate for #193.

---

## 1. COMM-001 (xmDNS Discovery) treated as SKIPPED-OPTIONAL

**Ambiguity.** CSIP V1.2 section 4 Profile Test Conformance (pp 19-20) marks
COMM-001 unmarked in the Server column - the section 4 matrix marks Server-
required rows with an explicit indicator, and COMM-001's row is blank
under Server. This contrasts with COMM-002, COMM-003, COMM-004 which
are marked. The section 5.1 procedure body does not restate the section 4 marking.

**Interpretation.** COMM-001 is **optional** for the Server profile.
The section 4 marking is authoritative; an unmarked Server cell is read as
"not required." The server still publishes `_smartenergy._tcp` via
`internal/discovery/` (already in production), but no `test/csip/`
file asserts the discovery semantics end-to-end. xmDNS multicast on
CI runners is finicky (self-hosted runner network may not allow
multicast loopback), and the cost-benefit of wiring a multicast
sniffer for an optional row exceeded the budget for Phase 5.

**Implemented in.** (n/a - intentionally absent from `test/csip/`.)

**Decision lineage.** Phase 5 closure note, journal 2026-05-08 (see
`projects/ieee-2030_5-go/journal.md` for that date). Re-confirmed at
Phase 8 walk on 2026-05-13. Decision-maker: Pike.

---

## 2. COMM-003 (Basic Security) lax-mode cipher assertion

**Status: superseded.** The server now offers CCM-8 only, unconditionally,
under every `make run*` target; there is no lax mode, no GCM fallback,
and no `SEP2_CSIP_STRICT` toggle. `comm_003_basic_security_test.go`
asserts CCM-8 directly (`TestCOMM_003_BasicSecurity_CCM8Only`). The
ambiguity and interpretation below record the Phase 5 skeleton-era call
that this superseded; kept for the #194 attestation lineage.

**Ambiguity (as it stood in Phase 5).** CSIP V1.2 section 5.3 step 2
requires:

> "The server selects a cipher suite from the offered set, completes
> the handshake, and the negotiated cipher suite is
> TLS_ECDHE_ECDSA_WITH_AES_128_CCM_8 (0xC0AE)."

The server already supported CCM-8 under `make run-ccm` (the fork vendored
at `vendor/github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2tls/gotls/`
registers 0xC0AE). The default
`csiptest.BootServer()` path used the stdlib `crypto/tls` GCM path -
no CCM-8 registration - because the CCM build was opt-in via
`SEP2_CSIP_STRICT=true` (#22 work-in-progress; tracked at
GRIDAPPSD/ieee-2030_5-go#20).

**Interpretation (as it stood in Phase 5).** COMM-003's assertion accepted
CCM-8 (0xC0AE) OR GCM (0xC02B / 0xC02C) under default boot. The test prose
explicitly flagged this as a skeleton-pending-#22 condition, to tighten to
CCM-8-only once `SEP2_CSIP_STRICT=true` propagated through `BootServer`.

The conformance claim under this now-superseded interpretation was: "the
server binary negotiates CCM-8 end-to-end under `make run-ccm` (verified in
`test/csip/handshake_test.go`); the unit-level CSIP harness asserts
the negotiated cipher is in the CSIP-permitted set, with strict-mode
tightening pending #22." See INTERPRETATIONS.md section 3 for the
COMM-004 cert-variant scope this depended on.

**Implemented in.** `test/csip/comm_003_basic_security_test.go`
(#22 escalation; landed as test skeleton under Phase 5).

**Decision lineage.** Phase 5 ticket #22 file note, journal
2026-05-08. PR review thread on the #22 follow-up confirmed
"land skeleton, tighten in #22" by Dutch + Leon. Decision-maker:
Pike.

---

## 3. COMM-004 (Advanced Security) minimum-viable cert variants

**Ambiguity.** CSIP V1.2 section 5.4 requires the server reject:

- Invalid MICA Extended Key Usage
- Invalid MICA Name (Non-Critical)
- Invalid MICA Policy Mapping (Non-Critical)
- Self-signed device cert

Across chain lengths: 2-link (SERCA -> Device), 3-link
(SERCA -> MICA -> Device), 4-link (SERCA -> MCA -> MICA -> Device). That is
six cert-fixture variants for the happy paths, plus four
broken-variant negatives.

Frank's cert-materials inventory (`artifacts/outputs/frank-csip-cert-materials.md`)
flagged that no on-disk SunSpec/QualityLogic SERCA test PKI exists in
the workspace today; until Quinn surfaces one, fixtures must be
locally rooted (workstation-generated SERCA).

**Interpretation.** COMM-004 ships with minimum-viable cert variants:

- `test/csip/handshake_test.go` covers the SunSpec V1.2 self-rooted
  PKI happy path (CCM-8 cipher negotiation under env-gated fixtures).
- `test/csip/testdevice_handshake_test.go` covers the self-minted test
  device PKI variant (committed under `testdata/csip-pki/testdevice/`)
  with a CSIP section 6.11-compliant device cert (HardwareModuleName SAN,
  empty Subject, KeyUsage, BasicConstraints). Exercises the server
  handling a compliant cert in default (non-strict) mode.

The four broken-variant negatives (invalid MICA ext-key, invalid MICA
name, invalid MICA policy mapping, self-signed device) are **NOT**
wired today. The full 6-cert-chain x broken-variant matrix is half a
day of crypto-fixture work and was descoped from Phase 5 by mutual
agreement (Pike + Frank + Craig) on 2026-05-09. The conformance claim
under this interpretation is: "TLS-layer cert verification is exercised
via two PKI variants; full negative-variant matrix is acknowledged as
an out-of-scope cert-fixture exposure documented here."

**Recommended follow-up.** #22 strict mode landing will surface
which broken-variants the server rejects loudly versus silently -
filing per-variant negative tickets at that point is cheaper than
generating all six fixtures now and discovering the server quietly
accepts some.

**Implemented in.** `test/csip/handshake_test.go`,
`test/csip/testdevice_handshake_test.go` (#19, #75).
COMM-004 row in the matrix is DOCUMENTED-EXCEPTION pointing here.

**Decision lineage.** Phase 5 PR review (#75 thread),
2026-05-09. Re-confirmed at Phase 8 walk. Decision-maker: Pike, sign-
off Craig.

---

## 4. CORE-002 (HTTP Response) "500 vs 501" + 501-fallback skeleton

**Ambiguity.** CSIP V1.2 section 5.5 step 7 says:

> "Client issues GET on an advertised function-set the server does
> not implement. Server responds HTTP 501 Not Implemented."

The pass criteria line in the PDF (section 5.5, last paragraph) reads "HTTP
500 Not Implemented" - this is a **PDF typo**. The step body
consistently says 501. Reason code 501 is the HTTP semantic match.

Independent of the typo: today's server router (`internal/server/`)
does not advertise any function-set link in `/dcap` that lacks a
registered sub-route. Every `dcap.*Link` field either resolves to a
real handler or is omitted from the response. The server therefore
has no production code path that returns 501. The CORE-002 test
cannot probe a 501 response because the server never emits one in
default config.

**Interpretation.** Match the procedure step (501), not the pass-
criteria line (500). Ship CORE-002 as a documented skeleton that
probes for a 501-returning endpoint; t.Skip with explicit rationale
when none exists. Once a future change introduces an advertised-but-
unimplemented function-set link (e.g. a per-handler stub for the
CustomerAccount / DemandResponseProgram / File / Prepayment /
TariffProfile FSA-base links DCAP could advertise), the skeleton
trips back to a real assertion automatically.

The skeleton is **not** a silent pass: it logs the gap reason and
points at the spec-typo + this INTERPRETATIONS entry.

**Implemented in.** `test/csip/core_002_http_response_test.go`
(#54).

**Decision lineage.** Phase 3 closure note (#54 PR review),
2026-05-04. Spec-typo flagged in Phase 1 baseline matrix
(recorded internally).
Decision-maker: Pike, validated by Dutch.

---

## 5. BASIC-007 (Ramp Rates) DefaultDERControl-only fixture handling

**Ambiguity.** CSIP V1.2 section 8.7 specifies the inverter ramp-rate
control via `setGradW` and `setSoftGradW`. Unlike the other inverter-
mode tests (section 8.4 LVRT/HVRT, section 8.6 Volt/Var, section 8.8 Fixed PF, etc.) which
exercise both `DefaultDERControl` and `DERControl`-attached events,
BASIC-007 ramp rates is **DefaultDERControl-only** - there is no
event-driven ramp-rate DERControl in the procedure. This is not
flagged loudly in the section 8.7 prose; it surfaces when wiring the test
and finding no DERControl shape applies.

**Interpretation.** The shared fixture loader (`basic_mode_helpers_test.go`)
distinguishes "default-only" mode from the standard "default +
event" shape. BASIC-007's fixture path loads exactly one
DefaultDERControl carrying `setGradW` + `setSoftGradW`, with no
DERControl events at all. The assertion walks DefaultDERControlLink
from the DERProgram and confirms the ramp-rate fields render
conformantly.

Noor flagged this in the Phase 1 baseline matrix (Section 5, last
bullet) as an item to confirm with the loader API design. Pike's
loader implementation (#135 / #140) explicitly supports the
default-only shape.

**Implemented in.** `test/csip/basic_007_ramp_rates_test.go`
(#135, #140). Helper:
`test/csip/basic_mode_helpers_test.go` - default-only branch.

**Decision lineage.** Phase 4 PR review (#135 thread), 2026-05-06.
Phase 1 baseline matrix flag. Decision-maker: Pike.

---

## 6. BASIC inverter-control modes - DERControl response-field
   assertions pinned by #140

**Ambiguity.** CSIP V1.2 sections 8.4-8.12 procedures step through "Server
exposes opMod`X` field on the DERControl; client reads it and applies
the mode." The opMod field set in `pkg/sep2` today covers the
mainstream modes (opModFixedPFInjectW, opModVoltVar, opModVoltWatt,
opModFreqWatt, opModConnect, opModEnergize, opModMaxLimW), but a
handful of fields specified in V1.2 (notably the LVRT/HVRT
must-trip + momentary-cessation curves' wire-level field tags) were
not present in the in-flight `pkg/sep2` types at the time of Phase 4
wiring.

**Interpretation.** Each BASIC-* test that hits a missing-field gap
runs the procedure walk up to the field-assertion step, then
`t.Skip`s with a `// Pinned by #140 - implementation gap`
comment referencing the follow-up ticket. The DERCurveList walk leg
(curve-based modes) still runs unconditionally - DERCurve is
present in `pkg/sep2` and renders correctly via `/dc`. This was a
deliberate scope-boundary call: Phase 4 (#135) added the test
files without modifying `pkg/sep2`; field additions ride on
#140.

**Implemented in.** Helpers in
`test/csip/basic_mode_helpers_test.go` (the gating logic).
Per-mode files: `basic_004_lvrt_hvrt_test.go`,
`basic_005_lfrt_hfrt_test.go`, `basic_007_ramp_rates_test.go`,
`basic_011_volt_watt_test.go`, `basic_012_freq_watt_test.go`.

**Decision lineage.** Phase 4 closure note (#135 PR review),
2026-05-06. #140 backlog entry. Decision-maker: Pike, validated
by Dutch.

---

## 7. ERR-002 (Subscription Survival) - in-memory fake-restart

**Ambiguity.** CSIP V1.2 section 11 ERR-002 step 4 specifies:

> "Server experiences a power-reset event. After server restart,
> subscriptions are still active."

Today's subscription manager (`internal/subscription/manager.go`)
keeps subscriptions in memory only; an actual server restart loses
all subscriptions. ERR-002 as literally written requires persistent
subscription storage.

**Interpretation.** ERR-002 is satisfied via a **test-only
fake-restart hook** that preserves in-memory subscription state
across the simulated restart (the in-process csiptest server stops
and restarts under the same subscription manager instance). This
satisfies the procedure mechanics (subscription survives the
restart event and is cancellable per step 5) without persistent
storage. Persistent storage is a future-deployment concern, not a
V1.2 conformance gate.

This was Noor's Path A recommendation in the Phase 1 matrix
(Section 4, ERR-002 subsection). Path B (persistent subscription
store) remains on the future backlog under #25.

**Implemented in.** `test/csip/err_002_subscription_survival_test.go`
(#224). The fake-restart hook lives in
`test/csip/csiptest/server.go` via a "soft restart" helper that
preserves the subscription manager across an HTTP listener
re-bind.

**Decision lineage.** Phase 1 matrix recommendation (Noor),
2026-05-11. Phase 5 #224 PR review confirmed Path A.
Decision-maker: Craig (sign-off), Pike (implementation).

---

## 8. MAINT mutation surface - `csip_test_hooks` build tag

**Ambiguity.** CSIP V1.2 section 11 MAINT-* procedures (BASIC-003, MAINT-003,
MAINT-004, MAINT-005) require the server to perform mid-flight
mutations (swap FSA assignments, change DERProgram primacy, add a
DERControl to an existing DERProgram, etc.) on behalf of the test.
These are inherently administrative operations not exposed by the
default CSIP-mode HTTP surface.

**Interpretation.** A `csip_test_hooks` build-tag-gated mutation HTTP
surface (#27) hosts the mid-flight mutation endpoints. The
surface compiles into the binary only under
`go build -tags csip_test_hooks`; production builds have zero
mutation surface. CI's #192 `csip` job runs the matrix axis with
`[off, on]` for the build tag - both axes must pass.

The mutation surface listens on the in-process test server and is
gated by `SEP2_TEST_MUTATION_TOKEN`. Per #27's design note
(security-reviewed), the token is not a secret - the surface only
exists in test builds and only listens on the in-process test
server bound by `csiptest.BootServer`.

**Implemented in.** Mutation HTTP routes in
`internal/server/` under `//go:build csip_test_hooks`. Test files
that consume the surface: `test/csip/basic_003_advanced_group_mgmt_test.go`,
`test/csip/maint_001_inverter_oob_test.go`,
`test/csip/maint_004_controls_test.go`,
`test/csip/maint_005_primacy_swap_test.go`.

**Decision lineage.** Phase 1 baseline matrix recommendation
(Noor, Section 4), carried through the Phase 5 PR review (Leon
security-cleared the token rationale), 2026-05-09, tracked under #27.
Decision-maker: Pike, security sign-off Leon.

---

## Cross-references

- Phase 1 baseline coverage matrix: recorded internally.
- Final coverage matrix (#193 output):
  `projects/ieee-2030_5-go/artifacts/outputs/csip-v1.2-final-coverage-matrix.md`
- Phase 8 doc:
  `projects/ieee-2030_5-go/plans/plan-2-csip-server-conformance/phase-8-coverage-gap-analysis.md`
- Frank's cert materials inventory:
  `projects/ieee-2030_5-go/artifacts/outputs/frank-csip-cert-materials.md`
- CSIP V1.2 spec PDF:
  `projects/ieee-2030_5-go/artifacts/inputs/SunSpecCSIPConformanceTestProceduresV1.2.pdf`

# Conformance harnesses and standards access

This directory holds conformance harnesses that measure this server against
the normative IEEE 2030.5 documents.

It also documents, in one place, **how this project gets at the normative
standards artifacts**. If you are an agent or a contributor asked to verify a
requirement, read this file first: you should not need anyone to paste a path
into your instructions.

## The two normative artifacts

| Artifact | What it decides | Environment variable |
|---|---|---|
| `sep.xsd` | Whether a message is well-formed against the schema | `SEP2_SCHEMA_PATH` |
| `sep_wadl.xml` | Which resources and methods exist, and at what conformance level | `SEP2_WADL_PATH` |

**Neither is committed to this repository, in any form.** Both are published
by IEEE. See [NOTICE](../NOTICE) for attribution and for how to obtain them at
no charge through the IEEE GET Program.

The schema gate lives in the core module (`ieee-2030_5-core-go`, packages
`schema` and `internal/xsdgate`). The WADL gate lives here, in
[`wadl/`](wadl/). The two contracts are deliberately identical in shape, so
learning one teaches you the other.

## Where the standards live locally

On a workstation provisioned for this project, the IEEE 2030.5 standards
library, including all three editions, the CSIP guides, and the XML artifacts,
is checked out under the knowledge workspace at:

```
projects/ieee-2030_5/ieee-2030_5-go/artifacts/inputs/2030_5/
```

The XML model release, which is where both artifacts live, is under
`xmlspy-project/` within that tree:

```
.../artifacts/inputs/2030_5/xmlspy-project/sep.xsd
.../artifacts/inputs/2030_5/xmlspy-project/sep_wadl.xml
```

### The `2030_5` component is a symlink

This has already cost one investigation, which concluded a requirement "could
not be verified" because a search never reached the documents.

`2030_5` is a **symlink** to a directory outside the workspace. `find` does
not follow symlinks unless you ask it to, so this finds nothing:

```sh
find artifacts/inputs -name 'sep_wadl.xml'      # silently empty
```

and this works:

```sh
find -L artifacts/inputs -name 'sep_wadl.xml'   # note -L
```

`grep -r`, `rg`, and most editor-indexed searches have the same behaviour by
default. When you are looking for a standards document and come up empty,
check for the symlink before concluding the document is absent. Passing the
path explicitly, rather than searching for it, avoids the problem entirely.

## The contract, in four cases

Both `SEP2_SCHEMA_PATH` and `SEP2_WADL_PATH` behave identically:

1. **Unset, and no copy at the conventional location: SKIP.** The gated tests
   skip and the rest of the suite runs normally, so a contributor who has not
   obtained the standard still gets a green build. `go test` reports a skip,
   never a pass, and each skipped test names the variable and this file.

2. **Set to a path that does not resolve: HARD ERROR.** An explicitly
   configured path that is wrong is a misconfiguration, not an absence.
   Reporting it as "not available" would let a broken CI secret masquerade as
   a missing copy and skip the whole gate while reporting green.

3. **A copy that is the wrong document: HARD ERROR.** Both loaders verify a
   digest over a normalized form of the file (BOM and carriage returns
   stripped). Measuring against the wrong document is worse than not
   measuring, because it reports conformance against requirements the standard
   never stated.

4. **Absence made fatal on demand.** Set `SEP2_SCHEMA_REQUIRED=1` or
   `SEP2_WADL_REQUIRED=1` and an absent copy becomes a hard failure instead of
   a skip. CI that supplies an artifact sets this. The value must be a boolean
   that Go's `strconv.ParseBool` accepts; **anything else is itself an error**,
   so `SEP2_WADL_REQUIRED=ture` cannot silently disarm the gate.

## Running the WADL conformance sweep

From a clean checkout, with no standards artifacts present:

```sh
go test ./test/conformance/...
```

The WADL-gated tests skip; everything else passes.

With a licensed copy:

```sh
export SEP2_WADL_PATH=/path/to/sep_wadl.xml
go test ./test/conformance/...
```

Or drop the file at the conventional location, which is gitignored so it
cannot be committed by accident:

```
test/conformance/wadl/sep_wadl.xml
```

`SEP2_WADL_PATH` wins over the conventional location.

To prove the gate actually armed, rather than skipped:

```sh
go test -run '^TestWADLGateArmed$' -v ./test/conformance/wadl/
```

`TestWADLGateArmed` **passes only when a WADL was located, digest-verified,
and parsed**. It is the single test name to look at: a PASS means the sweep
really drove from the standard, a SKIP means nothing was measured against it.
This mirrors `TestSchemaGateArmed` in the core module.

To record the full row-by-row sweep for analysis, set an output path. It is
off by default and the output is a record of what this server did, not a copy
of the standard:

```sh
SEP2_WADL_SWEEP_OUT=/tmp/sweep.jsonl go test -v ./test/conformance/wadl/
```

## How the sweep decides "unrouted"

The most consequential distinction in the report is between a path that is
**not routed at all** (a spec gap) and a path that is **routed but empty** (a
fixture gap). Both return 404. The sweep separates them **two independent
ways**, and cross-checks the two:

1. **The wire.** Go's `net/http` ServeMux writes exactly `404 page not found`
   when no pattern matches. A handler that ran and found nothing writes its
   own body. `TestMuxNotFoundBodyIsStillTheStdlibMarker` asserts this stdlib
   behaviour against a live mux, so a Go upgrade that changes the text fails
   loudly instead of silently reclassifying every unrouted row.

2. **The router's own enumeration.** `BuildProtocolRouter` returns the list of
   patterns it registered. `csiptest.BootedServer.MountedPatterns` carries the
   list **from the same router instance that serves the requests**, and the
   sweep replays it into a fresh `ServeMux` so matching uses the real matcher
   rather than a second, subtly different implementation.

When the two disagree, `TestWADLConformanceSweep` fails before reporting any
conformance number, because a disagreement means the harness or a middleware
is wrong and every routing verdict in that run is suspect.

`HEAD` is a special case: the stdlib strips the body, so discriminator 1
cannot fire. The sweep borrows the evidence from the `GET` on the same path
(GETs always run first) and reports `unrouted_indeterminate` when no such
evidence exists, rather than guessing.

## What the sweep gates on

Deliberately narrow: the integrity of the sweep itself, plus failures that are
wrong under any reading of the standard (a 5xx, a transport failure). It does
**not** fail on every non-conformant row, because the conformance gap is a
known, tracked body of work, and a test that is red by design gets muted.

The gap is reported in full, and `TestMandatoryRouteCoverageRatchet` keeps it
from widening: it pins a ceiling on the number of Mandatory methods that are
unrouted. A fix that lowers the number passes and logs the new value to pin.

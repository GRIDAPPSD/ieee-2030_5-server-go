# Admin dashboard selector map

The operator dashboard at `GET /` is now the embedded Svelte admin UI
(`pkg/adminui/web/frontend/`). It replaced a single server-rendered
HTML string, which the Playwright suite in `e2e/` addresses almost
entirely by element `id`.

This table records, for every selector that suite uses, which component
renders the element now and whether the selector still resolves. It exists
so the Playwright port can be scoped from evidence rather than guessed at:
the answer is that **every selector survives unchanged**, so the port is a
question of what the specs assert, not of rewriting how they find things.

## Counting

Scope: `e2e/dashboard.spec.ts`, `e2e/add_device.spec.ts`,
`e2e/fsa_hierarchy.spec.ts` (334 lines total). `e2e/chart_offline.spec.ts`
is excluded: it was written against the new UI.

| Measure | Count |
|---|---|
| `.locator(...)` occurrences | 39 |
| Distinct locator arguments | 28 |
| ... of which are `id` selectors | 24 (23 literal, 1 built from an mRID at runtime) |
| ... of which are not `id` selectors | 4 |
| Distinct `getByRole('button', ...)` names | 7 |
| Distinct `getByText(...)` arguments | 11 |

No `.locator(` call in these files spans more than one line, so a
line-based extraction of them is complete.

## `id` selectors

All 24 `id` selectors the suite addresses keep the same `id`. No element
needed a `data-testid` substitute, because none disappeared. The table
also lists the 7 ids the previous markup carried that the suite does not
address, which are preserved too, plus one id this work added.

| Selector | Rendered by | Tab | Addressed by the suite | Status |
|---|---|---|---|---|
| `#tlsMode` | `panels/NavBar.svelte` | every tab | yes | same id |
| `#uptime` | `panels/NavBar.svelte` | every tab | yes | same id |
| `#deviceCount` | `panels/NavBar.svelte` | every tab | no | same id |
| `#bigDeviceCount` | `panels/Overview.svelte` | Overview | yes | same id |
| `#mupCount` | `panels/Overview.svelte` | Overview | yes | same id |
| `#tlsCipher` | `panels/ServerInfo.svelte` | Overview | no | same id |
| `#uptimeDetail` | `panels/ServerInfo.svelte` | Overview | no | same id |
| `#hwSerial` | `panels/CertPanel.svelte` | Certificates | yes | same id |
| `#hwType` | `panels/CertPanel.svelte` | Certificates | no | new: the route rejects a device cert request without the PEN OID |
| `#certResult` | `panels/CertPanel.svelte` | Certificates | yes | same id |
| `#controlType` | `panels/DerControl.svelte` | Control | yes | same id |
| `#controlValue` | `panels/DerControl.svelte` | Control | no | same id, hidden for connect/disconnect (#567) |
| `#controlResult` | `panels/DerControl.svelte` | Control | yes | same id |
| `#controlDevice` | `panels/DerControl.svelte` | Control | yes | new (#567): device select, added when the card was wired to the admin API |
| `#controlProgram` | `panels/DerControl.svelte` | Control | yes | new (#567): DER program select, populated from `GET /api/devices/{id}/der-programs` |
| `#controlExcitation` | `panels/DerControl.svelte` | Control | no | new (#567): shown only for `fixedPFInjectW` |
| `#controlStartNow` | `panels/DerControl.svelte` | Control | no | new (#567) |
| `#controlStartAt` | `panels/DerControl.svelte` | Control | no | new (#567): shown only when Start now is unchecked |
| `#controlDuration` | `panels/DerControl.svelte` | Control | yes | new (#567) |
| `#controlDescription` | `panels/DerControl.svelte` | Control | no | new (#567) |
| `#derControlsTable` | `panels/DerControl.svelte` | Control | yes | new (#567): the program's admin-issued controls |
| `#addDevCert` | `panels/AddDevice.svelte` | Devices | yes | same id |
| `#addDevSFDI` | `panels/AddDevice.svelte` | Devices | yes | same id, still readonly |
| `#addDevLFDI` | `panels/AddDevice.svelte` | Devices | yes | same id, still readonly |
| `#addDevDesc` | `panels/AddDevice.svelte` | Devices | yes | same id |
| `#addDevPIN` | `panels/AddDevice.svelte` | Devices | yes | same id |
| `#addDevEnabled` | `panels/AddDevice.svelte` | Devices | no | same id |
| `#addDevResult` | `panels/AddDevice.svelte` | Devices | yes | same id |
| `#lookupLFDI` | `panels/LookupDevice.svelte` | Devices | yes | same id |
| `#lookupResult` | `panels/LookupDevice.svelte` | Devices | yes | same id |
| `#newFSADesc` | `panels/CreateFsa.svelte` | FSAs | yes | same id |
| `#newFSAMRID` | `panels/CreateFsa.svelte` | FSAs | yes | same id |
| `#newFSAPrimacy` | `panels/CreateFsa.svelte` | FSAs | yes | same id |
| `#createFSAResult` | `panels/CreateFsa.svelte` | FSAs | yes | same id |
| `#deviceTable` | `panels/DeviceTable.svelte` | Devices | yes | same id, still the `tbody` |
| `#assignSel-{deviceId}` | `panels/DeviceTable.svelte` | Devices | no | same id pattern, still keyed by the href's last segment |
| `#topologyTree` | `panels/TopologyTree.svelte` | FSAs | yes | same id |
| `#attachInp-{mRID}` | `panels/FsaNode.svelte` | FSAs | yes (built from an mRID at runtime) | same id pattern |
| `#attachResult-{mRID}` | `panels/FsaNode.svelte` | FSAs | no | same id pattern |
| `#activityChart` | `panels/ActivityChart.svelte` | Overview | yes | same id |

24 rows are marked yes, which is the suite's `id` selector count. Of the
8 marked no, 7 are preserved ids the suite does not currently use and one
(`#hwType`) is new.

## Non-`id` selectors

| Selector | Rendered by | Tab | Status |
|---|---|---|---|
| `.navbar h1` | `panels/NavBar.svelte` | every tab | survives: still an `h1` inside `.navbar` |
| `th:has-text("SFDI")` | `panels/DeviceTable.svelte` | Devices | survives: same `thead` row |
| `th:has-text("LFDI")` | `panels/DeviceTable.svelte` | Devices | survives: same `thead` row |
| `button:has-text("Delete")` | `panels/FsaNode.svelte` | FSAs | survives: still rendered at the system root only |

## Button and text locators

Every `getByRole('button', ...)` name is unchanged: Generate Device Cert,
Send, Parse Cert, Add Device, Lookup, Create FSA, Refresh. So are the
`getByText` strings, including the uppercase card headings, which are
still `h2` elements uppercased by CSS rather than in the markup.

New controls that had no previous equivalent carry `data-testid`: Download
CA, Save certificate, Save private key, Detach (per program), Unassign
(per assigned device), and the FSA template table. One new form input,
`#hwType`, carries an id instead, matching the convention every other
input in that card already uses.

## Tab bar selectors

The dashboard is split into tabs, each a client-side route under `/ui/`.
The tab bar is rendered by `AdminShell.svelte` below the header, outside
`.navbar`, and only when an admin session exists. It adds selectors and
changes no existing one.

| Selector | Rendered by | Tab | Notes |
|---|---|---|---|
| `nav.tab-bar` | `AdminShell.svelte` | every tab | the tab bar, a `nav` of anchors |
| `[data-testid="tab-overview"]` | `AdminShell.svelte` | every tab | `href="/ui/overview"` |
| `[data-testid="tab-devices"]` | `AdminShell.svelte` | every tab | `href="/ui/devices"` |
| `[data-testid="tab-fsas"]` | `AdminShell.svelte` | every tab | `href="/ui/fsas"` |
| `[data-testid="tab-control"]` | `AdminShell.svelte` | every tab | `href="/ui/control"` |
| `[data-testid="tab-certificates"]` | `AdminShell.svelte` | every tab | `href="/ui/certificates"` |
| `[data-testid="tab-derms"]` | `AdminShell.svelte` | every tab | `href="/ui/derms"` |
| `[aria-current="page"]` on a tab anchor | `AdminShell.svelte` | every tab | set on the active tab only; absent on all six at a not-found path |
| `[data-testid="not-found"]` | `AdminShell.svelte` | none (any other `/ui/` path) | card naming the unmatched path |
| `[data-testid="not-found-overview-link"]` | `AdminShell.svelte` | none (any other `/ui/` path) | `href="/ui/overview"` |

`/` and `/ui/` render Overview with its tab current and leave the address
bar alone. A tab path with a trailing slash renders that tab and replaces
the URL with the slash-free path.

## Verified

The three existing spec files were run unmodified against the new
dashboard: 12 of 12 passed. That is the empirical form of this table.

Two behaviours changed without changing a selector, and a ported spec
should know about them:

- Expanding or collapsing a topology node no longer refetches
  `GET /api/topology`; collapse state is held in the component.
- The chart is no longer loaded from a public CDN, so `#activityChart`
  contains a `canvas` on a host with no outbound network. The pre-Svelte
  page, still reachable behind `SEP2_ADMIN_LEGACY_DASHBOARD=true`, loads
  no chart library at all and therefore renders no `canvas`.

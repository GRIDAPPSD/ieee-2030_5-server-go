# Admin dashboard selector map

The operator dashboard at `GET /` is now the embedded Svelte admin UI
(`internal/server/web/frontend/`). It replaced a single server-rendered
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

| Selector | Rendered by | Addressed by the suite | Status |
|---|---|---|---|
| `#tlsMode` | `panels/NavBar.svelte` | yes | same id |
| `#uptime` | `panels/NavBar.svelte` | yes | same id |
| `#deviceCount` | `panels/NavBar.svelte` | no | same id |
| `#bigDeviceCount` | `panels/Overview.svelte` | yes | same id |
| `#mupCount` | `panels/Overview.svelte` | yes | same id |
| `#tlsCipher` | `panels/ServerInfo.svelte` | no | same id |
| `#uptimeDetail` | `panels/ServerInfo.svelte` | no | same id |
| `#hwSerial` | `panels/CertPanel.svelte` | yes | same id |
| `#hwType` | `panels/CertPanel.svelte` | no | new: the route rejects a device cert request without the PEN OID |
| `#certResult` | `panels/CertPanel.svelte` | yes | same id |
| `#controlType` | `panels/DerControl.svelte` | yes | same id |
| `#controlValue` | `panels/DerControl.svelte` | no | same id |
| `#controlResult` | `panels/DerControl.svelte` | yes | same id |
| `#addDevCert` | `panels/AddDevice.svelte` | yes | same id |
| `#addDevSFDI` | `panels/AddDevice.svelte` | yes | same id, still readonly |
| `#addDevLFDI` | `panels/AddDevice.svelte` | yes | same id, still readonly |
| `#addDevDesc` | `panels/AddDevice.svelte` | yes | same id |
| `#addDevPIN` | `panels/AddDevice.svelte` | yes | same id |
| `#addDevEnabled` | `panels/AddDevice.svelte` | no | same id |
| `#addDevResult` | `panels/AddDevice.svelte` | yes | same id |
| `#lookupLFDI` | `panels/LookupDevice.svelte` | yes | same id |
| `#lookupResult` | `panels/LookupDevice.svelte` | yes | same id |
| `#newFSADesc` | `panels/CreateFsa.svelte` | yes | same id |
| `#newFSAMRID` | `panels/CreateFsa.svelte` | yes | same id |
| `#newFSAPrimacy` | `panels/CreateFsa.svelte` | yes | same id |
| `#createFSAResult` | `panels/CreateFsa.svelte` | yes | same id |
| `#deviceTable` | `panels/DeviceTable.svelte` | yes | same id, still the `tbody` |
| `#assignSel-{deviceId}` | `panels/DeviceTable.svelte` | no | same id pattern, still keyed by the href's last segment |
| `#topologyTree` | `panels/TopologyTree.svelte` | yes | same id |
| `#attachInp-{mRID}` | `panels/FsaNode.svelte` | yes (built from an mRID at runtime) | same id pattern |
| `#attachResult-{mRID}` | `panels/FsaNode.svelte` | no | same id pattern |
| `#activityChart` | `panels/ActivityChart.svelte` | yes | same id |

24 rows are marked yes, which is the suite's `id` selector count. Of the
8 marked no, 7 are preserved ids the suite does not currently use and one
(`#hwType`) is new.

## Non-`id` selectors

| Selector | Rendered by | Status |
|---|---|---|
| `.navbar h1` | `panels/NavBar.svelte` | survives: still an `h1` inside `.navbar` |
| `th:has-text("SFDI")` | `panels/DeviceTable.svelte` | survives: same `thead` row |
| `th:has-text("LFDI")` | `panels/DeviceTable.svelte` | survives: same `thead` row |
| `button:has-text("Delete")` | `panels/FsaNode.svelte` | survives: still rendered at the system root only |

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

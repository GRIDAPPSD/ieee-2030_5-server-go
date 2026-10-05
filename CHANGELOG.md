# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog 1.1.0](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [0.11.0] - 2026-10-05

This entry covers `v0.10.0..v0.11.0` (6 merged pull requests: five feature and
maintenance changes and this release-preparation change). It is a MINOR
release under the 0.x carve-out: the range adds a streaming panel kind and
typed panel actions to `pkg/sep2admin`, with the plane setting and shutdown
methods that serve them, and the admin UI renders both (`feature`), and the rest is `bug fix`, `chore` and
`test`. No exported identifier is removed or changed, so nothing is `breaking`
under the repository's API policy. The additions, all in `pkg/sep2admin` and
`pkg/sep2adminplane`, are: the new `Stream*` and `Action*` types; the
`Panel.Stream` and `Panel.Actions` fields and the `Config.PanelActions` field;
the constants `ActionChoice`, `ActionInteger`, `ActionBoolean`, `ActionToggle`,
`ActionText`, `StreamMessage`, `StreamStatus`, `MaxActionsPerPanel`,
`MaxActionFields`, `MaxActionLabel`, `MaxActionTextLen`, `MaxActionBodyBytes`,
`MaxActionMessageBytes`, `MaxStreamParamLen` and `MaxStreamEventBytes`; the
sentinel errors `ErrInvalidAction`, `ErrInvalidActionValues`,
`ErrInvalidStream` and `ErrInvalidStreamParam`; the methods `Action.Parse`,
`Action.CheckChoices`, `Action.ListChoices`, `ActionValues.Bool`,
`ActionValues.Int`, `ActionValues.String`, `StreamParam.Validate` and the
`Error` and `Is` methods of the new error types; and `Plane.CloseStreams` and
`Plane.Close`. The
module's `go` directive moves from 1.26.3 to 1.26.8, so building it needs
Go 1.26.8 or newer. Requires `ieee-2030_5-core-go` v0.23.0.

### Added

- **Streaming panels.** A `sep2admin.Panel` can set the new optional `Stream`
  field to serve a live feed over server-sent events beside its `View`.
  `Stream`, `StreamParam`, `StreamRequest`, `StreamEvent`, `StreamEventKind`,
  `StreamOpenFunc` and `StreamSendFunc` are new, and `Register` refuses a
  malformed `Stream`. Opens are bounded, the final status event is marked so
  clients stop reconnecting, and refused cross-origin streams are logged.
  `Plane.CloseStreams` ends every open stream with a final status and refuses
  new ones; call it before the serving `http.Server`'s `Shutdown`. The server
  mounts `GET /api/ui/panels/{id}/stream` unconditionally, so the boot route
  list gains one route, and registers `CloseStreams` for shutdown.
  ([#866](https://github.com/GRIDAPPSD/ieee-2030_5-server-go/pull/866))
- **Typed panel actions.** A `sep2admin.Panel` can set the new optional
  `Actions` field, typed forms the shell offers beside `View`. `Action`,
  `ActionField`, `ActionFieldKind`, `ActionResult`, `ActionRefusal`,
  `ActionValueError` and `ActionValues` are new, and `Register` refuses
  malformed actions. They are served under `/api/ui/panels/{id}/actions` only
  when the new `sep2adminplane.Config.PanelActions` is set; off, those routes
  answer 404. A running action is counted from admission. Every action that
  reaches the plane is audited, and a refused POST of a known action is
  logged; an unknown panel or action (404) and the action-list refusals (429
  and 503) are not logged. `CloseStreams` already tells running actions to
  stop and refuses new ones with 503; `Plane.Close(wait)` calls it and adds
  only a bounded wait of up to `wait` and a return value, the number of
  actions still running at the bound. Both are new methods, so no existing
  signature changes.
  ([#868](https://github.com/GRIDAPPSD/ieee-2030_5-server-go/pull/868))
- **Admin UI renders streaming panels.** The shell shows a stream panel's live
  feed and its state, and ends it on the final status event.
  ([#870](https://github.com/GRIDAPPSD/ieee-2030_5-server-go/pull/870))
- **Admin UI renders panel actions.** The shell shows a panel's typed actions
  as forms and runs them; the action toggle state, reload and timeout are
  handled.
  ([#872](https://github.com/GRIDAPPSD/ieee-2030_5-server-go/pull/872))

### Changed

- Moved to Go 1.26.8, `ieee-2030_5-core-go` v0.23.0 and current `golang.org/x`
  modules, clearing the standard-library and `x/crypto`, `x/net` advisories
  govulncheck reported at the old pins. The `Dockerfile` builder image and the
  CI note move with it.
  ([#871](https://github.com/GRIDAPPSD/ieee-2030_5-server-go/pull/871))
- README: latest-release and core-pin statements corrected to v0.11.0 and
  v0.23.0.

## [0.10.0] - 2026-10-03

This entry covers `v0.9.0..v0.10.0` (5 merged pull requests: four feature and
maintenance changes and this release-preparation change). It is a MINOR
release under the 0.x carve-out: the range adds a configuration surface and an
exported option (`feature`), and the rest is `chore` and `documentation`. No
exported identifier is removed or changed, and no request or response changes
shape, so nothing is `breaking`. Requires `ieee-2030_5-core-go` v0.22.0.

### Added

- **Configurable notification timeouts.** `subscription.WithNotificationTimeouts`
  sets the POST, dial and creation-resolve durations of a `Manager`; zero keeps
  the 30 s, 30 s and 5 s defaults, and `NotificationTimeouts.Validate` refuses
  a negative field by name; the dial budget is capped at the POST timeout.
  The server binary reads
  `SEP2_NOTIFICATION_POST_TIMEOUT`, `SEP2_NOTIFICATION_DIAL_TIMEOUT` and
  `SEP2_NOTIFICATION_RESOLVE_TIMEOUT` as Go durations; an unparseable or
  non-positive value keeps the default and logs a warning naming the variable.
  ([#856](https://github.com/GRIDAPPSD/ieee-2030_5-server-go/pull/856))

### Changed

- Moved to `ieee-2030_5-core-go` v0.22.0, which adds a configurable CCM
  handshake timeout.
  ([#860](https://github.com/GRIDAPPSD/ieee-2030_5-server-go/pull/860))
- `.gitignore` ignores only the repository-root `certs/` folder, so
  `internal/certs/` and nested `certs` folders are no longer hidden from
  `git status`.
  ([#852](https://github.com/GRIDAPPSD/ieee-2030_5-server-go/pull/852))
- README: removed the `SEP2_USE_CORE_ROUTER` toggle text, the `pkg/sep2` line,
  the Interop section and the `make run-ccm` line; documented the notification
  timeouts; corrected the release and core version statements.
  ([#854](https://github.com/GRIDAPPSD/ieee-2030_5-server-go/pull/854))

## [0.9.0] - 2026-10-01

This entry covers `v0.8.0..v0.9.0` (2 merged pull requests). It is a MINOR
release under the 0.x carve-out: the range adds an optional panel picker
contract, a route and the shell control for it (`feature`), and one request
that v0.8.0 answered 200 is now refused with 400 (`breaking`). Requires
`ieee-2030_5-core-go` v0.21.0.

### Added

- **Panel picker.** A `sep2admin.Panel` can set the new optional `Picker`
  field, a pair of functions `Choices` and `Select`, to offer a closed list of
  choices from which the shell asks it to show a subset. `Register` refuses a
  picker with either function nil (`ErrInvalidPicker`). `GET /api/ui/panels`
  adds `"picker":{"max":16}` to a panel that has one, and the new
  `GET /api/ui/panels/{id}/choices` returns `{"max":16,"choices":[{id,label}]}`.
  `GET /api/ui/panels/{id}?sel=a&sel=b` renders the panel for the selection:
  only IDs that match the panel's own choices reach `Select`, a well-formed
  unknown ID is dropped, and when every ID is dropped `View` answers. A
  selection holds at most 16 IDs, each matching `^[A-Za-z0-9_.:-]{1,64}$`
  with no duplicate. `Choices` accepts at most 256 entries with labels of 1 to
  128 characters; a duplicate choice ID or label is refused. `Choices` and
  `Select` run under the same one-at-a-time flag and timeout as `View`. A panel
  with no `Picker` keeps its wire shape.
  ([#848](https://github.com/GRIDAPPSD/ieee-2030_5-server-go/pull/848))
- **Picker control in the admin shell.** A panel that declares a picker shows
  a search box and checkboxes, with Clear, Select all and Apply. The applied
  selection is kept in the browser's local storage per panel, not in the URL.
  A saved ID that has left the choices is not sent, and the panel says how
  many saved choices are no longer available. If a refreshed selection is
  refused with 400, the last good view stays on screen with a message.
  ([#849](https://github.com/GRIDAPPSD/ieee-2030_5-server-go/pull/849))

### Changed

- **Breaking.** A request to `GET /api/ui/panels/{id}` that carries a
  non-empty query string is refused with 400 and the body
  `{"error":"invalid selection"}` when the panel has no `Picker`, where v0.8.0
  ignored the query and answered 200. On a panel that has a picker, a query
  with a key other than `sel`, a query over 2048 bytes, an ID outside the
  pattern, a duplicate ID or more than 16 IDs is refused the same way. A
  request with no query is unchanged. One case, read from the code and not
  exercised: a panel read authenticated by a one-time `?ticket=` parameter has
  its ticket consumed and is then answered 400. The bridge sends no query and
  is not affected. To adapt, send no query string to a panel that has no
  picker, and send only `sel` to one that has. Authenticate panel reads with
  the admin client certificate, the `Authorization: Bearer` header or the
  session cookie, not `?ticket=`; the shell uses a ticket only for
  `/dashboard/events`.
  ([#848](https://github.com/GRIDAPPSD/ieee-2030_5-server-go/pull/848))

## [0.8.0] - 2026-10-01

This entry covers `v0.7.0..v0.8.0` (63 merged pull requests). It is a MINOR
release under the 0.x carve-out: the range adds new routes, packages and
admin UI panes (`feature`) and changes several exported signatures
(`breaking`), neither of which can be a PATCH. Requires
`ieee-2030_5-core-go` v0.21.0.

### Added

- **Flow reservation as a DERMS workflow.** A flow reservation request is held
  for the operator's answer or a deadline fallback, answered, revised
  (cancel-and-create) or cancelled through new admin routes, and cancelled by
  the requesting client. Response list subscribers are notified, the list
  advertises a short `pollRate` while a request is pending, ended reservations
  are removed after a grace period, and pending requests and interrupted writes
  are recovered at startup. Requests, responses and cancel marks persist, and
  pending requests are recovered, only when `SEP2_DATA_DIR` is set; the default
  is in-memory and loses them on restart.
  ([#736](https://github.com/GRIDAPPSD/ieee-2030_5-server-go/pull/736),
  [#755](https://github.com/GRIDAPPSD/ieee-2030_5-server-go/pull/755),
  [#768](https://github.com/GRIDAPPSD/ieee-2030_5-server-go/pull/768),
  [#770](https://github.com/GRIDAPPSD/ieee-2030_5-server-go/pull/770),
  [#778](https://github.com/GRIDAPPSD/ieee-2030_5-server-go/pull/778),
  [#781](https://github.com/GRIDAPPSD/ieee-2030_5-server-go/pull/781),
  [#782](https://github.com/GRIDAPPSD/ieee-2030_5-server-go/pull/782),
  [#789](https://github.com/GRIDAPPSD/ieee-2030_5-server-go/pull/789),
  [#796](https://github.com/GRIDAPPSD/ieee-2030_5-server-go/pull/796),
  [#811](https://github.com/GRIDAPPSD/ieee-2030_5-server-go/pull/811))
- **Commitment ledger.** Fleet windows, grant fit rules and a ledger that
  serializes check-then-write per fleet and reads grants and controls from
  their own stores each time; it keeps no copy of any commitment. Every admin
  DER control create, and every flow reservation grant whose interval has a
  positive duration, is checked against it. A grant can be cancelled or
  revised through it, and a read route serves each fleet's current
  commitments.
  ([#741](https://github.com/GRIDAPPSD/ieee-2030_5-server-go/pull/741),
  [#747](https://github.com/GRIDAPPSD/ieee-2030_5-server-go/pull/747),
  [#749](https://github.com/GRIDAPPSD/ieee-2030_5-server-go/pull/749),
  [#750](https://github.com/GRIDAPPSD/ieee-2030_5-server-go/pull/750),
  [#753](https://github.com/GRIDAPPSD/ieee-2030_5-server-go/pull/753),
  [#809](https://github.com/GRIDAPPSD/ieee-2030_5-server-go/pull/809))
- **DER controls.** DERControls and their lifecycle records persist;
  `EventStatus` and `DERControlListLink.all` are derived at serve time; a
  control links to a grant and carries a target power; admin routes create,
  list and cancel controls; and each admin-issued control reports metered
  delivery.
  ([#725](https://github.com/GRIDAPPSD/ieee-2030_5-server-go/pull/725),
  [#726](https://github.com/GRIDAPPSD/ieee-2030_5-server-go/pull/726),
  [#742](https://github.com/GRIDAPPSD/ieee-2030_5-server-go/pull/742),
  [#745](https://github.com/GRIDAPPSD/ieee-2030_5-server-go/pull/745),
  [#808](https://github.com/GRIDAPPSD/ieee-2030_5-server-go/pull/808))
- **Fleet read API.** Admin routes for aggregator status and measurements,
  now carrying each fleet device's EndDevice id and href.
  ([#719](https://github.com/GRIDAPPSD/ieee-2030_5-server-go/pull/719),
  [#795](https://github.com/GRIDAPPSD/ieee-2030_5-server-go/pull/795))
- **Admin UI.** The DERMS tab gains a fleet pane, a request queue pane with
  answer, revise and cancel actions, and a dispatch pane that executes a live
  grant or sends a plain dispatch and shows metered delivery; the Send DER
  Control card is wired to the admin API; the fleet, request queue and dispatch
  panes show `Source:` labels and show each fleet's commitments; entering the Devices or FSAs tab reloads
  the FSA list and topology; and a chart section renders in panels. The admin UI type check now
  runs in CI.
  ([#730](https://github.com/GRIDAPPSD/ieee-2030_5-server-go/pull/730),
  [#748](https://github.com/GRIDAPPSD/ieee-2030_5-server-go/pull/748),
  [#765](https://github.com/GRIDAPPSD/ieee-2030_5-server-go/pull/765),
  [#769](https://github.com/GRIDAPPSD/ieee-2030_5-server-go/pull/769),
  [#790](https://github.com/GRIDAPPSD/ieee-2030_5-server-go/pull/790),
  [#792](https://github.com/GRIDAPPSD/ieee-2030_5-server-go/pull/792),
  [#807](https://github.com/GRIDAPPSD/ieee-2030_5-server-go/pull/807),
  [#812](https://github.com/GRIDAPPSD/ieee-2030_5-server-go/pull/812),
  [#814](https://github.com/GRIDAPPSD/ieee-2030_5-server-go/pull/814),
  [#816](https://github.com/GRIDAPPSD/ieee-2030_5-server-go/pull/816),
  [#838](https://github.com/GRIDAPPSD/ieee-2030_5-server-go/pull/838))
- **Embeddable admin plane.** New public package `pkg/sep2adminplane`: a
  facade over the admin plane with a loopback bypass switch, a `ReadOnly`
  mode that mounts no admin write route except `POST /auth/login` and
  `POST /auth/ticket`, and an exported `SettingsFromEnv` with its `Settings`
  type. An embedder can register tabs that the shell renders.
  ([#835](https://github.com/GRIDAPPSD/ieee-2030_5-server-go/pull/835),
  [#837](https://github.com/GRIDAPPSD/ieee-2030_5-server-go/pull/837),
  [#840](https://github.com/GRIDAPPSD/ieee-2030_5-server-go/pull/840),
  [#843](https://github.com/GRIDAPPSD/ieee-2030_5-server-go/pull/843))
- A generic persistent scoped store in `pkg/store/memory`, and a retention
  bound on mirror meter readings.
  ([#771](https://github.com/GRIDAPPSD/ieee-2030_5-server-go/pull/771),
  [#823](https://github.com/GRIDAPPSD/ieee-2030_5-server-go/pull/823))

### Changed

- **Breaking.** `subscription.HandleCreateSubscription` takes a
  `subscription.ReadCheck` as its third argument; nil refuses every create,
  and `BuildProtocolRouter` passes its own. Only `/edev` resources can now be
  subscribed to: `/mup`, `/tm`, `/dcap` and every other non-`/edev` route are
  refused. An embedder that calls the handler must update its call.
  ([#830](https://github.com/GRIDAPPSD/ieee-2030_5-server-go/pull/830))
- **Breaking.** `flow_reservation.HandlePostResponse` takes a
  `ResponseSenderAuthorizer`. A posted DERControlResponse with no
  `endDeviceLFDI`, or a malformed one, is now refused with 400, and a Response
  whose `endDeviceLFDI` names neither the sender nor a device the sender
  currently manages is refused with 403, where v0.7.0 accepted both. Other Response types that name no device are still stored.
  ([#742](https://github.com/GRIDAPPSD/ieee-2030_5-server-go/pull/742))
- **Breaking.** The five mirror handlers `HandleCreateMirrorUsagePoint`,
  `HandleMirrorUsagePoint`, `HandlePutMirrorUsagePoint`,
  `HandleDeleteMirrorUsagePoint` and `HandlePostMirrorMeterReading` take an
  `EndDeviceManagementReader`. An embedder that calls them must update its
  calls.
  ([#722](https://github.com/GRIDAPPSD/ieee-2030_5-server-go/pull/722))
- **Breaking.** `NewFlowReservationLinkedEndDeviceStore` and
  `NewLogEventLinkedEndDeviceStore` take additional stores.
  ([#718](https://github.com/GRIDAPPSD/ieee-2030_5-server-go/pull/718))
- **Breaking.** The `flow_reservation.FRPCreator` interface is removed, and
  `HandlePostFlowReservationRequest` takes a `Submitter` in place of the store.
  ([#736](https://github.com/GRIDAPPSD/ieee-2030_5-server-go/pull/736))
- **Breaking.** The admin UI descriptor in `pkg/sep2admin` is version 2:
  `CurrentDescriptorVersion` is 2, `Row` is `[]Cell` (was `[]Value`),
  `Descriptor.Body` became `Descriptor.Sections`, `DefinitionEntry.Value` is
  now a `Cell`, and `ErrUnhandledBodyKind` now names `Section.Body`. A consumer
  that builds or reads descriptors must update.
  ([#835](https://github.com/GRIDAPPSD/ieee-2030_5-server-go/pull/835))
- The admin plane moves into `internal/adminplane`; embedders use the new
  `pkg/sep2adminplane` facade.
  ([#834](https://github.com/GRIDAPPSD/ieee-2030_5-server-go/pull/834))
- Adopts `ieee-2030_5-core-go` v0.21.0.
  ([#832](https://github.com/GRIDAPPSD/ieee-2030_5-server-go/pull/832))
- Change notifications carry status 0 where v0.7.0 carried 2.
  ([#780](https://github.com/GRIDAPPSD/ieee-2030_5-server-go/pull/780))

### Fixed

- Flow reservation: every server-built `FlowReservationResponse` gets a minted
  mRID; a request with a blank mRID is refused with 400 and one with an
  invalid `RequestStatus` is refused, where v0.7.0 accepted both; an answer-record conflict is told apart
  from an existing response; `potentiallySuperseded` is edition-aware and
  status times are ordered; a refused relink rolls back cleanly and a stored
  revision is named on retry; and a cancel-log line names the control when a
  DER cancel runs unlocked.
  ([#717](https://github.com/GRIDAPPSD/ieee-2030_5-server-go/pull/717),
  [#723](https://github.com/GRIDAPPSD/ieee-2030_5-server-go/pull/723),
  [#759](https://github.com/GRIDAPPSD/ieee-2030_5-server-go/pull/759),
  [#767](https://github.com/GRIDAPPSD/ieee-2030_5-server-go/pull/767),
  [#819](https://github.com/GRIDAPPSD/ieee-2030_5-server-go/pull/819),
  [#822](https://github.com/GRIDAPPSD/ieee-2030_5-server-go/pull/822))
- Deleting an EndDevice now removes its flow reservation records, log events,
  configuration, device status, power status, FSA and FSA link records and leftover
  registrations. Its DER records and its subscriptions are still left behind;
  that is not fixed in this release and is tracked in
  [#721](https://github.com/GRIDAPPSD/ieee-2030_5-server-go/issues/721).
  ([#718](https://github.com/GRIDAPPSD/ieee-2030_5-server-go/pull/718),
  [#724](https://github.com/GRIDAPPSD/ieee-2030_5-server-go/pull/724))
- An aggregator's mirror usage point is attributed to the managed device, and
  seeded DER programs get the runtime href shape.
  ([#722](https://github.com/GRIDAPPSD/ieee-2030_5-server-go/pull/722),
  [#746](https://github.com/GRIDAPPSD/ieee-2030_5-server-go/pull/746))
- Fleet reads fail with 500 when a store read fails, flag a sum whose
  direction is unknown, and map Net `flowDirection` for 2023 DER readings; the
  dashboard device list reports store errors and every device.
  ([#758](https://github.com/GRIDAPPSD/ieee-2030_5-server-go/pull/758),
  [#774](https://github.com/GRIDAPPSD/ieee-2030_5-server-go/pull/774),
  [#787](https://github.com/GRIDAPPSD/ieee-2030_5-server-go/pull/787),
  [#818](https://github.com/GRIDAPPSD/ieee-2030_5-server-go/pull/818))
- Admin UI: the fleet pane bounds its fetch, the queue pane shows every
  failure and the cancel-requested flag, and delivery shows small values and
  open windows truthfully.
  ([#757](https://github.com/GRIDAPPSD/ieee-2030_5-server-go/pull/757),
  [#786](https://github.com/GRIDAPPSD/ieee-2030_5-server-go/pull/786),
  [#817](https://github.com/GRIDAPPSD/ieee-2030_5-server-go/pull/817))
- Two flaky tests are made deterministic, and a test now fails when a
  sensitive admin write is listed as non-sensitive. No runtime change.
  ([#788](https://github.com/GRIDAPPSD/ieee-2030_5-server-go/pull/788),
  [#820](https://github.com/GRIDAPPSD/ieee-2030_5-server-go/pull/820))

### Security

- A subscription's `subscribedResource` is now checked against the same read
  decision the GET routes use. On create, a resource the caller could not GET
  is refused with 400; at delivery, each stored subscription is re-checked, so
  a subscriber that can no longer read the resource is not notified. Only
  `/edev` resources can be subscribed to. Versions 0.7.0 and earlier are
  affected; 0.8.0 fixes it. The subscription route is mounted whenever the subscription
  store is present, which is the default store set, so no non-default
  configuration is needed to be affected. See GHSA-95gm-c948-4xxm
  (https://github.com/GRIDAPPSD/ieee-2030_5-server-go/security/advisories/GHSA-95gm-c948-4xxm).
  ([#830](https://github.com/GRIDAPPSD/ieee-2030_5-server-go/pull/830))

## [0.7.0] - 2026-09-25

This entry covers `v0.6.0..v0.7.0` (3 merged pull requests).

### Changed

- **Breaking.** Adopts `ieee-2030_5-core-go` v0.20.0's CCM-8-only `sep2tls`.
  The protocol listener now negotiates only
  `TLS_ECDHE_ECDSA_WITH_AES_128_CCM_8`; the GCM cipher-suite fallback is gone.
  A client, monitoring probe, or test harness that only offered GCM can no
  longer complete a TLS handshake against the protocol listener; a client
  offering CCM-8 (the standard's mandatory suite) now connects under this
  server's default configuration, having already connected when
  `SEP2_CCM=true` was set. `make run-ccm` is now an alias for `make run`,
  since the two modes
  became identical. The Python client and the stress load generator's `-ccm`
  flag cannot yet reach a CCM-8-only server; not fixed in this release.
  ([#709](https://github.com/GRIDAPPSD/ieee-2030_5-server-go/pull/709),
  [#707](https://github.com/GRIDAPPSD/ieee-2030_5-server-go/issues/707),
  [#708](https://github.com/GRIDAPPSD/ieee-2030_5-server-go/issues/708))
- `pkg/adminui/web` (formerly `internal/server/web`) is now a public package:
  the embedded FS is unexported in favor of an `Assets()` accessor. No
  behavior change; no known consumer currently imports it.
  ([#705](https://github.com/GRIDAPPSD/ieee-2030_5-server-go/pull/705))

### Removed

- **Breaking.** The `SEP2_CCM` environment variable and the `EnableCCM`
  configuration field (`pkg/sep2server.Config`, `pkg/sep2srv.Options`,
  `internal/config.Config`) are removed, not deprecated. Setting `SEP2_CCM`
  in the environment now has no effect.
  ([#709](https://github.com/GRIDAPPSD/ieee-2030_5-server-go/pull/709))

### Fixed

- Two false README claims are corrected (development on GitLab; core
  consumed via a local-path `replace` directive), and the dead
  `.gitlab-ci.yml` is removed. No runtime change.
  ([#706](https://github.com/GRIDAPPSD/ieee-2030_5-server-go/pull/706))

## [0.6.0] - 2026-09-24

This entry covers `v0.5.0..v0.6.0` (12 merged pull requests; two,
[#574](https://github.com/GRIDAPPSD/ieee-2030_5-server-go/pull/574) (an
intermediate core hop folded into the #690 citation) and
[#697](https://github.com/GRIDAPPSD/ieee-2030_5-server-go/pull/697) (a
regression test, no server-go runtime change), are not cited separately
below). `CHANGELOG.md` carries no entries for `v0.4.0` or `v0.5.0`. See the
published GitHub Releases for
[v0.4.0](https://github.com/GRIDAPPSD/ieee-2030_5-server-go/releases/tag/v0.4.0)
and
[v0.5.0](https://github.com/GRIDAPPSD/ieee-2030_5-server-go/releases/tag/v0.5.0),
each with a per-commit table. Two breaking changes live only there: the
default certificate directory moved outside the working tree (v0.4.0), and
admin write routes began requiring a real credential, closing a loopback
bypass (v0.5.0). A reader upgrading from `v0.3.0` on this file alone should
read both releases first.

### Added

- `FlowReservationRequestListLink` and `FlowReservationResponseListLink` are
  now advertised on every served `EndDevice`, mounted at `/edev/{id}/frq` and
  `/edev/{id}/frp`. A client that discovers resources by following links can
  now reach the flow reservation lists; a client that hardcodes the address
  needed no change.
  ([#699](https://github.com/GRIDAPPSD/ieee-2030_5-server-go/pull/699),
  [#693](https://github.com/GRIDAPPSD/ieee-2030_5-server-go/issues/693))
- CI now fails when the vendored tree differs from the pinned modules.
  CI-only gate; no runtime change.
  ([#694](https://github.com/GRIDAPPSD/ieee-2030_5-server-go/pull/694))
- The admin plane can provision EndDevice management pairs through new
  `EndDeviceManagementStore.RekeyManager` / `RekeyManaged` methods on
  `*memory.EndDeviceManagementStore`. Purely additive; no existing signature
  changed.
  ([#677](https://github.com/GRIDAPPSD/ieee-2030_5-server-go/pull/677),
  [#440](https://github.com/GRIDAPPSD/ieee-2030_5-server-go/issues/440))
- Admin UI: renders a descriptor's table and definition-list bodies.
  Frontend-only; no Go API surface.
  ([#610](https://github.com/GRIDAPPSD/ieee-2030_5-server-go/pull/610))
- `Config.ServingCAFile` and `Config.DeviceCAFile` separate the serving CA
  from the device CA, each falling back to the legacy `CAFile` when unset. A
  deployment that never sets the new variables behaves exactly as before.
  ([#638](https://github.com/GRIDAPPSD/ieee-2030_5-server-go/pull/638),
  [#622](https://github.com/GRIDAPPSD/ieee-2030_5-server-go/issues/622))

### Changed

- **Breaking.** `ieee-2030_5-core-go` bumped `v0.17.0` -> `v0.19.0`. This
  range retypes `RequestStatus`. It was an optional pointer to a bare
  integer; it is now a mandatory struct of `dateTime` and `requestStatus`,
  the complex type the schema declares. `DERAvailability` gains
  `reserveChargePercent` and `reservePercent`, which round-trip unchanged
  through this server's DER handler, and loses `omitempty` on
  `readingTime`, which is now always emitted, including as a literal zero;
  no test in this repository observes that field. A consumer that reads or
  constructs `RequestStatus` values, directly or through server-go's
  exported types that embed it, must rebuild against core v0.19.0 and
  re-check that code. A consumer that reads `DERAvailability.readingTime`
  should re-check it against the new always-emitted zero; any other
  consumer needs no change.
  ([#690](https://github.com/GRIDAPPSD/ieee-2030_5-server-go/pull/690))

### Fixed

- **Breaking.** The manager write-delegation rule is replaced with an
  explicit allow-list: only the four DER PUT sub-resources and the LogEvent
  POST are now delegated to a manager on a device it manages. Every other
  write below `/edev/{id}` that the previous wildcard rule permitted is now
  refused. An aggregator or manager client that wrote to a sub-resource
  outside that list must make that write with the device owner's own
  credential instead; it now gets refused where it previously succeeded
  with the manager's.
  ([#679](https://github.com/GRIDAPPSD/ieee-2030_5-server-go/pull/679),
  [#510](https://github.com/GRIDAPPSD/ieee-2030_5-server-go/issues/510))

### Security

- **Breaking.** The admin listener's client trust anchor now defaults to the
  serving CA rather than the host root store. A deployment running
  `AdminTLS` with an operator certificate signed by a public CA, and no
  explicit setting, previously verified against the host root store; it now
  verifies against the serving CA and fails the handshake unless
  `SEP2_ADMIN_CLIENT_CA=system` is set explicitly.
  ([#657](https://github.com/GRIDAPPSD/ieee-2030_5-server-go/pull/657),
  [#624](https://github.com/GRIDAPPSD/ieee-2030_5-server-go/issues/624),
  [#418](https://github.com/GRIDAPPSD/ieee-2030_5-server-go/issues/418))
- `GET /api/certs/ca` previously echoed the stored CA file's raw bytes; a
  combined-PEM layout leaked the CA private key. It now serves only
  certificate DER the server re-encodes itself.
  ([#652](https://github.com/GRIDAPPSD/ieee-2030_5-server-go/pull/652),
  [#644](https://github.com/GRIDAPPSD/ieee-2030_5-server-go/issues/644))
- A one-time admin ticket could mint its own successor, making a single
  leaked ticket an unbounded credential. Now refused.
  ([#653](https://github.com/GRIDAPPSD/ieee-2030_5-server-go/pull/653),
  [#641](https://github.com/GRIDAPPSD/ieee-2030_5-server-go/issues/641))

## [0.3.0] - 2026-09-17

### Added

- `assembly.Stores.EndDeviceManagers` (`store.EndDeviceManagementStore`), with the in-memory
  `memory.EndDeviceManagementStore`: provisioned (manager, managed) LFDI pairs through which an
  aggregator reaches the EndDevices it manages. The server binary and `sep2server.NewStores` wire an
  empty store; admin-plane provisioning and persistence are tracked in
  [#440](https://github.com/GRIDAPPSD/ieee-2030_5-server-go/issues/440). See
  [docs/enddevice-access.md](docs/enddevice-access.md).
  ([#354](https://github.com/GRIDAPPSD/ieee-2030_5-server-go/issues/354))
- `store.ErrInvalidManagementPair`, returned when a management pair has an empty or non-canonical
  LFDI or names a device as its own manager.
  ([#354](https://github.com/GRIDAPPSD/ieee-2030_5-server-go/issues/354))
- A log line for ownership refusals on `/edev/{id}` (route, caller LFDI, requested id, reason class),
  budgeted at 5 lines per caller and 100 total per one-minute window, shared by all callers. A window
  that suppressed at least one line reports the suppressed count, by reason, when it closes.
  ([#354](https://github.com/GRIDAPPSD/ieee-2030_5-server-go/issues/354),
  [#447](https://github.com/GRIDAPPSD/ieee-2030_5-server-go/issues/447))
- `internal/dercontrol`, the server-side DER control issuer: turns an operator's DER control request
  into a `DERControl` event and keeps its lifecycle record (issue, supersede, cancel). No HTTP route
  yet; the route, served status, and persistence are tracked in
  [#419](https://github.com/GRIDAPPSD/ieee-2030_5-server-go/issues/419).
  ([#563](https://github.com/GRIDAPPSD/ieee-2030_5-server-go/issues/563))
- The admin UI frontend toolchain: a Svelte source tree, a built bundle embedded via `go:embed`, an
  SPA handler at `GET /ui/`, and a stale-bundle gate (`make ui-check`).
  ([#363](https://github.com/GRIDAPPSD/ieee-2030_5-server-go/issues/363))
- `CSIP_SUNSPEC_REQUIRED`: demands the SunSpec CSIP fixture material and fails the run instead of
  skipping it when unset.
  ([#348](https://github.com/GRIDAPPSD/ieee-2030_5-server-go/issues/348))
- `DERCurveSpec.creation_time` (optional): a curve whose spec sets it is served with exactly that
  value; a curve that leaves it out is served with the fixture's load time, never `0`.
  ([#539](https://github.com/GRIDAPPSD/ieee-2030_5-server-go/issues/539),
  [#555](https://github.com/GRIDAPPSD/ieee-2030_5-server-go/issues/555))
- `make ci-local` and `make ci-local-drift-check`: run the same gates `ci.yml` runs, locally, and fail
  if a CI gate has no local counterpart.
  ([#410](https://github.com/GRIDAPPSD/ieee-2030_5-server-go/issues/410))

### Changed

- **Breaking for embedders and clients.** Every route under `/edev/{id}` now answers only the device
  whose certificate LFDI is stored on the EndDevice, or its provisioned manager. Other callers get
  `403` (not owner or manager, no identity, or a record with no LFDI) or `404` (no such EndDevice, for a
  caller with an identity);
  a store failure, or no EndDevice store wired, is `500`. A manager may `GET`/`HEAD` the record and use
  every route below it except the Registration; it may not `PUT` or `DELETE` the record. `GET /edev`
  lists only the caller's own and managed EndDevices, with `all` counting that set and `results` the page.
  Deployments that relied on any certificate reaching any EndDevice, or on `GET /edev` listing the
  fleet, must change.
  ([#354](https://github.com/GRIDAPPSD/ieee-2030_5-server-go/issues/354))
- `BuildProtocolRouter` logs when it substitutes a deny-all stub for a nil `AuthPolicy.Identity`.
  ([#354](https://github.com/GRIDAPPSD/ieee-2030_5-server-go/issues/354))
- `subscription.HandleCreateSubscription` takes a notificationURI validator as
  its second argument: pass `(*subscription.Manager).ValidateNotificationURI`,
  or nil for the default policy. `subscription.NewManager` accepts
  `ManagerOption`s, including `WithDestinationPolicy`.
  ([#427](https://github.com/GRIDAPPSD/ieee-2030_5-server-go/issues/427))
- **Breaking.** The admin credential model is split: `?ticket=` stays strictly single-use, while a new
  `admin_ticket` cookie is validated without being consumed, on a sliding idle timeout with a
  non-extendable absolute cap. An integration that assumed the cookie was consumed per request, or
  reused a query ticket across requests, must move to the cookie.
  ([#365](https://github.com/GRIDAPPSD/ieee-2030_5-server-go/issues/365))
- The operator dashboard's nine working capabilities are rewritten as Svelte panels served at
  `GET /ui/`; the previous single-string-constant page stays available behind a rollback flag.
  **Corrected during verification: not breaking.** `GET /` is unchanged and the prior placeholder
  page remains reachable behind the rollback flag; no protocol client or wire contract is affected.
  ([#364](https://github.com/GRIDAPPSD/ieee-2030_5-server-go/issues/364))
- The admin dashboard's shared state (SSE connection, activity history, FSA list, topology tree) moved
  into a shell component rendered once for every admin UI path, so a client-side route change no
  longer closes the event stream or drops the activity history.
  ([#560](https://github.com/GRIDAPPSD/ieee-2030_5-server-go/issues/560))
- An unauthenticated top-level browser navigation now gets a `303` redirect to `/login` instead of a
  JSON `401`; every other unauthenticated request keeps the JSON `401`.
  ([#402](https://github.com/GRIDAPPSD/ieee-2030_5-server-go/issues/402))
- **Breaking.** `ieee-2030_5-core-go` bumped `v0.15.0` -> `v0.16.0`. `opModFixedW` and `opModMaxLimW`
  on DER control resources are now hundredths-of-percent (`SignedPerCent`/`PerCent`) rather than the
  previous multiplier-and-value shape; a fixture still in the old shape fails to load, and an
  out-of-range percent value is refused at load.
  ([#535](https://github.com/GRIDAPPSD/ieee-2030_5-server-go/issues/535))
- **Breaking.** `ieee-2030_5-core-go` bumped `v0.16.0` -> `v0.17.0`. `curve_type` in the committed
  BASIC-004, BASIC-005, BASIC-006, BASIC-011, BASIC-012 and BASIC-015 CSIP fixtures is renumbered to
  core's mode-based numbering (table in
  [GRIDAPPSD/ieee-2030_5-core-go#162](https://github.com/GRIDAPPSD/ieee-2030_5-core-go/issues/162)).
  An operator with a custom boot fixture file must renumber it the same way, and only once every
  client reading this server has moved to core v0.17.0 semantics.
  ([#539](https://github.com/GRIDAPPSD/ieee-2030_5-server-go/issues/539),
  [#555](https://github.com/GRIDAPPSD/ieee-2030_5-server-go/issues/555))

### Deprecated

### Removed

- **Breaking.** `PUT` on the `DefaultDERControl` route
  (`/edev/{id}/fsa/{fsaId}/derp/{derpId}/dderc`) is no longer mounted on the protocol router; the
  handler also refuses `PUT` itself with `405 Allow: GET, HEAD`. Default controls are set only by the
  server (boot fixture today).
  ([#456](https://github.com/GRIDAPPSD/ieee-2030_5-server-go/issues/456))

### Fixed

- The subscription notifier returns an error instead of panicking when a notification is attempted
  after the notifier has shut down.
  ([#460](https://github.com/GRIDAPPSD/ieee-2030_5-server-go/issues/460))
- Boot fixture records are seeded once against persisted state, rather than being re-seeded (and
  potentially duplicated or reset) on every restart.
  ([#352](https://github.com/GRIDAPPSD/ieee-2030_5-server-go/issues/352))
- The EndDevice ownership gate now refuses (`500`) when the EndDevices store is absent or mis-wired,
  instead of behaving as if every device were absent.
  ([#359](https://github.com/GRIDAPPSD/ieee-2030_5-server-go/issues/359))
- `DERProgramStore.Update` now persists to disk, matching the read path; previously an update was
  silently lost on restart.
  ([#495](https://github.com/GRIDAPPSD/ieee-2030_5-server-go/issues/495))
- Protocol and admin-plane `4xx` responses no longer echo raw decoder/parse error text to the client;
  the client gets a fixed message and the detail is logged with the route pattern.
  ([#360](https://github.com/GRIDAPPSD/ieee-2030_5-server-go/issues/360))

### Security

- Routes under `/edev/{id}` no longer let a device holding a CA-signed client certificate reach
  another device's EndDevice or the resources under it, and `GET /edev` no longer discloses every
  device's LFDI and SFDI. ([#354](https://github.com/GRIDAPPSD/ieee-2030_5-server-go/issues/354))
  Two cross-device paths that were open at the time of that change are now both closed, in this same
  release: `DELETE /edev/{id}/sub/{subId}` is scoped to the EndDevice named in the path (see Fixed,
  [#435](https://github.com/GRIDAPPSD/ieee-2030_5-server-go/issues/435)), and `POST /edev` refuses
  with `409` instead of returning another device's record on an index or SFDI collision (see Fixed,
  [#443](https://github.com/GRIDAPPSD/ieee-2030_5-server-go/issues/443)).
- **Breaking, corrected during verification (was unmarked).** `DELETE /edev/{id}/sub/{subId}` now
  compares the subscription's stored `href` against the path, so a `subId` belonging to a different
  EndDevice returns `404` and the subscription is left in place, instead of being deleted with a
  `204`.
  ([#435](https://github.com/GRIDAPPSD/ieee-2030_5-server-go/issues/435))
- **Breaking, corrected during verification (was unmarked).** `POST /edev` now answers `409 Conflict`,
  instead of returning the existing record, when the allocated index or the computed SFDI is already
  held by a different EndDevice; the in-memory index is seeded from the store on startup so a restart
  cannot hand out an already-occupied index.
  ([#443](https://github.com/GRIDAPPSD/ieee-2030_5-server-go/issues/443))
- `PUT /edev/{id}` no longer takes the LFDI or SFDI from the request body. A device could rewrite its
  own record with another device's identity, redirecting that device's `GET /edev` and `POST /edev`
  and its manager's access to the rewritten record, or erase its own identity and lock itself out.
  ([#434](https://github.com/GRIDAPPSD/ieee-2030_5-server-go/issues/434))
- **Breaking, corrected during verification (was unmarked).** Subscription `notificationURI`
  destinations are validated.
  `POST /edev/{id}/sub` refuses non-http(s) URIs, hosts that do not resolve,
  and loopback, link-local, unspecified, local multicast, and known cloud
  metadata addresses (including their IPv4-mapped, IPv4-compatible, and NAT64
  forms) with `400 Bad Request`. Delivery re-checks the address at connect
  time, bounds the lookup and shares its connect time across a host's
  addresses, does not follow redirects, ignores proxy environment variables,
  and redacts userinfo, query values, and fragments from logs. `SEP2_NOTIFICATION_ALLOW_LOOPBACK=true` allows loopback for test
  harnesses.
  ([#427](https://github.com/GRIDAPPSD/ieee-2030_5-server-go/issues/427))
- Admin-plane hardening: every response now carries `Content-Security-Policy: frame-ancestors 'none'`,
  `X-Frame-Options: DENY`, `X-Content-Type-Options: nosniff` and `Referrer-Policy: no-referrer`; a
  failed admin credential attempt (login form or Bearer) is logged; and a bug where the login page's
  own headers were silently dropped by a premature `WriteHeader` is fixed.
  ([#413](https://github.com/GRIDAPPSD/ieee-2030_5-server-go/issues/413))
- **Breaking.** A state-changing admin request declaring cross-origin provenance (`Sec-Fetch-Site`
  other than same-origin/none, or a mismatched `Origin`) is refused with `403` before any handler
  runs; an admin write route that decodes a body now refuses an undeclared or mismatched Content-Type
  with `415`.
  ([#416](https://github.com/GRIDAPPSD/ieee-2030_5-server-go/issues/416))

[0.3.0]: https://github.com/GRIDAPPSD/ieee-2030_5-server-go/compare/v0.2.0...v0.3.0
[0.6.0]: https://github.com/GRIDAPPSD/ieee-2030_5-server-go/compare/v0.5.0...v0.6.0
[0.7.0]: https://github.com/GRIDAPPSD/ieee-2030_5-server-go/compare/v0.6.0...v0.7.0
[0.8.0]: https://github.com/GRIDAPPSD/ieee-2030_5-server-go/compare/v0.7.0...v0.8.0
[0.9.0]: https://github.com/GRIDAPPSD/ieee-2030_5-server-go/compare/v0.8.0...v0.9.0
[0.10.0]: https://github.com/GRIDAPPSD/ieee-2030_5-server-go/compare/v0.9.0...v0.10.0
[0.11.0]: https://github.com/GRIDAPPSD/ieee-2030_5-server-go/compare/v0.10.0...v0.11.0
[Unreleased]: https://github.com/GRIDAPPSD/ieee-2030_5-server-go/compare/v0.11.0...HEAD

# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog 1.1.0](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

### Changed

### Deprecated

### Removed

### Fixed

### Security

## [0.6.0] - 2026-09-24

This entry covers `v0.5.0..v0.6.0` (12 merged pull requests). `CHANGELOG.md`
carries no entries for `v0.4.0` or `v0.5.0`: that history was never recorded
at the time, and reconstructing it now would be writing a record nobody kept.

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

- **Breaking.** `ieee-2030_5-core-go` bumped `v0.17.0` -> `v0.19.0`. Core's
  own range types `RequestStatus` as the complex type the schema declares
  (previously a different shape) and churns the `DERAvailability` fields. A
  consumer that reads or constructs `RequestStatus` values, directly or
  through server-go's exported types that embed it, must rebuild against
  core v0.19.0 and re-check that code; a consumer that never touches
  `RequestStatus` or the `DERAvailability` fields needs no change.
  ([#690](https://github.com/GRIDAPPSD/ieee-2030_5-server-go/pull/690))

### Fixed

- **Breaking.** The manager write-delegation rule is replaced with an
  explicit allow-list: only the four DER PUT sub-resources and the LogEvent
  POST are now delegated to a manager on a device it manages. Every other
  write below `/edev/{id}` that the previous wildcard rule permitted is now
  refused. An aggregator or manager client that wrote to a sub-resource
  outside that list now gets refused where it previously succeeded.
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
[Unreleased]: https://github.com/GRIDAPPSD/ieee-2030_5-server-go/compare/v0.6.0...HEAD

# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog 1.1.0](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

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
  at most 20 in each one-minute window per router, shared by all callers. Refusals past that are
  counted, and the count is logged by the first refusal after the window closes.
  ([#354](https://github.com/GRIDAPPSD/ieee-2030_5-server-go/issues/354))

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

### Deprecated

### Removed

### Fixed

- `flow_reservation` handler: `POST /edev/{id}/frq` now returns `500 Internal Server Error`
  when the underlying store fails to create the auto-generated `FlowReservationResponse`.
  Previously the handler returned `201 Created` regardless of the store outcome, silently
  dropping the reservation response while reporting success to the client.
  ([#4](https://github.com/GRIDAPPSD/ieee-2030_5-go/issues/4))

### Security

- Routes under `/edev/{id}` no longer let a device holding a CA-signed client certificate reach
  another device's EndDevice or the resources under it, and `GET /edev` no longer discloses every
  device's LFDI and SFDI. ([#354](https://github.com/GRIDAPPSD/ieee-2030_5-server-go/issues/354))
  Two cross-device paths remain open: `DELETE /edev/{id}/sub/{subId}` deletes a subscription by
  `subId` whatever device `{id}` names, so a device can delete another device's subscription through
  its own `{id}` ([#435](https://github.com/GRIDAPPSD/ieee-2030_5-server-go/issues/435)); and
  `POST /edev` can return another device's EndDevice when the allocated index is already occupied
  ([#443](https://github.com/GRIDAPPSD/ieee-2030_5-server-go/issues/443)).
- `PUT /edev/{id}` no longer takes the LFDI or SFDI from the request body. A device could rewrite its
  own record with another device's identity, redirecting that device's `GET /edev` and `POST /edev`
  and its manager's access to the rewritten record, or erase its own identity and lock itself out.
  ([#434](https://github.com/GRIDAPPSD/ieee-2030_5-server-go/issues/434))

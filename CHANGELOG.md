# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog 1.1.0](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

### Changed

- `subscription.HandleCreateSubscription` takes a notificationURI validator as
  its second argument: pass `(*subscription.Manager).ValidateNotificationURI`,
  or nil for the default policy. `subscription.NewManager` accepts
  `ManagerOption`s, including `WithDestinationPolicy`.
  ([#427](https://github.com/GRIDAPPSD/ieee-2030_5-server-go/issues/427))

### Deprecated

### Removed

### Fixed

- `flow_reservation` handler: `POST /edev/{id}/frq` now returns `500 Internal Server Error`
  when the underlying store fails to create the auto-generated `FlowReservationResponse`.
  Previously the handler returned `201 Created` regardless of the store outcome, silently
  dropping the reservation response while reporting success to the client.
  ([#4](https://github.com/GRIDAPPSD/ieee-2030_5-go/issues/4))

### Security

- Subscription `notificationURI` destinations are validated.
  `POST /edev/{id}/sub` refuses non-http(s) URIs, hosts that do not resolve,
  and loopback, link-local, unspecified, local multicast, and known cloud
  metadata addresses (including their IPv4-mapped, IPv4-compatible, and NAT64
  forms) with `400 Bad Request`. Delivery re-checks the address at connect
  time, shares its connect time across a host's addresses, does not follow
  redirects, ignores proxy environment variables, and redacts userinfo from
  logs. `SEP2_NOTIFICATION_ALLOW_LOOPBACK=true` allows loopback for test
  harnesses.
  ([#427](https://github.com/GRIDAPPSD/ieee-2030_5-server-go/issues/427))

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

- `flow_reservation` handler: `POST /edev/{id}/frq` now returns `500 Internal Server Error`
  when the underlying store fails to create the auto-generated `FlowReservationResponse`.
  Previously the handler returned `201 Created` regardless of the store outcome, silently
  dropping the reservation response while reporting success to the client.
  ([#4](https://github.com/GRIDAPPSD/ieee-2030_5-go/issues/4))

### Security

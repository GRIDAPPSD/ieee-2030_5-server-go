# EndDevice access: ownership and aggregator management

Every route under `/edev/{id}` answers only the device that owns the
EndDevice, or the aggregator provisioned to manage it. This document is for
embedders wiring `assembly.Stores` and operators deciding what a device can
reach. The implementation is `pkg/sep2srv/assembly/ownership.go` (the gate)
and `pkg/sep2srv/handlers/enddevice/owner.go` (`GET /edev`).

## Who may reach an EndDevice

Identity is the LFDI derived from the client certificate. It is compared with
the LFDI stored on the EndDevice record, never with the `{id}` path segment,
which is an opaque server-chosen index.

| Relation | Holds when | Grants |
|---|---|---|
| Self | The stored `EndDevice.LFDI` equals the caller's LFDI, exactly and case-sensitively. Neither may be empty. | Every gated route on the record. |
| Management | A provisioned pair (manager LFDI, managed LFDI) names the caller as the manager of the record's stored LFDI. | The delegated routes below. |

Each managed LFDI has at most one manager. Management is not transitive: a
manager of a device that itself manages others reaches only the first device.
"Aggregator" is not a role flag; it is any LFDI that manages at least one
EndDevice. An aggregator with no pairs is an ordinary device.

## What a manager can and cannot do

A manager may use:

- `GET` and `HEAD /edev/{id}` on the managed record;
- every gated route strictly below `/edev/{id}/` (DER resources, FSAs,
  DERPrograms and DERControls, subscriptions, LogEvents, flow reservations,
  configuration, status singletons), for every method the route serves.

A manager may not:

- `PUT` or `DELETE /edev/{id}`: a manager never rewrites or removes the
  managed device's record;
- `GET /edev/{id}/rg`: the Registration, including its pIN, is the device's
  own.

## GET /edev and PUT /edev/{id}

`GET /edev` lists the caller's own EndDevice and every EndDevice it manages,
ordered by store key. A managed LFDI with no record, a record the LFDI index
returned for a different LFDI, and a record whose href names no store key are
left out and logged. `all` and `results` count the listed set and the page, and
`s`, `l` and `a` page over it. A caller with nothing to list gets an empty
list, not a 404. `POST /edev` and `GET /edev` are the only routes under
`/edev` outside the gate; `POST /edev` takes the LFDI and SFDI from the
certificate.

`PUT /edev/{id}` never changes the record's identity. The stored LFDI and SFDI
are kept whatever the body carries: present, empty, absent, or another
device's. A differing value is ignored rather than refused, so a client that
PUTs back the document it fetched is not turned away.

## Denial statuses

The gate never answers 405; a 405 can still come from the router for a method
no route serves.

| Status | When |
|---|---|
| 403 | No identity, or an identity with an empty LFDI. Checked before the record is read, so it reveals nothing about which ids exist. |
| 404 | The caller has an identity and no EndDevice exists at `{id}`. |
| 403 | The record exists and the caller is neither its owner nor, on a delegated route, its manager. |
| 403 | The record exists but carries no LFDI, so it is owned by nobody. |
| 500 | The EndDevice store or the management store failed, or no EndDevice store is wired. The handler does not run. |

A denial body is a fixed string (`forbidden` or `not found`) and carries no
field of the record.

A refusal by the gate writes a log line naming the route pattern, the caller's
LFDI, the requested id (truncated to 64 bytes, then quoted) and a reason class:
`no-identity`, `no-device-id`, `absent`, `record-has-no-lfdi`, `not-owner` (a
route management never grants, or no management store), or
`not-owner-or-manager` (a delegated route where the caller is not the
record's manager either).

These lines are budgeted per router in one-minute windows. A window opens at
the first refusal after the previous window has closed. Within a window each
caller, keyed by the LFDI its line names, gets at most 5 lines, and all callers
together get at most 100, so one caller's refusals cannot use up the lines of
another. Refusals with no identity share one caller budget. Refusals past
either budget are counted by reason, not written, and a window tracks at most
100 callers however many are refused.

The 100-line cap is 20 caller budgets of 5, so 20 distinct authenticated
callers each spending their own budget in one window still fill it, and a
21st caller's lines are then suppressed the same as an over-budget caller's
would be. Every refusal is still enforced and still counted by reason in that
window's summary; only which caller a line names can be lost.

When a window that counted anything closes, one line reports the count, the
time from the window's first refusal to the report, and the count per reason,
for example `assembly: ownership gate suppressed 495 denial log lines in the
last 1m0s: not-owner=495`. The line is written when the window closes, whether
or not another refusal arrives, and names no caller or id. The router has no
shutdown hook, so a report still pending when the server stops is written once
when its window closes, if the process is still running. A 500 is logged
through the usual server error line.

## Wiring the management store

`assembly.Stores.EndDeviceManagers` holds the pairs, as a
`store.EndDeviceManagementStore`. `memory.NewEndDeviceManagementStore` is the
in-memory implementation.

- An absent (nil) store delegates nothing: every caller reaches only its own
  EndDevice. The router logs that once when it is built.
- `Assign` refuses an empty LFDI, one that is not upper case or carries
  surrounding space, and a device named as its own manager, with
  `store.ErrInvalidManagementPair`. It returns `store.ErrAlreadyExists` when
  another manager already holds the device. Lookups never fold case.
- Pairs are keyed by LFDI, not by the URL index, and outlive the EndDevice
  records they name. A pair whose managed LFDI has no record grants nothing.
- The in-memory store is not persisted: pairs do not survive a restart.

The server binary and `sep2server.NewStores` wire an empty store.

## Who provisions pairs

Management is utility data, established only on the utility side: by an
embedder writing pairs to the store it passes as `Stores.EndDeviceManagers`,
which the gate and `GET /edev` read on every request.
No IEEE 2030.5 request, registration included, creates, changes, or removes a
pair. Admin-plane provisioning and persistence of pairs are not implemented
yet and are tracked in
[#440](https://github.com/GRIDAPPSD/ieee-2030_5-server-go/issues/440); until
then an aggregator on the server binary has self access only.

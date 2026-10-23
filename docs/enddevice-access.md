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

Reads and writes are two separate rules.

**Reads.** A manager may `GET` and `HEAD` whatever the managed device's own
client may: the record itself, and every gated route strictly below it
(DER resources, FSAs, DERPrograms and DERControls, subscriptions, LogEvents,
flow reservations, configuration, status singletons), except the
Registration. Unlike writes, a read added to the route table later is
delegated by default: the gate grants a new GET pattern until an entry here
and in the gate specifically refuses it.

**Writes, creates and deletes.** A manager may make only the requests on an
explicit allow-list, each granted by a specific CSIP IG or V1.2 procedure
citation:

| Request | Grants |
|---|---|
| `PUT /edev/{id}/der/{derId}/dercap` | DERCapability |
| `PUT /edev/{id}/der/{derId}/derg` | DERSettings |
| `PUT /edev/{id}/der/{derId}/ders` | DERStatus |
| `PUT /edev/{id}/der/{derId}/dera` | DERAvailability |
| `POST /edev/{id}/lel` | a LogEvent |

Every other write below a managed record is refused, including
`PUT /edev/{id}/der/{derId}` itself, `POST` and `DELETE` on the managed
device's own subscriptions, `DELETE` on a LogEvent instance, and writes to
`cfg`, `dstat`, `ps` and `frq`, including `PUT /edev/{id}/frq/{frqId}`, the
client's own cancel of a flow reservation request: a manager never rewrites or deletes the
record, and IEEE 2030.5-2018 8.5.3's default is that a write below an
EndDevice is restricted to the device itself unless the allow-list names an
exception.

A manager may not:

- `PUT` or `DELETE /edev/{id}`: a manager never rewrites or removes the
  managed device's record;
- `GET /edev/{id}/rg`: the Registration, including its pIN, is the device's
  own.

A write pattern added to the route table later is refused by default until
an entry naming it, with its source, is added here and to the gate.

## Mirroring for a managed device

A `/mup` route admits a manager acting for a managed device only when that
device also has an EndDevice record, the same pair-plus-record rule `GET /edev`
applies. A pair whose managed LFDI has no record is refused with 403, and the
refusal log line names the reason `managed-device-unregistered` on every
`/mup` route. A device mirroring for itself needs no record. An embedder gets
the check by passing `metering.RequireDeviceRecords(endDevices)` as an option
to each of the `/mup` handlers; without it a pair alone admits the manager.

## GET /edev and PUT /edev/{id}

`GET /edev` lists the caller's own EndDevice and every EndDevice it manages,
ordered by store key. A managed LFDI with no record, a record the LFDI index
returned for a different LFDI, and a record whose href names no store key are
left out and logged. `all` and `results` count the listed set and the page, and
`s`, `l` and `a` page over it. A caller with nothing to list gets an empty
list, not a 404. `POST /edev` and `GET /edev` are the only routes under
`/edev` outside the gate.

## POST /edev

`POST /edev` registers the caller by default: the record carries the LFDI and
SFDI of the client certificate, whatever the body says. The body's lFDI is
compared with the caller's after padding it to 40 hex digits, so the short
lower-case form some clients send still means the caller.

A manager may instead register a device it manages. When the body names another
lFDI and a provisioned pair names the caller as that LFDI's manager, the record
is stored with that LFDI and the SFDI derived from it, `GET /edev` from the
manager lists it, and a repeat POST returns the same href. Registering never
creates a pair.

| Status | When |
|---|---|
| 201 / 200 | The record is created / already exists under that LFDI. |
| 400 | The body lFDI is not hex of at most 40 digits, or its sFDI differs from the one derived from the lFDI. |
| 403 | The body names an lFDI the caller does not manage, or in-band management is off. No record is written. |
| 409 | The derived SFDI is held by another LFDI, or the LFDI exists in another letter case. |

The derived SFDI is not authenticated: any LFDI sharing the first 9 hex digits
yields the same SFDI, which is why a collision is refused rather than
overwritten. `SEP2_INBAND_MANAGEMENT=false` turns manager registration off
(default on). It does not cover manager delete.

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
in-memory implementation; `memory.NewEndDeviceManagementStoreWithPersistence`
wraps it with the same on-disk JSON snapshot machinery the other
admin-mutated stores use (#440), writing to
`<SEP2_DATA_DIR>/enddevicemanagement.json` when a data directory is
configured, and staying pure in-memory otherwise.

- An absent (nil) store delegates nothing: every caller reaches only its own
  EndDevice. The router logs that once when it is built.
- `Assign` refuses an empty LFDI, one that is not upper case, carries
  surrounding space, or is not exactly 40 hex digits, and a device named as
  its own manager, with `store.ErrInvalidManagementPair`. The load path
  enforces the same 40-hex-digit rule as the admin API's own normalization,
  so a value the API would refuse cannot arrive from disk and become an
  entry no API call can address again. It returns `store.ErrAlreadyExists`
  when another manager already holds the device. Lookups never fold case.
- `RekeyManager` and `RekeyManaged` replace a manager LFDI or a managed LFDI
  across that LFDI's pairs, for a certificate rotation: a rotated
  certificate carries a new LFDI, so a pair does not survive rotation of
  either party without this. Both refuse when the LFDI being replaced
  manages, or is managed, nothing, and both refuse a destination that
  collides with an existing pair (`RekeyManager` when the new manager LFDI
  already manages other devices, `RekeyManaged` when the new managed LFDI
  already has a manager): a rekey never silently merges two fleets or
  overwrites an existing pair.
- A certificate rotation is refused, never followed (#1087). A rotated
  certificate presents an LFDI no pair names, and IEEE 2030.5 gives the
  server no way to tell a rotation from another holder of a valid
  certificate claiming the old device's grant. That caller is an ordinary
  device: every delegated route answers 403 with the fixed `forbidden` body,
  and the denial log line names the caller and `reason=not-owner-or-manager`.
  A pair moves only when an operator rekeys it. After a manager rekey the
  new LFDI holds the grant and the old one loses it on the next request.
  After a managed-side rekey the pair names the new LFDI, which has no
  record, so the manager loses the record; the old certificate still owns
  its own record, because the record keeps the old LFDI.
  `GET /api/management-pairs` shows the signal: each LFDI in the answer
  carries `hasRecord` (an EndDevice record exists for it; omitted when the
  handler has no EndDevice reader) and `lastSeen` (RFC 3339 UTC time of its
  last request since the server started, or null). A manager that is quiet
  while an unpaired LFDI starts connecting is the rotation to rekey.
- A write is durable before it is live: a create, remove, or rekey writes
  the candidate snapshot to disk first, and only then updates the in-memory
  pairs. A write reported as failed is therefore never granting access in
  memory, and a retry re-attempts the same write rather than answering
  success for a change that never reached disk.
- A snapshot is validated as it loads, by the same rules `Assign` enforces
  (canonical LFDI, no self-management, one manager per device). A file
  holding a record that fails any of them is refused wholesale, the same as
  a corrupt file or an unsupported version.
- Pairs are keyed by LFDI, not by the URL index, and outlive the EndDevice
  records they name. A pair whose managed LFDI has no record grants nothing.

The server binary wires the persisted store; `sep2server.NewStores` (the
pure in-memory embeddable builder) wires an empty, unpersisted one.

## Who provisions pairs

Management is utility data, established only on the utility side. The
server binary exposes this as the admin-plane API documented in
[`admin.md`](admin.md#admin-features) (`POST`/`GET`/`DELETE
/api/management-pairs`, `POST /api/management-pairs/rekey`, #440); an
embedder may instead write pairs directly to the store it passes as
`Stores.EndDeviceManagers`, which the gate and `GET /edev` read on every
request. No IEEE 2030.5 request, registration included, creates, changes, or
removes a pair.

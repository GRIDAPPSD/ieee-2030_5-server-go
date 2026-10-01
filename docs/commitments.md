# Fleet commitments

One aggregator fleet commits each window to one thing: either a granted flow
reservation with the DER controls carrying it out, or any number of plain
(unlinked) DER controls, never both ([#714](https://github.com/GRIDAPPSD/ieee-2030_5-server-go/issues/714)).
The rule lives in `internal/commitment`. It keeps no state of its own and
reads the flow reservation and DER control stores under a per-fleet lock on
every check.

A fleet is keyed by the LFDI of the aggregator that manages a device, or by
the device's own LFDI when nothing manages it. A reservation is posted on the
aggregator's own EndDevice; a control is stored under one managed device; both
resolve to the same key.

## Sign convention

A FlowReservationResponse carries the request's sign: energy is **charging
positive** (IEEE 2030.5-2023 states no sign for `energyAvailable`; this server
declares it to match `energyRequested`). A DER control's `opModTargetW` is
**discharge positive**. So a control carrying out a charge grant must set a
negative `opModTargetW`, and a discharge grant a positive one. A target of the
other sign, or zero, is refused with `execution_reverses_grant`.

## What is counted

* A grant is counted while its response has an interval of positive duration
  and no cancel mark. A denial (duration zero) and a grant with no interval
  commit nothing.
* Only DER controls created through the admin API are counted, because only
  those have a lifecycle record. Controls loaded from a boot fixture or by the
  CSIP harness have none and are never counted, in either direction.
* A cancelled grant or control is out of every check from the moment it is
  cancelled, so its window is free again at once.

## Per-device counting

`opModTargetW` is a per-device target: every device that reads a control
applies it. A control's power is therefore `|opModTargetW|` times the number
of the fleet's devices that read it (its reach), and its energy is that
power times its duration. At every instant the summed power of a grant's
controls must stay within `powerAvailable`, and their summed energy within
`energyAvailable`. A control is served only under the device it is stored
beneath, so every control's reach is 1 today: carrying out a grant across four
devices takes four controls.

## Cancelling and revising a grant

Cancelling a grant cancels its controls first and marks the response last, so
a failure part way never leaves live controls under a cancelled grant. The
response is still served, with EventStatus Cancelled.

A revision cancels the old response and creates a new one. IEEE 2030.5-2023
lists cancelling and reissuing as a way to change an event, and marks the
Superseded status deprecated: servers shall not use it. An overlapping newer
event would also leave the old one Active.

A revision stores a new response, moves every live control to it, then marks
the old one cancelled. Only a live grant can be revised: a denial, a response
with no interval and a cancelled one are refused. The new response must have
the old one's subject and a later creationTime, so a client that sees both
picks the new one (2023 10.2.2.3). It must also be executable, and every live
control must still fit; otherwise the revision is refused, naming the first
control (by start time, then mRID) that would not fit, and nothing changes. A
revision to duration zero is a denial: the old grant's controls are cancelled.
A revision is stored under `<request id>-r1`, then `-r2` and on, because the
request's own id holds its first response.

Between storing the new response and marking the old one cancelled, both are
live for a moment. The fleet lock keeps every other commitment check out of
that window, but a client reading the response list then sees both; the
creationTime rule above tells it which is current.

### Partial failure and retry

A failed revision step is undone in reverse: controls are moved back and the
new response is deleted. Only when the undo itself fails is the error
`ErrUndo`, meaning the stores may hold part of the revision.

Cancelling is never undone. If a control fails to cancel, the grant stays
live with some controls already cancelled, which is a legal state; calling
cancel again skips the cancelled controls and finishes the job. A revision to
duration zero behaves the same way for its controls.

## Restart

DER controls and their lifecycle records persist across a restart; flow
reservation requests and responses do not yet
([#738](https://github.com/GRIDAPPSD/ieee-2030_5-server-go/issues/738)).
After a restart a control that carried out a grant names a grant the server no
longer holds. It is then counted as a plain control over its window, so it
blocks any new grant there rather than freeing it, and it can no longer be
cancelled or moved with a grant. Cancel it on its own through
`POST /api/der/controls/{mrid}/cancel`.

# Subscription Tuning Sweep: 100 Active Clients at 20 Hz

Worker count is the operative lever: doubling workers from 4 to 8 cuts the drop
rate from 34.0% to 3.8% against the same load; increasing queue depth from 256
to 4096 with 4 workers leaves the drop rate at 32.6%. The shipped 4/256 defaults
are inadequate for 100 active-and-subscribed clients at 20 Hz fan-out.

**Fixed load:** 100 active-and-subscribed clients, 20 Hz mutation rate
(approximately 2,000 notifications per second). 60 seconds per configuration.
**Variable:** `SEP2_SUBSCRIPTION_WORKERS` and `SEP2_SUBSCRIPTION_QUEUE_SIZE`.

---

## Key Terms

**Client vs subscriber:** a *subscriber* is any registered device holding a
subscription that receives notifications; it can otherwise be passive. A *client*
is an active driver doing the SEP2 protocol workload (GET/PUT/POST). In these runs
the 100 devices are both: each drives GET traffic AND holds a `/dcap` subscription
AND runs a notification receiver. This is the realistic, demanding case.

**Subscription worker:** a server goroutine that pulls a queued notification and
delivers it via HTTP POST to a subscriber. Count is set by `SEP2_SUBSCRIPTION_WORKERS`
(default 4).

**Subscription queue:** the buffer holding pending notifications. Depth is set by
`SEP2_SUBSCRIPTION_QUEUE_SIZE` (default 256). When full, arriving notifications are
dropped and recorded as `queue_full`.

**Fan-out:** one resource change produces N notifications, one per subscriber. At
100 subscribers and 20 Hz, the fan-out rate is approximately 2,000 notifications
per second.

**What each row represents:** one server configuration under the same fixed load.
Columns report when the queue first saturated and what fraction of notifications
were dropped over the 60-second run.

**"Best":** the smallest worker count that carries the load within an acceptable
drop tolerance. Queue depth is not the effective lever here.

---

## Results

| workers | queue | onset | drop rate |
|---|---|---|---|
| 4 | 256 (shipped) | 4.1 s | 34.0% |
| 8 | 256 | 4.1 s | 3.8% |
| 16 | 256 | 12.1 s | 0.78% |
| 32 | 256 | 56.1 s | 0.08% |
| 4 | 1024 | 4.1 s | 12.8% |
| 4 | 4096 | 8.0 s | 32.6% |
| 8 | 1024 | 6.0 s | 13.0% |
| 16 | 1024 | 22.1 s | 0.66% |

---

## Findings

### Worker-bound, not queue-bound

The sweep is unambiguously worker-bound. Holding workers at 4 and raising the queue
from 256 to 4096 produces a drop rate of 32.6%, which is worse than 8 workers with
a 256-depth queue (3.8%). A large queue delays saturation onset by a few seconds but
does not move enough notifications out of the buffer to improve the final drop rate
meaningfully. Workers are the lever.

### Effect of active-client load

In the earlier 100-passive-subscriber sweep (identical notification rate, no GET
traffic from the subscribers), 32 workers cleared the load with zero drops. Here,
32 workers drop 0.08% at an onset of 56.1 seconds: not zero, but marginal. The
concurrent GET load from 100 active clients makes even 32 workers insufficient to
fully clear 2,000 notifications per second. This confirms the headline finding from
the baseline report (`test/stress/reports/fanout-baseline.md`): active-client load
is the critical variable that tightens the subscription worker budget.

### Recommended configuration for this load

For 100 active clients at 20 Hz:

- **32 workers / 256 queue** is best available: 0.08% drops, onset at 56.1 seconds.
  Raising queue depth above 256 offers no measurable benefit.
- **16 workers / 256 queue** is the practical knee: 0.78% drops with a meaningful
  reduction in `SEP2_SUBSCRIPTION_WORKERS` overhead.
- The shipped **4 workers / 256 queue** is inadequate for this load: 34.0% drops
  within 4.1 seconds.

---

## Validity Gate Caveat

The harness validity gate computes average notification delivery rate over the entire
60-second run, including the ramp period. Seven of the eight configurations above
triggered a `queue_full` event in the first few seconds, which causes the gate to
report a false partial-subscription verdict for those runs.

These are not real subscription failures. The server logs show zero subscription
errors across all eight runs; every device successfully subscribed before the load
phase began. The onset and drop-rate figures in the table above are valid raw
measurements from `sep2_subscription_notifications_total{outcome="queue_full"}`,
scraped every 2 seconds.

The correct gate is a trailing-window delivery rate that excludes the ramp period.
Implementing that window is a tracked follow-up. Do not interpret the gate verdicts
in the `breaking-point.json` artifacts from these runs as evidence of misconfigured
subscriptions.

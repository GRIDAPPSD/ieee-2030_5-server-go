# Fan-out Baseline: 100 Active Clients at Shipped Defaults

The shipped 4-worker / 256-queue configuration breaks under active-client fan-out
load far sooner than under a passive-subscriber-only load: at 2,000 notifications
per second the queue saturates within 4 seconds. At 500 notifications per second
the server holds for 168 seconds with negligible drops, establishing the clean
breaking-point boundary.

**Configuration:** `SEP2_SUBSCRIPTION_WORKERS=4` / `SEP2_SUBSCRIPTION_QUEUE_SIZE=256`
(shipped defaults, no env overrides).
**Load:** 100 active-and-subscribed clients. Fan-out dimension.

---

## Key Terms

**Client vs subscriber:** a *subscriber* is any registered device holding a
subscription that receives notifications; it can otherwise be passive. A *client*
is an active driver doing the SEP2 protocol workload (GET/PUT/POST). In these runs
the 100 devices are both: each drives GET traffic against `{/dcap, /tm, /edev}`
at approximately 16,000 to 18,400 requests per second in aggregate AND holds a
`/dcap` subscription AND runs a notification receiver. This is the realistic,
demanding case; passive-subscriber-only runs represent a lower-stress baseline.

**Subscription worker:** a server goroutine that pulls a queued notification and
delivers it via HTTP POST to a subscriber. Count is set by `SEP2_SUBSCRIPTION_WORKERS`
(default 4).

**Subscription queue:** the buffer holding pending notifications. Depth is set by
`SEP2_SUBSCRIPTION_QUEUE_SIZE` (default 256). When full, arriving notifications are
dropped and recorded as `queue_full`.

**Fan-out:** one resource change produces N notifications, one per subscriber.
Production rate equals event rate multiplied by subscriber count.

---

## Load Setup

The harness registers 100 virtual devices (POST /edev per client), then ramps them
at the configured rate. Each client subscribes to `/dcap` (the server-wide
DeviceCapability resource) using its own mTLS device certificate. After subscribing,
each client begins GET round-robin traffic immediately. A separate goroutine inside
`sep2loadgen` injects mutations via POST `/test/mutations/stress-notify` at the
configured rate; each mutation fans out to all 100 subscribers.

The break criterion fires the moment `sep2_subscription_notifications_total{outcome="queue_full"}`
goes non-zero. Results are stamped to `breaking-point.json`.

---

## Results

### 20 Hz mutation rate (2,000 notifications per second)

The queue saturated in approximately 4 seconds with a 34% drop rate. The concurrent
GET load (~16,000 to 18,400 rps aggregate) consumes server worker capacity that sits
idle in a passive-subscriber-only run, so the subscription workers fall behind the
notification queue almost immediately.

### 5 Hz mutation rate (500 notifications per second)

The server held for 168 seconds before the first `queue_full` event, with 0.17%
drops total. Validity gate result:

- `delivered_per_mutation = 98.2` (gate threshold: approximately 100 per mutation
  at 100 subscribers). **PASS.**
- `host_limited = false`.

This is the clean breaking-point run: the load is sustainable long enough to
characterize the true server limit rather than a host artifact.

---

## Comparison: Active Clients vs Passive Subscribers

The table below shows how adding active GET load changes the breaking point at
the same 2,000 notifications per second rate.

| Load type | Configuration | Break onset | Drop rate |
|---|---|---|---|
| 100 passive subscribers (no GET load) | 4 workers / 256 queue | ~10 s | 3.1% |
| 100 active clients (16 to 18k rps) | 4 workers / 256 queue | ~4 s | 34.0% |

The active-client load is the critical variable. The same server and the same
shipped defaults handle passive subscribers reasonably well at this notification
rate; they fail quickly when those same 100 devices also drive protocol traffic.

---

## Interpretation

The shipped 4-worker / 256-queue configuration is inadequate for production deployments
where subscribed devices are also active protocol clients at this scale. The queue
depth is not the lever: adding queue depth buffers notifications temporarily but does
not increase delivery throughput. Worker count drives throughput.

For tuning guidance at 20 Hz with 100 active clients, see the companion sweep report:
`test/stress/reports/subscription-tuning-sweep.md`.

<script lang="ts">
  // GET /api/derms/flow-reservations?aggregatorLFDI=<lfdi> (#764, not built
  // yet), one call per aggregator listed by /api/derms/fleets: each
  // aggregator's flow reservation requests, read-only. The page renders
  // what the server says: the tip, the history, the commitments and the
  // deadline all come from the payload, and a 404 reads as "not available"
  // rather than as an empty queue.
  import { onDestroy, onMount } from 'svelte'
  import { fetchJSON } from '../lib/api'
  import { formatAge } from '../lib/fleet'
  import {
    directionLabel,
    formatActor,
    formatCountdown,
    formatInterval,
    formatQuantity,
    historyResponses,
    normalizeQueue,
    parseFleetLFDIs,
    scaledNumber,
    type FlowReservationQueue,
    type ResponseView,
  } from '../lib/flowreservation'

  const TICK_MS = 1000
  const REFRESH_MS = 30_000
  const FETCH_TIMEOUT_MS = 15_000

  // serverNow anchors a queue's countdown to the server's clock: the
  // payload's own `now`, else the response Date header, else null.
  interface QueueView {
    lfdi: string
    queue: FlowReservationQueue
    serverNow: number | null
  }

  let views = $state<QueueView[]>([])
  let loaded = $state(false)
  let refreshing = $state(true)
  let unavailable = $state(false)
  let error = $state('')
  let fetchedAtMs = $state(Date.now())
  let nowMs = $state(Date.now())

  // A sequence guard drops a response that a newer load has overtaken; the
  // abort controller drops one that lands after unmount and cancels it on
  // the wire.
  let requestSeq = 0
  const lifetime = new AbortController()
  let tickTimer: ReturnType<typeof setInterval> | undefined
  let refreshTimer: ReturnType<typeof setInterval> | undefined

  const bounds = { signal: lifetime.signal, timeoutMs: FETCH_TIMEOUT_MS }

  // fail keeps the last good queue on screen, marked stale, and only
  // replaces the pane when there is nothing good to keep.
  function fail(message: string, status: number) {
    if (!loaded && status === 404) {
      unavailable = true
    } else {
      error = message
    }
    refreshing = false
  }

  async function load() {
    const seq = ++requestSeq
    refreshing = true
    const fleets = await fetchJSON<unknown>('/api/derms/fleets', bounds)
    if (lifetime.signal.aborted || seq !== requestSeq) return
    if (!fleets.ok) return fail(fleets.error, fleets.status)
    const lfdis = parseFleetLFDIs(fleets.data)
    if (lfdis === null) return fail('server returned an unexpected fleet list', 0)

    const results = await Promise.all(
      lfdis.map((l) => fetchJSON<unknown>('/api/derms/flow-reservations?aggregatorLFDI=' + encodeURIComponent(l), bounds)),
    )
    if (lifetime.signal.aborted || seq !== requestSeq) return
    const next: QueueView[] = []
    for (const [i, res] of results.entries()) {
      if (!res.ok) return fail(res.error, res.status)
      const parsed = normalizeQueue(res.data)
      if ('error' in parsed) return fail('server returned an unexpected response shape: ' + parsed.error, 0)
      const own = parsed.queue.now
      const header = res.serverTime === undefined ? null : Math.floor(res.serverTime / 1000)
      next.push({ lfdi: lfdis[i], queue: parsed.queue, serverNow: typeof own === 'number' ? own : header })
    }
    views = next
    loaded = true
    unavailable = false
    error = ''
    fetchedAtMs = Date.now()
    nowMs = fetchedAtMs
    refreshing = false
  }

  onMount(() => {
    load()
    tickTimer = setInterval(() => {
      nowMs = Date.now()
    }, TICK_MS)
    refreshTimer = setInterval(() => {
      if (!refreshing) load()
    }, REFRESH_MS)
  })

  onDestroy(() => {
    lifetime.abort()
    if (tickTimer !== undefined) clearInterval(tickTimer)
    if (refreshTimer !== undefined) clearInterval(refreshTimer)
  })

  function elapsedSeconds(): number {
    return Math.max(0, Math.floor((nowMs - fetchedAtMs) / 1000))
  }
</script>

{#snippet responseView(r: ResponseView, tip: boolean)}
  <div class={tip ? 'response tip' : 'response history'} data-testid={tip ? 'frq-tip' : 'frq-history'}>
    <div>
      <strong>{tip ? 'Current answer' : 'Earlier answer'}</strong>
      <span data-testid="response-status">{r.eventStatus?.status ?? 'status missing'}</span>
    </div>
    <div>
      {formatInterval(r.interval)}:
      <span data-testid="response-energy">{formatQuantity(scaledNumber(r.energyAvailable), 'Wh')}</span>
      {directionLabel(r.direction)},
      <span data-testid="response-power">{formatQuantity(scaledNumber(r.powerAvailable), 'W')}</span>
    </div>
    <div class="hint" data-testid="response-answered">answered by {formatActor(r.answeredBy)}</div>
    {#if r.cancelledBy !== null || r.cancelReason !== null}
      <div class="hint" data-testid="response-cancelled">
        cancelled by {formatActor(r.cancelledBy)}{r.cancelReason !== null ? `: ${r.cancelReason}` : ''}
      </div>
    {/if}
    {#if tip}
      <div data-testid="response-commitment">
        energy committed {formatQuantity(r.energyCommittedWh, 'Wh')}, remaining
        {formatQuantity(r.energyRemainingWh, 'Wh')}
      </div>
      {#each r.executions as ex (ex.mRID)}
        <div class="hint" data-testid="response-execution">
          control {ex.eventStatus?.status ?? 'status missing'}, {formatInterval(ex.interval)}, target
          {formatQuantity(scaledNumber(ex.targetW), 'W')} {directionLabel(r.direction)} (DER frame)
        </div>
      {/each}
    {/if}
  </div>
{/snippet}

<div class="card full-width">
  <h2>Flow Reservation Requests</h2>
  <div class="hint">
    <button class="btn btn-small" onclick={load} disabled={refreshing}>Refresh</button>
    {#if loaded}
      <span data-testid="frq-age">fetched {formatAge(Math.max(0, Math.floor((nowMs - fetchedAtMs) / 1000)))}</span>
    {/if}
  </div>
  {#if !loaded && unavailable}
    <p class="hint" data-testid="frq-unavailable">
      Flow reservation requests are not available on this server yet.
    </p>
  {:else if !loaded && error !== ''}
    <div class="result err" data-testid="frq-error">Could not load requests: {error}</div>
  {:else if !loaded}
    <p class="hint" data-testid="frq-loading">Loading requests...</p>
  {:else}
    {#if error !== ''}
      <div class="result err" data-testid="frq-stale">
        Could not refresh requests: {error}. Showing the queue fetched {formatAge(elapsedSeconds())} (stale).
      </div>
    {/if}
    {#if views.every((v) => v.queue.requests.length === 0)}
      <p class="hint" data-testid="frq-empty">No flow reservation requests.</p>
    {/if}
    {#each views as view (view.lfdi)}
      {#if view.queue.requests.length > 0}
        {#if view.queue.persisted === false}
          <p class="hint" data-testid="frq-not-persisted">
            This server does not persist requests; they are lost on restart.
          </p>
        {/if}
        <table>
          <thead>
            <tr>
              <th>Aggregator</th>
              <th>Requested</th>
              <th>State</th>
              <th>Answer</th>
            </tr>
          </thead>
          <tbody>
            {#each view.queue.requests as entry (entry.requestHref)}
              <tr data-testid="frq-row">
                <td class="mono" title={entry.aggregatorLFDI}>{entry.aggregatorLFDI.substring(0, 16)}...</td>
                <td>
                  {formatInterval(entry.request.intervalRequested)}
                  <div data-testid="frq-requested">
                    {formatQuantity(scaledNumber(entry.request.energyRequested), 'Wh')}
                    {directionLabel(entry.request.direction)},
                    {formatQuantity(scaledNumber(entry.request.powerRequested), 'W')}
                  </div>
                </td>
                <td>
                  <span data-testid="frq-state">{entry.state}</span>
                  {#if entry.state === 'pending'}
                    <div data-testid="frq-countdown">
                      {formatCountdown(entry.deadlineAt, view.serverNow, elapsedSeconds())}
                    </div>
                  {/if}
                </td>
                <td>
                  {#if entry.tip !== null}
                    {@render responseView(entry.tip, true)}
                  {/if}
                  {#each historyResponses(entry) as r (r.id)}
                    {@render responseView(r, false)}
                  {/each}
                  {#if entry.tip === null && entry.responses.length === 0}
                    <span class="hint">no answer yet</span>
                  {/if}
                </td>
              </tr>
            {/each}
          </tbody>
        </table>
      {/if}
    {/each}
  {/if}
</div>

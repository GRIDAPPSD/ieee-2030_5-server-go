<script lang="ts">
  // GET /api/derms/flow-reservations (#764, not built yet): each aggregator's
  // flow reservation requests, read-only. The page renders what the server
  // says: the tip, the history, the commitments and the deadline all come
  // from the payload, and a 404 reads as "not available" rather than as an
  // empty queue.
  import { onDestroy, onMount } from 'svelte'
  import { fetchJSON } from '../lib/api'
  import {
    directionLabel,
    formatActor,
    formatCountdown,
    formatInterval,
    formatQuantity,
    historyResponses,
    normalizeQueues,
    remainingSeconds,
    scaledNumber,
    type FlowReservationQueue,
    type ResponseView,
  } from '../lib/flowreservation'

  const TICK_MS = 1000

  let queues = $state<FlowReservationQueue[]>([])
  let status = $state<'loading' | 'ready' | 'unavailable' | 'error'>('loading')
  let error = $state('')
  // fetchedAtMs and the per-queue serverNow anchor the countdown to the
  // server's clock; nowMs only advances the display.
  let fetchedAtMs = $state(Date.now())
  let nowMs = $state(Date.now())

  let requestSeq = 0
  let destroyed = false
  let tickTimer: ReturnType<typeof setInterval> | undefined

  async function load() {
    const seq = ++requestSeq
    status = 'loading'
    const res = await fetchJSON<unknown>('/api/derms/flow-reservations')
    if (destroyed || seq !== requestSeq) return
    if (!res.ok) {
      if (res.status === 404) {
        status = 'unavailable'
        return
      }
      status = 'error'
      error = res.error
      return
    }
    const parsed = normalizeQueues(res.data)
    if (parsed === null) {
      status = 'error'
      error = 'server returned an unexpected response shape'
      return
    }
    queues = parsed
    fetchedAtMs = Date.now()
    nowMs = fetchedAtMs
    status = 'ready'
  }

  onMount(() => {
    load()
    tickTimer = setInterval(() => {
      nowMs = Date.now()
    }, TICK_MS)
  })

  onDestroy(() => {
    destroyed = true
    if (tickTimer !== undefined) clearInterval(tickTimer)
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
    <button class="btn btn-small" onclick={load} disabled={status === 'loading'}>Refresh</button>
  </div>
  {#if status === 'loading'}
    <p class="hint" data-testid="frq-loading">Loading requests...</p>
  {:else if status === 'unavailable'}
    <p class="hint" data-testid="frq-unavailable">
      Flow reservation requests are not available on this server yet.
    </p>
  {:else if status === 'error'}
    <div class="result err" data-testid="frq-error">Could not load requests: {error}</div>
  {:else if queues.every((q) => q.requests.length === 0)}
    <p class="hint" data-testid="frq-empty">No flow reservation requests.</p>
  {:else}
    {#each queues as queue (queue.aggregatorLFDI)}
      {#if queue.persisted === false}
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
          {#each queue.requests as entry (entry.requestHref)}
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
                    {formatCountdown(
                      remainingSeconds(entry.deadlineAt, queue.now ?? Math.floor(fetchedAtMs / 1000), elapsedSeconds()),
                    )}
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
    {/each}
  {/if}
</div>

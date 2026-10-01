<script lang="ts">
  // GET /api/derms/flow-reservations?aggregatorLFDI=<lfdi> (#764, not built
  // yet), one call per aggregator listed by /api/derms/fleets: each
  // aggregator's flow reservation requests, read-only. The page renders
  // what the server says: the tip, the history, the commitments and the
  // deadline all come from the payload, and a 404 reads as "not available"
  // rather than as an empty queue. The operator's answer, revise and cancel
  // actions post to the write routes (#670) and the row is replaced from the
  // view the server returns, never patched here.
  import { onDestroy, onMount } from 'svelte'
  import { fetchJSON } from '../lib/api'
  import { formatAge } from '../lib/fleet'
  import {
    REASON_MAX,
    buildAction,
    emptyForm,
    submitWrite,
    type ActionForm,
    type ActionKind,
  } from '../lib/flowreservationWrite'
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
    type FlowReservationEntry,
    type FlowReservationQueue,
    type ResponseView,
  } from '../lib/flowreservation'

  const TICK_MS = 1000
  const REFRESH_MS = 30_000
  const FETCH_TIMEOUT_MS = 15_000
  const WRITE_TIMEOUT_MS = 20_000

  // One action is open at a time. `confirming` is the step after the
  // inputs: nothing is sent until it is on screen and Confirm is pressed.
  interface OpenAction {
    href: string
    kind: ActionKind
    form: ActionForm
    confirming: boolean
    busy: boolean
    error: string
  }

  // serverNow anchors a queue's countdown to the server's clock: the
  // payload's own `now`, else the response Date header, else null.
  // One section per aggregator. queue is null when that aggregator has
  // never loaded; error is set when its latest fetch failed, in which case
  // a queue from an earlier fetch stays visible, marked stale.
  interface QueueView {
    lfdi: string
    queue: FlowReservationQueue | null
    serverNow: number | null
    fetchedAtMs: number
    error: string | null
  }

  let views = $state<QueueView[]>([])
  let loaded = $state(false)
  let refreshing = $state(true)
  let unavailable = $state(false)
  let failed = $state(false)
  let error = $state('')
  let fetchedAtMs = $state(Date.now())
  let nowMs = $state(Date.now())

  // An empty message must not hide a failure, so visibility rides on `failed`.
  function reason(message: string): string {
    return message === '' ? 'no message from the server' : message
  }

  // A sequence guard drops a response that a newer load has overtaken; the
  // abort controller drops one that lands after unmount and cancels it on
  // the wire.
  let requestSeq = 0
  let writeSeq = 0
  let writeCtrl: AbortController | null = null
  let action = $state<OpenAction | null>(null)
  let writeNote = $state('')
  // A write that lands while a load is in flight would be undone by that
  // load's older payload, so the written row is kept over it. `seq` is the
  // latest load at write time, and any load numbered at or below it started
  // before the write.
  const written = new Map<string, { entry: FlowReservationEntry; seq: number }>()
  const lifetime = new AbortController()
  let tickTimer: ReturnType<typeof setInterval> | undefined
  let refreshTimer: ReturnType<typeof setInterval> | undefined

  const bounds = { signal: lifetime.signal, timeoutMs: FETCH_TIMEOUT_MS }

  // fail keeps the last good queue on screen, marked stale, and only
  // replaces the pane when there is nothing good to keep.
  function fail(message: string, status: number) {
    if (!loaded && status === 404) {
      unavailable = true
      failed = false
      error = ''
    } else {
      unavailable = false
      failed = true
      error = message
    }
    refreshing = false
  }

  // load never leaves `refreshing` set: a throw in any step reaches the
  // same visible error as a failed fetch, unless a newer load owns the pane.
  async function load() {
    const seq = ++requestSeq
    refreshing = true
    try {
      await run(seq)
    } catch (err) {
      if (!lifetime.signal.aborted && seq === requestSeq) {
        fail(err instanceof Error ? err.message : 'unexpected error', 0)
      }
    }
  }

  async function run(seq: number) {
    const fleets = await fetchJSON<unknown>('/api/derms/fleets', bounds)
    if (lifetime.signal.aborted || seq !== requestSeq) return
    if (!fleets.ok) return fail(fleets.error, fleets.status)
    const lfdis = parseFleetLFDIs(fleets.data)
    if (lfdis === null) return fail('server returned an unexpected fleet list', 0)

    const results = await Promise.all(
      lfdis.map(async (l) => {
        const res = await fetchJSON<unknown>(
          '/api/derms/flow-reservations?aggregatorLFDI=' + encodeURIComponent(l),
          bounds,
        )
        return { res, arrivedAt: Date.now() }
      }),
    )
    if (lifetime.signal.aborted || seq !== requestSeq) return
    const next: QueueView[] = []
    let succeeded = 0
    let firstFailure: { error: string; status: number } | null = null
    for (const [i, { res, arrivedAt }] of results.entries()) {
      const lfdi = lfdis[i]
      const prev = views.find((v) => v.lfdi === lfdi)
      let problem: { error: string; status: number } | null = null
      if (!res.ok) {
        problem = { error: res.error, status: res.status }
      } else {
        const parsed = normalizeQueue(res.data)
        if ('error' in parsed) {
          problem = { error: 'server returned an unexpected response shape: ' + parsed.error, status: 0 }
        } else {
          parsed.queue.requests = parsed.queue.requests.map((e) => {
            const w = written.get(e.requestHref)
            return w !== undefined && w.seq >= seq ? w.entry : e
          })
          const own = parsed.queue.now
          const header = res.serverTime === undefined ? null : Math.floor(res.serverTime / 1000)
          next.push({
            lfdi,
            queue: parsed.queue,
            serverNow: typeof own === 'number' ? own : header,
            fetchedAtMs: arrivedAt,
            error: null,
          })
          succeeded++
        }
      }
      if (problem !== null) {
        // A non-404 failure outranks a 404, so a real error is never read as
        // "not available yet".
        if (firstFailure === null || (firstFailure.status === 404 && problem.status !== 404)) {
          firstFailure = problem
        }
        next.push({
          lfdi,
          queue: prev?.queue ?? null,
          serverNow: prev?.serverNow ?? null,
          fetchedAtMs: prev?.fetchedAtMs ?? Date.now(),
          error: problem.error,
        })
      }
    }
    // Every aggregator failing is a pane-level failure; one failing is
    // only that aggregator's section.
    if (lfdis.length > 0 && succeeded === 0 && firstFailure !== null) {
      return fail(firstFailure.error, firstFailure.status)
    }
    for (const [href, w] of written) if (w.seq < seq) written.delete(href)
    views = next
    loaded = true
    unavailable = false
    failed = false
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
    writeCtrl?.abort()
    if (tickTimer !== undefined) clearInterval(tickTimer)
    if (refreshTimer !== undefined) clearInterval(refreshTimer)
  })

  function openAction(entry: FlowReservationEntry, kind: ActionKind) {
    if (action?.busy === true) return
    const form = emptyForm()
    const asked = entry.request.intervalRequested
    const held = kind === 'revise' ? entry.tip?.interval : asked
    if ((kind === 'grant_adjusted' || kind === 'revise') && held) {
      form.start = new Date(held.start * 1000).toISOString().replace('.000Z', 'Z')
      form.duration = String(held.duration)
    }
    writeNote = ''
    action = {
      href: entry.requestHref,
      kind,
      form,
      confirming: kind === 'grant_as_asked' || kind === 'deny',
      busy: false,
      error: '',
    }
  }

  function review(entry: FlowReservationEntry) {
    if (action === null) return
    const built = buildAction(action.kind, entry, action.form)
    if (!built.ok) {
      action.error = built.error
      return
    }
    action.error = ''
    action.confirming = true
  }

  function closeAction() {
    if (action?.busy === true) return
    action = null
  }

  // stopWaiting drops the answer to an in-flight write: the response is
  // ignored when it lands, and the pane says the outcome is unknown.
  function stopWaiting() {
    writeSeq++
    writeCtrl?.abort()
    writeCtrl = null
    action = null
    writeNote = 'Stopped waiting. The server may or may not have applied the change; refresh to see the current state.'
  }

  function replaceRow(entry: FlowReservationEntry) {
    written.set(entry.requestHref, { entry, seq: requestSeq })
    views = views.map((v) =>
      v.queue === null
        ? v
        : {
            ...v,
            queue: {
              ...v.queue,
              requests: v.queue.requests.map((e) => (e.requestHref === entry.requestHref ? entry : e)),
            },
          },
    )
  }

  async function confirm(entry: FlowReservationEntry) {
    const open = action
    if (open === null || open.busy || !open.confirming) return
    const built = buildAction(open.kind, entry, open.form)
    if (!built.ok) {
      open.error = built.error
      open.confirming = false
      return
    }
    const seq = ++writeSeq
    const ctrl = new AbortController()
    writeCtrl = ctrl
    open.busy = true
    open.error = ''
    const result = await submitWrite(built.path, built.body, { signal: ctrl.signal, timeoutMs: WRITE_TIMEOUT_MS })
    if (lifetime.signal.aborted || seq !== writeSeq) return
    writeCtrl = null
    if (result.ok) {
      replaceRow(result.entry)
      writeNote = 'Request ' + result.entry.frqId + ' is now ' + result.entry.state + ' (server answer).'
      action = null
      return
    }
    open.busy = false
    open.error = result.message
    if (result.refresh && !refreshing) load()
  }

  const KIND_LABEL: Record<ActionKind, string> = {
    grant_as_asked: 'Grant as asked',
    grant_adjusted: 'Grant adjusted',
    deny: 'Deny',
    revise: 'Revise',
    cancel: 'Cancel grant',
  }

  // effectText says what Confirm will do, in the operator's terms. For an
  // adjusted grant it repeats the values typed, as magnitudes.
  function effectText(entry: FlowReservationEntry, a: OpenAction): string {
    const f = a.form
    const adjusted =
      [
        f.start !== '' ? 'interval ' + f.start + ' for ' + f.duration + ' s' : '',
        f.energy !== '' ? 'energy ' + f.energy + ' Wh' : '',
        f.power !== '' ? 'power ' + f.power + ' W' : '',
      ]
        .filter((t) => t !== '')
        .join(', ') || 'the values as asked'
    switch (a.kind) {
      case 'grant_as_asked':
        return 'Grant the request exactly as asked: ' + formatInterval(entry.request.intervalRequested) + '.'
      case 'grant_adjusted':
        return 'Grant the request with ' + adjusted + '. Anything left blank is granted as asked. The server applies the request\'s direction.'
      case 'deny':
        return 'Deny the request. The aggregator is answered with a zero-duration grant.'
      case 'revise':
        return f.deny
          ? 'Cancel the current grant and deny the request. Controls carrying out the grant are checked by the server.'
          : 'Cancel the current grant and issue a new one with ' + adjusted + '. Controls carrying out the old grant move to the new one only if they still fit.'
      case 'cancel':
        return 'Cancel the grant and the controls carrying it out.'
    }
  }

  function ageOf(since: number): number {
    return Math.max(0, Math.floor((nowMs - since) / 1000))
  }
</script>

{#snippet actionPanel(entry: FlowReservationEntry)}
  {#if action !== null && action.href === entry.requestHref}
    {@const a = action}
    <div class="action-panel" data-testid="frq-action">
      <strong>{KIND_LABEL[a.kind]}</strong>
      <span class="hint">request {entry.frqId} on device {entry.edevId}</span>
      {#if !a.confirming}
        {#if a.kind === 'grant_adjusted' || (a.kind === 'revise' && !a.form.deny)}
          <div class="hint">Blank means as asked. Energy and power are magnitudes, with no sign.</div>
          <label>Start (UTC) <input type="text" bind:value={a.form.start} data-testid="frq-in-start" /></label>
          <label>Duration (s) <input type="text" inputmode="numeric" bind:value={a.form.duration} data-testid="frq-in-duration" /></label>
          <label>Energy (Wh) <input type="text" inputmode="numeric" bind:value={a.form.energy} data-testid="frq-in-energy" /></label>
          <label>Power (W) <input type="text" inputmode="numeric" bind:value={a.form.power} data-testid="frq-in-power" /></label>
        {/if}
        {#if a.kind === 'revise'}
          <label><input type="checkbox" bind:checked={a.form.deny} data-testid="frq-in-deny" /> Deny instead</label>
        {/if}
        {#if a.kind === 'revise' || a.kind === 'cancel'}
          <label>Reason (optional, up to {REASON_MAX} characters)
            <input type="text" bind:value={a.form.reason} data-testid="frq-in-reason" />
          </label>
        {/if}
        <button class="btn btn-small" onclick={() => review(entry)}>Review</button>
        <button class="btn btn-small" onclick={closeAction}>Close</button>
      {:else}
        <p data-testid="frq-confirm-text">{effectText(entry, a)}</p>
        <button class="btn btn-small" disabled={a.busy} onclick={() => confirm(entry)}>Confirm</button>
        {#if a.busy}
          <span class="hint" data-testid="frq-writing">Sending...</span>
          <button class="btn btn-small" onclick={stopWaiting}>Stop waiting</button>
        {:else}
          <button class="btn btn-small" onclick={closeAction}>Do not send</button>
        {/if}
      {/if}
      {#if a.error !== ''}
        <div class="result err" data-testid="frq-write-error">{a.error}</div>
      {/if}
    </div>
  {/if}
{/snippet}

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
    <button class="btn btn-small" onclick={load}>Refresh</button>
    {#if loaded}
      <span data-testid="frq-age">fetched {formatAge(ageOf(fetchedAtMs))}</span>
    {/if}
  </div>
  {#if !loaded && unavailable}
    <p class="hint" data-testid="frq-unavailable">
      Flow reservation requests are not available on this server yet.
    </p>
  {:else if !loaded && failed}
    <div class="result err" data-testid="frq-error">Could not load requests: {reason(error)}</div>
  {:else if !loaded}
    <p class="hint" data-testid="frq-loading">Loading requests...</p>
  {:else}
    {#if failed}
      <div class="result err" data-testid="frq-stale">
        Could not refresh requests: {reason(error)}. Showing the queue fetched {formatAge(ageOf(fetchedAtMs))} (stale).
      </div>
    {/if}
    {#if writeNote !== ''}
      <div class="hint" data-testid="frq-write-note">{writeNote}</div>
    {/if}
    {#if views.every((v) => v.queue !== null && v.queue.requests.length === 0)}
      <p class="hint" data-testid="frq-empty">No flow reservation requests.</p>
    {/if}
    {#each views as view (view.lfdi)}
      {#if view.error !== null}
        <div class="result err" data-testid="frq-section-error">
          Could not load requests for <span class="mono">{view.lfdi.substring(0, 16)}...</span>: {reason(view.error)}.
          {#if view.queue !== null}Showing the queue fetched {formatAge(ageOf(view.fetchedAtMs))} (stale).{/if}
        </div>
      {/if}
      {#if view.queue !== null && view.queue.requests.length > 0}
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
                  {#if entry.requestCancelled === true}
                    <div class="hint" data-testid="frq-cancel-requested">cancel requested, grant still active</div>
                  {/if}
                  {#if entry.state === 'pending'}
                    <div data-testid="frq-countdown">
                      {formatCountdown(entry.deadlineAt, view.serverNow, ageOf(view.fetchedAtMs))}
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
                  {#if entry.state === 'pending' || entry.state === 'overdue'}
                    <div class="actions">
                      <button class="btn btn-small" disabled={action?.busy === true} onclick={() => openAction(entry, 'grant_as_asked')}>Grant as asked</button>
                      <button class="btn btn-small" disabled={action?.busy === true} onclick={() => openAction(entry, 'grant_adjusted')}>Grant adjusted</button>
                      <button class="btn btn-small" disabled={action?.busy === true} onclick={() => openAction(entry, 'deny')}>Deny</button>
                    </div>
                  {:else if entry.state === 'granted'}
                    <div class="actions">
                      <button class="btn btn-small" disabled={action?.busy === true} onclick={() => openAction(entry, 'revise')}>Revise</button>
                      <button class="btn btn-small" disabled={action?.busy === true} onclick={() => openAction(entry, 'cancel')}>Cancel grant</button>
                    </div>
                  {/if}
                  {@render actionPanel(entry)}
                </td>
              </tr>
            {/each}
          </tbody>
        </table>
      {/if}
    {/each}
  {/if}
</div>

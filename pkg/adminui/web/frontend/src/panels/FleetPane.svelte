<script lang="ts">
  // GET /api/derms/fleets (#715): one row per aggregator, its fleet size,
  // status counts and the two additive sums this pane shows (#671's first
  // criterion). Each fleet's current commitments come from
  // GET /api/derms/commitments (#801), one read per fleet.
  //
  // This panel computes no commitment or sign of its own: the export-
  // positive convention in directionWord is a label for the sign the
  // server already applied (admin_fleet.go's considerMeasurement), and
  // sumFigure's 'none' branch is the same "never a filled value" trap the
  // server's own FleetSum contract states, read off the counts the server
  // returned rather than re-derived.
  import { onDestroy, onMount } from 'svelte'
  import { fetchJSON } from '../lib/api'
  import { formatInterval, formatQuantity } from '../lib/flowreservation'
  import { grantDirectionWord, isCommitments, type Commitments } from '../lib/commitments'
  import {
    directionWord,
    formatAge,
    formatContributionNote,
    formatValue,
    newestAvailReadingTime,
    newestPReadingTime,
    sumFigure,
    unreportedStatusCount,
    isFleet,
    type Fleet,
  } from '../lib/fleet'

  // How often the displayed reading age re-derives itself against the wall
  // clock, so a page left open does not freeze "updated 10s ago" forever
  // while the underlying reading keeps aging (PR 730 round 1, finding 4).
  const AGE_TICK_MS = 15_000

  // A fetch that never settles would leave the pane on "Loading" with
  // Refresh disabled; past this the request is aborted into the error state.
  const FETCH_TIMEOUT_MS = 15_000

  let fleets = $state<Fleet[]>([])
  let status = $state<'loading' | 'ready' | 'error'>('loading')
  let error = $state('')
  let nowSeconds = $state(Math.floor(Date.now() / 1000))

  // requestSeq is the sequence guard PR 730 round 1 asked for: each load()
  // claims the next number, and only the call still holding the CURRENT
  // number when its response lands may write status/fleets/error. An older
  // request that resolves after a newer one, success or failure, is
  // dropped instead of overwriting the newer result. The abort controller
  // does the same for a response that lands after the component is gone,
  // and also cancels that request on the wire.
  let requestSeq = 0
  const lifetime = new AbortController()

  async function load() {
    const seq = ++requestSeq
    status = 'loading'
    const res = await fetchJSON<Fleet[]>('/api/derms/fleets', {
      signal: lifetime.signal,
      timeoutMs: FETCH_TIMEOUT_MS,
    })
    if (lifetime.signal.aborted || seq !== requestSeq) return
    if (!res.ok) {
      status = 'error'
      error = res.error
      return
    }
    // fetchJSON's decode only checks res.ok before handing back whatever
    // the body parsed to; a 200 with a null body, a non-array, or an array
    // holding a non-fleet element is not a shape this route sends, but
    // nothing upstream guarantees it, so this is the last place to catch it
    // before the render below throws.
    if (!Array.isArray(res.data) || !res.data.every(isFleet)) {
      status = 'error'
      error = 'server returned an unexpected response shape'
      return
    }
    fleets = res.data
    status = 'ready'
    commitments = Object.fromEntries(res.data.map((f) => [f.aggregatorLFDI, { kind: 'loading' } as CommitmentsState]))
    for (const f of res.data) loadCommitments(f.aggregatorLFDI, seq)
  }

  // One entry per fleet. A failed read is its own state, so the render can
  // never fall through to "none" for a fleet whose commitments are unknown.
  type CommitmentsState =
    | { kind: 'loading' }
    | { kind: 'ready'; data: Commitments }
    | { kind: 'error'; message: string }
  let commitments = $state<Record<string, CommitmentsState>>({})

  async function loadCommitments(lfdi: string, seq: number) {
    const res = await fetchJSON<unknown>(`/api/derms/commitments?aggregatorLFDI=${encodeURIComponent(lfdi)}`, {
      signal: lifetime.signal,
      timeoutMs: FETCH_TIMEOUT_MS,
    })
    if (lifetime.signal.aborted || seq !== requestSeq) return
    if (!res.ok) {
      const message = res.status === 404 ? 'fleet not found on the server' : res.error
      commitments[lfdi] = { kind: 'error', message }
    } else if (!isCommitments(res.data)) {
      commitments[lfdi] = { kind: 'error', message: 'server returned an unexpected response shape' }
    } else {
      commitments[lfdi] = { kind: 'ready', data: res.data }
    }
  }

  let ageTimer: ReturnType<typeof setInterval> | undefined

  onMount(() => {
    load()
    ageTimer = setInterval(() => {
      nowSeconds = Math.floor(Date.now() / 1000)
    }, AGE_TICK_MS)
  })

  onDestroy(() => {
    lifetime.abort()
    if (ageTimer !== undefined) clearInterval(ageTimer)
  })
</script>

<div class="card full-width">
  <h2>DERMS Fleets</h2>
  <div class="hint">
    <button class="btn btn-small" onclick={load} disabled={status === 'loading'}>Refresh</button>
  </div>
  {#if status === 'loading'}
    <p class="hint" data-testid="fleet-loading">Loading fleets...</p>
  {:else if status === 'error'}
    <div class="result err" data-testid="fleet-error">Could not load fleets: {error}</div>
  {:else if fleets.length === 0}
    <p class="hint" data-testid="fleet-empty">No aggregator fleets are registered.</p>
  {:else}
    <table>
      <thead>
        <tr>
          <th>Aggregator</th>
          <th>Devices</th>
          <th>Connected</th>
          <th>Alarmed</th>
          <th>Stale</th>
          <th>Unreported</th>
          <th>Measured power</th>
          <th>Available capacity</th>
          <th>Current commitments</th>
        </tr>
      </thead>
      <tbody>
        {#each fleets as fleet (fleet.aggregatorLFDI)}
          {@const power = sumFigure(fleet, fleet.rollup.p, newestPReadingTime(fleet), nowSeconds)}
          {@const availAge = newestAvailReadingTime(fleet)}
          {@const activeAvail = sumFigure(fleet, fleet.rollup.statWAvail, availAge, nowSeconds)}
          {@const reactiveAvail = sumFigure(fleet, fleet.rollup.statVarAvail, availAge, nowSeconds)}
          {@const held = commitments[fleet.aggregatorLFDI]}
          {@const neitherAvail = activeAvail.kind === 'none' && reactiveAvail.kind === 'none'}
          <tr data-testid="fleet-row">
            <td class="mono" title={fleet.aggregatorLFDI}>{fleet.aggregatorLFDI.substring(0, 16)}...</td>
            <td>{fleet.rollup.deviceCount}</td>
            <td>{fleet.rollup.connected}</td>
            <td>{fleet.rollup.alarmed}</td>
            <td>{fleet.rollup.stale}</td>
            <td>{unreportedStatusCount(fleet)}</td>
            <td data-testid="fleet-power">
              {#if power.kind === 'none'}
                No devices reporting
              {:else}
                {formatValue(power.value)} W{power.directionKnown && directionWord(power.value) ? ` ${directionWord(power.value)}` : ''}
                {#if !power.directionKnown}
                  <span class="hint" data-testid="fleet-power-direction-unknown">direction unknown</span>
                {/if}
                <div class="hint">
                  updated {formatAge(power.ageSeconds)}{formatContributionNote(power)}
                </div>
              {/if}
              {#if power.kind !== 'none'}
                <div class="hint" data-testid="fleet-power-source">Source: mirror readings (server time)</div>
              {/if}
            </td>
            <td data-testid="fleet-avail">
              <div data-testid="fleet-avail-active">
                {#if activeAvail.kind === 'reporting'}
                  {formatValue(activeAvail.value)} W active{formatContributionNote(activeAvail)}
                {:else}
                  No devices reporting{neitherAvail ? ' active' : ''}{formatContributionNote(activeAvail)}
                {/if}
              </div>
              <div data-testid="fleet-avail-reactive">
                {#if reactiveAvail.kind === 'reporting'}
                  {formatValue(reactiveAvail.value)} VAR reactive{formatContributionNote(reactiveAvail)}
                {:else}
                  No devices reporting{neitherAvail ? ' reactive' : ''}{formatContributionNote(reactiveAvail)}
                {/if}
              </div>
              <div class="hint">updated {formatAge(activeAvail.ageSeconds)}</div>
              <div class="hint" data-testid="fleet-avail-source">Source: device reports (device clock)</div>
            </td>
            <td data-testid="fleet-commitments">
              {#if held === undefined || held.kind === 'loading'}
                <span class="hint" data-testid="fleet-commitments-loading">Loading commitments...</span>
              {:else if held.kind === 'error'}
                <div class="result err" data-testid="fleet-commitments-error">Could not read commitments: {held.message}</div>
              {:else if held.data.grants.length === 0 && held.data.plainControls.length === 0}
                <span data-testid="fleet-commitments-none">none</span>
              {:else}
                {#each held.data.grants as g (g.mRID)}
                  <div data-testid="fleet-grant">
                    Grant {formatInterval(g.window)}: {grantDirectionWord(g.direction)},
                    {g.powerW === null ? 'power not sent' : formatQuantity(g.powerW, 'W')},
                    {g.energyRemainingWh === null ? 'energy left not sent' : `${formatQuantity(g.energyRemainingWh, 'Wh')} left`}
                  </div>
                {/each}
                {#each held.data.plainControls as c (c.mRID)}
                  <div data-testid="fleet-control">
                    Control {formatInterval(c.window)}: target
                    {c.targetW === null ? 'not sent' : `${formatQuantity(c.targetW, 'W')} (discharge positive)`}
                  </div>
                {/each}
              {/if}
              <div class="hint" data-testid="fleet-commitments-source">Source: this server's records of grants and controls</div>
            </td>
          </tr>
        {/each}
      </tbody>
    </table>
    <p class="hint" data-testid="fleet-counts-source">
      Source: device status reports (DERStatus, device clock). Connected, alarmed, stale and unreported count devices by those reports.
    </p>
  {/if}
</div>

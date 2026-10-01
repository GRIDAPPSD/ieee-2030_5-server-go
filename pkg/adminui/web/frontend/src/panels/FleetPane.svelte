<script lang="ts">
  // GET /api/derms/fleets (#715): one row per aggregator, its fleet size,
  // status counts and the two additive sums this pane shows (#671's first
  // criterion). The reservation queue and the dispatch pane need routes
  // that do not exist yet, so current commitments are not rendered here.
  //
  // This panel computes no commitment or sign of its own: the export-
  // positive convention in directionWord is a label for the sign the
  // server already applied (admin_fleet.go's considerMeasurement), and
  // sumFigure's 'none' branch is the same "never a filled value" trap the
  // server's own FleetSum contract states, read off the counts the server
  // returned rather than re-derived.
  import { onDestroy, onMount } from 'svelte'
  import { fetchJSON } from '../lib/api'
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
        </tr>
      </thead>
      <tbody>
        {#each fleets as fleet (fleet.aggregatorLFDI)}
          {@const power = sumFigure(fleet, fleet.rollup.p, newestPReadingTime(fleet), nowSeconds)}
          {@const availAge = newestAvailReadingTime(fleet)}
          {@const activeAvail = sumFigure(fleet, fleet.rollup.statWAvail, availAge, nowSeconds)}
          {@const reactiveAvail = sumFigure(fleet, fleet.rollup.statVarAvail, availAge, nowSeconds)}
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
                {formatValue(power.value)} W{directionWord(power.value) ? ` ${directionWord(power.value)}` : ''}
                <div class="hint">
                  updated {formatAge(power.ageSeconds)}{formatContributionNote(power)}
                </div>
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
            </td>
          </tr>
        {/each}
      </tbody>
    </table>
  {/if}
</div>

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
  import { onMount } from 'svelte'
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
    type Fleet,
  } from '../lib/fleet'

  let fleets = $state<Fleet[]>([])
  let status = $state<'loading' | 'ready' | 'error'>('loading')
  let error = $state('')

  async function load() {
    status = 'loading'
    const res = await fetchJSON<Fleet[]>('/api/derms/fleets')
    if (!res.ok) {
      status = 'error'
      error = res.error
      return
    }
    fleets = res.data
    status = 'ready'
  }

  onMount(load)
</script>

<div class="card full-width">
  <h2>DERMS Fleets</h2>
  <div class="hint">
    <button class="btn btn-small" onclick={load}>Refresh</button>
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
          {@const now = Math.floor(Date.now() / 1000)}
          {@const power = sumFigure(fleet, fleet.rollup.p, newestPReadingTime(fleet), now)}
          {@const availAge = newestAvailReadingTime(fleet)}
          {@const activeAvail = sumFigure(fleet, fleet.rollup.statWAvail, availAge, now)}
          {@const reactiveAvail = sumFigure(fleet, fleet.rollup.statVarAvail, availAge, now)}
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
              {#if activeAvail.kind === 'none' && reactiveAvail.kind === 'none'}
                No devices reporting
              {:else}
                {#if activeAvail.kind === 'reporting'}
                  <div>{formatValue(activeAvail.value)} W active{formatContributionNote(activeAvail)}</div>
                {/if}
                {#if reactiveAvail.kind === 'reporting'}
                  <div>{formatValue(reactiveAvail.value)} VAR reactive{formatContributionNote(reactiveAvail)}</div>
                {/if}
                <div class="hint">updated {formatAge(activeAvail.ageSeconds)}</div>
              {/if}
            </td>
          </tr>
        {/each}
      </tbody>
    </table>
  {/if}
</div>

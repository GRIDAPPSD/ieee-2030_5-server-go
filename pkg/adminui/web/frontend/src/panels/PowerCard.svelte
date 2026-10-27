<script lang="ts">
  // The Overview power card. The effect owns the poll timer and the in-flight
  // read, and its teardown runs when the scope or window changes or the card
  // is destroyed, so leaving the tab stops polling. The last answer per scope
  // and window lives in lib/power, not here, so a return draws at once.
  import { fetchJSON } from '../lib/api'
  import { PANEL_REQUEST_TIMEOUT_MS, SIGN_IN_AGAIN } from '../lib/descriptor'
  import { deviceLabel } from '../lib/devices'
  import {
    ALL_SCOPE,
    DIRECT_KEY,
    POWER_WINDOWS,
    aggregatorBars,
    aggregatorLabels,
    cachedPower,
    energyBars,
    energyTotals,
    formatClock,
    formatKw,
    formatKwh,
    keepPower,
    lastPowerChoice,
    parsePower,
    powerBucketMs,
    powerPath,
    windowRange,
    rememberPowerChoice,
    type PowerAnswer,
  } from '../lib/power'
  import { startPoll } from '../lib/poll'
  import DescriptorChart from './DescriptorChart.svelte'
  import EventsTimeline from './EventsTimeline.svelte'
  import { nowPercent } from '../lib/controlEvents'

  // Every aggregator and the DERs without one are drawn together, so the
  // card always asks for the whole fleet.
  const scope = ALL_SCOPE
  const start = lastPowerChoice()
  let windowSeconds = $state(start.window)

  let answer = $state<PowerAnswer | null>(null)
  // When the shown answer arrived; null before any answer.
  let answerAt = $state<number | null>(null)
  // True once a read in this mount has succeeded for the shown scope and
  // window, so a cached answer is marked until the first fresh one lands.
  let fresh = $state(false)
  let error = $state('')

  function describe(status: number, message: string): string {
    switch (status) {
      case 401:
        return SIGN_IN_AGAIN
      default:
        return `Could not load the power card: ${message}`
    }
  }

  // The one time range and window every chart on the card shares. A second
  // chart under the energy chart takes this same range, so the two axes match.
  // The events timeline reports how far ahead of now it reaches, and every
  // chart then ends there; 0 on a server with no look-ahead.
  let aheadSeconds = $state(0)
  const range = $derived(windowRange(answerAt ?? Date.now(), windowSeconds, aheadSeconds))
  const nowAt = $derived(nowPercent(answerAt ?? Date.now(), range[0], range[1]))

  // The per-aggregator bars when the server sends them, else the total.
  const bars = $derived.by(() => {
    if (answer === null) return null
    const per = answer.aggregatorEnergy
    return per !== null && per.length > 0 ? aggregatorBars(per, answer.energyChart.unit, deviceLabel) : energyBars(answer.energyChart)
  })


  $effect(() => {
    const sc = scope
    const win = windowSeconds
    rememberPowerChoice(sc, win)

    const kept = cachedPower(sc, win)
    answer = kept?.answer ?? null
    answerAt = kept?.at ?? null
    fresh = false
    error = ''

    return startPoll(async (signal) => {
      const res = await fetchJSON<unknown>(powerPath(sc, win), { signal, timeoutMs: PANEL_REQUEST_TIMEOUT_MS })
      if (signal.aborted) return undefined
      const parsed = res.ok ? parsePower(res.data) : null
      if (parsed !== null) {
        const at = Date.now()
        keepPower(sc, win, { answer: parsed, at })
        answer = parsed
        answerAt = at
        fresh = true
        error = ''
        return parsed.bucketSeconds * 1000
      }
      // A failed or unreadable refresh keeps the last chart, marked with its time.
      fresh = false
      error = res.ok ? 'The server sent a power reply that could not be read.' : describe(res.status, res.error)
      return powerBucketMs(win)
    })
  })

  const totals = $derived(answer === null ? null : energyTotals(answer.energyChart))

  // The totals line says something only once the window holds energy; before
  // that it stays for assistive tools and the tests, out of sight.
  const hasEnergy = $derived(totals !== null && (totals.discharged !== 0 || totals.charged !== 0))

  const flagged = $derived(
    (answer?.devices ?? [])
      .filter((d) => d.directionUnknown || d.concurrentMirrors || d.nonDERWatts)
      .map((d) => {
        const why = [
          d.directionUnknown ? 'direction unknown' : '',
          d.concurrentMirrors ? 'concurrent mirrors' : '',
          d.nonDERWatts ? 'watts on a non-DER mirror' : '',
        ].filter((s) => s !== '')
        return { lfdi: d.lfdi, why: why.join(', ') }
      }),
  )
</script>

<!-- A device shows by name; its LFDI is the hover text. -->
{#snippet nameList(lfdis: string[])}{#each lfdis as lfdi, i (i)}{i > 0 ? ', ' : ''}<span title={lfdi}>{deviceLabel(lfdi)}</span>{/each}{/snippet}

<div class="card full-width power-card" data-testid="power-card">
  <div class="power-head">
    <h2>Power</h2>
    <div class="power-pickers">
      <label for="powerWindow">Window</label>
      <select id="powerWindow" bind:value={windowSeconds}>
        {#each POWER_WINDOWS as w (w.seconds)}
          <option value={w.seconds}>{w.label}</option>
        {/each}
      </select>
    </div>
    {#if answer !== null && answerAt !== null}
      <div class="power-now">
        <span class="stat-label">Power now</span>
        {#if answer.latestW !== null}
          <span class="big-number" data-testid="power-now">{formatKw(answer.latestW)}</span>
        {:else if answer.latestMissing.length > 0}
          <span data-testid="power-now-missing">Unavailable, no recent reading from {@render nameList(answer.latestMissing)}</span>
        {:else}
          <span data-testid="power-now-none">No reading yet</span>
        {/if}
        {#if answer.aggregatorEnergy !== null && answer.aggregatorEnergy.length > 0}
          <ul class="power-by-aggregator" data-testid="power-by-aggregator">
            {#each answer.aggregatorEnergy as e, i (e.key)}
              <li title={e.key === DIRECT_KEY ? 'DERs with no aggregator' : e.key}>
                <span class="stat-label">{aggregatorLabels(answer.aggregatorEnergy, deviceLabel)[i]}</span>
                {#if e.latestW !== null}{formatKw(e.latestW)}{:else if e.latestMissing.length > 0}unavailable{:else}no reading{/if}
              </li>
            {/each}
          </ul>
        {/if}
      </div>
    {/if}
  </div>

  {#if error}
    <div class="result err" role="alert" data-testid="power-error">{error}</div>
  {/if}

  {#if answer !== null && answerAt !== null}
    {#if !fresh}
      <p class="hint" data-testid="power-stale">Showing the answer from {formatClock(answerAt)}.</p>
    {/if}
    <!-- Charts stack here, each given the same range. -->
    <div class="power-charts">
      <div class="power-chart" title="Energy per bucket in kWh: discharge above zero, charge below.">
        <DescriptorChart body={bars ?? energyBars(answer.energyChart)} empty="No energy readings in this window yet." frame {range} />
        {#if aheadSeconds > 0 && nowAt !== null}
          <!-- The plot area's margins match DescriptorChart's grid. -->
          <div class="chart-ahead" aria-hidden="true" data-testid="chart-ahead">
            <div class="chart-future" style:left="{nowAt}%"></div>
            <div class="chart-now" data-testid="chart-now" style:left="{nowAt}%"></div>
          </div>
        {/if}
      </div>
      <EventsTimeline {answer} {range} {windowSeconds} nowMs={answerAt ?? Date.now()} bind:ahead={aheadSeconds} />
    </div>
    <div class="power-foot">
      {#if totals !== null}
        <span class="hint" class:visually-hidden={!hasEnergy} data-testid="power-energy-totals">
          This window: discharged {formatKwh(totals.discharged)}, charged {formatKwh(Math.abs(totals.charged))}
        </span>
      {/if}
      {#if answer.notReporting.length > 0}
        <span class="hint" data-testid="power-not-reporting">Not reporting: {@render nameList(answer.notReporting)}</span>
      {/if}
      {#if flagged.length > 0}
        <span class="hint" data-testid="power-flagged">Flagged: {#each flagged as f, i (i)}{i > 0 ? '; ' : ''}<span title={f.lfdi}>{deviceLabel(f.lfdi)}</span> ({f.why}){/each}</span>
      {/if}
    </div>
  {:else if !error}
    <p class="hint" data-testid="power-loading">Loading...</p>
  {/if}
</div>

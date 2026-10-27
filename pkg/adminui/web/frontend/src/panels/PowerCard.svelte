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
    POWER_WINDOWS,
    cachedPower,
    energyBars,
    energyTotals,
    formatKw,
    formatKwh,
    keepPower,
    lastPowerChoice,
    parsePower,
    powerBucketMs,
    powerPath,
    rememberPowerChoice,
    type PowerAnswer,
  } from '../lib/power'
  import { startPoll } from '../lib/poll'
  import DescriptorChart from './DescriptorChart.svelte'

  const start = lastPowerChoice()
  let scope = $state(start.scope)
  let windowSeconds = $state(start.window)

  let answer = $state<PowerAnswer | null>(null)
  // When the shown answer arrived; null before any answer.
  let answerAt = $state<number | null>(null)
  // True once a read in this mount has succeeded for the shown scope and
  // window, so a cached answer is marked until the first fresh one lands.
  let fresh = $state(false)
  let error = $state('')
  // Kept for the scope dropdown so it does not shrink to nothing while a
  // different scope is loading.
  let aggregators = $state<string[]>([])

  function describe(status: number, message: string): string {
    switch (status) {
      case 401:
        return SIGN_IN_AGAIN
      case 404:
        return 'That aggregator manages no devices.'
      default:
        return `Could not load the power card: ${message}`
    }
  }

  const time = (ms: number) => new Date(ms).toLocaleTimeString()

  $effect(() => {
    const sc = scope
    const win = windowSeconds
    rememberPowerChoice(sc, win)

    const kept = cachedPower(sc, win)
    answer = kept?.answer ?? null
    answerAt = kept?.at ?? null
    fresh = false
    error = ''
    if (kept) aggregators = kept.answer.aggregators

    return startPoll(async (signal) => {
      const res = await fetchJSON<unknown>(powerPath(sc, win), { signal, timeoutMs: PANEL_REQUEST_TIMEOUT_MS })
      if (signal.aborted) return undefined
      const parsed = res.ok ? parsePower(res.data) : null
      if (parsed !== null) {
        const at = Date.now()
        keepPower(sc, win, { answer: parsed, at })
        answer = parsed
        answerAt = at
        aggregators = parsed.aggregators
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

  const names = (lfdis: string[]) => lfdis.map((l) => deviceLabel(l)).join(', ')

  const flagged = $derived(
    (answer?.devices ?? [])
      .filter((d) => d.directionUnknown || d.concurrentMirrors || d.nonDERWatts)
      .map((d) => {
        const why = [
          d.directionUnknown ? 'direction unknown' : '',
          d.concurrentMirrors ? 'concurrent mirrors' : '',
          d.nonDERWatts ? 'watts on a non-DER mirror' : '',
        ].filter((s) => s !== '')
        return `${deviceLabel(d.lfdi)} (${why.join(', ')})`
      }),
  )
</script>

<div class="card full-width" data-testid="power-card">
  <h2>Power</h2>
  <div class="form-row">
    <label for="powerScope">Devices</label>
    <select id="powerScope" bind:value={scope}>
      <option value={ALL_SCOPE}>All devices</option>
      {#each aggregators as lfdi (lfdi)}
        <option value={lfdi}>Aggregator {deviceLabel(lfdi)}</option>
      {/each}
    </select>
    <label for="powerWindow">Window</label>
    <select id="powerWindow" bind:value={windowSeconds}>
      {#each POWER_WINDOWS as w (w.seconds)}
        <option value={w.seconds}>{w.label}</option>
      {/each}
    </select>
  </div>

  {#if error}
    <div class="result err" role="alert" data-testid="power-error">{error}</div>
  {/if}

  {#if answer !== null && answerAt !== null}
    <div class="stat-row">
      <span class="stat-label">Power now</span>
      {#if answer.latestW !== null}
        <span class="big-number" data-testid="power-now">{formatKw(answer.latestW)}</span>
      {:else if answer.latestMissing.length > 0}
        <span data-testid="power-now-missing">Unavailable, no recent reading from {names(answer.latestMissing)}</span>
      {:else}
        <span data-testid="power-now-none">No reading yet</span>
      {/if}
    </div>
    {#if !fresh}
      <p class="hint" data-testid="power-stale">Showing the answer from {time(answerAt)}.</p>
    {/if}
    <p class="hint">Energy per bucket in kWh: discharge above zero, charge below.</p>
    <DescriptorChart body={energyBars(answer.energyChart)} empty="No energy readings in this window yet." />
    {#if totals !== null}
      <p class="hint" data-testid="power-energy-totals">
        This window: discharged {formatKwh(totals.discharged)}, charged {formatKwh(totals.charged)}
      </p>
    {/if}
    {#if answer.notReporting.length > 0}
      <p class="hint" data-testid="power-not-reporting">Not reporting: {names(answer.notReporting)}</p>
    {/if}
    {#if flagged.length > 0}
      <p class="hint" data-testid="power-flagged">Flagged: {flagged.join('; ')}</p>
    {/if}
  {:else if !error}
    <p class="hint" data-testid="power-loading">Loading...</p>
  {/if}
</div>

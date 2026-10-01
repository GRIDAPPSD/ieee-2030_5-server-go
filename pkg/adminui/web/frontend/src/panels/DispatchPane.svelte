<script lang="ts">
  // Dispatch a targetW control to an aggregator fleet, either executing a live
  // grant or as a plain dispatch. Nothing is sent before Confirm; the server
  // decides every conflict and the page shows its answer as given. An outcome
  // the page cannot know (no answer, a 5xx) closes the confirm step and locks
  // sending until the controls list has been read again, so the same write is
  // never sent twice on a guess.
  import { onDestroy, onMount } from 'svelte'
  import { fetchJSON } from '../lib/api'
  import { fmtTime, type DERControlListItem, type DERControlListResponse, type DERProgramListResponse, type DERProgramView } from '../lib/dercontrol'
  import {
    buildDispatch,
    fleetDeviceChoices,
    grantPrefill,
    parseGrants,
    submitDispatch,
    type DeviceChoice,
    type GrantView,
  } from '../lib/dispatch'
  import { formatAge, isFleet, type Fleet } from '../lib/fleet'
  import { directionLabel, formatInterval, formatQuantity, scaledNumber } from '../lib/flowreservation'
  import { deviceIdFromHref } from '../lib/fsa'
  import type { DashboardDevice } from '../lib/dashboard'

  let { devices }: { devices: DashboardDevice[] } = $props()

  const TICK_MS = 1000
  const FETCH_TIMEOUT_MS = 15_000
  const WRITE_TIMEOUT_MS = 20_000

  let fleets = $state<Fleet[]>([])
  let fleetsLoaded = $state(false)
  let fleetsError = $state('')
  let fleetLFDI = $state('')
  let mode = $state<'grant' | 'plain'>('grant')

  let grants = $state<GrantView[]>([])
  let grantsLoaded = $state(false)
  let grantsError = $state('')
  let grantsServerNow = $state<number | null>(null)
  let grantsFetchedAt = $state(0)
  let grantResponseId = $state('')

  let deviceId = $state('')
  let programs = $state<DERProgramView[]>([])
  let programsError = $state('')
  let programHref = $state('')

  let start = $state('')
  let duration = $state('')
  let power = $state('')

  let controls = $state<DERControlListItem[]>([])
  let controlsLoaded = $state(false)
  let controlsError = $state('')
  let controlsFetchedAt = $state(0)

  let confirming = $state(false)
  let busy = $state(false)
  let formError = $state('')
  let result = $state('')
  let resultOk = $state(false)
  let createdMRID = $state('')
  let unknownNote = $state('')
  let nowMs = $state(Date.now())

  let fleetsSeq = 0
  let grantsSeq = 0
  let programsSeq = 0
  let controlsSeq = 0
  let writeSeq = 0
  let writeCtrl: AbortController | null = null
  const lifetime = new AbortController()
  let tickTimer: ReturnType<typeof setInterval> | undefined
  const bounds = { signal: lifetime.signal, timeoutMs: FETCH_TIMEOUT_MS }

  const fleet = $derived(fleets.find((f) => f.aggregatorLFDI === fleetLFDI) ?? null)
  const grant = $derived(grants.find((g) => g.response.id === grantResponseId) ?? null)
  const choices = $derived.by((): DeviceChoice[] => {
    const own = fleet === null ? [] : fleetDeviceChoices(fleet.devices, devices, deviceIdFromHref)
    if (grant !== null && !own.some((c) => c.id === grant.edevId)) {
      return [...own, { id: grant.edevId, label: 'device ' + grant.edevId }]
    }
    return own
  })
  const program = $derived(programs.find((p) => p.href === programHref) ?? null)
  const locked = $derived(busy || unknownNote !== '')

  function ageOf(sinceMs: number): number {
    return Math.max(0, Math.floor((nowMs - sinceMs) / 1000))
  }

  function short(lfdi: string): string {
    return lfdi.substring(0, 16) + '...'
  }

  async function loadFleets() {
    const seq = ++fleetsSeq
    const res = await fetchJSON<unknown>('/api/derms/fleets', bounds)
    if (lifetime.signal.aborted || seq !== fleetsSeq) return
    fleetsLoaded = true
    if (!res.ok) {
      fleetsError = res.error
      return
    }
    if (!Array.isArray(res.data) || !res.data.every(isFleet)) {
      fleetsError = 'server returned an unexpected fleet list'
      return
    }
    fleetsError = ''
    fleets = res.data
  }

  async function loadGrants() {
    const seq = ++grantsSeq
    if (fleetLFDI === '' || mode !== 'grant') return
    const res = await fetchJSON<unknown>(
      '/api/derms/grants?live=true&aggregatorLFDI=' + encodeURIComponent(fleetLFDI),
      bounds,
    )
    if (lifetime.signal.aborted || seq !== grantsSeq) return
    grantsLoaded = true
    if (!res.ok) {
      grantsError = res.status === 404 ? 'Grants are not available on this server yet.' : res.error
      grants = []
      return
    }
    const parsed = parseGrants(res.data)
    if ('error' in parsed) {
      grantsError = 'server returned an unexpected response shape: ' + parsed.error
      grants = []
      return
    }
    grantsError = ''
    grants = parsed.list.grants
    grantsServerNow = parsed.list.now
    grantsFetchedAt = Date.now()
    nowMs = grantsFetchedAt
    if (grantResponseId !== '' && !grants.some((g) => g.response.id === grantResponseId)) {
      grantResponseId = ''
    }
  }

  async function loadPrograms(id: string) {
    const seq = ++programsSeq
    programs = []
    programHref = ''
    programsError = ''
    controls = []
    controlsLoaded = false
    if (id === '') return
    const res = await fetchJSON<DERProgramListResponse>(`/api/devices/${encodeURIComponent(id)}/der-programs`, bounds)
    if (lifetime.signal.aborted || seq !== programsSeq) return
    if (!res.ok) {
      programsError = res.error
      return
    }
    programs = res.data.programs ?? []
    if (programs.length === 1) {
      programHref = programs[0].href
      void loadControls()
    }
  }

  async function loadControls() {
    const seq = ++controlsSeq
    if (deviceId === '' || programHref === '') return
    const res = await fetchJSON<DERControlListResponse>(
      `/api/der/controls?device=${encodeURIComponent(deviceId)}&derProgramHref=${encodeURIComponent(programHref)}`,
      bounds,
    )
    if (lifetime.signal.aborted || seq !== controlsSeq) return
    if (!res.ok) {
      controlsError = res.error
      return
    }
    controlsError = ''
    controls = res.data.controls ?? []
    controlsLoaded = true
    controlsFetchedAt = Date.now()
    nowMs = controlsFetchedAt
    unknownNote = ''
  }

  onMount(() => {
    void loadFleets()
    tickTimer = setInterval(() => {
      nowMs = Date.now()
    }, TICK_MS)
  })

  onDestroy(() => {
    lifetime.abort()
    writeCtrl?.abort()
    if (tickTimer !== undefined) clearInterval(tickTimer)
  })

  function resetInputs() {
    confirming = false
    formError = ''
    result = ''
    grantResponseId = ''
    start = ''
    duration = ''
    power = ''
    deviceId = ''
    void loadPrograms('')
  }

  function setFleet(lfdi: string) {
    if (locked) return
    fleetLFDI = lfdi
    grants = []
    grantsLoaded = false
    grantsError = ''
    resetInputs()
    void loadGrants()
  }

  function setMode(next: 'grant' | 'plain') {
    if (locked) return
    mode = next
    grants = []
    grantsLoaded = false
    grantsError = ''
    resetInputs()
    void loadGrants()
  }

  function setGrant(id: string) {
    if (locked) return
    grantResponseId = id
    confirming = false
    formError = ''
    const g = grants.find((x) => x.response.id === id)
    if (g === undefined) return
    const fill = grantPrefill(g)
    start = fill.start
    duration = fill.duration
    power = fill.power
    deviceId = g.edevId
    void loadPrograms(g.edevId)
  }

  function setDevice(id: string) {
    if (locked) return
    deviceId = id
    confirming = false
    void loadPrograms(id)
  }

  function setProgram(href: string) {
    if (locked) return
    programHref = href
    confirming = false
    controls = []
    controlsLoaded = false
    void loadControls()
  }

  function formValues() {
    return { programHref, start, duration, power, grantMRID: mode === 'grant' && grant !== null ? grant.response.mRID : '' }
  }

  function review() {
    if (locked) return
    if (fleet === null) {
      formError = 'Pick a fleet first.'
      return
    }
    if (mode === 'grant' && grant === null) {
      formError = 'Pick a live grant, or choose a plain dispatch.'
      return
    }
    const built = buildDispatch(formValues())
    if (!built.ok) {
      formError = built.error
      return
    }
    formError = ''
    result = ''
    confirming = true
  }

  function focusOnMount(el: HTMLElement) {
    el.focus()
  }

  function confirmText(): string {
    const built = buildDispatch(formValues())
    if (!built.ok || fleet === null) return ''
    const b = built.body
    const when = b.startTime === undefined ? 'starting now' : formatInterval({ start: b.startTime, duration: b.durationSeconds })
    const carrying =
      grant === null
        ? 'A plain dispatch, carrying out no grant.'
        : `Carrying out grant ${grant.response.mRID} (request ${grant.frqId}, ${directionLabel(grant.response.direction)}).`
    const watts = b.targetW.value * Math.pow(10, b.targetW.multiplier)
    return (
      `Send a control to fleet ${short(fleet.aggregatorLFDI)} through program ${program?.description || program?.mRID || programHref}: ` +
      `target power ${formatQuantity(watts, 'W')} (DER frame: positive discharges, negative charges), ` +
      `${when}${b.startTime === undefined ? ` for ${b.durationSeconds} s` : ''}. ${carrying}`
    )
  }

  function backToInputs() {
    if (busy) return
    confirming = false
  }

  function markUnknown(note: string) {
    unknownNote = note
    result = note
    resultOk = false
    void loadControls()
    void loadGrants()
  }

  async function confirm() {
    if (locked || !confirming) return
    const built = buildDispatch(formValues())
    if (!built.ok) {
      formError = built.error
      confirming = false
      return
    }
    const seq = ++writeSeq
    const ctrl = new AbortController()
    writeCtrl = ctrl
    busy = true
    const res = await submitDispatch(built.body, { signal: ctrl.signal, timeoutMs: WRITE_TIMEOUT_MS })
    if (lifetime.signal.aborted || seq !== writeSeq) return
    writeCtrl = null
    busy = false
    confirming = false
    if (res.ok) {
      const c = res.control
      createdMRID = c.mRID
      resultOk = true
      result =
        `Stored control ${c.mRID}: ${formatInterval(c.interval)}, status ${c.eventStatus.status} (as of ${fmtTime(c.eventStatus.dateTime)}).` +
        (c.supersedes.length > 0 ? ` Superseded ${c.supersedes.length} earlier control${c.supersedes.length === 1 ? '' : 's'}.` : '') +
        (c.persisted ? '' : ' A server restart forgets this control.')
      void loadControls()
      void loadGrants()
      return
    }
    if (res.kind === 'refused') {
      resultOk = false
      result = res.message
      return
    }
    markUnknown(res.message)
  }

  function stopWaiting() {
    writeSeq++
    writeCtrl?.abort()
    writeCtrl = null
    busy = false
    confirming = false
    markUnknown('Stopped waiting. The server may or may not have stored the control. Reloading the controls list.')
  }

  function targetText(c: DERControlListItem): string {
    const t = c.derControlBase.opModTargetW
    if (t === undefined) return '-'
    return formatQuantity(scaledNumber(t), 'W') + ' (DER frame)'
  }

  function grantLabel(g: GrantView): string {
    return `request ${g.frqId} on device ${g.edevId}: ${formatInterval(g.response.interval)}, ${directionLabel(g.response.direction)}`
  }
</script>

<div class="card full-width" data-testid="dispatch-pane">
  <h2>Dispatch</h2>

  {#if !fleetsLoaded}
    <p class="hint" data-testid="dispatch-loading">Loading fleets...</p>
  {:else if fleetsError}
    <div class="result err" data-testid="dispatch-fleets-error">Could not load fleets: {fleetsError}</div>
  {:else if fleets.length === 0}
    <p class="hint" data-testid="dispatch-no-fleets">No aggregator fleets.</p>
  {:else}
    <div class="form-row">
      <label>Fleet
        <select
          data-testid="dispatch-fleet"
          value={fleetLFDI}
          disabled={locked}
          onchange={(e) => setFleet(e.currentTarget.value)}
        >
          <option value="">(pick fleet)</option>
          {#each fleets as f (f.aggregatorLFDI)}
            <option value={f.aggregatorLFDI}>{short(f.aggregatorLFDI)} ({f.rollup.deviceCount} devices)</option>
          {/each}
        </select>
      </label>
      <label>
        <input type="radio" name="dispatch-mode" checked={mode === 'grant'} disabled={locked} onchange={() => setMode('grant')} />
        Execute a live grant
      </label>
      <label>
        <input type="radio" name="dispatch-mode" checked={mode === 'plain'} disabled={locked} onchange={() => setMode('plain')} />
        Plain dispatch
      </label>
    </div>

    {#if fleet !== null && mode === 'grant'}
      {#if grantsError}
        <div class="result err" data-testid="dispatch-grants-error">Could not load grants: {grantsError}</div>
      {:else if !grantsLoaded}
        <p class="hint" data-testid="dispatch-grants-loading">Loading grants...</p>
      {:else if grants.length === 0}
        <p class="hint" data-testid="dispatch-no-grants">This fleet has no live grants to execute.</p>
      {:else}
        <div class="form-row">
          <label>Live grant
            <select
              data-testid="dispatch-grant"
              value={grantResponseId}
              disabled={locked}
              onchange={(e) => setGrant(e.currentTarget.value)}
            >
              <option value="">(pick grant)</option>
              {#each grants as g (g.response.id)}
                <option value={g.response.id}>{grantLabel(g)}</option>
              {/each}
            </select>
          </label>
        </div>
        <p class="hint" data-testid="dispatch-grants-age">
          Source: live grants read from the server{grantsServerNow === null ? '' : ` (server time ${fmtTime(grantsServerNow)})`},
          fetched {formatAge(ageOf(grantsFetchedAt))}.
        </p>
      {/if}
      {#if grant !== null}
        <div class="response tip" data-testid="dispatch-grant-detail">
          <div>
            Grant {grant.response.mRID}: {formatInterval(grant.response.interval)}, {directionLabel(grant.response.direction)},
            power {formatQuantity(scaledNumber(grant.response.powerAvailable), 'W')},
            energy {formatQuantity(scaledNumber(grant.response.energyAvailable), 'Wh')}
          </div>
          <div data-testid="dispatch-energy-left">
            Energy left in this grant: {formatQuantity(grant.response.energyRemainingWh, 'Wh')}
            (committed {formatQuantity(grant.response.energyCommittedWh, 'Wh')}), {grant.response.executions.length}
            control{grant.response.executions.length === 1 ? '' : 's'} carrying it out.
          </div>
          <div class="hint" data-testid="dispatch-suggested">
            Suggested target {formatQuantity(scaledNumber(grant.suggestedTargetW), 'W')} (DER frame: positive discharges, negative charges), from the server.
          </div>
        </div>
      {/if}
    {/if}

    {#if fleet !== null && (mode === 'plain' || grant !== null)}
      {#if !confirming}
        <div class="form-row">
          <label>Device
            <select data-testid="dispatch-device" value={deviceId} disabled={locked} onchange={(e) => setDevice(e.currentTarget.value)}>
              <option value="">(pick device)</option>
              {#each choices as c (c.id)}
                <option value={c.id}>{c.label}</option>
              {/each}
            </select>
          </label>
          <label>Program
            <select
              data-testid="dispatch-program"
              value={programHref}
              disabled={locked || deviceId === ''}
              onchange={(e) => setProgram(e.currentTarget.value)}
            >
              <option value="">(pick DER program)</option>
              {#each programs as p (p.href)}
                <option value={p.href}>{p.description || p.mRID}</option>
              {/each}
            </select>
          </label>
        </div>
        {#if programsError}
          <div class="result err" data-testid="dispatch-programs-error">{programsError}</div>
        {/if}
        <div class="form-row">
          <label>Start (UTC, empty to start now)
            <input type="text" data-testid="dispatch-start" bind:value={start} disabled={locked} />
          </label>
          <label>Duration (s)
            <input type="text" inputmode="numeric" data-testid="dispatch-duration" bind:value={duration} disabled={locked} />
          </label>
          <label>Target power (W, DER frame: positive discharges, negative charges)
            <input type="text" inputmode="numeric" data-testid="dispatch-power" bind:value={power} disabled={locked} />
          </label>
        </div>
        <div class="form-row">
          <button class="btn btn-green" disabled={locked} onclick={review}>Review</button>
        </div>
      {:else}
        <div class="action-panel" data-testid="dispatch-confirm">
          <p data-testid="dispatch-confirm-text" tabindex="-1" {@attach focusOnMount}>{confirmText()}</p>
          <button class="btn btn-green" disabled={busy} onclick={confirm}>Confirm</button>
          {#if busy}
            <span class="hint" data-testid="dispatch-sending">Sending...</span>
            <button class="btn btn-small" onclick={stopWaiting}>Stop waiting</button>
          {:else}
            <button class="btn btn-small" onclick={backToInputs}>Back</button>
          {/if}
        </div>
      {/if}
      {#if formError}
        <div class="result err" role="alert" data-testid="dispatch-form-error">{formError}</div>
      {/if}
    {/if}

    {#if result}
      <div class="result" class:ok={resultOk} class:err={!resultOk} role="alert" data-testid="dispatch-result">{result}</div>
    {/if}

    {#if programHref !== ''}
      <h3>Controls on this program</h3>
      {#if controlsError}
        <div class="result err" data-testid="dispatch-controls-error">
          Could not load controls: {controlsError}.
          <button class="btn btn-small" onclick={() => void loadControls()}>Reload</button>
        </div>
      {/if}
      {#if controlsLoaded}
        <p class="hint" data-testid="dispatch-controls-age">Source: controls list read from the server, fetched {formatAge(ageOf(controlsFetchedAt))}.</p>
      {/if}
      <table>
        <thead>
          <tr>
            <th>Control</th>
            <th>Target</th>
            <th>Interval</th>
            <th>Status</th>
            <th>Grant carried out</th>
            <th>Responses (reported by device, not verified by server)</th>
          </tr>
        </thead>
        <tbody>
          {#each controls as c (c.mRID)}
            <tr data-testid="dispatch-control-row" data-mrid={c.mRID} data-created={c.mRID === createdMRID}>
              <td class="mono">{c.mRID}</td>
              <td>{c.type === 'targetW' ? targetText(c) : c.type}</td>
              <td>{formatInterval(c.interval)}</td>
              <td>{c.eventStatus.status}</td>
              <td class="mono" data-testid="dispatch-control-grant">{c.executesGrant ?? 'none (plain dispatch)'}</td>
              <td data-testid="dispatch-control-responses">
                {c.responses.total} reported
                {#if c.responses.total > 0}
                  ({Object.entries(c.responses.byStatus)
                    .map(([status, count]) => `${status}: ${count}`)
                    .join(', ')})
                {/if}
              </td>
            </tr>
          {:else}
            <tr><td colspan="6" class="stat-label">{controlsLoaded ? 'No admin-issued controls for this program' : 'Loading controls...'}</td></tr>
          {/each}
        </tbody>
      </table>
      <p class="hint" data-testid="dispatch-delivery-note">
        Metered delivery: the server does not expose it yet, so none is shown.
      </p>
    {/if}
  {/if}
</div>

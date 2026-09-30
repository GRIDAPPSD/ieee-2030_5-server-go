<script lang="ts">
  // Send DER Control: collect a control, show a one-sentence summary on
  // Send, and post only when Confirm is clicked (acceptance criterion 2).
  // The result area always reports what the server stored, never a claim
  // of delivery (criterion 3): devices pull this list, so "stored" is the
  // only thing the server can promise at response time.
  import { fetchJSON, postJSON } from '../lib/api'
  import { deviceIdFromHref } from '../lib/fsa'
  import type { DashboardDevice } from '../lib/dashboard'
  import {
    buildControlRequest,
    canCancel,
    controlValueText,
    expectedDeviceReadTime,
    fmtTime,
    keptControl,
    POLL_RATE_SECONDS,
    type ControlFormInput,
    type ControlRequestBody,
    type ControlType,
    type DERControlCreated,
    type DERControlListItem,
    type DERControlListResponse,
    type DERControlView,
    type DERProgramListResponse,
    type DERProgramView,
  } from '../lib/dercontrol'

  let { devices }: { devices: DashboardDevice[] } = $props()

  let selectedDeviceId = $state('')
  let programs = $state<DERProgramView[]>([])
  let programsChecked = $state(false)
  let programsError = $state('')
  let selectedProgramHref = $state('')

  let controlType = $state<ControlType>('connect')
  let controlValue = $state<number | null>(null)
  let excitation = $state<'false' | 'true'>('false')
  let startNow = $state(true)
  let startAt = $state('')
  let durationMinutes = $state<number | null>(null)
  let description = $state('')

  let pendingBody = $state<ControlRequestBody | null>(null)
  let pendingSummary = $state('')
  let posting = $state(false)
  let result = $state('')
  let ok = $state(false)

  let controls = $state<DERControlListItem[]>([])
  let controlsError = $state('')
  // Wall clock at the last successful controls read; it decides which
  // Active rows have ended, so it is not re-read on every render.
  let nowSeconds = $state(Math.floor(Date.now() / 1000))
  let cancelTarget = $state<string | null>(null)
  let cancelReason = $state('')
  let cancelPosting = $state(false)
  let cancelResult = $state('')

  // programSeq/controlSeq are sequence guards: a device or program switch
  // mid-flight must not let a stale response overwrite a newer selection,
  // the same pattern FleetPane's load() uses for its own requestSeq.
  let programSeq = 0
  let controlSeq = 0

  // A pending confirmation, a shown result and an open cancel belong to the
  // selection they were made under, so any change of selection drops them.
  function dropSelectionState() {
    pendingBody = null
    pendingSummary = ''
    result = ''
    cancelTarget = null
    cancelResult = ''
  }

  $effect(() => {
    const id = selectedDeviceId
    programs = []
    programsChecked = false
    programsError = ''
    selectedProgramHref = ''
    dropSelectionState()
    const seq = ++programSeq
    if (!id) return
    void loadPrograms(id, seq)
  })

  $effect(() => {
    const href = selectedProgramHref
    controls = []
    controlsError = ''
    dropSelectionState()
    const seq = ++controlSeq
    if (!href) return
    void loadControls(selectedDeviceId, href, seq)
  })

  async function loadPrograms(deviceId: string, seq: number) {
    const res = await fetchJSON<DERProgramListResponse>(`/api/devices/${encodeURIComponent(deviceId)}/der-programs`)
    if (seq !== programSeq) return
    programsChecked = true
    if (!res.ok) {
      programsError = res.error
      programs = []
      return
    }
    programsError = ''
    programs = res.data.programs ?? []
  }

  async function loadControls(deviceId: string, href: string, seq: number) {
    const res = await fetchJSON<DERControlListResponse>(
      `/api/der/controls?device=${encodeURIComponent(deviceId)}&derProgramHref=${encodeURIComponent(href)}`,
    )
    if (seq !== controlSeq) return
    if (!res.ok) {
      controlsError = res.error
      controls = []
      return
    }
    controlsError = ''
    nowSeconds = Math.floor(Date.now() / 1000)
    controls = res.data.controls ?? []
  }

  function reloadControls() {
    if (selectedProgramHref) void loadControls(selectedDeviceId, selectedProgramHref, ++controlSeq)
  }

  function formInput(): ControlFormInput {
    const device = devices.find((d) => deviceIdFromHref(d.href) === selectedDeviceId)
    const program = programs.find((p) => p.href === selectedProgramHref)
    return {
      deviceLabel: device?.sfdi ?? selectedDeviceId,
      programLabel: program ? program.description || program.mRID : selectedProgramHref,
      programHref: selectedProgramHref,
      type: controlType,
      value: controlValue,
      excitation: excitation === 'true',
      startNow,
      startAtLocal: startAt,
      durationMinutes,
      description,
    }
  }

  // send only builds and shows the pending request; it never posts. Only
  // confirmSend below calls postJSON, so a re-render or a repeated Send
  // click cannot issue a second request on its own.
  function send() {
    const built = buildControlRequest(formInput())
    if (!built.ok) {
      ok = false
      result = built.error
      pendingBody = null
      pendingSummary = ''
      return
    }
    pendingBody = built.body
    pendingSummary = built.summary
    result = ''
  }

  function editPending() {
    pendingBody = null
    pendingSummary = ''
  }

  function keptText(kept: { mRID: string; href: string }): string {
    return ` The control was kept and is live: ${kept.mRID} (${kept.href}). Find it in the table and cancel it if it is not wanted.`
  }

  async function confirmSend() {
    if (!pendingBody || posting) return
    posting = true
    const body = pendingBody
    pendingBody = null
    pendingSummary = ''
    const res = await postJSON<DERControlCreated>('/api/der/controls', body)
    posting = false
    if (!res.ok) {
      ok = false
      const kept = keptControl(res.body)
      result = `Error: ${res.error}` + (kept ? keptText(kept) : '')
      reloadControls()
      return
    }
    ok = true
    const d = res.data
    let text =
      `Stored ${d.mRID} at ${fmtTime(d.creationTime)}; starts ${fmtTime(d.interval.start)}. ` +
      `Status: ${d.eventStatus.status} (as of ${fmtTime(d.eventStatus.dateTime)}). ` +
      `Nominal estimate only, not a delivery: a device polling every ${POLL_RATE_SECONDS} s would read it by ` +
      `${expectedDeviceReadTime(d.creationTime)} (stored time + ${POLL_RATE_SECONDS} s, computed here).`
    if (d.supersedes.length > 0) {
      text += ` Superseded ${d.supersedes.length} earlier control${d.supersedes.length === 1 ? '' : 's'}.`
    }
    if (!d.persisted) {
      text += ' A server restart forgets this control.'
    }
    result = text
    reloadControls()
  }

  function startCancel(mrid: string) {
    cancelTarget = mrid
    cancelReason = ''
    cancelResult = ''
  }

  function abortCancel() {
    cancelTarget = null
  }

  async function confirmCancel(mrid: string) {
    if (cancelPosting) return
    cancelPosting = true
    const body: { reason?: string } = {}
    if (cancelReason) body.reason = cancelReason
    const res = await postJSON<DERControlView>(`/api/der/controls/${encodeURIComponent(mrid)}/cancel`, body)
    cancelPosting = false
    cancelTarget = null
    if (!res.ok) {
      const kept = keptControl(res.body)
      cancelResult = kept ? res.error + keptText(kept) : `Cancel failed: ${res.error}`
    } else {
      cancelResult = ''
    }
    reloadControls()
  }
</script>

<div class="card full-width">
  <h2>Send DER Control</h2>

  <div class="form-row">
    <select id="controlDevice" bind:value={selectedDeviceId}>
      <option value="">(pick device)</option>
      {#each devices as device (device.href)}
        <option value={deviceIdFromHref(device.href)}>{device.sfdi}</option>
      {/each}
    </select>
    <select id="controlProgram" bind:value={selectedProgramHref} disabled={!selectedDeviceId}>
      <option value="">(pick DER program)</option>
      {#each programs as program (program.href)}
        <option value={program.href}>{program.description || program.mRID}</option>
      {/each}
    </select>
  </div>

  {#if programsError}
    <div class="result err" data-testid="der-control-programs-error">{programsError}</div>
  {:else if selectedDeviceId && programsChecked && programs.length === 0}
    <p class="hint" data-testid="der-control-no-programs">
      This device has no DER programs. Programs come from the boot fixture; they cannot be created in the admin UI.
    </p>
  {/if}

  <div class="form-row">
    <select id="controlType" bind:value={controlType}>
      <option value="connect">Connect</option>
      <option value="disconnect">Disconnect</option>
      <option value="maxLimW">Max Power Limit (% of setMaxW)</option>
      <option value="fixedPFInjectW">Fixed Power Factor</option>
    </select>
    {#if controlType === 'maxLimW'}
      <input
        type="number"
        id="controlValue"
        placeholder="Percent (0-100)"
        min="0"
        max="100"
        step="0.01"
        style="max-width: 120px;"
        bind:value={controlValue}
      />
    {:else if controlType === 'fixedPFInjectW'}
      <input
        type="number"
        id="controlValue"
        placeholder="Power factor (0.001-1.000)"
        min="0.001"
        max="1"
        step="0.001"
        style="max-width: 120px;"
        bind:value={controlValue}
      />
      <select id="controlExcitation" bind:value={excitation}>
        <option value="false">Over-excited: DER injects reactive power</option>
        <option value="true">Under-excited: DER absorbs reactive power</option>
      </select>
    {:else}
      <input type="number" id="controlValue" style="display: none;" bind:value={controlValue} />
    {/if}
  </div>

  <div class="form-row">
    <label>
      <input type="checkbox" id="controlStartNow" bind:checked={startNow} /> Start now
    </label>
    {#if !startNow}
      <input type="datetime-local" id="controlStartAt" step="1" bind:value={startAt} />
    {/if}
    <input
      type="number"
      id="controlDuration"
      placeholder="Duration (minutes)"
      min="1"
      style="max-width: 120px;"
      bind:value={durationMinutes}
    />
    <input type="text" id="controlDescription" placeholder="Description (optional)" bind:value={description} />
  </div>

  {#if pendingSummary}
    <div class="form-row" data-testid="der-control-confirm">
      <span>{pendingSummary}</span>
      <button class="btn btn-green" onclick={confirmSend} disabled={posting}>Confirm</button>
      <button class="btn btn-small" onclick={editPending} disabled={posting}>Edit</button>
    </div>
  {:else}
    <div class="form-row">
      <button class="btn btn-green" onclick={send}>Send</button>
    </div>
  {/if}

  <div class="result" class:ok class:err={result !== '' && !ok} id="controlResult">{result}</div>

  {#if selectedProgramHref}
    <table id="derControlsTable">
      <thead>
        <tr>
          <th>Type</th>
          <th>Value</th>
          <th>Start</th>
          <th>End</th>
          <th>Status</th>
          <th>Responses (reported by device, not verified by server)</th>
          <th></th>
        </tr>
      </thead>
      <tbody>
        {#if controlsError}
          <tr>
            <td colspan="7" class="result err" data-testid="der-control-controls-error">{controlsError}</td>
          </tr>
        {:else if controls.length === 0}
          <tr><td colspan="7" class="stat-label">No admin-issued controls for this program</td></tr>
        {:else}
          {#each controls as ctrl (ctrl.mRID)}
            <tr data-testid="der-control-row" data-mrid={ctrl.mRID}>
              <td>{ctrl.type}</td>
              <td>{controlValueText(ctrl)}</td>
              <td>{fmtTime(ctrl.interval.start)}</td>
              <td>{fmtTime(ctrl.interval.start + ctrl.interval.duration)}</td>
              <td data-testid="der-control-status">{ctrl.eventStatus.status}</td>
              <td>
                {ctrl.responses.total} reported
                {#if ctrl.responses.total > 0}
                  ({Object.entries(ctrl.responses.byStatus)
                    .map(([status, count]) => `${status}: ${count}`)
                    .join(', ')})
                {/if}
              </td>
              <td>
                {#if canCancel(ctrl.eventStatus.status, ctrl.interval.start, ctrl.interval.duration, nowSeconds)}
                  {#if cancelTarget === ctrl.mRID}
                    <input
                      type="text"
                      placeholder="reason (optional)"
                      style="max-width: 100px;"
                      bind:value={cancelReason}
                    />
                    <button
                      class="btn btn-red btn-small"
                      data-testid="der-control-confirm-cancel-{ctrl.mRID}"
                      disabled={cancelPosting}
                      onclick={() => confirmCancel(ctrl.mRID)}>Confirm Cancel</button
                    >
                    <button class="btn btn-small" disabled={cancelPosting} onclick={abortCancel}>Back</button>
                  {:else}
                    <button
                      class="btn btn-red btn-small"
                      data-testid="der-control-cancel-{ctrl.mRID}"
                      onclick={() => startCancel(ctrl.mRID)}>Cancel</button
                    >
                  {/if}
                {/if}
              </td>
            </tr>
          {/each}
        {/if}
      </tbody>
    </table>
    {#if cancelResult}
      <div class="result err" data-testid="der-control-cancel-result">{cancelResult}</div>
    {/if}
  {/if}
</div>

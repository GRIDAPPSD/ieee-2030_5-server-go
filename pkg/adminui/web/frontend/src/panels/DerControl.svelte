<script lang="ts">
  // Send DER Control: pick a device, choose a default or a scheduled control,
  // tick functions and fill their values, then Send shows a one-sentence
  // summary and only Confirm posts. The form is built from the control
  // function catalog by value kind, so this file names no function. The result
  // area reports what the server stored, never a claim of delivery: devices
  // pull their control list, so "stored" is all the server can promise.
  import { onMount } from 'svelte'
  import FunctionField from './FunctionField.svelte'
  import TabLink from './TabLink.svelte'
  import { requestPrograms } from '../lib/programsRequest'
  import { fetchJSON, postJSON } from '../lib/api'
  import { deviceIdFromHref } from '../lib/fsa'
  import type { DashboardDevice } from '../lib/dashboard'
  import {
    buildDeviceControl,
    buildFunctions,
    functionsText,
    initialValue,
    primacyText,
    type CatalogResponse,
    type CatalogRow,
    type CurveInfo,
    type DeviceControlBody,
    type CurveListResponse,
    type FieldValue,
  } from '../lib/controlcatalog'
  import {
    canCancel,
    expectedDeviceReadTime,
    fmtTime,
    keptControl,
    POLL_RATE_SECONDS,
    type DERControlListItem,
    type DERControlListResponse,
    type DERControlView,
    type DERProgramListResponse,
    type DERProgramView,
    type DeviceControlCreated,
  } from '../lib/dercontrol'

  let { devices }: { devices: DashboardDevice[] } = $props()

  let catalog = $state<CatalogRow[]>([])
  let catalogError = $state('')
  let curves = $state<CurveInfo[]>([])
  let curvesError = $state('')

  let selectedDeviceId = $state('')
  let programs = $state<DERProgramView[]>([])
  let programsChecked = $state(false)
  let programsError = $state('')
  // The table shows the controls of one program, or of all when blank.
  let programFilter = $state('')

  let mode = $state<'scheduled' | 'default'>('scheduled')
  let ticked = $state<Record<string, boolean>>({})
  let values = $state<Record<string, FieldValue>>({})
  let startNow = $state(true)
  let startAt = $state('')
  let durationMinutes = $state<number | null>(null)
  let description = $state('')

  let pendingBody = $state<DeviceControlBody | null>(null)
  let pendingSummary = $state('')
  let posting = $state(false)
  let result = $state('')
  let primacyLine = $state('')
  let ok = $state(false)

  // The selected device's stored default, read from the server on a device
  // switch and set from the reply of a default send. programHref is null when
  // it came from the read, which does not say which program holds it.
  let storedDefault = $state<{ deviceId: string; programHref: string | null; type: string; base: Record<string, unknown> } | null>(null)
  let defaultError = $state('')

  let controls = $state<DERControlListItem[]>([])
  let controlsError = $state('')
  // The server's clock at the last successful controls read (its Date
  // header), which decides which Active rows have ended; null when the
  // response carried none.
  let serverNow = $state<number | null>(null)
  let cancelTarget = $state<string | null>(null)
  let cancelReason = $state('')
  let cancelPosting = $state(false)
  let cancelResult = $state('')

  // programSeq/controlSeq are sequence guards: a device switch mid-flight
  // must not let a stale response overwrite a newer selection.
  let programSeq = 0
  let controlSeq = 0
  // selectionSeq advances on every device change. A create or cancel
  // remembers the value it was sent under; a reply that finds it changed is
  // shown named with its own device, not as the new selection's.
  let selectionSeq = 0
  let defaultSeq = 0
  // Cleared on unmount so a reply that lands afterwards writes nothing.
  let mounted = true

  onMount(() => {
    void loadCatalog()
    void loadCurves()
    return () => {
      mounted = false
    }
  })

  async function loadCatalog() {
    const res = await fetchJSON<CatalogResponse>('/api/der/control-functions')
    if (!mounted) return
    if (!res.ok) {
      catalogError = res.error
      return
    }
    catalogError = ''
    catalog = res.data.functions ?? []
    for (const row of catalog) {
      ticked[row.name] = false
      values[row.name] = initialValue(row)
    }
  }

  async function loadCurves() {
    const res = await fetchJSON<CurveListResponse>('/api/der/curves')
    if (!mounted) return
    if (!res.ok) {
      curvesError = `Stored curves could not be read: ${res.error}`
      return
    }
    curvesError = ''
    curves = res.data.curves ?? []
  }

  // A pending confirmation, a shown result and an open cancel belong to the
  // selection they were made under, so any change of device drops them.
  function dropSelectionState() {
    selectionSeq++
    pendingBody = null
    pendingSummary = ''
    result = ''
    primacyLine = ''
    cancelTarget = null
    cancelResult = ''
  }

  $effect(() => {
    const id = selectedDeviceId
    programs = []
    programsChecked = false
    programsError = ''
    programFilter = ''
    controls = []
    controlsError = ''
    dropSelectionState()
    const pseq = ++programSeq
    const cseq = ++controlSeq
    const dseq = ++defaultSeq
    storedDefault = null
    defaultError = ''
    if (!id) return
    void loadPrograms(id, pseq)
    void loadControls(id, cseq)
    void loadDefault(id, dseq)
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

  async function loadDefault(deviceId: string, seq: number) {
    const res = await fetchJSON<{ mRID: string; type: string; derControlBase: Record<string, unknown> }>(
      `/api/devices/${encodeURIComponent(deviceId)}/default-control`,
    )
    if (seq !== defaultSeq) return
    if (!res.ok) {
      // 404 is the answer for a device with no default: nothing to show.
      if (res.status !== 404) defaultError = `The stored default could not be read: ${res.error}`
      return
    }
    storedDefault = { deviceId, programHref: null, type: res.data.type, base: res.data.derControlBase }
  }

  async function loadControls(deviceId: string, seq: number) {
    const res = await fetchJSON<DERControlListResponse>(`/api/der/controls?device=${encodeURIComponent(deviceId)}`)
    if (seq !== controlSeq) return
    if (!res.ok) {
      controlsError = res.error
      controls = []
      return
    }
    controlsError = ''
    serverNow = res.serverTime === undefined ? null : Math.floor(res.serverTime / 1000)
    controls = res.data.controls ?? []
  }

  function reload() {
    if (!selectedDeviceId) return
    void loadPrograms(selectedDeviceId, ++programSeq)
    void loadControls(selectedDeviceId, ++controlSeq)
  }

  function deviceLabel(): string {
    const device = devices.find((d) => deviceIdFromHref(d.href) === selectedDeviceId)
    return device?.sfdi ?? selectedDeviceId
  }

  // send only builds and shows the pending request; it never posts. Only
  // confirmSend below calls postJSON, so a re-render or a repeated Send
  // click cannot issue a second request on its own.
  function send() {
    const fns = buildFunctions(catalog, ticked, values)
    const built = fns.ok
      ? buildDeviceControl({
          mode,
          deviceLabel: deviceLabel(),
          functions: fns.functions,
          startNow,
          startAtLocal: startAt,
          durationMinutes,
          description,
        })
      : fns
    if (!selectedDeviceId && built.ok) {
      ok = false
      result = 'Pick a device first.'
      return
    }
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
    primacyLine = ''
  }

  function editPending() {
    pendingBody = null
    pendingSummary = ''
  }

  // For a create 500, the server says the control may be live. stale means
  // the table now on screen belongs to another device.
  function keptCreateText(kept: { mRID: string; href: string }, stale: boolean): string {
    const where = stale ? 'Select that device to see it in the table' : 'Cancel it from the table if it is not wanted'
    return `The control was kept and may be live: ${kept.mRID} (${kept.href}). ${where}.`
  }

  // For a cancel 500, only what the server says: the cancellation may or may
  // not have taken, so the control still exists and its status is unknown.
  function keptCancelText(kept: { mRID: string; href: string }, stale: boolean): string {
    const where = stale ? 'Select that device to see its status' : 'The reloaded table shows its status'
    return `The control ${kept.mRID} (${kept.href}) still exists. ${where}.`
  }

  async function confirmSend() {
    if (!pendingBody || posting) return
    posting = true
    const body = pendingBody
    const deviceId = selectedDeviceId
    const sentUnder = selectionSeq
    const target = `device ${deviceLabel()}`
    pendingBody = null
    pendingSummary = ''
    const res = await postJSON<DeviceControlCreated>(`/api/devices/${encodeURIComponent(deviceId)}/controls`, body)
    posting = false
    const stale = sentUnder !== selectionSeq
    const prefix = stale ? `For ${target}: ` : ''
    if (!res.ok) {
      ok = false
      const kept = keptControl(res.body)
      result = `${prefix}Error: ${res.error}` + (kept ? `. ${keptCreateText(kept, stale)}` : '')
      primacyLine = ''
      reload()
      return
    }
    ok = true
    const d = res.data
    primacyLine = prefix + primacyText(d.primacy, d.primacyReason)
    if (d.control) {
      const c = d.control
      let text =
        prefix +
        `Stored ${c.mRID} at ${fmtTime(c.creationTime)}; starts ${fmtTime(c.interval.start)}. ` +
        `Status: ${c.eventStatus.status} (as of ${fmtTime(c.eventStatus.dateTime)}). ` +
        `Nominal estimate only, not a delivery: a device polling every ${POLL_RATE_SECONDS} s would read it by ` +
        `${expectedDeviceReadTime(c.creationTime)} (stored time + ${POLL_RATE_SECONDS} s, computed here).`
      if (c.supersedes.length > 0) {
        text += ` Superseded ${c.supersedes.length} earlier control${c.supersedes.length === 1 ? '' : 's'}.`
      }
      if (!c.persisted) {
        text += ' A server restart forgets this control.'
      }
      result = text
    } else if (d.defaultControl) {
      result = `${prefix}Stored the default control ${d.defaultControl.mRID} on program ${d.programHref}. It is in force now, has no end, and devices send no responses.`
      if (!stale) {
        defaultSeq++
        defaultError = ''
        storedDefault = { deviceId, programHref: d.programHref, type: d.defaultControl.type, base: d.defaultControl.derControlBase }
      }
    } else {
      result = `${prefix}Stored on program ${d.programHref}.`
    }
    if (!stale) programFilter = ''
    reload()
  }

  function startCancel(mrid: string) {
    cancelTarget = mrid
    cancelReason = ''
    cancelResult = ''
  }

  function abortCancel() {
    cancelTarget = null
  }

  function onFilterChange() {
    cancelTarget = null
    cancelResult = ''
  }

  async function confirmCancel(mrid: string) {
    if (cancelPosting) return
    cancelPosting = true
    const sentUnder = selectionSeq
    const target = `device ${deviceLabel()}`
    const body: { reason?: string } = {}
    if (cancelReason) body.reason = cancelReason
    const res = await postJSON<DERControlView>(`/api/der/controls/${encodeURIComponent(mrid)}/cancel`, body)
    cancelPosting = false
    cancelTarget = null
    const stale = sentUnder !== selectionSeq
    if (!res.ok) {
      const kept = keptControl(res.body)
      const text = kept ? `${res.error}. ${keptCancelText(kept, stale)}` : `Cancel failed: ${res.error}`
      if (stale) {
        // The table and its cancel box now belong to another device.
        ok = false
        result = `For ${target}, control ${mrid}: ${text}`
      } else {
        cancelResult = text
      }
    } else if (!stale) {
      cancelResult = ''
    }
    reload()
  }

  const shownControls = $derived(programFilter ? controls.filter((c) => c.derProgramHref === programFilter) : controls)
  const shownDefault = $derived(storedDefault && storedDefault.deviceId === selectedDeviceId ? storedDefault : null)
  const primacyOf = (href: string): string => {
    const p = programs.find((x) => x.href === href)
    return p ? String(p.primacy) : '-'
  }
  const programName = (p: DERProgramView): string => p.description || p.mRID
  const controlCount = (href: string): number => controls.filter((c) => c.derProgramHref === href).length
</script>

<div class="card full-width">
  <h2>Send DER Control</h2>

  <div class="form-row">
    <span class="stat-label">Target</span>
    <select id="controlDevice" bind:value={selectedDeviceId} disabled={posting || cancelPosting}>
      <option value="">(pick device)</option>
      {#each devices as device (device.href)}
        <option value={deviceIdFromHref(device.href)}>{device.sfdi}</option>
      {/each}
    </select>
  </div>

  {#if programsError}
    <div class="result err" data-testid="der-control-programs-error">{programsError}</div>
  {:else if selectedDeviceId && programsChecked && programs.length === 0}
    <p class="hint" data-testid="der-control-no-programs">
      This device has no DER programs yet; sending a control creates one for this device alone. Program templates are
      added on the <TabLink path="/ui/fsas">FSAs tab</TabLink>, and a device's programs are managed on the
      <TabLink path="/ui/devices" testid="der-control-devices-link" onNavigate={() => requestPrograms(selectedDeviceId)}
        >Devices tab</TabLink
      >.
    </p>
  {/if}

  <div class="form-row">
    <label>
      <input type="radio" name="controlMode" value="default" data-testid="control-mode-default" bind:group={mode} />
      Default control: in force now, no end, devices send no responses
    </label>
    <label>
      <input type="radio" name="controlMode" value="scheduled" data-testid="control-mode-scheduled" bind:group={mode} />
      Scheduled control
    </label>
  </div>

  {#if catalogError}
    <div class="result err" data-testid="control-catalog-error">Functions could not be read: {catalogError}</div>
  {/if}
  {#each catalog as row (row.name)}
    <FunctionField {row} bind:ticked={ticked[row.name]} bind:value={values[row.name]} {curves} {curvesError} />
  {/each}

  {#if mode === 'scheduled'}
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
  {/if}

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
  {#if primacyLine}
    <div class="hint" data-testid="der-control-primacy">{primacyLine}</div>
  {/if}

  {#if selectedDeviceId && programs.length > 0}
    <table id="derProgramsTable">
      <thead>
        <tr><th>Program</th><th>Primacy</th><th>Controls</th></tr>
      </thead>
      <tbody>
        {#each programs as program (program.href)}
          <tr data-testid="der-control-program-row">
            <td>{programName(program)}</td>
            <td>{program.primacy}</td>
            <td>{controlCount(program.href)}</td>
          </tr>
        {/each}
      </tbody>
    </table>
    <div class="form-row">
      <select id="controlProgram" bind:value={programFilter} onchange={onFilterChange} disabled={posting || cancelPosting}>
        <option value="">(all programs)</option>
        {#each programs as program (program.href)}
          <option value={program.href}>{programName(program)}</option>
        {/each}
      </select>
    </div>
  {/if}

  {#if defaultError}
    <div class="result err" data-testid="der-control-default-error">{defaultError}</div>
  {/if}

  {#if selectedDeviceId}
    <table id="derControlsTable">
      <thead>
        <tr>
          <th>Type</th>
          <th>Value</th>
          <th>Program (primacy)</th>
          <th>Start</th>
          <th>End</th>
          <th>Status</th>
          <th>Responses (reported by device, not verified by server)</th>
          <th></th>
        </tr>
      </thead>
      <tbody>
        {#if shownDefault}
          <tr data-testid="der-control-default-row">
            <td>default</td>
            <td>{functionsText(shownDefault.base)}</td>
            <td>{shownDefault.programHref ? `${shownDefault.programHref} (${primacyOf(shownDefault.programHref)})` : '-'}</td>
            <td colspan="5" class="stat-label">Stored default; in force now, no end, no responses</td>
          </tr>
        {/if}
        {#if controlsError}
          <tr>
            <td colspan="8" class="result err" data-testid="der-control-controls-error">{controlsError}</td>
          </tr>
        {:else if shownControls.length === 0}
          <tr><td colspan="8" class="stat-label">No admin-issued controls for this selection</td></tr>
        {:else}
          {#each shownControls as ctrl (ctrl.mRID)}
            <tr data-testid="der-control-row" data-mrid={ctrl.mRID}>
              <td>{ctrl.type}</td>
              <td>{functionsText(ctrl.functions)}</td>
              <td>{ctrl.derProgramHref} ({primacyOf(ctrl.derProgramHref)})</td>
              <td>{fmtTime(ctrl.interval.start)}</td>
              <td>{fmtTime(ctrl.interval.start + ctrl.interval.duration)}</td>
              <td data-testid="der-control-status">{ctrl.eventStatus.status}{ctrl.ended && ctrl.eventStatus.status === 'active' ? ' (ended)' : ''}</td>
              <td>
                {ctrl.responses.total} reported
                {#if ctrl.responses.total > 0}
                  ({Object.entries(ctrl.responses.byStatus)
                    .map(([status, count]) => `${status}: ${count}`)
                    .join(', ')})
                {/if}
              </td>
              <td>
                {#if canCancel(ctrl.eventStatus.status, ctrl.interval.start, ctrl.interval.duration, serverNow)}
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

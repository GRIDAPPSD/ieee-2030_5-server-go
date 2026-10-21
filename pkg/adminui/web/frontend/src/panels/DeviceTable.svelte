<script lang="ts">
  // The registered EndDevice list, plus the per-device FSA assignment
  // control. The LFDI cell is truncated for width, as it always has been;
  // the untruncated value is available through Lookup Device by LFDI.
  import { untrack } from 'svelte'
  import { fetchJSON, postJSON } from '../lib/api'
  import { takeProgramsRequest } from '../lib/programsRequest'
  import { deviceIdFromHref, type AdminFSA } from '../lib/fsa'
  import type { DashboardColumn, DashboardDevice } from '../lib/dashboard'
  import { commsClass, commsLabel, formatAge } from '../lib/comms'
  import { deviceConnectionView, deviceInverterView } from '../lib/derstatus'
  import { canon, deviceKind, deviceLabel, deviceName, saveDeviceProfile } from '../lib/devices'
  import { applyPairs, pairFailures } from '../lib/management'
  import ReportedCell from './ReportedCell.svelte'
  import DevicePrograms from './DevicePrograms.svelte'
  import DevicePicker from './DevicePicker.svelte'

  let {
    devices,
    columns: columnList = [],
    fsas,
    onChanged,
  }: {
    devices: DashboardDevice[]
    // Embedder columns, rendered after the reported columns in order.
    columns?: DashboardColumn[]
    fsas: AdminFSA[]
    onChanged: () => void
  } = $props()

  // The each block is keyed by column id, which throws on a repeat, so a
  // repeated id (the server drops them, but the wire is not trusted) keeps its
  // first column only.
  const columns = $derived(columnList.filter((c, i) => columnList.findIndex((o) => o.id === c.id) === i))

  function enabledLabel(enabled: boolean | null): string {
    if (enabled === true) return 'Yes'
    if (enabled === false) return 'No'
    return 'Not set'
  }

  // Template count per FSA mRID, from GET /api/fsas/{id}/der-programs. A
  // refresh keeps the last counts until the new reads land, so labels do not
  // flicker. An FSA whose read failed or never ran has no entry, and the
  // select shows its bare mRID rather than a "no program templates" it has not
  // established.
  let templateCounts = $state<Record<string, number>>({})

  $effect(() => {
    const ids = fsas.map((f) => f.mRID)
    const ctrl = new AbortController()
    for (const id of ids) {
      void fetchJSON<{ templates?: unknown[] | null }>(`/api/fsas/${encodeURIComponent(id)}/der-programs`, {
        signal: ctrl.signal,
      }).then((res) => {
        if (ctrl.signal.aborted) return
        if (!res.ok || !Array.isArray(res.data?.templates)) {
          delete templateCounts[id]
          return
        }
        templateCounts[id] = res.data.templates.length
      })
    }
    return () => ctrl.abort()
  })

  function fsaLabel(mRID: string): string {
    const n = templateCounts[mRID]
    if (n === undefined) return mRID
    if (n === 0) return `${mRID} (no program templates)`
    return `${mRID} (${n} ${n === 1 ? 'program template' : 'program templates'})`
  }

  // The device whose Programs drawer is open. A link from another tab may
  // have asked for one; that request is read once, on mount.
  let programsFor = $state(untrack(() => takeProgramsRequest()))

  let selected = $state<Record<string, string>>({})
  let error = $state('')

  // The device being edited, and its drafts: name, kind and, for an
  // aggregator, the picked devices. One at a time, so a second Edit closes
  // the first without saving it.
  let editingLfdi = $state('')
  let draftName = $state('')
  let draftKind = $state('')
  let initialKind = $state('')
  let picked = $state<string[]>([])
  let held = $state<string[]>([])
  let pickerLoaded = $state(false)
  let renameError = $state('')
  let pairError = $state('')
  let renaming = $state(false)

  function startRename(lfdi: string) {
    editingLfdi = lfdi
    draftName = deviceName(lfdi)
    initialKind = deviceKind(lfdi)
    draftKind = initialKind
    picked = []
    held = []
    pickerLoaded = false
    renameError = ''
    pairError = ''
  }

  function cancelRename() {
    editingLfdi = ''
    renameError = ''
  }

  function label(lfdi: string): string {
    return deviceLabel(lfdi, devices.find((d) => canon(d.lfdi) === canon(lfdi))?.sfdi)
  }

  async function saveRename() {
    if (renaming || editingLfdi === '') return
    if (draftKind === 'aggregator' && !pickerLoaded) return
    const saving = editingLfdi
    renaming = true
    try {
      const res = await saveDeviceProfile(saving, draftName.trim(), draftKind === initialKind ? undefined : draftKind)
      if (!res.ok) {
        renameError = `Save failed (${res.status}): ${res.error}`
        return
      }
      if (draftKind === 'aggregator') {
        const add = picked.filter((l) => !held.includes(l))
        const remove = held.filter((l) => !picked.includes(l))
        const failed = pairFailures(await applyPairs(saving, add, remove), label)
        if (failed.length > 0) pairError = `Pair failed for ${failed.join('; ')}`
        onChanged()
      }
      if (editingLfdi === saving) cancelRename()
    } finally {
      renaming = false
    }
  }

  async function assign(deviceID: string) {
    const fsaHref = selected[deviceID]
    if (!fsaHref) return
    const res = await postJSON<unknown>(`/api/devices/${encodeURIComponent(deviceID)}/fsa-assignment`, {
      fsaHref,
    })
    if (!res.ok) {
      error = `Assign failed (${res.status}): ${res.error}`
      return
    }
    error = ''
    selected[deviceID] = ''
    onChanged()
  }
</script>

<div class="card full-width">
  <h2>End Devices</h2>
  <table>
    <thead>
      <tr><th>Device</th><th>LFDI</th><th>Enabled</th><th>Comms</th><th>Last request</th><th>DER connection (reported)</th><th>Inverter state (reported)</th>{#each columns as col (col.id)}<th>{col.label}{#if col.error}<span style="color: #f59e0b; cursor: help" title={col.error} data-testid="device-column-error-{col.id}" role="img" aria-label="Column failed: {col.error}"> (!)</span>{/if}</th>{/each}<th>Href</th><th>Programs</th><th>Assign FSA</th></tr>
    </thead>
    <tbody id="deviceTable">
      {#if devices.length === 0}
        <tr><td colspan={10 + columns.length} class="stat-label">No devices registered</td></tr>
      {:else}
        {#each devices as device (device.href)}
          {@const deviceID = deviceIdFromHref(device.href)}
          <tr>
            <td data-testid="device-label-cell">
              {#if editingLfdi !== '' && editingLfdi === device.lfdi}
                <input
                  type="text"
                  maxlength="32"
                  aria-label="Name of device {deviceLabel(device.lfdi, device.sfdi)}"
                  data-testid="device-name-input"
                  placeholder="Name (empty clears it)"
                  bind:value={draftName}
                  onkeydown={(e) => {
                    if (e.key === 'Enter') void saveRename()
                    if (e.key === 'Escape' && !renaming) cancelRename()
                  }}
                />
                <select
                  aria-label="Kind of device {deviceLabel(device.lfdi, device.sfdi)}"
                  data-testid="device-kind-select"
                  bind:value={draftKind}
                >
                  {#if initialKind === ''}<option value="">(not set)</option>{/if}
                  <option value="device">Device</option>
                  <option value="aggregator">Aggregator</option>
                </select>
                {#if draftKind === 'aggregator'}
                  <DevicePicker {devices} manager={device.lfdi} bind:selected={picked} bind:held bind:loaded={pickerLoaded} />
                {/if}
                <button
                  class="btn btn-small"
                  disabled={renaming || (draftKind === 'aggregator' && !pickerLoaded)}
                  data-testid="device-name-save"
                  onclick={saveRename}>Save</button>
                <button class="btn btn-small" disabled={renaming} data-testid="device-name-cancel" onclick={cancelRename}>Cancel</button>
              {:else}
                <span class="mono" data-testid="device-label">{deviceLabel(device.lfdi, device.sfdi)}</span>
                <button
                  class="btn btn-small"
                  data-testid="device-name-edit-{deviceID}"
                  aria-label="Edit device {deviceLabel(device.lfdi, device.sfdi)}"
                  disabled={!device.lfdi || renaming} onclick={() => startRename(device.lfdi)}>Edit</button
                >
              {/if}
            </td>
            <td class="mono" data-testid="device-lfdi">{(device.lfdi ?? '').substring(0, 16)}...</td>
            <td class={device.enabled === true ? 'online' : 'offline'} data-testid="device-enabled">{enabledLabel(device.enabled)}</td>
            <td class={commsClass(device.comms)} data-testid="device-comms">{commsLabel(device.comms)}</td>
            <td data-testid="device-last-request">
              {#if device.lastRequest !== null}
                <time datetime={device.lastRequest} title={device.lastRequest}>{formatAge(device.lastRequest, Date.now())}</time>
              {/if}
            </td>
            <ReportedCell view={deviceConnectionView(device)} testid="device-der-connection" errorTestid="device-der-error" />
            <ReportedCell view={deviceInverterView(device)} testid="device-inverter" />
            {#each columns as col (col.id)}
              <td data-testid="device-column-{col.id}">{device.cells && Object.hasOwn(device.cells, col.id) ? device.cells[col.id] : '-'}</td>
            {/each}
            <td class="mono">{device.href}</td>
            <td>
              <button
                class="btn btn-small"
                data-testid="device-programs-toggle-{deviceID}"
                aria-expanded={programsFor === deviceID}
                aria-label="Programs of device {deviceID}"
                onclick={() => (programsFor = programsFor === deviceID ? '' : deviceID)}>Programs</button
              >
            </td>
            <td>
              <select id="assignSel-{deviceID}" bind:value={selected[deviceID]}>
                <option value="">(pick FSA)</option>
                {#each fsas as fsa (fsa.mRID)}
                  <option value={'/api/fsas/' + fsa.mRID}>{fsaLabel(fsa.mRID)}</option>
                {/each}
              </select>
              <button class="btn btn-small" onclick={() => assign(deviceID)}>Assign</button>
            </td>
          </tr>
          {#if programsFor === deviceID}
            <tr data-testid="device-programs-row-{deviceID}">
              <td colspan={10 + columns.length}>
                <DevicePrograms deviceId={deviceID} {fsas} onClose={() => (programsFor = '')} />
              </td>
            </tr>
          {/if}
        {/each}
      {/if}
    </tbody>
  </table>
  {#if error}
    <div class="result err" data-testid="device-table-error">{error}</div>
  {/if}
  {#if pairError}
    <div class="result err" role="alert" data-testid="device-pair-error">{pairError}</div>
  {/if}
  {#if renameError}
    <div class="result err" role="alert" data-testid="device-rename-error">{renameError}</div>
  {/if}
</div>

<style>
  select {
    background: var(--bg);
    border: 1px solid var(--border);
    color: var(--text);
    padding: 2px 4px;
    font-size: 12px;
    max-width: 200px;
  }
</style>

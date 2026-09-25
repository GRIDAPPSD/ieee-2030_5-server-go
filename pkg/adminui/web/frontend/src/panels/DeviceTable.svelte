<script lang="ts">
  // The registered EndDevice list, plus the per-device FSA assignment
  // control. The LFDI cell is truncated for width, as it always has been;
  // the untruncated value is available through Lookup Device by LFDI.
  import { postJSON } from '../lib/api'
  import { deviceIdFromHref, type AdminFSA } from '../lib/fsa'
  import type { DashboardDevice } from '../lib/dashboard'

  let {
    devices,
    fsas,
    onChanged,
  }: {
    devices: DashboardDevice[]
    fsas: AdminFSA[]
    onChanged: () => void
  } = $props()

  let selected = $state<Record<string, string>>({})
  let error = $state('')

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
      <tr><th>SFDI</th><th>LFDI</th><th>Status</th><th>Href</th><th>Assign FSA</th></tr>
    </thead>
    <tbody id="deviceTable">
      {#if devices.length === 0}
        <tr><td colspan="5" class="stat-label">No devices registered</td></tr>
      {:else}
        {#each devices as device (device.href)}
          {@const deviceID = deviceIdFromHref(device.href)}
          <tr>
            <td class="mono" data-testid="device-sfdi">{device.sfdi}</td>
            <td class="mono" data-testid="device-lfdi">{(device.lfdi ?? '').substring(0, 16)}...</td>
            <td class={device.enabled ? 'online' : 'offline'}>{device.enabled ? 'ONLINE' : 'OFFLINE'}</td>
            <td class="mono">{device.href}</td>
            <td>
              <select id="assignSel-{deviceID}" bind:value={selected[deviceID]}>
                <option value="">(pick FSA)</option>
                {#each fsas as fsa (fsa.mRID)}
                  <option value={'/api/fsas/' + fsa.mRID}>{fsa.mRID}</option>
                {/each}
              </select>
              <button class="btn btn-small" onclick={() => assign(deviceID)}>Assign</button>
            </td>
          </tr>
        {/each}
      {/if}
    </tbody>
  </table>
  {#if error}
    <div class="result err" data-testid="device-table-error">{error}</div>
  {/if}
</div>

<style>
  select {
    background: var(--bg);
    border: 1px solid var(--border);
    color: var(--text);
    padding: 2px 4px;
    font-size: 12px;
    max-width: 120px;
  }
</style>

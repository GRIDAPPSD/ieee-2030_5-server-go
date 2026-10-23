<script lang="ts">
  // The Logs tab: a picker over every device and aggregator, and the log of
  // the one picked. The pick lives in the URL (?device=<lfdi>), so a reload or
  // a link from the Devices tab lands on the same device.
  import type { DashboardDevice } from '../lib/dashboard'
  import { canon, deviceLabel } from '../lib/devices'
  import { currentSearch, navigate } from '../lib/router'
  import DeviceLog from './DeviceLog.svelte'

  // ready is false until the device list has been read once, so a device in
  // the URL is not reported missing before the list arrives.
  let { devices, ready }: { devices: DashboardDevice[]; ready: boolean } = $props()

  const wanted = $derived(new URLSearchParams($currentSearch).get('device') ?? '')
  const picked = $derived(wanted === '' ? undefined : devices.find((d) => canon(d.lfdi) === canon(wanted)))

  function pick(lfdi: string) {
    navigate(`/ui/logs?device=${encodeURIComponent(lfdi)}`)
  }
</script>

<div class="card" data-testid="logs-tab">
  <h2>Logs</h2>
  <label>
    Device
    <select
      data-testid="logs-picker"
      value={picked?.lfdi ?? ''}
      onchange={(e) => pick(e.currentTarget.value)}
    >
      <option value="" disabled>Pick a device</option>
      {#each devices as d (d.lfdi)}
        <option value={d.lfdi}>{deviceLabel(d.lfdi, d.sfdi)}</option>
      {/each}
    </select>
  </label>

  {#if wanted === ''}
    <p class="hint" data-testid="logs-pick-hint">Pick a device or aggregator to read its log.</p>
  {:else if picked}
    <DeviceLog lfdi={picked.lfdi} {devices} />
  {:else if !ready}
    <p class="hint">Loading...</p>
  {:else}
    <p class="hint" data-testid="logs-not-found">No device matches <span class="mono">{wanted}</span>: not found.</p>
  {/if}
</div>

<style>
  select {
    background: var(--bg);
    border: 1px solid var(--border);
    color: var(--text);
    padding: 2px 4px;
    font-size: 12px;
  }
</style>

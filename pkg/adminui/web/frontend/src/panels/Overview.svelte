<script lang="ts">
  import type { DashboardData } from '../lib/dashboard'
  import { commsOnlineCount } from '../lib/comms'
  import { derSummary } from '../lib/derstatus'

  let { data }: { data: DashboardData | null } = $props()

  // Without a comms recorder every device reads "unknown", and "0 of N"
  // would claim the fleet is silent when the server simply cannot tell.
  const commsText = $derived.by(() => {
    const devices = data?.devices ?? []
    if (devices.length > 0 && devices.every((d) => d.comms === 'unknown')) return 'Unknown'
    return `${commsOnlineCount(devices)} of ${data ? data.deviceCount : 0}`
  })

  // Counted per DER from the status each device reported. "Connected" is the
  // DER's own power connection and does not imply it is exporting.
  const derText = $derived(derSummary(data?.devices ?? []))
</script>

<div class="card">
  <h2>Registered devices</h2>
  <div class="big-number" id="bigDeviceCount">{data ? data.deviceCount : 0}</div>
  <div class="stat-row">
    <span class="stat-label">Comms online</span><span id="commsOnline">{commsText}</span>
  </div>
  <div class="stat-row">
    <span class="stat-label" title="Each DER counted once, from its own latest report. Connected is its power connection and does not mean energized or exporting.">DERs (reported)</span><span id="derConnected">{derText}</span>
  </div>
  <div class="stat-row">
    <span class="stat-label">Mirror Usage Points</span><span id="mupCount">{data ? data.mupCount : 0}</span>
  </div>
</div>

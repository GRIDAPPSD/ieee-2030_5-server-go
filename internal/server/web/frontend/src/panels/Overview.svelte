<script lang="ts">
  import type { DashboardData } from '../lib/dashboard'

  let { data }: { data: DashboardData | null } = $props()
</script>

<div class="card">
  <h2>Connected Devices</h2>
  <div class="big-number" id="bigDeviceCount">{data ? data.deviceCount : 0}</div>
  <div class="stat-row">
    <span class="stat-label">Mirror Usage Points</span><span id="mupCount">{data ? data.mupCount : 0}</span>
  </div>
</div>

<script lang="ts">
  // The dashboard's fixed header. The ids below (tlsMode, uptime,
  // deviceCount) and the ".navbar h1" shape are the selectors the
  // Playwright suite in e2e/ addresses, so they are preserved verbatim
  // from the markup this panel replaces.
  import type { DashboardData } from '../lib/dashboard'

  let { data }: { data: DashboardData | null } = $props()
</script>

<nav class="navbar">
  <h1>IEEE 2030.5 Server Admin</h1>
  <div class="status">
    <span>TLS: <span class="badge" id="tlsMode">{data ? data.tlsMode : '-'}</span></span>
    <span>Uptime: <span id="uptime">{data ? data.uptime : '-'}</span></span>
    <span>Devices: <span id="deviceCount">{data ? data.deviceCount : 0}</span></span>
  </div>
</nav>

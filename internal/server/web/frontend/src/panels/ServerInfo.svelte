<script lang="ts">
  // The protocol line is the fixed edition string the previous markup
  // carried; the Playwright suite asserts on it, so it is not derived
  // from anything here.
  import type { DashboardData } from '../lib/dashboard'

  let { data }: { data: DashboardData | null } = $props()
</script>

<div class="card">
  <h2>Server Info</h2>
  <div class="stat-row"><span class="stat-label">TLS Cipher</span><span id="tlsCipher">{data ? data.tlsMode : '-'}</span></div>
  <div class="stat-row"><span class="stat-label">Uptime</span><span id="uptimeDetail">{data ? data.uptime : '-'}</span></div>
  <div class="stat-row"><span class="stat-label">Protocol</span><span>IEEE 2030.5-2018</span></div>
</div>

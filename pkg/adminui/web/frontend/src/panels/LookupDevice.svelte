<script lang="ts">
  // GET /api/devices/by-lfdi/{lfdi}. "found: false" is a normal answer,
  // not an error, and is reported as such: an operator checking whether a
  // device is registered needs to tell "not registered" apart from
  // "the lookup failed".
  import { fetchJSON } from '../lib/api'
  import { deviceLabel } from '../lib/devices'

  interface LookupResponse {
    found: boolean
    device?: {
      sfdi: string
      lfdi: string
      href: string
      enabled: boolean
    }
  }

  let lfdi = $state('')
  let message = $state('')
  let foundDevice = $state<NonNullable<LookupResponse['device']> | null>(null)
  // The found line is derived so a name loaded or changed after the lookup
  // shows without a second click.
  const result = $derived(
    foundDevice === null
      ? message
      : `Found: ${foundDevice.href} ${deviceLabel(foundDevice.lfdi, foundDevice.sfdi)} enabled=${foundDevice.enabled}`,
  )
  let outcome = $state<'idle' | 'ok' | 'missing' | 'error'>('idle')

  async function lookup() {
    foundDevice = null
    if (!lfdi) {
      outcome = 'error'
      message = 'Enter an LFDI to look up.'
      return
    }
    const res = await fetchJSON<LookupResponse>(`/api/devices/by-lfdi/${encodeURIComponent(lfdi)}`)
    if (!res.ok) {
      outcome = 'error'
      message = `Error (${res.status}): ${res.error}`
      return
    }
    if (!res.data.found || !res.data.device) {
      outcome = 'missing'
      message = 'No device registered for that LFDI.'
      return
    }
    const dev = res.data.device
    outcome = 'ok'
    foundDevice = dev
  }
</script>

<div class="card full-width">
  <h2>Lookup Device by LFDI</h2>
  <div class="form-row">
    <input type="text" id="lookupLFDI" placeholder="LFDI (40 hex chars)" bind:value={lfdi} />
    <button class="btn" onclick={lookup}>Lookup</button>
  </div>
  <div class="result" class:ok={outcome === 'ok'} class:err={outcome === 'error'} id="lookupResult">{result}</div>
</div>

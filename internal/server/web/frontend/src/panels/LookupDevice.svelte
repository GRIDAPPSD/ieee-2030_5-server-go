<script lang="ts">
  // GET /api/devices/by-lfdi/{lfdi}. "found: false" is a normal answer,
  // not an error, and is reported as such: an operator checking whether a
  // device is registered needs to tell "not registered" apart from
  // "the lookup failed".
  import { fetchJSON } from '../lib/api'

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
  let result = $state('')
  let outcome = $state<'idle' | 'ok' | 'missing' | 'error'>('idle')

  async function lookup() {
    if (!lfdi) {
      outcome = 'error'
      result = 'Enter an LFDI to look up.'
      return
    }
    const res = await fetchJSON<LookupResponse>(`/api/devices/by-lfdi/${encodeURIComponent(lfdi)}`)
    if (!res.ok) {
      outcome = 'error'
      result = `Error (${res.status}): ${res.error}`
      return
    }
    if (!res.data.found || !res.data.device) {
      outcome = 'missing'
      result = 'No device registered for that LFDI.'
      return
    }
    const dev = res.data.device
    outcome = 'ok'
    result = `Found: ${dev.href} SFDI=${dev.sfdi} enabled=${dev.enabled}`
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

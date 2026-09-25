<script lang="ts">
  // Add End Device: paste a PEM, let the server derive the identity, then
  // register it with a PIN. SFDI and LFDI are readonly and only ever come
  // from POST /api/certs/info (internal/handler/admin_register.go): the
  // registration key must be the identity the server computed from the
  // certificate, never an operator-typed value that could disagree with
  // the cert the device will present.
  import { postBody, postJSON } from '../lib/api'
  import { downloadText, PEM_MIME } from '../lib/download'
  import type { MintedCert } from '../lib/deviceCert'

  interface CertInfoResponse {
    sfdi: string
    lfdi: string
    subject: string
  }

  interface AddDeviceResponse {
    href: string
    sfdi: string
    lfdi: string
  }

  // pending is a mint carried from the Certificates tab (issue 594),
  // through AdminShell's hoisted state. undefined/null means no mint is
  // waiting, and the paste path below is unaffected.
  let { onAdded, pending }: { onAdded?: () => void; pending?: MintedCert | null } = $props()

  let pem = $state('')
  let sfdi = $state('')
  let lfdi = $state('')
  let description = $state('')
  let pin = $state<number | null>(null)
  let enabled = $state(true)
  let result = $state('')
  let ok = $state(false)

  // A tab switch to here unmounts and remounts this component ({#if} in
  // AdminShell.svelte), so a mint that happened on the Certificates tab
  // cannot have been held in this component's own state; it arrives only
  // through the pending prop, on mount or when a later mint replaces it.
  // Pre-fills the same readonly fields parseCert() fills, without
  // re-deriving anything: the identifiers already came from the server.
  $effect(() => {
    if (pending) {
      sfdi = pending.sfdi
      lfdi = pending.lfdi
    }
  })

  function downloadPending(kind: 'crt' | 'key') {
    if (!pending) return
    downloadText(
      `${pending.serial}.${kind}`,
      kind === 'crt' ? pending.certPEM : pending.keyPEM,
      PEM_MIME,
    )
  }

  async function parseCert() {
    if (!pem || !pem.includes('BEGIN CERTIFICATE')) {
      ok = false
      result = 'Paste a PEM certificate first.'
      return
    }
    const res = await postBody<CertInfoResponse>('/api/certs/info', 'application/x-pem-file', pem)
    if (!res.ok) {
      ok = false
      result = `Error: ${res.error}`
      return
    }
    sfdi = res.data.sfdi ?? ''
    lfdi = res.data.lfdi ?? ''
    ok = true
    result = `Parsed cert. SFDI=${res.data.sfdi} Subject="${res.data.subject ?? ''}"`
  }

  async function addDevice() {
    if (!sfdi || !lfdi) {
      ok = false
      result = 'Parse a cert first (SFDI + LFDI required).'
      return
    }
    if (pin === null || Number.isNaN(pin) || pin < 0) {
      ok = false
      result = 'PIN must be a non-negative integer.'
      return
    }
    const res = await postJSON<AddDeviceResponse>('/api/devices', {
      sfdi,
      lfdi,
      description,
      pin,
      enabled,
    })
    if (!res.ok) {
      ok = false
      result = `Error: ${res.error}`
      return
    }
    ok = true
    result = `Created ${res.data.href} (PIN persisted).`
    onAdded?.()
  }
</script>

<div class="card full-width">
  <h2>Add End Device</h2>
  <p class="hint">
    Paste the device certificate (PEM). The server derives SFDI and LFDI; you supply PIN and description.
  </p>
  {#if pending}
    <div class="form-row" data-testid="pending-mint">
      <p class="hint">A minted certificate is loaded below; SFDI and LFDI are pre-filled.</p>
      <button class="btn btn-small" data-testid="download-pending-cert" onclick={() => downloadPending('crt')}>
        Save certificate
      </button>
      <button class="btn btn-small" data-testid="download-pending-key" onclick={() => downloadPending('key')}>
        Save private key
      </button>
    </div>
  {/if}
  <textarea
    id="addDevCert"
    rows="6"
    placeholder={'-----BEGIN CERTIFICATE-----\n...\n-----END CERTIFICATE-----'}
    bind:value={pem}
  ></textarea>
  <div class="form-row">
    <button class="btn" onclick={parseCert}>Parse Cert</button>
  </div>
  <div class="form-row">
    <input type="text" id="addDevSFDI" placeholder="SFDI (12 digits)" readonly value={sfdi} />
    <input type="text" id="addDevLFDI" placeholder="LFDI (40 hex chars)" readonly value={lfdi} />
  </div>
  <div class="form-row">
    <input type="text" id="addDevDesc" placeholder="Description" bind:value={description} />
    <input type="number" id="addDevPIN" placeholder="PIN (uint32)" min="0" max="4294967295" bind:value={pin} />
    <label>
      <input type="checkbox" id="addDevEnabled" bind:checked={enabled} /> Enabled
    </label>
    <button class="btn btn-green" onclick={addDevice}>Add Device</button>
  </div>
  <div class="result" class:ok class:err={result !== '' && !ok} id="addDevResult">{result}</div>
</div>

<script lang="ts">
  // Add End Device: paste a PEM, let the server derive the identity, then
  // register it with a PIN. SFDI and LFDI are readonly and only ever come
  // from POST /api/certs/info (internal/handler/admin_register.go): the
  // registration key must be the identity the server computed from the
  // certificate, never an operator-typed value that could disagree with
  // the cert the device will present.
  import { postBody, postJSON } from '../lib/api'

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

  let { onAdded }: { onAdded?: () => void } = $props()

  let pem = $state('')
  let sfdi = $state('')
  let lfdi = $state('')
  let description = $state('')
  let pin = $state<number | null>(null)
  let enabled = $state(true)
  let result = $state('')
  let ok = $state(false)

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

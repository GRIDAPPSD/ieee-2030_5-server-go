<script lang="ts">
  // Certificate management. Two operations are reachable here: download the
  // CA, and mint a device certificate.
  //
  // GET /api/certs/ca answers with JSON carrying the PEM in a field
  // (internal/handler/admin_certs.go's certResponse), NOT with a PEM body,
  // so a plain download link would save a JSON document under a .crt name
  // and an operator's trust store would reject it. The PEM is extracted
  // here and saved as a file.
  //
  // POST /api/certs/server is deliberately NOT wired to a button. It mints
  // a server private key, and a browser panel that receives one without
  // delivering it puts key material through the devtools network log, the
  // JS heap and any HAR attached to a bug report, for nothing. That route
  // stays available to the CLI and to curl, where the key is the point.
  import { onMount } from 'svelte'
  import { fetchJSON, postJSON } from '../lib/api'
  import { downloadText, PEM_MIME } from '../lib/download'
  import type { MintedCert } from '../lib/deviceCert'

  interface DeviceCertResponse {
    certPEM: string
    keyPEM: string
    sfdi: string
    lfdi: string
  }

  interface CAResponse {
    certPEM: string
  }

  // The shape GET /api/certs/device-types returns (internal/certs/oids.go's
  // DeviceTypeInfo): value is what a mint request's deviceType field
  // expects, name is the stable machine-readable identifier used to find
  // the default, label is what the select shows.
  interface DeviceTypeInfo {
    value: number
    name: string
    label: string
  }

  interface DeviceTypesResponse {
    deviceTypes: DeviceTypeInfo[]
  }

  // onMinted carries a successful mint to Add End Device (issue 594),
  // through AdminShell's hoisted state: this panel and AddDevice sit on
  // different tabs, so neither can hold the value in its own local state
  // across the switch.
  let { onMinted }: { onMinted?: (cert: MintedCert) => void } = $props()

  let deviceTypes = $state<DeviceTypeInfo[]>([])
  let deviceTypesStatus = $state<'loading' | 'ready' | 'error'>('loading')
  let deviceTypesError = $state('')
  // null until the fetch below resolves; Generate stays disabled until then
  // (see deviceTypesStatus in the template) so a mint never goes out with a
  // guessed or stale type.
  let deviceType = $state<number | null>(null)
  let hwSerial = $state('')
  // The route rejects a device-cert request with no hwType: the OID is part
  // of the CSIP HardwareModuleName SAN and the server cannot invent it.
  // Without this field every request from here answered 400.
  let hwType = $state('')
  let certResult = $state('')
  let certOk = $state(false)
  let caResult = $state('')
  let caOk = $state(false)

  // The issued pair is held only to hand it to the operator, and is cleared
  // on the next generation so a stale key does not sit in the heap behind a
  // button that now refers to a different device.
  let issued: { certPEM: string; keyPEM: string; serial: string } | null = $state(null)

  // The endpoint exists precisely so this component carries no copy of the
  // values (issue 594); "generic" is preferred over deviceTypes[0] so the
  // default does not depend on response order.
  async function loadDeviceTypes() {
    const res = await fetchJSON<DeviceTypesResponse>('/api/certs/device-types')
    if (!res.ok) {
      deviceTypesStatus = 'error'
      deviceTypesError = res.error
      return
    }
    deviceTypes = res.data.deviceTypes
    deviceType = deviceTypes.find((dt) => dt.name === 'generic')?.value ?? deviceTypes[0]?.value ?? null
    deviceTypesStatus = 'ready'
  }

  onMount(() => {
    loadDeviceTypes()
  })

  async function generateDeviceCert() {
    if (deviceType === null) return
    issued = null
    const res = await postJSON<DeviceCertResponse>('/api/certs/device', {
      deviceType,
      hwSerialNum: hwSerial,
      hwType,
    })
    if (!res.ok) {
      certOk = false
      certResult = `Error: ${res.error}`
      return
    }
    const serial = hwSerial || 'device'
    issued = {
      certPEM: res.data.certPEM,
      keyPEM: res.data.keyPEM,
      serial,
    }
    certOk = true
    certResult = `Generated! SFDI: ${res.data.sfdi}`
    onMinted?.({
      certPEM: res.data.certPEM,
      keyPEM: res.data.keyPEM,
      sfdi: res.data.sfdi,
      lfdi: res.data.lfdi,
      serial,
    })
  }

  async function downloadCA() {
    const res = await fetchJSON<CAResponse>('/api/certs/ca')
    if (!res.ok) {
      caOk = false
      caResult = `Error: ${res.error}`
      return
    }
    if (!res.data.certPEM) {
      caOk = false
      caResult = 'The server returned no CA certificate.'
      return
    }
    downloadText('ca.crt', res.data.certPEM, PEM_MIME)
    caOk = true
    caResult = 'Saved ca.crt.'
  }

  function downloadIssued(kind: 'crt' | 'key') {
    if (issued === null) return
    downloadText(
      `${issued.serial}.${kind}`,
      kind === 'crt' ? issued.certPEM : issued.keyPEM,
      PEM_MIME,
    )
  }
</script>

<div class="card">
  <h2>Certificate Management</h2>
  <div class="form-row">
    <select id="deviceType" bind:value={deviceType} disabled={deviceTypesStatus !== 'ready'}>
      {#each deviceTypes as dt (dt.value)}
        <option value={dt.value}>{dt.label}</option>
      {/each}
    </select>
    <input type="text" id="hwSerial" placeholder="Hardware Serial (e.g., INV-001)" bind:value={hwSerial} />
    <input
      type="text"
      id="hwType"
      placeholder="Manufacturer PEN OID (e.g., 1.3.6.1.4.1.40732.99)"
      bind:value={hwType}
    />
    <button class="btn" onclick={generateDeviceCert} disabled={deviceTypesStatus !== 'ready'}>
      Generate Device Cert
    </button>
  </div>
  {#if deviceTypesStatus === 'loading'}
    <p class="hint" data-testid="device-types-loading">Loading device types...</p>
  {:else if deviceTypesStatus === 'error'}
    <div class="result err" data-testid="device-types-error">
      Could not load device types: {deviceTypesError}. Reload the page to try again.
    </div>
  {/if}
  <div class="result" class:ok={certOk} class:err={certResult !== '' && !certOk} id="certResult">{certResult}</div>

  {#if issued !== null}
    <div class="form-row">
      <button class="btn btn-small" data-testid="download-device-cert" onclick={() => downloadIssued('crt')}>
        Save certificate
      </button>
      <button class="btn btn-small" data-testid="download-device-key" onclick={() => downloadIssued('key')}>
        Save private key
      </button>
    </div>
    <p class="hint" data-testid="device-key-note">
      The private key is issued once and is not stored on the server. Save it now and install it on the device.
    </p>
  {/if}

  <div class="form-row">
    <button class="btn" data-testid="download-ca" onclick={downloadCA}>Download CA certificate</button>
  </div>
  <div class="result" class:ok={caOk} class:err={caResult !== '' && !caOk} data-testid="ca-result">{caResult}</div>
</div>

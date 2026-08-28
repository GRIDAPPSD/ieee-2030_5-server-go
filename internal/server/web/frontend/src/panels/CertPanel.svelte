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
  import { fetchJSON, postJSON } from '../lib/api'
  import { downloadText, PEM_MIME } from '../lib/download'

  interface DeviceCertResponse {
    certPEM: string
    keyPEM: string
    sfdi: string
    lfdi: string
  }

  interface CAResponse {
    certPEM: string
  }

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

  async function generateDeviceCert() {
    issued = null
    const res = await postJSON<DeviceCertResponse>('/api/certs/device', {
      deviceType: 1,
      hwSerialNum: hwSerial,
      hwType,
    })
    if (!res.ok) {
      certOk = false
      certResult = `Error: ${res.error}`
      return
    }
    issued = {
      certPEM: res.data.certPEM,
      keyPEM: res.data.keyPEM,
      serial: hwSerial || 'device',
    }
    certOk = true
    certResult = `Generated! SFDI: ${res.data.sfdi}`
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
    <input type="text" id="hwSerial" placeholder="Hardware Serial (e.g., INV-001)" bind:value={hwSerial} />
    <input
      type="text"
      id="hwType"
      placeholder="Manufacturer PEN OID (e.g., 1.3.6.1.4.1.40732.99)"
      bind:value={hwType}
    />
    <button class="btn" onclick={generateDeviceCert}>Generate Device Cert</button>
  </div>
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

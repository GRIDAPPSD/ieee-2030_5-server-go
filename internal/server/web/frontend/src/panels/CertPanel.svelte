<script lang="ts">
  // Certificate management: the three routes the admin cert service
  // exposes (internal/handler/admin_certs.go). The CA download is a
  // plain link because GET /api/certs/ca returns a PEM body, not JSON,
  // and the browser should save it rather than this panel buffering it.
  import { postJSON } from '../lib/api'

  interface DeviceCertResponse {
    sfdi: string
    lfdi: string
  }

  interface ServerCertResponse {
    certPEM: string
  }

  let hwSerial = $state('')
  let serverHosts = $state('')
  let certResult = $state('')
  let certOk = $state(false)
  let serverResult = $state('')
  let serverOk = $state(false)

  async function generateDeviceCert() {
    const res = await postJSON<DeviceCertResponse>('/api/certs/device', {
      deviceType: 1,
      hwSerialNum: hwSerial,
    })
    certOk = res.ok
    certResult = res.ok ? `Generated! SFDI: ${res.data.sfdi}` : `Error: ${res.error}`
  }

  async function generateServerCert() {
    const hosts = serverHosts
      .split(',')
      .map((h) => h.trim())
      .filter((h) => h !== '')
    if (hosts.length === 0) {
      serverOk = false
      serverResult = 'Enter at least one host or IP.'
      return
    }
    const res = await postJSON<ServerCertResponse>('/api/certs/server', { hosts })
    serverOk = res.ok
    serverResult = res.ok ? `Issued a server cert for ${hosts.join(', ')}.` : `Error: ${res.error}`
  }
</script>

<div class="card">
  <h2>Certificate Management</h2>
  <div class="form-row">
    <input type="text" id="hwSerial" placeholder="Hardware Serial (e.g., INV-001)" bind:value={hwSerial} />
    <button class="btn" onclick={generateDeviceCert}>Generate Device Cert</button>
  </div>
  <div class="result" class:ok={certOk} class:err={certResult !== '' && !certOk} id="certResult">{certResult}</div>

  <div class="form-row">
    <input
      type="text"
      id="serverCertHosts"
      placeholder="Server cert hosts (comma separated)"
      bind:value={serverHosts}
    />
    <button class="btn" data-testid="generate-server-cert" onclick={generateServerCert}>Generate Server Cert</button>
  </div>
  <div
    class="result"
    class:ok={serverOk}
    class:err={serverResult !== '' && !serverOk}
    data-testid="server-cert-result"
  >
    {serverResult}
  </div>

  <div class="form-row">
    <a data-testid="download-ca" href="/api/certs/ca" download="ca.crt">Download CA certificate</a>
  </div>
</div>

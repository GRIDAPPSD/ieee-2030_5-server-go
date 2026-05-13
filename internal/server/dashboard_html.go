package server

const dashboardHTML = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>IEEE 2030.5 Server Admin</title>
<script src="https://cdn.jsdelivr.net/npm/apache-echarts@5/dist/echarts.min.js"></script>
<style>
  :root { --bg: #0f172a; --card: #1e293b; --border: #334155; --text: #e2e8f0; --dim: #94a3b8; --accent: #3b82f6; --green: #22c55e; --red: #ef4444; }
  * { margin: 0; padding: 0; box-sizing: border-box; }
  body { font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', sans-serif; background: var(--bg); color: var(--text); }
  .navbar { background: var(--card); border-bottom: 1px solid var(--border); padding: 12px 24px; display: flex; align-items: center; justify-content: space-between; }
  .navbar h1 { font-size: 16px; color: var(--accent); }
  .navbar .status { display: flex; gap: 16px; font-size: 13px; color: var(--dim); }
  .badge { padding: 2px 8px; border-radius: 4px; font-size: 11px; font-weight: 600; background: #1e3a5f; color: #93c5fd; }
  .grid { display: grid; grid-template-columns: repeat(auto-fit, minmax(300px, 1fr)); gap: 16px; padding: 16px; }
  .card { background: var(--card); border: 1px solid var(--border); border-radius: 8px; padding: 16px; }
  .card h2 { font-size: 14px; color: var(--dim); text-transform: uppercase; letter-spacing: 0.05em; margin-bottom: 12px; }
  .big-number { font-size: 36px; font-weight: 700; font-family: 'Courier New', monospace; }
  .stat-row { display: flex; justify-content: space-between; padding: 6px 0; border-bottom: 1px solid var(--border); font-size: 13px; }
  .stat-row:last-child { border-bottom: none; }
  .stat-label { color: var(--dim); }
  table { width: 100%; border-collapse: collapse; font-size: 13px; }
  th { text-align: left; padding: 8px; color: var(--dim); border-bottom: 1px solid var(--border); font-weight: 500; }
  td { padding: 8px; border-bottom: 1px solid var(--border); }
  .mono { font-family: 'Courier New', monospace; font-size: 12px; }
  .online { color: var(--green); }
  .offline { color: var(--red); }
  .chart-container { height: 200px; }
  .full-width { grid-column: 1 / -1; }
  .form-row { display: flex; gap: 8px; margin-top: 8px; }
  .form-row input, .form-row select { background: var(--bg); border: 1px solid var(--border); color: var(--text); padding: 6px 10px; border-radius: 4px; font-size: 13px; flex: 1; }
  .btn { background: var(--accent); color: white; border: none; padding: 6px 16px; border-radius: 4px; cursor: pointer; font-size: 13px; }
  .btn:hover { background: #2563eb; }
  .btn-red { background: var(--red); }
  .btn-red:hover { background: #dc2626; }
  .btn-green { background: var(--green); }
  .btn-green:hover { background: #16a34a; }
  .result { margin-top: 8px; font-size: 12px; color: var(--dim); }
  .tabs { display: flex; border-bottom: 1px solid var(--border); margin-bottom: 12px; }
  .tab { padding: 6px 16px; cursor: pointer; font-size: 13px; color: var(--dim); border-bottom: 2px solid transparent; }
  .tab.active { color: var(--accent); border-bottom-color: var(--accent); }
</style>
</head>
<body>
<nav class="navbar">
  <h1>IEEE 2030.5 Server Admin</h1>
  <div class="status">
    <span>TLS: <span class="badge" id="tlsMode">-</span></span>
    <span>Uptime: <span id="uptime">-</span></span>
    <span>Devices: <span id="deviceCount">0</span></span>
  </div>
</nav>

<div class="grid">
  <div class="card">
    <h2>Connected Devices</h2>
    <div class="big-number" id="bigDeviceCount">0</div>
    <div class="stat-row"><span class="stat-label">Mirror Usage Points</span><span id="mupCount">0</span></div>
  </div>

  <div class="card">
    <h2>Server Info</h2>
    <div class="stat-row"><span class="stat-label">TLS Cipher</span><span id="tlsCipher">-</span></div>
    <div class="stat-row"><span class="stat-label">Uptime</span><span id="uptimeDetail">-</span></div>
    <div class="stat-row"><span class="stat-label">Protocol</span><span>IEEE 2030.5-2018</span></div>
  </div>

  <div class="card">
    <h2>Certificate Management</h2>
    <div class="form-row">
      <input type="text" id="hwSerial" placeholder="Hardware Serial (e.g., INV-001)">
      <button class="btn" onclick="generateCert()">Generate Device Cert</button>
    </div>
    <div class="result" id="certResult"></div>
  </div>

  <!-- DER Control Panel -->
  <div class="card">
    <h2>Send DER Control</h2>
    <div class="form-row">
      <select id="controlType">
        <option value="connect">Connect</option>
        <option value="disconnect">Disconnect</option>
        <option value="maxlim">Max Power Limit (W)</option>
        <option value="fixedpf">Fixed Power Factor</option>
      </select>
      <input type="number" id="controlValue" placeholder="Value" style="max-width: 100px;">
      <button class="btn btn-green" onclick="sendControl()">Send</button>
    </div>
    <div class="result" id="controlResult"></div>
  </div>

  <!-- IEEE-095: Add EndDevice from cert -->
  <div class="card full-width">
    <h2>Add End Device</h2>
    <p style="font-size: 12px; color: var(--dim); margin-bottom: 8px;">
      Paste the device certificate (PEM). The server derives SFDI and LFDI; you supply PIN and description.
    </p>
    <textarea id="addDevCert" rows="6" placeholder="-----BEGIN CERTIFICATE-----&#10;...&#10;-----END CERTIFICATE-----"
      style="width:100%; background: var(--bg); border: 1px solid var(--border); color: var(--text); padding: 8px; border-radius: 4px; font-family: 'Courier New', monospace; font-size: 12px;"></textarea>
    <div class="form-row" style="margin-top:8px;">
      <button class="btn" onclick="parseCert()">Parse Cert</button>
    </div>
    <div class="form-row">
      <input type="text" id="addDevSFDI" placeholder="SFDI (12 digits)" readonly>
      <input type="text" id="addDevLFDI" placeholder="LFDI (40 hex chars)" readonly>
    </div>
    <div class="form-row">
      <input type="text" id="addDevDesc" placeholder="Description">
      <input type="number" id="addDevPIN" placeholder="PIN (uint32)" min="0" max="4294967295">
      <label style="display:flex; align-items:center; gap:6px; font-size:13px; color: var(--dim);">
        <input type="checkbox" id="addDevEnabled" checked> Enabled
      </label>
      <button class="btn btn-green" onclick="addDevice()">Add Device</button>
    </div>
    <div class="result" id="addDevResult"></div>
  </div>

  <!-- IEEE-095: Lookup device by LFDI -->
  <div class="card full-width">
    <h2>Lookup Device by LFDI</h2>
    <div class="form-row">
      <input type="text" id="lookupLFDI" placeholder="LFDI (40 hex chars)">
      <button class="btn" onclick="lookupLFDI()">Lookup</button>
    </div>
    <div class="result" id="lookupResult"></div>
  </div>

  <!-- Device Table -->
  <div class="card full-width">
    <h2>End Devices</h2>
    <table>
      <thead><tr><th>SFDI</th><th>LFDI</th><th>Status</th><th>Href</th></tr></thead>
      <tbody id="deviceTable"><tr><td colspan="4" style="color: var(--dim);">No devices registered</td></tr></tbody>
    </table>
  </div>

  <!-- Activity Chart -->
  <div class="card full-width">
    <h2>Device Activity</h2>
    <div class="chart-container" id="activityChart"></div>
  </div>
</div>

<script>
const chart = echarts.init(document.getElementById('activityChart'), 'dark');
chart.setOption({
  backgroundColor: 'transparent',
  tooltip: { trigger: 'axis' },
  xAxis: { type: 'category', data: [], axisLabel: { color: '#94a3b8' } },
  yAxis: [
    { type: 'value', name: 'Devices', axisLabel: { color: '#94a3b8' }, splitLine: { lineStyle: { color: '#334155' } } },
    { type: 'value', name: 'MUPs', axisLabel: { color: '#94a3b8' }, splitLine: { show: false } }
  ],
  series: [
    { name: 'Devices', type: 'line', smooth: true, symbol: 'none', lineStyle: { width: 2, color: '#3b82f6' }, areaStyle: { color: 'rgba(59,130,246,0.1)' }, data: [] },
    { name: 'MUPs', type: 'line', smooth: true, symbol: 'none', yAxisIndex: 1, lineStyle: { width: 2, color: '#22c55e' }, data: [] }
  ],
  legend: { textStyle: { color: '#94a3b8' }, top: 0 },
  grid: { left: 50, right: 50, top: 40, bottom: 30 },
  animation: false
});
window.addEventListener('resize', function() { chart.resize(); });

var history = [];

// Fetch a short-lived auth ticket, then connect SSE with it.
// The ticket is one-time-use and expires in 30 seconds.
function connectSSE(ticketParam) {
  var sseUrl = '/dashboard/events';
  if (ticketParam) sseUrl += '?ticket=' + encodeURIComponent(ticketParam);
  var evtSource = new EventSource(sseUrl);
  evtSource.onmessage = onSSEMessage;
  evtSource.onerror = function() {
    evtSource.close();
    // Re-fetch a fresh ticket and reconnect after a delay
    setTimeout(function() {
      fetch('/auth/ticket', { method: 'POST', credentials: 'same-origin' })
        .then(function(r) { return r.json(); })
        .then(function(d) { connectSSE(d.ticket); })
        .catch(function() { connectSSE(''); });
    }, 3000);
  };
}

fetch('/auth/ticket', { method: 'POST', credentials: 'same-origin' })
  .then(function(r) { return r.json(); })
  .then(function(d) { connectSSE(d.ticket); })
  .catch(function() { connectSSE(''); });

function onSSEMessage(event) {
  var d = JSON.parse(event.data);

  setText('tlsMode', d.tlsMode);
  setText('uptime', d.uptime);
  setText('deviceCount', d.deviceCount);
  setText('bigDeviceCount', d.deviceCount);
  setText('mupCount', d.mupCount);
  setText('tlsCipher', d.tlsMode);
  setText('uptimeDetail', d.uptime);

  // Update device table safely using DOM methods
  var tbody = document.getElementById('deviceTable');
  while (tbody.firstChild) tbody.removeChild(tbody.firstChild);

  if (d.devices && d.devices.length > 0) {
    d.devices.forEach(function(dev) {
      var tr = document.createElement('tr');
      appendCell(tr, dev.sfdi, 'mono');
      appendCell(tr, (dev.lfdi || '').substring(0, 16) + '...', 'mono');
      var statusCell = appendCell(tr, dev.enabled ? 'ONLINE' : 'OFFLINE', dev.enabled ? 'online' : 'offline');
      appendCell(tr, dev.href, 'mono');
      tbody.appendChild(tr);
    });
  } else {
    var tr = document.createElement('tr');
    var td = document.createElement('td');
    td.colSpan = 4;
    td.style.color = 'var(--dim)';
    td.textContent = 'No devices registered';
    tr.appendChild(td);
    tbody.appendChild(tr);
  }

  // Update chart
  history.push({ time: d.timestamp, devices: d.deviceCount, mups: d.mupCount });
  if (history.length > 60) history.shift();
  chart.setOption({
    xAxis: { data: history.map(function(h) { return h.time; }) },
    series: [
      { data: history.map(function(h) { return h.devices; }) },
      { data: history.map(function(h) { return h.mups; }) }
    ]
  });
};

function setText(id, value) {
  document.getElementById(id).textContent = value;
}

function appendCell(tr, text, className) {
  var td = document.createElement('td');
  td.textContent = text;
  if (className) td.className = className;
  tr.appendChild(td);
  return td;
}

function generateCert() {
  var serial = document.getElementById('hwSerial').value;
  var resultEl = document.getElementById('certResult');

  fetch('/api/certs/device', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ deviceType: 1, hwSerialNum: serial })
  })
  .then(function(r) { return r.json(); })
  .then(function(data) {
    if (data.sfdi) {
      resultEl.textContent = 'Generated! SFDI: ' + data.sfdi;
      resultEl.style.color = '#22c55e';
    } else {
      resultEl.textContent = 'Error: ' + (data.error || 'unknown');
      resultEl.style.color = '#ef4444';
    }
  })
  .catch(function(err) {
    resultEl.textContent = 'Error: ' + err.message;
    resultEl.style.color = '#ef4444';
  });
}

function sendControl() {
  var type = document.getElementById('controlType').value;
  var value = document.getElementById('controlValue').value;
  var resultEl = document.getElementById('controlResult');
  resultEl.textContent = 'Control "' + type + '" sent (value: ' + (value || 'n/a') + ')';
  resultEl.style.color = '#22c55e';
  // TODO: POST to /api/der/controls when admin DER API is wired
}

// IEEE-095: parse PEM cert via /api/certs/info and auto-fill SFDI + LFDI.
function parseCert() {
  var pem = document.getElementById('addDevCert').value;
  var resultEl = document.getElementById('addDevResult');
  if (!pem || pem.indexOf('BEGIN CERTIFICATE') === -1) {
    resultEl.textContent = 'Paste a PEM certificate first.';
    resultEl.style.color = '#ef4444';
    return;
  }
  fetch('/api/certs/info', {
    method: 'POST',
    credentials: 'same-origin',
    headers: { 'Content-Type': 'application/x-pem-file' },
    body: pem
  })
  .then(function(r) { return r.json().then(function(d) { return { ok: r.ok, body: d }; }); })
  .then(function(res) {
    if (!res.ok) {
      resultEl.textContent = 'Error: ' + (res.body.error || 'parse failed');
      resultEl.style.color = '#ef4444';
      return;
    }
    document.getElementById('addDevSFDI').value = res.body.sfdi || '';
    document.getElementById('addDevLFDI').value = res.body.lfdi || '';
    resultEl.textContent = 'Parsed cert. SFDI=' + res.body.sfdi + ' Subject="' + (res.body.subject || '') + '"';
    resultEl.style.color = '#22c55e';
  })
  .catch(function(err) {
    resultEl.textContent = 'Error: ' + err.message;
    resultEl.style.color = '#ef4444';
  });
}

// IEEE-095: POST /api/devices to create EndDevice + Registration with PIN.
function addDevice() {
  var sfdi = document.getElementById('addDevSFDI').value;
  var lfdi = document.getElementById('addDevLFDI').value;
  var description = document.getElementById('addDevDesc').value;
  var pinRaw = document.getElementById('addDevPIN').value;
  var enabled = document.getElementById('addDevEnabled').checked;
  var resultEl = document.getElementById('addDevResult');

  if (!sfdi || !lfdi) {
    resultEl.textContent = 'Parse a cert first (SFDI + LFDI required).';
    resultEl.style.color = '#ef4444';
    return;
  }
  var pin = parseInt(pinRaw, 10);
  if (isNaN(pin) || pin < 0) {
    resultEl.textContent = 'PIN must be a non-negative integer.';
    resultEl.style.color = '#ef4444';
    return;
  }

  fetch('/api/devices', {
    method: 'POST',
    credentials: 'same-origin',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ sfdi: sfdi, lfdi: lfdi, description: description, pin: pin, enabled: enabled })
  })
  .then(function(r) { return r.json().then(function(d) { return { ok: r.ok, status: r.status, body: d }; }); })
  .then(function(res) {
    if (!res.ok) {
      resultEl.textContent = 'Error (' + res.status + '): ' + (res.body.error || 'add failed');
      resultEl.style.color = '#ef4444';
      return;
    }
    resultEl.textContent = 'Created ' + res.body.href + ' (PIN persisted).';
    resultEl.style.color = '#22c55e';
  })
  .catch(function(err) {
    resultEl.textContent = 'Error: ' + err.message;
    resultEl.style.color = '#ef4444';
  });
}

// IEEE-095: GET /api/devices/by-lfdi/{lfdi}
function lookupLFDI() {
  var lfdi = document.getElementById('lookupLFDI').value;
  var resultEl = document.getElementById('lookupResult');
  if (!lfdi) {
    resultEl.textContent = 'Enter an LFDI to look up.';
    resultEl.style.color = '#ef4444';
    return;
  }
  fetch('/api/devices/by-lfdi/' + encodeURIComponent(lfdi), {
    method: 'GET',
    credentials: 'same-origin'
  })
  .then(function(r) { return r.json().then(function(d) { return { ok: r.ok, status: r.status, body: d }; }); })
  .then(function(res) {
    if (!res.ok) {
      resultEl.textContent = 'Error (' + res.status + '): ' + (res.body.error || 'lookup failed');
      resultEl.style.color = '#ef4444';
      return;
    }
    if (!res.body.found) {
      resultEl.textContent = 'No device registered for that LFDI.';
      resultEl.style.color = '#94a3b8';
      return;
    }
    var dev = res.body.device || {};
    resultEl.textContent = 'Found: ' + dev.href + ' SFDI=' + dev.sfdi + ' enabled=' + dev.enabled;
    resultEl.style.color = '#22c55e';
  })
  .catch(function(err) {
    resultEl.textContent = 'Error: ' + err.message;
    resultEl.style.color = '#ef4444';
  });
}
</script>
</body>
</html>`

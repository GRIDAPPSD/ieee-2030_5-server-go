<script lang="ts">
  // The submit here is deliberately local: there is no admin DER control
  // route on the server to post to. Wiring this form needs
  // POST /api/der/controls to exist first (it does not); until then the
  // panel echoes the selection so the form itself is exercisable, and
  // sends nothing.
  let controlType = $state('connect')
  // A type=number binding yields null when the field is empty, so the
  // echoed message distinguishes "no value" from a real 0.
  let controlValue = $state<number | null>(null)
  let controlResult = $state('')

  function sendControl() {
    controlResult = `Control "${controlType}" sent (value: ${controlValue ?? 'n/a'})`
  }
</script>

<div class="card">
  <h2>Send DER Control</h2>
  <div class="form-row">
    <select id="controlType" bind:value={controlType}>
      <option value="connect">Connect</option>
      <option value="disconnect">Disconnect</option>
      <option value="maxlim">Max Power Limit (W)</option>
      <option value="fixedpf">Fixed Power Factor</option>
    </select>
    <input type="number" id="controlValue" placeholder="Value" style="max-width: 100px;" bind:value={controlValue} />
    <button class="btn btn-green" onclick={sendControl}>Send</button>
  </div>
  <div class="result" class:ok={controlResult !== ''} id="controlResult">{controlResult}</div>
  <p class="hint" data-testid="der-control-stub-note">
    Not yet delivered to the device: the admin DER control route
    (POST /api/der/controls) is not implemented on the server.
  </p>
</div>

<script lang="ts">
  // POST /api/fsas. The server generates an mRID when the field is left
  // blank, so a blank mRID is sent as an absent field rather than an
  // empty string: the create handler rejects unknown/empty values instead
  // of treating "" as "please generate one".
  import { postJSON } from '../lib/api'
  import type { AdminFSA } from '../lib/fsa'

  let { onCreated }: { onCreated?: () => void } = $props()

  let description = $state('')
  let mRID = $state('')
  let primacy = $state<number | null>(null)
  let result = $state('')
  let ok = $state(false)

  async function createFSA() {
    if (!description) {
      ok = false
      result = 'Description required.'
      return
    }
    const body: Record<string, unknown> = { description }
    if (mRID) body.mRID = mRID
    if (primacy !== null && !Number.isNaN(primacy) && primacy >= 0) body.primacy = primacy

    const res = await postJSON<AdminFSA>('/api/fsas', body)
    if (!res.ok) {
      ok = false
      result = `Error (${res.status}): ${res.error}`
      return
    }
    ok = true
    result = `Created ${res.data.href} (mRID=${res.data.mRID})`
    description = ''
    mRID = ''
    primacy = null
    onCreated?.()
  }
</script>

<div class="card full-width">
  <h2>Create FSA Template</h2>
  <p class="hint">
    Create a FunctionSetAssignments template, then attach DERPrograms and assign it to one or more devices.
  </p>
  <div class="form-row">
    <input type="text" id="newFSADesc" placeholder="Description (required)" bind:value={description} />
    <input type="text" id="newFSAMRID" placeholder="mRID (optional, auto-generated if blank)" bind:value={mRID} />
    <input
      type="number"
      id="newFSAPrimacy"
      placeholder="Primacy"
      min="0"
      max="255"
      style="max-width: 100px;"
      bind:value={primacy}
    />
    <button class="btn btn-green" onclick={createFSA}>Create FSA</button>
  </div>
  <div class="result" class:ok class:err={result !== '' && !ok} id="createFSAResult">{result}</div>
</div>

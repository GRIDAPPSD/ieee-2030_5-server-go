<script lang="ts">
  // Enter timed grid blocks on the DERMS tab. The server splits each block
  // across every available aggregator, so no device is named. Nothing is sent
  // before Confirm, a refused batch keeps what was typed and marks the rows
  // the server names, and an outcome the page cannot know (no answer, a 5xx)
  // locks sending until the operator says the Controls tab was checked, so the
  // same batch is never sent twice on a guess.
  import { onDestroy } from 'svelte'
  import { deviceLabel } from '../lib/devices'
  import { formatQuantity } from '../lib/flowreservation'
  import {
    buildGridRequest,
    gridWord,
    newRow,
    refusedRows,
    shareText,
    submitGridRequest,
    type GridConfirmLine,
    type GridCreated,
    type GridDeviceEvents,
    type GridRequestBody,
    type GridRowDraft,
  } from '../lib/gridrows'
  import GridBlockRows from './GridBlockRows.svelte'

  const WRITE_TIMEOUT_MS = 20_000

  // Called once a batch is stored, so the block views read it at once.
  let { onCreated = () => {} }: { onCreated?: () => void } = $props()

  let rows = $state<GridRowDraft[]>([newRow()])
  let anchorMs = $state(Date.now())
  let marks = $state(new Map<number, string[]>())
  let formError = $state('')
  let devices = $state<GridDeviceEvents[]>([])
  let pending = $state<{ body: GridRequestBody; lines: GridConfirmLine[] } | null>(null)
  let busy = $state(false)
  let created = $state<GridCreated | null>(null)
  let unknownNote = $state('')

  let writeSeq = 0
  let writeCtrl: AbortController | null = null
  const locked = $derived(busy || unknownNote !== '')

  onDestroy(() => {
    writeSeq++
    writeCtrl?.abort()
  })

  function clearAnswer() {
    marks = new Map()
    formError = ''
    devices = []
  }

  function review() {
    if (locked) return
    clearAnswer()
    created = null
    anchorMs = Date.now()
    const built = buildGridRequest(rows)
    if (!built.ok) {
      const byRow = new Map<number, string[]>()
      for (const e of built.errors) byRow.set(e.row, [...(byRow.get(e.row) ?? []), e.message])
      marks = byRow
      formError = `${byRow.size} of ${rows.length} blocks cannot be sent yet.`
      return
    }
    pending = { body: built.body, lines: built.lines }
  }

  async function confirm() {
    if (locked || pending === null) return
    const seq = ++writeSeq
    const ctrl = new AbortController()
    writeCtrl = ctrl
    busy = true
    const res = await submitGridRequest(pending.body, { signal: ctrl.signal, timeoutMs: WRITE_TIMEOUT_MS })
    if (seq !== writeSeq) return
    writeCtrl = null
    busy = false
    pending = null
    if (res.ok) {
      created = res.created
      onCreated()
      rows = [newRow()]
      anchorMs = Date.now()
      return
    }
    if (res.kind === 'unknown') {
      unknownNote = res.message
      return
    }
    formError = res.message
    if (res.refusal !== null) {
      marks = refusedRows(res.refusal)
      devices = res.refusal.devices
    }
  }

  function stopWaiting() {
    writeSeq++
    writeCtrl?.abort()
    writeCtrl = null
    busy = false
    pending = null
    unknownNote = 'Stopped waiting. The server may or may not have written the batch. Check the Controls tab before sending again.'
  }

  function focusOnMount(el: HTMLElement) {
    el.focus()
  }
</script>

<div class="card full-width" data-testid="grid-request-pane">
  <h2>Grid power requests</h2>
  <p class="hint">
    Enter one or more timed blocks. The server splits each block across every available aggregator; you do not name devices.
    The batch is accepted or refused whole.
  </p>

  {#if pending === null}
    <GridBlockRows bind:rows {marks} disabled={locked} {anchorMs} />
    <div class="form-row">
      <button class="btn btn-green" disabled={locked} onclick={review}>Review</button>
    </div>
  {:else}
    <div class="action-panel" data-testid="grid-confirm">
      <p tabindex="-1" {@attach focusOnMount}>Send {pending.lines.length} block{pending.lines.length === 1 ? '' : 's'} to the grid, split across every available aggregator:</p>
      <ul class="confirm-lines" data-testid="grid-confirm-lines">
        {#each pending.lines as line (line.row)}
          <li data-testid="grid-confirm-line" class="dir-{line.direction}">{line.text}</li>
        {/each}
      </ul>
      <button class="btn btn-green" disabled={busy} onclick={confirm}>Confirm</button>
      {#if busy}
        <span class="hint" data-testid="grid-sending">Sending...</span>
        <button class="btn btn-small" onclick={stopWaiting}>Stop waiting</button>
      {:else}
        <button class="btn btn-small" onclick={() => (pending = null)}>Back</button>
      {/if}
    </div>
  {/if}

  {#if formError}
    <div class="result err" role="alert" data-testid="grid-form-error">{formError}</div>
  {/if}
  {#if devices.length > 0}
    <ul data-testid="grid-devices">
      {#each devices as d}
        <li>Device {d.device} of aggregator {deviceLabel(d.aggregatorLFDI)}: {d.live} live, {d.added} added by this batch.</li>
      {/each}
    </ul>
  {/if}

  {#if unknownNote !== ''}
    <div class="result err" role="alert" data-testid="grid-unknown">{unknownNote}</div>
    <div class="form-row">
      <button class="btn btn-small" onclick={() => (unknownNote = '')}>I checked the Controls tab, allow sending again</button>
    </div>
  {/if}

  {#if created !== null}
    <div class="result ok" role="status" data-testid="grid-created">
      Stored batch {created.batch} with {created.rows.length} block{created.rows.length === 1 ? '' : 's'}.
      {#if !created.persisted}A server restart forgets this batch.{/if}
    </div>
    {#each created.rows as r (r.row)}
      <div data-testid="grid-created-row">
        <strong>{gridWord(r.direction)}</strong>
        block {r.row}: requested {formatQuantity(r.requestedW, 'W')}, encoded {formatQuantity(r.encodedW, 'W')}
        (difference {formatQuantity(r.differenceW, 'W')}).
        <ul>
          {#each r.shares as s}
            <li data-testid="grid-share">{deviceLabel(s.aggregatorLFDI)}: {shareText(s)}</li>
          {/each}
        </ul>
      </div>
    {/each}
    {#if created.warnings.length > 0}
      <div class="result" role="status" data-testid="grid-warnings">
        Warnings from the server:
        <ul>
          {#each created.warnings as w}
            <li>{w}</li>
          {/each}
        </ul>
      </div>
    {/if}
  {/if}
</div>

<style>
  .confirm-lines {
    list-style: none;
    margin: var(--sp-3) 0 var(--sp-4);
    padding: 0;
    font-size: var(--fs-4);
  }
  .confirm-lines li {
    margin-bottom: var(--sp-2);
  }
  li.dir-discharge {
    border-left: 4px solid var(--power-discharge);
    padding-left: var(--sp-3);
  }
  li.dir-charge {
    border-left: 4px solid var(--power-charge);
    padding-left: var(--sp-3);
  }
</style>

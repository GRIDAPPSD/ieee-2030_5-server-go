<script lang="ts">
  // Every scheduled grid block on the DERMS tab, as a 24 hour timeline and a
  // list sorted by start. Both draw the same shown set, so they cannot
  // disagree, and both come from the next read of the list: a cancel changes
  // nothing on screen until that read answers. The effect owns the poll and
  // its in-flight read; a cancel in flight is aborted on destroy.
  import { onDestroy } from 'svelte'
  import { deviceLabel } from '../lib/devices'
  import { formatQuantity } from '../lib/flowreservation'
  import {
    batchCancelPath,
    blockCancelPath,
    fetchGridBlocks,
    isLiveBlock,
    postCancel,
    shownBlocks,
    type BlockEntry,
    type FailedShare,
  } from '../lib/gridblocks'
  import { gridWord } from '../lib/gridrows'
  import { startPoll } from '../lib/poll'
  import ConfirmAction from './ConfirmAction.svelte'
  import GridBlockTimeline from './GridBlockTimeline.svelte'

  const REFRESH_MS = 15_000
  const READ_TIMEOUT_MS = 15_000
  const WRITE_TIMEOUT_MS = 20_000

  // refreshKey changes when a batch was just stored, so the new blocks show
  // without waiting for the next poll.
  let { refreshKey = 0 }: { refreshKey?: number } = $props()

  let entries = $state<BlockEntry[]>([])
  let serverNow = $state<number | null>(null)
  let nowMs = $state(Date.now())
  let loaded = $state(false)
  let absent = $state(false)
  let error = $state('')
  let liveOnly = $state(true)
  let reads = $state(0)

  // The cancel in flight (its key), and what the last one answered.
  let pending = $state('')
  let failure = $state<{ key: string; message: string; failed: FailedShare[]; retry: string | null } | null>(null)
  let writeSeq = 0
  let writeCtrl: AbortController | null = null

  $effect(() => {
    void refreshKey
    void reads
    return startPoll(async (signal) => {
      const res = await fetchGridBlocks({ signal, timeoutMs: READ_TIMEOUT_MS })
      if (signal.aborted) return undefined
      if (res.ok) {
        entries = res.entries
        serverNow = res.serverTime
        nowMs = Date.now()
        error = ''
        absent = false
        loaded = true
      } else if (res.absent) {
        absent = true
        return undefined
      } else {
        error = `Could not load grid blocks: ${res.error}`
      }
      return REFRESH_MS
    })
  })

  onDestroy(() => {
    writeSeq++
    writeCtrl?.abort()
  })

  const shown = $derived(shownBlocks(entries, liveOnly, serverNow))
  const liveCount = $derived(entries.filter((e) => isLiveBlock(e, serverNow)).length)
  const batches = $derived([...new Set(shown.map((e) => e.batch))])

  async function cancel(key: string, path: string): Promise<void> {
    if (pending !== '') return
    const seq = ++writeSeq
    const ctrl = new AbortController()
    writeCtrl = ctrl
    pending = key
    failure = null
    const res = await postCancel(path, { signal: ctrl.signal, timeoutMs: WRITE_TIMEOUT_MS })
    if (seq !== writeSeq) return
    writeCtrl = null
    pending = ''
    if (!res.ok) failure = { key, message: res.message, failed: res.failed, retry: res.retry }
    // Whatever the answer, the next read decides what is live now.
    reads++
  }

  const when = (seconds: number): string => new Date(seconds * 1000).toLocaleString()
  const watts = (w: number | null): string => (w === null ? 'unknown' : formatQuantity(w, 'W'))
  const goneShares = (e: BlockEntry) => e.block.shares.filter((s) => s.aggregatorGone)
</script>

{#snippet cancelButton(key: string, id: string, noun: string, path: string)}
  <ConfirmAction
    triggerId="grid-cancel-{id}"
    confirmId="grid-cancel-confirm-{id}"
    trigger="Cancel"
    triggerLabel="Cancel {noun}"
    confirm="Confirm cancel"
    confirmLabel="Confirm cancel {noun}"
    busyText="Cancelling..."
    keepLabel="Keep {noun}"
    busy={pending === key}
    disabled={pending !== ''}
    onOpen={() => (failure = null)}
    onConfirm={() => cancel(key, path)}
  />
{/snippet}

{#if !absent}
  <div class="card full-width" data-testid="grid-blocks-pane">
    <h2>Scheduled grid blocks</h2>
    {#if error}
      <div class="result err" role="alert" data-testid="grid-blocks-error">{error}</div>
    {/if}
    {#if !loaded && !error}
      <p class="hint" data-testid="grid-blocks-loading">Loading grid blocks...</p>
    {:else if loaded}
      <GridBlockTimeline entries={shown} {nowMs} />

      <div class="form-row">
        <span data-testid="grid-blocks-live-count">{liveCount} pending or active</span>
        <label>
          <input type="checkbox" data-testid="grid-blocks-live-only" bind:checked={liveOnly} />
          Show pending and active only
        </label>
      </div>

      {#if failure !== null}
        <div class="result err" role="alert" data-testid="grid-cancel-error">
          {failure.message}
          {#if failure.failed.length > 0}
            <ul>
              {#each failure.failed as f}
                <li data-testid="grid-cancel-failed">
                  Block {f.row}, aggregator {deviceLabel(f.aggregatorLFDI)} ({f.mRID}): {f.reason}
                </li>
              {/each}
            </ul>
          {/if}
          {#if failure.retry !== null}
            {@const retry = failure.retry}
            <button class="btn btn-small" data-testid="grid-cancel-retry" disabled={pending !== ''} onclick={() => cancel('retry', retry)}>
              {pending === 'retry' ? 'Retrying...' : 'Retry'}
            </button>
          {/if}
        </div>
      {/if}

      {#if batches.length > 0}
        <ul class="batch-cancels" data-testid="grid-batches">
          {#each batches as batch (batch)}
            <li>
              Batch {batch}
              {@render cancelButton(`batch:${batch}`, `batch-${batch}`, `batch ${batch}`, batchCancelPath(batch))}
            </li>
          {/each}
        </ul>
      {/if}

      <div class="table-scroll">
        <table data-testid="grid-blocks-table">
          <thead>
            <tr>
              <th>Start</th>
              <th>End</th>
              <th>Direction</th>
              <th>Requested</th>
              <th>Encoded</th>
              <th>Status</th>
              <th>Delivered</th>
              <th>Notes</th>
              <th>Batch, block</th>
              <th></th>
            </tr>
          </thead>
          <tbody>
            {#each shown as e (e.batch + ':' + e.block.row)}
              <tr data-testid="grid-block" data-block="{e.batch}:{e.block.row}" class="dir-{e.block.direction}">
                <td>{when(e.block.start)}</td>
                <td>{when(e.block.end)}</td>
                <td><strong data-testid="grid-block-direction">{gridWord(e.block.direction)}</strong></td>
                <td>{watts(e.block.requestedW)}</td>
                <td>{watts(e.block.encodedW)}</td>
                <td data-testid="grid-block-status">{e.block.status}</td>
                <td data-testid="grid-block-delivered">{e.block.deliveredW === null ? 'no reading' : formatQuantity(e.block.deliveredW, 'W')}</td>
                <td>
                  {#if e.block.basisChanged}<span data-testid="grid-block-basis">basis changed</span>{/if}
                  {#each goneShares(e) as s}
                    <span data-testid="grid-block-gone">aggregator {deviceLabel(s.aggregatorLFDI)} gone</span>
                  {/each}
                </td>
                <td>{e.batch}, {e.block.row}</td>
                <td>
                  {#if isLiveBlock(e, serverNow)}
                    {@render cancelButton(`block:${e.batch}:${e.block.row}`, `block-${e.batch}-${e.block.row}`, `block ${e.block.row} of batch ${e.batch}`, blockCancelPath(e.batch, e.block.row))}
                  {/if}
                </td>
              </tr>
            {:else}
              <tr>
                <td colspan="10" data-testid="grid-blocks-empty">
                  {liveOnly && entries.length > 0 ? 'No pending or active blocks' : 'No grid blocks'}
                </td>
              </tr>
            {/each}
          </tbody>
        </table>
      </div>
    {/if}
  </div>
{/if}

<style>
  .batch-cancels {
    list-style: none;
    margin: var(--sp-3) 0;
    padding: 0;
    display: flex;
    flex-wrap: wrap;
    gap: var(--sp-3);
  }
  tr.dir-discharge td:first-child {
    border-left: 4px solid var(--power-discharge);
  }
  tr.dir-charge td:first-child {
    border-left: 4px solid var(--power-charge);
  }
</style>

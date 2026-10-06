<script lang="ts">
  // One device-reported cell: chips or plain text, freshness notes, and a
  // read error. The error is rendered in the cell, in red, so a device whose
  // DER data failed to read is never mistaken for one with no DERs.
  import type { CellView } from '../lib/derstatus'

  let { view, testid, errorTestid = '' }: { view: CellView; testid: string; errorTestid?: string } = $props()
</script>

<td data-testid={testid} title={view.title || undefined}>
  {#if view.error}
    <div class="offline" data-testid={errorTestid || undefined}>{view.error}</div>
  {/if}
  {#each view.chips as chip (chip.label)}
    <span class="chip" class:warn={chip.warn}>{chip.label}</span>
  {/each}
  {#if view.text}
    <span class="plain">{view.text}</span>
  {/if}
  {#each view.notes as note (note)}
    <div class="note">{note}</div>
  {/each}
</td>

<style>
  .chip {
    display: inline-block;
    padding: 0 6px;
    margin: 1px 2px 1px 0;
    border: 1px solid var(--border);
    border-radius: 8px;
    font-size: 12px;
  }
  .chip.warn {
    color: #f59e0b;
    border-color: #f59e0b;
  }
  .note {
    font-size: 11px;
    color: var(--dim);
  }
</style>

<script lang="ts">
  // The row list for entering several timed blocks: a direction with no
  // default, an unsigned amount, a start (now, chained to the block before, or
  // typed) and a duration per row. It holds no request logic: the parent owns
  // the rows, builds the body and shows the server's answer, and passes back
  // the refusals to mark on rows. Typing into a start clears the chain, so a
  // chained row sends followsPrevious and a typed one sends its start.
  import { startPickerValue } from '../lib/dercontrol'
  import { formatQuantity } from '../lib/flowreservation'
  import {
    addRow,
    MAX_GRID_ROWS,
    pickerValue,
    removeRow,
    rowFigureW,
    type GridRowDraft,
  } from '../lib/gridrows'

  interface Props {
    rows: GridRowDraft[]
    // Messages to show on a row, keyed by its number from 1.
    marks?: Map<number, string[]>
    disabled?: boolean
    // The moment a block that starts now is drawn from, for the pickers of
    // the blocks that chain to it.
    anchorMs: number
  }

  let { rows = $bindable(), marks = new Map(), disabled = false, anchorMs }: Props = $props()

  function figureOf(r: GridRowDraft): string {
    const w = rowFigureW(r)
    return w === null ? '' : formatQuantity(w, 'W')
  }

  function onStartNow(i: number, checked: boolean) {
    rows[i].startNow = checked
    if (!checked) rows[i].start = startPickerValue(rows[i].start)
  }

  function onStartTyped(i: number, value: string) {
    rows[i].follows = false
    rows[i].startNow = false
    rows[i].start = value
  }

  function chainTo(i: number) {
    rows[i].follows = true
    rows[i].startNow = false
    rows[i].start = ''
  }
</script>

<div class="grid-rows" data-testid="grid-rows">
  {#each rows as row, i}
    {@const refused = marks.get(i + 1) ?? []}
    <fieldset
      class="grid-row"
      class:dir-discharge={row.direction === 'discharge'}
      class:dir-charge={row.direction === 'charge'}
      class:refused={refused.length > 0}
      data-testid="grid-row"
      {disabled}
    >
      <legend>Block {i + 1}</legend>
      <div class="form-row">
        <label>
          <input type="radio" name="grid-direction-{i}" value="discharge" bind:group={rows[i].direction} />
          To grid (discharge, export)
        </label>
        <label>
          <input type="radio" name="grid-direction-{i}" value="charge" bind:group={rows[i].direction} />
          From grid (charge, import)
        </label>
      </div>
      <div class="form-row">
        <label>Amount (x, no sign)
          <input type="text" inputmode="numeric" data-testid="grid-x" bind:value={rows[i].x} />
        </label>
        <label>Multiplier (y, x times 10^y W)
          <input type="text" inputmode="numeric" data-testid="grid-y" bind:value={rows[i].y} />
        </label>
        <span class="figure" data-testid="grid-figure">{figureOf(row)}</span>
        <label>Duration (minutes)
          <input type="text" inputmode="numeric" data-testid="grid-duration" bind:value={rows[i].durationMinutes} />
        </label>
      </div>
      <div class="form-row">
        {#if !row.follows}
          <label>
            <input type="checkbox" checked={row.startNow} onchange={(e) => onStartNow(i, e.currentTarget.checked)} />
            Start now
          </label>
        {/if}
        {#if row.follows || !row.startNow}
          <label>Start (local time)
            <input
              type="datetime-local"
              step="1"
              data-testid="grid-start"
              value={pickerValue(rows, i, anchorMs)}
              oninput={(e) => onStartTyped(i, e.currentTarget.value)}
            />
          </label>
        {/if}
        {#if row.follows}
          <span class="hint" data-testid="grid-follows">Starts when block {i} ends. Type a start to unchain it.</span>
        {:else if i > 0}
          <button class="btn btn-small" type="button" onclick={() => chainTo(i)}>Follow block {i}</button>
        {/if}
        <button class="btn btn-small" type="button" disabled={rows.length === 1} onclick={() => (rows = removeRow(rows, i))}>
          Remove block {i + 1}
        </button>
      </div>
      {#if refused.length > 0}
        <ul class="refusal" role="alert" data-testid="grid-row-refusal">
          {#each refused as m}
            <li>Refused: {m}</li>
          {/each}
        </ul>
      {/if}
    </fieldset>
  {/each}
  <div class="form-row">
    <button class="btn btn-small" type="button" {disabled} onclick={() => (rows = addRow(rows))}>Add row</button>
    <span class="hint">{rows.length} of {MAX_GRID_ROWS} blocks. A start that is now or chained is placed by the server; times shown for it are estimates from this browser's clock.</span>
  </div>
</div>

<style>
  .grid-row {
    border: 1px solid var(--border);
    border-left-width: 4px;
    border-radius: var(--radius-sm);
    margin: var(--sp-3) 0;
    padding: var(--sp-3) var(--sp-4);
  }
  .grid-row.dir-discharge {
    border-left-color: var(--power-discharge);
  }
  .grid-row.dir-charge {
    border-left-color: var(--power-charge);
  }
  .grid-row.refused {
    border-color: var(--status-fault);
    background: var(--status-fault-bg);
  }
  .figure {
    font-weight: 600;
    min-width: 8ch;
  }
  .refusal {
    margin: var(--sp-3) 0 0;
    padding-left: var(--sp-5);
    color: var(--text);
  }
  @media (max-width: 800px) {
    .grid-row {
      padding: var(--sp-2) var(--sp-3);
    }
  }
</style>

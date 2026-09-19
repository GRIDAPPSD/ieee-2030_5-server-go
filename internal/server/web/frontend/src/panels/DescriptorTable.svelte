<script lang="ts">
  // Renders a TableBody's columns and rows as plain text. Every cell is
  // {expression} interpolation, never {@html}, so a value containing
  // markup renders as the literal characters instead of becoming an
  // element (issue 368 criterion 6).
  //
  // descriptor.go documents that nothing enforces len(row) ==
  // len(columns) and calls a mismatch a bug for a renderer to catch. A
  // mismatched row is not padded or truncated to fit: either would
  // invent or silently drop a value with no way to know which column it
  // belonged to. It is surfaced instead, with every supplied value still
  // shown, so the anomaly is visible rather than quietly repaired.
  import type { DescriptorTableBody } from '../lib/descriptor'

  let { body }: { body: DescriptorTableBody } = $props()
</script>

<table>
  <thead>
    <tr>
      {#each body.columns as column, i (i)}
        <th>{column}</th>
      {/each}
    </tr>
  </thead>
  <tbody>
    {#if body.rows.length === 0}
      <tr><td colspan={body.columns.length || 1} class="stat-label">No rows</td></tr>
    {:else}
      {#each body.rows as row, i (i)}
        {#if row.length === body.columns.length}
          <tr>
            {#each row as cell, j (j)}
              <td data-testid="descriptor-cell">{cell}</td>
            {/each}
          </tr>
        {:else}
          <tr class="row-mismatch" data-testid="row-mismatch">
            <td colspan={body.columns.length || 1}>
              Malformed row: expected {body.columns.length} columns, got {row.length}: {row.join(', ')}
            </td>
          </tr>
        {/if}
      {/each}
    {/if}
  </tbody>
</table>

<style>
  .row-mismatch td {
    color: var(--red);
  }
</style>

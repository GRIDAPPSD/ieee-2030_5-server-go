<script lang="ts">
  // A table body. descriptor.go does not enforce len(row) ==
  // len(columns); a mismatched row is shown with every supplied value
  // rather than padded or truncated, so the anomaly stays visible. An
  // empty table shows the section's own empty text.
  import type { DescriptorTableBody } from '../lib/descriptor'
  import DescriptorCell from './DescriptorCell.svelte'

  let { body, empty = '' }: { body: DescriptorTableBody; empty?: string } = $props()
</script>

<div class="table-scroll">
<table>
  <thead>
    <tr>
      {#each body.columns as column, i (i)}
        <th data-testid="descriptor-column-header">{column}</th>
      {/each}
    </tr>
  </thead>
  <tbody>
    {#if body.rows.length === 0}
      <tr><td colspan={body.columns.length || 1} class="stat-label" data-testid="descriptor-table-empty">{empty || 'No rows'}</td></tr>
    {:else}
      {#each body.rows as row, i (i)}
        {#if row.length > 0 && row.length === body.columns.length}
          <tr>
            {#each row as cell, j (j)}
              <td data-testid="descriptor-cell"><DescriptorCell {cell} /></td>
            {/each}
          </tr>
        {:else}
          <tr class="row-mismatch" data-testid="row-mismatch">
            <td colspan={body.columns.length || 1}>
              Malformed row: expected {body.columns.length} columns, got {row.length}: {row.map((c) => c.text).join(', ')}
            </td>
          </tr>
        {/if}
      {/each}
    {/if}
  </tbody>
</table>
</div>

<style>
  .row-mismatch td {
    color: var(--red);
  }
</style>

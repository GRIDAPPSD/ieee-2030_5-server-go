<script lang="ts">
  // A definition-list body. A group's heading renders only when
  // non-empty, so a flat list shows none. No entries in any group shows
  // the section's own empty text.
  import type { DescriptorDefinitionListBody } from '../lib/descriptor'
  import DescriptorCell from './DescriptorCell.svelte'

  let { body, empty = '' }: { body: DescriptorDefinitionListBody; empty?: string } = $props()

  let hasEntries = $derived(body.groups.some((group) => group.entries.length > 0))
</script>

{#if !hasEntries}
  <p class="hint" data-testid="descriptor-list-empty">{empty || 'This panel has no content.'}</p>
{:else}
  {#each body.groups as group, i (i)}
    <div class="card">
      {#if group.heading}
        <h3 data-testid="descriptor-group-heading">{group.heading}</h3>
      {/if}
      <dl>
        {#each group.entries as entry, j (j)}
          <div class="stat-row">
            <dt class="stat-label" data-testid="descriptor-key">{entry.key}</dt>
            <dd data-testid="descriptor-value"><DescriptorCell cell={entry.value} /></dd>
          </div>
        {/each}
      </dl>
    </div>
  {/each}
{/if}

<style>
  dt,
  dd {
    margin: 0;
  }
</style>

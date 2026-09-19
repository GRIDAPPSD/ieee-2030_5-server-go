<script lang="ts">
  // Renders a DefinitionListBody's groups. A single group with an empty
  // heading is the normal flat case (descriptor.go's doc comment): the
  // heading element is rendered only when Heading is non-empty, so a
  // flat list shows no empty heading. Every key and value is
  // {expression} interpolation, never {@html}.
  import type { DescriptorDefinitionListBody } from '../lib/descriptor'

  let { body }: { body: DescriptorDefinitionListBody } = $props()
</script>

{#each body.groups as group, i (i)}
  <div class="card">
    {#if group.heading}
      <h2>{group.heading}</h2>
    {/if}
    <dl>
      {#each group.entries as entry, j (j)}
        <div class="stat-row">
          <dt class="stat-label" data-testid="descriptor-key">{entry.key}</dt>
          <dd data-testid="descriptor-value">{entry.value}</dd>
        </div>
      {/each}
    </dl>
  </div>
{/each}

<style>
  dt,
  dd {
    margin: 0;
  }
</style>

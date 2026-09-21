<script lang="ts">
  // Renders a DefinitionListBody's groups. A single group with an empty
  // heading is the normal flat case (descriptor.go's doc comment): the
  // heading element is rendered only when Heading is non-empty, so a
  // flat list shows no empty heading. Every key and value is
  // {expression} interpolation, never {@html}.
  //
  // Groups has no `omitempty` on the Go side, so an empty list marshals
  // as JSON null rather than []. Svelte's {#each} tolerates a null
  // iterable without throwing, so this needs no guard against a crash;
  // it needs one so an empty list renders the same honest message every
  // other empty descriptor shape uses, instead of rendering nothing
  // (fix round 1, item 1 and item 6).
  import type { DescriptorDefinitionListBody } from '../lib/descriptor'

  let { body }: { body: DescriptorDefinitionListBody } = $props()

  let groups = $derived(body.groups ?? [])
</script>

{#if groups.length === 0}
  <p class="hint" data-testid="descriptor-list-empty">This panel has no content.</p>
{:else}
  {#each groups as group, i (i)}
    <div class="card">
      {#if group.heading}
        <h2 data-testid="descriptor-heading">{group.heading}</h2>
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
{/if}

<style>
  dt,
  dd {
    margin: 0;
  }
</style>

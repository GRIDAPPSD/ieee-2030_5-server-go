<script lang="ts">
  // Reads Descriptor.kind by name and renders the matching body, never
  // by inferring the shape from which fields happen to be present.
  // kind is the wire discriminator (descriptor.go's wireDescriptor); an
  // unknown kind and a descriptor with no body each render an honest
  // message instead of nothing or a crash (issue 368 criterion 3).
  import DescriptorTable from './DescriptorTable.svelte'
  import DescriptorDefinitionList from './DescriptorDefinitionList.svelte'
  import type { Descriptor, DescriptorTableBody, DescriptorDefinitionListBody } from '../lib/descriptor'

  let { descriptor }: { descriptor: Descriptor } = $props()
</script>

<!-- descriptor.body is typed unknown: TypeScript cannot see its shape
     from field presence, only from the kind check just taken, so the
     cast below reads the discriminator rather than guessing the shape. -->
{#if descriptor.kind === 'table'}
  <DescriptorTable body={descriptor.body as DescriptorTableBody} />
{:else if descriptor.kind === 'definitionList'}
  <DescriptorDefinitionList body={descriptor.body as DescriptorDefinitionListBody} />
{:else if descriptor.kind === undefined}
  <p class="hint" data-testid="descriptor-empty">This panel has no content.</p>
{:else}
  <p class="hint" data-testid="descriptor-unknown-kind">Unsupported content type: "{descriptor.kind}"</p>
{/if}

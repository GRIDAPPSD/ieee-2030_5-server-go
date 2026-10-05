<script lang="ts">
  // Renders a v2 Descriptor: each section's kind picks its body renderer
  // by name. An unsupported version, an unknown section kind, and a body
  // that does not match its kind each render a message, never nothing.
  import DescriptorTable from './DescriptorTable.svelte'
  import DescriptorDefinitionList from './DescriptorDefinitionList.svelte'
  import DescriptorChart from './DescriptorChart.svelte'
  import { descriptorChartRefusals, isChartBody } from '../lib/chart'
  import type { Descriptor, DescriptorTableBody, DescriptorDefinitionListBody } from '../lib/descriptor'

  let { descriptor }: { descriptor: Descriptor } = $props()

  // The literal 2, as descriptor_test.go pins the wire number: a renderer
  // in another language hardcodes it rather than importing Go's constant.
  const SUPPORTED_VERSION = 2

  function isTableBody(value: unknown): value is DescriptorTableBody {
    if (typeof value !== 'object' || value === null) return false
    const body = value as Partial<DescriptorTableBody>
    return Array.isArray(body.columns) && Array.isArray(body.rows) && body.rows.every(Array.isArray)
  }

  function isDefinitionListBody(value: unknown): value is DescriptorDefinitionListBody {
    if (typeof value !== 'object' || value === null) return false
    const body = value as Partial<DescriptorDefinitionListBody>
    return Array.isArray(body.groups) && body.groups.every((g) => Array.isArray(g?.entries))
  }

  // Per section, so the Descriptor-wide point bound is checked across all
  // chart sections as the encoder does.
  const chartRefusals = $derived(Array.isArray(descriptor.sections) ? descriptorChartRefusals(descriptor) : [])
</script>

{#if descriptor.version !== SUPPORTED_VERSION}
  <p class="hint" data-testid="descriptor-unsupported-version">Unsupported content version: {descriptor.version}</p>
{:else if !Array.isArray(descriptor.sections) || descriptor.sections.length === 0}
  <p class="hint" data-testid="descriptor-empty">This panel has no content.</p>
{:else}
  {#each descriptor.sections as section, i (i)}
    <div class="card" class:card-wide={section.kind === 'table'} data-testid="descriptor-section">
      {#if section.heading}
        <h2 data-testid="descriptor-heading">{section.heading}</h2>
      {/if}
      {#each section.prose ?? [] as line, j (j)}
        <p class="hint" data-testid="descriptor-prose">{line}</p>
      {/each}
      {#if section.kind === 'table' && isTableBody(section.body)}
        <DescriptorTable body={section.body} empty={section.empty} />
      {:else if section.kind === 'definitionList' && isDefinitionListBody(section.body)}
        <DescriptorDefinitionList body={section.body} empty={section.empty} />
      {:else if section.kind === 'chart' && chartRefusals[i] === null && isChartBody(section.body)}
        <DescriptorChart body={section.body} empty={section.empty} />
      {:else if section.kind === 'table' || section.kind === 'definitionList' || section.kind === 'chart'}
        <p class="hint" data-testid="descriptor-malformed-body">This section's content could not be rendered.</p>
      {:else}
        <p class="hint" data-testid="descriptor-unknown-kind">Unsupported content type: "{section.kind}"</p>
      {/if}
    </div>
  {/each}
{/if}

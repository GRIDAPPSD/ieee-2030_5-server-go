<script lang="ts">
  // Reads Descriptor.kind by name and renders the matching body, never
  // by inferring the shape from which fields happen to be present.
  // kind is the wire discriminator (descriptor.go's wireDescriptor); an
  // unrecognised kind, an absent kind, and a body that does not match
  // its kind (an absent body included) each render an honest message
  // instead of nothing or a crash (issue 368 criterion 3).
  import DescriptorTable from './DescriptorTable.svelte'
  import DescriptorDefinitionList from './DescriptorDefinitionList.svelte'
  import type { Descriptor, DescriptorTableBody, DescriptorDefinitionListBody } from '../lib/descriptor'

  let { descriptor }: { descriptor: Descriptor } = $props()

  // The literal 1, not a shared constant: descriptor_test.go pins the
  // wire "version" number the same way, reasoning that a renderer in
  // another language hardcodes the number rather than importing Go's
  // CurrentDescriptorVersion. Register already refuses an unsupported
  // Panel.DescriptorVersion at boot (registry.go,
  // ErrUnsupportedDescriptorVersion); this guard is this frontend's half
  // of issue 368 criterion 10, covering the wire body a future build's
  // server could still send.
  const SUPPORTED_VERSION = 1

  function isTableBody(value: unknown): value is DescriptorTableBody {
    return typeof value === 'object' && value !== null && 'columns' in value && 'rows' in value
  }

  function isDefinitionListBody(value: unknown): value is DescriptorDefinitionListBody {
    return typeof value === 'object' && value !== null && 'groups' in value
  }
</script>

<!-- descriptor.body is typed unknown: TypeScript cannot see its shape
     from the kind check alone, so isTableBody/isDefinitionListBody
     confirm the body actually carries its kind's keys before the cast
     below reads it as that shape. A kind with no body, and a kind whose
     body is the other shape, both fail their guard and fall to the
     malformed-body message instead of throwing. -->
{#if descriptor.version !== SUPPORTED_VERSION}
  <p class="hint" data-testid="descriptor-unsupported-version">Unsupported content version: {descriptor.version}</p>
{:else if descriptor.kind === 'table' && isTableBody(descriptor.body)}
  <DescriptorTable body={descriptor.body as DescriptorTableBody} />
{:else if descriptor.kind === 'definitionList' && isDefinitionListBody(descriptor.body)}
  <DescriptorDefinitionList body={descriptor.body as DescriptorDefinitionListBody} />
{:else if descriptor.kind === undefined}
  <p class="hint" data-testid="descriptor-empty">This panel has no content.</p>
{:else if descriptor.kind === 'table' || descriptor.kind === 'definitionList'}
  <p class="hint" data-testid="descriptor-malformed-body">This panel's content could not be rendered.</p>
{:else}
  <p class="hint" data-testid="descriptor-unknown-kind">Unsupported content type: "{descriptor.kind}"</p>
{/if}

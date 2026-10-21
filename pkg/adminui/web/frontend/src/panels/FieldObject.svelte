<script lang="ts">
  // The field for an object value: one boolean or integer field per member
  // the row lists, in the order it lists them.
  import FieldBoolean from './FieldBoolean.svelte'
  import FieldInteger from './FieldInteger.svelte'
  import type { CatalogField } from '../lib/controlcatalog'
  let {
    id,
    fields,
    value = $bindable({}),
  }: { id: string; fields: CatalogField[]; value?: Record<string, boolean | string> } = $props()
</script>

{#each fields as field (field.name)}
  {#if field.kind === 'boolean'}
    <FieldBoolean
      label={field.name}
      id="{id}-{field.name}"
      bind:value={() => value[field.name] === true, (v) => (value[field.name] = v)}
    />
  {:else}
    <FieldInteger
      label={field.name}
      id="{id}-{field.name}"
      min={field.min}
      max={field.max}
      required={field.required === true}
      bind:value={() => (typeof value[field.name] === 'string' ? (value[field.name] as string) : ''), (v) => (value[field.name] = v)}
    />
  {/if}
{/each}

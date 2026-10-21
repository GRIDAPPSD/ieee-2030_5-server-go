<script lang="ts">
  // One catalog row as a form field. There is one component per value kind
  // and none per function: the row's kind picks the component and its
  // min, max, fields and curveType configure it.
  import FieldBoolean from './FieldBoolean.svelte'
  import FieldCurve from './FieldCurve.svelte'
  import FieldInteger from './FieldInteger.svelte'
  import FieldObject from './FieldObject.svelte'
  import { curvesOfType, isSupportedKind, type CatalogRow, type CurveInfo, type FieldValue } from '../lib/controlcatalog'

  let {
    row,
    ticked = $bindable(),
    value = $bindable(),
    curves,
    curvesError = '',
  }: {
    row: CatalogRow
    ticked?: boolean
    value?: FieldValue
    curves: CurveInfo[]
    curvesError?: string
  } = $props()

  const supported = $derived(isSupportedKind(row.kind))
  const id = $derived(`control-fv-${row.name}`)
</script>

<div class="form-row control-function" data-testid="control-function-{row.name}">
  <label title={row.description}>
    <input type="checkbox" data-testid="control-fn-{row.name}" disabled={!supported} bind:checked={ticked} />
    {row.name}
  </label>
  {#if !supported}
    <span class="hint">Not supported on this page.</span>
  {:else if ticked}
    {#if row.kind === 'boolean'}
      <FieldBoolean label="on" {id} bind:value={() => value === true, (v) => (value = v)} />
    {:else if row.kind === 'integer'}
      <FieldInteger
        label="value"
        {id}
        min={row.min}
        max={row.max}
        bind:value={() => (typeof value === 'string' ? value : ''), (v) => (value = v)}
      />
    {:else if row.kind === 'object'}
      <FieldObject
        {id}
        fields={row.fields ?? []}
        bind:value={() => (typeof value === 'object' ? value : {}), (v) => (value = v)}
      />
    {:else if row.kind === 'curve'}
      <FieldCurve
        label="curve"
        {id}
        curves={curvesOfType(curves, row.curveType)}
        bind:value={() => (typeof value === 'string' ? value : ''), (v) => (value = v)}
      />
      {#if curvesError}<span class="hint" data-testid="control-curves-error">{curvesError}</span>{/if}
    {/if}
  {/if}
</div>

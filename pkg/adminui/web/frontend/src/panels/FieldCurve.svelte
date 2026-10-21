<script lang="ts">
  // The field for a curve value: a pick among the stored curves of the
  // row's curveType, already filtered by the caller.
  import type { CurveInfo } from '../lib/controlcatalog'
  let {
    label,
    id,
    curves,
    value = $bindable(''),
  }: { label: string; id: string; curves: CurveInfo[]; value?: string } = $props()
</script>

<label class="control-field">
  {label}
  <select data-testid={id} bind:value>
    <option value="">{curves.length === 0 ? '(no stored curve of this type)' : '(pick curve)'}</option>
    {#each curves as curve (curve.id)}
      <option value={curve.id}>{curve.description || curve.id}</option>
    {/each}
  </select>
</label>

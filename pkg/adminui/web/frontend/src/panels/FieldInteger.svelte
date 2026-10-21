<script lang="ts">
  // The field for an integer value. The text is kept as typed and parsed by
  // buildValue against min and max, so a bad entry is named on Send.
  let {
    label,
    id,
    min,
    max,
    required = true,
    value = $bindable(''),
  }: { label: string; id: string; min?: number; max?: number; required?: boolean; value?: string } = $props()
  const range = $derived(min !== undefined && max !== undefined ? `${min} to ${max}` : '')
</script>

<label class="control-field">
  {label}
  <input
    type="text"
    inputmode="numeric"
    data-testid={id}
    placeholder={range + (required ? '' : ' (optional)')}
    style="max-width: 140px;"
    bind:value
  />
</label>

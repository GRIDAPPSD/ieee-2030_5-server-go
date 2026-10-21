<script lang="ts">
  // The field for a boolean value: one checkbox.
  let { label, id, value = $bindable(false) }: { label: string; id: string; value?: boolean } = $props()
</script>

<label class="control-field">
  <input type="checkbox" data-testid={id} bind:checked={value} /> {label}
</label>

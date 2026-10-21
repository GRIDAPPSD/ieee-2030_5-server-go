<script lang="ts">
  // A destructive action behind one confirmation: the trigger opens it, Keep
  // closes it and returns focus to the trigger, and Confirm runs onConfirm.
  // `busy` marks this action as the one in flight; `disabled` holds every
  // action of the panel while any one is out.
  import { tick } from 'svelte'
  import { focusOnMount } from '../lib/focus'

  let {
    triggerId,
    confirmId,
    trigger,
    triggerLabel,
    confirm,
    confirmLabel,
    busyText,
    keepLabel,
    busy,
    disabled,
    onOpen,
    onConfirm,
  }: {
    triggerId: string
    confirmId: string
    trigger: string
    triggerLabel: string
    confirm: string
    confirmLabel: string
    busyText: string
    keepLabel: string
    busy: boolean
    disabled: boolean
    onOpen: () => void
    onConfirm: () => Promise<void>
  } = $props()

  let open = $state(false)
  let triggerButton = $state<HTMLButtonElement | undefined>()

  async function keep() {
    open = false
    await tick()
    triggerButton?.focus()
  }

  async function run() {
    await onConfirm()
    open = false
  }
</script>

{#if open}
  <button
    class="btn btn-red btn-small"
    data-testid={confirmId}
    aria-label={confirmLabel}
    {disabled}
    use:focusOnMount
    onclick={run}>{busy ? busyText : confirm}</button
  >
  <button class="btn btn-small" aria-label={keepLabel} disabled={busy} onclick={keep}>Keep</button>
{:else}
  <button
    class="btn btn-red btn-small"
    data-testid={triggerId}
    aria-label={triggerLabel}
    {disabled}
    bind:this={triggerButton}
    onclick={() => {
      open = true
      onOpen()
    }}>{trigger}</button
  >
{/if}

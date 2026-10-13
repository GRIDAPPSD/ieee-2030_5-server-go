<script lang="ts">
  // The multi-select control for a panel that declares a picker. It holds
  // the draft the viewer is editing and hands the ids to onapply; the
  // parent owns what is applied, stored and sent. Labels are interpolated
  // as text, and each sits in an isolated bidi context so a hostile label
  // cannot reorder the controls around it.
  import { untrack } from 'svelte'
  import type { PickerChoice } from '../lib/descriptor'

  let {
    choices,
    max,
    applied,
    departed,
    onapply,
  }: {
    choices: PickerChoice[]
    max: number
    applied: string[]
    departed: number
    onapply: (ids: string[]) => void
  } = $props()

  let draft = $state<string[]>(untrack(() => [...applied]))
  let query = $state('')

  const matches = $derived.by(() => {
    const q = query.trim().toLowerCase()
    return q === '' ? choices : choices.filter((c) => c.label.toLowerCase().includes(q))
  })
  const full = $derived(draft.length >= max)
  const selectAllFits = $derived(new Set([...draft, ...matches.map((c) => c.id)]).size <= max)

  function toggle(id: string, on: boolean) {
    if (on) {
      if (!draft.includes(id) && draft.length < max) draft = [...draft, id]
    } else {
      draft = draft.filter((d) => d !== id)
    }
  }

  function selectAll() {
    if (!selectAllFits) return
    const next = [...draft]
    for (const c of matches) if (!next.includes(c.id)) next.push(c.id)
    draft = next
  }

  // Ids that left the choices stop counting toward the cap, so Apply
  // never carries one.
  $effect(() => {
    const present = new Set(choices.map((c) => c.id))
    untrack(() => {
      if (draft.some((id) => !present.has(id))) draft = draft.filter((id) => present.has(id))
    })
  })

  function apply() {
    onapply([...draft])
  }
</script>

<div class="card picker" data-testid="picker">
  <div class="form-row">
    <input type="search" aria-label="Search choices" placeholder="Search" bind:value={query} />
    <span class="hint" role="status" data-testid="picker-count">{draft.length} of {max}</span>
    <button type="button" class="btn btn-small" onclick={() => (draft = [])}>Clear</button>
    <button
      type="button"
      class="btn btn-small"
      disabled={!selectAllFits}
      aria-describedby={selectAllFits ? undefined : 'picker-select-all-reason'}
      onclick={selectAll}
    >Select all</button>
    <button type="button" class="btn btn-small btn-green" onclick={apply}>Apply</button>
  </div>
  {#if !selectAllFits}
    <span id="picker-select-all-reason" class="hint">Select all is off: the matches would go past the limit of {max}.</span>
  {/if}
  {#if departed > 0}
    <p class="hint" role="status" data-testid="picker-departed">{departed} saved {departed === 1 ? 'choice is' : 'choices are'} no longer available.</p>
  {/if}
  <ul class="picker-list">
    {#each matches as choice (choice.id)}
      {@const checked = draft.includes(choice.id)}
      <li>
        <label>
          <input
            type="checkbox"
            {checked}
            disabled={!checked && full}
            onchange={(e) => toggle(choice.id, e.currentTarget.checked)}
          />
          <span class="picker-label" dir="auto">{choice.label}</span>
        </label>
      </li>
    {:else}
      <li class="hint" data-testid="picker-none">No matches.</li>
    {/each}
  </ul>
</div>

<style>
  .picker-list {
    list-style: none;
    margin: 8px 0 0;
    padding: 0;
    max-height: 240px;
    overflow-y: auto;
  }
  .picker-list label {
    display: flex;
    align-items: center;
    gap: 6px;
  }
  .picker-label {
    unicode-bidi: isolate;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
</style>

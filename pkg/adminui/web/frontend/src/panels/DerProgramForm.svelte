<script lang="ts">
  // The add and edit form for one DER program. It validates and builds the
  // request body but sends nothing: the caller supplies onSubmit and decides
  // which route the body goes to, so the FSAs tab (templates) and the
  // device Programs drawer share it. onSubmit reports a refusal as the text
  // to show, which keeps the server's wording in front of the operator.
  import { untrack } from 'svelte'
  import {
    buildProgramBody,
    CONTROL_KINDS,
    emptyForm,
    formFromProgram,
    type ControlKind,
    type ProgramBody,
    type ProgramView,
  } from '../lib/derprogram'

  let {
    mode,
    initial,
    noun = 'template',
    onSubmit,
    onCancel,
  }: {
    mode: 'create' | 'edit'
    initial?: ProgramView
    noun?: string
    onSubmit: (body: ProgramBody) => Promise<{ ok: true } | { ok: false; message: string }>
    onCancel?: () => void
  } = $props()

  // Read once: the form owns its fields from here, and the parent mounts a
  // fresh form per program being edited.
  let values = $state(untrack(() => (initial ? formFromProgram(initial) : emptyForm())))
  let error = $state('')
  let busy = $state(false)

  async function submit(event: SubmitEvent) {
    event.preventDefault()
    if (busy) return
    const built = buildProgramBody(values, { requireDefault: mode === 'create' })
    if (!built.ok) {
      error = built.error
      return
    }
    error = ''
    busy = true
    try {
      const res = await onSubmit(built.body)
      if (!res.ok) error = res.message
    } finally {
      busy = false
    }
  }
</script>

<form class="program-form" onsubmit={submit} novalidate data-testid="program-form">
  {#if mode === 'edit' && initial}
    <p class="hint">mRID <span class="mono" data-testid="program-form-mrid">{initial.mRID}</span> (cannot change)</p>
  {/if}
  <div class="form-row">
    <label>Description
      <input type="text" bind:value={values.description} />
    </label>
    <label>Primacy
      <input type="text" inputmode="numeric" bind:value={values.primacy} />
    </label>
    {#if mode === 'create'}
      <label>mRID (optional)
        <input type="text" placeholder="32 hex digits, generated if blank" bind:value={values.mRID} />
      </label>
    {/if}
  </div>
  <div class="form-row">
    <label>Default control
      <select
        value={values.controlType}
        onchange={(e) => (values.controlType = e.currentTarget.value as ControlKind | '')}
      >
        <option value="">{mode === 'create' ? '(pick a type)' : '(keep the current control)'}</option>
        {#each CONTROL_KINDS as c (c.kind)}
          <option value={c.kind}>{c.label}</option>
        {/each}
      </select>
    </label>
    {#if values.controlType === 'maxLimW'}
      <label>Max limit
        <input type="text" inputmode="numeric" bind:value={values.maxLimW} />
      </label>
    {:else if values.controlType === 'fixedPFInjectW'}
      <label>Displacement
        <input type="text" inputmode="numeric" placeholder="1 to 1000 (thousandths)" bind:value={values.displacement} />
      </label>
      <label>Excitation
        <select bind:value={values.excitation}>
          <option value="false">Over-excited: DER injects reactive power</option>
          <option value="true">Under-excited: DER absorbs reactive power</option>
        </select>
      </label>
    {:else if values.controlType === 'targetW'}
      <label>Target value
        <input type="text" inputmode="numeric" bind:value={values.targetValue} />
      </label>
      <label>Multiplier
        <input type="text" inputmode="numeric" placeholder="-9 to 9, 0 if blank" bind:value={values.targetMultiplier} />
      </label>
    {/if}
  </div>
  <div class="form-row">
    <button class="btn btn-green" type="submit" disabled={busy}>
      {mode === 'create' ? 'Add' : 'Save'} {noun}
    </button>
    {#if onCancel}
      <button class="btn" type="button" onclick={onCancel}>Cancel</button>
    {/if}
  </div>
  {#if error}
    <div class="result err" role="alert" data-testid="program-form-error">{error}</div>
  {/if}
</form>

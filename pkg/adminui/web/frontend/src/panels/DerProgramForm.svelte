<script lang="ts">
  // The add and edit form for one DER program. Its default control is set
  // from the control function catalog with the control window's field
  // components. It validates and builds the request body but sends nothing: the caller supplies onSubmit and decides
  // which route the body goes to, so the FSAs tab (templates) and the
  // device Programs drawer share it. onSubmit reports a refusal as the text
  // to show, which keeps the server's wording in front of the operator.
  //
  // `initial` is read once, when the form mounts: to edit a different
  // program, mount a fresh form (key it on the program id). `noun` names the
  // thing in the submit button and defaults to "template"; a caller adding a
  // device program passes its own.
  import { onMount, untrack } from 'svelte'
  import FunctionField from './FunctionField.svelte'
  import { focusOnMount } from '../lib/focus'
  import {
    buildFunctions,
    emptyFieldState,
    fieldStateFromFunctions,
    loadCatalog,
    loadCurves,
    uneditableFunctions,
    type CatalogRow,
    type CurveInfo,
    type FieldValue,
  } from '../lib/controlcatalog'
  import {
    buildProgramBody,
    emptyForm,
    formFromProgram,
    type DefaultControlInput,
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

  // Whether the program has a default control. A create always has one (the
  // server refuses a program without), so its box is ticked and locked; an
  // edit starts from what is stored, and unticking removes the default.
  let hasDefault = $state(untrack(() => mode === 'create' || Boolean(initial?.defaultControl)))

  // The default control is set from the control function catalog, with the
  // same field components as the control window. An edit starts with the
  // stored default's functions ticked and filled.
  let catalog = $state<CatalogRow[]>([])
  let catalogError = $state('')
  let catalogReady = $state(false)
  let curves = $state<CurveInfo[]>([])
  let curvesError = $state('')
  let ticked = $state<Record<string, boolean>>({})
  let fieldValues = $state<Record<string, FieldValue>>({})
  // Stored functions the catalog cannot edit here. Saving a changed default
  // sends them back as they are, so a save never drops one.
  let kept = $state<Record<string, unknown>>({})
  // Cleared on unmount so a reply that lands afterwards writes nothing.
  let mounted = true

  onMount(() => {
    void load()
    return () => {
      mounted = false
    }
  })

  async function load() {
    const [cat, cur] = await Promise.all([loadCatalog(), loadCurves()])
    if (!mounted) return
    if (!cat.ok) {
      catalogError = cat.error
    } else {
      catalogError = ''
      catalogReady = true
      catalog = cat.data
      const state = untrack(() =>
        initial?.defaultControl ? fieldStateFromFunctions(cat.data, initial.defaultControl.functions) : emptyFieldState(cat.data),
      )
      ticked = state.ticked
      kept = untrack(() => uneditableFunctions(cat.data, initial?.defaultControl?.functions))
      fieldValues = state.values
    }
    if (!cur.ok) {
      curvesError = cur.error
    } else {
      curvesError = ''
      curves = cur.data
    }
  }

  function defaultControl(): DefaultControlInput {
    if (!hasDefault) return null
    // Until the catalog loads it cannot show the stored default, so an edit
    // leaves it alone rather than refusing the save.
    if (!catalogReady && mode === 'edit') return null
    if (!catalog.some((row) => ticked[row.name]) && Object.keys(kept).length === 0) {
      return { ok: false, error: 'defaultControl: tick at least one function' }
    }
    const built = buildFunctions(catalog, ticked, fieldValues)
    return built.ok ? { ok: true, functions: { ...built.functions, ...kept } } : built
  }

  async function submit(event: SubmitEvent) {
    event.preventDefault()
    if (busy) return
    const built = buildProgramBody(values, {
      requireDefault: mode === 'create',
      defaultControl: defaultControl(),
      clearDefault: mode === 'edit' && !hasDefault && Boolean(initial?.defaultControl),
    })
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
  <fieldset class="default-control" data-testid="program-form-default">
    <label>
      <input type="checkbox" bind:checked={hasDefault} disabled={mode === 'create'} data-testid="program-form-has-default" />
      Default control
    </label>
    {#if mode === 'edit' && !hasDefault}
      <p class="hint">Saving removes this program's default control.</p>
    {/if}
    {#if hasDefault && Object.keys(kept).length > 0}
      <p class="hint" data-testid="program-form-uneditable">
        This default also holds {Object.keys(kept).join(', ')}, which this form cannot edit. Saving a changed default keeps
        {Object.keys(kept).length === 1 ? 'it' : 'them'} as stored.
      </p>
    {/if}
    {#if hasDefault}
      {#if catalogError}
        <div class="result err" data-testid="program-form-catalog-error">Functions could not be read: {catalogError}</div>
      {/if}
      {#each catalog as row (row.name)}
        <FunctionField {row} bind:ticked={ticked[row.name]} bind:value={fieldValues[row.name]} {curves} {curvesError} />
      {/each}
    {/if}
  </fieldset>
  <div class="form-row">
    <button class="btn btn-green" type="submit" disabled={busy}>
      {mode === 'create' ? 'Add' : 'Save'} {noun}
    </button>
    {#if onCancel}
      <button class="btn" type="button" onclick={onCancel}>Cancel</button>
    {/if}
  </div>
  {#if error}
    {#key error}
      <div class="result err" role="alert" tabindex="-1" use:focusOnMount data-testid="program-form-error">{error}</div>
    {/key}
  {/if}
</form>

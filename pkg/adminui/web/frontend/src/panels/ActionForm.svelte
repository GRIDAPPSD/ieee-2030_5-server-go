<script lang="ts">
  // One panel action as a form. The checks mirror the server's; the server
  // stays the authority and its refusal is shown as it words it. Values
  // stay in the form after any outcome, so a refused 16 KiB text is
  // corrected rather than retyped. The component owns the in-flight guard:
  // while a request is out, a second submit does nothing.
  import { onDestroy } from 'svelte'
  import { postJSON } from '../lib/api'
  import {
    ACTION_TIMEOUT_MS,
    actionURL,
    bodyTooLarge,
    buildBody,
    describeOutcome,
    validateForm,
    type ActionSpec,
    type FieldValue,
    type Outcome,
  } from '../lib/actions'

  let {
    panelId,
    action,
    onbusy,
  }: { panelId: string; action: ActionSpec; onbusy?: (busy: boolean) => void } = $props()

  const fields = $derived(action.fields)
  // A lone switch is the whole action: one click sends the flipped value.
  const loneToggle = $derived(fields.length === 1 && fields[0].kind === 'toggle')

  let values = $state<Record<string, FieldValue>>({})
  let fieldErrors = $state<Record<string, string>>({})
  let outcome = $state<Outcome | null>(null)
  let inFlight = $state(false)
  let destroyed = false
  const ctrl = new AbortController()

  onDestroy(() => {
    destroyed = true
    ctrl.abort()
  })

  function initial(kind: string): FieldValue {
    if (kind === 'boolean') return false
    return kind === 'toggle' ? undefined : ''
  }

  function value(name: string, kind: string): FieldValue {
    return values[name] ?? initial(kind)
  }

  async function send(sent: Record<string, FieldValue>, onOk?: () => void) {
    const problems = validateForm(fields, sent)
    fieldErrors = problems
    outcome = null
    if (Object.keys(problems).length > 0) return
    const body = buildBody(fields, sent)
    if (bodyTooLarge(body)) {
      outcome = { kind: 'error', text: 'The submission is too large for the server (16 KiB once encoded).' }
      return
    }
    inFlight = true
    onbusy?.(true)
    const res = await postJSON<unknown>(actionURL(panelId, action.id), body, {
      signal: ctrl.signal,
      timeoutMs: ACTION_TIMEOUT_MS,
    })
    if (destroyed) return
    inFlight = false
    onbusy?.(false)
    const result = describeOutcome(res, fields.map((f) => f.name))
    outcome = result
    if (result.kind === 'refused' && result.field !== undefined) {
      fieldErrors = { [result.field]: result.text }
    }
    if (result.kind === 'ok') onOk?.()
    // The server may have flipped it, so the last accepted state is stale.
    if (result.kind === 'unknown' && loneToggle) values[fields[0].name] = undefined
  }

  function submit(event: SubmitEvent) {
    event.preventDefault()
    if (inFlight) return
    const sent: Record<string, FieldValue> = {}
    for (const f of fields) sent[f.name] = value(f.name, f.kind)
    void send(sent)
  }

  // The shown state changes only when the server accepts the value.
  function set(name: string, next: boolean) {
    if (inFlight) return
    void send({ [name]: next }, () => {
      values[name] = next
    })
  }

  function flip(name: string) {
    if (loneToggle) set(name, value(name, 'toggle') !== true)
    else if (!inFlight) values[name] = value(name, 'toggle') !== true
  }

  function bytes(text: string): number {
    return new TextEncoder().encode(text).length
  }
</script>

<form class="card" data-testid={`action-${action.id}`} onsubmit={submit}>
  <h3>{action.label}</h3>
  {#each fields as f (f.name)}
    {@const inputId = `action-${action.id}-${f.name}`}
    {@const err = fieldErrors[f.name]}
    <div class="form-row action-field">
      {#if f.problem}
        <span id={inputId + '-label'}>{f.label}</span>
        <span class="result err" role="alert" data-testid={inputId + '-problem'}>{f.problem}</span>
      {:else if f.kind === 'toggle'}
        {@const state = value(f.name, f.kind)}
        <span id={inputId + '-label'}>{f.label}</span>
        {#if loneToggle && state === undefined}
          <span class="hint" data-testid={inputId + '-unknown'}>Unknown</span>
          <button type="button" class="btn" disabled={inFlight} onclick={() => set(f.name, true)}>On</button>
          <button type="button" class="btn" disabled={inFlight} onclick={() => set(f.name, false)}>Off</button>
        {:else}
        <button
          type="button"
          class="btn"
          role="switch"
          aria-checked={state === true}
          aria-labelledby={inputId + '-label'}
          data-testid={inputId}
          disabled={inFlight}
          onclick={() => flip(f.name)}
        >{state === undefined ? 'Unknown' : state ? 'On' : 'Off'}</button>
        {/if}
      {:else if f.kind === 'boolean'}
        <label>
          <input type="checkbox" id={inputId} checked={value(f.name, f.kind) === true} onchange={(e) => (values[f.name] = e.currentTarget.checked)} />
          {f.label}
        </label>
      {:else if f.kind === 'choice'}
        <label for={inputId}>{f.label}</label>
        <select id={inputId} value={value(f.name, f.kind) as string} onchange={(e) => (values[f.name] = e.currentTarget.value)}>
          <option value="" disabled>Choose...</option>
          {#each f.choices ?? [] as c (c.id)}
            <option value={c.id}>{c.label}</option>
          {/each}
        </select>
      {:else if f.kind === 'integer'}
        <label for={inputId}>{f.label}</label>
        <input
          type="text"
          inputmode="numeric"
          autocomplete="off"
          id={inputId}
          value={value(f.name, f.kind) as string}
          oninput={(e) => (values[f.name] = e.currentTarget.value)}
        />
        <span class="hint">{f.min} to {f.max}</span>
      {:else}
        <label for={inputId}>{f.label}</label>
        <textarea
          id={inputId}
          rows="6"
          spellcheck="false"
          value={value(f.name, f.kind) as string}
          oninput={(e) => (values[f.name] = e.currentTarget.value)}
        ></textarea>
        <span class="hint">{bytes(value(f.name, f.kind) as string)} of {f.maxLen} bytes</span>
      {/if}
    </div>
    {#if err}
      <div class="result err" role="alert" data-testid={`${inputId}-error`}>{err}</div>
    {/if}
  {/each}
  {#if !loneToggle}
    <div class="form-row">
      <button class="btn" type="submit" disabled={inFlight}>{inFlight ? 'Working...' : 'Run'}</button>
    </div>
  {/if}
  {#if outcome}
    <div
      class="result"
      class:ok={outcome.kind === 'ok'}
      class:err={outcome.kind !== 'ok'}
      role={outcome.kind === 'ok' ? 'status' : 'alert'}
      data-testid={`action-${action.id}-outcome`}
      data-kind={outcome.kind}
    >{outcome.text}</div>
  {/if}
</form>

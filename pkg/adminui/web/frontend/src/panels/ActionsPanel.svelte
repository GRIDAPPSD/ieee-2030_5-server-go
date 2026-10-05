<script lang="ts">
  // A panel's actions. The schema is read once when the panel opens and on
  // Reload, because a choice field's list is the embedder's live answer. The
  // effect owns the read: a panel swap or unmount aborts it.
  import { fetchJSON } from '../lib/api'
  import { actionURL, parseActions, type ActionSpec } from '../lib/actions'
  import { PANEL_REQUEST_TIMEOUT_MS } from '../lib/descriptor'
  import ActionForm from './ActionForm.svelte'

  let { id }: { id: string } = $props()

  let specs = $state<ActionSpec[] | null>(null)
  let error = $state('')
  let reloads = $state(0)
  // Reload rebuilds every form, which would drop an outcome the operator has
  // not yet seen, so it waits for every request to end.
  let busy = $state<Record<string, boolean>>({})
  const anyBusy = $derived(Object.values(busy).some(Boolean))

  function describe(status: number, message: string): string {
    switch (status) {
      case 429:
        return 'Too many requests. Wait a few seconds, then reload.'
      case 503:
        return 'The server is shutting down or canceled the request.'
      case 504:
        return 'The panel did not answer in time.'
      case 401:
        return 'Admin session required. Sign in again.'
      case 404:
        return 'No actions are available for this panel.'
      default:
        return `Could not load the actions: ${message}`
    }
  }

  $effect(() => {
    void reloads
    const panelId = id
    const ctrl = new AbortController()
    specs = null
    error = ''
    busy = {}
    void fetchJSON<unknown>(actionURL(panelId), { signal: ctrl.signal, timeoutMs: PANEL_REQUEST_TIMEOUT_MS }).then((res) => {
      if (ctrl.signal.aborted) return
      if (!res.ok) {
        error = describe(res.status, res.error)
        return
      }
      const parsed = parseActions(res.data)
      if (parsed === null) error = 'The server sent actions that could not be read.'
      else specs = parsed
    })
    return () => ctrl.abort()
  })
</script>

<div class="card full-width" data-testid="actions-panel">
  <h2>Actions</h2>
  {#if error}
    <div class="result err" role="alert" data-testid="actions-error">{error}</div>
  {:else if specs === null}
    <p class="hint" data-testid="actions-loading">Loading...</p>
  {/if}
  {#if specs}
    {#each specs as spec (spec.id)}
      <ActionForm panelId={id} action={spec} onbusy={(b) => (busy[spec.id] = b)} />
    {/each}
  {/if}
  <div class="form-row">
    <button class="btn" type="button" disabled={anyBusy} onclick={() => reloads++}>Reload</button>
  </div>
</div>

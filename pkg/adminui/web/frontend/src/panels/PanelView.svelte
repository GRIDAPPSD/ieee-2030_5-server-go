<script lang="ts">
  // One embedder panel: fetches its Descriptor and polls while open. The
  // effect owns the timer and the in-flight request, and its teardown
  // runs when the panel id changes or the view is destroyed, so a swap
  // never leaves a poll running against a panel nobody is looking at.
  import { fetchJSON } from '../lib/api'
  import {
    PANEL_POLL_MS,
    PANEL_REQUEST_TIMEOUT_MS,
    parseChoices,
    readSelection,
    writeSelection,
    type Descriptor,
    type PanelPickerInfo,
    type PickerChoice,
  } from '../lib/descriptor'
  import DescriptorPanel from './DescriptorPanel.svelte'
  import PanelPicker from './PanelPicker.svelte'

  let { id, picker }: { id: string; picker?: PanelPickerInfo } = $props()

  // After an Apply aborts a read, the server's View may still be running,
  // and the next read can get a 504 for that reason alone. One silent
  // retry after this delay covers it.
  const APPLY_RETRY_MS = 1000

  let descriptor = $state<Descriptor | null>(null)
  let error = $state('')
  let choices = $state<PickerChoice[] | null>(null)
  let known = $state<string[]>([])
  let departed = $state(0)
  let shownId: string | undefined

  const max = $derived(picker?.max)
  // The applied selection is the stored one until the viewer applies a new
  // one for this panel.
  const stored = $derived(max === undefined ? [] : readSelection(id, max))
  let override = $state<{ id: string; ids: string[] } | null>(null)
  const applied = $derived(override !== null && override.id === id ? override.ids : stored)
  let retryAfterApply = false

  function onapply(ids: string[]) {
    writeSelection(id, ids)
    retryAfterApply = true
    override = { id, ids }
  }

  function describe(status: number, message: string): string {
    switch (status) {
      case 504:
        return 'This panel did not answer in time. It will be retried.'
      case 503:
        return 'The request for this panel was canceled. It will be retried.'
      case 401:
        return 'Admin session required. Sign in again.'
      case 404:
        return 'No such panel.'
      case 400:
        return 'The server refused this selection. The last view is still shown.'
      default:
        return `Could not load this panel: ${message}`
    }
  }

  $effect(() => {
    const panelId = id
    const hasPicker = picker !== undefined
    const cap = picker?.max ?? 0
    const selection = applied
    let retry = retryAfterApply
    retryAfterApply = false
    const ctrl = new AbortController()
    let timer: ReturnType<typeof setTimeout> | undefined
    // A re-run for the same panel (an Apply) keeps the last good view; a
    // different panel starts clean.
    if (shownId !== panelId) {
      shownId = panelId
      descriptor = null
      error = ''
      choices = null
      known = []
      departed = 0
    }

    // Returns the ids to send, or null when the choices could not be read.
    async function loadChoices(): Promise<string[] | null> {
      const res = await fetchJSON<unknown>(`/api/ui/panels/${encodeURIComponent(panelId)}/choices`, {
        signal: ctrl.signal,
        timeoutMs: PANEL_REQUEST_TIMEOUT_MS,
      })
      if (ctrl.signal.aborted) return null
      const parsed = res.ok ? parseChoices(res.data) : null
      if (parsed === null) {
        error = res.ok ? 'The server sent a list of choices that could not be read.' : `Could not load the choices: ${res.error}`
        return null
      }
      // A stored id that has left the choices is never sent, so a departed
      // device cannot turn every poll into a refused request.
      const present = new Set(parsed.choices.map((c) => c.id))
      const sendable = selection.filter((sid) => present.has(sid)).slice(0, cap)
      choices = parsed.choices
      known = sendable
      departed = selection.length - sendable.length
      return sendable
    }

    async function load() {
      let sel: string[] = []
      if (hasPicker) {
        const ids = await loadChoices()
        if (ctrl.signal.aborted) return
        if (ids === null) {
          timer = setTimeout(load, PANEL_POLL_MS)
          return
        }
        sel = ids
      }
      let path = `/api/ui/panels/${encodeURIComponent(panelId)}`
      if (sel.length > 0) {
        const q = new URLSearchParams()
        for (const sid of sel) q.append('sel', sid)
        path += `?${q.toString()}`
      }
      const res = await fetchJSON<Descriptor>(path, { signal: ctrl.signal, timeoutMs: PANEL_REQUEST_TIMEOUT_MS })
      if (ctrl.signal.aborted) return
      if (!res.ok && res.status === 504 && retry) {
        retry = false
        timer = setTimeout(load, APPLY_RETRY_MS)
        return
      }
      retry = false
      if (res.ok && typeof res.data === 'object' && res.data !== null) {
        descriptor = res.data
        error = ''
      } else if (res.ok) {
        descriptor = null
        error = 'The server sent a reply for this panel that could not be read.'
      } else {
        // A refused selection keeps the last good view beside its message.
        if (res.status !== 400) descriptor = null
        error = describe(res.status, res.error)
      }
      timer = setTimeout(load, PANEL_POLL_MS)
    }
    void load()

    return () => {
      ctrl.abort()
      clearTimeout(timer)
    }
  })
</script>

{#if picker && choices}
  <PanelPicker {choices} max={picker.max} applied={known} {departed} {onapply} />
{/if}
{#if error}
  <div class="card result err" role="alert" data-testid="panel-error">{error}</div>
{/if}
{#if descriptor !== null}
  <DescriptorPanel {descriptor} />
{:else if !error}
  <p class="hint" data-testid="panel-loading">Loading...</p>
{/if}

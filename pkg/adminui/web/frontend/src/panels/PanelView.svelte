<script lang="ts">
  // One embedder panel: fetches its Descriptor and polls while open. The
  // effect owns the timer and the in-flight request, and its teardown
  // runs when the panel id changes or the view is destroyed, so a swap
  // never leaves a poll running against a panel nobody is looking at.
  import { fetchJSON } from '../lib/api'
  import { PANEL_POLL_MS, PANEL_REQUEST_TIMEOUT_MS, type Descriptor } from '../lib/descriptor'
  import DescriptorPanel from './DescriptorPanel.svelte'

  let { id }: { id: string } = $props()

  let descriptor = $state<Descriptor | null>(null)
  let error = $state('')

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
      default:
        return `Could not load this panel: ${message}`
    }
  }

  $effect(() => {
    const panelId = id
    const ctrl = new AbortController()
    let timer: ReturnType<typeof setTimeout> | undefined
    descriptor = null
    error = ''

    async function load() {
      const res = await fetchJSON<Descriptor>(`/api/ui/panels/${encodeURIComponent(panelId)}`, {
        signal: ctrl.signal,
        timeoutMs: PANEL_REQUEST_TIMEOUT_MS,
      })
      if (ctrl.signal.aborted) return
      if (res.ok) {
        descriptor = res.data
        error = ''
      } else {
        descriptor = null
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

{#if error}
  <div class="card result err" role="alert" data-testid="panel-error">{error}</div>
{:else if descriptor === null}
  <p class="hint" data-testid="panel-loading">Loading...</p>
{:else}
  <DescriptorPanel {descriptor} />
{/if}

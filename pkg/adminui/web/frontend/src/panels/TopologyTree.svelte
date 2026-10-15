<script lang="ts">
  // GET /api/topology renders the SY -> FD -> SP -> DEV tree with the
  // FSAs attached at each level. The panel owns nothing: the tree and its
  // refresh are supplied by the dashboard so a create/attach/delete
  // anywhere on the page refreshes this one place.
  import TopologyNodeView from './TopologyNodeView.svelte'
  import { listAllPrograms, type ProgramListItem } from '../lib/derprogram'
  import type { TopologyNode } from '../lib/fsa'

  let {
    tree,
    error,
    onRefresh,
  }: {
    tree: TopologyNode | null
    error: string
    onRefresh: () => void
  } = $props()

  // The attach inputs offer every program on the server (GET
  // /api/der-programs) through one shared datalist. The input stays a text
  // field, so a href the list does not hold can still be typed. Each tree
  // refresh reads the list again, since a template added on this tab changes
  // it.
  let programs = $state<ProgramListItem[]>([])
  let pickerError = $state('')

  $effect(() => {
    void tree
    const ctrl = new AbortController()
    void (async () => {
      const res = await listAllPrograms({ signal: ctrl.signal })
      if (ctrl.signal.aborted) return
      if (res.ok) {
        programs = res.data.programs
        pickerError = ''
      } else {
        programs = []
        pickerError = `Could not load the program list (${res.status}): ${res.error}`
      }
    })()
    return () => ctrl.abort()
  })

  function label(p: ProgramListItem): string {
    return `${p.description} (device ${p.device}, ${p.fsa}, ${p.origin})`
  }
</script>

<div class="card full-width">
  <h2>FSA Tree (SY -&gt; FD -&gt; SP -&gt; DEV)</h2>
  <div class="hint">
    <button class="btn" onclick={onRefresh}>Refresh</button>
    <span style="margin-left: 8px;">Click a node to expand or collapse.</span>
  </div>
  {#if pickerError}
    <div class="result err" role="alert" data-testid="program-picker-error">{pickerError}</div>
  {/if}
  <datalist id="attachPrograms">
    {#each programs as p (p.href)}
      <option value={p.href} label={label(p)}></option>
    {/each}
  </datalist>
  <div id="topologyTree" class="tree">
    {#if error}
      <span data-testid="topology-error">Error loading topology: {error}</span>
    {:else if tree}
      <TopologyNodeView node={tree} depth={0} onChanged={onRefresh} />
    {/if}
  </div>
</div>

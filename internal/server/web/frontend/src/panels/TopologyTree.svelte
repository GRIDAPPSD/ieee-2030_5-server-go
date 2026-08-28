<script lang="ts">
  // GET /api/topology renders the SY -> FD -> SP -> DEV tree with the
  // FSAs attached at each level. The panel owns nothing: the tree and its
  // refresh are supplied by the dashboard so a create/attach/delete
  // anywhere on the page refreshes this one place.
  import TopologyNodeView from './TopologyNodeView.svelte'
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
</script>

<div class="card full-width">
  <h2>FSA Tree (SY -&gt; FD -&gt; SP -&gt; DEV)</h2>
  <div class="hint">
    <button class="btn" onclick={onRefresh}>Refresh</button>
    <span style="margin-left: 8px;">Click a node to expand or collapse.</span>
  </div>
  <div id="topologyTree" class="tree">
    {#if error}
      <span data-testid="topology-error">Error loading topology: {error}</span>
    {:else if tree}
      <TopologyNodeView node={tree} depth={0} onChanged={onRefresh} />
    {/if}
  </div>
</div>

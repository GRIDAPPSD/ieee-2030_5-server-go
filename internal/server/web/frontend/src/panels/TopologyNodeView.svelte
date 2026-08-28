<script lang="ts">
  // One SY/FD/SP/DEV level of the topology tree, rendered recursively by
  // importing itself. Collapse state is local to the node rather than
  // held in a map on the panel: expanding a branch is a display concern
  // and no longer costs a topology refetch.
  import Self from './TopologyNodeView.svelte'
  import FsaNode from './FsaNode.svelte'
  import { nodeColor, type TopologyNode } from '../lib/fsa'

  let {
    node,
    depth,
    onChanged,
  }: {
    node: TopologyNode
    depth: number
    onChanged: () => void
  } = $props()

  let collapsed = $state(false)

  let hasChildren = $derived((node.children?.length ?? 0) > 0 || (node.fsas?.length ?? 0) > 0)
  let prefix = $derived(hasChildren ? (collapsed ? '[+] ' : '[-] ') : '    ')
  let suffix = $derived(
    node.kind === 'DEV' ? ` (SFDI=${node.sfdi || '-'}, ${node.enabled ? 'ON' : 'OFF'})` : '',
  )
</script>

<div style="margin-left: {depth * 16}px; color: {nodeColor(node.kind)};">
  <div
    class="node-header"
    style="cursor: {hasChildren ? 'pointer' : 'default'};"
    data-testid="topology-node-{node.kind}-{node.id}"
    onclick={() => (collapsed = !collapsed)}
    onkeydown={(e) => {
      if (e.key === 'Enter' || e.key === ' ') collapsed = !collapsed
    }}
    role="button"
    tabindex="0"
  >
    {prefix}{node.kind} {node.label || node.id}{suffix}
  </div>

  {#if !collapsed}
    {#each node.fsas ?? [] as fsa (fsa.mRID)}
      <FsaNode {fsa} depth={depth + 1} allowDelete={node.kind === 'SY'} {onChanged} />
    {/each}
    {#each node.children ?? [] as child (child.kind + ':' + child.id)}
      <Self node={child} depth={depth + 1} {onChanged} />
    {/each}
  {/if}
</div>

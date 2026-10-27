<script lang="ts">
  // One typed cell. Every value is {expression} interpolation, never
  // {@html}. A kind this build does not know renders its text.
  import { badgeClass, safeHref, type DescriptorCell } from '../lib/descriptor'

  let { cell }: { cell: DescriptorCell } = $props()

  let href = $derived(cell.kind === 'link' ? safeHref(cell.href) : null)
</script>

{#if cell.kind === 'badge'}
  <span class="badge {badgeClass(cell.badge)}" data-testid="descriptor-badge">{cell.text}</span>
{:else if cell.kind === 'time'}
  <time datetime={cell.datetime}>{cell.text}</time>
{:else if cell.kind === 'link' && href !== null}
  <a {href} rel="noopener noreferrer" data-testid="descriptor-link">{cell.text}</a>
{:else}
  <span>{cell.text}</span>
{/if}

<style>
  .badge-neutral {
    background: var(--border);
    color: var(--text);
  }
  .badge-info {
    background: var(--accent-solid);
    color: var(--text-on-solid);
  }
  .badge-ok {
    background: var(--status-ok-bg);
    color: var(--text);
  }
  .badge-warn {
    background: var(--status-warn-bg);
    color: var(--text);
  }
  .badge-error {
    background: var(--status-fault-bg);
    color: var(--text);
  }
</style>

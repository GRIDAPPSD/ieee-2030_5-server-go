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
    background: #1e3a5f;
    color: #93c5fd;
  }
  .badge-ok {
    background: #14532d;
    color: #86efac;
  }
  .badge-warn {
    background: #713f12;
    color: #fde68a;
  }
  .badge-error {
    background: #7f1d1d;
    color: #fca5a5;
  }
</style>

<script lang="ts">
  // A link to another admin tab. A plain click moves within the SPA; a
  // modifier key or non-primary button keeps the browser's own handling, the
  // same rule the tab bar follows.
  import type { Snippet } from 'svelte'
  import { navigate } from '../lib/router'

  // onNavigate runs just before a plain click navigates, for a caller that
  // must leave a request for the tab it is moving to.
  let { path, testid, onNavigate, children }: { path: string; testid?: string; onNavigate?: () => void; children: Snippet } = $props()

  function onClick(event: MouseEvent) {
    if (event.defaultPrevented || event.button !== 0) return
    if (event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) return
    event.preventDefault()
    onNavigate?.()
    navigate(path)
  }
</script>

<a href={path} data-testid={testid} onclick={onClick}>{@render children()}</a>

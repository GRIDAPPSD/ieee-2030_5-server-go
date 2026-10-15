<script lang="ts">
  // A link to another admin tab. A plain click moves within the SPA; a
  // modifier key or non-primary button keeps the browser's own handling, the
  // same rule the tab bar follows.
  import type { Snippet } from 'svelte'
  import { navigate } from '../lib/router'

  let { path, children }: { path: string; children: Snippet } = $props()

  function onClick(event: MouseEvent) {
    if (event.defaultPrevented || event.button !== 0) return
    if (event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) return
    event.preventDefault()
    navigate(path)
  }
</script>

<a href={path} onclick={onClick}>{@render children()}</a>

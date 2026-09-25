<script lang="ts">
  // The SPA's shell: just the current route's component, resolved from
  // currentPath via the routes table. The dashboard renders its own
  // header (src/panels/NavBar.svelte), so there is no chrome here to
  // duplicate it.
  import { currentPath } from './lib/router'
  import { routes, defaultRoute } from './routes'

  let Route = $derived(routes[$currentPath] ?? defaultRoute)
</script>

<Route />

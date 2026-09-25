// A minimal client side router for the admin UI shell: the History API plus
// a Svelte store, so a future panel registers a route (src/routes/index.ts)
// without pulling in a routing library. The admin UI is a small, single
// operator surface, not a general purpose web app.
//
// This router only distinguishes between the SPA's own client side routes;
// it never issues a request to /api. The server side fallback (spa.go's
// handler serving index.html for any unknown non-/api path under /ui/) is
// what makes a hard reload on a client side route still work.

import { readable } from 'svelte/store'

// currentPath is the current window.location.pathname, updated on
// popstate (back/forward) and on every navigate() call below. The store
// re-syncs to window.location on every (re)subscription, not only at
// module load: a subscriber that attaches after something else moved
// window.location (a test rendering a second component in the same
// module instance; a hard reload landing past this module's first
// evaluation) must see the current path, not the value frozen when this
// module first ran.
export const currentPath = readable(window.location.pathname, (set) => {
  set(window.location.pathname)
  const onPopState = () => set(window.location.pathname)
  window.addEventListener('popstate', onPopState)
  return () => window.removeEventListener('popstate', onPopState)
})

// navigate pushes a new history entry for path and notifies currentPath's
// subscribers by dispatching a synthetic popstate, keeping the store's
// single source of truth as window.location rather than a second,
// potentially-divergent piece of state.
export function navigate(path: string): void {
  if (path === window.location.pathname) return
  window.history.pushState({}, '', path)
  window.dispatchEvent(new PopStateEvent('popstate'))
}

// replace swaps the current history entry's URL instead of adding a new
// one, for normalizing a canonical redirect (a tab path's trailing slash,
// issue 561 criterion 6) so the back button does not have to skip past it.
export function replace(path: string): void {
  if (path === window.location.pathname) return
  window.history.replaceState({}, '', path)
  window.dispatchEvent(new PopStateEvent('popstate'))
}

// Route describes one client side route: the exact pathname it matches and
// a human label for the nav link. Panels register their own Route entries
// in src/routes/index.ts as they land; this file stays generic.
export interface Route {
  path: string
  label: string
}

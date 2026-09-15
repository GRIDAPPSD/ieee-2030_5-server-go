// The SPA's route table. Each entry maps an exact pathname to the Svelte
// component that renders it, plus a human label for the nav
// (src/lib/router.ts's Route interface).
//
// The dashboard is registered at both "/" and "/ui/": the admin listener
// serves the built index.html at "/" (internal/server/dashboard.go) as
// well as under the SPA's own /ui/ mount, and the client side router
// matches on the exact pathname, so both need an entry.

import type { Component } from 'svelte'
import type { Route } from '../lib/router'
import AdminShell from '../AdminShell.svelte'

export const routeList: Route[] = [{ path: '/', label: 'Dashboard' }]

export const routes: Record<string, Component> = {
  '/': AdminShell,
  '/ui/': AdminShell,
}

export const defaultRoute: Component = AdminShell

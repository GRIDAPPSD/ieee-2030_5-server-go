// The SPA's route table. Each entry maps an exact pathname to the Svelte
// component that renders it, plus a human label for the nav
// (src/lib/router.ts's Route interface).
//
// The dashboard is registered at both "/" and "/ui/": the admin listener
// serves the built index.html at "/" (internal/server/dashboard.go) as
// well as under the SPA's own /ui/ mount, and the client side router
// matches on the exact pathname, so both need an entry.
//
// The five tab paths map to the same AdminShell reference as "/" and
// "/ui/", not to per-tab components: AdminShell reads the active tab from
// currentPath itself (issue 561), so every admin UI path resolves to one
// component identity and a tab switch never remounts the shell.

import type { Component } from 'svelte'
import type { Route } from '../lib/router'
import AdminShell from '../AdminShell.svelte'

export const routeList: Route[] = [
  { path: '/', label: 'Dashboard' },
  { path: '/ui/overview', label: 'Overview' },
  { path: '/ui/devices', label: 'Devices' },
  { path: '/ui/fsas', label: 'FSAs' },
  { path: '/ui/control', label: 'Control' },
  { path: '/ui/certificates', label: 'Certificates' },
]

export const routes: Record<string, Component> = {
  '/': AdminShell,
  '/ui/': AdminShell,
  '/ui/overview': AdminShell,
  '/ui/devices': AdminShell,
  '/ui/fsas': AdminShell,
  '/ui/control': AdminShell,
  '/ui/certificates': AdminShell,
}

export const defaultRoute: Component = AdminShell

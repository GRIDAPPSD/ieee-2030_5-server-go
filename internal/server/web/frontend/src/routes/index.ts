// The SPA's route table. Each entry maps an exact pathname (relative to the
// /ui/ base the shell is mounted at, internal/server/spa.go) to the Svelte
// component that renders it, plus a human label for the nav
// (src/lib/router.ts's Route interface). App.svelte only needs to import
// routeList/routes/defaultRoute from this table, never the individual
// route components directly.

import type { Component } from 'svelte'
import type { Route } from '../lib/router'
import Shell from './Shell.svelte'

export const routeList: Route[] = [{ path: '/ui/', label: 'Shell' }]

export const routes: Record<string, Component> = {
  '/ui/': Shell,
}

export const defaultRoute: Component = Shell

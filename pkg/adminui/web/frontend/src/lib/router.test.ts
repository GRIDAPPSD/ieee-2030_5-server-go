import { afterEach, describe, expect, it } from 'vitest'
import { get } from 'svelte/store'
import { currentPath, currentSearch, navigate, replace } from './router'

afterEach(() => window.history.replaceState({}, '', '/'))

describe('router query handling', () => {
  it('navigate carries a query into currentSearch and treats a changed query as a new location', () => {
    window.history.replaceState({}, '', '/ui/logs?device=A')
    const before = window.history.length
    navigate('/ui/logs?device=A')
    expect(window.history.length).toBe(before)

    navigate('/ui/logs?device=B')
    expect(window.history.length).toBe(before + 1)
    expect(get(currentPath)).toBe('/ui/logs')
    expect(get(currentSearch)).toBe('?device=B')
  })

  it('a path with no query does not push just to drop the query the page loaded with', () => {
    window.history.replaceState({}, '', '/ui/devices?token=t')
    const before = window.history.length
    navigate('/ui/devices')
    expect(window.history.length).toBe(before)
    expect(window.location.search).toBe('?token=t')
  })

  it('replace swaps the query without adding an entry', () => {
    window.history.replaceState({}, '', '/ui/logs?device=A')
    const before = window.history.length
    replace('/ui/logs?device=B')
    expect(window.history.length).toBe(before)
    expect(get(currentSearch)).toBe('?device=B')
  })
})

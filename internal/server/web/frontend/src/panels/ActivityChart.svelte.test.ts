// The chart's library is bundled, not fetched, so mounting the panel with
// no network available at all must still produce an initialized chart.
// jsdom supplies no 2D canvas context, hence the stub; that the chart
// visibly DRAWS with outbound egress blocked is asserted in a real
// browser by e2e/chart_offline.spec.ts.
import { describe, expect, it, vi } from 'vitest'
import { render, waitFor } from '@testing-library/svelte'
import ActivityChart from './ActivityChart.svelte'
import { installCanvasStub } from '../test-canvas-stub'

installCanvasStub()

describe('ActivityChart', () => {
  it('initializes the bundled chart library with no network request of any kind', async () => {
    const fetchSpy = vi.spyOn(globalThis, 'fetch')

    const { container } = render(ActivityChart, {
      props: { history: [{ time: '00:00:05', devices: 2, mups: 1 }] },
    })

    const mount = container.querySelector('#activityChart') as HTMLElement
    expect(mount).not.toBeNull()
    await waitFor(() => expect(mount.querySelector('canvas')).not.toBeNull())
    expect(fetchSpy).not.toHaveBeenCalled()
    expect(container.querySelector('[data-testid="chart-error"]')).toBeNull()
  })

  it('does not inject a script element pointing at any external origin', () => {
    render(ActivityChart, { props: { history: [] } })

    const external = Array.from(document.querySelectorAll('script[src]')).filter((s) =>
      /^(https?:)?\/\//.test(s.getAttribute('src') ?? ''),
    )
    expect(external).toEqual([])
  })
})

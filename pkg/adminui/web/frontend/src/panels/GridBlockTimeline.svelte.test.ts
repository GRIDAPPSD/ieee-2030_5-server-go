// The block timeline's own duties: it measures its tracks with a
// ResizeObserver it must disconnect, and it says so for blocks it cannot draw.
import { afterEach, describe, expect, it, vi } from 'vitest'
import { cleanup, render, screen } from '@testing-library/svelte'
import GridBlockTimeline from './GridBlockTimeline.svelte'
import type { BlockEntry } from '../lib/gridblocks'

const NOW_MS = Date.UTC(2031, 0, 1, 12, 0, 0)
const NOW = NOW_MS / 1000

function entry(row: number, startOffsetS: number, durationS: number): BlockEntry {
  return {
    batch: 'b1',
    block: {
      row,
      direction: row % 2 === 1 ? 'discharge' : 'charge',
      requestedW: 1000,
      encodedW: 1000,
      differenceW: 0,
      start: NOW + startOffsetS,
      end: NOW + startOffsetS + durationS,
      durationSeconds: durationS,
      status: 'scheduled',
      basisChanged: false,
      deliveredW: null,
      shares: [],
    },
  }
}

function observerClass(observe: () => void, disconnect: () => void) {
  return class {
    observe = observe
    disconnect = disconnect
    unobserve = () => {}
  }
}

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
})

describe('GridBlockTimeline', () => {
  it('observes its tracks once and disconnects the observer on unmount', () => {
    const observe = vi.fn()
    const disconnect = vi.fn()
    vi.stubGlobal('ResizeObserver', observerClass(observe, disconnect))
    const view = render(GridBlockTimeline, { entries: [entry(1, 600, 600)], nowMs: NOW_MS })
    expect(observe).toHaveBeenCalledTimes(1)
    expect(disconnect).not.toHaveBeenCalled()
    view.unmount()
    expect(disconnect).toHaveBeenCalledTimes(1)
  })

  it('names the blocks it leaves off: one ended over an hour ago, one starting after 24 hours', () => {
    render(GridBlockTimeline, {
      entries: [entry(1, -7200, 600), entry(2, 600, 600), entry(3, 25 * 3600, 600)],
      nowMs: NOW_MS,
    })
    expect(screen.getAllByTestId('events-bar').map((b) => b.dataset.block)).toEqual(['b1:2'])
    expect(screen.getByTestId('grid-timeline-earlier')).toHaveTextContent('1 block ended more than an hour ago and is listed below only')
    expect(screen.getByTestId('grid-timeline-beyond')).toHaveTextContent('1 block starts after the next 24 hours')
  })

  it('says nothing about omitted blocks when every block is drawn', () => {
    render(GridBlockTimeline, { entries: [entry(1, 600, 600)], nowMs: NOW_MS })
    expect(screen.queryByTestId('grid-timeline-earlier')).toBeNull()
    expect(screen.queryByTestId('grid-timeline-beyond')).toBeNull()
  })
})

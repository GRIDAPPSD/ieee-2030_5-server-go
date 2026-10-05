// StreamPanel owns an EventSource. jsdom has none, so a fake records every
// instance; each test unmounts what it mounts and asserts the fake's
// closed flag, which is what frees the server's stream slot.
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen } from '@testing-library/svelte'
import { tick } from 'svelte'
import StreamPanel from './StreamPanel.svelte'

class FakeEventSource {
  static instances: FakeEventSource[] = []
  static readonly CONNECTING = 0
  static readonly OPEN = 1
  static readonly CLOSED = 2
  onopen: (() => void) | null = null
  onmessage: ((ev: MessageEvent) => void) | null = null
  onerror: (() => void) | null = null
  readyState = 0
  closed = false
  constructor(public url: string) {
    FakeEventSource.instances.push(this)
  }
  close() {
    this.closed = true
    this.readyState = 2
  }
  emit(data: string) {
    this.onmessage?.(new MessageEvent('message', { data }))
  }
}

const stream = { maxLen: 8, charset: 'abc123' }
const ev = (kind: string, text: string, i = 1) => JSON.stringify({ id: String(i), time: '2026-10-05T00:00:00Z', kind, text })

beforeEach(() => {
  FakeEventSource.instances = []
  vi.stubGlobal('EventSource', FakeEventSource)
})

afterEach(() => {
  vi.unstubAllGlobals()
})

async function start(value: string) {
  await fireEvent.input(screen.getByLabelText('Parameter'), { target: { value } })
  await fireEvent.click(screen.getByRole('button', { name: 'Start' }))
}

describe('StreamPanel', () => {
  it('opens nothing until a valid value is submitted, and shows why when it is not', async () => {
    const { unmount } = render(StreamPanel, { props: { id: 'p', label: 'Feed', stream } })
    await start('zzz')
    expect(FakeEventSource.instances).toHaveLength(0)
    expect(screen.getByTestId('stream-validation')).toHaveTextContent('does not accept')
    await start('')
    expect(FakeEventSource.instances).toHaveLength(0)
    expect(screen.getByTestId('stream-validation')).toHaveTextContent('Enter a value')
    unmount()
  })

  it('opens one stream at the encoded URL on submit', async () => {
    const { unmount } = render(StreamPanel, { props: { id: 'my panel', label: 'Feed', stream } })
    await start('abc')
    expect(FakeEventSource.instances.map((i) => i.url)).toEqual(['/api/ui/panels/my%20panel/stream?param=abc'])
    unmount()
  })

  it('appends messages and shows status events distinctly', async () => {
    const { unmount } = render(StreamPanel, { props: { id: 'p', label: 'Feed', stream } })
    await start('abc')
    const src = FakeEventSource.instances[0]
    src.emit(ev('message', 'hello', 1))
    src.emit(ev('status', 'replay done', 2))
    src.emit('garbage')
    await tick()
    const items = screen.getAllByRole('listitem')
    expect(items.map((li) => li.getAttribute('data-kind'))).toEqual(['message', 'status', 'status'])
    expect(items[0]).toHaveTextContent('hello')
    expect(items[0]).not.toHaveClass('stream-status')
    expect(items[1]).toHaveClass('stream-status')
    expect(items[2]).toHaveTextContent('could not be read')
    unmount()
  })

  it('caps the log at 1000 lines, newest kept', async () => {
    const { unmount } = render(StreamPanel, { props: { id: 'p', label: 'Feed', stream } })
    await start('abc')
    const src = FakeEventSource.instances[0]
    for (let i = 1; i <= 1005; i++) src.emit(ev('message', `line-${i}`, i))
    await tick()
    const items = screen.getAllByRole('listitem')
    expect(items).toHaveLength(1000)
    expect(items[0]).toHaveTextContent('line-6')
    expect(items[999]).toHaveTextContent('line-1005')
    unmount()
  })

  it('closes the stream on unmount', async () => {
    const { unmount } = render(StreamPanel, { props: { id: 'p', label: 'Feed', stream } })
    await start('abc')
    expect(FakeEventSource.instances[0].closed).toBe(false)
    unmount()
    expect(FakeEventSource.instances[0].closed).toBe(true)
  })

  it('closes the old stream and clears the log when a new value is submitted', async () => {
    const { unmount } = render(StreamPanel, { props: { id: 'p', label: 'Feed', stream } })
    await start('abc')
    FakeEventSource.instances[0].emit(ev('message', 'old'))
    await tick()
    await start('123')
    expect(FakeEventSource.instances).toHaveLength(2)
    expect(FakeEventSource.instances[0].closed).toBe(true)
    expect(FakeEventSource.instances[1].closed).toBe(false)
    expect(screen.queryAllByRole('listitem')).toHaveLength(0)
    unmount()
  })

  it('closes the stream on Stop', async () => {
    const { unmount } = render(StreamPanel, { props: { id: 'p', label: 'Feed', stream } })
    await start('abc')
    await fireEvent.click(screen.getByRole('button', { name: 'Stop' }))
    expect(FakeEventSource.instances[0].closed).toBe(true)
    expect(screen.getByTestId('stream-connection')).toHaveTextContent('idle')
    unmount()
  })

  it('says so when the server refuses the stream', async () => {
    const { unmount } = render(StreamPanel, { props: { id: 'p', label: 'Feed', stream } })
    await start('abc')
    const src = FakeEventSource.instances[0]
    src.readyState = FakeEventSource.CLOSED
    src.onerror?.()
    await tick()
    expect(screen.getByTestId('stream-connection')).toHaveTextContent('closed')
    expect(screen.getByRole('listitem')).toHaveTextContent('refused')
    unmount()
  })
})

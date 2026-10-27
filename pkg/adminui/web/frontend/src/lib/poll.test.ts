import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { startPoll } from './poll'

beforeEach(() => vi.useFakeTimers())
afterEach(() => vi.useRealTimers())

describe('startPoll', () => {
  it('runs now, then after each returned delay', async () => {
    const step = vi.fn(async () => 100)
    const stop = startPoll(step)
    await vi.advanceTimersByTimeAsync(0)
    expect(step).toHaveBeenCalledTimes(1)
    await vi.advanceTimersByTimeAsync(99)
    expect(step).toHaveBeenCalledTimes(1)
    await vi.advanceTimersByTimeAsync(1)
    expect(step).toHaveBeenCalledTimes(2)
    stop()
  })

  it('ends when the step returns undefined', async () => {
    const step = vi.fn(async () => undefined)
    startPoll(step)
    await vi.advanceTimersByTimeAsync(10000)
    expect(step).toHaveBeenCalledTimes(1)
  })

  it('aborts the signal on stop and schedules nothing from a read that settles after it', async () => {
    let signal!: AbortSignal
    let settle!: () => void
    const step = vi.fn((s: AbortSignal) => {
      signal = s
      return new Promise<number | undefined>((resolve) => (settle = () => resolve(100)))
    })
    const stop = startPoll(step)
    await vi.advanceTimersByTimeAsync(0)
    stop()
    expect(signal.aborted).toBe(true)
    settle()
    await vi.advanceTimersByTimeAsync(1000)
    expect(step).toHaveBeenCalledTimes(1)
  })

  it('clears a pending wait on stop', async () => {
    const step = vi.fn(async () => 100)
    const stop = startPoll(step)
    await vi.advanceTimersByTimeAsync(0)
    stop()
    await vi.advanceTimersByTimeAsync(1000)
    expect(step).toHaveBeenCalledTimes(1)
  })
})

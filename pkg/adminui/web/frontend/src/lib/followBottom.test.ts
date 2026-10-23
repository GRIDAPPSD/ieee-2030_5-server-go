import { describe, expect, it } from 'vitest'
import { BOTTOM_SLACK, followBottom } from './followBottom'

function box(scrollHeight: number, clientHeight: number, scrollTop: number) {
  const el = document.createElement('div')
  Object.defineProperty(el, 'scrollHeight', { value: scrollHeight, configurable: true })
  Object.defineProperty(el, 'clientHeight', { value: clientHeight, configurable: true })
  el.scrollTop = scrollTop
  return el
}

describe('followBottom', () => {
  it('follows at the bottom, stops when the reader scrolls up, and resumes on follow()', () => {
    const el = box(1000, 400, 600)
    const f = followBottom(() => el)
    expect(f.following).toBe(true)

    el.scrollTop = 600 - BOTTOM_SLACK
    f.onscroll()
    expect(f.following).toBe(false)

    el.scrollTop = 600 - BOTTOM_SLACK + 1
    f.onscroll()
    expect(f.following).toBe(true)

    el.scrollTop = 100
    f.onscroll()
    expect(f.following).toBe(false)
    f.follow()
    expect(f.following).toBe(true)
  })

  it('pins to the end after the update only while following', async () => {
    const el = box(1000, 400, 0)
    const f = followBottom(() => el)
    f.pinAfterUpdate()
    await Promise.resolve()
    await Promise.resolve()
    expect(el.scrollTop).toBe(1000)

    el.scrollTop = 100
    f.onscroll()
    f.pinAfterUpdate()
    await Promise.resolve()
    await Promise.resolve()
    expect(el.scrollTop).toBe(100)
  })

  it('does not pin if the reader scrolled up between the update and the flush', async () => {
    const el = box(1000, 400, 600)
    const f = followBottom(() => el)
    f.pinAfterUpdate()
    el.scrollTop = 100
    f.onscroll()
    await Promise.resolve()
    await Promise.resolve()
    expect(el.scrollTop).toBe(100)
  })
})

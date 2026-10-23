// Follow-the-bottom state for a scroll box that grows at its end. The panel
// owns the flag: it stays set while the reader is within BOTTOM_SLACK of the
// end, clears when they scroll up, and only then do new lines stop moving the view.
import { tick } from 'svelte'

// Pixels from the end that still count as being at the bottom.
export const BOTTOM_SLACK = 8

export function followBottom(box: () => HTMLElement | undefined) {
  let following = true
  const pin = () => {
    const el = box()
    if (el) el.scrollTop = el.scrollHeight
  }
  return {
    get following() {
      return following
    },
    onscroll() {
      const el = box()
      if (el) following = el.scrollHeight - el.scrollTop - el.clientHeight < BOTTOM_SLACK
    },
    follow() {
      following = true
    },
    pin,
    // Pins once the DOM has taken the new lines, if the reader is still following.
    pinAfterUpdate() {
      if (!following) return
      void tick().then(() => {
        if (following) pin()
      })
    },
  }
}

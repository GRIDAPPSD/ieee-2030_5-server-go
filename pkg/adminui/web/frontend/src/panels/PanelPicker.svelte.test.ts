// PanelPicker holds the draft selection and nothing else. Labels come from
// the server and are untrusted, so the label tests assert on the DOM the
// browser would build, not on a string.
import { describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen } from '@testing-library/svelte'
import PanelPicker from './PanelPicker.svelte'
import type { PickerChoice } from '../lib/descriptor'

const many = (n: number): PickerChoice[] =>
  Array.from({ length: n }, (_, i) => ({ id: `id${i}`, label: `Battery ${i}` }))

const props = (over: Partial<Parameters<typeof render<typeof PanelPicker>>[1]> = {}) => ({
  choices: many(49),
  max: 16,
  applied: [] as string[],
  departed: 0,
  onapply: vi.fn(),
  ...over,
})

describe('PanelPicker', () => {
  it('renders an HTML label and a bidi-override label as text in an isolated element', () => {
    const html = '<img src=x onerror=alert(1)>'
    const bidi = 'ab\u202Ecd\u2066ef'
    const { container, unmount } = render(PanelPicker, {
      props: props({
        choices: [
          { id: 'a', label: html },
          { id: 'b', label: bidi },
        ],
      }),
    })

    expect(container.querySelector('img')).toBeNull()
    const labels = [...container.querySelectorAll('.picker-label')]
    expect(labels.map((l) => l.textContent)).toEqual([html, bidi])
    for (const l of labels) expect(l.getAttribute('dir')).toBe('auto')
    unmount()
  })

  it('filters the choices by the search text, ignoring case', async () => {
    const { unmount } = render(PanelPicker, { props: props() })
    await fireEvent.input(screen.getByRole('searchbox', { name: 'Search choices' }), { target: { value: 'BATTERY 4' } })

    const names = screen.getAllByRole('checkbox').map((c) => c.closest('label')?.textContent?.trim())
    expect(names).toEqual(['Battery 4', 'Battery 40', 'Battery 41', 'Battery 42', 'Battery 43', 'Battery 44', 'Battery 45', 'Battery 46', 'Battery 47', 'Battery 48'])
    unmount()
  })

  it('stops at 16: the counter reads 16 of 16 and every unchecked box is disabled', async () => {
    const { unmount } = render(PanelPicker, { props: props() })
    for (const box of screen.getAllByRole('checkbox').slice(0, 16)) await fireEvent.click(box)

    expect(screen.getByTestId('picker-count')).toHaveTextContent('16 of 16')
    const boxes = screen.getAllByRole('checkbox') as HTMLInputElement[]
    expect(boxes.filter((b) => b.checked)).toHaveLength(16)
    expect(boxes.filter((b) => !b.checked).every((b) => b.disabled)).toBe(true)
    expect(boxes.filter((b) => !b.checked)).toHaveLength(33)
    unmount()
  })

  it('enables Select all only when the matches fit, then ticks exactly the matches', async () => {
    const { unmount } = render(PanelPicker, { props: props() })
    const all = screen.getByRole('button', { name: 'Select all' })
    expect(all).toBeDisabled()

    await fireEvent.input(screen.getByRole('searchbox'), { target: { value: 'battery 1' } })
    // Battery 1, 10 to 19: 11 matches.
    expect(all).toBeEnabled()
    await fireEvent.click(all)
    expect(screen.getByTestId('picker-count')).toHaveTextContent('11 of 16')

    await fireEvent.input(screen.getByRole('searchbox'), { target: { value: 'battery 3' } })
    // Ten more matches would make 21, which does not fit.
    expect(all).toBeDisabled()
    unmount()
  })

  it('Clear unticks everything', async () => {
    const { unmount } = render(PanelPicker, { props: props({ applied: ['id1', 'id2'] }) })
    expect(screen.getByTestId('picker-count')).toHaveTextContent('2 of 16')
    await fireEvent.click(screen.getByRole('button', { name: 'Clear' }))

    expect(screen.getByTestId('picker-count')).toHaveTextContent('0 of 16')
    expect((screen.getAllByRole('checkbox') as HTMLInputElement[]).some((b) => b.checked)).toBe(false)
    unmount()
  })

  it('Apply hands over the ticked ids in tick order and no others', async () => {
    const onapply = vi.fn()
    const { unmount } = render(PanelPicker, { props: props({ onapply }) })
    await fireEvent.click(screen.getByRole('checkbox', { name: 'Battery 7' }))
    await fireEvent.click(screen.getByRole('checkbox', { name: 'Battery 3' }))
    await fireEvent.click(screen.getByRole('button', { name: 'Apply' }))

    expect(onapply).toHaveBeenCalledTimes(1)
    expect(onapply).toHaveBeenCalledWith(['id7', 'id3'])
    unmount()
  })

  it('shows a count for departed saved choices and never their ids', () => {
    const { container, unmount } = render(PanelPicker, { props: props({ departed: 3 }) })

    expect(screen.getByTestId('picker-departed')).toHaveTextContent('3 saved choices are no longer available.')
    expect(container.textContent).not.toMatch(/gone-id/)
    unmount()
  })
})

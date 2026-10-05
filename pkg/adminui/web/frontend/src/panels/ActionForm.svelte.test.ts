// ActionForm owns the in-flight guard and the form values. postJSON is
// mocked with a promise the test settles, so "in flight" is observable.
import { afterEach, describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen } from '@testing-library/svelte'
import ActionForm from './ActionForm.svelte'
import * as api from '../lib/api'
import { ACTION_TIMEOUT_MS, type ActionSpec } from '../lib/actions'

afterEach(() => {
  vi.restoreAllMocks()
})

const multi: ActionSpec = {
  id: 'send',
  label: 'Send',
  fields: [
    { name: 'device', label: 'Device', kind: 'choice', choices: [{ id: 'dev-1', label: 'Device one' }] },
    { name: 'level', label: 'Level', kind: 'integer', min: 0, max: 100 },
    { name: 'raw', label: 'Raw', kind: 'text', maxLen: 64 },
  ],
}
const lone: ActionSpec = { id: 'pub', label: 'Publishing', fields: [{ name: 'on', label: 'Publishing', kind: 'toggle' }] }

type Res = Awaited<ReturnType<typeof api.postJSON>>
function deferred() {
  let resolve!: (r: Res) => void
  const promise = new Promise<Res>((r) => (resolve = r))
  return { promise, resolve }
}
const fail = (status: number, error: string, body?: unknown): Res => ({ ok: false, status, error, body })

async function fill(raw = '{"a":1}', level = '5') {
  await fireEvent.change(screen.getByLabelText('Device'), { target: { value: 'dev-1' } })
  await fireEvent.input(screen.getByLabelText('Level'), { target: { value: level } })
  await fireEvent.input(screen.getByLabelText('Raw'), { target: { value: raw } })
}
const run = () => fireEvent.click(screen.getByRole('button', { name: 'Run' }))
const outcome = () => screen.getByTestId('action-send-outcome')

describe('ActionForm', () => {
  it('posts the typed values to the action path and shows the result', async () => {
    const spy = vi.spyOn(api, 'postJSON').mockResolvedValue({ ok: true, data: { ok: true, message: 'queued 1' } })
    const { unmount } = render(ActionForm, { props: { panelId: 'bridge', action: multi } })
    await fill()
    await run()
    expect(spy).toHaveBeenCalledTimes(1)
    expect(spy.mock.calls[0][0]).toBe('/api/ui/panels/bridge/actions/send')
    expect(spy.mock.calls[0][1]).toEqual({ device: 'dev-1', level: 5, raw: '{"a":1}' })
    expect(outcome()).toHaveTextContent('queued 1')
    expect(outcome().dataset.kind).toBe('ok')
    unmount()
  })

  it('sends nothing and names the field when a value fails the schema', async () => {
    const spy = vi.spyOn(api, 'postJSON')
    const { unmount } = render(ActionForm, { props: { panelId: 'p', action: multi } })
    await fill('x', '101')
    await run()
    expect(spy).not.toHaveBeenCalled()
    expect(screen.getByTestId('action-send-level-error')).toHaveTextContent('Enter a number from 0 to 100.')
    expect(screen.queryByTestId('action-send-raw-error')).toBeNull()
    unmount()
  })

  it('disables Run while a request is out and ignores a second submit', async () => {
    const d = deferred()
    const spy = vi.spyOn(api, 'postJSON').mockReturnValue(d.promise)
    const { unmount } = render(ActionForm, { props: { panelId: 'p', action: multi } })
    await fill()
    await run()
    expect(screen.getByRole('button', { name: 'Working...' })).toBeDisabled()
    await fireEvent.submit(screen.getByTestId('action-send'))
    expect(spy).toHaveBeenCalledTimes(1)
    d.resolve({ ok: true, data: { ok: true, message: 'done' } })
    expect(await screen.findByRole('button', { name: 'Run' })).toBeEnabled()
    unmount()
  })

  it('shows a 422 refusal reason and keeps every typed value, the raw text included', async () => {
    vi.spyOn(api, 'postJSON').mockResolvedValue(fail(422, 'publishing is off'))
    const { unmount } = render(ActionForm, { props: { panelId: 'p', action: multi } })
    await fill('line1\nline2')
    await run()
    expect(outcome()).toHaveTextContent('publishing is off')
    expect(outcome().dataset.kind).toBe('refused')
    expect(screen.getByLabelText('Raw')).toHaveValue('line1\nline2')
    expect(screen.getByLabelText('Level')).toHaveValue('5')
    expect(screen.getByLabelText('Device')).toHaveValue('dev-1')
    unmount()
  })

  it('puts a 400 that names a field beside that field', async () => {
    vi.spyOn(api, 'postJSON').mockResolvedValue(fail(400, 'invalid action values', { error: 'invalid action values', field: 'device' }))
    const { unmount } = render(ActionForm, { props: { panelId: 'p', action: multi } })
    await fill()
    await run()
    expect(screen.getByTestId('action-send-device-error')).toHaveTextContent('The server refused this value')
    unmount()
  })

  it('shows the server sentence for a 504, marks it unknown, and never retries', async () => {
    vi.useFakeTimers()
    const s = 'action did not answer in time; it may still complete, so do not retry blindly'
    const spy = vi.spyOn(api, 'postJSON').mockResolvedValue(fail(504, s))
    const { unmount } = render(ActionForm, { props: { panelId: 'p', action: multi } })
    await fill()
    await run()
    expect(outcome()).toHaveTextContent(s)
    expect(outcome().dataset.kind).toBe('unknown')
    await vi.advanceTimersByTimeAsync(120_000)
    expect(spy).toHaveBeenCalledTimes(1)
    vi.useRealTimers()
    unmount()
  })

  it('words 429 and 503 plainly', async () => {
    const spy = vi.spyOn(api, 'postJSON').mockResolvedValueOnce(fail(429, 'too many panel actions'))
    const { unmount } = render(ActionForm, { props: { panelId: 'p', action: multi } })
    await fill()
    await run()
    expect(outcome()).toHaveTextContent('Too many actions in a short time')
    spy.mockResolvedValueOnce(fail(503, 'plane is shutting down'))
    await run()
    expect(outcome()).toHaveTextContent('shutting down or canceled the request')
    unmount()
  })

  it('renders server text as text, never as markup', async () => {
    vi.spyOn(api, 'postJSON').mockResolvedValue(fail(422, '<img src=x onerror=alert(1)>'))
    const { unmount, container } = render(ActionForm, { props: { panelId: 'p', action: multi } })
    await fill()
    await run()
    expect(outcome()).toHaveTextContent('<img src=x onerror=alert(1)>')
    expect(container.querySelector('img')).toBeNull()
    unmount()
  })

  it('offers explicit On and Off while the state is unknown, and sends the one clicked', async () => {
    const spy = vi.spyOn(api, 'postJSON').mockResolvedValue({ ok: true, data: { ok: true, message: 'publishing off' } })
    const { unmount } = render(ActionForm, { props: { panelId: 'p', action: lone } })
    expect(screen.getByTestId('action-pub-on-unknown')).toHaveTextContent('Unknown')
    expect(screen.queryByRole('switch')).toBeNull()
    await fireEvent.click(screen.getByRole('button', { name: 'Off' }))
    expect(spy.mock.calls[0][1]).toEqual({ on: false })
    await screen.findByText('publishing off')
    expect(screen.getByRole('switch', { name: 'Publishing' })).toHaveTextContent('Off')
    unmount()
  })

  it('flips a known toggle with one click, and shows the new state only once the server accepts', async () => {
    const d = deferred()
    const spy = vi.spyOn(api, 'postJSON').mockResolvedValueOnce({ ok: true, data: { ok: true, message: 'publishing on' } })
    const { unmount } = render(ActionForm, { props: { panelId: 'p', action: lone } })
    await fireEvent.click(screen.getByRole('button', { name: 'On' }))
    const sw = await screen.findByRole('switch', { name: 'Publishing' })
    expect(sw).toHaveTextContent('On')
    expect(sw).toHaveAttribute('aria-checked', 'true')

    spy.mockReturnValue(d.promise)
    await fireEvent.click(sw)
    expect(spy.mock.calls[1][1]).toEqual({ on: false })
    expect(sw).toBeDisabled()
    expect(sw).toHaveTextContent('On')
    d.resolve({ ok: true, data: { ok: true, message: 'publishing off' } })
    await screen.findByText('publishing off')
    expect(sw).toHaveTextContent('Off')
    unmount()
  })

  it('leaves a toggle where it was when the server refuses the flip', async () => {
    const spy = vi.spyOn(api, 'postJSON').mockResolvedValueOnce({ ok: true, data: { ok: true, message: 'on' } })
    const { unmount } = render(ActionForm, { props: { panelId: 'p', action: lone } })
    await fireEvent.click(screen.getByRole('button', { name: 'On' }))
    const sw = await screen.findByRole('switch')
    spy.mockResolvedValueOnce(fail(422, 'cannot switch off'))
    await fireEvent.click(sw)
    await screen.findByText('cannot switch off')
    expect(sw).toHaveTextContent('On')
    unmount()
  })

  it.each([
    ['a 504', fail(504, 'action did not answer in time; it may still complete, so do not retry blindly')],
    ['an aborted request', fail(0, 'request timed out')],
    ['a 503 cancel', fail(503, 'request canceled; the action may still complete, so do not retry blindly')],
  ])('goes back to Unknown after %s, since the server may have flipped', async (_name, res) => {
    const spy = vi.spyOn(api, 'postJSON').mockResolvedValueOnce({ ok: true, data: { ok: true, message: 'on' } })
    const { unmount } = render(ActionForm, { props: { panelId: 'p', action: lone } })
    await fireEvent.click(screen.getByRole('button', { name: 'On' }))
    const sw = await screen.findByRole('switch')
    expect(sw).toHaveTextContent('On')
    spy.mockResolvedValueOnce(res)
    await fireEvent.click(sw)
    expect(await screen.findByTestId('action-pub-on-unknown')).toHaveTextContent('Unknown')
    expect(screen.queryByRole('switch')).toBeNull()
    unmount()
  })

  it('bounds the request and aborts it when the form is unmounted', async () => {
    const spy = vi.spyOn(api, 'postJSON').mockReturnValue(new Promise(() => {}))
    const { unmount } = render(ActionForm, { props: { panelId: 'p', action: multi } })
    await fill()
    await run()
    const opts = spy.mock.calls[0][2]
    expect(opts?.timeoutMs).toBe(ACTION_TIMEOUT_MS)
    expect(opts?.signal?.aborted).toBe(false)
    unmount()
    expect(opts?.signal?.aborted).toBe(true)
  })

  it('tells its owner when a request starts and ends', async () => {
    const d = deferred()
    vi.spyOn(api, 'postJSON').mockReturnValue(d.promise)
    const onbusy = vi.fn()
    const { unmount } = render(ActionForm, { props: { panelId: 'p', action: multi, onbusy } })
    await fill()
    await run()
    expect(onbusy.mock.calls).toEqual([[true]])
    d.resolve({ ok: true, data: { ok: true, message: 'done' } })
    await screen.findByText('done')
    expect(onbusy.mock.calls).toEqual([[true], [false]])
    unmount()
  })

  it('holds a toggle inside a larger form until Run, and refuses Run while it is unset', async () => {
    const spy = vi.spyOn(api, 'postJSON').mockResolvedValue({ ok: true, data: { ok: true, message: 'ok' } })
    const action: ActionSpec = {
      id: 'send',
      label: 'Send',
      fields: [
        { name: 'on', label: 'On', kind: 'toggle' },
        { name: 'ack', label: 'Acknowledge', kind: 'boolean' },
      ],
    }
    const { unmount } = render(ActionForm, { props: { panelId: 'p', action } })
    await run()
    expect(spy).not.toHaveBeenCalled()
    expect(screen.getByTestId('action-send-on-error')).toHaveTextContent('Set this switch')
    await fireEvent.click(screen.getByRole('switch', { name: 'On' }))
    expect(spy).not.toHaveBeenCalled()
    await fireEvent.click(screen.getByLabelText('Acknowledge'))
    await run()
    expect(spy.mock.calls[0][1]).toEqual({ on: true, ack: true })
    unmount()
  })
})

// The form is shared by the FSAs tab and the device Programs drawer, so the
// tests pin what it hands its caller (the exact request body) and what it
// shows when the caller reports a refusal, not how it is laid out. Its default
// control is built from the catalog, so each test serves one.
import { afterEach, describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen, waitFor } from '@testing-library/svelte'
import DerProgramForm from './DerProgramForm.svelte'
import { CATALOG_ROUTES, FIXTURE_ROWS } from '../lib/controlcatalog.fixture'
import type { ProgramBody, ProgramView } from '../lib/derprogram'

type Result = { ok: true } | { ok: false; message: string }

afterEach(() => {
  vi.restoreAllMocks()
})

function serveCatalog(routes: Record<string, { status: number; body?: unknown }> = CATALOG_ROUTES) {
  vi.spyOn(globalThis, 'fetch').mockImplementation((input, init) => {
    const r = routes[`${init?.method ?? 'GET'} ${String(input)}`] ?? { status: 404, body: { error: 'no route' } }
    return Promise.resolve({
      ok: r.status >= 200 && r.status < 300,
      status: r.status,
      json: () => Promise.resolve(r.body),
    } as Response)
  })
}

async function mountCreate(onSubmit: (b: ProgramBody) => Promise<Result> = () => Promise.resolve({ ok: true })) {
  serveCatalog()
  const spy = vi.fn(onSubmit)
  render(DerProgramForm, { props: { mode: 'create', onSubmit: spy } })
  // The catalog arrives after mount; its rows are the default control.
  await screen.findByTestId('control-fn-fnFlag')
  return spy
}

async function fill(label: string, value: string) {
  await fireEvent.input(screen.getByLabelText(label), { target: { value } })
}

async function tick(name: string) {
  await fireEvent.click(screen.getByTestId(`control-fn-${name}`))
}

async function fillBasics() {
  await fill('Description', 'roof fleet')
  await fill('Primacy', '65')
}

describe('DerProgramForm create', () => {
  it('submits the typed description, primacy, optional mRID and every ticked function with its value', async () => {
    const onSubmit = await mountCreate()
    await fillBasics()
    await fill('mRID (optional)', '0123456789ABCDEF0123456789ABCDEF')
    await tick('fnFlag')
    await fireEvent.click(screen.getByTestId('control-fv-fnFlag'))
    await tick('fnLevel')
    await fireEvent.input(screen.getByTestId('control-fv-fnLevel'), { target: { value: '5000' } })
    await tick('fnPair')
    await fireEvent.input(screen.getByTestId('control-fv-fnPair-amount'), { target: { value: '950' } })
    await fireEvent.click(screen.getByTestId('control-fv-fnPair-sign'))
    await tick('fnCurve')
    await fireEvent.change(screen.getByTestId('control-fv-fnCurve'), { target: { value: 'cv11' } })
    await fireEvent.click(screen.getByRole('button', { name: 'Add template' }))

    await waitFor(() => expect(onSubmit).toHaveBeenCalledTimes(1))
    expect(onSubmit).toHaveBeenCalledWith({
      description: 'roof fleet',
      primacy: 65,
      mRID: '0123456789ABCDEF0123456789ABCDEF',
      defaultControl: {
        functions: { fnFlag: true, fnLevel: 5000, fnPair: { amount: 950, sign: true }, fnCurve: 'cv11' },
      },
    })
  })

  it('shows the value field of a function only while it is ticked', async () => {
    await mountCreate()
    expect(screen.queryByTestId('control-fv-fnLevel')).toBeNull()
    await tick('fnLevel')
    expect(screen.getByTestId('control-fv-fnLevel')).toBeInTheDocument()
    await tick('fnLevel')
    expect(screen.queryByTestId('control-fv-fnLevel')).toBeNull()
  })

  it('refuses a bad value with the control form wording and does not call the caller', async () => {
    const onSubmit = await mountCreate()
    await fillBasics()
    await tick('fnLevel')
    await fireEvent.input(screen.getByTestId('control-fv-fnLevel'), { target: { value: '10001' } })
    await fireEvent.click(screen.getByRole('button', { name: 'Add template' }))

    expect(await screen.findByRole('alert')).toHaveTextContent('fnLevel must be a whole number from 0 to 10000.')
    expect(onSubmit).not.toHaveBeenCalled()
  })

  it('refuses invalid input with the server wording and does not call the caller', async () => {
    const onSubmit = await mountCreate()
    await fill('Description', 'roof fleet')
    await fill('Primacy', '3')
    await tick('fnFlag')
    await fireEvent.click(screen.getByRole('button', { name: 'Add template' }))

    expect(await screen.findByRole('alert')).toHaveTextContent('primacy: must be 0, 1, 2 or 65 to 191')
    expect(onSubmit).not.toHaveBeenCalled()
  })

  it('moves focus to the error when one appears, validation or refusal', async () => {
    await mountCreate(() => Promise.resolve({ ok: false, message: 'Error (409): taken' }))
    await fireEvent.click(screen.getByRole('button', { name: 'Add template' }))
    await waitFor(() => expect(screen.getByRole('alert')).toHaveFocus())
    expect(screen.getByRole('alert')).toHaveTextContent('description')

    await fill('Description', 'd')
    await fill('Primacy', '1')
    await tick('fnFlag')
    await fireEvent.click(screen.getByRole('button', { name: 'Add template' }))
    await waitFor(() => expect(screen.getByRole('alert')).toHaveTextContent('Error (409): taken'))
    expect(screen.getByRole('alert')).toHaveFocus()
  })

  it('requires a ticked function on create', async () => {
    const onSubmit = await mountCreate()
    await fillBasics()
    await fireEvent.click(screen.getByRole('button', { name: 'Add template' }))

    expect(await screen.findByRole('alert')).toHaveTextContent('defaultControl: tick at least one function')
    expect(onSubmit).not.toHaveBeenCalled()
  })

  it('shows a refusal exactly as the caller reports it and keeps what was typed', async () => {
    const onSubmit = await mountCreate(() =>
      Promise.resolve({ ok: false, message: 'Error (409): a template with this mRID already exists on this fsa' }),
    )
    await fillBasics()
    await tick('fnFlag')
    await fireEvent.click(screen.getByRole('button', { name: 'Add template' }))

    expect(await screen.findByRole('alert')).toHaveTextContent(
      'Error (409): a template with this mRID already exists on this fsa',
    )
    expect(onSubmit).toHaveBeenCalledTimes(1)
    expect(screen.getByLabelText('Description')).toHaveValue('roof fleet')
  })

  it('disables the submit button while the request is out, so one click sends one request', async () => {
    let release: (r: Result) => void = () => {}
    const onSubmit = await mountCreate(() => new Promise<Result>((resolve) => (release = resolve)))
    await fill('Description', 'd')
    await fill('Primacy', '1')
    await tick('fnFlag')
    const button = screen.getByRole('button', { name: 'Add template' })
    await fireEvent.click(button)

    await waitFor(() => expect(button).toBeDisabled())
    await fireEvent.click(button)
    expect(onSubmit).toHaveBeenCalledTimes(1)
    release({ ok: true })
    await waitFor(() => expect(button).toBeEnabled())
  })

  it('says so when the catalog cannot be read, and sends nothing', async () => {
    serveCatalog({ ...CATALOG_ROUTES, 'GET /api/der/control-functions': { status: 500, body: { error: 'catalog down' } } })
    const onSubmit = vi.fn(() => Promise.resolve({ ok: true } as Result))
    render(DerProgramForm, { props: { mode: 'create', onSubmit } })

    expect(await screen.findByTestId('program-form-catalog-error')).toHaveTextContent('catalog down')
    await fillBasics()
    await fireEvent.click(screen.getByRole('button', { name: 'Add template' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('defaultControl: tick at least one function')
    expect(onSubmit).not.toHaveBeenCalled()
  })
})

describe('DerProgramForm edit', () => {
  const stored: ProgramView = {
    id: 'derp-1',
    mRID: 'ABCDEF0123456789ABCDEF0123456789',
    description: 'roof fleet',
    primacy: 65,
    version: 4,
    defaultControl: {
      mRID: 'DEF',
      type: 'combined',
      derControlBase: {},
      functions: { fnFlag: true, fnLevel: 7500, fnPair: { amount: 950, sign: true, scale: -3 }, fnCurve: 'cv11' },
    },
  }

  async function mountEdit(initial: ProgramView, onSubmit: (b: ProgramBody) => Promise<Result> = () => Promise.resolve({ ok: true })) {
    serveCatalog()
    const spy = vi.fn(onSubmit)
    render(DerProgramForm, { props: { mode: 'edit', initial, onSubmit: spy } })
    await screen.findByTestId('control-fn-fnFlag')
    return spy
  }

  it('reads a stored multi-function default back into the same ticks and values, and saves the same functions', async () => {
    const onSubmit = await mountEdit(stored)

    expect(screen.getByLabelText('Description')).toHaveValue('roof fleet')
    expect(screen.getByLabelText('Primacy')).toHaveValue('65')
    expect(screen.queryByLabelText('mRID (optional)')).toBeNull()
    expect(screen.getByTestId('program-form-mrid')).toHaveTextContent('ABCDEF0123456789ABCDEF0123456789')
    expect(screen.getByTestId('control-fn-fnFlag')).toBeChecked()
    expect(screen.getByTestId('control-fv-fnFlag')).toBeChecked()
    expect(screen.getByTestId('control-fv-fnLevel')).toHaveValue('7500')
    expect(screen.getByTestId('control-fv-fnPair-amount')).toHaveValue('950')
    expect(screen.getByTestId('control-fv-fnPair-sign')).toBeChecked()
    expect(screen.getByTestId('control-fv-fnPair-scale')).toHaveValue('-3')
    expect(screen.getByTestId('control-fv-fnCurve')).toHaveValue('cv11')

    await fill('Description', 'roof fleet 2')
    await fireEvent.click(screen.getByRole('button', { name: 'Save template' }))

    await waitFor(() => expect(onSubmit).toHaveBeenCalledTimes(1))
    expect(onSubmit).toHaveBeenCalledWith({
      description: 'roof fleet 2',
      primacy: 65,
      defaultControl: {
        functions: { fnFlag: true, fnLevel: 7500, fnPair: { amount: 950, sign: true, scale: -3 }, fnCurve: 'cv11' },
      },
    })
  })

  it('sends the edited set: an unticked function is dropped and a changed value is sent', async () => {
    const onSubmit = await mountEdit(stored)
    await tick('fnFlag')
    await tick('fnCurve')
    await fireEvent.input(screen.getByTestId('control-fv-fnLevel'), { target: { value: '100' } })
    await fireEvent.click(screen.getByRole('button', { name: 'Save template' }))

    await waitFor(() => expect(onSubmit).toHaveBeenCalledTimes(1))
    expect(onSubmit).toHaveBeenCalledWith({
      description: 'roof fleet',
      primacy: 65,
      defaultControl: { functions: { fnLevel: 100, fnPair: { amount: 950, sign: true, scale: -3 } } },
    })
  })

  it('keeps the stored default, by sending none, when every function is unticked', async () => {
    const onSubmit = await mountEdit(stored)
    for (const name of ['fnFlag', 'fnLevel', 'fnPair', 'fnCurve']) await tick(name)
    await fireEvent.click(screen.getByRole('button', { name: 'Save template' }))

    await waitFor(() => expect(onSubmit).toHaveBeenCalledTimes(1))
    expect(onSubmit).toHaveBeenCalledWith({ description: 'roof fleet', primacy: 65 })
  })

  describe('a stored function the form cannot edit', () => {
    const withExtra: ProgramView = {
      ...stored,
      defaultControl: {
        mRID: 'DEF',
        type: 'combined',
        derControlBase: {},
        functions: { fnFlag: true, fnGone: 7, fnOdd: { x: 1 } },
      },
    }
    // fnOdd is in the catalog with a kind the form has no field for.
    function serveWithOdd() {
      const rows = [...FIXTURE_ROWS, { name: 'fnOdd', description: 'odd', kind: 'matrix' }]
      serveCatalog({ ...CATALOG_ROUTES, 'GET /api/der/control-functions': { status: 200, body: { functions: rows } } })
    }

    it('lists each one in a visible note, a name the catalog lacks and a kind it cannot edit', async () => {
      serveWithOdd()
      render(DerProgramForm, { props: { mode: 'edit', initial: withExtra, onSubmit: () => Promise.resolve({ ok: true } as Result) } })
      const note = await screen.findByTestId('program-form-uneditable')
      expect(note).toHaveTextContent('fnGone')
      expect(note).toHaveTextContent('fnOdd')
      expect(note).not.toHaveTextContent('fnFlag')
    })

    it('keeps them in the saved body when the operator changes something else', async () => {
      serveWithOdd()
      const onSubmit = vi.fn(() => Promise.resolve({ ok: true } as Result))
      render(DerProgramForm, { props: { mode: 'edit', initial: withExtra, onSubmit } })
      await screen.findByTestId('program-form-uneditable')
      await tick('fnLevel')
      await fireEvent.input(screen.getByTestId('control-fv-fnLevel'), { target: { value: '9' } })
      await fireEvent.click(screen.getByRole('button', { name: 'Save template' }))

      await waitFor(() => expect(onSubmit).toHaveBeenCalledTimes(1))
      expect(onSubmit).toHaveBeenCalledWith({
        description: 'roof fleet',
        primacy: 65,
        defaultControl: { functions: { fnFlag: true, fnLevel: 9, fnGone: 7, fnOdd: { x: 1 } } },
      })
    })

    it('shows no note when every stored function is editable', async () => {
      await mountEdit(stored)
      expect(screen.queryByTestId('program-form-uneditable')).toBeNull()
    })
  })

  it('calls the cancel handler', async () => {
    serveCatalog()
    const onCancel = vi.fn()
    render(DerProgramForm, { props: { mode: 'edit', initial: stored, onSubmit: () => Promise.resolve({ ok: true } as Result), onCancel } })
    await fireEvent.click(screen.getByRole('button', { name: 'Cancel' }))
    expect(onCancel).toHaveBeenCalledTimes(1)
  })
})

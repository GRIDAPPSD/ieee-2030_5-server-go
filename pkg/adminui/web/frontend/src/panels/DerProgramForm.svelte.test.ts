// The form is shared by the FSAs tab and the device Programs drawer, so the
// tests pin what it hands its caller (the exact request body) and what it
// shows when the caller reports a refusal, not how it is laid out.
import { describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen, waitFor } from '@testing-library/svelte'
import DerProgramForm from './DerProgramForm.svelte'
import type { ProgramBody, ProgramView } from '../lib/derprogram'

type Result = { ok: true } | { ok: false; message: string }

function mountCreate(onSubmit: (b: ProgramBody) => Promise<Result> = () => Promise.resolve({ ok: true })) {
  const spy = vi.fn(onSubmit)
  render(DerProgramForm, { props: { mode: 'create', onSubmit: spy } })
  return spy
}

async function fill(label: string, value: string) {
  await fireEvent.input(screen.getByLabelText(label), { target: { value } })
}

async function pick(label: string, value: string) {
  await fireEvent.change(screen.getByLabelText(label), { target: { value } })
}

describe('DerProgramForm create', () => {
  it('submits the typed description, primacy, optional mRID and default control', async () => {
    const onSubmit = mountCreate()
    await fill('Description', 'roof fleet')
    await fill('Primacy', '65')
    await fill('mRID (optional)', '0123456789ABCDEF0123456789ABCDEF')
    await pick('Default control', 'maxLimW')
    await fill('Max limit', '5000')
    await fireEvent.click(screen.getByRole('button', { name: 'Add template' }))

    await waitFor(() => expect(onSubmit).toHaveBeenCalledTimes(1))
    expect(onSubmit).toHaveBeenCalledWith({
      description: 'roof fleet',
      primacy: 65,
      mRID: '0123456789ABCDEF0123456789ABCDEF',
      defaultControl: { type: 'maxLimW', maxLimW: 5000 },
    })
  })

  it('shows only the value fields of the chosen control type', async () => {
    mountCreate()
    expect(screen.queryByLabelText('Max limit')).toBeNull()
    await pick('Default control', 'fixedPFInjectW')
    expect(screen.getByLabelText('Displacement')).toBeInTheDocument()
    expect(screen.getByLabelText('Excitation')).toBeInTheDocument()
    expect(screen.queryByLabelText('Max limit')).toBeNull()
    await pick('Default control', 'targetW')
    expect(screen.getByLabelText('Target value')).toBeInTheDocument()
    expect(screen.getByLabelText('Multiplier')).toBeInTheDocument()
    expect(screen.queryByLabelText('Displacement')).toBeNull()
  })

  it('refuses invalid input with the server wording and does not call the caller', async () => {
    const onSubmit = mountCreate()
    await fill('Description', 'roof fleet')
    await fill('Primacy', '3')
    await pick('Default control', 'connect')
    await fireEvent.click(screen.getByRole('button', { name: 'Add template' }))

    expect(await screen.findByRole('alert')).toHaveTextContent('primacy: must be 0, 1, 2 or 65 to 191')
    expect(onSubmit).not.toHaveBeenCalled()
  })

  it('moves focus to the error when one appears, validation or refusal', async () => {
    mountCreate(() => Promise.resolve({ ok: false, message: 'Error (409): taken' }))
    await fireEvent.click(screen.getByRole('button', { name: 'Add template' }))
    await waitFor(() => expect(screen.getByRole('alert')).toHaveFocus())
    expect(screen.getByRole('alert')).toHaveTextContent('description')

    await fill('Description', 'd')
    await fill('Primacy', '1')
    await pick('Default control', 'connect')
    await fireEvent.click(screen.getByRole('button', { name: 'Add template' }))
    await waitFor(() => expect(screen.getByRole('alert')).toHaveTextContent('Error (409): taken'))
    expect(screen.getByRole('alert')).toHaveFocus()
  })

  it('requires a default control on create', async () => {
    const onSubmit = mountCreate()
    await fill('Description', 'roof fleet')
    await fill('Primacy', '65')
    await fireEvent.click(screen.getByRole('button', { name: 'Add template' }))

    expect(await screen.findByRole('alert')).toHaveTextContent('defaultControl.type: required')
    expect(onSubmit).not.toHaveBeenCalled()
  })

  it('shows a refusal exactly as the caller reports it and keeps what was typed', async () => {
    const onSubmit = mountCreate(() =>
      Promise.resolve({ ok: false, message: 'Error (409): a template with this mRID already exists on this fsa' }),
    )
    await fill('Description', 'roof fleet')
    await fill('Primacy', '65')
    await pick('Default control', 'connect')
    await fireEvent.click(screen.getByRole('button', { name: 'Add template' }))

    expect(await screen.findByRole('alert')).toHaveTextContent(
      'Error (409): a template with this mRID already exists on this fsa',
    )
    expect(onSubmit).toHaveBeenCalledTimes(1)
    expect(screen.getByLabelText('Description')).toHaveValue('roof fleet')
  })

  it('disables the submit button while the request is out, so one click sends one request', async () => {
    let release: (r: Result) => void = () => {}
    const onSubmit = mountCreate(() => new Promise<Result>((resolve) => (release = resolve)))
    await fill('Description', 'd')
    await fill('Primacy', '1')
    await pick('Default control', 'connect')
    const button = screen.getByRole('button', { name: 'Add template' })
    await fireEvent.click(button)

    await waitFor(() => expect(button).toBeDisabled())
    await fireEvent.click(button)
    expect(onSubmit).toHaveBeenCalledTimes(1)
    release({ ok: true })
    await waitFor(() => expect(button).toBeEnabled())
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
      type: 'fixedPFInjectW',
      derControlBase: { opModFixedPFInjectW: { displacement: 950, excitation: true, multiplier: -3 } },
    },
  }

  it('prefills from the stored program, shows its mRID read-only, and never sends an mRID', async () => {
    const onSubmit = vi.fn(() => Promise.resolve({ ok: true } as Result))
    render(DerProgramForm, { props: { mode: 'edit', initial: stored, onSubmit } })

    expect(screen.getByLabelText('Description')).toHaveValue('roof fleet')
    expect(screen.getByLabelText('Primacy')).toHaveValue('65')
    expect(screen.getByLabelText('Displacement')).toHaveValue('950')
    expect(screen.queryByLabelText('mRID (optional)')).toBeNull()
    expect(screen.getByTestId('program-form-mrid')).toHaveTextContent('ABCDEF0123456789ABCDEF0123456789')

    await fill('Description', 'roof fleet 2')
    await fireEvent.click(screen.getByRole('button', { name: 'Save template' }))

    await waitFor(() => expect(onSubmit).toHaveBeenCalledTimes(1))
    expect(onSubmit).toHaveBeenCalledWith({
      description: 'roof fleet 2',
      primacy: 65,
      defaultControl: { type: 'fixedPFInjectW', powerFactor: { displacement: 950, excitation: true } },
    })
  })

  it('calls the cancel handler', async () => {
    const onCancel = vi.fn()
    render(DerProgramForm, { props: { mode: 'edit', initial: stored, onSubmit: () => Promise.resolve({ ok: true } as Result), onCancel } })
    await fireEvent.click(screen.getByRole('button', { name: 'Cancel' }))
    expect(onCancel).toHaveBeenCalledTimes(1)
  })
})

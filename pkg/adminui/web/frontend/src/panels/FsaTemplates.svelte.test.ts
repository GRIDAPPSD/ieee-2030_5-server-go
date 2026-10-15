// Tests run against a fake fetch keyed by method and path, so each asserts
// the request that would reach the server (verb, escaped path, body) and the
// text the operator sees for the reply, including a refusal shown as the
// server sent it.
import { afterEach, describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen, waitFor, within } from '@testing-library/svelte'
import FsaTemplates from './FsaTemplates.svelte'
import type { ProgramTemplate } from '../lib/derprogram'

afterEach(() => {
  vi.restoreAllMocks()
})

function template(over: Partial<ProgramTemplate> = {}): ProgramTemplate {
  return {
    id: 'derp-1',
    mRID: 'ABCDEF0123456789ABCDEF0123456789',
    description: 'roof fleet',
    primacy: 65,
    version: 2,
    defaultControl: { mRID: 'DEF', type: 'maxLimW', derControlBase: { opModMaxLimW: 5000 } },
    copies: ['/edev/7/fsa/fsa-a/derp/derp-1', '/edev/8/fsa/fsa-a/derp/derp-1'],
    ...over,
  }
}

interface Reply {
  status: number
  body?: unknown
}

interface Call {
  method: string
  path: string
  body: unknown
}

// fake answers each "METHOD /path" from a table; a reply can be a function so
// one route can change between calls (a list that grows after a create).
function fake(routes: Record<string, Reply | (() => Reply)>) {
  const calls: Call[] = []
  vi.spyOn(globalThis, 'fetch').mockImplementation((input, init) => {
    const method = init?.method ?? 'GET'
    const path = String(input)
    calls.push({ method, path, body: init?.body ? JSON.parse(String(init.body)) : undefined })
    const hit = routes[`${method} ${path}`]
    const reply: Reply = typeof hit === 'function' ? hit() : (hit ?? { status: 404, body: { error: 'no route' } })
    return Promise.resolve({
      ok: reply.status >= 200 && reply.status < 300,
      status: reply.status,
      json: () => (reply.body === undefined ? Promise.reject(new Error('empty')) : Promise.resolve(reply.body)),
    } as Response)
  })
  return calls
}

const LIST = 'GET /api/fsas/fsa-a/der-programs'

function mount(onChanged: () => void = () => {}) {
  return render(FsaTemplates, { props: { fsaId: 'fsa-a', onChanged } })
}

async function fillForm(over: { description?: string; primacy?: string; type?: string } = {}) {
  await fireEvent.input(screen.getByLabelText('Description'), { target: { value: over.description ?? 'new one' } })
  await fireEvent.input(screen.getByLabelText('Primacy'), { target: { value: over.primacy ?? '1' } })
  await fireEvent.change(screen.getByLabelText('Default control'), { target: { value: over.type ?? 'connect' } })
}

describe('FsaTemplates list', () => {
  it('shows each template with its version, primacy, control and device count', async () => {
    fake({ [LIST]: { status: 200, body: { templates: [template()] } } })
    mount()

    const row = await screen.findByTestId('template-row-derp-1')
    expect(within(row).getByTestId('template-description')).toHaveTextContent('roof fleet')
    expect(within(row).getByTestId('template-primacy')).toHaveTextContent('65')
    expect(within(row).getByTestId('template-version')).toHaveTextContent('2')
    expect(within(row).getByTestId('template-control')).toHaveTextContent('maxLimW')
    expect(within(row).getByTestId('template-devices')).toHaveTextContent('2')
  })

  it('says there are no templates only after a successful empty read', async () => {
    fake({ [LIST]: { status: 200, body: { templates: [] } } })
    mount()
    expect(await screen.findByTestId('templates-empty-fsa-a')).toBeInTheDocument()
  })

  it('shows the error, not an empty list, when the read fails', async () => {
    fake({ [LIST]: { status: 500, body: { error: 'program write failed' } } })
    mount()

    expect(await screen.findByTestId('templates-error-fsa-a')).toHaveTextContent(
      'Could not load program templates (500): program write failed',
    )
    expect(screen.queryByTestId('templates-empty-fsa-a')).toBeNull()
  })

  it('shows the error for a reply with no templates array instead of an empty list', async () => {
    fake({ [LIST]: { status: 200, body: {} } })
    mount()
    expect(await screen.findByTestId('templates-error-fsa-a')).toHaveTextContent('no templates list')
    expect(screen.queryByTestId('templates-empty-fsa-a')).toBeNull()
  })

  it('offers no Add once the FSA holds 16 templates', async () => {
    const full = Array.from({ length: 16 }, (_, i) => template({ id: `derp-${i}` }))
    fake({ [LIST]: { status: 200, body: { templates: full } } })
    mount()

    await screen.findByTestId('template-row-derp-0')
    expect(screen.getByTestId('template-add-fsa-a')).toBeDisabled()
    expect(screen.getByTestId('templates-full-fsa-a')).toHaveTextContent('16')
  })
})

describe('FsaTemplates add', () => {
  it('posts the form body, reloads the list and tells the page', async () => {
    let created = false
    const calls = fake({
      [LIST]: () => ({ status: 200, body: { templates: created ? [template({ id: 'derp-new', description: 'new one' })] : [] } }),
      'POST /api/fsas/fsa-a/der-programs': () => {
        created = true
        return { status: 201, body: template({ id: 'derp-new', description: 'new one' }) }
      },
    })
    const onChanged = vi.fn()
    mount(onChanged)

    await fireEvent.click(await screen.findByTestId('template-add-fsa-a'))
    await fillForm()
    await fireEvent.click(screen.getByRole('button', { name: 'Add template' }))

    expect(await screen.findByTestId('template-row-derp-new')).toBeInTheDocument()
    const post = calls.find((c) => c.method === 'POST')
    expect(post?.body).toEqual({ description: 'new one', primacy: 1, defaultControl: { type: 'connect' } })
    expect(onChanged).toHaveBeenCalledTimes(1)
    expect(screen.queryByTestId('program-form')).toBeNull()
  })

  it('shows the 409 the server sent, verbatim with its status, and keeps the form open', async () => {
    fake({
      [LIST]: { status: 200, body: { templates: [] } },
      'POST /api/fsas/fsa-a/der-programs': {
        status: 409,
        body: { error: 'a template with this mRID already exists on this fsa' },
      },
    })
    const onChanged = vi.fn()
    mount(onChanged)

    await fireEvent.click(await screen.findByTestId('template-add-fsa-a'))
    await fillForm()
    await fireEvent.click(screen.getByRole('button', { name: 'Add template' }))

    expect(await screen.findByTestId('program-form-error')).toHaveTextContent(
      'Error (409): a template with this mRID already exists on this fsa',
    )
    expect(screen.getByTestId('program-form')).toBeInTheDocument()
    expect(onChanged).not.toHaveBeenCalled()
  })

  it('shows the error state for a write the plane does not mount (ReadOnly), not an empty view', async () => {
    fake({
      [LIST]: { status: 200, body: { templates: [] } },
      'POST /api/fsas/fsa-a/der-programs': { status: 405 },
    })
    mount()

    await fireEvent.click(await screen.findByTestId('template-add-fsa-a'))
    await fillForm()
    await fireEvent.click(screen.getByRole('button', { name: 'Add template' }))

    expect(await screen.findByTestId('program-form-error')).toHaveTextContent(
      'Error (405): request failed with status 405',
    )
  })
})

describe('FsaTemplates edit', () => {
  it('puts the edited body to the template route and reloads', async () => {
    const calls = fake({
      [LIST]: { status: 200, body: { templates: [template()] } },
      'PUT /api/fsas/fsa-a/der-programs/derp-1': { status: 200, body: template({ description: 'renamed', version: 3 }) },
    })
    const onChanged = vi.fn()
    mount(onChanged)

    await fireEvent.click(await screen.findByTestId('template-edit-derp-1'))
    expect(screen.getByLabelText('Max limit')).toHaveValue('5000')
    await fireEvent.input(screen.getByLabelText('Description'), { target: { value: 'renamed' } })
    await fireEvent.click(screen.getByRole('button', { name: 'Save template' }))

    await waitFor(() => expect(onChanged).toHaveBeenCalledTimes(1))
    const put = calls.find((c) => c.method === 'PUT')
    expect(put?.body).toEqual({
      description: 'renamed',
      primacy: 65,
      defaultControl: { type: 'maxLimW', maxLimW: 5000 },
    })
    expect(calls.filter((c) => c.method === 'GET')).toHaveLength(2)
  })

  it('shows a refusal such as version_exhausted as sent', async () => {
    fake({
      [LIST]: { status: 200, body: { templates: [template()] } },
      'PUT /api/fsas/fsa-a/der-programs/derp-1': {
        status: 409,
        body: { error: "the program's version is at 65535", code: 'version_exhausted' },
      },
    })
    mount()

    await fireEvent.click(await screen.findByTestId('template-edit-derp-1'))
    await fireEvent.click(screen.getByRole('button', { name: 'Save template' }))

    expect(await screen.findByTestId('program-form-error')).toHaveTextContent(
      "Error (409): the program's version is at 65535",
    )
  })
})

describe('FsaTemplates delete', () => {
  it('asks for confirmation, then deletes and reloads', async () => {
    let gone = false
    const calls = fake({
      [LIST]: () => ({ status: 200, body: { templates: gone ? [] : [template()] } }),
      'DELETE /api/fsas/fsa-a/der-programs/derp-1': () => {
        gone = true
        return { status: 204 }
      },
    })
    const onChanged = vi.fn()
    mount(onChanged)

    await fireEvent.click(await screen.findByTestId('template-delete-derp-1'))
    expect(calls.some((c) => c.method === 'DELETE')).toBe(false)
    await fireEvent.click(screen.getByTestId('template-delete-confirm-derp-1'))

    await waitFor(() => expect(screen.queryByTestId('template-row-derp-1')).toBeNull())
    expect(calls.filter((c) => c.method === 'DELETE')).toHaveLength(1)
    expect(onChanged).toHaveBeenCalledTimes(1)
  })

  it('lists the live controls that block a delete and keeps the template', async () => {
    fake({
      [LIST]: { status: 200, body: { templates: [template()] } },
      'DELETE /api/fsas/fsa-a/der-programs/derp-1': {
        status: 409,
        body: {
          error: 'cancel these controls, or wait for them to end, before deleting the program',
          code: 'live_controls',
          controls: ['CTRL-1', 'CTRL-2'],
        },
      },
    })
    const onChanged = vi.fn()
    mount(onChanged)

    await fireEvent.click(await screen.findByTestId('template-delete-derp-1'))
    await fireEvent.click(screen.getByTestId('template-delete-confirm-derp-1'))

    const conflict = await screen.findByTestId('template-conflict-derp-1')
    expect(conflict).toHaveTextContent('cancel these controls, or wait for them to end, before deleting the program')
    const items = within(conflict).getAllByRole('listitem')
    expect(items.map((li) => li.textContent)).toEqual(['CTRL-1', 'CTRL-2'])
    expect(screen.getByTestId('template-row-derp-1')).toBeInTheDocument()
    expect(onChanged).not.toHaveBeenCalled()
  })

  it('shows any other delete failure as sent', async () => {
    fake({
      [LIST]: { status: 200, body: { templates: [template()] } },
      'DELETE /api/fsas/fsa-a/der-programs/derp-1': { status: 404 },
    })
    mount()

    await fireEvent.click(await screen.findByTestId('template-delete-derp-1'))
    await fireEvent.click(screen.getByTestId('template-delete-confirm-derp-1'))

    expect(await screen.findByTestId('template-delete-error-derp-1')).toHaveTextContent(
      'Delete failed (404): request failed with status 404',
    )
  })
})

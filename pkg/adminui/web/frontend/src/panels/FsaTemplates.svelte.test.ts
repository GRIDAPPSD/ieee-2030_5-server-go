// Tests run against a fake fetch keyed by method and path, so each asserts
// the request that would reach the server (verb, escaped path, body) and the
// text the operator sees for the reply, including a refusal shown as the
// server sent it.
import { afterEach, describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen, waitFor, within } from '@testing-library/svelte'
import FsaTemplates from './FsaTemplates.svelte'
import { CATALOG_ROUTES } from '../lib/controlcatalog.fixture'
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
    defaultControl: {
      mRID: 'DEF',
      type: 'maxLimW',
      derControlBase: {},
      functions: { fnLevel: 5000, fnPair: { amount: 950, sign: true } },
    },
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
function fake(routes: Record<string, Reply | (() => Reply | Promise<Reply>)>) {
  const calls: Call[] = []
  vi.spyOn(globalThis, 'fetch').mockImplementation((input, init) => {
    const method = init?.method ?? 'GET'
    const path = String(input)
    calls.push({ method, path, body: init?.body ? JSON.parse(String(init.body)) : undefined })
    const hit = { ...CATALOG_ROUTES, ...routes }[`${method} ${path}`]
    const reply = typeof hit === 'function' ? hit() : (hit ?? { status: 404, body: { error: 'no route' } })
    return Promise.resolve(reply).then(
      (r) =>
        ({
          ok: r.status >= 200 && r.status < 300,
          status: r.status,
          json: () => (r.body === undefined ? Promise.reject(new Error('empty')) : Promise.resolve(r.body)),
        }) as Response,
    )
  })
  return calls
}

const LIST = 'GET /api/fsas/fsa-a/der-programs'

function mount(onChanged: () => void = () => {}) {
  return render(FsaTemplates, { props: { fsaId: 'fsa-a', onChanged } })
}

async function fillForm(over: { description?: string; primacy?: string } = {}) {
  await fireEvent.input(screen.getByLabelText('Description'), { target: { value: over.description ?? 'new one' } })
  await fireEvent.input(screen.getByLabelText('Primacy'), { target: { value: over.primacy ?? '1' } })
  // The default control is the catalog's functions, which arrive after the form mounts.
  await fireEvent.click(await screen.findByTestId('control-fn-fnFlag'))
  await fireEvent.click(screen.getByTestId('control-fv-fnFlag'))
  await fireEvent.click(screen.getByTestId('control-fn-fnLevel'))
  await fireEvent.input(screen.getByTestId('control-fv-fnLevel'), { target: { value: '4000' } })
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
    expect(post?.body).toEqual({
      description: 'new one',
      primacy: 1,
      defaultControl: { functions: { fnFlag: true, fnLevel: 4000 } },
    })
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
    expect(await screen.findByTestId('control-fv-fnLevel')).toHaveValue('5000')
    expect(screen.getByTestId('control-fv-fnPair-amount')).toHaveValue('950')
    await fireEvent.input(screen.getByLabelText('Description'), { target: { value: 'renamed' } })
    await fireEvent.click(screen.getByRole('button', { name: 'Save template' }))

    await waitFor(() => expect(onChanged).toHaveBeenCalledTimes(1))
    const put = calls.find((c) => c.method === 'PUT')
    expect(put?.body).toEqual({
      description: 'renamed',
      primacy: 65,
      defaultControl: { functions: { fnLevel: 5000, fnPair: { amount: 950, sign: true } } },
    })
    expect(calls.filter((c) => `${c.method} ${c.path}` === LIST)).toHaveLength(2)
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

// A reply the test releases by hand, so two requests can finish out of order.
function deferred() {
  let resolve: (r: Reply) => void = () => {}
  const promise = new Promise<Reply>((r) => (resolve = r))
  return { promise, resolve }
}

describe('FsaTemplates ordering and teardown', () => {
  it('keeps the newer FSA list when an older read finishes last', async () => {
    const a = deferred()
    const b = deferred()
    fake({
      'GET /api/fsas/fsa-a/der-programs': () => a.promise,
      'GET /api/fsas/fsa-b/der-programs': () => b.promise,
    })
    const { rerender } = render(FsaTemplates, { props: { fsaId: 'fsa-a', onChanged: () => {} } })
    await rerender({ fsaId: 'fsa-b', onChanged: () => {} })

    b.resolve({ status: 200, body: { templates: [template({ id: 'derp-b', description: 'from b' })] } })
    expect(await screen.findByTestId('template-row-derp-b')).toBeInTheDocument()
    a.resolve({ status: 200, body: { templates: [template({ id: 'derp-a', description: 'from a' })] } })
    await new Promise((r) => setTimeout(r, 20))

    expect(screen.queryByTestId('template-row-derp-a')).toBeNull()
    expect(screen.getByTestId('template-row-derp-b')).toBeInTheDocument()
  })

  it('does not let a failed older read replace the newer list with an error', async () => {
    const a = deferred()
    const b = deferred()
    fake({
      'GET /api/fsas/fsa-a/der-programs': () => a.promise,
      'GET /api/fsas/fsa-b/der-programs': () => b.promise,
    })
    const { rerender } = render(FsaTemplates, { props: { fsaId: 'fsa-a', onChanged: () => {} } })
    await rerender({ fsaId: 'fsa-b', onChanged: () => {} })

    b.resolve({ status: 200, body: { templates: [template({ id: 'derp-b' })] } })
    await screen.findByTestId('template-row-derp-b')
    a.resolve({ status: 500, body: { error: 'late failure' } })
    await new Promise((r) => setTimeout(r, 20))

    expect(screen.queryByTestId('templates-error-fsa-b')).toBeNull()
    expect(screen.getByTestId('template-row-derp-b')).toBeInTheDocument()
  })
})

describe('FsaTemplates in-flight actions', () => {
  const two = [template(), template({ id: 'derp-2', description: 'night', mRID: '00112233445566778899AABBCCDDEEFF' })]

  it('sends one DELETE for two confirm clicks and disables the action while it is out', async () => {
    const del = deferred()
    const calls = fake({
      [LIST]: { status: 200, body: { templates: two } },
      'DELETE /api/fsas/fsa-a/der-programs/derp-1': () => del.promise,
    })
    mount()

    await fireEvent.click(await screen.findByRole('button', { name: 'Delete roof fleet' }))
    const confirm = screen.getByRole('button', { name: 'Confirm delete roof fleet' })
    await fireEvent.click(confirm)
    await waitFor(() => expect(confirm).toBeDisabled())
    await fireEvent.click(confirm)

    expect(calls.filter((c) => c.method === 'DELETE')).toHaveLength(1)
    del.resolve({ status: 204 })
    await waitFor(() => expect(calls.filter((c) => c.method === 'GET')).toHaveLength(2))
  })

  it('leaves another row form open when an earlier save finishes', async () => {
    const put = deferred()
    fake({
      [LIST]: { status: 200, body: { templates: two } },
      'PUT /api/fsas/fsa-a/der-programs/derp-1': () => put.promise,
    })
    mount()

    await fireEvent.click(await screen.findByRole('button', { name: 'Edit roof fleet' }))
    await fireEvent.click(screen.getByRole('button', { name: 'Save template' }))
    await fireEvent.click(screen.getByRole('button', { name: 'Edit night' }))
    expect(screen.getByLabelText('Description')).toHaveValue('night')

    put.resolve({ status: 200, body: template({ version: 3 }) })
    await new Promise((r) => setTimeout(r, 30))

    expect(screen.getByTestId('program-form')).toBeInTheDocument()
    expect(screen.getByLabelText('Description')).toHaveValue('night')
  })

  it('does not reload or tell the page after a delete the server refused', async () => {
    const onChanged = vi.fn()
    const calls = fake({
      [LIST]: { status: 200, body: { templates: two } },
      'DELETE /api/fsas/fsa-a/der-programs/derp-1': {
        status: 409,
        body: { error: 'blocked', code: 'live_controls', controls: ['C1'] },
      },
    })
    mount(onChanged)

    await fireEvent.click(await screen.findByRole('button', { name: 'Delete roof fleet' }))
    await fireEvent.click(screen.getByRole('button', { name: 'Confirm delete roof fleet' }))
    await screen.findByTestId('template-conflict-derp-1')
    await new Promise((r) => setTimeout(r, 30))

    expect(calls.filter((c) => c.method === 'GET')).toHaveLength(1)
    expect(onChanged).not.toHaveBeenCalled()
  })
})

describe('FsaTemplates accessibility', () => {
  const two = [template(), template({ id: 'derp-2', description: 'night' })]

  it('names each row action after its template', async () => {
    fake({ [LIST]: { status: 200, body: { templates: two } } })
    mount()
    await screen.findByTestId('template-row-derp-2')

    for (const d of ['roof fleet', 'night']) {
      expect(screen.getByRole('button', { name: `Edit ${d}` })).toBeInTheDocument()
      expect(screen.getByRole('button', { name: `Delete ${d}` })).toBeInTheDocument()
    }
    await fireEvent.click(screen.getByRole('button', { name: 'Delete night' }))
    expect(screen.getByRole('button', { name: 'Confirm delete night' })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Keep night' })).toBeInTheDocument()
  })

  it('moves focus to Confirm when Delete swaps to it, and back to Delete on Keep', async () => {
    fake({ [LIST]: { status: 200, body: { templates: two } } })
    mount()

    await fireEvent.click(await screen.findByRole('button', { name: 'Delete night' }))
    await waitFor(() => expect(screen.getByRole('button', { name: 'Confirm delete night' })).toHaveFocus())

    await fireEvent.click(screen.getByRole('button', { name: 'Keep night' }))
    await waitFor(() => expect(screen.getByRole('button', { name: 'Delete night' })).toHaveFocus())
  })

  it('moves focus to a delete error when one appears', async () => {
    fake({
      [LIST]: { status: 200, body: { templates: two } },
      'DELETE /api/fsas/fsa-a/der-programs/derp-2': { status: 404 },
    })
    mount()

    await fireEvent.click(await screen.findByRole('button', { name: 'Delete night' }))
    await fireEvent.click(screen.getByRole('button', { name: 'Confirm delete night' }))

    const err = await screen.findByTestId('template-delete-error-derp-2')
    await waitFor(() => expect(err).toHaveFocus())
  })

  it('moves focus to a load error when one appears', async () => {
    fake({ [LIST]: { status: 500, body: { error: 'boom' } } })
    mount()
    const err = await screen.findByTestId('templates-error-fsa-a')
    await waitFor(() => expect(err).toHaveFocus())
  })
})

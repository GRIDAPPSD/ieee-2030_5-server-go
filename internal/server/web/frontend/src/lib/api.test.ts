// The 204 case is the one worth pinning: DELETE /api/fsas/{id} answers
// with no body at all, and feeding an empty body to json() rejects, which
// would turn a successful delete into a reported failure.
import { afterEach, describe, expect, it, vi } from 'vitest'
import { deleteJSON, fetchJSON, postBody, postJSON } from './api'

afterEach(() => {
  vi.restoreAllMocks()
})

function stubFetch(res: Partial<Response> & { json?: () => Promise<unknown> }) {
  return vi.spyOn(globalThis, 'fetch').mockResolvedValue(res as Response)
}

describe('api', () => {
  it('decodes a JSON success body', async () => {
    stubFetch({ ok: true, status: 200, json: () => Promise.resolve({ sfdi: '1' }) })

    const res = await fetchJSON<{ sfdi: string }>('/api/thing')
    expect(res).toEqual({ ok: true, data: { sfdi: '1' } })
  })

  it('treats a 204 as success with no body rather than a decode failure', async () => {
    stubFetch({ ok: true, status: 204, json: () => Promise.reject(new Error('no body')) })

    const res = await deleteJSON<void>('/api/fsas/x')
    expect(res.ok).toBe(true)
  })

  it('surfaces the server error message and status from an error body', async () => {
    stubFetch({ ok: false, status: 409, json: () => Promise.resolve({ error: 'already exists' }) })

    const res = await postJSON('/api/fsas', {})
    expect(res).toEqual({ ok: false, error: 'already exists', status: 409 })
  })

  it('falls back to a status message when an error body is not JSON', async () => {
    stubFetch({ ok: false, status: 500, json: () => Promise.reject(new Error('not json')) })

    const res = await postJSON('/api/fsas', {})
    expect(res).toEqual({ ok: false, error: 'request failed with status 500', status: 500 })
  })

  it('reports a transport failure as status 0 rather than throwing', async () => {
    vi.spyOn(globalThis, 'fetch').mockRejectedValue(new Error('connection refused'))

    const res = await fetchJSON('/api/thing')
    expect(res).toEqual({ ok: false, error: 'connection refused', status: 0 })
  })

  it('sends same-origin credentials and the given content type on a raw body post', async () => {
    const spy = stubFetch({ ok: true, status: 200, json: () => Promise.resolve({}) })

    await postBody('/api/certs/info', 'application/x-pem-file', 'PEM')

    expect(spy).toHaveBeenCalledWith('/api/certs/info', {
      credentials: 'same-origin',
      method: 'POST',
      headers: { 'Content-Type': 'application/x-pem-file' },
      body: 'PEM',
    })
  })
})

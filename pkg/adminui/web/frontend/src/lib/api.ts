// api.ts is the SPA's single client for the admin listener's JSON
// endpoints (internal/server/admin_router.go). Every panel goes through
// these helpers rather than calling fetch directly, so the error shape,
// the same-origin credential mode, and the "204 has no body" case stay
// consistent as panels are added.

export interface ApiError {
  error: string
}

// serverTime is the response's Date header in epoch milliseconds, present
// only when the server sent a parseable one; it lets a caller compare server
// timestamps against the server's clock rather than the browser's.
// body is the decoded error body when the server sent JSON, so a caller can
// read fields beyond "error" (the DER control 500 that names a kept control).
export type ApiResult<T> =
  | { ok: true; data: T; serverTime?: number }
  | { ok: false; error: string; status: number; body?: unknown }

// decode turns a Response into an ApiResult. A 204 (the FSA delete path)
// carries no body at all, so it resolves to undefined rather than being
// fed to json(), which would reject on the empty body.
async function decode<T>(res: Response): Promise<ApiResult<T>> {
  if (res.status === 204) {
    return { ok: true, data: undefined as T }
  }

  let body: unknown
  try {
    body = await res.json()
  } catch {
    // Non-JSON or empty body: fall back to a status-derived message
    // below rather than throwing out of the helper.
    body = null
  }

  if (!res.ok) {
    const message =
      body && typeof body === 'object' && typeof (body as ApiError).error === 'string'
        ? (body as ApiError).error
        : `request failed with status ${res.status}`
    return { ok: false, error: message, status: res.status, body: body ?? undefined }
  }

  const serverTime = Date.parse(res.headers?.get('Date') ?? '')
  return Number.isNaN(serverTime) ? { ok: true, data: body as T } : { ok: true, data: body as T, serverTime }
}

// RequestOptions bounds a request that could otherwise never settle: fetch
// has no timeout of its own, so a hung connection leaves the caller waiting
// until a page reload. A caller that passes neither gets the old behavior.
export interface RequestOptions {
  signal?: AbortSignal
  timeoutMs?: number
}

async function send<T>(path: string, init: RequestInit, opts?: RequestOptions): Promise<ApiResult<T>> {
  if (opts === undefined || (opts.signal === undefined && opts.timeoutMs === undefined)) {
    return sendUnbounded<T>(path, init)
  }
  const ctrl = new AbortController()
  let timedOut = false
  const timer =
    opts.timeoutMs === undefined
      ? undefined
      : setTimeout(() => {
          timedOut = true
          ctrl.abort()
        }, opts.timeoutMs)
  const onCallerAbort = () => ctrl.abort()
  if (opts.signal?.aborted) ctrl.abort()
  opts.signal?.addEventListener('abort', onCallerAbort)
  try {
    const result = await sendUnbounded<T>(path, { ...init, signal: ctrl.signal })
    if (!result.ok && ctrl.signal.aborted) {
      return { ok: false, error: timedOut ? 'request timed out' : 'request cancelled', status: 0 }
    }
    return result
  } finally {
    if (timer !== undefined) clearTimeout(timer)
    opts.signal?.removeEventListener('abort', onCallerAbort)
  }
}

async function sendUnbounded<T>(path: string, init: RequestInit): Promise<ApiResult<T>> {
  let res: Response
  try {
    res = await fetch(path, { credentials: 'same-origin', ...init })
  } catch (err) {
    return { ok: false, error: err instanceof Error ? err.message : 'network error', status: 0 }
  }
  return decode<T>(res)
}

export function fetchJSON<T>(path: string, opts?: RequestOptions): Promise<ApiResult<T>> {
  return send<T>(path, { method: 'GET', headers: { Accept: 'application/json' } }, opts)
}

export function postJSON<T>(path: string, body: unknown): Promise<ApiResult<T>> {
  return send<T>(path, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  })
}

// postBody posts a raw string with an explicit content type. Used by the
// cert parser, which takes a PEM body as application/x-pem-file rather
// than JSON (internal/handler/admin_register.go's HandleCertInfo).
export function postBody<T>(path: string, contentType: string, body: string): Promise<ApiResult<T>> {
  return send<T>(path, {
    method: 'POST',
    headers: { 'Content-Type': contentType },
    body,
  })
}

export function deleteJSON<T>(path: string): Promise<ApiResult<T>> {
  return send<T>(path, { method: 'DELETE' })
}

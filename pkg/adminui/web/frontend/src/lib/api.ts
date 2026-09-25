// api.ts is the SPA's single client for the admin listener's JSON
// endpoints (internal/server/admin_router.go). Every panel goes through
// these helpers rather than calling fetch directly, so the error shape,
// the same-origin credential mode, and the "204 has no body" case stay
// consistent as panels are added.

export interface ApiError {
  error: string
}

export type ApiResult<T> = { ok: true; data: T } | { ok: false; error: string; status: number }

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
    return { ok: false, error: message, status: res.status }
  }

  return { ok: true, data: body as T }
}

async function send<T>(path: string, init: RequestInit): Promise<ApiResult<T>> {
  let res: Response
  try {
    res = await fetch(path, { credentials: 'same-origin', ...init })
  } catch (err) {
    return { ok: false, error: err instanceof Error ? err.message : 'network error', status: 0 }
  }
  return decode<T>(res)
}

export function fetchJSON<T>(path: string): Promise<ApiResult<T>> {
  return send<T>(path, { method: 'GET', headers: { Accept: 'application/json' } })
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

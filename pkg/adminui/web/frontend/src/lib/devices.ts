// Device names for the admin UI (GET and PUT /api/device-profiles, #1032).
// The server keeps one name and kind per LFDI; this module loads them once
// and every view labels a device through deviceLabel, so no payload that
// already carries an SFDI or LFDI needs a name field of its own.

import { SvelteMap } from 'svelte/reactivity'
import { fetchJSON, putJSON, type ApiResult, type RequestOptions } from './api'

export interface DeviceProfile {
  lfdi: string
  name: string
  kind: string
}

const PATH = '/api/device-profiles'

// The server answers with uppercase LFDIs, so every lookup and send uses
// uppercase; this is the one place that rule lives.
export const canon = (lfdi: string) => lfdi.toUpperCase()

// A SvelteMap, so a template or effect that calls deviceLabel re-runs when a
// profile is loaded or renamed.
const profiles = new SvelteMap<string, DeviceProfile>()

function isProfile(v: unknown): v is DeviceProfile {
  if (typeof v !== 'object' || v === null) return false
  const p = v as Record<string, unknown>
  return typeof p.lfdi === 'string' && typeof p.name === 'string' && typeof p.kind === 'string'
}

// deviceLabel is what every view shows for a device: its name, else its
// SFDI, else a prefix of its LFDI. The name is plain text for the caller to
// interpolate, never markup.
export function deviceLabel(lfdi: string | undefined, sfdi?: string): string {
  const name = lfdi ? profiles.get(canon(lfdi))?.name : undefined
  if (name) return name
  if (sfdi) return sfdi
  if (lfdi) return lfdi.substring(0, 16) + '...'
  return ''
}

// Loads overlap (every tab entry starts one), so only the newest started
// read is applied; an older answer arriving late is dropped.
let loadSeq = 0

// loadDeviceProfiles replaces the held profiles with the server's list. A
// failed or malformed read keeps what was held, so labels do not collapse to
// SFDIs on one bad poll, and returns the failure for the caller to show.
export async function loadDeviceProfiles(opts?: RequestOptions): Promise<ApiResult<DeviceProfile[]>> {
  const seq = ++loadSeq
  const res = await fetchJSON<{ profiles?: unknown }>(PATH, opts)
  if (!res.ok) return res
  const list = res.data?.profiles
  if (!Array.isArray(list) || !list.every(isProfile)) {
    return { ok: false, error: 'server returned an unexpected device profile list', status: 200 }
  }
  if (seq !== loadSeq) return { ok: true, data: list }
  profiles.clear()
  for (const p of list) profiles.set(canon(p.lfdi), p)
  return { ok: true, data: list }
}

// renameDevice sets one device's name. The PUT replaces the whole profile, so
// the kind is read from the server immediately before it and sent back
// unchanged: a rename must not clear a kind, and with no readable kind the
// rename is refused rather than sent blind.
export async function renameDevice(lfdi: string, name: string): Promise<ApiResult<DeviceProfile>> {
  const fresh = await loadDeviceProfiles()
  if (!fresh.ok) return fresh
  const kind = fresh.data.find((p) => canon(p.lfdi) === canon(lfdi))?.kind ?? ''
  const res = await putJSON<DeviceProfile>(`${PATH}/${encodeURIComponent(canon(lfdi))}`, { name, kind })
  if (res.ok && isProfile(res.data)) profiles.set(canon(lfdi), res.data)
  return res
}

// deviceName is the held name for one device, or '' when it has none.
export function deviceName(lfdi: string): string {
  return profiles.get(canon(lfdi))?.name ?? ''
}

// resetDeviceProfiles drops every held profile; tests use it between cases.
export function resetDeviceProfiles(): void {
  loadSeq++
  profiles.clear()
}

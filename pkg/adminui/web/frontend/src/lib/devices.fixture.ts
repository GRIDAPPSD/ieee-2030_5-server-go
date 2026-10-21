// Test helper: loads device profiles through the real loader, so a view test
// sees the same state the shell produces. Call it before installing the
// test's own fetchJSON mock; its single canned reply is consumed by the load.
import { vi } from 'vitest'
import * as api from './api'
import { loadDeviceProfiles, type DeviceProfile } from './devices'

export async function seedDeviceProfiles(list: DeviceProfile[]): Promise<void> {
  vi.spyOn(api, 'fetchJSON').mockResolvedValueOnce({ ok: true, data: { profiles: list } } as never)
  const res = await loadDeviceProfiles()
  if (!res.ok) throw new Error('seeding device profiles failed: ' + res.error)
}

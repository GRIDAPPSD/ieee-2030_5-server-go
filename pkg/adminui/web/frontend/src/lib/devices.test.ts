import { afterEach, describe, expect, it, vi } from 'vitest'
import * as api from './api'
import { deviceLabel, deviceName, loadDeviceProfiles, renameDevice, resetDeviceProfiles } from './devices'
import { seedDeviceProfiles } from './devices.fixture'

const LFDI = '0123456789ABCDEF0123456789ABCDEF01234567'
const OTHER = 'FEDCBA9876543210FEDCBA9876543210FEDCBA98'

afterEach(() => {
  vi.restoreAllMocks()
  resetDeviceProfiles()
})

describe('deviceLabel', () => {
  it('shows the name alone for a named device, whatever the SFDI', async () => {
    await seedDeviceProfiles([{ lfdi: LFDI, name: 'Inverter 3', kind: 'device' }])
    expect(deviceLabel(LFDI, '167261211635')).toBe('Inverter 3')
  })

  it('shows the SFDI for an unnamed device, including one with an empty-name profile', async () => {
    expect(deviceLabel(LFDI, '167261211635')).toBe('167261211635')
    await seedDeviceProfiles([{ lfdi: LFDI, name: '', kind: 'device' }])
    expect(deviceLabel(LFDI, '167261211635')).toBe('167261211635')
  })

  it('falls back to a 16 character LFDI prefix with neither name nor SFDI', () => {
    expect(deviceLabel(LFDI)).toBe('0123456789ABCDEF...')
    expect(deviceLabel(LFDI, '')).toBe('0123456789ABCDEF...')
  })

  it('is empty when nothing identifies the device', () => {
    expect(deviceLabel(undefined)).toBe('')
    expect(deviceLabel('', '')).toBe('')
  })

  it('matches the LFDI whatever its case', async () => {
    await seedDeviceProfiles([{ lfdi: LFDI, name: 'Inverter 3', kind: '' }])
    expect(deviceLabel(LFDI.toLowerCase(), '1')).toBe('Inverter 3')
  })
})

describe('loadDeviceProfiles', () => {
  it('replaces the held profiles with the server list', async () => {
    await seedDeviceProfiles([{ lfdi: LFDI, name: 'Old', kind: '' }])
    vi.spyOn(api, 'fetchJSON').mockResolvedValueOnce({
      ok: true,
      data: { profiles: [{ lfdi: OTHER, name: 'New', kind: 'aggregator' }] },
    } as never)

    const res = await loadDeviceProfiles()

    expect(res.ok).toBe(true)
    expect(deviceName(LFDI)).toBe('')
    expect(deviceName(OTHER)).toBe('New')
  })

  it('keeps the held names and reports a failed read', async () => {
    await seedDeviceProfiles([{ lfdi: LFDI, name: 'Kept', kind: '' }])
    vi.spyOn(api, 'fetchJSON').mockResolvedValueOnce({ ok: false, error: 'down', status: 500 })

    const res = await loadDeviceProfiles()

    expect(res).toEqual({ ok: false, error: 'down', status: 500 })
    expect(deviceName(LFDI)).toBe('Kept')
  })

  it.each([
    ['no profiles key', {}],
    ['a non-array', { profiles: 'x' }],
    ['a profile without a name', { profiles: [{ lfdi: LFDI, kind: '' }] }],
  ])('rejects %s and keeps the held names', async (_label, data) => {
    await seedDeviceProfiles([{ lfdi: LFDI, name: 'Kept', kind: '' }])
    vi.spyOn(api, 'fetchJSON').mockResolvedValueOnce({ ok: true, data } as never)

    const res = await loadDeviceProfiles()

    expect(res.ok).toBe(false)
    expect(deviceName(LFDI)).toBe('Kept')
  })

  it('applies only the newest started read when answers arrive out of order', async () => {
    let releaseOld: (v: never) => void = () => {}
    const spy = vi.spyOn(api, 'fetchJSON')
    spy.mockReturnValueOnce(new Promise((resolve) => (releaseOld = resolve as never)))
    spy.mockResolvedValueOnce({ ok: true, data: { profiles: [{ lfdi: LFDI, name: 'Newer', kind: '' }] } } as never)

    const older = loadDeviceProfiles()
    await loadDeviceProfiles()
    releaseOld({ ok: true, data: { profiles: [{ lfdi: LFDI, name: 'Older', kind: '' }] } } as never)
    await older

    expect(deviceName(LFDI)).toBe('Newer')
  })
})

describe('renameDevice', () => {
  it('puts the new name with the held kind, so a rename does not clear the kind', async () => {
    await seedDeviceProfiles([{ lfdi: LFDI, name: 'Old', kind: 'aggregator' }])
    const put = vi.spyOn(api, 'putJSON').mockResolvedValue({
      ok: true,
      data: { lfdi: LFDI, name: 'Roof', kind: 'aggregator' },
    })

    const res = await renameDevice(LFDI.toLowerCase(), 'Roof')

    expect(res.ok).toBe(true)
    expect(put).toHaveBeenCalledWith(`/api/device-profiles/${LFDI}`, { name: 'Roof', kind: 'aggregator' })
    expect(deviceLabel(LFDI, '1')).toBe('Roof')
  })

  it('leaves the held name alone when the server refuses', async () => {
    await seedDeviceProfiles([{ lfdi: LFDI, name: 'Old', kind: '' }])
    vi.spyOn(api, 'putJSON').mockResolvedValue({ ok: false, error: 'name already in use by another device', status: 409 })

    const res = await renameDevice(LFDI, 'Taken')

    expect(res.ok).toBe(false)
    expect(deviceName(LFDI)).toBe('Old')
  })

  it('sends an empty kind for a device with no profile yet', async () => {
    const put = vi.spyOn(api, 'putJSON').mockResolvedValue({ ok: true, data: { lfdi: LFDI, name: 'First', kind: '' } })

    await renameDevice(LFDI, 'First')

    expect(put).toHaveBeenCalledWith(`/api/device-profiles/${LFDI}`, { name: 'First', kind: '' })
  })
})

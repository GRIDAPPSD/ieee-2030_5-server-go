// The sign help restates admin_fleet.go's exportPositive for 2018; the Go side
// is pinned by TestExportPositive_HelpMapping. This pins the text it shows.
import { describe, expect, it } from 'vitest'
import { SIGN_HELP_SECTIONS } from './fleet'

describe('SIGN_HELP_SECTIONS', () => {
  const s2018 = SIGN_HELP_SECTIONS.find((s) => s.edition === '2018 clients')

  it('has a 2018 section and no 2023 section yet', () => {
    expect(s2018).toBeDefined()
    expect(SIGN_HELP_SECTIONS.some((s) => s.edition.includes('2023'))).toBe(false)
  })
  it('names the 2018 mapping', () => {
    const m = (f: string) => s2018!.rows.find((r) => r.flowDirection.startsWith(f))?.meaning
    expect(m('1 ')).toMatch(/^import/)
    expect(m('19')).toMatch(/^export/)
    expect(m('4 ')).toMatch(/^direction unknown/)
  })
  it('states the missing-direction and zero cases', () => {
    expect(s2018!.notes.some((n) => n.startsWith('A reading with no flowDirection is shown as direction unknown'))).toBe(true)
    expect(s2018!.notes.some((n) => n.includes('flowDirection 0') && n.includes('0 W'))).toBe(true)
  })
})

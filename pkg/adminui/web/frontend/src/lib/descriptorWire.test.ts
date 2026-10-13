// The TypeScript types are hand-written, so nothing but this test notices
// the Go wire shape changing. It reads the pinned fixture and requires the
// keys of every object in it to equal the keys the matching TypeScript
// interface declares. `satisfies Record<keyof T, true>` makes a key added
// to or removed from an interface a compile error here, so a table cannot
// drift from the type it stands for.
/// <reference types="node" />
import { readFileSync } from 'node:fs'
import { describe, expect, it } from 'vitest'
import { badgeClass, safeHref } from './descriptor'
import type {
  Descriptor,
  DescriptorCell,
  DescriptorChartBody,
  DescriptorChartSeries,
  DescriptorDefinitionEntry,
  DescriptorDefinitionGroup,
  DescriptorDefinitionListBody,
  DescriptorSection,
  DescriptorTableBody,
} from './descriptor'

// vitest runs with the frontend directory as cwd, four levels below the
// repository root.
const fixture = JSON.parse(
  readFileSync('../../../../pkg/sep2admin/testdata/descriptor_v2.json', 'utf8'),
) as Descriptor

const keys = (table: Record<string, true>) => Object.keys(table).sort()
const sorted = (value: object) => Object.keys(value).sort()

const descriptorKeys = keys({ version: true, sections: true } satisfies Record<keyof Descriptor, true>)
const sectionKeys = keys({
  kind: true,
  heading: true,
  prose: true,
  empty: true,
  body: true,
} satisfies Record<keyof DescriptorSection, true>)
const tableKeys = keys({ columns: true, rows: true } satisfies Record<keyof DescriptorTableBody, true>)
const listKeys = keys({ groups: true } satisfies Record<keyof DescriptorDefinitionListBody, true>)
const groupKeys = keys({ heading: true, entries: true } satisfies Record<keyof DescriptorDefinitionGroup, true>)
const chartKeys = keys({ unit: true, series: true } satisfies Record<keyof DescriptorChartBody, true>)
const chartSeriesKeys = keys({ name: true, points: true } satisfies Record<keyof DescriptorChartSeries, true>)
const entryKeys = keys({ key: true, value: true } satisfies Record<keyof DescriptorDefinitionEntry, true>)

// A cell carries kind and text, plus the one field its kind adds.
const cellFields: Record<string, (keyof DescriptorCell)[]> = {
  text: [],
  badge: ['badge'],
  time: ['datetime'],
  link: ['href'],
}
const cellKeys = (kind: string) => ['kind', 'text', ...cellFields[kind]].sort()

function* cellsOf(d: Descriptor): Generator<DescriptorCell> {
  for (const section of d.sections) {
    if (section.kind === 'table') {
      for (const row of (section.body as DescriptorTableBody).rows) yield* row
    } else if (section.kind === 'definitionList') {
      for (const group of (section.body as DescriptorDefinitionListBody).groups) {
        for (const entry of group.entries) yield entry.value
      }
    }
  }
}

describe('Descriptor v2 wire shape', () => {
  it('has the keys the TypeScript types declare, at every level of the fixture', () => {
    expect(fixture.version).toBe(2)
    expect(sorted(fixture)).toEqual(descriptorKeys)
    expect(fixture.sections.map((s) => s.kind)).toEqual(['table', 'table', 'chart', 'definitionList'])
    for (const section of fixture.sections) {
      expect(sorted(section)).toEqual(sectionKeys)
      if (section.kind === 'table') {
        expect(sorted(section.body as object)).toEqual(tableKeys)
      } else if (section.kind === 'chart') {
        const body = section.body as DescriptorChartBody
        expect(sorted(body)).toEqual(chartKeys)
        for (const series of body.series) {
          expect(sorted(series)).toEqual(chartSeriesKeys)
          for (const point of series.points) expect(point).toHaveLength(2)
        }
      } else {
        const body = section.body as DescriptorDefinitionListBody
        expect(sorted(body)).toEqual(listKeys)
        for (const group of body.groups) {
          expect(sorted(group)).toEqual(groupKeys)
          for (const entry of group.entries) expect(sorted(entry)).toEqual(entryKeys)
        }
      }
    }
  })

  it('has, for every cell, exactly the fields its kind adds', () => {
    const cells = [...cellsOf(fixture)]
    expect(new Set(cells.map((c) => c.kind))).toEqual(new Set(['text', 'badge', 'time', 'link']))
    for (const cell of cells) expect(sorted(cell)).toEqual(cellKeys(cell.kind))
  })
})

describe('badgeClass', () => {
  it('maps each wire variant in the fixture to its own class, and anything else to neutral', () => {
    const wire = [...cellsOf(fixture)].filter((c) => c.kind === 'badge').map((c) => c.badge)
    expect(wire.map(badgeClass)).toEqual(['badge-ok', 'badge-error', 'badge-warn', 'badge-neutral', 'badge-info'])
    for (const hostile of ['"><script>', 'constructor', '__proto__', 'OK', '', undefined]) {
      expect(badgeClass(hostile)).toBe('badge-neutral')
    }
  })
})

// The same list pkg/sep2admin runs through its encoder check, so the
// renderer's second defence accepts exactly what the server lets through.
const hrefCases = JSON.parse(
  readFileSync('../../../../pkg/sep2admin/testdata/href_cases.json', 'utf8'),
) as { href: string; ok: boolean }[]

describe('safeHref', () => {
  it('accepts and refuses exactly the shared cases the Go encoder check does', () => {
    expect(hrefCases.length).toBeGreaterThan(30)
    expect(hrefCases.some((c) => c.ok)).toBe(true)
    expect(hrefCases.some((c) => !c.ok)).toBe(true)
    for (const c of hrefCases) {
      expect([c.href, safeHref(c.href) !== null]).toEqual([c.href, c.ok])
      if (c.ok) expect(safeHref(c.href)).toBe(c.href)
    }
  })

  it('refuses an undefined href', () => {
    expect(safeHref(undefined)).toBeNull()
  })
})

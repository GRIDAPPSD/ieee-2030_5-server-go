// A catalog with one row of each value kind and the stored curves, in the
// shape the two catalog routes reply, for tests of forms built from it. The
// names are arbitrary: the forms read the kind, never the name.
import type { CatalogRow, CurveInfo } from './controlcatalog'

export const FIXTURE_ROWS: CatalogRow[] = [
  { name: 'fnFlag', description: 'a flag', kind: 'boolean' },
  { name: 'fnLevel', description: 'a level', kind: 'integer', min: 0, max: 10000 },
  {
    name: 'fnPair',
    description: 'an object',
    kind: 'object',
    fields: [
      { name: 'amount', kind: 'integer', required: true, min: 1, max: 1000 },
      { name: 'sign', kind: 'boolean', required: true },
      { name: 'scale', kind: 'integer', min: -9, max: 9 },
    ],
  },
  { name: 'fnCurve', description: 'a curve', kind: 'curve', curveType: 11 },
]

export const FIXTURE_CURVES: CurveInfo[] = [
  { id: 'cv11', curveType: 11, description: 'volt-var one' },
  { id: 'cv12', curveType: 12, description: 'volt-watt one' },
]

// Routes for a fake fetch keyed "METHOD /path".
export const CATALOG_ROUTES: Record<string, { status: number; body?: unknown }> = {
  'GET /api/der/control-functions': { status: 200, body: { functions: FIXTURE_ROWS } },
  'GET /api/der/curves': { status: 200, body: { curves: FIXTURE_CURVES } },
}

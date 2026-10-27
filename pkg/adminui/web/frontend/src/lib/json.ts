// isObject narrows a decoded JSON value to a plain object, so a reader can
// check each field before it is used.
export function isObject(v: unknown): v is Record<string, unknown> {
  return typeof v === 'object' && v !== null && !Array.isArray(v)
}

// Formats a Date for a datetime-local input with step="1". Chromium rewrites a
// zero-seconds value to HH:MM, and fill() throws "Malformed value" when the
// read-back differs from what it wrote, so :00 is written already normalized.
export function localInput(d: Date): string {
  const p = (n: number) => String(n).padStart(2, '0');
  const base = `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())}T${p(d.getHours())}:${p(d.getMinutes())}`;
  return d.getSeconds() === 0 ? base : `${base}:${p(d.getSeconds())}`;
}

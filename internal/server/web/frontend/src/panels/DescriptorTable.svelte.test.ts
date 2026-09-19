// Value assertions (issue 368 criterion 5), the wrong-length-row decision
// (item 1), and the escaping proof (criterion 6, item 4). The escaping
// test's control is documented in the report: a raw-HTML version of this
// component was substituted, watched go RED against this same test, and
// reverted before this file was committed.
import { describe, expect, it } from 'vitest'
import { render, screen } from '@testing-library/svelte'
import DescriptorTable from './DescriptorTable.svelte'
import type { DescriptorTableBody } from '../lib/descriptor'

describe('DescriptorTable', () => {
  it('renders each supplied cell value as the exact text supplied', () => {
    const body: DescriptorTableBody = {
      columns: ['SFDI', 'Status'],
      rows: [
        ['123456789012', 'ONLINE'],
        ['210987654321', 'OFFLINE'],
      ],
    }
    render(DescriptorTable, { props: { body } })

    const cells = screen.getAllByTestId('descriptor-cell')
    expect(cells.map((c) => c.textContent)).toEqual(['123456789012', 'ONLINE', '210987654321', 'OFFLINE'])
  })

  it('renders the declared columns as headers, in order', () => {
    const body: DescriptorTableBody = { columns: ['A', 'B', 'C'], rows: [] }
    render(DescriptorTable, { props: { body } })

    expect(screen.getAllByRole('columnheader').map((h) => h.textContent)).toEqual(['A', 'B', 'C'])
  })

  it('renders an explicit empty state for zero rows', () => {
    render(DescriptorTable, { props: { body: { columns: ['A'], rows: [] } } })

    expect(screen.getByText('No rows')).toBeInTheDocument()
  })

  it('surfaces a wrong-length row instead of padding or truncating it', () => {
    // The Go doc comment (descriptor.go) explicitly does not enforce
    // len(row) == len(columns) and calls it "a bug for a renderer ...
    // to catch". This decides what the renderer does with that bug:
    // neither pad missing cells with an invented value nor silently
    // drop extra ones, since both would fabricate or destroy data with
    // no way to know which column a value belonged to. The row is
    // surfaced whole, flagged, with every value it carried still shown.
    const body: DescriptorTableBody = {
      columns: ['A', 'B', 'C'],
      rows: [['x', 'y']],
    }
    render(DescriptorTable, { props: { body } })

    const mismatch = screen.getByTestId('row-mismatch')
    expect(mismatch).toHaveTextContent('Malformed row: expected 3 columns, got 2: x, y')
    // Neither value was dropped, and no invented third cell appeared.
    expect(screen.queryAllByTestId('descriptor-cell')).toHaveLength(0)
  })

  it('renders a descriptor value containing each of the six named characters as text, not markup', () => {
    // Criterion 6's six characters, named: a script tag, an img with an
    // error handler, a double quote, a single quote, an ampersand, and a
    // null byte. The null byte is written as the \u0000 source escape
    // rather than an embedded raw byte, so this file stays plain text.
    const hostile = '<script>window.__pwned=1</script><img src=x onerror="window.__pwned=2">\'"&\u0000'
    const body: DescriptorTableBody = {
      columns: ['Value'],
      rows: [[hostile]],
    }
    render(DescriptorTable, { props: { body } })

    const cell = screen.getByTestId('descriptor-cell')
    // Read by the consumer's path: the DOM as a user's browser would see
    // it, not the component's props or a string comparison against the
    // source text.
    expect(cell.textContent).toBe(hostile)
    expect(cell.querySelector('script')).toBeNull()
    expect(cell.querySelector('img')).toBeNull()
    expect(cell.innerHTML).not.toContain('<script>')
    expect(cell.innerHTML).not.toContain('<img')
    expect((globalThis as { __pwned?: number }).__pwned).toBeUndefined()
  })
})

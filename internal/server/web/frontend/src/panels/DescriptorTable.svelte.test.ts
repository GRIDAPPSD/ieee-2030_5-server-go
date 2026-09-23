// Value assertions (issue 368 criterion 5), the wrong-length-row decision
// (item 1), and the escaping proof (criterion 6, item 4). The escaping
// test's control is documented in the report: a raw-HTML version of this
// component was substituted, watched go RED against this same test, and
// reverted before this file was committed.
import { describe, expect, it } from 'vitest'
import { render, screen } from '@testing-library/svelte'
import DescriptorTable from './DescriptorTable.svelte'
import type { DescriptorTableBody } from '../lib/descriptor'

// Criterion 6's six characters, named: a script tag, an img with an
// error handler, a double quote, a single quote, an ampersand, and a
// null byte. The null byte is written as the \u0000 source escape
// rather than an embedded raw byte, so this file stays plain text.
// Shared across every sink test in this file (fix round 1, item 3): each
// sink is checked with the same full hostile string, not a weaker one.
const hostile = '<script>window.__pwned=1</script><img src=x onerror="window.__pwned=2">\'"&\u0000'

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
    // No assertion on whether the script tag or the onerror handler
    // ran: an innerHTML-inserted <script> never executes and jsdom
    // loads no images, in every browser and regardless of what this
    // component does, so that check could not fail and proved nothing
    // (fix round 1, item 4). The four assertions below are what a
    // mutation to {@html cell} actually turns red.
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
  })

  // Fix round 1, item 1 (P1, P2): Columns and Rows have no `omitempty` on
  // the Go side, so an empty collection marshals as JSON null, not [].
  // Reproduced from the literal bytes the security lane measured:
  // marshalling a TableBody with a column set and no appended rows
  // prints {"columns":["A"],"rows":null}, and rendering that shape threw
  // before this fix.
  it('reproduces the empty-table wire bytes and treats null rows as empty, not a crash', () => {
    const wireBody = JSON.parse('{"columns":["A"],"rows":null}') as DescriptorTableBody
    render(DescriptorTable, { props: { body: wireBody } })

    expect(screen.getByText('No rows')).toBeInTheDocument()
    expect(screen.queryAllByTestId('descriptor-cell')).toHaveLength(0)
  })

  it('treats null columns as empty, rendering no header cells', () => {
    const wireBody = JSON.parse('{"columns":null,"rows":[]}') as DescriptorTableBody
    render(DescriptorTable, { props: { body: wireBody } })

    expect(screen.queryAllByRole('columnheader')).toHaveLength(0)
    expect(screen.getByText('No rows')).toBeInTheDocument()
  })

  it('treats a zero-length row against zero declared columns as an anomaly, not an invisible row', () => {
    // P8 / item 6: previously row.length === columns.length matched at
    // 0 === 0 and rendered a blank <tr> with no cells and no visible
    // signal. A zero-length row now always surfaces as the
    // malformed-row banner, even against zero columns.
    const body: DescriptorTableBody = { columns: [], rows: [[]] }
    render(DescriptorTable, { props: { body } })

    const mismatch = screen.getByTestId('row-mismatch')
    expect(mismatch).toHaveTextContent('Malformed row: expected 0 columns, got 0:')
    expect(screen.queryAllByTestId('descriptor-cell')).toHaveLength(0)
  })

  it('renders a hostile column header as text, not markup', () => {
    // Item 3: the column header sink had no escaping test.
    const body: DescriptorTableBody = { columns: [hostile], rows: [] }
    render(DescriptorTable, { props: { body } })

    const header = screen.getAllByTestId('descriptor-column-header')[0]
    expect(header.textContent).toBe(hostile)
    expect(header.querySelector('script')).toBeNull()
    expect(header.querySelector('img')).toBeNull()
    expect(header.innerHTML).not.toContain('<script>')
    expect(header.innerHTML).not.toContain('<img')
  })

  it('renders a hostile value in the malformed-row banner as text, not markup', () => {
    // Item 3: the malformed-row banner formats untrusted values into a
    // sentence and had no escaping test, despite being a sink this PR
    // introduced.
    const body: DescriptorTableBody = { columns: ['A', 'B', 'C'], rows: [[hostile, 'y']] }
    render(DescriptorTable, { props: { body } })

    const mismatch = screen.getByTestId('row-mismatch')
    expect(mismatch.textContent).toBe(`Malformed row: expected 3 columns, got 2: ${hostile}, y`)
    expect(mismatch.querySelector('script')).toBeNull()
    expect(mismatch.querySelector('img')).toBeNull()
    expect(mismatch.innerHTML).not.toContain('<script>')
    expect(mismatch.innerHTML).not.toContain('<img')
  })
})

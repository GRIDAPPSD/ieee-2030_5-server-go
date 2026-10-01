// The v2 renderer. Each test names the behavior it can fail on; the
// escaping and link tests use the shared hostile string so every sink is
// checked with the same full payload.
/// <reference types="node" />
import { readFileSync } from 'node:fs'
import { describe, expect, it } from 'vitest'
import { render, screen, within } from '@testing-library/svelte'
import DescriptorPanel from './DescriptorPanel.svelte'
import type { Descriptor, DescriptorCell, DescriptorSection } from '../lib/descriptor'

const fixture = JSON.parse(
  readFileSync('../../../../pkg/sep2admin/testdata/descriptor_v2.json', 'utf8'),
) as Descriptor

const hostile = '<script>window.__pwned=1</script><img src=x onerror="window.__pwned=2">\'"&\u0000'

function tableSection(cells: DescriptorCell[], over: Partial<DescriptorSection> = {}): DescriptorSection {
  return {
    kind: 'table',
    heading: '',
    prose: [],
    empty: '',
    body: { columns: cells.map(() => 'c'), rows: [cells] },
    ...over,
  }
}

// The variant class, without the scoped-style hash Svelte appends.
function variantClass(el: Element): string | undefined {
  return [...el.classList].find((c) => c.startsWith('badge-'))
}

function renderOne(section: DescriptorSection) {
  return render(DescriptorPanel, { props: { descriptor: { version: 2, sections: [section] } } })
}

describe('DescriptorPanel v2', () => {
  it('renders every section of the fixture: two tables and a definition list, with headings and prose', () => {
    render(DescriptorPanel, { props: { descriptor: fixture } })

    expect(screen.getAllByTestId('descriptor-section')).toHaveLength(3)
    expect(screen.getAllByRole('table')).toHaveLength(2)
    expect(screen.getAllByTestId('descriptor-heading').map((h) => h.textContent)).toEqual(['Registry', 'Clients'])
    expect(screen.getByTestId('descriptor-prose')).toHaveTextContent('Devices the bridge has registered.')
    expect(screen.getAllByRole('columnheader').map((h) => h.textContent)).toEqual(['Name', 'State', 'Last seen', 'Docs', 'LFDI'])
    expect(screen.getByTestId('descriptor-group-heading')).toHaveTextContent('Connection')
    expect(screen.getAllByTestId('descriptor-key').map((k) => k.textContent)).toEqual(['Status', 'Topic', 'Badges', 'Info'])
  })

  it('shows a section\'s empty text for a table with no rows and for a list with no entries', () => {
    const { unmount } = renderOne({
      kind: 'table',
      heading: 'T',
      prose: [],
      empty: 'No clients connected.',
      body: { columns: ['LFDI'], rows: [] },
    })
    expect(screen.getByTestId('descriptor-table-empty')).toHaveTextContent('No clients connected.')
    unmount()

    renderOne({
      kind: 'definitionList',
      heading: 'L',
      prose: [],
      empty: 'Nothing to list.',
      body: { groups: [{ heading: '', entries: [] }] },
    })
    expect(screen.getByTestId('descriptor-list-empty')).toHaveTextContent('Nothing to list.')
  })

  it('gives each badge the class of its closed-set variant, and neutral to a variant outside the set', () => {
    render(DescriptorPanel, { props: { descriptor: fixture } })
    const classes = screen.getAllByTestId('descriptor-badge').map((b) => [b.textContent, variantClass(b)])
    expect(classes).toEqual([
      ['accepted', 'badge-ok'],
      ['rejected', 'badge-error'],
      ['degraded', 'badge-warn'],
      ['n', 'badge-neutral'],
      ['i', 'badge-info'],
    ])
  })

  it('never lets a hostile badge value reach the class attribute', () => {
    renderOne(tableSection([{ kind: 'badge', text: 'x', badge: '" onmouseover="window.__pwned=3' }]))
    const badge = screen.getByTestId('descriptor-badge')
    expect(variantClass(badge)).toBe('badge-neutral')
    expect(badge.getAttributeNames().sort()).toEqual(['class', 'data-testid'])
  })

  it('shows a time cell\'s display text inside a time element carrying the machine value', () => {
    renderOne(tableSection([{ kind: 'time', text: '5 minutes ago', datetime: '2026-10-01T18:30:00Z' }]))
    const time = screen.getByText('5 minutes ago')
    expect(time.tagName).toBe('TIME')
    expect(time.getAttribute('datetime')).toBe('2026-10-01T18:30:00Z')
  })

  it('links http, https and relative hrefs', () => {
    render(DescriptorPanel, { props: { descriptor: fixture } })
    const links = screen.getAllByTestId('descriptor-link')
    expect(links.map((a) => [a.textContent, a.getAttribute('href')])).toEqual([
      ['devices', '/ui/devices'],
      ['spec', 'https://example.org/a?b=c'],
    ])
    expect(links.every((a) => a.getAttribute('rel') === 'noopener noreferrer')).toBe(true)
  })

  it('shows a link with any other scheme as plain text with no anchor', () => {
    for (const href of ['javascript:window.__pwned=4', 'data:text/html,x', '//evil.example/x']) {
      const { container, unmount } = renderOne(tableSection([{ kind: 'link', text: 'click me', href }]))
      expect(container.querySelector('a')).toBeNull()
      expect(screen.getByText('click me').tagName).toBe('SPAN')
      unmount()
    }
  })

  it('renders hostile text as text in every sink, with no element created from it', () => {
    const cell = (kind: string): DescriptorCell => ({ kind, text: hostile, badge: 'ok', datetime: hostile, href: '/x' })
    const { container } = render(DescriptorPanel, {
      props: {
        descriptor: {
          version: 2,
          sections: [
            {
              kind: 'table',
              heading: hostile,
              prose: [hostile],
              empty: hostile,
              body: { columns: [hostile], rows: [[cell('text')], [cell('badge')], [cell('time')], [cell('link')]] },
            },
            {
              kind: 'definitionList',
              heading: '',
              prose: [],
              empty: hostile,
              body: { groups: [{ heading: hostile, entries: [{ key: hostile, value: cell('text') }] }] },
            },
            { kind: hostile, heading: '', prose: [], empty: '', body: null },
          ],
        },
      },
    })

    expect(screen.getByTestId('descriptor-heading').textContent).toBe(hostile)
    expect(screen.getByTestId('descriptor-prose').textContent).toBe(hostile)
    expect(screen.getByTestId('descriptor-column-header').textContent).toBe(hostile)
    expect(screen.getAllByTestId('descriptor-cell').map((c) => c.textContent)).toEqual([hostile, hostile, hostile, hostile])
    expect(screen.getByTestId('descriptor-group-heading').textContent).toBe(hostile)
    expect(screen.getByTestId('descriptor-key').textContent).toBe(hostile)
    expect(screen.getByTestId('descriptor-unknown-kind').textContent).toBe(`Unsupported content type: "${hostile}"`)
    expect(container.querySelector('script')).toBeNull()
    expect(container.querySelector('img')).toBeNull()
    expect((window as unknown as { __pwned?: number }).__pwned).toBeUndefined()
  })

  it('says so for an unsupported version, an unknown section kind, a malformed body and no sections', () => {
    const { unmount } = render(DescriptorPanel, { props: { descriptor: { version: 1, sections: [] } } })
    expect(screen.getByTestId('descriptor-unsupported-version')).toHaveTextContent('Unsupported content version: 1')
    unmount()

    const empty = render(DescriptorPanel, { props: { descriptor: { version: 2, sections: [] } } })
    expect(screen.getByTestId('descriptor-empty')).toHaveTextContent('This panel has no content.')
    empty.unmount()

    const odd = renderOne({ kind: 'chart', heading: '', prose: [], empty: '', body: {} })
    expect(screen.getByTestId('descriptor-unknown-kind')).toHaveTextContent('Unsupported content type: "chart"')
    odd.unmount()

    renderOne({ kind: 'table', heading: '', prose: [], empty: '', body: { columns: ['A'] } })
    expect(screen.getByTestId('descriptor-malformed-body')).toBeInTheDocument()
  })

  it('surfaces a wrong-length row instead of padding or truncating it', () => {
    renderOne({
      kind: 'table',
      heading: '',
      prose: [],
      empty: '',
      body: {
        columns: ['A', 'B'],
        rows: [[{ kind: 'text', text: 'only' }]],
      },
    })
    const row = screen.getByTestId('row-mismatch')
    expect(within(row).getByText(/expected 2 columns, got 1: only/)).toBeInTheDocument()
    expect(screen.queryByTestId('descriptor-cell')).toBeNull()
  })
})

// The dispatch decision (issue 368 criterion 3): kind is read by name,
// and an absent kind, an unrecognised kind, and a body that does not
// match its kind (an absent body included) each render an honest
// message rather than nothing or a crash.
import { describe, expect, it } from 'vitest'
import { render, screen } from '@testing-library/svelte'
import DescriptorPanel from './DescriptorPanel.svelte'
import type { Descriptor } from '../lib/descriptor'

describe('DescriptorPanel', () => {
  it('dispatches a table kind to the table renderer', () => {
    const descriptor: Descriptor = {
      version: 1,
      kind: 'table',
      body: { columns: ['A'], rows: [['x']] },
    }
    render(DescriptorPanel, { props: { descriptor } })

    expect(screen.getByRole('columnheader', { name: 'A' })).toBeInTheDocument()
    expect(screen.getByTestId('descriptor-cell')).toHaveTextContent('x')
  })

  it('dispatches a definitionList kind to the definition-list renderer', () => {
    const descriptor: Descriptor = {
      version: 1,
      kind: 'definitionList',
      body: { groups: [{ heading: '', entries: [{ key: 'k', value: 'v' }] }] },
    }
    render(DescriptorPanel, { props: { descriptor } })

    expect(screen.getByTestId('descriptor-key')).toHaveTextContent('k')
    expect(screen.getByTestId('descriptor-value')).toHaveTextContent('v')
  })

  it('renders an honest empty state for a descriptor with no body, rather than nothing', () => {
    const descriptor: Descriptor = { version: 1 }
    render(DescriptorPanel, { props: { descriptor } })

    expect(screen.getByTestId('descriptor-empty')).toHaveTextContent('This panel has no content.')
  })

  it('renders an honest message for a kind this frontend does not recognise, rather than crashing', () => {
    // A future server version could add a shape this build predates;
    // simulating that is exactly why kind is typed loosely on the wire.
    const descriptor: Descriptor = { version: 1, kind: 'chart', body: { anything: true } }
    render(DescriptorPanel, { props: { descriptor } })

    expect(screen.getByTestId('descriptor-unknown-kind')).toHaveTextContent('Unsupported content type: "chart"')
  })

  it('renders a hostile unrecognised kind as text, not markup', () => {
    // Item 3: the unsupported-kind echo sink had no escaping test.
    const hostile = '<script>window.__pwned=1</script><img src=x onerror="window.__pwned=2">\'"&\u0000'
    const descriptor: Descriptor = { version: 1, kind: hostile }
    render(DescriptorPanel, { props: { descriptor } })

    const unknown = screen.getByTestId('descriptor-unknown-kind')
    expect(unknown.textContent).toBe(`Unsupported content type: "${hostile}"`)
    expect(unknown.querySelector('script')).toBeNull()
    expect(unknown.querySelector('img')).toBeNull()
    expect(unknown.innerHTML).not.toContain('<script>')
    expect(unknown.innerHTML).not.toContain('<img')
  })

  // Fix round 1, item 2 (P3): a kind present with no body previously
  // threw instead of rendering the honest message the doc comment
  // promises.
  it('renders an honest message for a table kind with no body, rather than throwing', () => {
    const descriptor: Descriptor = { version: 1, kind: 'table' }
    render(DescriptorPanel, { props: { descriptor } })

    expect(screen.getByTestId('descriptor-malformed-body')).toBeInTheDocument()
  })

  // Fix round 1, item 2 (P3): a kind whose body is the other shape
  // previously threw on property access instead of rendering the
  // honest message.
  it('renders an honest message for a table kind whose body is a definitionList shape, rather than throwing', () => {
    const descriptor: Descriptor = {
      version: 1,
      kind: 'table',
      body: { groups: [{ heading: '', entries: [{ key: 'k', value: 'v' }] }] },
    }
    render(DescriptorPanel, { props: { descriptor } })

    expect(screen.getByTestId('descriptor-malformed-body')).toBeInTheDocument()
  })

  it('renders an honest message for a definitionList kind whose body is a table shape, rather than throwing', () => {
    const descriptor: Descriptor = {
      version: 1,
      kind: 'definitionList',
      body: { columns: ['A'], rows: [['x']] },
    }
    render(DescriptorPanel, { props: { descriptor } })

    expect(screen.getByTestId('descriptor-malformed-body')).toBeInTheDocument()
  })

  // Fix round 1, item 1 (P1, P2): reproduced from the literal wire bytes
  // the security lane measured, through the full dispatch path.
  it('reproduces the empty-table wire bytes end to end and renders the empty state, not a crash', () => {
    const wireDescriptor = JSON.parse(
      '{"version":1,"kind":"table","body":{"columns":["A"],"rows":null}}',
    ) as Descriptor
    render(DescriptorPanel, { props: { descriptor: wireDescriptor } })

    expect(screen.getByRole('columnheader', { name: 'A' })).toBeInTheDocument()
    expect(screen.getByText('No rows')).toBeInTheDocument()
  })

  // Fix round 1, item 5: the version field was declared and never read.
  // Register already refuses an unsupported Panel.DescriptorVersion at
  // boot, so this covers the wire body a future build's server could
  // still send.
  it('renders an honest message for an unsupported descriptor version, rather than guessing at the shape', () => {
    const descriptor: Descriptor = {
      version: 99,
      kind: 'table',
      body: { columns: ['A'], rows: [['x']] },
    }
    render(DescriptorPanel, { props: { descriptor } })

    expect(screen.getByTestId('descriptor-unsupported-version')).toHaveTextContent(
      'Unsupported content version: 99',
    )
    expect(screen.queryByTestId('descriptor-cell')).toBeNull()
  })
})

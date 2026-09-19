// The dispatch decision (issue 368 criterion 3): kind is read by name,
// and both an absent body and an unrecognised kind render an honest
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
})

// Value assertions (issue 368 criterion 5) and the flat-list heading
// decision (descriptor.go: a single group with an empty heading is the
// normal case and must not render an empty heading element).
import { describe, expect, it } from 'vitest'
import { render, screen } from '@testing-library/svelte'
import DescriptorDefinitionList from './DescriptorDefinitionList.svelte'
import type { DescriptorDefinitionListBody } from '../lib/descriptor'

// Same hostile string as DescriptorTable's escaping test (fix round 1,
// item 5): this file's own version previously dropped the img/onerror
// vector and the null byte.
const hostile = '<script>window.__pwned=1</script><img src=x onerror="window.__pwned=2">\'"&\u0000'

describe('DescriptorDefinitionList', () => {
  it('renders each supplied entry value as the exact text supplied', () => {
    const body: DescriptorDefinitionListBody = {
      groups: [
        {
          heading: '',
          entries: [
            { key: 'TLS Cipher', value: 'TLS_AES_128_GCM_SHA256' },
            { key: 'Uptime', value: '3h12m' },
          ],
        },
      ],
    }
    render(DescriptorDefinitionList, { props: { body } })

    const keys = screen.getAllByTestId('descriptor-key')
    const values = screen.getAllByTestId('descriptor-value')
    expect(keys.map((k) => k.textContent)).toEqual(['TLS Cipher', 'Uptime'])
    expect(values.map((v) => v.textContent)).toEqual(['TLS_AES_128_GCM_SHA256', '3h12m'])
  })

  it('renders no heading element for a flat list with an empty heading', () => {
    const body: DescriptorDefinitionListBody = {
      groups: [{ heading: '', entries: [{ key: 'k', value: 'v' }] }],
    }
    const { container } = render(DescriptorDefinitionList, { props: { body } })

    expect(container.querySelector('h2')).toBeNull()
  })

  it('renders a heading element when a group carries one, and separates sectioned groups', () => {
    const body: DescriptorDefinitionListBody = {
      groups: [
        { heading: 'Connection topics', entries: [{ key: 'Publish', value: 'a/b' }] },
        { heading: 'Last applied control', entries: [{ key: 'Setpoint', value: '42' }] },
      ],
    }
    render(DescriptorDefinitionList, { props: { body } })

    expect(screen.getByRole('heading', { name: 'Connection topics' })).toBeInTheDocument()
    expect(screen.getByRole('heading', { name: 'Last applied control' })).toBeInTheDocument()
  })

  it('renders a descriptor value containing each of the six named characters as text, not markup', () => {
    const body: DescriptorDefinitionListBody = {
      groups: [{ heading: '', entries: [{ key: 'k', value: hostile }] }],
    }
    render(DescriptorDefinitionList, { props: { body } })

    const value = screen.getByTestId('descriptor-value')
    expect(value.textContent).toBe(hostile)
    expect(value.querySelector('script')).toBeNull()
    expect(value.querySelector('img')).toBeNull()
    expect(value.innerHTML).not.toContain('<script>')
    expect(value.innerHTML).not.toContain('<img')
  })

  it('renders a hostile entry key as text, not markup', () => {
    // Item 3: the entry key sink had no escaping test (only the value
    // sink did).
    const body: DescriptorDefinitionListBody = {
      groups: [{ heading: '', entries: [{ key: hostile, value: 'v' }] }],
    }
    render(DescriptorDefinitionList, { props: { body } })

    const key = screen.getByTestId('descriptor-key')
    expect(key.textContent).toBe(hostile)
    expect(key.querySelector('script')).toBeNull()
    expect(key.querySelector('img')).toBeNull()
    expect(key.innerHTML).not.toContain('<script>')
    expect(key.innerHTML).not.toContain('<img')
  })

  it('renders a hostile group heading as text, not markup', () => {
    // Item 3: the group heading sink had no escaping test.
    const body: DescriptorDefinitionListBody = {
      groups: [{ heading: hostile, entries: [{ key: 'k', value: 'v' }] }],
    }
    render(DescriptorDefinitionList, { props: { body } })

    const heading = screen.getByTestId('descriptor-heading')
    expect(heading.textContent).toBe(hostile)
    expect(heading.querySelector('script')).toBeNull()
    expect(heading.querySelector('img')).toBeNull()
    expect(heading.innerHTML).not.toContain('<script>')
    expect(heading.innerHTML).not.toContain('<img')
  })

  // Fix round 1, item 6 (P8): an empty groups array rendered nothing at
  // all rather than the honest message every other empty descriptor
  // shape uses.
  it('renders an honest message for an empty groups list, rather than nothing', () => {
    render(DescriptorDefinitionList, { props: { body: { groups: [] } } })

    expect(screen.getByTestId('descriptor-list-empty')).toHaveTextContent('This panel has no content.')
  })

  // Fix round 1, item 1 (P1): Groups has no `omitempty` on the Go side,
  // so an empty list marshals as JSON null, not []. Reproduced from the
  // literal wire bytes.
  it('reproduces the null-groups wire bytes and renders the honest message, not a crash', () => {
    const wireBody = JSON.parse('{"groups":null}') as DescriptorDefinitionListBody
    render(DescriptorDefinitionList, { props: { body: wireBody } })

    expect(screen.getByTestId('descriptor-list-empty')).toHaveTextContent('This panel has no content.')
  })
})

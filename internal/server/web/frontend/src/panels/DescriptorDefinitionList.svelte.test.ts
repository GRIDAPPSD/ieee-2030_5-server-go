// Value assertions (issue 368 criterion 5) and the flat-list heading
// decision (descriptor.go: a single group with an empty heading is the
// normal case and must not render an empty heading element).
import { describe, expect, it } from 'vitest'
import { render, screen } from '@testing-library/svelte'
import DescriptorDefinitionList from './DescriptorDefinitionList.svelte'
import type { DescriptorDefinitionListBody } from '../lib/descriptor'

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

  it('renders a descriptor value containing markup characters as text, not markup', () => {
    const hostile = '<script>window.__pwned2=1</script>\'"&'
    const body: DescriptorDefinitionListBody = {
      groups: [{ heading: '', entries: [{ key: 'k', value: hostile }] }],
    }
    render(DescriptorDefinitionList, { props: { body } })

    const value = screen.getByTestId('descriptor-value')
    expect(value.textContent).toBe(hostile)
    expect(value.querySelector('script')).toBeNull()
    expect((globalThis as { __pwned2?: number }).__pwned2).toBeUndefined()
  })
})

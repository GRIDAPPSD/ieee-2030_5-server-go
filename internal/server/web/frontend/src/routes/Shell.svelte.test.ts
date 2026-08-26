import { describe, expect, it } from 'vitest'
import { render, screen } from '@testing-library/svelte'
import Shell from './Shell.svelte'

describe('Shell', () => {
  it('renders the landing heading, status text, and a link back to the existing dashboard', () => {
    render(Shell)

    expect(screen.getByRole('heading', { level: 1 })).toHaveTextContent(
      'IEEE 2030.5 Server Admin',
    )
    expect(screen.getByTestId('shell-status')).toHaveTextContent('Admin UI shell is live')

    const link = screen.getByTestId('dashboard-link')
    expect(link).toHaveAttribute('href', '/')
    expect(link).toHaveTextContent('Existing dashboard')
  })
})

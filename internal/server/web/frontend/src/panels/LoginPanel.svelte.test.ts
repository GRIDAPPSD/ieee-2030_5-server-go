import { describe, expect, it } from 'vitest'
import { render, screen } from '@testing-library/svelte'
import LoginPanel from './LoginPanel.svelte'

describe('LoginPanel', () => {
  it('posts the key to the login route as a real form submit', () => {
    const { container } = render(LoginPanel, { props: { reason: 'Admin session required.' } })

    const form = container.querySelector('form') as HTMLFormElement
    expect(form).not.toBeNull()
    expect(form.getAttribute('method')?.toUpperCase()).toBe('POST')
    expect(form.getAttribute('action')).toBe('/auth/login')

    const key = container.querySelector('#loginKey') as HTMLInputElement
    expect(key.name).toBe('key')
    expect(key.type).toBe('password')

    expect(screen.getByTestId('login-reason')).toHaveTextContent('Admin session required.')
  })
})

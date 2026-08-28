import { describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen, waitFor } from '@testing-library/svelte'
import CreateFsa from './CreateFsa.svelte'
import * as api from '../lib/api'

describe('CreateFsa', () => {
  it('posts the typed mRID and primacy and renders the created href and mRID', async () => {
    const post = vi.spyOn(api, 'postJSON').mockResolvedValue({
      ok: true,
      data: { href: '/api/fsas/fsa-42', mRID: 'fsa-42', description: 'roof fleet', primacy: 1 },
    })
    const onCreated = vi.fn()

    const { container } = render(CreateFsa, { props: { onCreated } })
    await fireEvent.input(container.querySelector('#newFSADesc') as HTMLInputElement, {
      target: { value: 'roof fleet' },
    })
    await fireEvent.input(container.querySelector('#newFSAMRID') as HTMLInputElement, {
      target: { value: 'fsa-42' },
    })
    await fireEvent.input(container.querySelector('#newFSAPrimacy') as HTMLInputElement, {
      target: { value: '1' },
    })
    await fireEvent.click(screen.getByRole('button', { name: 'Create FSA' }))

    await waitFor(() => expect(post).toHaveBeenCalledTimes(1))
    expect(post).toHaveBeenCalledWith('/api/fsas', {
      description: 'roof fleet',
      mRID: 'fsa-42',
      primacy: 1,
    })
    expect(container.querySelector('#createFSAResult')).toHaveTextContent(
      'Created /api/fsas/fsa-42 (mRID=fsa-42)',
    )
    expect(onCreated).toHaveBeenCalledTimes(1)
  })

  it('omits a blank mRID from the body so the server generates one', async () => {
    const post = vi.spyOn(api, 'postJSON').mockResolvedValue({
      ok: true,
      data: { href: '/api/fsas/generated', mRID: 'generated', description: 'd', primacy: 0 },
    })

    const { container } = render(CreateFsa)
    await fireEvent.input(container.querySelector('#newFSADesc') as HTMLInputElement, {
      target: { value: 'd' },
    })
    await fireEvent.click(screen.getByRole('button', { name: 'Create FSA' }))

    await waitFor(() => expect(post).toHaveBeenCalledTimes(1))
    expect(post).toHaveBeenCalledWith('/api/fsas', { description: 'd' })
  })

  it('refuses a blank description without posting', async () => {
    const post = vi.spyOn(api, 'postJSON')

    const { container } = render(CreateFsa)
    await fireEvent.click(screen.getByRole('button', { name: 'Create FSA' }))

    await waitFor(() => {
      expect(container.querySelector('#createFSAResult')).toHaveTextContent('Description required.')
    })
    expect(post).not.toHaveBeenCalled()
  })

  it('reports a duplicate mRID conflict with its status', async () => {
    vi.spyOn(api, 'postJSON').mockResolvedValue({
      ok: false,
      error: 'fsa with this mRID already exists',
      status: 409,
    })

    const { container } = render(CreateFsa)
    await fireEvent.input(container.querySelector('#newFSADesc') as HTMLInputElement, {
      target: { value: 'dupe' },
    })
    await fireEvent.click(screen.getByRole('button', { name: 'Create FSA' }))

    await waitFor(() => {
      expect(container.querySelector('#createFSAResult')).toHaveTextContent(
        'Error (409): fsa with this mRID already exists',
      )
    })
  })
})

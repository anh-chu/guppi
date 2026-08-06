// @vitest-environment jsdom
import { describe, it, expect, vi, afterEach } from 'vitest'
import { render, screen, fireEvent, waitFor, cleanup } from '@testing-library/react'
import { NewSessionModal, type NewSessionInput } from './NewSessionModal'
import type { Host } from '../hooks/useHosts'

afterEach(() => {
  cleanup()
})

const localHost: Host = {
  id: 'local-fingerprint',
  owner_id: 'local-owner-id',
  name: 'local',
  local: true,
  online: true,
  sessions: [],
  last_seen: '',
}

describe('NewSessionModal', () => {
  it('calls onCreateSession with a single typed NewSessionInput object, no positional args', async () => {
    const onCreateSession = vi.fn().mockResolvedValue(undefined)
    render(
      <NewSessionModal hosts={[localHost]} sessions={[]} onCreateSession={onCreateSession} onClose={() => {}} />,
    )

    fireEvent.change(screen.getByTestId('ns-path'), { target: { value: '/tmp/proj' } })
    fireEvent.change(screen.getByTestId('ns-name'), { target: { value: 'my-session' } })
    fireEvent.click(screen.getByRole('button', { name: 'Create' }))

    await waitFor(() => expect(onCreateSession).toHaveBeenCalledTimes(1))
    const input: NewSessionInput = onCreateSession.mock.calls[0][0]
    expect(input).toMatchObject({ name: 'my-session', cwd: '/tmp/proj' })
    expect(input.agentType).toBe('claude')
    expect(input.shell).toBe('claude')
    // Positional-args contract must be gone: exactly one argument.
    expect(onCreateSession.mock.calls[0]).toHaveLength(1)
  })

  it('a successful worktree create resolves and never displays the session name as an error (regression)', async () => {
    const onCreateSession = vi.fn().mockResolvedValue(undefined)
    render(
      <NewSessionModal hosts={[localHost]} sessions={[]} onCreateSession={onCreateSession} onClose={() => {}} />,
    )

    fireEvent.change(screen.getByTestId('ns-path'), { target: { value: '/tmp/proj' } })
    fireEvent.click(screen.getByText('Create as worktree'))
    fireEvent.change(screen.getByTestId('ns-worktree-branch'), { target: { value: 'feature-x' } })
    fireEvent.change(screen.getByTestId('ns-name'), { target: { value: 'my-worktree-session' } })
    fireEvent.click(screen.getByRole('button', { name: 'Create' }))

    await waitFor(() => expect(onCreateSession).toHaveBeenCalledTimes(1))
    expect(onCreateSession.mock.calls[0][0]).toMatchObject({
      name: 'my-worktree-session',
      worktreeBranch: 'feature-x',
    })

    // Historical bug: any string returned from onCreateSession (including
    // the newly created session's own name) was rendered as an error. With
    // the void-returning contract there is nothing to misinterpret; no error
    // text ever appears on success.
    await waitFor(() => {
      expect(screen.queryByText('my-worktree-session')).toBeNull()
    })
  })

  it('renders the thrown error message on failure for a non-worktree create, and the modal itself never self-closes', async () => {
    const onCreateSession = vi.fn().mockRejectedValue(new Error('backend rejected create'))
    render(
      <NewSessionModal hosts={[localHost]} sessions={[]} onCreateSession={onCreateSession} onClose={() => {}} />,
    )

    fireEvent.change(screen.getByTestId('ns-path'), { target: { value: '/tmp/proj' } })
    fireEvent.click(screen.getByRole('button', { name: 'Create' }))

    await waitFor(() => { screen.getByText('backend rejected create') })
    // Modal never unmounts itself on failure -- staying open is the parent's
    // responsibility, satisfied here by never calling onClose.
    expect(screen.getByTestId('ns-path')).toBeTruthy()
  })

  it('renders the same thrown error message on failure for a worktree create -- identical error semantics, no special-cased string comparison', async () => {
    const onCreateSession = vi.fn().mockRejectedValue(new Error('backend rejected create'))
    render(
      <NewSessionModal hosts={[localHost]} sessions={[]} onCreateSession={onCreateSession} onClose={() => {}} />,
    )

    fireEvent.change(screen.getByTestId('ns-path'), { target: { value: '/tmp/proj' } })
    fireEvent.click(screen.getByText('Create as worktree'))
    fireEvent.change(screen.getByTestId('ns-worktree-branch'), { target: { value: 'feature-y' } })
    fireEvent.click(screen.getByRole('button', { name: 'Create' }))

    await waitFor(() => { screen.getByText('backend rejected create') })
    expect(screen.getByTestId('ns-path')).toBeTruthy()
  })

  it('passes the selected HostSelect option value straight through as targetOwner (already a canonical OwnerID)', async () => {
    const remoteHost: Host = {
      id: 'remote-fingerprint',
      owner_id: 'remote-owner-id',
      name: 'remote',
      online: true,
      sessions: [],
      last_seen: '',
    }
    const onCreateSession = vi.fn().mockResolvedValue(undefined)
    render(
      <NewSessionModal hosts={[localHost, remoteHost]} sessions={[]} onCreateSession={onCreateSession} onClose={() => {}} />,
    )

    fireEvent.change(screen.getByTestId('ns-path'), { target: { value: '/tmp/proj' } })
    fireEvent.change(screen.getByTestId('ns-name'), { target: { value: 'remote-sess' } })
    fireEvent.change(screen.getByRole('combobox'), { target: { value: 'remote-owner-id' } })
    fireEvent.click(screen.getByRole('button', { name: 'Create' }))

    await waitFor(() => expect(onCreateSession).toHaveBeenCalledTimes(1))
    expect(onCreateSession.mock.calls[0][0]).toMatchObject({ targetOwner: 'remote-owner-id' })
  })
})

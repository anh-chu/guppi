// @vitest-environment jsdom
import { render, screen, waitFor, fireEvent, cleanup } from '@testing-library/react'
import { expect, test, vi, beforeEach, afterEach, describe } from 'vitest'
import { PeersSection } from './PeersSection'

const self = { name: 'Self', fingerprint: 'self', public_key: 'self-pk' }

const basePeer = {
  public_key: 'pk1',
  fingerprint: 'fp1',
  name: 'Alpha',
  address: '10.0.0.1',
  enabled: false,
  status: 'idle' as const,
  paired_at: '2024-01-01T00:00:00Z',
  is_dialer: true,
}

function ok(body?: unknown) {
  return { ok: true, json: async () => body }
}

function setupFetch(...responses: unknown[]) {
  let i = 0
  return vi.fn(() => Promise.resolve(ok(responses[i++])))
}

describe('PeersSection', () => {
  beforeEach(() => {
    vi.stubGlobal('confirm', vi.fn(() => true))
  })

  afterEach(() => {
    cleanup()
    vi.unstubAllGlobals()
  })

  test('toggles peer enabled and refreshes the list', async () => {
    const fetchMock = setupFetch(
      { self, peers: [{ ...basePeer, enabled: false }] },
      {},
      { self, peers: [{ ...basePeer, enabled: true }] },
    )
    vi.stubGlobal('fetch', fetchMock)

    render(<PeersSection />)
    await waitFor(() => expect(screen.getByText('Alpha')).toBeTruthy())

    const checkbox = screen.getByLabelText('Auto-reconnect') as HTMLInputElement
    expect(checkbox.checked).toBe(false)

    fireEvent.click(checkbox)

    await waitFor(() =>
      expect(fetchMock).toHaveBeenCalledWith(
        '/api/peers/fp1',
        expect.objectContaining({ method: 'PATCH', body: JSON.stringify({ enabled: true }) }),
      ),
    )
    await waitFor(() => expect(checkbox.checked).toBe(true))
  })

  test('reconnects a dialer peer and refreshes status', async () => {
    const fetchMock = setupFetch(
      { self, peers: [{ ...basePeer, enabled: true, status: 'idle' as const }] },
      {},
      { self, peers: [{ ...basePeer, enabled: true, status: 'dialing' as const }] },
    )
    vi.stubGlobal('fetch', fetchMock)

    render(<PeersSection />)
    await waitFor(() => expect(screen.getByText('Alpha')).toBeTruthy())

    const button = screen.getByText('Reconnect now')
    fireEvent.click(button)

    await waitFor(() =>
      expect(fetchMock).toHaveBeenCalledWith(
        '/api/peers/fp1/reconnect',
        expect.objectContaining({ method: 'POST' }),
      ),
    )
    await waitFor(() => expect(screen.getByText('dialing…')).toBeTruthy())
  })

  test('forgets a peer after confirmation and clears the list', async () => {
    const fetchMock = setupFetch(
      { self, peers: [{ ...basePeer, enabled: true, status: 'idle' as const }] },
      {},
      { self, peers: [] },
    )
    vi.stubGlobal('fetch', fetchMock)

    render(<PeersSection />)
    await waitFor(() => expect(screen.getByText('Alpha')).toBeTruthy())

    fireEvent.click(screen.getByText('Forget'))

    await waitFor(() =>
      expect(fetchMock).toHaveBeenCalledWith(
        '/api/peers/fp1',
        expect.objectContaining({ method: 'DELETE' }),
      ),
    )
    await waitFor(() => expect(screen.getByText('No connected machines yet.')).toBeTruthy())
  })
})

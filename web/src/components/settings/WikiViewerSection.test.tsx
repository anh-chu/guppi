// @vitest-environment jsdom
import { render, screen, waitFor, fireEvent, cleanup } from '@testing-library/react'
import { expect, test, vi, beforeEach, afterEach } from 'vitest'
import { WikiViewerSection } from './WikiViewerSection'

const mockPrefs = vi.hoisted(() => ({
  prefs: { wiki_disabled: false },
  updatePrefs: vi.fn(),
}))

vi.mock('../../hooks/usePreferences', () => ({
  usePreferences: () => mockPrefs,
}))

function ok(body?: unknown) {
  return { ok: true, json: async () => body }
}

beforeEach(() => {
  mockPrefs.updatePrefs.mockClear()
})

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
})

test('fetches wiki status on mount', async () => {
  const fetchMock = vi.fn().mockResolvedValue(
    ok({ installed: false, installing: false, running: false, version: '', error: '', default_root: '' }),
  )
  vi.stubGlobal('fetch', fetchMock)

  render(<WikiViewerSection />)

  await waitFor(() => expect(fetchMock).toHaveBeenCalledWith('/api/wiki/status'))
})

test('install button POSTs /api/wiki/install and shows installing state', async () => {
  const fetchMock = vi
    .fn()
    .mockResolvedValueOnce(ok({ installed: false, installing: false, running: false, version: '', error: '', default_root: '' }))
    .mockResolvedValueOnce({ ok: true })
    .mockResolvedValueOnce(ok({ installed: false, installing: true, running: false, version: '', error: '', default_root: '' }))
  vi.stubGlobal('fetch', fetchMock)

  render(<WikiViewerSection />)

  await waitFor(() => expect(fetchMock).toHaveBeenCalledWith('/api/wiki/status'))

  const button = screen.getByRole('button', { name: /Install file viewer/i })
  fireEvent.click(button)

  await waitFor(() =>
    expect(fetchMock).toHaveBeenCalledWith(
      '/api/wiki/install',
      expect.objectContaining({ method: 'POST' }),
    ),
  )
  await waitFor(() => expect(screen.getByText('Installing...')).toBeTruthy(), { timeout: 3000 })
})

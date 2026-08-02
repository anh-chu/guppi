// @vitest-environment jsdom
import { render, screen, waitFor, fireEvent, within, cleanup } from '@testing-library/react'
import { expect, test, vi, beforeEach, afterEach } from 'vitest'
import { Settings } from '../Settings'

const mockPrefs = vi.hoisted(() => ({
  updatePrefs: vi.fn(),
  prefs: {
    terminal: {
      font_size: 13,
      font_family: 'Space Mono',
      scrollback: 50000,
      renderer: 'dom',
      unicode_graphemes: false,
      predictive_echo: false,
    },
    theme: 'dark',
    sidebar: { default_collapsed: false, collapse_mode: 'small' },
    default_view: 'overview',
    notifications: { statuses: ['waiting', 'stuck', 'error', 'completed'] },
    agent_banner: { auto_dismiss_seconds: 0 },
    lock_timeout_minutes: 30,
    fullscreen_hide_alerts: true,
    default_agent: 'claude',
    wiki_disabled: false,
    ai_naming: { enabled: false, endpoint: '', api_key: '', model: 'gpt-4o-mini' },
  },
}))

vi.mock('../../hooks/usePreferences', () => ({
  usePreferences: () => mockPrefs,
}))

beforeEach(() => {
  mockPrefs.updatePrefs.mockClear()
  vi.stubGlobal(
    'fetch',
    vi.fn().mockResolvedValue({
      ok: true,
      json: async () => ({ agents: [], setup_command: '' }),
    }),
  )
})

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
})

test('toggle change calls updatePrefs', async () => {
  render(
    <Settings
      pushState="unsupported"
      onPushSubscribe={() => {}}
      onPushUnsubscribe={() => {}}
      bucket="look"
    />,
  )

  await waitFor(() => expect(screen.getByText('Hide Alerts in Fullscreen')).toBeTruthy())

  const row = screen.getByText('Hide Alerts in Fullscreen').parentElement!.parentElement!
  const toggle = within(row).getByRole('switch')

  expect(mockPrefs.updatePrefs).not.toHaveBeenCalled()

  fireEvent.click(toggle)

  await waitFor(() =>
    expect(mockPrefs.updatePrefs).toHaveBeenCalledWith(
      expect.objectContaining({ fullscreen_hide_alerts: false }),
    ),
  )
})

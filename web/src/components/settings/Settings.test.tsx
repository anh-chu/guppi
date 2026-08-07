// @vitest-environment jsdom
import { render, screen, waitFor, fireEvent, within, cleanup } from '@testing-library/react'
import { expect, test, vi, beforeEach, afterEach } from 'vitest'
import { Settings } from '../Settings'

const mockPrefs = vi.hoisted(() => ({
  updatePrefs: vi.fn().mockImplementation((partial: any, opts?: any) => Promise.resolve(true)),
  prefs: {
    terminal: {
      font_size: 13,
      font_family: 'Space Mono',
      scrollback: 50000,
      unicode_graphemes: false,
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

test('renderer select removed, predictive echo removed', async () => {
  render(
    <Settings
      pushState="unsupported"
      onPushSubscribe={() => {}}
      onPushUnsubscribe={() => {}}
      bucket="look"
    />,
  )

  await waitFor(() => expect(screen.getByText('Unicode Graphemes')).toBeTruthy())

  expect(screen.queryByText(/Renderer/)).toBeNull()
  expect(screen.queryByText(/Predictive Echo/)).toBeNull()
})

test('AI Naming: inputs do not call updatePrefs until Save clicked', async () => {
  mockPrefs.prefs = { ...mockPrefs.prefs, ai_naming: { ...mockPrefs.prefs.ai_naming, enabled: true } }
  mockPrefs.updatePrefs.mockClear()

  render(
    <Settings
      pushState="unsupported"
      onPushSubscribe={() => {}}
      onPushUnsubscribe={() => {}}
      bucket="yard"
    />,
  )

  await waitFor(() => expect(screen.getByPlaceholderText('https://api.openai.com/v1')).toBeTruthy())

  const endpointInput = screen.getByPlaceholderText('https://api.openai.com/v1') as HTMLInputElement
  fireEvent.change(endpointInput, { target: { value: 'https://example.com' } })

  // updatePrefs should NOT be called yet
  expect(mockPrefs.updatePrefs).not.toHaveBeenCalled()
})

test('AI Naming: Save button disabled when not dirty', async () => {
  mockPrefs.prefs = { ...mockPrefs.prefs, ai_naming: { ...mockPrefs.prefs.ai_naming, enabled: true } }
  mockPrefs.updatePrefs.mockClear()

  render(
    <Settings
      pushState="unsupported"
      onPushSubscribe={() => {}}
      onPushUnsubscribe={() => {}}
      bucket="yard"
    />,
  )

  await waitFor(() => expect(screen.getByRole('button', { name: /Save/i })).toBeTruthy())

  const saveButton = screen.getByRole('button', { name: /Save/i }) as HTMLButtonElement
  expect(saveButton.disabled).toBe(true)
})

test('AI Naming: validation error on empty endpoint with enabled=true', async () => {
  mockPrefs.prefs = { ...mockPrefs.prefs, ai_naming: { ...mockPrefs.prefs.ai_naming, enabled: true, endpoint: 'https://example.com' } }
  mockPrefs.updatePrefs.mockClear()

  render(
    <Settings
      pushState="unsupported"
      onPushSubscribe={() => {}}
      onPushUnsubscribe={() => {}}
      bucket="yard"
    />,
  )

  await waitFor(() => expect(screen.getByPlaceholderText('https://api.openai.com/v1')).toBeTruthy())

  const endpointInput = screen.getByPlaceholderText('https://api.openai.com/v1') as HTMLInputElement
  fireEvent.change(endpointInput, { target: { value: '' } })

  const saveButton = screen.getByRole('button', { name: /Save/i })
  fireEvent.click(saveButton)

  await waitFor(() => expect(screen.getByText(/Endpoint is required/i)).toBeTruthy())
  expect(mockPrefs.updatePrefs).not.toHaveBeenCalled()
})

test('AI Naming: Save button present and initially disabled', async () => {
  mockPrefs.prefs = { ...mockPrefs.prefs, ai_naming: { ...mockPrefs.prefs.ai_naming, enabled: true } }
  mockPrefs.updatePrefs.mockClear()

  render(
    <Settings
      pushState="unsupported"
      onPushSubscribe={() => {}}
      onPushUnsubscribe={() => {}}
      bucket="yard"
    />,
  )

  await waitFor(() => expect(screen.getByPlaceholderText('https://api.openai.com/v1')).toBeTruthy())

  const saveButton = screen.getByRole('button', { name: /Save/i }) as HTMLButtonElement
  expect(saveButton).toBeTruthy()
  expect(saveButton.disabled).toBe(true) // Initially disabled when not dirty
})

test('AI Naming: enabled toggle still updates instantly', async () => {
  mockPrefs.prefs = { ...mockPrefs.prefs, ai_naming: { ...mockPrefs.prefs.ai_naming, enabled: false } }
  mockPrefs.updatePrefs.mockClear().mockImplementation(() => Promise.resolve(undefined))

  render(
    <Settings
      pushState="unsupported"
      onPushSubscribe={() => {}}
      onPushUnsubscribe={() => {}}
      bucket="yard"
    />,
  )

  await waitFor(() => {
    const toggles = screen.getAllByRole('switch')
    return toggles.length > 0
  })

  const aiNamingEnableSection = screen.getByText('Enable').parentElement!.parentElement!
  const toggle = within(aiNamingEnableSection).getByRole('switch')

  fireEvent.click(toggle)

  await waitFor(() =>
    expect(mockPrefs.updatePrefs).toHaveBeenCalledWith(
      expect.objectContaining({ ai_naming: expect.objectContaining({ enabled: true }) }),
    ),
  )
})

test('Font Family: selecting Custom reveals text input', async () => {
  mockPrefs.prefs = { ...mockPrefs.prefs, terminal: { ...mockPrefs.prefs.terminal, font_family: 'Space Mono' } }
  mockPrefs.updatePrefs.mockClear()

  render(
    <Settings
      pushState="unsupported"
      onPushSubscribe={() => {}}
      onPushUnsubscribe={() => {}}
      bucket="look"
    />,
  )

  await waitFor(() => expect(screen.getByDisplayValue('Space Mono')).toBeTruthy())

  const fontSelect = screen.getByDisplayValue('Space Mono') as HTMLSelectElement
  fireEvent.change(fontSelect, { target: { value: '__custom__' } })

  await waitFor(() => expect(screen.getByPlaceholderText(/e.g. Cascadia Code/)).toBeTruthy())
})

test('Font Family: typing custom value calls updatePrefs', async () => {
  mockPrefs.prefs = { ...mockPrefs.prefs, terminal: { ...mockPrefs.prefs.terminal, font_family: 'custom-font' } }
  mockPrefs.updatePrefs.mockClear()

  render(
    <Settings
      pushState="unsupported"
      onPushSubscribe={() => {}}
      onPushUnsubscribe={() => {}}
      bucket="look"
    />,
  )

  await waitFor(() => expect(screen.getByPlaceholderText(/e.g. Cascadia Code/)).toBeTruthy())

  const customInput = screen.getByPlaceholderText(/e.g. Cascadia Code/) as HTMLInputElement
  fireEvent.change(customInput, { target: { value: 'Cascadia Code' } })

  await waitFor(() =>
    expect(mockPrefs.updatePrefs).toHaveBeenCalledWith(
      expect.objectContaining({ terminal: expect.objectContaining({ font_family: 'Cascadia Code' }) }),
    ),
  )
})

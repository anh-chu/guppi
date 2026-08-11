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
    custom_theme: null as Record<string, string> | null,
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

test('renderer select removed, predictive echo removed, unicode graphemes toggle removed (always on)', async () => {
  render(
    <Settings
      pushState="unsupported"
      onPushSubscribe={() => {}}
      onPushUnsubscribe={() => {}}
      bucket="look"
    />,
  )

  await waitFor(() => expect(screen.getByText('Hide Alerts in Fullscreen')).toBeTruthy())

  expect(screen.queryByText(/Renderer/)).toBeNull()
  expect(screen.queryByText(/Predictive Echo/)).toBeNull()
  expect(screen.queryByText('Unicode Graphemes')).toBeNull()
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

test('AI Naming: empty endpoint is accepted and saved (env-var fallback is valid config)', async () => {
  mockPrefs.prefs = { ...mockPrefs.prefs, ai_naming: { ...mockPrefs.prefs.ai_naming, enabled: true, endpoint: 'https://example.com' } }
  mockPrefs.updatePrefs.mockClear()
  mockPrefs.updatePrefs.mockResolvedValue(true)

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

  await waitFor(() => expect(mockPrefs.updatePrefs).toHaveBeenCalled())
  expect(screen.queryByText(/Endpoint is required/i)).toBeNull()
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

test('theme picker: deleted presets are gone, only Default/Dark/Light/Custom shown', async () => {
  render(
    <Settings
      pushState="unsupported"
      onPushSubscribe={() => {}}
      onPushUnsubscribe={() => {}}
      bucket="look"
    />,
  )

  await waitFor(() => expect(screen.getByText('Default')).toBeTruthy())

  expect(screen.getByText('Default')).toBeTruthy()
  expect(screen.getByText('Dark')).toBeTruthy()
  expect(screen.getByText('Light')).toBeTruthy()
  expect(screen.getByText('Custom')).toBeTruthy()
  expect(screen.queryByText('Retro CRT Blue')).toBeNull()
  expect(screen.queryByText('Green Phosphor')).toBeNull()
  expect(screen.queryByText('Midnight')).toBeNull()
})

test('theme picker: selecting Custom reveals the palette editor', async () => {
  render(
    <Settings
      pushState="unsupported"
      onPushSubscribe={() => {}}
      onPushUnsubscribe={() => {}}
      bucket="look"
    />,
  )

  await waitFor(() => expect(screen.getByText('Custom')).toBeTruthy())

  expect(screen.queryByText('Custom Palette')).toBeNull()

  fireEvent.click(screen.getByText('Custom'))

  await waitFor(() =>
    expect(mockPrefs.updatePrefs).toHaveBeenCalledWith(
      expect.objectContaining({ theme: 'custom' }),
    ),
  )
})

test('theme picker: editing a custom color persists via updatePrefs', async () => {
  mockPrefs.prefs = { ...mockPrefs.prefs, theme: 'custom', custom_theme: null }
  mockPrefs.updatePrefs.mockClear()

  render(
    <Settings
      pushState="unsupported"
      onPushSubscribe={() => {}}
      onPushUnsubscribe={() => {}}
      bucket="look"
    />,
  )

  await waitFor(() => expect(screen.getByText('Custom Palette')).toBeTruthy())

  const backgroundRow = screen.getByText('Background').parentElement!.parentElement!
  const backgroundText = within(backgroundRow).getAllByDisplayValue('#0a0a09').find(el => (el as HTMLInputElement).type === 'text') as HTMLInputElement

  fireEvent.change(backgroundText, { target: { value: '#123456' } })

  await waitFor(() =>
    expect(mockPrefs.updatePrefs).toHaveBeenCalledWith(
      expect.objectContaining({ custom_theme: expect.objectContaining({ background: '#123456' }) }),
    ),
  )
})

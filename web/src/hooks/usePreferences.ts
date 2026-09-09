import { useState, useEffect, useCallback, createContext, useContext } from 'react'

export interface Preferences {
  terminal: {
    font_size: number
    font_family: string
    scrollback: number
    // renderer field retained for backward-compat with persisted preferences and
    // backend API contract, but is no longer consulted by rendering code.
    // WebGL is now unconditionally attempted with automatic silent DOM fallback.
    renderer: string
  }
  theme: string
  sidebar: {
    default_collapsed: boolean
    collapse_mode: string
  }
  default_view: string
  notifications: {
    statuses: string[]
  }
  agent_banner: {
    auto_dismiss_seconds: number
  }
  lock_timeout_minutes: number
  fullscreen_hide_alerts: boolean
  default_agent: string
  // Inverted: absent or false means the wiki panel is on. See preferences.go.
  wiki_disabled: boolean
  ai_naming: {
    enabled: boolean
    endpoint: string
    api_key: string
    model: string
  }
  custom_theme: Record<string, string> | null
}

export const defaultPreferences: Preferences = {
  terminal: {
    font_size: 13,
    font_family: 'JetBrains Mono',
    scrollback: 50000,
    renderer: 'webgl',
  },
  theme: 'dark',
  sidebar: {
    default_collapsed: false,
    collapse_mode: 'small',
  },
  default_view: 'overview',
  notifications: {
    statuses: ['waiting', 'stuck', 'error', 'completed'],
  },
  agent_banner: {
    auto_dismiss_seconds: 0,
  },
  lock_timeout_minutes: 30,
  fullscreen_hide_alerts: true,
  default_agent: 'claude',
  wiki_disabled: false,
  ai_naming: {
    enabled: false,
    endpoint: '',
    api_key: '',
    model: 'gpt-4o-mini',
  },
  custom_theme: null,
}

interface PreferencesContextValue {
  prefs: Preferences
  updatePrefs: (partial: Partial<Preferences>, opts?: { optimistic?: boolean }) => Promise<boolean>
  loaded: boolean
  refetch: () => Promise<void>
}

export const PreferencesContext = createContext<PreferencesContextValue>({
  prefs: defaultPreferences,
  updatePrefs: async () => false,
  loaded: false,
  refetch: async () => {},
})

export function usePreferencesProvider() {
  const [prefs, setPrefs] = useState<Preferences>(defaultPreferences)
  const [loaded, setLoaded] = useState(false)

  const fetchPrefs = useCallback(async () => {
    try {
      const res = await fetch('/api/preferences')
      if (!res.ok) return // don't parse 401/error responses as prefs
      const data = await res.json()
      // Validate shape before accepting
      if (data && typeof data.theme === 'string' && data.terminal) {
        setPrefs({
          ...defaultPreferences,
          ...data,
          terminal: { ...defaultPreferences.terminal, ...(data.terminal || {}) },
          sidebar: { ...defaultPreferences.sidebar, ...(data.sidebar || {}) },
          notifications: { ...defaultPreferences.notifications, ...(data.notifications || {}) },
          agent_banner: { ...defaultPreferences.agent_banner, ...(data.agent_banner || {}) },
          ai_naming: { ...defaultPreferences.ai_naming, ...(data.ai_naming || {}) },
        })
      }
    } catch {
      // ignore fetch errors
    }
    setLoaded(true)
  }, [])

  useEffect(() => {
    fetchPrefs()
  }, [fetchPrefs])

  const updatePrefs = useCallback(async (partial: Partial<Preferences>, opts?: { optimistic?: boolean }): Promise<boolean> => {
    const optimistic = opts?.optimistic !== false
    const merged = { ...prefs, ...partial }
    if (optimistic) setPrefs(merged)
    try {
      const res = await fetch('/api/preferences', {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(merged),
      })
      if (res.ok) {
        const saved = await res.json()
        setPrefs(saved)
        return true
      }
      return false
    } catch (err) {
      console.error('Failed to save preferences:', err)
      return false
    }
  }, [prefs])

  return { prefs, updatePrefs, loaded, refetch: fetchPrefs }
}

export function usePreferences() {
  return useContext(PreferencesContext)
}

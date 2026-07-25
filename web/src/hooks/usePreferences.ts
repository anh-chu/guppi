import { useState, useEffect, useCallback, createContext, useContext } from 'react'

export interface Preferences {
  terminal: {
    font_size: number
    font_family: string
    scrollback: number
    renderer: string
    unicode_graphemes: boolean
    predictive_echo: boolean
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
  wiki_viewer_url: string
  ai_naming: {
    enabled: boolean
    endpoint: string
    api_key: string
    model: string
  }
}

export const defaultPreferences: Preferences = {
  terminal: {
    font_size: 13,
    font_family: 'Space Mono',
    scrollback: 50000,
    renderer: 'dom',
    unicode_graphemes: false,
    predictive_echo: false,
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
  wiki_viewer_url: 'http://localhost:3000',
  ai_naming: {
    enabled: false,
    endpoint: '',
    api_key: '',
    model: 'gpt-4o-mini',
  },
}

interface PreferencesContextValue {
  prefs: Preferences
  updatePrefs: (partial: Partial<Preferences>) => Promise<void>
  loaded: boolean
  refetch: () => Promise<void>
}

export const PreferencesContext = createContext<PreferencesContextValue>({
  prefs: defaultPreferences,
  updatePrefs: async () => {},
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

  const updatePrefs = useCallback(async (partial: Partial<Preferences>) => {
    const merged = { ...prefs, ...partial }
    setPrefs(merged)
    try {
      const res = await fetch('/api/preferences', {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(merged),
      })
      if (res.ok) {
        const saved = await res.json()
        setPrefs(saved)
      }
    } catch (err) {
      console.error('Failed to save preferences:', err)
    }
  }, [prefs])

  return { prefs, updatePrefs, loaded, refetch: fetchPrefs }
}

export function usePreferences() {
  return useContext(PreferencesContext)
}

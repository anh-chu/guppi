import { useState, useEffect } from 'react'
import { Login } from './components/Login'
import { Setup } from './components/Setup'
import { usePreferencesProvider, PreferencesContext } from './hooks/usePreferences'
import { useAuth } from './hooks/useAuth'
import { applyTheme } from './theme'
import SessionApp from './SessionApp'

// App.tsx is an authentication/setup shell only. It resolves auth/setup
// state and then unconditionally renders SessionApp -- the only production
// UI. There is no runtime feature flag, env var, or localStorage switch
// that selects an alternate UI any more.
export default function App() {
  const prefsProvider = usePreferencesProvider()
  const { loading, authRequired, needsSetup, authenticated, error: authError, setup, login, logout } = useAuth()
  const [showOnboarding, setShowOnboarding] = useState(false)

  useEffect(() => {
    const syncViewport = () => {
      const viewport = window.visualViewport
      const height = viewport?.height ?? window.innerHeight
      const width = viewport?.width ?? window.innerWidth
      document.documentElement.style.setProperty('--app-height', `${Math.round(height)}px`)
      document.documentElement.style.setProperty('--app-width', `${Math.round(width)}px`)
    }

    syncViewport()
    window.addEventListener('resize', syncViewport)
    window.visualViewport?.addEventListener('resize', syncViewport)
    window.visualViewport?.addEventListener('scroll', syncViewport)

    return () => {
      window.removeEventListener('resize', syncViewport)
      window.visualViewport?.removeEventListener('resize', syncViewport)
      window.visualViewport?.removeEventListener('scroll', syncViewport)
    }
  }, [])

  // Re-fetch preferences after login (initial fetch may have gotten 401)
  useEffect(() => {
    if (authenticated) {
      prefsProvider.refetch()
    }
  }, [authenticated]) // eslint-disable-line react-hooks/exhaustive-deps

  // Apply last-used theme immediately (before auth) so login page is themed
  useEffect(() => {
    try {
      const cached = localStorage.getItem('termyard:theme')
      if (cached) {
        applyTheme(cached)
      }
    } catch {}
  }, [])

  // Apply theme when preferences load or theme changes, and cache for login page
  useEffect(() => {
    if (prefsProvider.loaded) {
      applyTheme(prefsProvider.prefs.theme)
      try {
        localStorage.setItem('termyard:theme', prefsProvider.prefs.theme)
      } catch {}
    }
  }, [prefsProvider.loaded, prefsProvider.prefs.theme])

  if (loading) {
    return <div className="flex items-center justify-center h-full w-full bg-background" />
  }

  if (authRequired && needsSetup) {
    const handleSetup = async (password: string) => {
      const ok = await setup(password)
      if (ok) setShowOnboarding(true)
      return ok
    }
    return <Login mode="setup" error={authError} onSubmit={handleSetup} />
  }

  if (authRequired && !authenticated) {
    return <Login mode="login" error={authError} onSubmit={login} />
  }

  if (authenticated && showOnboarding) {
    return (
      <PreferencesContext.Provider value={prefsProvider}>
        <Setup fullPage onComplete={() => {
          setShowOnboarding(false)
          try { localStorage.setItem('termyard:setup-seen', 'true') } catch {}
        }} />
      </PreferencesContext.Provider>
    )
  }

  return (
    <PreferencesContext.Provider value={prefsProvider}>
      <SessionApp onLogout={authRequired ? logout : undefined} authenticated={authRequired ? authenticated : true} />
    </PreferencesContext.Provider>
  )
}

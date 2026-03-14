import { useState, useEffect, useCallback } from 'react'

interface AuthState {
  loading: boolean
  authRequired: boolean
  needsSetup: boolean
  authenticated: boolean
  error: string | null
  setup: (password: string) => Promise<boolean>
  login: (password: string) => Promise<boolean>
  logout: () => Promise<void>
}

// Install a global fetch interceptor that fires a custom event on 401 responses.
// This lets the auth hook detect expired sessions without wrapping every fetch call.
let interceptorInstalled = false
function installFetchInterceptor() {
  if (interceptorInstalled) return
  interceptorInstalled = true
  const originalFetch = window.fetch
  window.fetch = async (...args) => {
    const res = await originalFetch(...args)
    if (res.status === 401) {
      // Don't fire for auth endpoints themselves
      const url = typeof args[0] === 'string' ? args[0] : (args[0] as Request).url
      if (!url.includes('/api/auth/')) {
        window.dispatchEvent(new Event('auth:unauthorized'))
      }
    }
    return res
  }
}

export function useAuth(): AuthState {
  const [loading, setLoading] = useState(true)
  const [authRequired, setAuthRequired] = useState(false)
  const [needsSetup, setNeedsSetup] = useState(false)
  const [authenticated, setAuthenticated] = useState(false)
  const [error, setError] = useState<string | null>(null)

  useEffect(() => {
    installFetchInterceptor()

    async function checkAuth() {
      try {
        // First check if auth is enabled at all
        const statusRes = await fetch('/api/auth/status')
        const status = await statusRes.json()
        if (!status.auth_required) {
          setAuthRequired(false)
          setAuthenticated(true)
          setLoading(false)
          return
        }
        setAuthRequired(true)

        if (status.needs_setup) {
          setNeedsSetup(true)
          setLoading(false)
          return
        }

        // Auth is required — check if we have a valid session
        const checkRes = await fetch('/api/auth/check')
        const check = await checkRes.json()
        setAuthenticated(check.authenticated === true)
      } catch {
        // If we can't reach the server, assume not authenticated
        setAuthenticated(false)
      }
      setLoading(false)
    }
    checkAuth()
  }, [])

  // Listen for 401s from the fetch interceptor
  useEffect(() => {
    if (!authRequired) return
    const onUnauthorized = () => setAuthenticated(false)
    window.addEventListener('auth:unauthorized', onUnauthorized)
    return () => window.removeEventListener('auth:unauthorized', onUnauthorized)
  }, [authRequired])

  const setup = useCallback(async (password: string): Promise<boolean> => {
    setError(null)
    try {
      const res = await fetch('/api/auth/setup', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ password }),
      })
      if (res.ok) {
        setNeedsSetup(false)
        setAuthenticated(true)
        return true
      }
      const data = await res.json()
      setError(data.error || 'Setup failed')
      return false
    } catch {
      setError('Connection failed')
      return false
    }
  }, [])

  const login = useCallback(async (password: string): Promise<boolean> => {
    setError(null)
    try {
      const res = await fetch('/api/auth/login', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ password }),
      })
      if (res.ok) {
        setAuthenticated(true)
        return true
      }
      const data = await res.json()
      setError(data.error || 'Invalid password')
      return false
    } catch {
      setError('Connection failed')
      return false
    }
  }, [])

  const logout = useCallback(async () => {
    try {
      await fetch('/api/auth/logout', { method: 'POST' })
    } catch {
      // ignore
    }
    setAuthenticated(false)
  }, [])

  return { loading, authRequired, needsSetup, authenticated, error, setup, login, logout }
}

//
// useTerminal.ts — React lease adapter for the terminal pool
//
// This hook no longer owns Terminal/addon/WS lifecycle. It delegates to
// the module-level TerminalPool and presents a stable API for Terminal.tsx.
//

import { useRef, useCallback, useState, useEffect } from 'react'
import { Terminal } from '@xterm/xterm'
import { usePreferences } from './usePreferences'
import { terminalPool, normalizeSelection } from '../lib/terminalPool'
import type { LeaseToken, CheckoutCallbacks, PoolIdentity, TerminalPrefs, ConnectionSnapshot } from '../lib/terminalPool'

// Re-export for external consumers (e.g. tests, clipboard usage)
export { normalizeSelection }

export function useTerminal(sessionName: string, hostId?: string, backend?: string) {
  const { prefs } = usePreferences()

  // Pool key
  const poolKey = hostId ? `${hostId}/${sessionName}` : sessionName

  // Refs
  const termRef = useRef<Terminal | null>(null)
  const leaseRef = useRef<LeaseToken | null>(null)
  const containerRef = useRef<HTMLElement | null>(null)

  // State
  const [termConnected, setTermConnected] = useState<boolean>(() => {
    // Synchronous initial snapshot — avoid disconnected overlay on warm switch
    const snap = terminalPool.getSnapshot(poolKey)
    return snap?.connected ?? false
  })
  const [ctrlModifierActive, setCtrlModifierActive] = useState(() => {
    const snap = terminalPool.getSnapshot(poolKey)
    return snap?.ctrlModifierActive ?? false
  })
  const [altModifierActive, setAltModifierActive] = useState(() => {
    const snap = terminalPool.getSnapshot(poolKey)
    return snap?.altModifierActive ?? false
  })
  const [selectionMenu, setSelectionMenu] = useState<{ x: number; y: number; text: string } | null>(null)

  // Callbacks — stable refs so we don't re-subscribe on every render
  const callbacksRef = useRef<CheckoutCallbacks>({
    onConnectionChange: (connected: boolean) => setTermConnected(connected),
    onCtrlModifierChange: (active: boolean) => setCtrlModifierActive(active),
    onAltModifierChange: (active: boolean) => setAltModifierActive(active),
    onSelectionMenu: (menu: { x: number; y: number; text: string } | null) => setSelectionMenu(menu),
  })

  // Build terminal prefs
  const buildPrefs = useCallback((): TerminalPrefs => ({
    theme: prefs.theme,
    customTheme: prefs.custom_theme,
    fontFamily: prefs.terminal.font_family,
    fontSize: prefs.terminal.font_size,
    scrollback: prefs.terminal.scrollback,
    renderer: prefs.terminal.renderer,
    unicodeGraphemes: prefs.terminal.unicode_graphemes,
  }), [prefs.theme, prefs.custom_theme, prefs.terminal.font_family, prefs.terminal.font_size, prefs.terminal.scrollback, prefs.terminal.renderer, prefs.terminal.unicode_graphemes])

  // Checkout into a container — called from Terminal.tsx layout effect
  const checkout = useCallback((container: HTMLElement) => {
    containerRef.current = container
    const identity: PoolIdentity = { sessionName, hostId, backend }
    const lease = terminalPool.checkout(identity, buildPrefs(), container, callbacksRef.current)
    leaseRef.current = lease

    // Update termRef
    const snap = terminalPool.getSnapshot(lease.key)
    if (snap) {
      termRef.current = terminalPool.getTerminalForPaste(lease)
    }
  }, [sessionName, hostId, backend, buildPrefs])

  // Checkin — called from cleanup
  const checkin = useCallback(() => {
    const lease = leaseRef.current
    if (lease) {
      terminalPool.checkin(lease)
      leaseRef.current = null
    }
    // Connection state: keep whatever the pool currently has for the key
    // (checkin does not set false). But if we're switching keys, don't
    // show stale "connected" from previous key.
    // The Terminal.tsx sessionName dep in the layout effect handles key switching.
  }, [])

  // Disconnect — now just checkin (no disposal)
  const disconnect = useCallback(() => {
    checkin()
  }, [checkin])

  // Connect — checkout into existing container
  const connect = useCallback((container: HTMLElement) => {
    checkout(container)
  }, [checkout])

  // Fit (lease-gated)
  const fit = useCallback(() => {
    const lease = leaseRef.current
    if (lease) terminalPool.fit(lease)
  }, [])

  // Focus (lease-gated)
  const focus = useCallback(() => {
    const lease = leaseRef.current
    if (lease) {
      terminalPool.focus(lease)
    }
  }, [])

  // Rebind / rehost (lease-gated, forced)
  const rebind = useCallback(() => {
    const lease = leaseRef.current
    if (lease && containerRef.current) {
      terminalPool.rehost(lease, containerRef.current, true)
      terminalPool.fit(lease)
    }
  }, [])

  // Send operations (lease-gated)
  const sendRawBytes = useCallback((bytes: Uint8Array) => {
    const lease = leaseRef.current
    if (lease) terminalPool.sendRawBytes(lease, bytes)
  }, [])

  const sendText = useCallback((text: string) => {
    if (!text) return
    sendRawBytes(new TextEncoder().encode(text))
  }, [sendRawBytes])

  const sendImage = useCallback((file: File, fallbackType: string) => {
    const lease = leaseRef.current
    if (lease) terminalPool.sendImage(lease, file, fallbackType)
  }, [])

  // Modifier toggles (lease-gated)
  const toggleCtrlModifier = useCallback(() => {
    const lease = leaseRef.current
    if (lease) terminalPool.toggleCtrlModifier(lease)
  }, [])

  const clearCtrlModifier = useCallback(() => {
    const lease = leaseRef.current
    if (lease) terminalPool.clearCtrlModifier(lease)
  }, [])

  const toggleAltModifier = useCallback(() => {
    const lease = leaseRef.current
    if (lease) terminalPool.toggleAltModifier(lease)
  }, [])

  const clearAltModifier = useCallback(() => {
    const lease = leaseRef.current
    if (lease) terminalPool.clearAltModifier(lease)
  }, [])

  // Selection menu (lease-gated)
  const setSelectionMenuFn = useCallback((menu: { x: number; y: number; text: string } | null) => {
    const lease = leaseRef.current
    if (lease) terminalPool.setSelectionMenu(lease, menu)
  }, [])

  // Preference reconfigure
  const reconfigure = useCallback(() => {
    // Global pref reconciliation — idempotent, iterates all entries
    terminalPool.applyGlobalPrefs(buildPrefs())
  }, [buildPrefs])

  // On key change: checkin old, update snapshot for new key
  const prevKeyRef = useRef(poolKey)
  // Must run synchronously before any paint to avoid flash
  // This reads pool snapshot BEFORE the layout effect checkout runs
  if (prevKeyRef.current !== poolKey) {
    // Sync-initialize connection state from pool for the new key
    const snap = terminalPool.getSnapshot(poolKey)
    if (snap) {
      // These are set synchronously during render — React batches them
      // We use useState's initializer to set the state, but for the key
      // switch case we need to force update. Since we're in a render function,
      // we can't call setters here. Instead, we use a ref to track the
      // key change and let useEffect handle it.
    }
    prevKeyRef.current = poolKey
  }

  // Effect: when poolKey changes, update state from pool snapshot
  // This runs after the layout effect checkout and avoids the stale state problem
  useEffect(() => {
    const snap = terminalPool.getSnapshot(poolKey)
    if (snap) {
      setTermConnected(snap.connected)
      setCtrlModifierActive(snap.ctrlModifierActive)
      setAltModifierActive(snap.altModifierActive)
    } else {
      // New key with no entry yet — these will be set by checkout callbacks
      // But we can set to defaults
      setTermConnected(false)
      setCtrlModifierActive(false)
      setAltModifierActive(false)
    }
  }, [poolKey])

  // Cleanup on unmount — checkin if we still own a lease
  useEffect(() => {
    return () => {
      const lease = leaseRef.current
      if (lease) {
        terminalPool.checkin(lease)
        leaseRef.current = null
      }
    }
  }, [])

  return {
    termRef,
    connect,
    disconnect,
    fit,
    rebind,
    focus,
    termConnected,
    sendRawBytes,
    sendText,
    sendImage,
    ctrlModifierActive,
    toggleCtrlModifier,
    clearCtrlModifier,
    altModifierActive,
    toggleAltModifier,
    clearAltModifier,
    selectionMenu,
    setSelectionMenu: setSelectionMenuFn,
    reconfigure,
  }
}

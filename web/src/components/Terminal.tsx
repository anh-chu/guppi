import { useEffect, useLayoutEffect, useRef, useState, useCallback } from 'react'
import { createPortal } from 'react-dom'
import { useTerminal } from '../hooks/useTerminal'
import { usePreferences } from '../hooks/usePreferences'
import { useArtifacts } from '../hooks/useArtifacts'
import { UploadStatus } from './UploadStatus'
import { cn } from '../lib/utils'
import { popOut, pipUnavailableReason } from '../lib/pip'
import { grantArtifactToken, getArtifactKind } from '../lib/artifactPreview'
import { ArtifactPreview } from './ArtifactPreview'
import { createFileLinkProvider } from '../lib/fileLinkProvider'
import type { IDisposable } from '@xterm/xterm'
import { useTerminalInput } from './terminal/useTerminalInput'
import { SelectionMenu } from './terminal/SelectionMenu'
import { MobileGestureKey, HoldableKey } from './terminal/MobileKeys'

interface TerminalProps {
  sessionName: string
  hostId?: string
  backend?: string
  fullscreen?: boolean
  onToggleFullscreen?: () => void
  // In split view, only the active pane shows the mobile key bar to avoid duplicates.
  keyBarEnabled?: boolean
  /**
   * Open a file in the app-level wiki panel. Terminal no longer owns one.
   * Returns false when the host declines (e.g. a remote-host session, which
   * wiki-viewer cannot read), in which case we fall back to the artifact-token
   * new-tab viewer that does work off-machine.
   */
  onOpenFile?: (path: string) => boolean
}

export function Terminal({ sessionName, hostId, backend, fullscreen, onToggleFullscreen, keyBarEnabled = true, onOpenFile }: TerminalProps) {
  const containerRef = useRef<HTMLDivElement>(null)
  const onOpenFileRef = useRef(onOpenFile)
  onOpenFileRef.current = onOpenFile
  const fileLinkDisposableRef = useRef<IDisposable | null>(null)
  const {
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
    setSelectionMenu,
    reconfigure,
  } = useTerminal(sessionName, hostId, backend)

  const {
    sendSequence,
    sendArrow,
    sendPage,
    handlePaste,
    handleChooseFile,
    handleFileSelection,
    uploadFiles,
    handleTerminalDragEnter,
    handleTerminalDragOver,
    handleTerminalDragLeave,
    handleTerminalDrop,
    handleCopy,
    clipboardMenuOpen,
    setClipboardMenuOpen,
    capturedText,
    setCapturedText,
    isDraggingFiles,
    fileInputRef,
    captureTextareaRef,
    uploads,
    cancelUpload,
    dismissUpload,
  } = useTerminalInput({
    sessionName,
    hostId,
    termRef,
    sendRawBytes,
    sendText,
    sendImage,
    termConnected,
    focus,
  })

  const { prefs } = usePreferences()
  const [showMobileKeyBar, setShowMobileKeyBar] = useState(false)
  const [artifactsOpen, setArtifactsOpen] = useState(false)
  const [expandedPreviewPath, setExpandedPreviewPath] = useState<string | null>(null)
  const { artifacts: serverArtifacts, refresh: refreshArtifacts } = useArtifacts(sessionName, hostId)

  // Shared by the selection-menu "Open file" button and the artifacts sidebar.
  const openFilePath = useCallback((path: string) => {
    // Open tab synchronously (popup blockers), then point it at token URL once grant minted.
    const tab = window.open('', '_blank')
    grantArtifactToken(path, sessionName, undefined, hostId)
      .then((token) => {
        if (tab) tab.location.href = `/file?token=${encodeURIComponent(token)}`
      })
      .catch(() => tab?.close())
  }, [sessionName, hostId])
  // Read by the file-link provider, which is registered in a layout effect that
  // must not list this in its deps (that would re-acquire the terminal lease).
  const openFilePathRef = useRef(openFilePath)
  openFilePathRef.current = openFilePath
  const downloadArtifact = useCallback((path: string, name: string) => {
    grantArtifactToken(path, sessionName, undefined, hostId)
      .then((token) => {
        const a = document.createElement('a')
        a.href = `/file?token=${encodeURIComponent(token)}`
        a.download = name || path.split('/').pop() || 'download'
        document.body.appendChild(a)
        a.click()
        document.body.removeChild(a)
      })
      .catch((err) => console.error('Download failed:', err))
  }, [sessionName, hostId])
  // The mobile key bar renders into a single shared slot at the bottom of the
  // app so split views show one full-width bar (active pane only), not one per pane.
  const [keyBarSlot, setKeyBarSlot] = useState<HTMLElement | null>(null)
  const popHomeRef = useRef<HTMLDivElement>(null)
  const popNodeRef = useRef<HTMLDivElement>(null)
  const [poppedOut, setPoppedOut] = useState(false)
  const restorePipRef = useRef<(() => void) | null>(null)

  const handlePopOut = useCallback(async () => {
    if (restorePipRef.current) { restorePipRef.current(); return }
    const reason = pipUnavailableReason()
    if (reason) {
      window.dispatchEvent(new CustomEvent('termyard:toast', {
        detail: { severity: 'warn', source: 'pop-out', message: reason },
      }))
      return
    }
    const node = popNodeRef.current, home = popHomeRef.current
    if (!node || !home) return
    try {
      const onRestore = () => {
        restorePipRef.current = null
        setPoppedOut(false)
        rebind()
      }
      const result = await popOut(node, home, { onRestore })
      setPoppedOut(true)
      rebind()
      restorePipRef.current = () => {
        if (restorePipRef.current === null) return // idempotent guard
        result.restore()
        onRestore()
      }
    } catch { /* user denied */ }
  }, [rebind])

  useEffect(() => {
    const media = window.matchMedia('(max-width: 900px), (pointer: coarse)')
    const sync = () => setShowMobileKeyBar(media.matches)
    sync()
    media.addEventListener('change', sync)
    return () => media.removeEventListener('change', sync)
  }, [])

  useEffect(() => {
    setKeyBarSlot(document.getElementById('mobile-keybar-slot'))
  }, [])

  useEffect(() => {
    if (showMobileKeyBar) return
    setClipboardMenuOpen(false)
  }, [showMobileKeyBar, setClipboardMenuOpen])

  // Layout-phase checkout/checkin: checkout before paint, checkin during cleanup.
  // Depends on canonical identity (name + host + backend) so backend changes
  // trigger a fresh cold-acquire via the pool, and same-identity warm switches
  // reuse the existing entry.
  useLayoutEffect(() => {
    const container = containerRef.current
    if (!container) return
    connect(container)
    // Register FileLinkProvider — makes file paths in terminal output clickable
    const term = termRef.current
    if (term) {
      fileLinkDisposableRef.current?.dispose()
      fileLinkDisposableRef.current = term.registerLinkProvider(
        createFileLinkProvider(term, ({ path }) => {
          // Fall back when there is no handler, or the handler declines because
          // this session lives on another host and wiki-viewer only sees its own
          // filesystem. Without the fallback a remote pane would clobber the
          // shared panel and then fail to resolve the path.
          if (!onOpenFileRef.current?.(path)) openFilePathRef.current(path)
        })
      )
    }
    return () => {
      disconnect()
      fileLinkDisposableRef.current?.dispose()
      fileLinkDisposableRef.current = null
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [sessionName, hostId, backend])

  // Reconfigure the live terminal when prefs change (scrollback triggers rebuild;
  // other changes are applied live), without tearing down the WebSocket connection.
  useEffect(() => {
    if (!prefs) return
    reconfigure()
  }, [prefs.terminal.scrollback, prefs.terminal.font_size, prefs.terminal.font_family,
      prefs.theme, reconfigure])

  // Auto-focus on mount only for the active pane — the inactive pane's
  // auto-focus would steal focus from the intended target.
  useEffect(() => {
    if (fullscreen && containerRef.current) {
      requestAnimationFrame(() => focus())
      setTimeout(() => focus(), 100)
    }
  }, [fullscreen, focus])

  // Refocus terminal when WebSocket reconnects (e.g. after iPad sleep)
  useEffect(() => {
    if (termConnected && !document.hidden && fullscreen) {
      setTimeout(() => {
        fit()
        focus()
        const textarea = containerRef.current?.querySelector('textarea.xterm-helper-textarea') as HTMLTextAreaElement | null
        textarea?.focus()
      }, 100)
    }
  }, [termConnected, fit, focus])

  // Refocus terminal when returning to the app/tab
  useEffect(() => {
    const refocus = () => {
      if (!document.hidden && containerRef.current && fullscreen) {
        setTimeout(() => {
          fit()
          focus()
          // On iPad, xterm's focus() doesn't always work — directly focus the textarea
          const textarea = containerRef.current?.querySelector('textarea.xterm-helper-textarea') as HTMLTextAreaElement | null
          textarea?.focus()
        }, 200)
      }
    }
    document.addEventListener('visibilitychange', refocus)
    // iOS also fires focus on the window when returning from app switcher
    window.addEventListener('focus', refocus)
    return () => {
      document.removeEventListener('visibilitychange', refocus)
      window.removeEventListener('focus', refocus)
    }
  }, [fit, focus])

  useEffect(() => {
    if (!containerRef.current) return
    let fitTimer: number | null = null
    // ponytail: touch devices slide the URL bar / soft keyboard over ~200-300ms,
    // firing ResizeObserver every frame. Short debounce refits mid-animation =
    // visible flash. Wait for the viewport to settle on coarse pointers.
    const settleMs = window.matchMedia('(pointer: coarse)').matches ? 250 : 50
    const observer = new ResizeObserver(() => {
      if (fitTimer !== null) clearTimeout(fitTimer)
      fitTimer = window.setTimeout(() => { fitTimer = null; fit() }, settleMs)
    })
    observer.observe(containerRef.current)
    return () => { observer.disconnect(); if (fitTimer !== null) clearTimeout(fitTimer) }
  }, [fit])

  // Touch scroll -> wheel events for terminal mouse mode
  useEffect(() => {
    const container = containerRef.current
    if (!container) return

    let lastY = 0
    let lastTime = 0
    let accumulated = 0
    let velocity = 0
    let inertiaId: number | null = null
    let lastClientX = 0
    let lastClientY = 0
    const BASE_LINE_HEIGHT = 20
    const MIN_VELOCITY = 0.5
    const FRICTION = 0.92
    const INERTIA_STOP = 0.3

    const dispatchScroll = (lines: number, clientX: number, clientY: number) => {
      const target = container.querySelector('.xterm-screen')
      if (!target) return
      for (let i = 0; i < Math.abs(lines); i++) {
        const wheelEvent = new WheelEvent('wheel', {
          deltaY: lines > 0 ? BASE_LINE_HEIGHT : -BASE_LINE_HEIGHT,
          deltaMode: 0,
          clientX,
          clientY,
          bubbles: true,
          cancelable: true,
        })
        target.dispatchEvent(wheelEvent)
      }
    }

    const processAccumulated = (clientX: number, clientY: number) => {
      const speed = Math.abs(velocity)
      let multiplier = 1
      if (speed > 2) multiplier = 2
      if (speed > 4) multiplier = 3
      if (speed > 7) multiplier = 5
      if (speed > 12) multiplier = 8
      const threshold = BASE_LINE_HEIGHT / multiplier
      while (Math.abs(accumulated) >= threshold) {
        const dir = accumulated > 0 ? 1 : -1
        dispatchScroll(dir, clientX, clientY)
        accumulated -= dir * threshold
      }
    }

    const stopInertia = () => {
      if (inertiaId !== null) {
        cancelAnimationFrame(inertiaId)
        inertiaId = null
      }
    }

    const inertiaLoop = () => {
      if (Math.abs(velocity) < INERTIA_STOP) {
        velocity = 0
        accumulated = 0
        inertiaId = null
        return
      }
      velocity *= FRICTION
      accumulated += velocity * 16
      processAccumulated(lastClientX, lastClientY)
      inertiaId = requestAnimationFrame(inertiaLoop)
    }

    const onTouchStart = (e: TouchEvent) => {
      if (e.touches.length !== 1) return
      stopInertia()
      lastY = e.touches[0].clientY
      lastTime = performance.now()
      accumulated = 0
      velocity = 0
    }

    const onTouchMove = (e: TouchEvent) => {
      if (e.touches.length !== 1) return
      e.preventDefault()
      const now = performance.now()
      const currentY = e.touches[0].clientY
      const deltaY = lastY - currentY
      const dt = now - lastTime
      lastClientX = e.touches[0].clientX
      lastClientY = e.touches[0].clientY
      if (dt > 0) {
        const instantVelocity = deltaY / dt
        velocity = velocity * 0.3 + instantVelocity * 0.7
      }
      accumulated += deltaY
      lastY = currentY
      lastTime = now
      processAccumulated(lastClientX, lastClientY)
    }

    const onTouchEnd = () => {
      if (Math.abs(velocity) > MIN_VELOCITY) {
        inertiaId = requestAnimationFrame(inertiaLoop)
      }
    }

    container.addEventListener('touchstart', onTouchStart, { passive: true })
    container.addEventListener('touchmove', onTouchMove, { passive: false })
    container.addEventListener('touchend', onTouchEnd, { passive: true })
    container.addEventListener('touchcancel', onTouchEnd, { passive: true })

    return () => {
      stopInertia()
      container.removeEventListener('touchstart', onTouchStart)
      container.removeEventListener('touchmove', onTouchMove)
      container.removeEventListener('touchend', onTouchEnd)
      container.removeEventListener('touchcancel', onTouchEnd)
    }
  }, [sessionName])

  // Refocus terminal after fullscreen toggle (especially needed on iPad where tapping the button steals focus)
  useEffect(() => {
    if (!fullscreen) return
    setTimeout(() => {
      fit()
      focus()
      const textarea = containerRef.current?.querySelector('textarea.xterm-helper-textarea') as HTMLTextAreaElement | null
      textarea?.focus()
    }, 100)
  }, [fullscreen, fit, focus])

  // Cmd/Ctrl+Shift+F toggles fullscreen, Escape exits (but not if quick switcher is open)
  useEffect(() => {
    if (!onToggleFullscreen) return
    const onKeyDown = (e: KeyboardEvent) => {
      if ((e.metaKey || e.ctrlKey) && e.shiftKey && e.key.toLowerCase() === 'f') {
        e.preventDefault()
        onToggleFullscreen()
        return
      }
      if (e.key === 'Escape' && fullscreen) {
        // Don't steal Escape from overlays like the quick switcher
        if (document.querySelector('[data-quick-switcher]')) return
        e.preventDefault()
        onToggleFullscreen()
      }
    }
    window.addEventListener('keydown', onKeyDown)
    return () => window.removeEventListener('keydown', onKeyDown)
  }, [fullscreen, onToggleFullscreen])

  // Idempotent PiP cleanup on unmount: if component unmounts while popped out,
  // restore the wrapper to home (or close empty PiP window) and let the pool
  // checkin move the terminal to the hidden host.
  useEffect(() => {
    return () => {
      if (restorePipRef.current) {
        restorePipRef.current()
        restorePipRef.current = null
        setPoppedOut(false)
      }
    }
  }, [])

  return (
    <div className="flex-1 overflow-hidden flex flex-row bg-canvas">
      <div className="h-full w-full flex flex-col p-[3px]">
        <div ref={popHomeRef} className="min-h-0 flex-1 relative">
        <div
          ref={popNodeRef}
          className={cn('absolute inset-0', isDraggingFiles && 'ring-2 ring-inset ring-primary')}
          onDragEnter={handleTerminalDragEnter}
          onDragOver={handleTerminalDragOver}
          onDragLeave={handleTerminalDragLeave}
          onDrop={handleTerminalDrop}
        >
          <div
            ref={containerRef}
            className="absolute inset-0 overflow-hidden"
          />
          <UploadStatus uploads={uploads} onCancel={cancelUpload} onDismiss={dismissUpload} />
        {/* Pop-out (PiP) toggle */}
          <button
            onClick={handlePopOut}
            title={poppedOut ? 'Return pane to tab' : 'Pop out to floating window'}
            className="absolute top-2.5 right-11 z-20 p-1.5 rounded-sm bg-surface border border-hairline text-mute hover:text-primary transition-all opacity-0 group-hover:opacity-100 focus-visible:opacity-100"
          >
            <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2.5" strokeLinecap="round" strokeLinejoin="round">
              <rect x="2" y="4" width="20" height="16" rx="2" /><rect x="12" y="11" width="8" height="6" rx="1" fill="currentColor" />
            </svg>
          </button>
        {/* Fullscreen toggle */}
          {onToggleFullscreen && (
            <button
              onClick={onToggleFullscreen}
              title={fullscreen ? 'Exit fullscreen (Esc / Cmd+Shift+F)' : 'Fullscreen (Cmd+Shift+F)'}
              className="absolute top-2.5 right-2.5 z-20 p-1.5 rounded-sm bg-surface border border-hairline text-mute hover:text-primary transition-all opacity-0 group-hover:opacity-100 focus-visible:opacity-100"
            >
              {fullscreen ? (
                <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2.5" strokeLinecap="round" strokeLinejoin="round">
                  <polyline points="4 14 10 14 10 20" /><polyline points="20 10 14 10 14 4" /><line x1="14" y1="10" x2="21" y2="3" /><line x1="3" y1="21" x2="10" y2="14" />
                </svg>
              ) : (
                <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2.5" strokeLinecap="round" strokeLinejoin="round">
                  <polyline points="15 3 21 3 21 9" /><polyline points="9 21 3 21 3 15" /><line x1="21" y1="3" x2="14" y2="10" /><line x1="3" y1="21" x2="10" y2="14" />
                </svg>
              )}
            </button>
          )}
          {serverArtifacts.length > 0 && (
            <button
              onClick={() => {
                setArtifactsOpen((v) => !v)
              }}
              title="Detected files"
              className="absolute top-2.5 right-[74px] z-20 flex items-center gap-1 px-1.5 py-1 rounded-sm bg-surface border border-hairline text-mute hover:text-primary transition-all"
            >
              <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2.5" strokeLinecap="round" strokeLinejoin="round">
                <path d="M13 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V9z" /><polyline points="13 2 13 9 20 9" />
              </svg>
              <span className="text-[10px] font-bold">{serverArtifacts.length}</span>
            </button>
          )}
          {artifactsOpen && (
            <>
              <div className="fixed inset-0 z-40" onMouseDown={() => setArtifactsOpen(false)} />
              <div className="absolute top-10 right-2.5 z-50 w-96 max-h-[70vh] overflow-y-auto bg-surface-elevated border border-hairline rounded-md shadow-lg flex flex-col">
                <div className="flex items-center justify-between px-3 py-2 border-b border-hairline">
                  <span className="text-[11px] font-bold uppercase tracking-widest text-mute">Detected Files</span>
                  <button onClick={() => void refreshArtifacts()} className="text-[10px] font-bold uppercase text-mute hover:text-ink">Refresh</button>
                </div>
                {[...serverArtifacts].reverse().map((art) => {
                  const kind = getArtifactKind(art.path, art.name)
                  const displayPath = art.display_path || art.path
                  const isExpanded = expandedPreviewPath === art.path
                  const canPreview = kind !== 'other' && !art.stale
                  return (
                    <div key={art.path} className="border-b border-hairline/40 last:border-b-0">
                      <div
                        className={cn(
                          'flex items-start gap-2 px-3 py-2 hover:bg-surface transition-colors',
                          art.stale && 'opacity-70'
                        )}
                      >
                        <button
                          type="button"
                          onMouseDown={(e) => e.preventDefault()}
                          onClick={() => {
                            // Same fallback as the terminal link path: a remote
                            // session declines, and the token viewer handles it.
                            if (!onOpenFile?.(art.path)) openFilePath(art.path)
                            setArtifactsOpen(false)
                          }}
                          className="min-w-0 flex-1 text-left text-xs font-mono"
                          title={art.path}
                        >
                          <div className={cn('flex min-w-0 items-center gap-2', art.stale && 'text-mute/70 italic')}>
                            <span className="min-w-0 truncate">{displayPath}</span>
                            {art.stale && (
                              <span className="flex-none rounded border border-hairline px-1 py-0.5 text-[9px] font-bold uppercase tracking-widest text-mute/70 not-italic">
                                deleted
                              </span>
                            )}
                          </div>
                          <div className="mt-0.5 flex items-center gap-2 text-[10px] uppercase tracking-widest text-mute/60">
                            <span>{art.source}</span>
                            {art.tool && <span>{art.tool}</span>}
                            {kind !== 'other' && <span>{kind}</span>}
                          </div>
                        </button>
                        {!art.stale && (
                          <button
                            type="button"
                            onMouseDown={(e) => e.preventDefault()}
                            onClick={() => downloadArtifact(art.path, art.name)}
                            title="Download"
                            className="flex-none rounded-sm border border-hairline px-1.5 py-1 text-mute hover:text-primary transition-colors"
                          >
                            <svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2.5" strokeLinecap="round" strokeLinejoin="round">
                              <path d="M21 15v4a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2v-4" /><polyline points="7 10 12 15 17 10" /><line x1="12" y1="15" x2="12" y2="3" />
                            </svg>
                          </button>
                        )}
                        {canPreview && (
                          <button
                            type="button"
                            onMouseDown={(e) => e.preventDefault()}
                            onClick={() => setExpandedPreviewPath(isExpanded ? null : art.path)}
                            title={isExpanded ? 'Collapse preview' : 'Preview'}
                            className="flex-none rounded-sm border border-hairline px-1.5 py-1 text-mute hover:text-primary transition-colors"
                          >
                            <svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2.5" strokeLinecap="round" strokeLinejoin="round">
                              <path d="M1 12s4-8 11-8 11 8 11 8-4 8-11 8-11-8-11-8z" /><circle cx="12" cy="12" r="3" />
                            </svg>
                          </button>
                        )}
                      </div>
                      {isExpanded && canPreview && (
                        <ArtifactPreview
                          artifact={art}
                          sessionName={sessionName}
                          hostId={hostId}
                          onOpenFull={() => {
                            openFilePath(art.path)
                            setArtifactsOpen(false)
                          }}
                        />
                      )}
                    </div>
                  )
                })}
              </div>
            </>
          )}
          {!termConnected && (
            <div className="absolute inset-0 flex items-center justify-center bg-canvas/90 z-10 pointer-events-none rounded-lg">
              <div className="py-4 px-6 rounded-md bg-surface border border-hairline text-ink text-[13px] font-sans font-bold uppercase tracking-widest flex items-center gap-3">
                <span className="inline-block w-2 h-2 rounded-full bg-destructive animate-[pulse_1.5s_ease-in-out_infinite]" />
                Disconnected — Reconnecting
              </div>
            </div>
          )}
        </div>
        </div>
        {showMobileKeyBar && keyBarEnabled && keyBarSlot && createPortal(
          <div className="flex-none pt-1 px-[3px] pb-[3px]">
            <div className="grid grid-cols-8 gap-1.5 p-1.5 bg-surface border border-hairline rounded-md">
              <div className="relative">
                <button
                  type="button"
                  onMouseDown={(e) => e.preventDefault()}
                  onClick={() => setClipboardMenuOpen((open) => !open)}
                  className="w-full h-10 rounded-sm border border-hairline bg-surface-elevated text-mute flex items-center justify-center transition-colors active:bg-surface"
                  title="Clipboard"
                  aria-label="Clipboard"
                >
                  <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
                    <rect x="8" y="2" width="8" height="4" rx="1" /><path d="M16 4h2a2 2 0 0 1 2 2v14a2 2 0 0 1-2 2H6a2 2 0 0 1-2-2V6a2 2 0 0 1 2-2h2" />
                  </svg>
                </button>
                {clipboardMenuOpen && (
                  <div className="absolute bottom-full left-0 mb-2 min-w-[120px] bg-surface-elevated border border-hairline rounded-md flex flex-col overflow-hidden z-50">
                    <button type="button" onMouseDown={(e) => e.preventDefault()} onClick={() => { void handlePaste(); setClipboardMenuOpen(false) }} className="px-4 py-2.5 text-left text-xs font-medium hover:bg-surface transition-colors border-b border-hairline/40">Paste</button>
                    <button type="button" onMouseDown={(e) => e.preventDefault()} onClick={handleChooseFile} className="px-4 py-2.5 text-left text-xs font-medium hover:bg-surface transition-colors border-b border-hairline/40">Paste file…</button>
                    <button type="button" onMouseDown={(e) => e.preventDefault()} onClick={() => { handleCopy(); setClipboardMenuOpen(false) }} className="px-4 py-2.5 text-left text-xs font-medium hover:bg-surface transition-colors">Copy</button>
                  </div>
                )}
              </div>
              <button
                type="button"
                onMouseDown={(e) => e.preventDefault()}
                onClick={toggleCtrlModifier}
                className={cn(
                  "h-10 rounded-sm border flex items-center justify-center text-[11px] font-medium transition-colors",
                  ctrlModifierActive
                    ? "bg-primary text-primary-foreground border-primary"
                    : "border-hairline bg-surface-elevated text-mute active:bg-surface"
                )}
              >
                Ctrl
              </button>
              <button
                type="button"
                onMouseDown={(e) => e.preventDefault()}
                onClick={toggleAltModifier}
                className={cn(
                  "h-10 rounded-sm border flex items-center justify-center text-[11px] font-medium transition-colors",
                  altModifierActive
                    ? "bg-primary text-primary-foreground border-primary"
                    : "border-hairline bg-surface-elevated text-mute active:bg-surface"
                )}
              >
                Alt
              </button>
              <HoldableKey
                label="Esc"
                onPress={() => {
                  clearCtrlModifier()
                  clearAltModifier()
                  sendSequence('\x1b')
                }}
                className="h-10 rounded-sm border border-hairline bg-surface-elevated flex items-center justify-center text-[11px] font-medium text-mute active:bg-surface transition-colors"
              />
              <HoldableKey
                label="Tab"
                onPress={() => {
                  clearCtrlModifier()
                  clearAltModifier()
                  sendSequence('\t')
                }}
                className="h-10 rounded-sm border border-hairline bg-surface-elevated flex items-center justify-center text-[11px] font-medium text-mute active:bg-surface transition-colors"
              />
              <HoldableKey
                label="⌫"
                onPress={() => {
                  clearCtrlModifier()
                  clearAltModifier()
                  sendSequence(new Uint8Array([0x7f]))
                }}
                className="h-10 rounded-sm border border-hairline bg-surface-elevated flex items-center justify-center text-base text-mute active:bg-surface transition-colors"
                title="Backspace"
                aria-label="Backspace"
              />
              <MobileGestureKey
                label="Pg"
                directions={['up', 'down']}
                onTrigger={(direction) => {
                  clearCtrlModifier()
                  clearAltModifier()
                  sendPage(direction)
                }}
                className="h-10 rounded-sm border border-hairline bg-surface-elevated flex items-center justify-center text-[11px] font-medium text-mute active:bg-surface transition-colors"
              />
              <MobileGestureKey
                label={(
                  <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
                    <path d="M12 2v20M2 12h20M5 9l-3 3 3 3M9 5l3-3 3 3M15 19l-3 3-3-3M19 9l3 3-3 3" />
                  </svg>
                )}
                directions={['left', 'right', 'up', 'down']}
                onTrigger={(direction) => {
                  clearCtrlModifier()
                  clearAltModifier()
                  sendArrow(direction)
                }}
                className="h-10 rounded-sm border border-hairline bg-surface-elevated flex items-center justify-center text-mute active:bg-surface transition-colors"
              />
            </div>
          </div>,
          keyBarSlot
        )}
      </div>
      <input
        ref={fileInputRef}
        type="file"
        tabIndex={-1}
        aria-hidden="true"
        className="sr-only"
        onChange={handleFileSelection}
      />
      {capturedText && (
        <div
          className="absolute inset-0 z-30 flex items-center justify-center bg-black/70 p-4"
          onClick={() => setCapturedText(null)}
        >
          <div
            className="flex h-full max-h-[36rem] w-full max-w-2xl flex-col rounded-lg border border-hairline bg-surface overflow-hidden"
            onClick={(e) => e.stopPropagation()}
          >
            <div className="flex items-center justify-between border-b border-hairline px-4 py-3 bg-surface-elevated/30">
              <div className="text-[13px] font-bold uppercase tracking-widest text-ink">Captured Terminal Text</div>
              <button
                type="button"
                onClick={() => setCapturedText(null)}
                className="rounded-sm border border-hairline px-3 py-1 text-xs font-bold uppercase tracking-widest text-mute hover:text-ink hover:bg-surface-elevated transition-all"
              >
                Close
              </button>
            </div>
            <div className="px-4 py-2.5 text-xs font-medium text-mute/60 italic">
              Select the text you want, then use the system copy action.
            </div>
            <textarea
              ref={captureTextareaRef}
              readOnly
              value={capturedText}
              className="min-h-0 flex-1 resize-none bg-canvas px-4 py-3 font-mono text-[13px] text-ink outline-none border-t border-hairline"
              spellCheck={false}
            />
          </div>
        </div>
      )}
      {selectionMenu && (
        <SelectionMenu
          selectionMenu={selectionMenu}
          onClose={() => setSelectionMenu(null)}
          onCopy={() => {
            navigator.clipboard?.writeText(selectionMenu.text)
            termRef.current?.clearSelection()
            setSelectionMenu(null)
          }}
          onOpenFile={() => {
            const path = selectionMenu.text.trim()
            if (!onOpenFile?.(path)) openFilePath(path)
            termRef.current?.clearSelection()
            setSelectionMenu(null)
          }}
          terminalRef={containerRef}
          aria-label="Terminal selection"
        />
      )}

    </div>
  )
}

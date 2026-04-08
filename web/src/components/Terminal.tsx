import { useEffect, useRef, useState, useCallback } from 'react'
import { useTerminal } from '../hooks/useTerminal'

interface TerminalProps {
  sessionName: string
  hostId?: string
  fullscreen?: boolean
  onToggleFullscreen?: () => void
}

type GestureDirection = 'up' | 'down' | 'left' | 'right'

const HOLD_DELAY_MS = 260
const HOLD_REPEAT_MS = 80

function MobileGestureKey({
  label,
  directions,
  onTrigger,
  className = '',
}: {
  label: string
  directions: GestureDirection[]
  onTrigger: (direction: GestureDirection) => void
  className?: string
}) {
  const startRef = useRef<{ x: number; y: number } | null>(null)
  const activeDirectionRef = useRef<GestureDirection | null>(null)
  const holdTimeoutRef = useRef<number | null>(null)
  const repeatTimerRef = useRef<number | null>(null)

  const stopRepeat = () => {
    if (holdTimeoutRef.current !== null) {
      window.clearTimeout(holdTimeoutRef.current)
      holdTimeoutRef.current = null
    }
    if (repeatTimerRef.current !== null) {
      window.clearInterval(repeatTimerRef.current)
      repeatTimerRef.current = null
    }
  }

  const trigger = (direction: GestureDirection, repeat = false) => {
    onTrigger(direction)
    if (!repeat) return
    stopRepeat()
    holdTimeoutRef.current = window.setTimeout(() => {
      repeatTimerRef.current = window.setInterval(() => onTrigger(direction), HOLD_REPEAT_MS)
    }, HOLD_DELAY_MS)
  }

  const resolveDirection = (dx: number, dy: number): GestureDirection | null => {
    if (Math.abs(dx) < 18 && Math.abs(dy) < 18) return null
    if (Math.abs(dx) > Math.abs(dy)) {
      return dx > 0 ? 'right' : 'left'
    }
    return dy > 0 ? 'down' : 'up'
  }

  return (
    <button
      type="button"
      onMouseDown={(e) => e.preventDefault()}
      onClick={(e) => e.preventDefault()}
      onTouchStart={(e) => {
        const touch = e.touches[0]
        startRef.current = { x: touch.clientX, y: touch.clientY }
        activeDirectionRef.current = null
        stopRepeat()
      }}
      onTouchMove={(e) => {
        const start = startRef.current
        if (!start) return
        const touch = e.touches[0]
        const direction = resolveDirection(touch.clientX - start.x, touch.clientY - start.y)
        if (!direction || !directions.includes(direction)) return
        if (activeDirectionRef.current === direction) return
        activeDirectionRef.current = direction
        trigger(direction, true)
      }}
      onTouchEnd={() => {
        stopRepeat()
        startRef.current = null
        activeDirectionRef.current = null
      }}
      onTouchCancel={() => {
        stopRepeat()
        startRef.current = null
        activeDirectionRef.current = null
      }}
      className={`terminal-key ${className}`}
    >
      {label}
    </button>
  )
}

function HoldableKey({
  label,
  onPress,
  className = '',
}: {
  label: string
  onPress: () => void
  className?: string
}) {
  const holdTimeoutRef = useRef<number | null>(null)
  const repeatTimerRef = useRef<number | null>(null)

  const stopRepeat = () => {
    if (holdTimeoutRef.current !== null) {
      window.clearTimeout(holdTimeoutRef.current)
      holdTimeoutRef.current = null
    }
    if (repeatTimerRef.current !== null) {
      window.clearInterval(repeatTimerRef.current)
      repeatTimerRef.current = null
    }
  }

  return (
    <button
      type="button"
      onMouseDown={(e) => e.preventDefault()}
      onTouchStart={() => {
        stopRepeat()
        holdTimeoutRef.current = window.setTimeout(() => {
          repeatTimerRef.current = window.setInterval(onPress, HOLD_REPEAT_MS)
        }, HOLD_DELAY_MS)
      }}
      onTouchEnd={stopRepeat}
      onTouchCancel={stopRepeat}
      onClick={onPress}
      className={`terminal-key ${className}`}
    >
      {label}
    </button>
  )
}

export function Terminal({ sessionName, hostId, fullscreen, onToggleFullscreen }: TerminalProps) {
  const containerRef = useRef<HTMLDivElement>(null)
  const {
    termRef,
    connect,
    disconnect,
    fit,
    focus,
    termConnected,
    sendRawBytes,
    sendText,
    ctrlModifierActive,
    toggleCtrlModifier,
    clearCtrlModifier,
  } = useTerminal(sessionName, hostId)
  const [showMobileKeyBar, setShowMobileKeyBar] = useState(false)
  const [capturedText, setCapturedText] = useState<string | null>(null)
  const [clipboardMenuOpen, setClipboardMenuOpen] = useState(false)
  const captureTextareaRef = useRef<HTMLTextAreaElement>(null)

  useEffect(() => {
    const media = window.matchMedia('(max-width: 900px), (pointer: coarse)')
    const sync = () => setShowMobileKeyBar(media.matches)
    sync()
    media.addEventListener('change', sync)
    return () => media.removeEventListener('change', sync)
  }, [])

  useEffect(() => {
    if (!capturedText || !captureTextareaRef.current) return
    captureTextareaRef.current.focus()
    captureTextareaRef.current.select()
  }, [capturedText])

  useEffect(() => {
    if (showMobileKeyBar) return
    setClipboardMenuOpen(false)
  }, [showMobileKeyBar])

  useEffect(() => {
    if (containerRef.current) {
      connect(containerRef.current)
      requestAnimationFrame(() => focus())
      setTimeout(() => focus(), 100)
    }
    return () => disconnect()
  }, [sessionName])

  // Refocus terminal when WebSocket reconnects (e.g. after iPad sleep)
  useEffect(() => {
    if (termConnected && !document.hidden) {
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
      if (!document.hidden && containerRef.current) {
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
    const observer = new ResizeObserver(() => {
      requestAnimationFrame(() => fit())
    })
    observer.observe(containerRef.current)
    return () => observer.disconnect()
  }, [fit])

  // Touch scroll -> wheel events for tmux mouse mode
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

  const sendSequence = useCallback((sequence: string | Uint8Array) => {
    if (typeof sequence === 'string') {
      sendText(sequence)
      return
    }
    sendRawBytes(sequence)
  }, [sendRawBytes, sendText])

  const sendArrow = useCallback((direction: GestureDirection) => {
    if (direction === 'left') sendSequence('\x1b[D')
    if (direction === 'right') sendSequence('\x1b[C')
    if (direction === 'up') sendSequence('\x1b[A')
    if (direction === 'down') sendSequence('\x1b[B')
  }, [sendSequence])

  const sendPage = useCallback((direction: GestureDirection) => {
    if (direction === 'up') sendSequence('\x1b[5~')
    if (direction === 'down') sendSequence('\x1b[6~')
  }, [sendSequence])

  const handlePaste = useCallback(async () => {
    setClipboardMenuOpen(false)
    try {
      const text = await navigator.clipboard?.readText?.()
      if (text) {
        sendText(text)
      }
    } catch (err) {
      console.error('Failed to paste from clipboard:', err)
    }
  }, [sendText])

  const handleCopy = useCallback(async () => {
    setClipboardMenuOpen(false)
    const term = termRef.current
    if (!term) return

    const lines: string[] = []
    const buffer = term.buffer.active
    const start = buffer.viewportY
    const end = Math.min(start + term.rows, buffer.length)

    for (let i = start; i < end; i++) {
      const line = buffer.getLine(i)
      if (!line) continue
      lines.push(line.translateToString(true))
    }

    const text = lines.join('\n').trim()
    if (!text) return

    term.clearSelection()
    setCapturedText(text)
  }, [termRef])

  return (
    <div className="flex-1 p-1 overflow-hidden relative group">
      <div className="h-full w-full flex flex-col gap-1">
        <div
          className="min-h-0 flex-1 border border-border rounded bg-card relative"
          style={{ boxShadow: 'inset 0 0 20px rgba(102, 179, 255, 0.08)' }}
        >
          <div
            ref={containerRef}
            className="absolute inset-0.5 overflow-hidden rounded-sm"
          />
        {/* Fullscreen toggle */}
          {onToggleFullscreen && (
            <button
              onClick={onToggleFullscreen}
              title={fullscreen ? 'Exit fullscreen (Esc / Cmd+Shift+F)' : 'Fullscreen (Cmd+Shift+F)'}
              className="absolute top-2 right-2 z-20 p-1.5 rounded bg-card border border-border text-muted-foreground hover:text-primary hover:border-primary/40 transition-colors"
            >
              {fullscreen ? (
                <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
                  <polyline points="4 14 10 14 10 20" /><polyline points="20 10 14 10 14 4" /><line x1="14" y1="10" x2="21" y2="3" /><line x1="3" y1="21" x2="10" y2="14" />
                </svg>
              ) : (
                <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
                  <polyline points="15 3 21 3 21 9" /><polyline points="9 21 3 21 3 15" /><line x1="21" y1="3" x2="14" y2="10" /><line x1="3" y1="21" x2="10" y2="14" />
                </svg>
              )}
            </button>
          )}
          {!termConnected && (
            <div className="absolute inset-0 flex items-center justify-center bg-background/85 z-10 pointer-events-none rounded">
              <div className="py-4 px-6 rounded bg-card border border-border text-foreground text-sm font-mono font-bold flex items-center gap-2.5">
                <span className="inline-block w-2 h-2 rounded-full bg-destructive animate-[pulse_1.5s_ease-in-out_infinite]" />
                Disconnected — reconnecting...
              </div>
            </div>
          )}
        </div>
        {showMobileKeyBar && (
          <div className="terminal-keybar-shell">
            <div className="terminal-keybar">
              <button
                type="button"
                onMouseDown={(e) => e.preventDefault()}
                onClick={() => setClipboardMenuOpen((open) => !open)}
                className="terminal-key terminal-key-clipboard"
                title="Clipboard"
                aria-label="Clipboard"
              >
                📋
              </button>
              {clipboardMenuOpen && (
                <div className="terminal-keybar-menu">
                  <button type="button" onClick={handlePaste} className="terminal-keybar-menu-item">Paste</button>
                  <button type="button" onClick={handleCopy} className="terminal-keybar-menu-item">Copy</button>
                </div>
              )}
              <button
                type="button"
                onMouseDown={(e) => e.preventDefault()}
                onClick={toggleCtrlModifier}
                className={`terminal-key ${ctrlModifierActive ? 'terminal-key-active' : ''}`}
              >
                Ctrl
              </button>
              <button
                type="button"
                onMouseDown={(e) => e.preventDefault()}
                onClick={() => {
                  clearCtrlModifier()
                  sendSequence('\x1b')
                }}
                className="terminal-key"
              >
                Esc
              </button>
              <HoldableKey
                label="Bksp"
                onPress={() => {
                  clearCtrlModifier()
                  sendSequence(new Uint8Array([0x7f]))
                }}
              />
              <MobileGestureKey
                label="Pg ↕"
                directions={['up', 'down']}
                onTrigger={(direction) => {
                  clearCtrlModifier()
                  sendPage(direction)
                }}
                className="terminal-key-page"
              />
              <MobileGestureKey
                label="← ↑ ↓ →"
                directions={['left', 'right', 'up', 'down']}
                onTrigger={(direction) => {
                  clearCtrlModifier()
                  sendArrow(direction)
                }}
                className="terminal-key-pad"
              />
            </div>
          </div>
        )}
      </div>
      {capturedText && (
        <div
          className="absolute inset-0 z-30 flex items-center justify-center bg-black/60 p-3"
          onClick={() => setCapturedText(null)}
        >
          <div
            className="flex h-full max-h-[32rem] w-full max-w-2xl flex-col rounded border border-border bg-card shadow-2xl"
            onClick={(e) => e.stopPropagation()}
          >
            <div className="flex items-center justify-between border-b border-border px-3 py-2">
              <div className="text-sm font-mono font-bold text-foreground">Captured Terminal Text</div>
              <button
                type="button"
                onClick={() => setCapturedText(null)}
                className="rounded border border-border px-2 py-1 text-xs text-muted-foreground hover:text-foreground"
              >
                Close
              </button>
            </div>
            <div className="px-3 py-2 text-[11px] text-muted-foreground">
              Select the text you want, then use the system copy action.
            </div>
            <textarea
              ref={captureTextareaRef}
              readOnly
              value={capturedText}
              className="min-h-0 flex-1 resize-none bg-background px-3 py-2 font-mono text-xs text-foreground outline-none"
              spellCheck={false}
            />
          </div>
        </div>
      )}
    </div>
  )
}

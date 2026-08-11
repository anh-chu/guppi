import { useCallback, useEffect, useRef, useState } from 'react'
import type { RefObject } from 'react'
import type { Terminal } from '@xterm/xterm'
import { cn } from '../../lib/utils'

interface ScrollScrubberProps {
  termRef: RefObject<Terminal | null>
  connected: boolean
}

// Touch-friendly scroll scrubber overlaying the terminal's right edge.
// Dragging maps track position linearly onto the full scrollback, so deep
// buffers (50k lines) are reachable in one gesture. Appears only while the
// buffer is scrollable; fades out after scrolling stops unless dragging.
export function ScrollScrubber({ termRef, connected }: ScrollScrubberProps) {
  const trackRef = useRef<HTMLDivElement>(null)
  const [thumb, setThumb] = useState<{ top: number; height: number } | null>(null)
  const [dragging, setDragging] = useState(false)
  const [active, setActive] = useState(false)
  const fadeTimerRef = useRef<number | null>(null)
  const rafRef = useRef<number | null>(null)
  const lastViewportYRef = useRef(-1)

  const update = useCallback(() => {
    const term = termRef.current
    if (!term) { setThumb(null); return }
    const buf = term.buffer.active
    const scrollableRows = buf.length - term.rows
    if (scrollableRows <= 0) { setThumb(null); return }
    const height = Math.max(10, (term.rows / buf.length) * 100)
    const top = (buf.viewportY / scrollableRows) * (100 - height)
    setThumb({ top, height })
    if (buf.viewportY !== lastViewportYRef.current) {
      lastViewportYRef.current = buf.viewportY
      setActive(true)
      if (fadeTimerRef.current !== null) clearTimeout(fadeTimerRef.current)
      fadeTimerRef.current = window.setTimeout(() => setActive(false), 1200)
    }
  }, [termRef])

  useEffect(() => {
    const term = termRef.current
    if (!term) return
    update()
    const schedule = () => {
      if (rafRef.current !== null) return
      rafRef.current = requestAnimationFrame(() => {
        rafRef.current = null
        update()
      })
    }
    const d1 = term.onScroll(schedule)
    const d2 = term.onRender(schedule)
    const d3 = term.onResize(schedule)
    return () => {
      d1.dispose(); d2.dispose(); d3.dispose()
      if (rafRef.current !== null) cancelAnimationFrame(rafRef.current)
      if (fadeTimerRef.current !== null) clearTimeout(fadeTimerRef.current)
    }
  }, [termRef, connected, update])

  const scrollToPointer = useCallback((clientY: number) => {
    const term = termRef.current
    const track = trackRef.current
    if (!term || !track) return
    const rect = track.getBoundingClientRect()
    const fraction = Math.min(1, Math.max(0, (clientY - rect.top) / rect.height))
    const scrollableRows = term.buffer.active.length - term.rows
    if (scrollableRows <= 0) return
    term.scrollToLine(Math.round(fraction * scrollableRows))
  }, [termRef])

  if (!thumb) return null

  return (
    <div
      ref={trackRef}
      className={cn(
        'absolute right-0 top-1 bottom-1 w-7 z-20 flex justify-end pr-0.5 transition-opacity duration-300',
        dragging || active ? 'opacity-100' : 'opacity-0',
      )}
      style={{ touchAction: 'none' }}
      onPointerDown={(e) => {
        e.preventDefault()
        e.currentTarget.setPointerCapture(e.pointerId)
        setDragging(true)
        scrollToPointer(e.clientY)
      }}
      onPointerMove={(e) => {
        if (!e.currentTarget.hasPointerCapture(e.pointerId)) return
        scrollToPointer(e.clientY)
      }}
      onPointerUp={(e) => {
        e.currentTarget.releasePointerCapture(e.pointerId)
        setDragging(false)
      }}
      onPointerCancel={() => setDragging(false)}
      aria-hidden="true"
    >
      <div className="relative h-full w-1.5">
        <div className="absolute inset-0 rounded-full bg-surface-elevated/40" />
        <div
          className={cn(
            'absolute left-0 right-0 rounded-full transition-colors',
            dragging ? 'bg-primary' : 'bg-mute/60',
          )}
          style={{ top: `${thumb.top}%`, height: `${thumb.height}%` }}
        />
      </div>
    </div>
  )
}

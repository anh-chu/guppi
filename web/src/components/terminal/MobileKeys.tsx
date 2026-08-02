import { useRef } from 'react'
import type { ReactNode } from 'react'
import type { GestureDirection } from './useTerminalInput'

const HOLD_DELAY_MS = 260
const HOLD_REPEAT_MS = 80

export function MobileGestureKey({
  label,
  directions,
  onTrigger,
  className = '',
}: {
  label: ReactNode
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
      clearTimeout(holdTimeoutRef.current)
      holdTimeoutRef.current = null
    }
    if (repeatTimerRef.current !== null) {
      clearInterval(repeatTimerRef.current)
      repeatTimerRef.current = null
    }
  }

  const trigger = (direction: GestureDirection, repeat = false) => {
    onTrigger(direction)
    if (!repeat) return
    stopRepeat()
    holdTimeoutRef.current = setTimeout(() => {
      repeatTimerRef.current = setInterval(() => onTrigger(direction), HOLD_REPEAT_MS)
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

export function HoldableKey({
  label,
  onPress,
  className = '',
  title,
  'aria-label': ariaLabel,
}: {
  label: string
  onPress: () => void
  className?: string
  title?: string
  'aria-label'?: string
}) {
  const holdTimeoutRef = useRef<number | null>(null)
  const repeatTimerRef = useRef<number | null>(null)

  const stopRepeat = () => {
    if (holdTimeoutRef.current !== null) {
      clearTimeout(holdTimeoutRef.current)
      holdTimeoutRef.current = null
    }
    if (repeatTimerRef.current !== null) {
      clearInterval(repeatTimerRef.current)
      repeatTimerRef.current = null
    }
  }

  return (
    <button
      type="button"
      onMouseDown={(e) => e.preventDefault()}
      onTouchStart={() => {
        stopRepeat()
        holdTimeoutRef.current = setTimeout(() => {
          repeatTimerRef.current = setInterval(onPress, HOLD_REPEAT_MS)
        }, HOLD_DELAY_MS)
      }}
      onTouchEnd={stopRepeat}
      onTouchCancel={stopRepeat}
      onClick={onPress}
      className={`terminal-key ${className}`}
      title={title}
      aria-label={ariaLabel}
    >
      {label}
    </button>
  )
}

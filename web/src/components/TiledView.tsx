import { useState, useRef, useCallback, useEffect } from 'react'
import { Terminal } from './Terminal'
import { parseSessionKey } from '../hooks/useSessions'
import { cn } from '../lib/utils'

interface TiledViewProps {
  panes: string[]
  activePaneIndex: number
  onActivate: (index: number) => void
  onClose: (index: number) => void
  fullscreen: boolean
  onToggleFullscreen: () => void
  terminalContainerRef?: React.RefObject<HTMLDivElement | null>
}

const MIN_PANE_SIZE = 200 // px

type DividerOrientation = 'vertical' | 'horizontal'

interface DragState {
  orientation: DividerOrientation
  /** Which dimension index this divider controls in the sizes array */
  sizeIndex: number
  /** Pointer start position (clientX for vertical, clientY for horizontal) */
  startPos: number
  /** Snapshot of sizes at drag start */
  startSizes: number[]
  /** Pane count at drag start */
  paneCount: number
}

export function TiledView({
  panes,
  activePaneIndex,
  onActivate,
  onClose,
  fullscreen,
  onToggleFullscreen,
  terminalContainerRef,
}: TiledViewProps) {
  const containerRef = useRef<HTMLDivElement>(null)
  const paneCount = panes.length

  // --------------- sizes state ---------------

  const getDefaultSizes = useCallback((count: number): number[] => {
    switch (count) {
      case 2: return [50]
      case 3: return [33.33, 33.33]
      case 4: return [50, 50, 50]
      default: return []
    }
  }, [])

  const [sizes, setSizes] = useState<number[]>(() => getDefaultSizes(paneCount))

  // Reset sizes when pane count changes (e.g. 2→3→2 etc.)
  useEffect(() => {
    setSizes(getDefaultSizes(paneCount))
  }, [paneCount, getDefaultSizes])

  // --------------- drag handling ---------------

  const dragRef = useRef<DragState | null>(null)
  // Keep a mutable ref to the latest sizes/paneCount so pointer handlers
  // can read them without stale closures
  const liveRef = useRef({ sizes, paneCount, containerRef })
  liveRef.current = { sizes, paneCount, containerRef }

  useEffect(() => {
    const onPointerMove = (e: PointerEvent) => {
      const state = dragRef.current
      if (!state) return

      const { sizes: curSizes, containerRef: cRef, paneCount: pc } = liveRef.current
      const container = cRef.current
      if (!container) return

      const rect = container.getBoundingClientRect()
      const containerSize =
        state.orientation === 'vertical' ? rect.width : rect.height
      if (containerSize <= 0) return

      const currentPos =
        state.orientation === 'vertical' ? e.clientX : e.clientY
      const delta = currentPos - state.startPos
      const deltaPercent = (delta / containerSize) * 100
      const minPercent = (MIN_PANE_SIZE / containerSize) * 100

      const newSizes = [...state.startSizes]
      const idx = state.sizeIndex

      if (state.orientation === 'vertical') {
        if (pc === 2) {
          // One divider: sizes[0] = left width
          newSizes[0] = Math.max(
            minPercent,
            Math.min(100 - minPercent, state.startSizes[0] + deltaPercent),
          )
        } else if (pc === 3) {
          // Two dividers: sizes[0] = col1, sizes[1] = col2, col3 = remaining
          const otherFixed = idx === 0 ? state.startSizes[1] : state.startSizes[0]
          const maxVal = 100 - minPercent - otherFixed
          newSizes[idx] = Math.max(
            minPercent,
            Math.min(maxVal, state.startSizes[idx] + deltaPercent),
          )
        } else if (pc === 4) {
          // sizes[1] = top-left, sizes[2] = bottom-left
          const maxVal = 100 - minPercent
          newSizes[idx] = Math.max(
            minPercent,
            Math.min(maxVal, state.startSizes[idx] + deltaPercent),
          )
        }
      } else {
        // Horizontal divider (only for 4-pane): sizes[0] = top row height %
        newSizes[0] = Math.max(
          minPercent,
          Math.min(100 - minPercent, state.startSizes[0] + deltaPercent),
        )
      }

      setSizes(newSizes)
    }

    const onPointerUp = () => {
      dragRef.current = null
    }

    document.addEventListener('pointermove', onPointerMove)
    document.addEventListener('pointerup', onPointerUp)
    return () => {
      document.removeEventListener('pointermove', onPointerMove)
      document.removeEventListener('pointerup', onPointerUp)
    }
  }, [])

  const onDividerPointerDown = useCallback(
    (orientation: DividerOrientation, sizeIndex: number) =>
      (e: React.PointerEvent<HTMLDivElement>) => {
        e.preventDefault()
        const container = containerRef.current
        if (!container) return

        dragRef.current = {
          orientation,
          sizeIndex,
          startPos:
            orientation === 'vertical' ? e.clientX : e.clientY,
          startSizes: [...liveRef.current.sizes],
          paneCount: liveRef.current.paneCount,
        }
      },
    [],
  )

  // --------------- render helpers ---------------

  const renderPane = (index: number) => {
    const key = panes[index]
    const { host, name } = parseSessionKey(key)
    const isActive = index === activePaneIndex

    return (
      <div
        key={key}
        className={cn(
          'flex-1 flex flex-col overflow-hidden rounded-lg border min-h-0',
          isActive ? 'border-primary' : 'border-hairline',
        )}
        onClick={() => {
          if (index !== activePaneIndex) onActivate(index)
        }}
      >
        {/* Header — only when more than one tile */}
        {paneCount > 1 && (
          <div className="flex items-center justify-between px-2.5 py-1 bg-surface border-b border-hairline rounded-t-lg shrink-0">
            <span className="text-[11px] font-medium text-ink truncate min-w-0 mr-2">
              {name}
            </span>
            <button
              type="button"
              onClick={(e) => {
                e.stopPropagation()
                onClose(index)
              }}
              className="text-mute hover:text-ink p-0.5 rounded shrink-0 hover:bg-surface-elevated transition-colors"
              aria-label="Close pane"
            >
              <svg
                width="12"
                height="12"
                viewBox="0 0 24 24"
                fill="none"
                stroke="currentColor"
                strokeWidth="2"
                strokeLinecap="round"
                strokeLinejoin="round"
              >
                <line x1="18" y1="6" x2="6" y2="18" />
                <line x1="6" y1="6" x2="18" y2="18" />
              </svg>
            </button>
          </div>
        )}
        <div
          ref={isActive ? terminalContainerRef : undefined}
          className="flex-1 flex flex-col overflow-hidden"
        >
          <Terminal
            sessionName={name}
            hostId={host || undefined}
            fullscreen={isActive ? fullscreen : false}
            onToggleFullscreen={isActive ? onToggleFullscreen : undefined}
          />
        </div>
      </div>
    )
  }

  const renderVerticalDivider = (sizeIndex: number) => (
    <div
      key={`vdiv-${sizeIndex}`}
      className="relative shrink-0 bg-hairline hover:bg-primary/40 transition-colors cursor-col-resize"
      style={{ width: 2 }}
      onPointerDown={onDividerPointerDown('vertical', sizeIndex)}
    />
  )

  const renderHorizontalDivider = () => (
    <div
      key="hdiv"
      className="relative shrink-0 bg-hairline hover:bg-primary/40 transition-colors cursor-row-resize"
      style={{ height: 2 }}
      onPointerDown={onDividerPointerDown('horizontal', 0)}
    />
  )

  // --------------- layout ---------------

  if (paneCount === 1) {
    return (
      <div ref={containerRef} className="flex-1 flex flex-col overflow-hidden">
        {renderPane(0)}
      </div>
    )
  }

  if (paneCount === 2) {
    return (
      <div
        ref={containerRef}
        className="flex-1 flex flex-row overflow-hidden gap-0"
      >
        <div
          className="flex flex-col overflow-hidden"
          style={{ flex: `0 0 ${sizes[0]}%` }}
        >
          {renderPane(0)}
        </div>
        {renderVerticalDivider(0)}
        <div className="flex flex-col overflow-hidden min-w-0 flex-1">
          {renderPane(1)}
        </div>
      </div>
    )
  }

  if (paneCount === 3) {
    return (
      <div
        ref={containerRef}
        className="flex-1 flex flex-row overflow-hidden gap-0"
      >
        <div
          className="flex flex-col overflow-hidden"
          style={{ flex: `0 0 ${sizes[0]}%` }}
        >
          {renderPane(0)}
        </div>
        {renderVerticalDivider(0)}
        <div
          className="flex flex-col overflow-hidden"
          style={{ flex: `0 0 ${sizes[1]}%` }}
        >
          {renderPane(1)}
        </div>
        {renderVerticalDivider(1)}
        <div className="flex flex-col overflow-hidden min-w-0 flex-1">
          {renderPane(2)}
        </div>
      </div>
    )
  }

  // paneCount === 4
  return (
    <div
      ref={containerRef}
      className="flex-1 flex flex-col overflow-hidden gap-0"
    >
      {/* Top row */}
      <div
        className="flex flex-row overflow-hidden"
        style={{ flex: `0 0 ${sizes[0]}%` }}
      >
        <div
          className="flex flex-col overflow-hidden"
          style={{ flex: `0 0 ${sizes[1]}%` }}
        >
          {renderPane(0)}
        </div>
        {renderVerticalDivider(1)}
        <div className="flex flex-col overflow-hidden min-w-0 flex-1">
          {renderPane(1)}
        </div>
      </div>
      {renderHorizontalDivider()}
      {/* Bottom row */}
      <div className="flex flex-row overflow-hidden min-w-0 flex-1">
        <div
          className="flex flex-col overflow-hidden"
          style={{ flex: `0 0 ${sizes[2]}%` }}
        >
          {renderPane(2)}
        </div>
        {renderVerticalDivider(2)}
        <div className="flex flex-col overflow-hidden min-w-0 flex-1">
          {renderPane(3)}
        </div>
      </div>
    </div>
  )
}

import { useState, useRef, useCallback, useEffect } from 'react'
import { Terminal } from './Terminal'
import { parseSessionKey } from '../hooks/useSessions'
import { cn } from '../lib/utils'
import { PaneTree, getLeaves } from '../lib/paneTree'

interface TiledViewProps {
  tree: PaneTree | null
  activeKey: string | null
  onActivate: (key: string) => void
  onClose: (key: string) => void
  onPopOut: (key: string) => void
  onSplit: (key: string, direction: 'h' | 'v') => void
  onRatioChange: (path: string, ratio: number) => void
  fullscreen: boolean
  onToggleFullscreen: () => void
  terminalContainerRef?: React.RefObject<HTMLDivElement | null>
  onDropSession?: (key: string) => void
}

const MIN_PANE_SIZE = 200 // px

export function TiledView({
  tree,
  activeKey,
  onActivate,
  onClose,
  onPopOut,
  onSplit,
  onRatioChange,
  fullscreen,
  onToggleFullscreen,
  terminalContainerRef,
  onDropSession,
}: TiledViewProps) {
  const containerRef = useRef<HTMLDivElement>(null)
  const [dragOver, setDragOver] = useState(false)

  const totalLeaves = tree ? getLeaves(tree).length : 0

  // --------------- divider drag handling ---------------

  const dragRef = useRef<{
    path: string
    direction: 'h' | 'v'
    startPos: number
    startRatio: number
  } | null>(null)

  const onRatioChangeRef = useRef(onRatioChange)
  onRatioChangeRef.current = onRatioChange

  useEffect(() => {
    const onPointerMove = (e: PointerEvent) => {
      const state = dragRef.current
      if (!state) return
      const container = containerRef.current
      if (!container) return

      const rect = container.getBoundingClientRect()
      const containerSize =
        state.direction === 'h' ? rect.width : rect.height
      if (containerSize <= 0) return

      const currentPos =
        state.direction === 'h' ? e.clientX : e.clientY
      const delta = currentPos - state.startPos
      const deltaRatio = delta / containerSize
      const minPercent = MIN_PANE_SIZE / containerSize

      let newRatio = state.startRatio + deltaRatio
      newRatio = Math.max(minPercent, Math.min(1 - minPercent, newRatio))

      onRatioChangeRef.current(state.path, newRatio)
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

  const handleDividerPointerDown = useCallback(
    (
      path: string,
      direction: 'h' | 'v',
      currentRatio: number,
      e: React.PointerEvent<HTMLDivElement>,
    ) => {
      e.preventDefault()
      const startPos = direction === 'h' ? e.clientX : e.clientY
      dragRef.current = { path, direction, startPos, startRatio: currentRatio }
    },
    [],
  )

  // --------------- drop handling ---------------

  const handleDragOver = useCallback((e: React.DragEvent) => {
    e.preventDefault()
    e.stopPropagation()
    setDragOver(true)
  }, [])

  const handleDragLeave = useCallback((e: React.DragEvent) => {
    e.preventDefault()
    e.stopPropagation()
    setDragOver(false)
  }, [])

  const handleDrop = useCallback(
    (e: React.DragEvent) => {
      e.preventDefault()
      e.stopPropagation()
      setDragOver(false)
      const sessKey = e.dataTransfer.getData('text/plain')
      if (sessKey) {
        onDropSession?.(sessKey)
      }
    },
    [onDropSession],
  )

  const dropOverlay = dragOver ? (
    <div className="absolute inset-0 z-10 bg-primary/10 border-2 border-dashed border-primary rounded-lg flex items-center justify-center pointer-events-none">
      <span className="text-sm font-medium text-primary">Drop to split</span>
    </div>
  ) : null

  // --------------- render pane ---------------

  const renderPane = (sessionKey: string) => {
    const { host, name } = parseSessionKey(sessionKey)
    const isActive = sessionKey === activeKey

    return (
      <div
        key={sessionKey}
        className={cn(
          'flex-1 flex flex-col overflow-hidden rounded-lg border min-h-0',
          isActive ? 'border-primary' : 'border-hairline',
        )}
        onClick={() => {
          if (sessionKey !== activeKey) onActivate(sessionKey)
        }}
      >
        {/* Header — only when more than one leaf */}
        {totalLeaves > 1 && (
          <div className="flex items-center justify-between px-2.5 py-1 bg-surface border-b border-hairline rounded-t-lg shrink-0">
            <span className="text-[11px] font-medium text-ink truncate min-w-0 mr-2">
              {name}
            </span>
            <div className="flex items-center gap-1">
              {/* Split horizontal */}
              <button
                type="button"
                onClick={(e) => {
                  e.stopPropagation()
                  onSplit(sessionKey, 'h')
                }}
                className="text-mute hover:text-ink p-0.5 rounded shrink-0 hover:bg-surface-elevated transition-colors"
                aria-label="Split pane horizontally"
                title="Split horizontally"
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
                  <rect x="3" y="3" width="7" height="18" rx="1" />
                  <rect x="14" y="3" width="7" height="18" rx="1" />
                </svg>
              </button>
              {/* Split vertical */}
              <button
                type="button"
                onClick={(e) => {
                  e.stopPropagation()
                  onSplit(sessionKey, 'v')
                }}
                className="text-mute hover:text-ink p-0.5 rounded shrink-0 hover:bg-surface-elevated transition-colors"
                aria-label="Split pane vertically"
                title="Split vertically"
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
                  <rect x="3" y="3" width="18" height="7" rx="1" />
                  <rect x="3" y="14" width="18" height="7" rx="1" />
                </svg>
              </button>
              {/* Pop out */}
              <button
                type="button"
                onClick={(e) => {
                  e.stopPropagation()
                  onPopOut(sessionKey)
                }}
                className="text-mute hover:text-ink p-0.5 rounded shrink-0 hover:bg-surface-elevated transition-colors"
                aria-label="Pop out pane"
                title="Pop out"
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
                  <polyline points="15 3 21 3 21 9" />
                  <polyline points="9 21 3 21 3 15" />
                  <line x1="21" y1="3" x2="14" y2="10" />
                  <line x1="3" y1="21" x2="10" y2="14" />
                </svg>
              </button>
              {/* Close */}
              <button
                type="button"
                onClick={(e) => {
                  e.stopPropagation()
                  onClose(sessionKey)
                }}
                className="text-mute hover:text-ink p-0.5 rounded shrink-0 hover:bg-surface-elevated transition-colors"
                aria-label="Close pane"
                title="Close"
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

  // --------------- recursive render ---------------

  const renderNode = (node: PaneTree, path: string): React.ReactNode => {
    if (node.type === 'leaf') {
      return renderPane(node.sessionKey)
    }

    // Split node
    const isH = node.direction === 'h'
    const isV = node.direction === 'v'

    const dividerProps: React.HTMLAttributes<HTMLDivElement> & {
      style: React.CSSProperties
    } = {
      className:
        'relative shrink-0 bg-hairline hover:bg-primary/40 transition-colors',
      style: isH
        ? { width: 2, cursor: 'col-resize' }
        : { height: 2, cursor: 'row-resize' },
      onPointerDown: (e: React.PointerEvent<HTMLDivElement>) =>
        handleDividerPointerDown(path, node.direction, node.ratio, e),
    }

    return (
      <div
        className={`flex-1 flex overflow-hidden ${isH ? 'flex-row' : 'flex-col'}`}
      >
        <div
          className="flex flex-col overflow-hidden"
          style={{ flex: `0 0 ${node.ratio * 100}%` }}
        >
          {renderNode(node.first, path ? `${path}/0` : '0')}
        </div>
        <div {...dividerProps} />
        <div className="flex flex-col overflow-hidden min-w-0 flex-1">
          {renderNode(node.second, path ? `${path}/1` : '1')}
        </div>
      </div>
    )
  }

  // --------------- main render ---------------

  return (
    <div
      ref={containerRef}
      className="flex-1 flex flex-col overflow-hidden relative"
      onDragOver={handleDragOver}
      onDragLeave={handleDragLeave}
      onDrop={handleDrop}
    >
      {tree ? renderNode(tree, '') : null}
      {dropOverlay}
    </div>
  )
}

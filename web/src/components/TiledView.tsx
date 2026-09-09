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
  onKill?: (key: string) => void
  onPopOut: (key: string) => void
  onSplit: (key: string, direction: 'h' | 'v') => void
  onRatioChange: (path: string, ratio: number) => void
  fullscreen: boolean
  onToggleFullscreen: () => void
  terminalContainerRef?: React.RefObject<HTMLDivElement | null>
  onDropSession?: (sessKey: string, targetKey: string, edge: 'left'|'right'|'top'|'bottom'|'center') => void
  onDropNewSession?: (targetKey: string, edge: 'left'|'right'|'top'|'bottom'|'center') => void
  onSwapPanes?: (keyA: string, keyB: string) => void
  onMovePanes?: (sourceKey: string, targetKey: string, edge: 'left'|'right'|'top'|'bottom') => void
  getBackend?: (key: string) => string | undefined
  getCwd?: (key: string) => string | undefined
  getName?: (key: string) => string | undefined
  onOpenFile?: (path: string, cwd?: string, hostId?: string, sessionName?: string) => boolean
  composeTarget?: { key: string; nonce: number } | null
  // Phone mode: collapse a group's split layout to one pane at a time with a
  // tab strip to switch, since tiling is unusable on a phone-sized screen.
  phoneSingle?: boolean
}

const MIN_PANE_SIZE = 200 // px

export function TiledView({
  tree,
  activeKey,
  onActivate,
  onClose,
  onKill,
  onPopOut,
  onSplit,
  onRatioChange,
  fullscreen,
  onToggleFullscreen,
  terminalContainerRef,
  onDropSession,
  onDropNewSession,
  onSwapPanes,
  onMovePanes,
  getBackend,
  getCwd,
  getName,
  onOpenFile,
  composeTarget,
  phoneSingle,
}: TiledViewProps) {
  const containerRef = useRef<HTMLDivElement>(null)
  const [dragOver, setDragOver] = useState(false)
  const [dragType, setDragType] = useState<'pane' | 'new-session' | 'sidebar' | null>(null)
  const [dropTarget, setDropTarget] = useState<{ key: string; zone: 'left'|'right'|'top'|'bottom'|'center' } | null>(null)

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
    // Ignore pane swap drags
    if (e.dataTransfer.types.includes('application/x-termyard-pane')) return
    e.preventDefault()
    e.stopPropagation()
    setDragOver(true)
  }, [])

  const handleDragLeave = useCallback((e: React.DragEvent) => {
    // Ignore pane swap drags
    if (e.dataTransfer.types.includes('application/x-termyard-pane')) return
    // Only clear when leaving the container itself, not moving into a child
    if (e.currentTarget.contains(e.relatedTarget as Node)) return
    e.preventDefault()
    setDragOver(false)
    setDragType(null)
  }, [])

  const handleDrop = useCallback(
    (e: React.DragEvent) => {
      e.preventDefault()
      e.stopPropagation()
      setDragOver(false)
      if (e.dataTransfer.types.includes('application/x-termyard-new-session')) {
        onDropNewSession?.(activeKey ?? '', 'center')
        return
      }
      // Only handle sidebar drops (text/plain), not pane swaps
      if (e.dataTransfer.types.includes('application/x-termyard-pane')) return
      const sessKey = e.dataTransfer.getData('text/plain')
      if (sessKey) {
        onDropSession?.(sessKey, activeKey ?? '', 'center')
      }
    },
    [onDropSession, onDropNewSession, activeKey],
  )

  // Safety net: clear overlay if drag is cancelled (Escape) or ends outside
  useEffect(() => {
    const onDragEnd = () => { setDragOver(false); setDropTarget(null); setDragType(null) }
    document.addEventListener('dragend', onDragEnd)
    return () => document.removeEventListener('dragend', onDragEnd)
  }, [])

  const dropOverlay = dragOver ? (
    <div className="absolute inset-0 z-10 bg-primary/10 border-2 border-dashed border-primary rounded-lg flex items-center justify-center pointer-events-none">
      <span className="text-sm font-medium text-primary">Drop to split</span>
    </div>
  ) : null

  // --------------- render pane ---------------

  const renderPane = (sessionKey: string, path: string) => {
    const { host, name } = parseSessionKey(sessionKey)
    const isActive = sessionKey === activeKey
    const isDropTarget = dropTarget?.key === sessionKey

    return (
      <div
        key={path}
        className={cn(
          'flex-1 flex flex-col overflow-hidden min-h-0 relative',
        )}
        data-pane-key={sessionKey}
        onClick={() => {
          if (sessionKey !== activeKey) onActivate(sessionKey)
        }}
        onDragOver={(e) => {
          const dt = e.dataTransfer
          const getZone = (): 'left'|'right'|'top'|'bottom'|'center' => {
            const rect = e.currentTarget.getBoundingClientRect()
            const x = e.clientX - rect.left
            const y = e.clientY - rect.top
            const w = rect.width
            const h = rect.height
            if (x < w * 0.25) return 'left'
            if (x > w * 0.75) return 'right'
            if (y < h * 0.25) return 'top'
            if (y > h * 0.75) return 'bottom'
            return 'center'
          }
          if (dt.types.includes('application/x-termyard-new-session')) {
            e.preventDefault()
            setDragType('new-session')
            setDropTarget({ key: sessionKey, zone: getZone() })
            return
          }
          if (dt.types.includes('text/plain') && !dt.types.includes('application/x-termyard-pane')) {
            e.preventDefault()
            setDragType('sidebar')
            setDropTarget({ key: sessionKey, zone: getZone() })
            return
          }
          if (totalLeaves > 1 && dt.types.includes('application/x-termyard-pane')) {
            const droppedKey = dt.getData('application/x-termyard-pane')
            if (droppedKey !== sessionKey) {
              e.preventDefault()
              setDragType('pane')
              setDropTarget({ key: sessionKey, zone: getZone() })
            }
          }
        }}
        onDragLeave={(e) => {
          if (e.currentTarget === e.target || !e.currentTarget.contains(e.relatedTarget as Node)) {
            setDropTarget(null)
            setDragType(null)
          }
        }}
        onDrop={(e) => {
          e.preventDefault()
          e.stopPropagation()
          const currentDropTarget = dropTarget
          setDropTarget(null)
          if (e.dataTransfer.types.includes('application/x-termyard-new-session')) {
            const zone = currentDropTarget?.key === sessionKey ? currentDropTarget.zone : 'center'
            onDropNewSession?.(sessionKey, zone)
            return
          }
          // Pane-to-pane swap/move
          const paneKey = e.dataTransfer.getData('application/x-termyard-pane')
          if (paneKey && paneKey !== sessionKey && totalLeaves > 1 && currentDropTarget?.key === sessionKey) {
            if (currentDropTarget.zone === 'center') {
              onSwapPanes?.(paneKey, sessionKey)
            } else {
              onMovePanes?.(paneKey, sessionKey, currentDropTarget.zone)
            }
            return
          }
          // Sidebar session drop
          setDragOver(false)
          const sidebarKey = e.dataTransfer.getData('text/plain')
          if (sidebarKey) {
            const zone = currentDropTarget?.key === sessionKey ? currentDropTarget.zone : 'center'
            onDropSession?.(sidebarKey, sessionKey, zone)
          }
        }}
      >
        {/* Drop zone overlay */}
        {isDropTarget && (
          <div className="absolute inset-0 z-10 pointer-events-none">
            {/* Edge strip */}
            <div className={cn(
              'absolute bg-primary',
              dropTarget!.zone === 'left' && 'left-0 top-0 bottom-0 w-1',
              dropTarget!.zone === 'right' && 'right-0 top-0 bottom-0 w-1',
              dropTarget!.zone === 'top' && 'top-0 left-0 right-0 h-1',
              dropTarget!.zone === 'bottom' && 'bottom-0 left-0 right-0 h-1',
            )} />
            {/* Overlay area */}
            {dropTarget!.zone === 'center' ? (
              <div className="absolute inset-0 bg-primary/10 border-2 border-dashed border-primary rounded-lg flex items-center justify-center">
                <span className="text-sm font-medium text-primary">
                  {dragType === 'pane' ? '⇄ Swap' : '+ Split'}
                </span>
              </div>
            ) : (
              <div className={cn(
                'absolute bg-primary/10',
                dropTarget!.zone === 'left' && 'left-0 top-0 bottom-0 w-1/2',
                dropTarget!.zone === 'right' && 'right-0 top-0 bottom-0 w-1/2',
                dropTarget!.zone === 'top' && 'top-0 left-0 right-0 h-1/2',
                dropTarget!.zone === 'bottom' && 'bottom-0 left-0 right-0 h-1/2',
              )} />
            )}
          </div>
        )}
        <div
          ref={isActive ? terminalContainerRef : undefined}
          className="flex-1 flex flex-col overflow-hidden"
        >
          <Terminal
            sessionName={name}
            hostId={host || undefined}
            backend={getBackend?.(sessionKey)}
            fullscreen={isActive ? fullscreen : false}
            onOpenFile={(path) => onOpenFile?.(path, getCwd?.(sessionKey), host || undefined, name) ?? false}
            onToggleFullscreen={isActive ? onToggleFullscreen : undefined}
            keyBarEnabled={isActive}
            composeTarget={composeTarget}
            currentKey={sessionKey}
            displayName={getName?.(sessionKey) ?? name}
            cwd={getCwd?.(sessionKey)}
            onSplit={(dir) => onSplit(sessionKey, dir)}
            onClose={() => onClose(sessionKey)}
            onKill={() => onKill?.(sessionKey)}
            onPopOut={() => onPopOut(sessionKey)}
            draggableHeader={totalLeaves > 1}
            onHeaderDragStart={(e) => {
              if ((e.target as HTMLElement).closest('button')) { e.preventDefault(); return }
              e.dataTransfer.setData('application/x-termyard-pane', sessionKey)
              e.dataTransfer.effectAllowed = 'move'
            }}
          />
        </div>
      </div>
    )
  }

  // --------------- recursive render ---------------

  const renderNode = (node: PaneTree, path: string): React.ReactNode => {
    if (node.type === 'leaf') {
      return renderPane(node.sessionKey, path || '0')
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
        ? { width: 2, cursor: 'col-resize', zIndex: 1 }
        : { height: 2, cursor: 'row-resize', zIndex: 1 },
      onPointerDown: (e: React.PointerEvent<HTMLDivElement>) =>
        handleDividerPointerDown(path, node.direction, node.ratio, e),
      children: (
        <div style={isH
          ? { position: 'absolute', top: 0, bottom: 0, left: -4, right: -4, cursor: 'col-resize' }
          : { position: 'absolute', left: 0, right: 0, top: -4, bottom: -4, cursor: 'row-resize' }
        } />
      ),
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

  // --------------- phone single-in-view ---------------

  // On phones, a group shows one pane at a time. All terminals stay mounted
  // (hidden via visibility, so xterm keeps its measured size) and a tab strip
  // switches the visible one.
  const renderPhoneSingle = (t: PaneTree) => {
    const leaves = getLeaves(t)
    const active = activeKey && leaves.includes(activeKey) ? activeKey : leaves[0]
    return (
      <div className="flex-1 flex flex-col overflow-hidden">
        <div className="flex-none flex overflow-x-auto bg-surface border-b border-hairline">
          {leaves.map((key) => {
            const { name } = parseSessionKey(key)
            const isActive = key === active
            return (
              <button
                key={key}
                type="button"
                onClick={() => { if (key !== active) onActivate(key) }}
                className={cn(
                  'px-3 py-2 text-[12px] whitespace-nowrap border-r border-hairline transition-colors',
                  isActive ? 'bg-surface-elevated text-ink font-medium' : 'text-mute active:bg-surface-elevated',
                )}
              >
                {name}
              </button>
            )
          })}
        </div>
        <div className="flex-1 relative overflow-hidden">
          {leaves.map((key) => {
            const { host, name } = parseSessionKey(key)
            const isActive = key === active
            return (
              <div
                key={key}
                ref={isActive ? terminalContainerRef : undefined}
                className="absolute inset-0 flex flex-col overflow-hidden"
                style={{ visibility: isActive ? 'visible' : 'hidden', zIndex: isActive ? 1 : 0 }}
              >
                <Terminal
                  sessionName={name}
                  hostId={host || undefined}
                  backend={getBackend?.(key)}
                  fullscreen={isActive ? fullscreen : false}
                  onOpenFile={(path) => onOpenFile?.(path, getCwd?.(key), host || undefined, name) ?? false}
                  onToggleFullscreen={isActive ? onToggleFullscreen : undefined}
                  keyBarEnabled={isActive}
                  composeTarget={composeTarget}
                  currentKey={key}
                  displayName={getName?.(key) ?? name}
                  cwd={getCwd?.(key)}
                  onSplit={(dir) => onSplit(key, dir)}
                  onClose={() => onClose(key)}
                  onKill={() => onKill?.(key)}
                  onPopOut={() => onPopOut(key)}
                />
              </div>
            )
          })}
        </div>
      </div>
    )
  }

  // --------------- main render ---------------

  if (phoneSingle && tree && totalLeaves > 1) {
    return (
      <div ref={containerRef} className="flex-1 flex flex-col overflow-hidden relative">
        {renderPhoneSingle(tree)}
      </div>
    )
  }

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

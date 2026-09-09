import { useEffect, useRef, useState } from 'react'
import { cn } from '../lib/utils'
import { pathLeaf } from '../lib/path'

// One shared terminal-view header, used by both the single/focused view and
// each tiled pane. Two variants: a top `bar` on desktop, and an overlay `pill`
// on coarse-pointer (mobile) to preserve vertical space. Presentational only —
// all state/handlers are owned by Terminal/App/TiledView.
export interface TerminalHeaderProps {
  name: string
  cwd?: string
  variant: 'bar' | 'pill'
  fullscreen?: boolean
  artifactsCount: number
  onCompose: () => void
  onToggleArtifacts: () => void
  onSplit?: (dir: 'h' | 'v') => void
  onPopOut: () => void
  onToggleFullscreen?: () => void
  onClose?: () => void
  onKill?: () => void
  draggable?: boolean
  onHeaderDragStart?: (e: React.DragEvent) => void
}

const btn = 'text-mute hover:text-ink p-1 rounded-sm hover:bg-surface-elevated transition-colors shrink-0 flex items-center'

export function TerminalHeader({
  name, cwd, variant, fullscreen, artifactsCount,
  onCompose, onToggleArtifacts, onSplit, onPopOut, onToggleFullscreen, onClose, onKill,
  draggable, onHeaderDragStart,
}: TerminalHeaderProps) {
  const [killArmed, setKillArmed] = useState(false)
  const killTimerRef = useRef<number | undefined>(undefined)
  const disarm = () => {
    if (killTimerRef.current) { window.clearTimeout(killTimerRef.current); killTimerRef.current = undefined }
    setKillArmed(false)
  }
  useEffect(() => () => { if (killTimerRef.current) window.clearTimeout(killTimerRef.current) }, [])
  const handleKill = () => {
    if (killArmed) { disarm(); onKill?.(); return }
    setKillArmed(true)
    if (killTimerRef.current) window.clearTimeout(killTimerRef.current)
    killTimerRef.current = window.setTimeout(() => setKillArmed(false), 2500)
  }

  const cwdLeaf = cwd ? pathLeaf(cwd) : ''

  const controls = (
    <div className="flex items-center gap-1 shrink-0 ml-auto">
      <button type="button" onClick={(e) => { e.stopPropagation(); onCompose() }} title="Compose Input (Cmd/Ctrl+Shift+U)" className={btn}>
        <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2.5" strokeLinecap="round" strokeLinejoin="round">
          <rect x="2" y="6" width="20" height="12" rx="2" /><path d="M6 10h.01M10 10h.01M14 10h.01M18 10h.01M6 14h.01M18 14h.01M9 14h6" />
        </svg>
      </button>
      {artifactsCount > 0 && (
        <button type="button" onClick={(e) => { e.stopPropagation(); onToggleArtifacts() }} title="Detected files" className={cn(btn, 'gap-1')}>
          <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2.5" strokeLinecap="round" strokeLinejoin="round">
            <path d="M13 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V9z" /><polyline points="13 2 13 9 20 9" />
          </svg>
          <span className="text-[10px] font-bold">{artifactsCount}</span>
        </button>
      )}
      {onSplit && (
        <>
          <button type="button" onClick={(e) => { e.stopPropagation(); onSplit('h') }} title="Split horizontally" aria-label="Split pane horizontally" className={btn}>
            <svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
              <rect x="3" y="3" width="7" height="18" rx="1" /><rect x="14" y="3" width="7" height="18" rx="1" />
            </svg>
          </button>
          <button type="button" onClick={(e) => { e.stopPropagation(); onSplit('v') }} title="Split vertically" aria-label="Split pane vertically" className={btn}>
            <svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
              <rect x="3" y="3" width="18" height="7" rx="1" /><rect x="3" y="14" width="18" height="7" rx="1" />
            </svg>
          </button>
        </>
      )}
      <button type="button" onClick={(e) => { e.stopPropagation(); onPopOut() }} title="Pop out to floating window" aria-label="Pop out pane" className={btn}>
        <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2.5" strokeLinecap="round" strokeLinejoin="round">
          <rect x="2" y="4" width="20" height="16" rx="2" /><rect x="12" y="11" width="8" height="6" rx="1" fill="currentColor" />
        </svg>
      </button>
      {onToggleFullscreen && (
        <button type="button" onClick={(e) => { e.stopPropagation(); onToggleFullscreen() }} title={fullscreen ? 'Exit fullscreen (Esc / Cmd+Shift+F)' : 'Fullscreen (Cmd+Shift+F)'} className={btn}>
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
      {onClose && (
        <button type="button" onClick={(e) => { e.stopPropagation(); onClose() }} title="Close" aria-label="Close" className={btn}>
          <svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
            <line x1="18" y1="6" x2="6" y2="18" /><line x1="6" y1="6" x2="18" y2="18" />
          </svg>
        </button>
      )}
      {onKill && (
        <button
          type="button"
          onClick={(e) => { e.stopPropagation(); handleKill() }}
          title={killArmed ? 'Click again to confirm' : 'Kill session'}
          aria-label="Kill session"
          className={cn(
            'shrink-0 rounded-sm transition-colors text-[11px] font-medium leading-none flex items-center',
            killArmed ? 'px-1.5 py-1 bg-red-500/20 text-red-400 hover:bg-red-500/30' : 'p-1 text-mute hover:text-red-400 hover:bg-surface-elevated',
          )}
        >
          {killArmed ? <>Kill?&nbsp;<span className="text-[10px]">✕</span></> : (
            <svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
              <polyline points="3 6 5 6 21 6" /><path d="M19 6v14a2 2 0 0 1-2 2H7a2 2 0 0 1-2-2V6m3 0V4a2 2 0 0 1 2-2h4a2 2 0 0 1 2 2v2" />
            </svg>
          )}
        </button>
      )}
    </div>
  )

  if (variant === 'pill') {
    return (
      <div
        onMouseLeave={disarm}
        className="absolute top-2.5 right-2.5 z-20 flex items-center gap-0.5 rounded-md bg-surface/40 backdrop-blur-md border border-hairline/60 p-0.5 transition-opacity opacity-60 group-hover:opacity-100 focus-within:opacity-100"
      >
        {controls}
      </div>
    )
  }

  return (
    <div
      onMouseLeave={disarm}
      draggable={draggable}
      onDragStart={onHeaderDragStart}
      className={cn(
        'flex items-center gap-2 px-2.5 h-7 bg-surface border-b border-hairline shrink-0',
        draggable && 'cursor-grab active:cursor-grabbing',
      )}
    >
      <span className="flex items-baseline gap-1 min-w-0 select-none">
        {cwdLeaf && (
          <>
            <span className="text-[11px] text-mute/60 truncate max-w-[40%]">{cwdLeaf}</span>
            <span className="text-[11px] text-mute/30">/</span>
          </>
        )}
        <span className="text-[11px] font-medium text-ink truncate">{name}</span>
      </span>
      {controls}
    </div>
  )
}

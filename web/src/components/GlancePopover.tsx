import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from 'react'
import { createPortal } from 'react-dom'

// Read-only terminal snapshot shown on hover. Fetches once from
// /api/pane-capture (last 40 lines), no polling. Pointer-leave dismisses.

export interface GlanceTarget {
  name: string
  host?: string
  display_name?: string
  host_name?: string
}

const ENTER_DELAY_MS = 400

function GlancePopover({ anchor, target, hasMultipleHosts }: { anchor: HTMLElement; target: GlanceTarget; hasMultipleHosts: boolean }) {
  const ref = useRef<HTMLDivElement>(null)
  const bodyRef = useRef<HTMLDivElement>(null)
  const [status, setStatus] = useState<'loading' | 'loaded' | 'empty' | 'error'>('loading')
  const [text, setText] = useState('')

  // One fetch per mount (a fresh hover = fresh mount = fresh capture).
  useEffect(() => {
    const ac = new AbortController()
    const host = target.host ? `&host=${encodeURIComponent(target.host)}` : ''
    const url = `/api/pane-capture?session=${encodeURIComponent(target.name)}&lines=40${host}`
    setStatus('loading')
    fetch(url, { signal: ac.signal })
      .then(res => { if (!res.ok) throw new Error(`HTTP ${res.status}`); return res.json() })
      .then((data: { text?: string }) => {
        const t = (data.text || '').replace(/\s+$/, '')
        setText(t)
        setStatus(t ? 'loaded' : 'empty')
      })
      .catch((err) => { if (err?.name !== 'AbortError') setStatus('error') })
    return () => ac.abort()
  }, [target.name, target.host])

  // Position near anchor: prefer right, flip left, clamp to viewport (8px pad).
  // Mirrors the Sidebar context-menu clamp.
  useLayoutEffect(() => {
    const el = ref.current
    if (!el) return
    const pad = 8, gap = 8
    const a = anchor.getBoundingClientRect()
    const rect = el.getBoundingClientRect()
    let left = a.right + gap
    if (left + rect.width + pad > window.innerWidth) left = a.left - gap - rect.width
    if (left + rect.width + pad > window.innerWidth) left = window.innerWidth - rect.width - pad
    left = Math.max(pad, left)
    let top = a.top
    if (top + rect.height + pad > window.innerHeight) top = window.innerHeight - rect.height - pad
    top = Math.max(pad, top)
    el.style.left = `${left}px`
    el.style.top = `${top}px`
  }, [anchor, status])

  // Show the freshest lines (tail) once loaded.
  useEffect(() => {
    if (status === 'loaded' && bodyRef.current) bodyRef.current.scrollTop = bodyRef.current.scrollHeight
  }, [status])

  return createPortal(
    <div
      ref={ref}
      aria-hidden
      className="fixed z-50 w-[440px] max-h-[320px] flex flex-col rounded-sm border border-hairline bg-surface-elevated shadow-[0_8px_24px_rgba(0,0,0,0.45)] overflow-hidden pointer-events-none"
    >
      <div className="flex items-center gap-2 px-3 py-1.5 border-b border-hairline bg-surface-elevated/40 shrink-0">
        <span className="font-sans text-[12px] font-bold text-ink truncate">{target.display_name || target.name}</span>
        {hasMultipleHosts && <span className="text-[10px] text-mute/60 shrink-0">{target.host_name || 'Local'}</span>}
      </div>
      <div ref={bodyRef} className="min-h-0 flex-1 overflow-y-auto">
        {status === 'loading' && (
          <div className="flex items-center justify-center gap-2 px-3 py-6 text-mute/60">
            <span className="inline-block w-1.5 h-1.5 rounded-full bg-mute/60 animate-[pulse_1.5s_ease-in-out_infinite]" />
            <span className="text-[11px] font-bold uppercase tracking-widest">Loading…</span>
          </div>
        )}
        {status === 'empty' && <div className="px-3 py-6 text-center text-[12px] text-mute/60 italic">No output captured.</div>}
        {status === 'error' && <div className="px-3 py-6 text-center text-[12px] text-destructive">Couldn’t load preview.</div>}
        {status === 'loaded' && <pre className="m-0 px-3 py-2 font-mono text-[11px] leading-[1.45] text-body-text whitespace-pre overflow-x-auto">{text}</pre>}
      </div>
    </div>,
    document.body,
  )
}

// Shared hover-glance controller. Spread trigger(target) on a row/card; render
// `popover` once in the same component.
export function useGlance(hasMultipleHosts: boolean) {
  const [active, setActive] = useState<{ anchor: HTMLElement; target: GlanceTarget } | null>(null)
  const timerRef = useRef<number | null>(null)
  const canHover = useMemo(() => typeof window !== 'undefined' && window.matchMedia('(hover: hover) and (pointer: fine)').matches, [])

  const clearTimer = () => { if (timerRef.current !== null) { window.clearTimeout(timerRef.current); timerRef.current = null } }

  const hide = useCallback(() => { clearTimer(); setActive(null) }, [])

  const trigger = useCallback((target: GlanceTarget) => {
    if (!canHover) return {}
    return {
      onPointerEnter: (e: React.PointerEvent) => {
        const anchor = e.currentTarget as HTMLElement
        clearTimer()
        timerRef.current = window.setTimeout(() => setActive({ anchor, target }), ENTER_DELAY_MS)
      },
      onPointerLeave: hide,
    }
  }, [canHover, hide])

  useEffect(() => () => clearTimer(), [])

  const popover = active ? <GlancePopover anchor={active.anchor} target={active.target} hasMultipleHosts={hasMultipleHosts} /> : null
  return { trigger, popover }
}

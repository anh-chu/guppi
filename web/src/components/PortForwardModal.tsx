import { useState, useRef, useEffect } from 'react'
import { usePortForwards, ForwardMode } from '../hooks/usePortForwards'
import { cn } from '../lib/utils'

interface Props {
  onClose: () => void
}

const modeLabels: Record<ForwardMode, { short: string; desc: string; color: string }> = {
  proxy: {
    short: 'HTTP proxy',
    desc: 'Route HTTP/WebSocket through guppi — same port, same auth, browser link.',
    color: 'text-primary bg-primary/10 border-primary/30',
  },
  socat: {
    short: 'TCP rebind',
    desc: 'Bind the port on 0.0.0.0 via socat — works for any protocol, no auth layer.',
    color: 'text-amber-400 bg-amber-400/10 border-amber-400/30',
  },
}

export function PortForwardModal({ onClose }: Props) {
  const { forwards, add, remove } = usePortForwards()
  const [port, setPort] = useState('')
  const [label, setLabel] = useState('')
  const [mode, setMode] = useState<ForwardMode>('proxy')
  const [externalPort, setExternalPort] = useState('')
  const [error, setError] = useState<string | null>(null)
  const [adding, setAdding] = useState(false)
  const portRef = useRef<HTMLInputElement>(null)

  // Keep external port in sync with port unless user has overridden it
  const [externalPortTouched, setExternalPortTouched] = useState(false)
  useEffect(() => {
    if (!externalPortTouched) setExternalPort(port)
  }, [port, externalPortTouched])

  // Reset external port touched state when mode changes away from socat
  useEffect(() => {
    if (mode !== 'socat') setExternalPortTouched(false)
  }, [mode])

  useEffect(() => { portRef.current?.focus() }, [])

  // Close on Escape
  useEffect(() => {
    const handler = (e: KeyboardEvent) => { if (e.key === 'Escape') onClose() }
    window.addEventListener('keydown', handler)
    return () => window.removeEventListener('keydown', handler)
  }, [onClose])

  const submit = async () => {
    const portNum = parseInt(port, 10)
    if (!portNum || portNum < 1 || portNum > 65535) {
      setError('Port must be 1–65535')
      return
    }
    const extPortNum = mode === 'socat' ? parseInt(externalPort, 10) || portNum : 0
    if (mode === 'socat' && (extPortNum < 1 || extPortNum > 65535)) {
      setError('External port must be 1–65535')
      return
    }
    setAdding(true)
    setError(null)
    const err = await add(portNum, label.trim(), mode, mode === 'socat' ? extPortNum : undefined)
    setAdding(false)
    if (err) {
      setError(err)
      return
    }
    setPort('')
    setLabel('')
    setExternalPort('')
    setExternalPortTouched(false)
  }

  return (
    <div
      className="fixed inset-0 z-50 flex items-center justify-center bg-black/60 backdrop-blur-sm"
      onClick={(e) => { if (e.target === e.currentTarget) onClose() }}
    >
      <div className="w-full max-w-lg bg-canvas border border-hairline rounded-xl shadow-2xl overflow-hidden">
        {/* Header */}
        <div className="flex items-center justify-between px-5 py-4 border-b border-hairline">
          <div>
            <h2 className="text-sm font-bold text-ink tracking-tight">Port Forwards</h2>
            <p className="text-xs text-mute mt-0.5">Expose localhost-bound services through guppi</p>
          </div>
          <button
            onClick={onClose}
            className="w-7 h-7 flex items-center justify-center rounded-md text-mute hover:text-ink hover:bg-surface-elevated transition-colors text-lg leading-none"
          >
            ×
          </button>
        </div>

        {/* Active forwards */}
        <div className="px-5 py-3">
          {forwards.length === 0 ? (
            <p className="text-xs text-mute/60 text-center py-4">No active port forwards</p>
          ) : (
            <ul className="space-y-1.5">
              {forwards.map(f => {
                const meta = modeLabels[f.mode] ?? modeLabels.proxy
                const isProxy = f.mode === 'proxy'
                return (
                  <li
                    key={f.port}
                    className="group flex items-center gap-3 px-3 py-2.5 rounded-lg bg-surface border border-hairline"
                  >
                    {/* Port + label */}
                    <div className="flex-1 min-w-0 flex items-center gap-2">
                      <span className="font-mono text-sm font-bold text-ink shrink-0">{f.port}</span>
                      {f.label && (
                        <span className="text-xs text-mute truncate">{f.label}</span>
                      )}
                    </div>

                    {/* Mode badge */}
                    <span className={cn(
                      'shrink-0 text-[10px] font-semibold px-1.5 py-0.5 rounded-md border',
                      meta.color,
                    )}>
                      {meta.short}
                    </span>

                    {/* Open link — proxy only */}
                    {isProxy && (
                      <a
                        href={`/proxy/${f.port}/`}
                        target="_blank"
                        rel="noopener noreferrer"
                        title={`Open /proxy/${f.port}/`}
                        className="shrink-0 flex items-center gap-1 text-[11px] text-mute hover:text-primary transition-colors"
                      >
                        <svg width="12" height="12" viewBox="0 0 12 12" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round">
                          <path d="M5 2H2a1 1 0 0 0-1 1v7a1 1 0 0 0 1 1h7a1 1 0 0 0 1-1V7" />
                          <path d="M8 1h3v3M11 1 6 6" />
                        </svg>
                        Open
                      </a>
                    )}
                    {!isProxy && (
                      <span
                        className="shrink-0 text-[11px] text-mute/50 font-mono"
                        title="Direct TCP — connect with any client"
                      >
                        {f.external_port && f.external_port !== f.port
                          ? `0.0.0.0:${f.external_port} → :${f.port}`
                          : `0.0.0.0:${f.port}`}
                      </span>
                    )}

                    {/* Remove */}
                    <button
                      type="button"
                      onClick={() => remove(f.port)}
                      title="Remove forward"
                      className="shrink-0 opacity-0 group-hover:opacity-100 w-6 h-6 flex items-center justify-center rounded-md text-mute hover:text-red-400 hover:bg-red-400/10 transition-all"
                    >
                      <svg width="12" height="12" viewBox="0 0 12 12" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round">
                        <path d="M2 2l8 8M10 2l-8 8" />
                      </svg>
                    </button>
                  </li>
                )
              })}
            </ul>
          )}
        </div>

        {/* Add form */}
        <div className="px-5 pb-5 border-t border-hairline pt-4 space-y-3">
          <p className="text-[10px] uppercase tracking-widest font-semibold text-mute/60">Add forward</p>

          {/* Mode selector */}
          <div className="flex gap-2">
            {(['proxy', 'socat'] as ForwardMode[]).map(m => {
              const meta = modeLabels[m]
              const active = mode === m
              return (
                <button
                  key={m}
                  type="button"
                  onClick={() => setMode(m)}
                  className={cn(
                    'flex-1 px-3 py-2.5 rounded-lg border text-left transition-all',
                    active
                      ? 'border-primary bg-primary/8 text-ink'
                      : 'border-hairline bg-surface text-mute hover:border-primary/40 hover:text-ink',
                  )}
                >
                  <div className="text-xs font-semibold">{meta.short}</div>
                  <div className="text-[10px] mt-0.5 leading-relaxed opacity-70">{meta.desc}</div>
                </button>
              )
            })}
          </div>

          {/* Port + label row */}
          <div className="flex gap-2">
            <input
              ref={portRef}
              type="number"
              min={1}
              max={65535}
              placeholder="Port"
              value={port}
              onChange={e => setPort(e.target.value)}
              onKeyDown={e => { if (e.key === 'Enter') submit() }}
              className="w-24 shrink-0 rounded-lg border border-hairline bg-surface-elevated text-sm text-ink px-3 py-2 outline-none focus:border-primary font-mono placeholder:text-mute/40"
            />
            {mode === 'socat' && (
              <input
                type="number"
                min={1}
                max={65535}
                placeholder="Ext. port"
                title="Port socat will bind on 0.0.0.0 — must differ from the service port"
                value={externalPort}
                onChange={e => { setExternalPort(e.target.value); setExternalPortTouched(true) }}
                onKeyDown={e => { if (e.key === 'Enter') submit() }}
                className="w-28 shrink-0 rounded-lg border border-hairline bg-surface-elevated text-sm text-ink px-3 py-2 outline-none focus:border-primary font-mono placeholder:text-mute/40"
              />
            )}
            <input
              type="text"
              placeholder="Label (optional)"
              value={label}
              onChange={e => setLabel(e.target.value)}
              onKeyDown={e => { if (e.key === 'Enter') submit() }}
              className="flex-1 min-w-0 rounded-lg border border-hairline bg-surface-elevated text-sm text-ink px-3 py-2 outline-none focus:border-primary placeholder:text-mute/40"
            />
            <button
              type="button"
              onClick={submit}
              disabled={adding}
              className="shrink-0 px-4 py-2 rounded-lg bg-primary text-canvas text-sm font-semibold hover:opacity-90 transition-opacity disabled:opacity-50"
            >
              {adding ? '…' : 'Forward'}
            </button>
          </div>

          {error && (
            <p className="text-xs text-red-400 bg-red-400/8 border border-red-400/20 rounded-lg px-3 py-2">
              {error}
            </p>
          )}
        </div>
      </div>
    </div>
  )
}

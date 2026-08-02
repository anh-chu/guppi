import { useState, useEffect, useCallback } from 'react'
import { cn } from '../../lib/utils'
import { ConnectPeerModal } from '../ConnectPeerModal'

export type PeerSnapshot = {
  public_key: string
  fingerprint: string
  name: string
  address: string
  enabled: boolean
  status: 'idle' | 'dialing' | 'connected' | 'backoff' | 'listener'
  last_error?: string
  next_retry?: string
  last_seen?: string
  paired_at: string
  is_dialer: boolean
}

export type PeersResponse = {
  self: { name: string; fingerprint: string; public_key: string }
  peers: PeerSnapshot[]
}

function statusDot(status: PeerSnapshot['status']) {
  switch (status) {
    case 'connected': return { glyph: '●', color: 'text-emerald-400' }
    case 'dialing':   return { glyph: '○', color: 'text-amber-400' }
    case 'backoff':   return { glyph: '○', color: 'text-amber-400' }
    case 'idle':      return { glyph: '◌', color: 'text-mute/60' }
    case 'listener':  return { glyph: '▶', color: 'text-mute/60' }
    default:          return { glyph: '◌', color: 'text-mute/60' }
  }
}

function statusLine(p: PeerSnapshot): string {
  switch (p.status) {
    case 'connected':
      return 'connected'
    case 'dialing':
      return 'dialing…'
    case 'backoff': {
      if (p.next_retry) {
        const ms = new Date(p.next_retry).getTime() - Date.now()
        const s = Math.max(0, Math.round(ms / 1000))
        const lastSeen = p.last_seen ? ` · last seen ${formatAgo(p.last_seen)}` : ''
        return `offline · retrying in ${s}s${lastSeen}${p.last_error ? ` · ${p.last_error}` : ''}`
      }
      return p.last_error || 'offline'
    }
    case 'idle':
      return 'disabled'
    case 'listener':
      return 'listener (remote dials us)'
  }
}

function formatAgo(iso: string): string {
  const ms = Date.now() - new Date(iso).getTime()
  if (ms < 0) return 'now'
  const s = Math.round(ms / 1000)
  if (s < 60) return `${s}s ago`
  const m = Math.round(s / 60)
  if (m < 60) return `${m}m ago`
  const h = Math.round(m / 60)
  return `${h}h ago`
}

export function PeersSection() {
  const [data, setData] = useState<PeersResponse | null>(null)
  const [modalOpen, setModalOpen] = useState(false)
  const [busy, setBusy] = useState<string | null>(null)

  const refresh = useCallback(async () => {
    try {
      const res = await fetch('/api/peers')
      if (res.ok) setData(await res.json())
    } catch {}
  }, [])

  useEffect(() => {
    refresh()
    const t = setInterval(refresh, 5000)
    return () => clearInterval(t)
  }, [refresh])

  const setEnabled = async (fp: string, enabled: boolean) => {
    setBusy(fp)
    try {
      await fetch(`/api/peers/${encodeURIComponent(fp)}`, {
        method: 'PATCH',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ enabled }),
      })
      await refresh()
    } finally { setBusy(null) }
  }

  const reconnect = async (fp: string) => {
    setBusy(fp)
    try {
      await fetch(`/api/peers/${encodeURIComponent(fp)}/reconnect`, { method: 'POST' })
      await refresh()
    } finally { setBusy(null) }
  }

  const forget = async (fp: string, name: string) => {
    if (!confirm(`Forget ${name}? This closes the link and removes the peer.`)) return
    setBusy(fp)
    try {
      await fetch(`/api/peers/${encodeURIComponent(fp)}`, { method: 'DELETE' })
      await refresh()
    } finally { setBusy(null) }
  }

  return (
    <>
      {modalOpen && (
        <ConnectPeerModal
          onClose={() => setModalOpen(false)}
          onConnected={refresh}
        />
      )}
      <div className="flex flex-col gap-4">
        {data?.self && (
          <div className="text-xs font-medium text-mute/70 flex flex-col gap-0.5">
            <div><span className="text-mute/40 uppercase tracking-widest mr-2">This machine</span> {data.self.name}</div>
            <div className="text-mute/40">fingerprint <span className="font-mono text-mute/70">{data.self.fingerprint}</span></div>
          </div>
        )}
        <button
          onClick={() => setModalOpen(true)}
          className="self-start px-4 py-2 rounded-md text-xs font-bold uppercase tracking-widest border border-hairline bg-surface text-ink hover:bg-surface-elevated transition-all"
        >
          Connect to another machine
        </button>
        {data?.peers && data.peers.length > 0 ? (
          <div className="flex flex-col gap-2">
            {data.peers.map(p => {
              const dot = statusDot(p.status)
              return (
                <div key={p.fingerprint} className="rounded-md border border-hairline bg-surface p-3 flex flex-col gap-2">
                  <div className="flex items-baseline justify-between gap-3">
                    <div className="flex items-baseline gap-2 min-w-0">
                      <span className={cn('text-base leading-none', dot.color)}>{dot.glyph}</span>
                      <span className="text-[13px] font-bold text-ink truncate">{p.name || p.fingerprint}</span>
                      <span className="text-xs font-mono text-mute/60 truncate">{p.address || '—'}</span>
                    </div>
                    <span className="text-xs font-mono text-mute/40 shrink-0">{p.fingerprint}</span>
                  </div>
                  <div className="text-xs text-mute/70">{statusLine(p)}</div>
                  <div className="flex items-center gap-3 flex-wrap">
                    <label className="flex items-center gap-2 text-xs text-mute/70 cursor-pointer">
                      <input
                        type="checkbox"
                        checked={p.enabled}
                        disabled={busy === p.fingerprint}
                        onChange={(e) => setEnabled(p.fingerprint, e.target.checked)}
                        className="w-3.5 h-3.5 accent-primary"
                      />
                      Auto-reconnect
                    </label>
                    {p.is_dialer && p.enabled && (
                      <button
                        onClick={() => reconnect(p.fingerprint)}
                        disabled={busy === p.fingerprint}
                        className="text-xs font-bold uppercase tracking-widest text-mute/70 hover:text-ink transition-colors disabled:opacity-50"
                      >
                        Reconnect now
                      </button>
                    )}
                    <button
                      onClick={() => forget(p.fingerprint, p.name || p.fingerprint)}
                      disabled={busy === p.fingerprint}
                      className="text-xs font-bold uppercase tracking-widest text-mute/40 hover:text-destructive transition-colors ml-auto disabled:opacity-50"
                    >
                      Forget
                    </button>
                  </div>
                </div>
              )
            })}
          </div>
        ) : (
          <p className="text-xs text-mute/60 italic">No connected machines yet.</p>
        )}
      </div>
    </>
  )
}

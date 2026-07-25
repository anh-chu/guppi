import { useState, useEffect, useCallback } from 'react'
import { usePreferences, type Preferences } from '../hooks/usePreferences'
import type { UpdateStatus } from '../hooks/useSelfUpdate'
import { usePushNotifications } from '../hooks/usePushNotifications'
import { themePresets, applyTheme } from '../theme'
import { cn } from '../lib/utils'
import { AgentStatusList, SetupCommandBox } from './Setup'
import { ConnectPeerModal } from './ConnectPeerModal'
import { getShortcuts } from '../lib/shortcuts'

type PeerSnapshot = {
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

type PeersResponse = {
  self: { name: string; fingerprint: string; public_key: string }
  peers: PeerSnapshot[]
}

const terminalFontFamilies = [
  'Space Mono',
  'JetBrains Mono',
  'Fira Code',
  'Menlo',
  'Monaco',
  'Consolas',
  'Courier New',
  'Inconsolata LGC Nerd Font Mono',
  'monospace',
]


const notifStatuses = [
  { value: 'waiting', label: 'Waiting' },
  { value: 'stuck', label: 'Stuck' },
  { value: 'error', label: 'Error' },
  { value: 'completed', label: 'Completed' },
]

const sectionIds = ['appearance', 'terminal', 'interface', 'naming', 'shortcuts', 'notifications', 'agents', 'peers', 'integrations', 'security'] as const

type SectionId = (typeof sectionIds)[number]

const bucketSections: Record<'look' | 'yard' | 'alerts' | 'network', readonly SectionId[]> = {
  look: ['appearance', 'terminal'],
  yard: ['interface', 'naming', 'shortcuts'],
  alerts: ['notifications', 'agents'],
  network: ['peers', 'integrations', 'security'],
}

function Section({ id, title, description, children, hidden }: { id: string; title: string; description?: string; children: React.ReactNode; hidden?: boolean }) {
  return (
    <section id={id} className={cn('rounded-lg border border-hairline bg-surface p-6 scroll-mt-6', hidden && 'hidden')}>
      <h3 className="font-display text-[13px] font-bold text-ink mb-1">{title}</h3>
      {description && <p className="text-xs font-medium text-mute/60 mb-5">{description}</p>}
      {!description && <div className="mb-5" />}
      <div className="flex flex-col gap-4">
        {children}
      </div>
    </section>
  )
}

function Divider() {
  return <div className="border-t border-hairline/40 -mx-6 my-1" />
}

function Row({ label, description, children }: { label: string; description?: string; children: React.ReactNode }) {
  return (
    <div className="flex flex-wrap items-center justify-between gap-x-6 gap-y-2 py-1">
      <div className="flex-1 min-w-[140px]">
        <div className="text-[13px] font-semibold text-ink tracking-tight">{label}</div>
        {description && <div className="text-xs font-medium text-mute/50 mt-1">{description}</div>}
      </div>
      <div className="shrink-0">
        {children}
      </div>
    </div>
  )
}

function SelectInput({ value, onChange, options }: { value: string; onChange: (v: string) => void; options: { value: string; label: string }[] }) {
  return (
    <select
      value={value}
      onChange={(e) => onChange(e.target.value)}
      className="bg-surface-elevated border border-hairline rounded-sm px-3 py-1.5 text-[13px] font-medium text-ink outline-none focus:border-primary/60 min-w-[180px] transition-colors cursor-pointer"
    >
      {options.map(o => (
        <option key={o.value} value={o.value}>{o.label}</option>
      ))}
    </select>
  )
}

function NumberInput({ value, onChange, min, max, step }: { value: number; onChange: (v: number) => void; min?: number; max?: number; step?: number }) {
  return (
    <input
      type="number"
      value={value}
      onChange={(e) => onChange(Number(e.target.value))}
      min={min}
      max={max}
      step={step}
      className="bg-surface-elevated border border-hairline rounded-sm px-3 py-1.5 text-[13px] font-medium text-ink outline-none focus:border-primary/60 w-[80px] text-right"
    />
  )
}

function TextInput({ value, onChange, placeholder, type = 'text', wide }: { value: string; onChange: (v: string) => void; placeholder?: string; type?: string; wide?: boolean }) {
  return (
    <input
      type={type}
      value={value}
      onChange={(e) => onChange(e.target.value)}
      placeholder={placeholder}
      autoComplete="off"
      spellCheck={false}
      className={cn(
        'bg-surface-elevated border border-hairline rounded-sm px-3 py-1.5 text-[13px] font-medium text-ink outline-none focus:border-primary/60 transition-colors',
        wide ? 'min-w-[280px]' : 'min-w-[180px]',
      )}
    />
  )
}

function Toggle({ checked, onChange, label }: { checked: boolean; onChange: (v: boolean) => void; label?: string }) {
  return (
    <div className="flex items-center gap-3">
      <button
        type="button"
        role="switch"
        aria-checked={checked}
        onClick={() => onChange(!checked)}
        className={cn(
          'inline-flex h-5 w-9 shrink-0 items-center rounded-full border border-transparent transition-all duration-200',
          checked ? 'bg-primary' : 'bg-surface-elevated border-hairline',
        )}
      >
        <span
          className={cn(
            'pointer-events-none block h-3.5 w-3.5 rounded-full transition-transform duration-200 mx-0.5',
            checked ? 'translate-x-4 bg-primary-foreground' : 'translate-x-0 bg-muted-foreground/60',
          )}
        />
      </button>
      {label && <span className="text-xs font-bold uppercase tracking-wider text-mute/60">{label}</span>}
    </div>
  )
}

function Kbd({ children }: { children: string }) {
  return (
    <kbd className="inline-flex items-center justify-center min-w-[28px] h-6 px-1.5 rounded-xs border border-hairline bg-gradient-to-b from-[#121212] to-[#0d0d0d] text-mute text-xs font-mono font-bold">
      {children}
    </kbd>
  )
}


const sectionLabels: Record<typeof sectionIds[number], string> = {
  appearance: 'Appearance',
  terminal: 'Terminal',
  interface: 'Interface',
  naming: 'AI Naming',
  shortcuts: 'Shortcuts',
  notifications: 'Notifications',
  agents: 'Agents',
  peers: 'Machines',
  integrations: 'Integrations',
  security: 'Security',
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

function PeersSection() {
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

function WikiViewerSection() {
  const { prefs, updatePrefs } = usePreferences()
  const [url, setUrl] = useState(prefs.wiki_viewer_url ?? 'http://localhost:3000')
  const [apiKey, setApiKey] = useState(prefs.wiki_api_key ?? '')
  const [saved, setSaved] = useState(false)

  const save = async () => {
    await updatePrefs({
      wiki_viewer_url: url.trim() || 'http://localhost:3000',
      wiki_api_key: apiKey.trim(),
    })
    setSaved(true)
    setTimeout(() => setSaved(false), 2000)
  }

  return (
    <div className="flex flex-col gap-4">
      <Row
        label="wiki-viewer URL"
        description="URL of a running wiki-viewer instance."
      >
        <div className="flex items-center gap-2">
          <input
            type="url"
            value={url}
            onChange={e => setUrl(e.target.value)}
            placeholder="http://localhost:3000"
            className="flex-1 rounded-md border border-hairline bg-canvas px-3 py-1.5 text-[13px] font-mono text-ink focus:outline-none focus:ring-1 focus:ring-primary"
          />
        </div>
      </Row>
      <Row
        label="API Key"
        description="Paste the key shown in wiki-viewer Settings → API Access."
      >
        <div className="flex items-center gap-2">
          <input
            type="password"
            value={apiKey}
            onChange={e => setApiKey(e.target.value)}
            placeholder="Paste API key…"
            autoComplete="off"
            className="flex-1 rounded-md border border-hairline bg-canvas px-3 py-1.5 text-[13px] font-mono text-ink focus:outline-none focus:ring-1 focus:ring-primary"
          />
          <button
            onClick={save}
            className="px-3 py-1.5 rounded-md bg-primary text-white text-[12px] font-medium hover:bg-primary/90 transition-colors"
          >
            {saved ? 'Saved!' : 'Save'}
          </button>
        </div>
      </Row>
      <p className="text-[12px] text-mute">
        When configured, clicking a file path in terminal output opens it in a side panel powered by wiki-viewer.
        The wiki-viewer root directory should cover the files your agents work with.
      </p>
      <div className="rounded-md bg-surface border border-hairline px-3 py-2.5 text-[11px] font-mono text-mute flex flex-col gap-1">
        <span className="text-[10px] uppercase tracking-widest font-bold text-mute/60 font-sans">Launch command</span>
        <span>npx wiki-viewer</span>
      </div>
      <p className="text-[11px] text-mute/60">
        Generate and manage your API key in wiki-viewer Settings → API Access.
      </p>
    </div>
  )
}


export function Settings({ pushState, onPushSubscribe, onPushUnsubscribe, onLogout, bucket, version, updateAvailable, binaryUpdate, onApplyUpdate, updateApplying, updateRestartMode, updateError, updateChecking, onCheckUpdate }: {
  pushState: string
  onPushSubscribe: () => void
  onPushUnsubscribe: () => void
  onLogout?: () => void
  bucket?: 'look' | 'yard' | 'alerts' | 'network'
  version?: string | null
  updateAvailable?: boolean
  binaryUpdate?: UpdateStatus | null
  onApplyUpdate?: () => Promise<void>
  updateApplying?: boolean
  updateRestartMode?: 'auto' | 'manual' | null
  updateError?: string | null
  updateChecking?: boolean
  onCheckUpdate?: () => void
}) {
  const { prefs, updatePrefs } = usePreferences()
  const [saving, setSaving] = useState(false)
  const [agentStatus, setAgentStatus] = useState<{ agents: { name: string; key: string; installed: boolean; configured: boolean }[]; setup_command: string } | null>(null)
  const [agentLoading, setAgentLoading] = useState(false)

  const fetchAgentStatus = useCallback(async () => {
    setAgentLoading(true)
    try {
      const res = await fetch('/api/agent-status')
      if (res.ok) setAgentStatus(await res.json())
    } catch {}
    setAgentLoading(false)
  }, [])

  useEffect(() => {
    fetchAgentStatus()
  }, [fetchAgentStatus])

  const update = async (partial: Partial<Preferences>) => {
    setSaving(true)
    await updatePrefs(partial)
    setSaving(false)
  }

  const updateNested = async <K extends keyof Preferences>(
    key: K,
    nested: Partial<Preferences[K]>,
  ) => {
    const current = prefs[key]
    await update({ [key]: { ...(typeof current === 'object' ? current : {}), ...nested } } as Partial<Preferences>)
  }

  const handleThemeChange = async (theme: string) => {
    applyTheme(theme)
    await update({ theme })
  }



  const toggleNotifStatus = async (status: string) => {
    const current = prefs.notifications.statuses
    const next = current.includes(status)
      ? current.filter(s => s !== status)
      : [...current, status]
    await updateNested('notifications', { statuses: next })
  }

  const bucketVisible: readonly SectionId[] = bucket ? bucketSections[bucket] : sectionIds
  const visibleSections: readonly SectionId[] = bucket ? bucketVisible : (onLogout ? sectionIds : sectionIds.filter(s => s !== 'security'))
  const showSection = (id: SectionId) => bucketVisible.includes(id) && (onLogout ? true : id !== 'security')

  return (
    <div className={cn('flex-1 overflow-y-auto font-sans text-[13px] font-medium bg-canvas scroll-smooth', bucket ? 'px-5 pb-5 pt-5 sm:pt-12' : 'p-10')}>
      <div className={cn(bucket ? 'max-w-full' : 'max-w-2xl mx-auto')}>
        <div className="flex items-center justify-between mb-8">
          <h2 className="font-display text-xl font-bold text-ink">Settings</h2>
          {saving && <span className="text-xs font-bold text-primary animate-pulse">SAVING...</span>}
        </div>

        {/* Jump nav */}
        {!bucket && (
          <nav className="flex gap-1.5 mb-8 flex-wrap">
            {visibleSections.map(id => (
              <a
                key={id}
                href={`#${id}`}
                className="px-3 py-1.5 rounded-sm text-xs font-bold text-mute/60 hover:text-ink hover:bg-surface-elevated transition-all"
              >
                {sectionLabels[id]}
              </a>
            ))}
          </nav>
        )}

        <div className="flex flex-col gap-6">
          {/* ── Appearance ── */}
          <Section hidden={!showSection('appearance')} id="appearance" title="Appearance" description="Theme and fonts">
            <div className="grid grid-cols-2 gap-3">
              {Object.values(themePresets).map(theme => (
                <button
                  key={theme.name}
                  onClick={() => handleThemeChange(theme.name)}
                  className={cn(
                    'p-4 rounded-lg border text-left transition-all duration-200 group',
                    prefs.theme === theme.name
                      ? 'border-primary bg-primary/5 shadow-[0_0_15px_rgba(255,255,255,0.05)]'
                      : 'border-hairline bg-surface hover:border-hairline/60',
                  )}
                >
                  <div className="flex items-center gap-2 mb-3">
                    <div className="w-4.5 h-4.5 rounded-full border border-hairline/40" style={{ background: theme.xterm.background }} />
                    <div className="w-4.5 h-4.5 rounded-full border border-hairline/40" style={{ background: theme.xterm.foreground }} />
                    <div className="w-4.5 h-4.5 rounded-full border border-hairline/40" style={{ background: theme.xterm.blue }} />
                    <div className="w-4.5 h-4.5 rounded-full border border-hairline/40" style={{ background: theme.xterm.green }} />
                  </div>
                  <div className={cn(
                    'text-[13px] font-bold tracking-tight',
                    prefs.theme === theme.name ? 'text-primary' : 'text-ink/80'
                  )}>{theme.label}</div>
                </button>
              ))}
            </div>

            <Divider />


          </Section>

          {/* ── Terminal ── */}
          <Section hidden={!showSection('terminal')} id="terminal" title="Terminal" description="Font, scrollback, and fullscreen behavior">
            <Row label="Font Family" description="Monospace font for the terminal">
              <SelectInput
                value={prefs.terminal.font_family}
                onChange={(v) => updateNested('terminal', { font_family: v })}
                options={terminalFontFamilies.map(f => ({ value: f, label: f }))}
              />
            </Row>
            <Row label="Font Size" description="Terminal text size in pixels">
              <NumberInput
                value={prefs.terminal.font_size}
                onChange={(v) => updateNested('terminal', { font_size: Math.max(8, Math.min(32, v)) })}
                min={8}
                max={32}
              />
            </Row>
            <Row label="Scrollback" description="Number of lines to keep in history">
              <NumberInput
                value={prefs.terminal.scrollback}
                onChange={(v) => updateNested('terminal', { scrollback: Math.max(100, Math.min(100000, v)) })}
                min={100}
                max={100000}
                step={500}
              />
            </Row>
            <Divider />
            <Row label="Renderer" description="Terminal renderer backend (WebGL uses GPU acceleration)">
              <SelectInput
                value={prefs.terminal.renderer || 'dom'}
                onChange={(v) => updateNested('terminal', { renderer: v })}
                options={[
                  { value: 'dom', label: 'DOM (default)' },
                  { value: 'webgl', label: 'WebGL' },
                ]}
              />
            </Row>
            <Row label="Unicode Graphemes" description="Experimental: proper rendering of ZWJ emoji, CJK, and combining marks">
              <Toggle
                checked={prefs.terminal.unicode_graphemes}
                onChange={(v) => updateNested('terminal', { unicode_graphemes: v })}
                label={prefs.terminal.unicode_graphemes ? 'EXPERIMENTAL · ON' : 'EXPERIMENTAL · OFF'}
              />
            </Row>
            <Row label="Predictive Echo" description="Experimental: show keystrokes immediately while awaiting server confirmation. Only safe printable ASCII in normal mode.">
              <Toggle
                checked={prefs.terminal.predictive_echo}
                onChange={(v) => updateNested('terminal', { predictive_echo: v })}
                label={prefs.terminal.predictive_echo ? 'EXPERIMENTAL · ON' : 'EXPERIMENTAL · OFF'}
              />
            </Row>
            <Divider />
            <Row label="Hide Alerts in Fullscreen" description="Hide the agent alert banner when terminal is fullscreen">
              <Toggle
                checked={prefs.fullscreen_hide_alerts}
                onChange={(v) => update({ fullscreen_hide_alerts: v })}
              />
            </Row>
          </Section>

          {/* ── Interface ── */}
          <Section hidden={!showSection('interface')} id="interface" title="Interface" description="Layout, sidebar, and keyboard shortcuts">
            <Row label="Default View" description="View shown on launch">
              <SelectInput
                value={prefs.default_view}
                onChange={(v) => update({ default_view: v })}
                options={[
                  { value: 'overview', label: 'Overview' },
                  { value: 'last-session', label: 'Last Session' },
                ]}
              />
            </Row>
            <Row label="Sidebar on Launch" description="Start collapsed or expanded">
              <Toggle
                checked={prefs.sidebar.default_collapsed}
                onChange={(v) => updateNested('sidebar', { default_collapsed: v })}
                label={prefs.sidebar.default_collapsed ? 'COLLAPSED' : 'EXPANDED'}
              />
            </Row>
            <Row label="Collapsed Style" description="Narrow column or fully hidden">
              <SelectInput
                value={prefs.sidebar.collapse_mode || 'small'}
                onChange={(v) => updateNested('sidebar', { collapse_mode: v })}
                options={[
                  { value: 'small', label: 'Narrow column' },
                  { value: 'hidden', label: 'Completely hidden' },
                ]}
              />
            </Row>
          </Section>

          {/* ── AI Naming ── */}
          <Section hidden={!showSection('naming')} id="naming" title="AI Session Naming" description="Auto-generate friendly session names from context via an OpenAI-compatible endpoint. Manually renamed sessions are never overwritten.">
            <Row label="Enable" description="Synthesize names from prompt, workdir, branch, agent, and shell activity">
              <Toggle
                checked={prefs.ai_naming.enabled}
                onChange={(v) => updateNested('ai_naming', { enabled: v })}
                label={prefs.ai_naming.enabled ? 'ON' : 'OFF'}
              />
            </Row>
            {prefs.ai_naming.enabled && (
              <>
                <Divider />
                <Row label="Endpoint" description="Base URL, e.g. https://api.openai.com/v1 (falls back to TERMYARD_NAMER_ENDPOINT)">
                  <TextInput
                    value={prefs.ai_naming.endpoint}
                    onChange={(v) => updateNested('ai_naming', { endpoint: v })}
                    placeholder="https://api.openai.com/v1"
                    wide
                  />
                </Row>
                <Row label="API Key" description="Bearer token (optional for local endpoints; falls back to env)">
                  <TextInput
                    type="password"
                    value={prefs.ai_naming.api_key}
                    onChange={(v) => updateNested('ai_naming', { api_key: v })}
                    placeholder="sk-…"
                    wide
                  />
                </Row>
                <Row label="Model" description="Chat completion model name">
                  <TextInput
                    value={prefs.ai_naming.model}
                    onChange={(v) => updateNested('ai_naming', { model: v })}
                    placeholder="gpt-4o-mini"
                  />
                </Row>
              </>
            )}
          </Section>

          {/* ── Shortcuts ── */}
          <Section hidden={!showSection('shortcuts')} id="shortcuts" title="Shortcuts" description="Keyboard shortcuts reference. Combos are chosen to avoid browser and terminal conflicts.">
            {getShortcuts().map((item, i) => {
              if ('section' in item) {
                return (
                  <div key={i} className={cn('text-[11px] font-bold text-primary uppercase tracking-widest', i > 0 && 'mt-4')}>
                    {item.section}
                  </div>
                )
              }
              return (
                <div key={i} className="flex items-center justify-between gap-6 py-1">
                  <span className="text-[13px] font-semibold text-ink tracking-tight">{item.label}</span>
                  <div className="flex items-center gap-1.5 shrink-0">
                    {item.keys.map((k, j) => (
                      <Kbd key={j}>{k}</Kbd>
                    ))}
                  </div>
                </div>
              )
            })}
          </Section>

          {/* ── Notifications ── */}
          <Section hidden={!showSection('notifications')} id="notifications" title="Notifications" description="Push alerts and agent event notifications">
            <Row label="Push Alerts" description={
              pushState === 'unsupported'
                ? 'Requires HTTPS or localhost with a supported browser'
                : pushState === 'denied'
                ? 'Blocked by browser — reset in browser site settings'
                : pushState === 'subscribed'
                ? 'Receiving push alerts for agent events'
                : 'Enable to receive alerts even when the tab is closed'
            }>
              {pushState === 'unsupported' ? (
                <span className="text-xs font-bold text-mute/40 uppercase tracking-widest">Unavailable</span>
              ) : pushState === 'denied' ? (
                <span className="text-xs font-bold text-destructive uppercase tracking-widest">Blocked</span>
              ) : (
                <Toggle
                  checked={pushState === 'subscribed'}
                  onChange={(v) => v ? onPushSubscribe() : onPushUnsubscribe()}
                />
              )}
            </Row>
            <Row label="Alert Statuses" description="Which agent statuses trigger alerts">
              <div className="flex gap-2">
                {notifStatuses.map(s => {
                  const isActive = prefs.notifications.statuses.includes(s.value)
                  return (
                    <button
                      key={s.value}
                      onClick={() => toggleNotifStatus(s.value)}
                      className={cn(
                        'px-3 py-1.5 rounded-sm text-xs font-bold uppercase tracking-widest border transition-all',
                        isActive
                          ? 'border-primary bg-primary text-primary-foreground'
                          : 'border-hairline bg-surface text-mute/60 hover:border-hairline/60 hover:text-ink',
                      )}
                    >
                      {s.label}
                    </button>
                  )
                })}
              </div>
            </Row>
            <Row label="Auto-dismiss" description="Seconds before alerts auto-dismiss (0 = manual)">
              <NumberInput
                value={prefs.agent_banner.auto_dismiss_seconds}
                onChange={(v) => updateNested('agent_banner', { auto_dismiss_seconds: Math.max(0, Math.min(300, v)) })}
                min={0}
                max={300}
              />
            </Row>
          </Section>

          {/* ── Agents ── */}
          <Section hidden={!showSection('agents')} id="agents" title="Agents" description="Agent installation and hook configuration status">
            {agentStatus ? (
              <div className="flex flex-col gap-4">
                <AgentStatusList agents={agentStatus.agents} />
                <SetupCommandBox command={agentStatus.setup_command} />
                <button
                  onClick={fetchAgentStatus}
                  disabled={agentLoading}
                  className="self-start px-4 py-2 rounded-md text-xs font-bold uppercase tracking-widest border border-hairline bg-surface text-ink hover:bg-surface-elevated transition-all disabled:opacity-50"
                >
                  {agentLoading ? 'Checking...' : 'Refresh Status'}
                </button>
              </div>
            ) : (
              <p className="text-[13px] font-medium text-mute/60 italic">
                {agentLoading ? 'Checking agents...' : 'Could not load agent status.'}
              </p>
            )}
          </Section>

          {/* ── Machines / Peers ── */}
          <Section hidden={!showSection('peers')} id="peers" title="Machines" description="Connect other termyard machines to share sessions across hosts">
            <PeersSection />
          </Section>

          {/* ── Integrations ── */}
          <Section hidden={!showSection('integrations')} id="integrations" title="Integrations" description="Connect external tools to termyard">
            <WikiViewerSection />
          </Section>

          {/* ── Security ── */}
          {onLogout && (
            <Section hidden={!showSection('security')} id="security" title="Security" description="Session locking and sign out">
              <Row label="Auto-lock Timeout" description="Sign out after idle inactivity (0 = disabled)">
                <div className="flex items-center gap-2">
                  <NumberInput
                    value={prefs.lock_timeout_minutes}
                    onChange={(v) => update({ lock_timeout_minutes: Math.max(0, Math.min(120, v)) })}
                    min={0}
                    max={120}
                  />
                  <span className="text-xs font-bold text-mute/40 uppercase tracking-widest">min</span>
                </div>
              </Row>
              <Divider />
              <Divider />
              <Row label="Sign Out" description="End your current session">
                <button
                  onClick={onLogout}
                  className="px-6 py-2.5 rounded-full text-[13px] font-bold uppercase tracking-widest border border-destructive/40 text-destructive hover:bg-destructive hover:text-white transition-all"
                >
                  Sign out
                </button>
              </Row>
            </Section>
          )}

          {(bucket === 'network' || !bucket) && version && (
            <Section id="about" title="About" description="Version and updates">
              <Row label="Version" description={updateAvailable ? 'A new version is available' : 'You are up to date'}>
                {updateAvailable ? (
                  <button
                    onClick={() => window.location.reload()}
                    className="rounded-sm border border-warning/40 bg-warning/10 px-3 py-1.5 text-[13px] font-bold text-warning hover:text-ink transition-colors"
                    title="Reload to update"
                  >
                    {version} · update
                  </button>
                ) : (
                  <span className="font-mono text-mute">{version}</span>
                )}
              </Row>
              <Row
                label="App Update"
                description={updateError || (updateRestartMode === 'manual' || binaryUpdate?.pending_restart ? 'Installed — restart termyard manually' : binaryUpdate?.update_available ? `Channel ${binaryUpdate.channel}` : 'Checking for app updates')}
              >
                {updateApplying ? (
                  <span className="inline-flex items-center gap-2 rounded-sm border border-warning/40 bg-warning/10 px-3 py-1.5 text-[13px] font-bold text-warning">
                    <span className="h-2 w-2 animate-pulse rounded-full bg-warning" />
                    Updating, reconnecting…
                  </span>
                ) : updateRestartMode === 'manual' || binaryUpdate?.pending_restart ? (
                  <span className="rounded-sm border border-warning/40 bg-warning/10 px-3 py-1.5 text-[13px] font-bold text-warning">
                    Updated — restart manually
                  </span>
                ) : binaryUpdate?.update_available ? (
                  <button
                    onClick={() => { void onApplyUpdate?.().catch(() => {}) }}
                    className="rounded-sm border border-warning/40 bg-warning/10 px-3 py-1.5 text-[13px] font-bold text-warning hover:text-ink transition-colors"
                    title="Update app"
                  >
                    Update to {binaryUpdate.latest_version}
                  </button>
                ) : (
                  <span className="inline-flex items-center gap-2">
                    <span className="font-mono text-mute">{binaryUpdate?.current_version || version}</span>
                    <button
                      onClick={() => onCheckUpdate?.()}
                      disabled={updateChecking}
                      className="rounded-sm border border-hairline px-2 py-1 text-[11px] font-bold text-mute hover:text-ink transition-colors disabled:opacity-50"
                      title="Check for app updates"
                    >
                      {updateChecking ? 'Checking…' : 'Check now'}
                    </button>
                  </span>
                )}
              </Row>
            </Section>
          )}
        </div>
      </div>
    </div>
  )
}

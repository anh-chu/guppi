import { useState, useEffect, useCallback } from 'react'
import { usePreferences, type Preferences } from '../hooks/usePreferences'
import type { UpdateStatus } from '../hooks/useSelfUpdate'
import { usePushNotifications } from '../hooks/usePushNotifications'
import { themePresets, applyTheme, defaultCustomThemePalette, type CustomThemePalette } from '../theme'
import { cn } from '../lib/utils'
import { AgentStatusList, SetupCommandBox } from './Setup'
import { getShortcuts } from '../lib/shortcuts'
import { Section, Divider, Row, SelectInput, NumberInput, TextInput, Toggle, Kbd, ColorInput } from './settings/controls'
import { PeersSection } from './settings/PeersSection'
import { WikiViewerSection } from './settings/WikiViewerSection'

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

const FONT_PRESET_OPTIONS = terminalFontFamilies.map(f => ({ value: f, label: f }))
const FONT_CUSTOM_OPTION = { value: '__custom__', label: 'Custom…' }

const isFontPreset = (fontFamily: string) => terminalFontFamilies.includes(fontFamily)


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

  // AI Naming staged state
  const [stagedAiNaming, setStagedAiNaming] = useState({ endpoint: prefs.ai_naming.endpoint, api_key: prefs.ai_naming.api_key, model: prefs.ai_naming.model })
  const [aiNamingDirty, setAiNamingDirty] = useState(false)
  const [aiNamingSaving, setAiNamingSaving] = useState(false)
  const [aiNamingError, setAiNamingError] = useState<string | null>(null)

  // Font family custom mode
  const [fontFamilyCustomSelected, setFontFamilyCustomSelected] = useState(false)

  // Sync staged state when prefs changes externally
  useEffect(() => {
    if (!aiNamingDirty) {
      setStagedAiNaming({ endpoint: prefs.ai_naming.endpoint, api_key: prefs.ai_naming.api_key, model: prefs.ai_naming.model })
    }
  }, [prefs.ai_naming, aiNamingDirty])

  // Sync font family custom mode when pref changes
  useEffect(() => {
    if (isFontPreset(prefs.terminal.font_family)) {
      setFontFamilyCustomSelected(false)
    }
  }, [prefs.terminal.font_family])

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
    applyTheme(theme, theme === 'custom' ? prefs.custom_theme : undefined)
    await update({ theme })
  }

  // Resolved custom palette (defaults filled in) used for editing + preview
  const customPalette: CustomThemePalette = { ...defaultCustomThemePalette, ...(prefs.custom_theme || {}) }

  const updateCustomPalette = async (patch: Partial<CustomThemePalette>) => {
    const next = { ...customPalette, ...patch }
    applyTheme('custom', next)
    await update({ custom_theme: next })
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
              <button
                key="custom"
                onClick={() => handleThemeChange('custom')}
                className={cn(
                  'p-4 rounded-lg border text-left transition-all duration-200 group',
                  prefs.theme === 'custom'
                    ? 'border-primary bg-primary/5 shadow-[0_0_15px_rgba(255,255,255,0.05)]'
                    : 'border-hairline bg-surface hover:border-hairline/60',
                )}
              >
                <div className="flex items-center gap-2 mb-3">
                  <div className="w-4.5 h-4.5 rounded-full border border-hairline/40" style={{ background: customPalette.background }} />
                  <div className="w-4.5 h-4.5 rounded-full border border-hairline/40" style={{ background: customPalette.foreground }} />
                  <div className="w-4.5 h-4.5 rounded-full border border-hairline/40" style={{ background: customPalette.ansiBlue }} />
                  <div className="w-4.5 h-4.5 rounded-full border border-hairline/40" style={{ background: customPalette.ansiGreen }} />
                </div>
                <div className={cn(
                  'text-[13px] font-bold tracking-tight',
                  prefs.theme === 'custom' ? 'text-primary' : 'text-ink/80'
                )}>Custom</div>
              </button>
            </div>

            {prefs.theme === 'custom' && (
              <div id="custom-theme-editor" className="mt-2 flex flex-col gap-3 rounded-lg border border-hairline bg-surface-elevated/40 p-4">
                <div className="text-xs font-bold uppercase tracking-wider text-mute/60 mb-1">Custom Palette</div>
                <Row label="Background"><ColorInput value={customPalette.background} onChange={(v) => updateCustomPalette({ background: v })} /></Row>
                <Row label="Text"><ColorInput value={customPalette.foreground} onChange={(v) => updateCustomPalette({ foreground: v })} /></Row>
                <Row label="Muted Text"><ColorInput value={customPalette.muted} onChange={(v) => updateCustomPalette({ muted: v })} /></Row>
                <Row label="Accent / Primary"><ColorInput value={customPalette.accent} onChange={(v) => updateCustomPalette({ accent: v })} /></Row>
                <Row label="Success"><ColorInput value={customPalette.success} onChange={(v) => updateCustomPalette({ success: v })} /></Row>
                <Row label="Warning"><ColorInput value={customPalette.warning} onChange={(v) => updateCustomPalette({ warning: v })} /></Row>
                <Row label="Destructive"><ColorInput value={customPalette.destructive} onChange={(v) => updateCustomPalette({ destructive: v })} /></Row>
                <Divider />
                <div className="text-xs font-bold uppercase tracking-wider text-mute/60 mb-1">Terminal Palette</div>
                <Row label="Cursor"><ColorInput value={customPalette.cursor} onChange={(v) => updateCustomPalette({ cursor: v })} /></Row>
                <Row label="Selection Background" description="Supports rgba() for translucency">
                  <TextInput value={customPalette.selectionBackground} onChange={(v) => updateCustomPalette({ selectionBackground: v })} wide />
                </Row>
                <div className="grid grid-cols-2 gap-3">
                  <Row label="Black"><ColorInput value={customPalette.ansiBlack} onChange={(v) => updateCustomPalette({ ansiBlack: v })} /></Row>
                  <Row label="Bright Black"><ColorInput value={customPalette.ansiBrightBlack} onChange={(v) => updateCustomPalette({ ansiBrightBlack: v })} /></Row>
                  <Row label="Red"><ColorInput value={customPalette.ansiRed} onChange={(v) => updateCustomPalette({ ansiRed: v })} /></Row>
                  <Row label="Bright Red"><ColorInput value={customPalette.ansiBrightRed} onChange={(v) => updateCustomPalette({ ansiBrightRed: v })} /></Row>
                  <Row label="Green"><ColorInput value={customPalette.ansiGreen} onChange={(v) => updateCustomPalette({ ansiGreen: v })} /></Row>
                  <Row label="Bright Green"><ColorInput value={customPalette.ansiBrightGreen} onChange={(v) => updateCustomPalette({ ansiBrightGreen: v })} /></Row>
                  <Row label="Yellow"><ColorInput value={customPalette.ansiYellow} onChange={(v) => updateCustomPalette({ ansiYellow: v })} /></Row>
                  <Row label="Bright Yellow"><ColorInput value={customPalette.ansiBrightYellow} onChange={(v) => updateCustomPalette({ ansiBrightYellow: v })} /></Row>
                  <Row label="Blue"><ColorInput value={customPalette.ansiBlue} onChange={(v) => updateCustomPalette({ ansiBlue: v })} /></Row>
                  <Row label="Bright Blue"><ColorInput value={customPalette.ansiBrightBlue} onChange={(v) => updateCustomPalette({ ansiBrightBlue: v })} /></Row>
                  <Row label="Magenta"><ColorInput value={customPalette.ansiMagenta} onChange={(v) => updateCustomPalette({ ansiMagenta: v })} /></Row>
                  <Row label="Bright Magenta"><ColorInput value={customPalette.ansiBrightMagenta} onChange={(v) => updateCustomPalette({ ansiBrightMagenta: v })} /></Row>
                  <Row label="Cyan"><ColorInput value={customPalette.ansiCyan} onChange={(v) => updateCustomPalette({ ansiCyan: v })} /></Row>
                  <Row label="Bright Cyan"><ColorInput value={customPalette.ansiBrightCyan} onChange={(v) => updateCustomPalette({ ansiBrightCyan: v })} /></Row>
                  <Row label="White"><ColorInput value={customPalette.ansiWhite} onChange={(v) => updateCustomPalette({ ansiWhite: v })} /></Row>
                  <Row label="Bright White"><ColorInput value={customPalette.ansiBrightWhite} onChange={(v) => updateCustomPalette({ ansiBrightWhite: v })} /></Row>
                </div>
              </div>
            )}

            <Divider />


          </Section>

          {/* ── Terminal ── */}
          <Section hidden={!showSection('terminal')} id="terminal" title="Terminal" description="Font, scrollback, and fullscreen behavior">
            <Row label="Font Family" description="Monospace font for the terminal">
              <div className="flex flex-col gap-3 w-full sm:w-auto">
                <SelectInput
                  value={fontFamilyCustomSelected || !isFontPreset(prefs.terminal.font_family) ? '__custom__' : prefs.terminal.font_family}
                  onChange={(v) => {
                    if (v === '__custom__') {
                      setFontFamilyCustomSelected(true)
                    } else {
                      setFontFamilyCustomSelected(false)
                      updateNested('terminal', { font_family: v })
                    }
                  }}
                  options={[...FONT_PRESET_OPTIONS, FONT_CUSTOM_OPTION]}
                />
                {(fontFamilyCustomSelected || !isFontPreset(prefs.terminal.font_family)) && (
                  <TextInput
                    value={prefs.terminal.font_family}
                    onChange={(v) => updateNested('terminal', { font_family: v })}
                    placeholder="e.g. Cascadia Code, Berkeley Mono, monospace"
                    wide
                  />
                )}
                <span style={{ fontFamily: prefs.terminal.font_family }} className="text-xs text-mute/60 font-medium">
                  The quick brown fox 0123
                </span>
              </div>
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
            <Row label="Unicode Graphemes" description="Experimental: proper rendering of ZWJ emoji, CJK, and combining marks">
              <Toggle
                checked={prefs.terminal.unicode_graphemes}
                onChange={(v) => updateNested('terminal', { unicode_graphemes: v })}
                label={prefs.terminal.unicode_graphemes ? 'EXPERIMENTAL · ON' : 'EXPERIMENTAL · OFF'}
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
                    value={stagedAiNaming.endpoint}
                    onChange={(v) => {
                      setStagedAiNaming(prev => ({ ...prev, endpoint: v }))
                      setAiNamingDirty(true)
                      setAiNamingError(null)
                    }}
                    placeholder="https://api.openai.com/v1"
                    wide
                  />
                </Row>
                <Row label="API Key" description="Bearer token (optional for local endpoints; falls back to env)">
                  <TextInput
                    type="password"
                    value={stagedAiNaming.api_key}
                    onChange={(v) => {
                      setStagedAiNaming(prev => ({ ...prev, api_key: v }))
                      setAiNamingDirty(true)
                      setAiNamingError(null)
                    }}
                    placeholder="sk-…"
                    wide
                  />
                </Row>
                <Row label="Model" description="Chat completion model name">
                  <TextInput
                    value={stagedAiNaming.model}
                    onChange={(v) => {
                      setStagedAiNaming(prev => ({ ...prev, model: v }))
                      setAiNamingDirty(true)
                      setAiNamingError(null)
                    }}
                    placeholder="gpt-4o-mini"
                  />
                </Row>
                <Row label="" description="">
                  <div className="flex flex-col gap-2">
                    <button
                      onClick={async () => {
                        setAiNamingError(null)
                        setAiNamingSaving(true)
                        try {
                          const result = await updatePrefs(
                            { ai_naming: { enabled: prefs.ai_naming.enabled, endpoint: stagedAiNaming.endpoint, api_key: stagedAiNaming.api_key, model: stagedAiNaming.model } },
                            { optimistic: false }
                          )
                          if (result) {
                            setAiNamingDirty(false)
                            setAiNamingError(null)
                          } else {
                            // Reset to last-known-good
                            setStagedAiNaming({ endpoint: prefs.ai_naming.endpoint, api_key: prefs.ai_naming.api_key, model: prefs.ai_naming.model })
                            setAiNamingDirty(false)
                            setAiNamingError('Failed to save — changes were not applied and have been reverted.')
                          }
                        } finally {
                          setAiNamingSaving(false)
                        }
                      }}
                      disabled={!aiNamingDirty || aiNamingSaving}
                      className="px-4 py-2 rounded-sm text-xs font-bold uppercase tracking-widest border border-hairline bg-surface text-ink hover:bg-surface-elevated transition-all disabled:opacity-50"
                    >
                      Save
                    </button>
                    {aiNamingSaving && <span className="text-xs font-bold text-primary animate-pulse">SAVING…</span>}
                    {aiNamingError && <span className="text-xs font-medium text-destructive">{aiNamingError}</span>}
                  </div>
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

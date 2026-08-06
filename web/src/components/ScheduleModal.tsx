import { useEffect, useMemo, useRef, useState } from 'react'
import { useHosts } from '../hooks/useHosts'
import { usePreferences } from '../hooks/usePreferences'
import { Schedule, ScheduleForm, useSchedules } from '../hooks/useSchedules'
import { Session, sessionKey, sessionScheduleID } from '../lib/session'
import { AgentMark } from './AgentMark'
import { HostSelect } from './HostSelect'
import { cn } from '../lib/utils'
import { describeCron } from '../lib/cron'
import { formatRelativeTime, formatRunCount } from '../lib/time'

interface Props {
  onClose: () => void
}

const agentPresets = [
  { id: 'claude', label: 'Claude', command: 'claude' },
  { id: 'pi', label: 'Pi', command: 'pi' },
  { id: 'codex', label: 'Codex', command: 'codex' },
  { id: 'gemini', label: 'Gemini', command: 'gemini' },
  { id: 'copilot', label: 'Copilot', command: 'copilot' },
  { id: 'opencode', label: 'OpenCode', command: 'opencode' },
]

const cronPresets = [
  { id: 'hourly', label: 'Hourly', spec: '0 * * * *' },
  { id: 'daily', label: 'Daily', spec: '0 0 * * *' },
  { id: 'weekly', label: 'Weekly', spec: '0 0 * * 0' },
  { id: 'custom', label: 'Custom', spec: '' },
] as const

function Toggle({ checked, onChange, label }: { checked: boolean; onChange: (next: boolean) => void; label: string }) {
  return (
    <button
      type="button"
      role="switch"
      aria-checked={checked}
      onClick={() => onChange(!checked)}
      className="flex items-center gap-2 text-xs font-bold uppercase tracking-wider text-mute/60"
    >
      <span className={cn('inline-flex h-5 w-9 items-center rounded-full border transition-colors', checked ? 'bg-primary border-primary' : 'bg-surface-elevated border-hairline')}>
        <span className={cn('block h-3.5 w-3.5 rounded-full bg-canvas transition-transform mx-0.5', checked ? 'translate-x-4' : 'translate-x-0')} />
      </span>
      {label}
    </button>
  )
}

export function ScheduleModal({ onClose }: Props) {
  const { schedules, create, update, remove, runNow, refresh } = useSchedules()
  const { hosts } = useHosts()
  const { prefs } = usePreferences()
  const [selectedId, setSelectedId] = useState<string | null>(null)
  const [name, setName] = useState('')
  const [cronSpec, setCronSpec] = useState('0 * * * *')
  const [cronPreset, setCronPreset] = useState<(typeof cronPresets)[number]['id']>('hourly')
  const [command, setCommand] = useState('')
  const [path, setPath] = useState('~')
  const [preset, setPreset] = useState<string | null>(prefs.default_agent || 'claude')
  const [worktreeMode, setWorktreeMode] = useState(false)
  const [worktreeBranch, setWorktreeBranch] = useState('')
  const [maxConcurrency, setMaxConcurrency] = useState(0)
  const [enabled, setEnabled] = useState(true)
  const [hostId, setHostId] = useState('')
  const [error, setError] = useState<string | null>(null)
  const [busyId, setBusyId] = useState<string | null>(null)
  const nameRef = useRef<HTMLInputElement>(null)
  const cronDescription = useMemo(() => describeCron(cronSpec), [cronSpec])
  const loadedIdRef = useRef<string | null>(null)

  const onlineHosts = useMemo(() => hosts.filter(host => host.online), [hosts])
  const localHost = useMemo(() => onlineHosts.find(host => host.local), [onlineHosts])
  const preferredHostId = localHost ? '' : (onlineHosts[0]?.id || '')
  const hostLabelById = useMemo(() => new Map(onlineHosts.map(host => [host.id, host.name])), [onlineHosts])
  // The wire (Schedule.host / ScheduleForm.host, sent as target_owner --
  // see useSchedules.ts's toWire/normalizeSchedule) carries a canonical catalog
  // OwnerID when the target host has one, falling back to its raw peer
  // fingerprint on a legacy-only node (state.OwnerID and the transport
  // fingerprint are different, non-invertible identifier spaces -- see
  // Host.owner_id's doc). hostId (component state, and every HostSelect
  // option value) is always the fingerprint (`host.id`), so these maps
  // translate between the two identity spaces at the wire boundary only.
  const ownerIdByHostId = useMemo(() => new Map(onlineHosts.map(host => [host.id, host.owner_id || host.id])), [onlineHosts])
  const hostIdByOwnerId = useMemo(() => new Map(onlineHosts.map(host => [host.owner_id || host.id, host.id])), [onlineHosts])
  const hostLabelByOwnerId = useMemo(() => new Map(onlineHosts.map(host => [host.owner_id || host.id, host.name])), [onlineHosts])

  const defaultCommand = useMemo(() => {
    return agentPresets.find(option => option.id === (prefs.default_agent || 'claude'))?.command || prefs.default_agent || 'claude'
  }, [prefs.default_agent])

  useEffect(() => {
    nameRef.current?.focus()
  }, [])

  useEffect(() => {
    const handler = (e: KeyboardEvent) => {
      if (e.key === 'Escape') onClose()
    }
    window.addEventListener('keydown', handler)
    return () => window.removeEventListener('keydown', handler)
  }, [onClose])

  useEffect(() => {
    if (!selectedId) {
      loadedIdRef.current = null
      return
    }
    // Only hydrate the form when the selection changes, not on every 15s poll
    // refresh, otherwise an in-progress edit gets clobbered mid-typing.
    if (loadedIdRef.current === selectedId) return
    const next = schedules.find(schedule => schedule.id === selectedId)
    if (!next) return
    loadedIdRef.current = selectedId
    setName(next.name)
    setCronSpec(next.cronSpec)
    const presetMatch = cronPresets.find(item => item.spec === next.cronSpec)
    setCronPreset((presetMatch?.id ?? 'custom') as typeof cronPreset)
    setCommand(next.command)
    setPath(next.path || '~')
    setPreset(next.agentType || null)
    setWorktreeMode(Boolean(next.worktreeBranch))
    setWorktreeBranch(next.worktreeBranch || '')
    setMaxConcurrency(next.maxConcurrency || 0)
    setEnabled(next.enabled)
    setHostId(next.host ? (hostIdByOwnerId.get(next.host) || next.host) : '')
    setError(null)
  }, [selectedId, schedules])

  useEffect(() => {
    if (hostId) return
    setHostId(preferredHostId)
  }, [preferredHostId, hostId])

  const resetForm = () => {
    setSelectedId(null)
    setName('')
    setCronPreset('hourly')
    setCronSpec('0 * * * *')
    setCommand(defaultCommand)
    setPath('~')
    setPreset(prefs.default_agent || 'claude')
    setWorktreeMode(false)
    setWorktreeBranch('')
    setMaxConcurrency(0)
    setEnabled(true)
    setHostId(preferredHostId)
    setError(null)
  }

  useEffect(() => {
    // Only drop back to a blank form when the schedule being edited was
    // deleted out from under us. Do NOT reset on an empty list: the 15s poll
    // hands us a fresh (still-empty) array every cycle, which would wipe an
    // in-progress create mid-typing.
    if (selectedId && !schedules.some(schedule => schedule.id === selectedId)) {
      resetForm()
    }
  }, [schedules, selectedId])

  useEffect(() => {
    if (selectedId) return
    if (name || command || path !== '~' || worktreeBranch || !enabled) return
    setCommand(defaultCommand)
  }, [defaultCommand, name, command, path, worktreeBranch, enabled, selectedId])

  const currentSchedule: Schedule | undefined = useMemo(() => schedules.find(schedule => schedule.id === selectedId), [schedules, selectedId])
  const hostName = hostId ? (hostLabelById.get(hostId) || hostId) : 'Local'

  const buildPayload = (): ScheduleForm => ({
    name: name.trim(),
    cronSpec: cronSpec.trim(),
    command: command.trim(),
    path: path.trim() || '~',
    agentType: preset || prefs.default_agent || 'claude',
    host: hostId ? (ownerIdByHostId.get(hostId) || hostId) : '',
    worktreeBranch: worktreeMode ? worktreeBranch.trim() : '',
    maxConcurrency: Number.isFinite(maxConcurrency) && maxConcurrency > 0 ? Math.floor(maxConcurrency) : 0,
    enabled,
  })

  const submit = async () => {
    const payload = buildPayload()
    if (!payload.name) {
      setError('Name required')
      return
    }
    if (!payload.cronSpec) {
      setError('Cron required')
      return
    }
    if (!payload.command) {
      setError('Command required')
      return
    }
    if (worktreeMode && !payload.worktreeBranch) {
      setError('Worktree branch required')
      return
    }
    setBusyId(selectedId || '__create__')
    setError(null)
    const err = selectedId ? await update(selectedId, payload) : await create(payload)
    setBusyId(null)
    if (err) {
      setError(err)
      return
    }
    await refresh()
    resetForm()
  }

  const selectPreset = (id: string) => {
    if (preset === id) {
      setPreset(null)
      setCommand('')
      return
    }
    setPreset(id)
    setCommand(agentPresets.find(option => option.id === id)?.command || '')
  }

  const chooseCronPreset = (id: typeof cronPreset) => {
    setCronPreset(id)
    const presetItem = cronPresets.find(item => item.id === id)
    if (presetItem?.spec) setCronSpec(presetItem.spec)
  }

  const deleteSchedule = async (schedule: Schedule) => {
    if (!confirm(`Delete schedule ${schedule.name}?`)) return

    // Find the sessions this schedule spawned (attrs map is authoritative; the
    // session field is the fallback). Offer to kill them along with the schedule.
    let scheduleSessions: Session[] = []
    try {
      const [sessRes, attrsRes] = await Promise.all([
        fetch('/api/sessions'),
        fetch('/api/session-attrs'),
      ])
      const sessions: Session[] = sessRes.ok ? (await sessRes.json()) || [] : []
      const attrs = attrsRes.ok ? await attrsRes.json().catch(() => null) : null
      const scheduleIDs: Record<string, string> = attrs?.schedule_ids || {}
      scheduleSessions = sessions.filter(s => {
        const sid = scheduleIDs[sessionKey(s)] || scheduleIDs[s.name] || sessionScheduleID(s)
        return sid === schedule.id
      })
    } catch {
      // non-fatal: proceed with schedule-only delete
    }

    let killSessions = false
    if (scheduleSessions.length > 0) {
      killSessions = confirm(`Also kill ${scheduleSessions.length} session${scheduleSessions.length === 1 ? '' : 's'} spawned by this schedule?`)
    }

    setBusyId(schedule.id)
    if (killSessions) {
      await Promise.all(scheduleSessions.map(s =>
        fetch('/api/session/kill', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ id: s.id, name: s.name, host: s.host || undefined }),
        }).catch(() => {}),
      ))
    }
    const err = await remove(schedule.id)
    setBusyId(null)
    if (err) setError(err)
    else if (selectedId === schedule.id) resetForm()
  }

  const runSchedule = async (schedule: Schedule) => {
    setBusyId(schedule.id)
    const err = await runNow(schedule.id)
    setBusyId(null)
    if (err) setError(err)
  }

  const displayName = selectedId ? `Edit ${currentSchedule?.name || 'schedule'}` : 'Create schedule'

  return (
    <div
      className="fixed inset-0 z-50 flex items-center justify-center bg-black/60 backdrop-blur-sm"
      onClick={(e) => { if (e.target === e.currentTarget) onClose() }}
    >
      <div className="w-full max-w-4xl max-h-[88vh] bg-canvas border border-hairline rounded-xl shadow-2xl overflow-hidden flex flex-col">
        <div className="flex items-center justify-between px-5 py-4 border-b border-hairline">
          <div>
            <h2 className="text-sm font-bold text-ink tracking-tight">Schedules</h2>
            <p className="text-xs text-mute mt-0.5">Cron runs spawn fresh sessions</p>
          </div>
          <button
            onClick={onClose}
            className="w-7 h-7 flex items-center justify-center rounded-md text-mute hover:text-ink hover:bg-surface-elevated transition-colors text-lg leading-none"
          >
            ×
          </button>
        </div>

        <div className="grid grid-cols-[1.15fr_1fr] gap-0 flex-1 min-h-0">
          <div className="border-r border-hairline overflow-y-auto p-4">
            <div className="flex items-center justify-between gap-3 mb-3">
              <p className="text-[10px] uppercase tracking-widest font-semibold text-mute/60">Saved schedules</p>
              <button
                type="button"
                onClick={() => { resetForm(); setSelectedId(null) }}
                className="text-[10px] font-bold uppercase tracking-widest text-primary hover:opacity-80 transition-opacity"
              >
                New schedule
              </button>
            </div>
            <div className="space-y-1.5">
              {schedules.length === 0 ? (
                <p className="text-xs text-mute/60 text-center py-8">No schedules yet</p>
              ) : schedules.map(schedule => {
                const isSelected = schedule.id === selectedId
                return (
                  <button
                    key={schedule.id}
                    type="button"
                    onClick={() => setSelectedId(schedule.id)}
                    className={cn(
                      'w-full text-left rounded-lg border px-3 py-2.5 transition-colors',
                      isSelected ? 'border-primary bg-primary/5' : 'border-hairline bg-surface hover:bg-surface-elevated',
                    )}
                  >
                    <div className="flex items-start justify-between gap-3">
                      <div className="min-w-0 flex-1">
                        <div className="flex items-center gap-2 min-w-0">
                          <span className="text-[13px] font-semibold text-ink truncate">{schedule.name}</span>
                          <span className={cn('text-[10px] font-bold uppercase tracking-widest px-1.5 py-0.5 rounded-xs border shrink-0', schedule.enabled ? 'border-emerald-400/30 text-emerald-400 bg-emerald-400/10' : 'border-amber-400/30 text-amber-400 bg-amber-400/10')}>
                            {schedule.enabled ? 'enabled' : 'paused'}
                          </span>
                        </div>
                        <div className="mt-1 text-[11px] text-mute/70 truncate" title={schedule.cronSpec}>{describeCron(schedule.cronSpec) ?? schedule.cronSpec}</div>
                        <div className="mt-1 flex items-center gap-2 text-[10px] text-mute/50 font-medium flex-wrap">
                          <span>next {formatRelativeTime(schedule.nextRun)}</span>
                          <span>·</span>
                          <span>{formatRunCount(schedule.runCount)}</span>
                          {schedule.host && (
                            <>
                              <span>·</span>
                              <span>{hostLabelByOwnerId.get(schedule.host) || schedule.host}</span>
                            </>
                          )}
                        </div>
                      </div>
                      <div className="shrink-0 flex items-center gap-1">
                        <button
                          type="button"
                          onClick={(e) => { e.stopPropagation(); runSchedule(schedule) }}
                          disabled={busyId === schedule.id}
                          className="px-2 py-1 rounded-md border border-hairline text-[10px] font-bold uppercase tracking-widest text-mute/70 hover:text-ink hover:bg-surface-elevated transition-colors disabled:opacity-50"
                        >
                          Run now
                        </button>
                        <button
                          type="button"
                          onClick={(e) => { e.stopPropagation(); deleteSchedule(schedule) }}
                          disabled={busyId === schedule.id}
                          className="px-2 py-1 rounded-md border border-hairline text-[10px] font-bold uppercase tracking-widest text-mute/70 hover:text-red-400 hover:bg-red-400/10 transition-colors disabled:opacity-50"
                        >
                          Delete
                        </button>
                      </div>
                    </div>
                  </button>
                )
              })}
            </div>
          </div>

          <div className="overflow-y-auto p-4">
            <div className="flex items-center justify-between gap-3 mb-3">
              <p className="text-[10px] uppercase tracking-widest font-semibold text-mute/60">{displayName}</p>
              {selectedId && (
                <button
                  type="button"
                  onClick={() => resetForm()}
                  className="text-[10px] font-bold uppercase tracking-widest text-mute/60 hover:text-ink transition-colors"
                >
                  Clear
                </button>
              )}
            </div>

            <div className="space-y-4">
              <div>
                <div className="text-xs font-bold text-mute/60 uppercase tracking-wider mb-2 ml-1">Name</div>
                <input
                  ref={nameRef}
                  value={name}
                  onChange={(e) => setName(e.target.value)}
                  placeholder="Nightly review"
                  className="w-full text-[13px] text-ink bg-surface-elevated border border-hairline rounded-sm px-3 py-2 outline-none font-sans font-medium placeholder:text-mute/40 focus:border-primary/60 transition-colors"
                />
              </div>

              <div>
                <div className="text-xs font-bold text-mute/60 uppercase tracking-wider mb-2 ml-1">Cron</div>
                <div className="grid grid-cols-4 gap-2">
                  {cronPresets.map(option => {
                    const active = cronPreset === option.id
                    return (
                      <button
                        key={option.id}
                        type="button"
                        onClick={() => chooseCronPreset(option.id)}
                        className={cn(
                          'rounded-lg border px-3 py-2 text-left transition-colors',
                          active ? 'border-primary bg-primary/5 text-ink' : 'border-hairline bg-surface-elevated/30 text-mute hover:text-ink hover:border-primary/40',
                        )}
                      >
                        <div className="text-[11px] font-semibold">{option.label}</div>
                        {option.spec && <div className="text-[10px] mt-0.5 font-mono opacity-70 truncate">{option.spec}</div>}
                      </button>
                    )
                  })}
                </div>
                <input
                  value={cronSpec}
                  onChange={(e) => { setCronSpec(e.target.value); setCronPreset('custom') }}
                  placeholder="0 * * * *"
                  className="mt-2 w-full text-[13px] text-ink bg-surface-elevated border border-hairline rounded-sm px-3 py-2 outline-none font-mono placeholder:text-mute/40 focus:border-primary/60 transition-colors"
                />
                {cronSpec.trim() && (
                  cronDescription
                    ? <div className="mt-1 ml-1 text-[11px] text-mute/70">{cronDescription}</div>
                    : <div className="mt-1 ml-1 text-[11px] text-danger/80">Invalid cron expression</div>
                )}
              </div>

              <div>
                <div className="text-xs font-bold text-mute/60 uppercase tracking-wider mb-2 ml-1">Agent</div>
                <div className="grid grid-cols-3 gap-2">
                  {agentPresets.map(option => {
                    const active = option.id === preset
                    const isDefault = option.id === (prefs.default_agent || 'claude')
                    return (
                      <button
                        key={option.id}
                        type="button"
                        onClick={() => selectPreset(option.id)}
                        className={cn(
                          'relative flex flex-col items-center gap-2 rounded-lg border p-3 transition-all duration-200 group',
                          active
                            ? 'border-primary bg-primary/5'
                            : 'border-hairline bg-surface-elevated/30 hover:border-hairline/60 grayscale opacity-70 hover:grayscale-0 hover:opacity-100',
                        )}
                      >
                        <span
                          role="button"
                          aria-label={isDefault ? 'Default agent' : 'Set as default'}
                          title={isDefault ? 'Default agent' : 'Set as default'}
                          onClick={e => { e.stopPropagation() }}
                          className={cn(
                            'absolute top-1.5 right-1.5 w-4 h-4 flex items-center justify-center transition-opacity cursor-pointer',
                            isDefault ? 'opacity-100' : 'opacity-0 group-hover:opacity-60 hover:!opacity-100',
                          )}
                        >
                          <svg viewBox="0 0 16 16" className={cn('w-3 h-3', isDefault ? 'fill-amber-400 text-amber-400' : 'fill-none text-mute stroke-current')} strokeWidth={isDefault ? 0 : 1.5}>
                            <polygon points="8,1.5 10,6 15,6.5 11.5,10 12.5,15 8,12.5 3.5,15 4.5,10 1,6.5 6,6" />
                          </svg>
                        </span>
                        <AgentMark agentType={option.id} className="h-6 min-w-10 px-2 shrink-0" />
                        <span className={cn('text-xs font-bold uppercase tracking-tight', active ? 'text-primary' : 'text-mute')}>{option.label}</span>
                      </button>
                    )
                  })}
                </div>
                <input
                  value={command}
                  onChange={(e) => { setCommand(e.target.value); setPreset(null) }}
                  placeholder="shell command..."
                  className="mt-2 w-full text-[13px] text-ink bg-surface-elevated border border-hairline rounded-sm px-3 py-2 outline-none font-mono placeholder:text-mute/40 focus:border-primary/60 transition-colors"
                />
              </div>

              <div>
                <div className="text-xs font-bold text-mute/60 uppercase tracking-wider mb-2 ml-1">Path</div>
                <input
                  value={path}
                  onChange={(e) => setPath(e.target.value)}
                  placeholder="~/repo"
                  className="w-full text-[13px] text-ink bg-surface-elevated border border-hairline rounded-sm px-3 py-2 outline-none font-mono placeholder:text-mute/40 focus:border-primary/60 transition-colors"
                />
              </div>

              <div>
                <div className="text-xs font-bold text-mute/60 uppercase tracking-wider mb-2 ml-1">Worktree</div>
                <label className="flex items-center gap-2 cursor-pointer select-none mb-2">
                  <input
                    type="checkbox"
                    checked={worktreeMode}
                    onChange={(e) => setWorktreeMode(e.target.checked)}
                    className="w-3.5 h-3.5 accent-primary"
                  />
                  <span className="text-xs font-bold text-mute/60 uppercase tracking-wider">Create as worktree</span>
                </label>
                {worktreeMode && (
                  <input
                    value={worktreeBranch}
                    onChange={(e) => setWorktreeBranch(e.target.value)}
                    placeholder="branch-name"
                    className="w-full text-[13px] text-ink bg-surface-elevated border border-hairline rounded-sm px-3 py-2 outline-none font-mono placeholder:text-mute/40 focus:border-primary/60 transition-colors"
                  />
                )}
              </div>

              <div>
                <div className="text-xs font-bold text-mute/60 uppercase tracking-wider mb-2 ml-1">Max concurrent runs</div>
                <input
                  type="number"
                  min={0}
                  value={maxConcurrency || ''}
                  onChange={(e) => setMaxConcurrency(Math.max(0, Math.floor(Number(e.target.value) || 0)))}
                  placeholder="0 = unlimited"
                  className="w-full text-[13px] text-ink bg-surface-elevated border border-hairline rounded-sm px-3 py-2 outline-none font-mono placeholder:text-mute/40 focus:border-primary/60 transition-colors"
                />
                <div className="mt-1 ml-1 text-[11px] text-mute/70">
                  {maxConcurrency > 0
                    ? `Keeps newest ${maxConcurrency} run${maxConcurrency === 1 ? '' : 's'}; oldest are killed when a new one spawns.`
                    : 'Unlimited. Runs accumulate until killed manually.'}
                </div>
              </div>

              <div>
                <div className="text-xs font-bold text-mute/60 uppercase tracking-wider mb-2 ml-1">Host</div>
                {onlineHosts.length > 1 ? (
                  <HostSelect
                    value={hostId}
                    onChange={setHostId}
                    options={[{ value: '', label: 'Local' }, ...onlineHosts.filter(host => !host.local).map(host => ({ value: host.id, label: host.name }))]}
                    className="w-full text-[13px] font-bold text-ink bg-surface-elevated border border-hairline rounded-sm px-3 py-2 outline-none focus:border-primary/60 transition-colors cursor-pointer"
                  />
                ) : (
                  <div className="text-[13px] text-mute/70 bg-surface-elevated border border-hairline rounded-sm px-3 py-2">
                    {hostName}
                  </div>
                )}
              </div>

              <Toggle checked={enabled} onChange={setEnabled} label="Enabled" />

              {error && (
                <p className="text-xs text-red-400 bg-red-400/8 border border-red-400/20 rounded-lg px-3 py-2 break-all">{error}</p>
              )}
            </div>
          </div>
        </div>

        <div className="py-4 px-5 border-t border-hairline bg-surface-elevated/10 flex justify-between items-center gap-4">
          <div className="text-xs text-mute/50 font-medium">
            {selectedId ? currentSchedule?.name : 'New schedule'}
          </div>
          <div className="flex items-center gap-2">
            <button
              onClick={submit}
              disabled={busyId !== null || !name.trim() || !cronSpec.trim() || !command.trim() || (worktreeMode && !worktreeBranch.trim())}
              className="px-5 py-2 rounded-full text-[12px] font-bold uppercase tracking-widest bg-primary text-primary-foreground hover:bg-white/90 transition-all disabled:opacity-30 disabled:cursor-not-allowed"
            >
              {busyId === (selectedId || '__create__') ? '…' : selectedId ? 'Save' : 'Create'}
            </button>
          </div>
        </div>
      </div>
    </div>
  )
}

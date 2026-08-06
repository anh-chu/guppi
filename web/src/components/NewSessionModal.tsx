import { useState, useEffect, useMemo, useRef } from 'react'
import { Host } from '../hooks/useHosts'
import { Session } from '../lib/session'
import { usePreferences } from '../hooks/usePreferences'
import { cn } from '../lib/utils'
import { AgentMark } from './AgentMark'
import { HostSelect } from './HostSelect'

interface NewSessionModalProps {
  hosts: Host[]
  sessions: Session[]
  onCreateSession: (name: string, path: string, command: string, hostId?: string, worktreeBranch?: string) => Promise<string | null>
  onClose: () => void
}

const presets = [
  { id: 'claude', label: 'Claude', command: 'claude' },
  { id: 'pi', label: 'Pi', command: 'pi' },
  { id: 'codex', label: 'Codex', command: 'codex' },
  { id: 'gemini', label: 'Gemini', command: 'gemini' },
  { id: 'copilot', label: 'Copilot', command: 'copilot' },
  { id: 'opencode', label: 'OpenCode', command: 'opencode' },
]

function basename(value: string): string {
  const trimmed = value.trim().replace(/[\\/]+$/, '')
  if (!trimmed) return ''
  if (trimmed === '~') return 'home'
  const parts = trimmed.split(/[\\/]/)
  return parts[parts.length - 1] || ''
}

export function NewSessionModal({ hosts, sessions, onCreateSession, onClose }: NewSessionModalProps) {
  const { prefs, updatePrefs } = usePreferences()
  const defaultAgent = prefs.default_agent || 'claude'
  const [name, setName] = useState('')
  const [path, setPath] = useState('')
  const [preset, setPreset] = useState<string | null>(defaultAgent)
  const [command, setCommand] = useState(() => presets.find(p => p.id === defaultAgent)?.command || defaultAgent)
  const [worktreeMode, setWorktreeMode] = useState(false)
  const [worktreeBranch, setWorktreeBranch] = useState('')
  const [worktreeError, setWorktreeError] = useState<string | null>(null)
  const onlineHosts = hosts.filter(h => h.online)
  const showHostSelect = onlineHosts.length > 1
  const localHost = onlineHosts.find(h => h.local)
  const [selectedHost, setSelectedHost] = useState<string>(localHost?.id || '')
  const pathInputRef = useRef<HTMLInputElement>(null)
  const [dropdownOpen, setDropdownOpen] = useState(false)
  const [highlightedIndex, setHighlightedIndex] = useState(-1)
  const containerRef = useRef<HTMLDivElement>(null)
  const dropdownOpenRef = useRef(dropdownOpen)
  dropdownOpenRef.current = dropdownOpen
  const resolvedCommand = command.trim()
  // selectedHost is a Host.id (peer transport fingerprint, from HostSelect's
  // options). AppLegacy's Session.host also carries the fingerprint (or is
  // falsy for local), so a raw string comparison works there. AppV2's
  // Session.host instead always carries the v2 OwnerID (a DIFFERENT string
  // encoding -- see state.OwnerIDFromFingerprint) -- even for local
  // sessions, which are never falsy under v2. Resolving the selected host's
  // OwnerID once here and matching against either encoding keeps duplicate-
  // name detection correct for both.
  const selectedHostOwnerId = useMemo(
    () => hosts.find(h => h.id === selectedHost)?.owner_id,
    [hosts, selectedHost],
  )
  const isSelectedHost = (session: Session) => {
    if (!session.host) return !selectedHost || selectedHost === localHost?.id
    return session.host === selectedHost || (!!selectedHostOwnerId && session.host === selectedHostOwnerId)
  }
  const existingNames = useMemo(() => {
    return new Set(
      sessions
        .filter(isSelectedHost)
        .map(session => session.name),
    )
  }, [selectedHost, selectedHostOwnerId, sessions])
  const uniqueSessionName = (value: string) => {
    const trimmed = value.trim()
    if (!trimmed) return ''
  if (trimmed === '~') return 'home'
    if (!existingNames.has(trimmed)) return trimmed
    let suffix = 2
    let candidate = `${trimmed}-${suffix}`
    while (existingNames.has(candidate)) {
      suffix += 1
      candidate = `${trimmed}-${suffix}`
    }
    return candidate
  }
  const suggestedName = useMemo(() => {
    const leaf = basename(path || '~')
    if (!leaf) return ''
    const branch = worktreeMode && worktreeBranch.trim()
      ? worktreeBranch.trim().replace(/\//g, '-')
      : ''
    const base = branch ? `${leaf}-${branch}` : leaf
    return uniqueSessionName(base)
  }, [path, worktreeMode, worktreeBranch, existingNames])

  interface RecentLocation {
    path: string
    hostId: string   // value to assign to selectedHost
    hostName: string
    local: boolean
  }

  const recentLocations = useMemo<RecentLocation[]>(() => {
    const localId = localHost?.id || ''
    const localOwnerId = localHost?.owner_id
    const onlineIds = new Set(onlineHosts.map(h => h.id))
    const hostNameById = new Map(onlineHosts.map(h => [h.id, h.name]))
    // AppV2's Session.host is always the v2 OwnerID (never falsy, even for
    // local sessions), a DIFFERENT string encoding than Host.id (the peer
    // transport fingerprint) that onlineIds/hostNameById are keyed by --
    // see state.OwnerIDFromFingerprint. Without this reverse lookup, every
    // v2 session's location was silently dropped below (onlineIds never
    // contains an OwnerID) instead of being resolved to its host's real
    // fingerprint id.
    const hostIdByOwnerId = new Map(
      onlineHosts.filter(h => h.owner_id).map(h => [h.owner_id as string, h.id]),
    )
    const seen = new Set<string>()
    const sorted = [...sessions]
      .filter(s => s.project_path && s.project_path.trim())
      .sort((a, b) => new Date(b.last_activity).getTime() - new Date(a.last_activity).getTime())
    const unique: RecentLocation[] = []
    for (const s of sorted) {
      const p = s.project_path!
      const local = !s.host || (!!localOwnerId && s.host === localOwnerId)
      const hostId = local ? localId : (hostIdByOwnerId.get(s.host!) || s.host!)
      // Skip locations whose host is offline/unknown (cannot create there)
      if (!onlineIds.has(hostId)) continue
      const key = `${hostId}::${p}`
      if (seen.has(key)) continue
      seen.add(key)
      unique.push({
        path: p,
        hostId,
        hostName: local
          ? (localHost?.name || s.host_name || 'Local')
          : (hostNameById.get(hostId) || s.host_name || s.host!),
        local,
      })
      if (unique.length >= 10) break
    }
    return unique
  }, [sessions, onlineHosts, localHost])

  const filteredLocations = useMemo(() => {
    if (!path) return recentLocations
    const lower = path.toLowerCase()
    return recentLocations.filter(l => l.path.toLowerCase().startsWith(lower))
  }, [path, recentLocations])

  const handlePresetClick = (id: string) => {
    if (preset === id) {
      setPreset(null)
      setCommand('')
    } else {
      setPreset(id)
      setCommand(presets.find(p => p.id === id)?.command || '')
    }
  }

  const selectLocation = (loc: RecentLocation) => {
    setPath(loc.path)
    setSelectedHost(loc.hostId)
    setDropdownOpen(false)
    setHighlightedIndex(-1)
  }

  const handlePathKeyDown = (e: React.KeyboardEvent<HTMLInputElement>) => {
    if (dropdownOpen && filteredLocations.length > 0) {
      if (e.key === 'ArrowDown') {
        e.preventDefault()
        setHighlightedIndex(prev => (prev < filteredLocations.length - 1 ? prev + 1 : 0))
        return
      }
      if (e.key === 'ArrowUp') {
        e.preventDefault()
        setHighlightedIndex(prev => (prev > 0 ? prev - 1 : filteredLocations.length - 1))
        return
      }
      if (e.key === 'Enter' && highlightedIndex >= 0) {
        e.preventDefault()
        selectLocation(filteredLocations[highlightedIndex])
        return
      }
      if (e.key === 'Escape') {
        e.preventDefault()
        setDropdownOpen(false)
        setHighlightedIndex(-1)
        return
      }
      if (e.key === 'Tab') {
        setDropdownOpen(false)
        setHighlightedIndex(-1)
        return
      }
    }
    if (e.key === 'Enter') {
      e.preventDefault()
      handleSubmit()
    }
  }

  useEffect(() => {
    pathInputRef.current?.focus()
  }, [])

  useEffect(() => {
    const handler = (e: KeyboardEvent) => {
      if (e.key === 'Escape') {
        if (dropdownOpenRef.current) {
          e.preventDefault()
          e.stopImmediatePropagation()
          setDropdownOpen(false)
          setHighlightedIndex(-1)
          return
        }
        e.preventDefault()
        e.stopImmediatePropagation()
        onClose()
      }
    }
    window.addEventListener('keydown', handler, true)
    return () => window.removeEventListener('keydown', handler, true)
  }, [onClose])

  useEffect(() => {
    if (!dropdownOpen) return
    const handleMouseDown = (e: MouseEvent) => {
      if (containerRef.current && !containerRef.current.contains(e.target as Node)) {
        setDropdownOpen(false)
      }
    }
    document.addEventListener('mousedown', handleMouseDown)
    return () => document.removeEventListener('mousedown', handleMouseDown)
  }, [dropdownOpen])

  useEffect(() => {
    setHighlightedIndex(-1)
  }, [filteredLocations])

  const handleSubmit = async () => {
    const trimmedPath = path.trim() || '~'
    const trimmedName = uniqueSessionName(name.trim() || suggestedName)
    if (!trimmedName) return
    setWorktreeError(null)
    const err = await onCreateSession(trimmedName, trimmedPath, resolvedCommand, selectedHost || undefined, worktreeMode ? worktreeBranch.trim() || undefined : undefined)
    if (err) setWorktreeError(err)
  }

  const handleKeyDown = (e: React.KeyboardEvent) => {
    if (e.key === 'Enter') {
      e.preventDefault()
      handleSubmit()
    }
  }

  return (
    <div
      className="fixed inset-0 z-[9999] flex items-start justify-center pt-[18vh] bg-black/70 backdrop-blur-sm"
      onClick={onClose}
    >
      <div
        className="w-[440px] bg-surface border border-hairline rounded-xl shadow-[0_32px_128px_rgba(0,0,0,0.8)] flex flex-col overflow-hidden"
        onClick={e => e.stopPropagation()}
      >
        <div className="p-6">
          <div className="text-[15px] text-ink font-bold tracking-tight mb-5 uppercase tracking-widest">New Session</div>
          <div className="space-y-4">
            <div>
              <div className="text-xs font-bold text-mute/60 uppercase tracking-wider mb-2 ml-1">Location</div>
              <div ref={containerRef} className="relative">
                <input
                  ref={pathInputRef}
                  value={path}
                  onChange={e => setPath(e.target.value)}
                  onKeyDown={handlePathKeyDown}
                  onFocus={() => setDropdownOpen(true)}
                  placeholder="~"
                  className="w-full text-[14px] text-ink bg-surface-elevated border border-hairline rounded-sm px-3 py-2 outline-none font-sans font-medium placeholder:text-mute/40 focus:border-primary/60 transition-colors"
                />
                {dropdownOpen && filteredLocations.length > 0 && (
                  <div className="absolute left-0 right-0 top-full mt-0.5 bg-surface border border-hairline rounded-sm shadow-lg z-10 overflow-hidden">
                    {filteredLocations.map((loc, i) => (
                      <div
                        key={`${loc.hostId}::${loc.path}`}
                        onMouseDown={() => selectLocation(loc)}
                        className={cn(
                          'flex items-center justify-between gap-2 px-3 py-2 text-[13px] font-mono text-ink cursor-pointer',
                          i === highlightedIndex && 'bg-primary/10 text-primary'
                        )}
                      >
                        <span className="truncate">{loc.path}</span>
                        <span
                          className={cn(
                            'shrink-0 text-[9px] font-bold uppercase tracking-wider px-1.5 py-0.5 rounded-xs border',
                            loc.local
                              ? 'border-hairline text-mute/60'
                              : 'border-primary/40 text-primary/80'
                          )}
                        >
                          {loc.local ? 'Local' : loc.hostName}
                        </span>
                      </div>
                    ))}
                  </div>
                )}
              </div>
              <label className="mt-2 flex items-center gap-2 cursor-pointer select-none">
                <input
                  type="checkbox"
                  checked={worktreeMode}
                  onChange={e => setWorktreeMode(e.target.checked)}
                  className="w-3.5 h-3.5 accent-primary"
                />
                <span className="text-xs font-bold text-mute/60 uppercase tracking-wider">Create as worktree</span>
              </label>
              {worktreeMode && (
                <>
                  <input
                    value={worktreeBranch}
                    onChange={e => { setWorktreeBranch(e.target.value); setWorktreeError(null) }}
                    onKeyDown={handleKeyDown}
                    placeholder="branch-name"
                    className="mt-2 w-full text-[13px] text-ink bg-surface-elevated border border-hairline rounded-sm px-3 py-2 outline-none font-mono placeholder:text-mute/40 focus:border-primary/60 transition-colors"
                  />
                  {worktreeError && (
                    <div className="mt-1.5 text-xs text-red-400 font-mono break-all">{worktreeError}</div>
                  )}
                </>
              )}
            </div>
            <div>
              <div className="text-xs font-bold text-mute/60 uppercase tracking-wider mb-2 ml-1">Agent</div>
              <div className="grid grid-cols-3 gap-2">
                {presets.map(option => {
                  const active = option.id === preset
                  const isDefault = option.id === defaultAgent
                  return (
                    <button
                      key={option.id}
                      type="button"
                      onClick={() => handlePresetClick(option.id)}
                      className={cn(
                        'relative flex flex-col items-center gap-2 rounded-lg border p-3 transition-all duration-200 group',
                        active 
                          ? 'border-primary bg-primary/5' 
                          : 'border-hairline bg-surface-elevated/30 hover:border-hairline/60 grayscale opacity-70 hover:grayscale-0 hover:opacity-100'
                      )}
                    >
                      <span
                        role="button"
                        aria-label={isDefault ? 'Default agent' : 'Set as default'}
                        title={isDefault ? 'Default agent' : 'Set as default'}
                        onClick={e => { e.stopPropagation(); updatePrefs({ default_agent: option.id }) }}
                        className={cn(
                          'absolute top-1.5 right-1.5 w-4 h-4 flex items-center justify-center transition-opacity cursor-pointer',
                          isDefault ? 'opacity-100' : 'opacity-0 group-hover:opacity-60 hover:!opacity-100'
                        )}
                      >
                        <svg viewBox="0 0 16 16" className={cn('w-3 h-3', isDefault ? 'fill-amber-400 text-amber-400' : 'fill-none text-mute stroke-current')} strokeWidth={isDefault ? 0 : 1.5}>
                          <polygon points="8,1.5 10,6 15,6.5 11.5,10 12.5,15 8,12.5 3.5,15 4.5,10 1,6.5 6,6" />
                        </svg>
                      </span>
                      <AgentMark agentType={option.id} className="h-6 min-w-10 px-2 shrink-0" />
                      <span className={cn(
                        'text-xs font-bold uppercase tracking-tight',
                        active ? 'text-primary' : 'text-mute'
                      )}>{option.label}</span>
                    </button>
                  )
                })}
              </div>
              <input
                value={command}
                onChange={e => { setCommand(e.target.value); setPreset(null) }}
                onKeyDown={handleKeyDown}
                placeholder="shell command..."
                className="mt-3 w-full text-[13px] text-ink bg-surface-elevated border border-hairline rounded-sm px-3 py-2 outline-none font-mono placeholder:text-mute/40 focus:border-primary/60 transition-colors"
              />
            </div>
            <div>
              <div className="text-xs font-bold text-mute/60 uppercase tracking-wider mb-2 ml-1">Session Name</div>
              <input
                value={name}
                onChange={e => setName(e.target.value)}
                onKeyDown={handleKeyDown}
                placeholder={suggestedName || 'Automatic name...'}
                className="w-full text-[14px] text-ink bg-surface-elevated border border-hairline rounded-sm px-3 py-2 outline-none font-sans font-medium placeholder:text-mute/40 focus:border-primary/60 transition-colors"
              />
            </div>
          </div>
          {showHostSelect && (
            <div className="mt-4">
              <div className="text-xs font-bold text-mute/60 uppercase tracking-wider mb-2 ml-1">Host</div>
              <HostSelect
                value={selectedHost}
                onChange={setSelectedHost}
                options={onlineHosts.map(h => ({ value: h.id, label: `${h.name}${h.local ? ' (LOCAL)' : ''}` }))}
                className="w-full text-[13px] font-bold text-ink bg-surface-elevated border border-hairline rounded-sm px-3 py-2 outline-none focus:border-primary/60 transition-colors cursor-pointer"
              />
            </div>
          )}
        </div>
        <div className="py-4 px-6 border-t border-hairline bg-surface-elevated/10 flex justify-between items-center">
          <div className="flex items-center gap-4 text-xs font-bold uppercase tracking-widest text-mute/40">
             <div className="flex items-center gap-1.5">
               <span className="px-1.5 py-0.5 rounded-xs border border-hairline bg-surface font-mono text-[9px]">↵</span>
               <span>Create</span>
             </div>
             <div className="flex items-center gap-1.5">
               <span className="px-1.5 py-0.5 rounded-xs border border-hairline bg-surface font-mono text-[9px]">ESC</span>
               <span>Cancel</span>
             </div>
          </div>
          <div className="flex gap-3">
            <button
              onClick={handleSubmit}
              disabled={!(name.trim() || suggestedName) || !resolvedCommand || (worktreeMode && !worktreeBranch.trim())}
              className="px-6 py-2 rounded-full text-[13px] font-bold uppercase tracking-widest bg-primary text-primary-foreground hover:bg-white/90 transition-all disabled:opacity-30 disabled:cursor-not-allowed"
            >
              Create
            </button>
          </div>
        </div>
      </div>
    </div>
  )
}

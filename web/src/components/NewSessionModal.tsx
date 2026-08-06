import { useState, useEffect, useMemo, useRef } from 'react'
import type { HostSnapshot } from '../state/session/wireTypes'
import type { SessionView } from '../state/session/viewModel'
type Host = HostSnapshot
import { usePreferences } from '../hooks/usePreferences'
import { cn } from '../lib/utils'
import { AgentMark } from './AgentMark'
import { HostSelect } from './HostSelect'

// NewSessionInput is the single typed contract for creating a session from
// the New Session modal. targetOwner is already the canonical OwnerID
// (HostInfo.OwnerID / Host.owner_id) -- HostSelect's option values are
// OwnerIDs directly (see below), so no fingerprint-to-owner translation is
// needed anywhere along this path.
export interface NewSessionInput {
  name?: string
  cwd: string
  shell?: string
  targetOwner?: string
  worktreeBranch?: string
  agentType?: string
}

interface NewSessionModalProps {
  hosts: Host[]
  sessions: SessionView[]
  // Resolves (void) on success, rejects (Error) on failure -- never a string
  // that carries dual success/error meaning. Callers must not swallow the
  // rejection; NewSessionModal catches it itself and renders the message.
  onCreateSession: (input: NewSessionInput) => Promise<void>
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
  const [error, setError] = useState<string | null>(null)
  const onlineHosts = hosts.filter(h => h.online)
  const localHost = onlineHosts.find(h => h.local)
  // HostSelect option values are canonical OwnerIDs, matching SessionView.ownerId's
  // encoding directly -- a host with no owner_id has no v2 identity and
  // cannot be a create target (no canonical OwnerID).
  const selectableHosts = useMemo(() => onlineHosts.filter(h => h.owner_id), [onlineHosts])
  const showHostSelect = selectableHosts.length > 1
  const [selectedHost, setSelectedHost] = useState<string>(localHost?.owner_id || '')
  const pathInputRef = useRef<HTMLInputElement>(null)
  const [dropdownOpen, setDropdownOpen] = useState(false)
  const [highlightedIndex, setHighlightedIndex] = useState(-1)
  const containerRef = useRef<HTMLDivElement>(null)
  const dropdownOpenRef = useRef(dropdownOpen)
  dropdownOpenRef.current = dropdownOpen
  const resolvedCommand = command.trim()

  // suggestedName is display-only input assistance (a placeholder shown when
  // the user hasn't typed a name, and the fallback actually sent if they
  // never do). It is derived purely from the path/branch -- the backend
  // already owns unique display-name selection, so this never checks
  // existing session names for collisions.
  const suggestedName = useMemo(() => {
    const leaf = basename(path || '~')
    if (!leaf) return ''
    const branch = worktreeMode && worktreeBranch.trim()
      ? worktreeBranch.trim().replace(/\//g, '-')
      : ''
    return branch ? `${leaf}-${branch}` : leaf
  }, [path, worktreeMode, worktreeBranch])

  interface RecentLocation {
    path: string
    ownerId: string   // value to assign to selectedHost -- already a canonical OwnerID
    hostName: string
    local: boolean
  }

  const recentLocations = useMemo<RecentLocation[]>(() => {
    const localOwnerId = localHost?.owner_id || ''
    const onlineOwnerIds = new Set(
      selectableHosts.map(h => h.owner_id as string).concat(localOwnerId ? [localOwnerId] : []),
    )
    const hostNameByOwnerId = new Map(
      selectableHosts.map(h => [h.owner_id as string, h.name]),
    )
    const seen = new Set<string>()
    const sorted = [...sessions]
      .filter(s => s.cwd && s.cwd.trim())
      .sort((a, b) => new Date(b.createdAt).getTime() - new Date(a.createdAt).getTime())
    const unique: RecentLocation[] = []
    for (const s of sorted) {
      const p = s.cwd!
      const local = s.isLocal || (!!localOwnerId && s.ownerId === localOwnerId)
      const ownerId = local ? localOwnerId : s.ownerId
      // Skip locations whose host is offline/unknown (cannot create there)
      if (!onlineOwnerIds.has(ownerId)) continue
      const key = `${ownerId}::${p}`
      if (seen.has(key)) continue
      seen.add(key)
      unique.push({
        path: p,
        ownerId,
        hostName: local
          ? (localHost?.name || s.host?.name || 'Local')
          : (hostNameByOwnerId.get(ownerId) || s.host?.name || ownerId),
        local,
      })
      if (unique.length >= 10) break
    }
    return unique
  }, [sessions, selectableHosts, localHost])

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
    setSelectedHost(loc.ownerId)
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
    const trimmedName = name.trim() || suggestedName || undefined
    setError(null)
    try {
      await onCreateSession({
        name: trimmedName,
        cwd: trimmedPath,
        shell: resolvedCommand || undefined,
        targetOwner: selectedHost || undefined,
        worktreeBranch: worktreeMode ? worktreeBranch.trim() || undefined : undefined,
        agentType: preset || undefined,
      })
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
    }
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
                  data-testid="ns-path"
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
                        key={`${loc.ownerId}::${loc.path}`}
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
                <input
                  data-testid="ns-worktree-branch"
                  value={worktreeBranch}
                  onChange={e => { setWorktreeBranch(e.target.value); setError(null) }}
                  onKeyDown={handleKeyDown}
                  placeholder="branch-name"
                  className="mt-2 w-full text-[13px] text-ink bg-surface-elevated border border-hairline rounded-sm px-3 py-2 outline-none font-mono placeholder:text-mute/40 focus:border-primary/60 transition-colors"
                />
              )}
              {error && (
                <div className="mt-1.5 text-xs text-red-400 font-mono break-all">{error}</div>
              )}
            </div>
            <div>
              <div className="text-xs font-bold text-mute/60 uppercase tracking-wider mb-2 ml-1">Agent</div>
              <div className="grid grid-cols-3 gap-2">
                {presets.map(option => {
                  const active = preset === option.id
                  return (
                    <button
                      key={option.id}
                      onClick={() => { handlePresetClick(option.id); void updatePrefs({ default_agent: option.id }) }}
                      className={cn(
                        'flex items-center gap-1.5 justify-center px-2 py-2 rounded-sm border text-[12px] font-bold uppercase tracking-wide transition-colors',
                        active
                          ? 'border-primary/60 bg-primary/10 text-primary'
                          : 'border-hairline text-mute/60 hover:text-ink hover:border-mute/40'
                      )}
                    >
                      <AgentMark agentType={option.id} className="w-3.5 h-3.5 shrink-0" />
                      {option.label}
                    </button>
                  )
                })}
              </div>
              <input
                data-testid="ns-command"
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
                data-testid="ns-name"
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
                options={selectableHosts.map(h => ({ value: h.owner_id as string, label: `${h.name}${h.local ? ' (LOCAL)' : ''}` }))}
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

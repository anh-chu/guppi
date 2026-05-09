import { useState, useEffect, useMemo, useRef } from 'react'
import { Host } from '../hooks/useHosts'
import { Session } from '../hooks/useSessions'
import { cn } from '../lib/utils'
import { AgentMark } from './AgentMark'

interface NewSessionModalProps {
  hosts: Host[]
  sessions: Session[]
  onCreateSession: (name: string, path: string, command: string, hostId?: string) => void
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
  const parts = trimmed.split(/[\\/]/)
  return parts[parts.length - 1] || ''
}

export function NewSessionModal({ hosts, sessions, onCreateSession, onClose }: NewSessionModalProps) {
  const [name, setName] = useState('')
  const [path, setPath] = useState('')
  const [preset, setPreset] = useState<string | null>('claude')
  const [command, setCommand] = useState('claude')
  const onlineHosts = hosts.filter(h => h.online)
  const showHostSelect = onlineHosts.length > 1
  const localHost = onlineHosts.find(h => h.local)
  const [selectedHost, setSelectedHost] = useState<string>(localHost?.id || '')
  const pathInputRef = useRef<HTMLInputElement>(null)
  const resolvedCommand = command.trim()
  const existingNames = useMemo(() => {
    return new Set(
      sessions
        .filter(session => (selectedHost ? session.host === selectedHost : !session.host))
        .map(session => session.name),
    )
  }, [selectedHost, sessions])
  const uniqueSessionName = (value: string) => {
    const trimmed = value.trim()
    if (!trimmed) return ''
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
    return uniqueSessionName(`${preset || 'session'}-${leaf}`)
  }, [path, preset, existingNames])

  const handlePresetClick = (id: string) => {
    if (preset === id) {
      setPreset(null)
      setCommand('')
    } else {
      setPreset(id)
      setCommand(presets.find(p => p.id === id)?.command || '')
    }
  }

  useEffect(() => {
    pathInputRef.current?.focus()
  }, [])

  useEffect(() => {
    const handler = (e: KeyboardEvent) => {
      if (e.key === 'Escape') {
        e.preventDefault()
        e.stopImmediatePropagation()
        onClose()
      }
    }
    window.addEventListener('keydown', handler, true)
    return () => window.removeEventListener('keydown', handler, true)
  }, [onClose])

  const handleSubmit = () => {
    const trimmedPath = path.trim() || '~'
    const trimmedName = uniqueSessionName(name.trim() || suggestedName)
    if (!trimmedName) return
    onCreateSession(trimmedName, trimmedPath, resolvedCommand, selectedHost || undefined)
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
              <input
                ref={pathInputRef}
                value={path}
                onChange={e => setPath(e.target.value)}
                onKeyDown={handleKeyDown}
                placeholder="~"
                className="w-full text-[14px] text-ink bg-surface-elevated border border-hairline rounded-sm px-3 py-2 outline-none font-sans font-medium placeholder:text-mute/40 focus:border-primary/60 transition-colors"
              />
            </div>
            <div>
              <div className="text-xs font-bold text-mute/60 uppercase tracking-wider mb-2 ml-1">Agent</div>
              <div className="grid grid-cols-3 gap-2">
                {presets.map(option => {
                  const active = option.id === preset
                  return (
                    <button
                      key={option.id}
                      type="button"
                      onClick={() => handlePresetClick(option.id)}
                      className={cn(
                        'flex flex-col items-center gap-2 rounded-lg border p-3 transition-all duration-200',
                        active 
                          ? 'border-primary bg-primary/5' 
                          : 'border-hairline bg-surface-elevated/30 hover:border-hairline/60 grayscale opacity-70 hover:grayscale-0 hover:opacity-100'
                      )}
                    >
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
              <select
                value={selectedHost}
                onChange={e => setSelectedHost(e.target.value)}
                className="w-full text-[13px] font-bold text-ink bg-surface-elevated border border-hairline rounded-sm px-3 py-2 outline-none focus:border-primary/60 transition-colors cursor-pointer"
              >
                {onlineHosts.map(h => (
                  <option key={h.id} value={h.id}>
                    {h.name}{h.local ? ' (LOCAL)' : ''}
                  </option>
                ))}
              </select>
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
              disabled={!(name.trim() || suggestedName) || !resolvedCommand}
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

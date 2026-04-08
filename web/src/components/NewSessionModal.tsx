import { useState, useEffect, useMemo, useRef } from 'react'
import { Host } from '../hooks/useHosts'
import { AgentMark } from './AgentMark'

interface NewSessionModalProps {
  hosts: Host[]
  onCreateSession: (name: string, path: string, command: string, hostId?: string) => void
  onClose: () => void
}

const presets = [
  { id: 'claude', label: 'Claude', command: 'claude' },
  { id: 'codex', label: 'Codex', command: 'codex' },
  { id: 'gemini', label: 'Gemini', command: 'gemini' },
  { id: 'copilot', label: 'Copilot', command: 'copilot' },
  { id: 'opencode', label: 'OpenCode', command: 'opencode' },
  { id: 'custom', label: 'Custom', command: '' },
]

function basename(value: string): string {
  const trimmed = value.trim().replace(/[\\/]+$/, '')
  if (!trimmed) return ''
  const parts = trimmed.split(/[\\/]/)
  return parts[parts.length - 1] || ''
}

export function NewSessionModal({ hosts, onCreateSession, onClose }: NewSessionModalProps) {
  const [name, setName] = useState('')
  const [path, setPath] = useState('')
  const [preset, setPreset] = useState('codex')
  const [customCommand, setCustomCommand] = useState('')
  const onlineHosts = hosts.filter(h => h.online)
  const showHostSelect = onlineHosts.length > 1
  const localHost = onlineHosts.find(h => h.local)
  const [selectedHost, setSelectedHost] = useState<string>(localHost?.id || '')
  const pathInputRef = useRef<HTMLInputElement>(null)
  const resolvedCommand = useMemo(() => {
    if (preset === 'custom') return customCommand.trim()
    return presets.find(p => p.id === preset)?.command || ''
  }, [preset, customCommand])
  const suggestedName = useMemo(() => {
    const leaf = basename(path)
    if (!leaf) return ''
    return `${preset === 'custom' ? 'session' : preset}-${leaf}`
  }, [path, preset])

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
    const trimmedPath = path.trim()
    const trimmedName = name.trim() || suggestedName
    if (!trimmedPath || !trimmedName || !resolvedCommand) return
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
      className="fixed inset-0 z-[9999] flex items-start justify-center pt-[20vh] bg-black/50"
      onClick={onClose}
    >
      <div
        className="w-[400px] bg-card border border-border rounded-xl shadow-2xl flex flex-col overflow-hidden"
        onClick={e => e.stopPropagation()}
      >
        <div className="p-4 border-b border-border">
          <div className="text-sm text-foreground font-semibold mb-3">New Session</div>
          <div className="space-y-3">
            <div>
              <div className="text-xs text-muted-foreground mb-1.5">Location</div>
              <input
                ref={pathInputRef}
                value={path}
                onChange={e => setPath(e.target.value)}
                onKeyDown={handleKeyDown}
                placeholder="/path/to/project"
                className="w-full text-[15px] text-foreground bg-input border border-border rounded px-3 py-1.5 outline-none font-mono placeholder:text-muted-foreground focus:border-primary"
              />
            </div>
            <div>
              <div className="text-xs text-muted-foreground mb-1.5">Agent</div>
              <div className="grid grid-cols-3 gap-2">
                {presets.map(option => {
                  const active = option.id === preset
                  return (
                    <button
                      key={option.id}
                      type="button"
                      onClick={() => setPreset(option.id)}
                      className={`flex items-center gap-2 rounded border px-2 py-2 text-xs transition-colors ${active ? 'border-primary bg-primary/15 text-foreground' : 'border-border text-muted-foreground hover:text-foreground hover:border-primary/40'}`}
                    >
                      <AgentMark agentType={option.id} className="h-5 min-w-8 px-1.5" />
                      <span>{option.label}</span>
                    </button>
                  )
                })}
              </div>
              {preset === 'custom' && (
                <input
                  value={customCommand}
                  onChange={e => setCustomCommand(e.target.value)}
                  onKeyDown={handleKeyDown}
                  placeholder="custom command..."
                  className="mt-2 w-full text-[14px] text-foreground bg-input border border-border rounded px-3 py-1.5 outline-none font-mono placeholder:text-muted-foreground focus:border-primary"
                />
              )}
            </div>
            <div>
              <div className="text-xs text-muted-foreground mb-1.5">Session Name</div>
              <input
                value={name}
                onChange={e => setName(e.target.value)}
                onKeyDown={handleKeyDown}
                placeholder={suggestedName || 'auto-generated'}
                className="w-full text-[15px] text-foreground bg-input border border-border rounded px-3 py-1.5 outline-none font-mono placeholder:text-muted-foreground focus:border-primary"
              />
            </div>
          </div>
          {showHostSelect && (
            <div className="mt-3">
              <div className="text-xs text-muted-foreground mb-1.5">Host</div>
              <select
                value={selectedHost}
                onChange={e => setSelectedHost(e.target.value)}
                className="w-full text-sm text-foreground bg-input border border-border rounded px-3 py-1.5 outline-none focus:border-primary"
              >
                {onlineHosts.map(h => (
                  <option key={h.id} value={h.id}>
                    {h.name}{h.local ? ' (local)' : ''}
                  </option>
                ))}
              </select>
            </div>
          )}
        </div>
        <div className="py-2 px-4 border-t border-border flex justify-between items-center">
          <span className="text-[11px] text-muted-foreground">↵ create &nbsp; esc cancel</span>
          <div className="flex gap-2">
            <button
              onClick={onClose}
              className="text-xs text-muted-foreground hover:text-foreground px-3 py-1 rounded transition-colors"
            >
              Cancel
            </button>
            <button
              onClick={handleSubmit}
              disabled={!path.trim() || !resolvedCommand || !(name.trim() || suggestedName)}
              className="text-xs text-foreground bg-primary/20 hover:bg-primary/30 border border-primary/40 px-3 py-1 rounded transition-colors disabled:opacity-40 disabled:cursor-not-allowed"
            >
              Create
            </button>
          </div>
        </div>
      </div>
    </div>
  )
}

import { useEffect, useRef, useState } from 'react'
import { renameSession, aiNameSession, killSession } from '../lib/sessionActions'

// Self-contained right-click menu for a single session. Owns the menu UI and
// its confirm/rename state; the actual API calls live in lib/sessionActions so
// the Sidebar context menu shares them.

export type SessionMenuTarget = {
  key: string
  id: string
  name: string
  label: string
  host?: string
  isWorktree: boolean
}

export function SessionActionsMenu({
  target,
  x,
  y,
  hiddenSet,
  backgroundSet,
  setSessionAttr,
  onSessionKilled,
  onClose,
  v2Mode,
  onRenameSession,
}: {
  target: SessionMenuTarget
  x: number
  y: number
  hiddenSet: Set<string>
  backgroundSet: Set<string>
  setSessionAttr: (key: string, next: { background?: boolean; hidden?: boolean }) => void
  onSessionKilled?: (key: string) => void
  onClose: () => void
  // When true, this menu is rendered under the v2 (AppV2) state path: legacy
  // REST session-action routes (kill/rename/AI-rename) must not also fire
  // alongside the v2 callback, and features with no v2 equivalent yet
  // (AI rename, hide, background, worktree removal) are hidden.
  v2Mode?: boolean
  onRenameSession?: (key: string, label: string) => void
}) {
  const menuRef = useRef<HTMLDivElement>(null)
  const renameInputRef = useRef<HTMLInputElement>(null)
  const [renaming, setRenaming] = useState(false)
  const [renameValue, setRenameValue] = useState(target.label)
  const [confirmKill, setConfirmKill] = useState(false)
  const [confirmWorktreeKill, setConfirmWorktreeKill] = useState(false)

  useEffect(() => {
    const onDoc = (e: MouseEvent) => {
      if (menuRef.current && !menuRef.current.contains(e.target as Node)) onClose()
    }
    document.addEventListener('mousedown', onDoc)
    return () => document.removeEventListener('mousedown', onDoc)
  }, [onClose])

  useEffect(() => {
    if (renaming && renameInputRef.current) {
      renameInputRef.current.focus()
      renameInputRef.current.select()
    }
  }, [renaming])

  const submitRename = () => {
    const next = renameValue.trim()
    if (next && next !== target.label) {
      if (v2Mode) onRenameSession?.(target.key, next)
      else renameSession(target.name, next, target.host)
    }
    onClose()
  }

  const aiName = () => {
    onClose()
    aiNameSession(target.name, target.host)
  }

  const kill = (removeWorktree: boolean) => {
    onClose()
    onSessionKilled?.(target.key)
    // In v2 mode, onSessionKilled already IS the real kill (routed through
    // v2State.sessionCommand). Don't also fire the legacy REST route.
    if (!v2Mode) killSession(target.id, target.name, target.host, removeWorktree)
  }

  const item = 'px-3 py-1.5 text-sm text-ink cursor-pointer hover:bg-surface-card hover:text-ink'

  return (
    <div
      ref={menuRef}
      className="fixed bg-surface-elevated border border-hairline rounded-md py-1 z-[1000] min-w-[160px]"
      style={{ left: x, top: y }}
      onClick={(e) => e.stopPropagation()}
    >
      {renaming ? (
        <input
          ref={renameInputRef}
          value={renameValue}
          onChange={(e) => setRenameValue(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === 'Enter') submitRename()
            if (e.key === 'Escape') onClose()
          }}
          onBlur={submitRename}
          className="mx-2 my-1 w-[calc(100%-1rem)] text-sm text-ink bg-surface-card border border-primary rounded-sm px-1.5 py-0.5 outline-none font-sans font-medium"
        />
      ) : (
        <>
          <div className={item} onClick={() => setRenaming(true)}>Rename</div>
          {/* AI rename has no v2 equivalent (POST /api/group/name is legacy-only). */}
          {!v2Mode && <div className={item} onClick={aiName}>AI rename</div>}
        </>
      )}
      {/* Hide/Background are server-authoritative session attrs v2 does not
          support yet — hide rather than show a click with no durable effect. */}
      {!v2Mode && (
        <div className={item} onClick={() => { setSessionAttr(target.key, { hidden: !hiddenSet.has(target.key) }); onClose() }}>
          {hiddenSet.has(target.key) ? 'Unhide' : 'Hide'}
        </div>
      )}
      {!v2Mode && (
        <div className={item} onClick={() => { setSessionAttr(target.key, { background: !backgroundSet.has(target.key) }); onClose() }}>
          {backgroundSet.has(target.key) ? 'Foreground' : 'Background'}
        </div>
      )}
      <div className="my-1 border-t border-hairline" />
      <div
        className="px-3 py-1.5 text-sm cursor-pointer text-red-400 hover:bg-red-500/10"
        onClick={() => { if (confirmKill) kill(false); else setConfirmKill(true) }}
      >
        {confirmKill ? 'Confirm kill?' : 'Kill'}
      </div>
      {/* Worktree removal has no v2 route — hide rather than fire a legacy call
          that bypasses v2 authority. */}
      {target.isWorktree && !v2Mode && (
        <div
          className="px-3 py-1.5 text-sm cursor-pointer text-red-400 hover:bg-red-500/10"
          onClick={() => { if (confirmWorktreeKill) kill(true); else setConfirmWorktreeKill(true) }}
        >
          {confirmWorktreeKill ? 'Confirm remove worktree?' : 'Kill + remove worktree'}
        </div>
      )}
    </div>
  )
}

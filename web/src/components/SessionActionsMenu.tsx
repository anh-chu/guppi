import { useEffect, useRef, useState } from 'react'

// Self-contained right-click menu for a single session. Owns the menu UI and
// its confirm/rename state; kill/rename route through the canonical v2
// session-command callbacks passed in as props.

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
  onRenameSession?: (key: string, label: string) => void
}) {
  const menuRef = useRef<HTMLDivElement>(null)
  const renameInputRef = useRef<HTMLInputElement>(null)
  const [renaming, setRenaming] = useState(false)
  const [renameValue, setRenameValue] = useState(target.label)
  const [confirmKill, setConfirmKill] = useState(false)

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
      onRenameSession?.(target.key, next)
    }
    onClose()
  }

  const kill = () => {
    onClose()
    onSessionKilled?.(target.key)
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
        </>
      )}
      {/* Hide/Background: server-authoritative session attrs, dispatched via
          the session command's `set_presentation` action
          (ActionSetPresentation) -- see SessionApp.tsx's wiring. */}
      <div className={item} onClick={() => { setSessionAttr(target.key, { hidden: !hiddenSet.has(target.key) }); onClose() }}>
        {hiddenSet.has(target.key) ? 'Unhide' : 'Hide'}
      </div>
      <div className={item} onClick={() => { setSessionAttr(target.key, { background: !backgroundSet.has(target.key) }); onClose() }}>
        {backgroundSet.has(target.key) ? 'Foreground' : 'Background'}
      </div>
      <div className="my-1 border-t border-hairline" />
      <div
        className="px-3 py-1.5 text-sm cursor-pointer text-red-400 hover:bg-red-500/10"
        onClick={() => { if (confirmKill) kill(); else setConfirmKill(true) }}
      >
        {confirmKill ? 'Confirm kill?' : 'Kill'}
      </div>
    </div>
  )
}

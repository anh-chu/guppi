import { useRef, useLayoutEffect, useCallback } from 'react'
import type { RefObject } from 'react'

interface SelectionMenuProps {
  selectionMenu: { x: number; y: number; text: string } | null
  onClose: () => void
  onCopy: () => void
  onOpenFile: () => void
  'aria-label'?: string
  terminalRef?: RefObject<HTMLElement | null>
}

export function SelectionMenu({
  selectionMenu,
  onClose,
  onCopy,
  onOpenFile,
  'aria-label': ariaLabel,
  terminalRef,
}: SelectionMenuProps) {
  const menuRef = useRef<HTMLDivElement>(null)

  const handleClose = useCallback(() => {
    onClose()
    // Return focus to the terminal container so keyboard input resumes.
    const el = terminalRef?.current
    if (el && 'focus' in el && typeof el.focus === 'function') {
      el.focus()
    }
  }, [onClose, terminalRef])

  useLayoutEffect(() => {
    const first = menuRef.current?.querySelector<HTMLButtonElement>('button[role="menuitem"]')
    first?.focus()
  }, [])

  useLayoutEffect(() => {
    const onKeyDown = (e: KeyboardEvent) => {
      if (e.key === 'Escape') {
        e.stopPropagation()
        handleClose()
      }
    }
    window.addEventListener('keydown', onKeyDown)
    return () => window.removeEventListener('keydown', onKeyDown)
  }, [handleClose])

  if (!selectionMenu) return null

  return (
    <>
      <div
        className="fixed inset-0 z-40"
        onMouseDown={handleClose}
        onContextMenu={(e) => { e.preventDefault(); handleClose() }}
      />
      {/* ponytail: no edge-clamp; add if menus near viewport edge get clipped */}
      <div
        ref={menuRef}
        role="menu"
        aria-label={ariaLabel}
        className="fixed z-50 min-w-[140px] bg-surface-elevated border border-hairline rounded-md flex flex-col overflow-hidden shadow-lg"
        style={{ left: selectionMenu.x, top: selectionMenu.y }}
      >
        <button
          type="button"
          role="menuitem"
          onMouseDown={(e) => e.preventDefault()}
          onClick={() => { onCopy(); handleClose() }}
          className="px-4 py-2.5 text-left text-xs font-medium hover:bg-surface transition-colors"
        >
          Copy
        </button>
        {/^\S+$/.test(selectionMenu.text.trim()) && (
          <button
            type="button"
            role="menuitem"
            onMouseDown={(e) => e.preventDefault()}
            onClick={() => { onOpenFile(); handleClose() }}
            className="px-4 py-2.5 text-left text-xs font-medium hover:bg-surface transition-colors border-t border-hairline"
          >
            Open file
          </button>
        )}
      </div>
    </>
  )
}

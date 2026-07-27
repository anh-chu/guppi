import { useEffect, useRef, useState } from 'react'
import type { ReactNode } from 'react'
import { cn } from '../lib/utils'

type HeaderOverflowProps = {
  onPortForwards?: () => void
  onSchedules?: () => void
  onWiki?: () => void
  onHelp?: () => void
}

function Icon({ children }: { children: ReactNode }) {
  return <span className="inline-flex w-4 h-4 items-center justify-center">{children}</span>
}

export function HeaderOverflow({ onPortForwards, onSchedules, onWiki, onHelp }: HeaderOverflowProps) {
  const [open, setOpen] = useState(false)
  const rootRef = useRef<HTMLDivElement>(null)

  useEffect(() => {
    const onDown = (e: MouseEvent) => {
      if (!rootRef.current?.contains(e.target as Node)) setOpen(false)
    }
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') setOpen(false)
    }
    document.addEventListener('mousedown', onDown)
    document.addEventListener('keydown', onKey)
    return () => {
      document.removeEventListener('mousedown', onDown)
      document.removeEventListener('keydown', onKey)
    }
  }, [])

  const items = [
    onPortForwards && {
      key: 'ports',
      label: 'Port forwards',
      action: onPortForwards,
    },
    onSchedules && {
      key: 'schedules',
      label: 'Schedules',
      action: onSchedules,
    },
    onWiki && {
      key: 'wiki',
      label: 'Wiki panel',
      action: onWiki,
    },
    onHelp && {
      key: 'help',
      label: 'Keyboard shortcuts',
      action: onHelp,
    },
  ].filter(Boolean) as Array<{ key: string; label: string; action: () => void }>

  return (
    <div ref={rootRef} className="relative">
      <button
        type="button"
        onClick={() => setOpen(v => !v)}
        title="More actions"
        className={cn('p-1.5 rounded-sm hover:bg-surface-elevated text-ink transition-colors', open && 'bg-surface-elevated')}
      >
        <Icon>⋯</Icon>
      </button>
      {open && (
        <div className="absolute right-0 top-full mt-2 z-50 min-w-48 rounded-md border border-hairline bg-surface-elevated shadow-xl overflow-hidden">
          {items.map(item => (
            <button
              key={item.key}
              type="button"
              onClick={() => { item.action(); setOpen(false) }}
              className="flex w-full items-center gap-2 px-3 py-2 text-left text-[13px] text-ink hover:bg-surface transition-colors"
            >
              <span className="text-mute">•</span>
              <span>{item.label}</span>
            </button>
          ))}
        </div>
      )}
    </div>
  )
}

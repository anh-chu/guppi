import { cn } from '../../lib/utils'

function Section({ id, title, description, children, hidden }: { id: string; title: string; description?: string; children: React.ReactNode; hidden?: boolean }) {
  return (
    <section id={id} className={cn('rounded-lg border border-hairline bg-surface p-6 scroll-mt-6', hidden && 'hidden')}>
      <h3 className="font-display text-[13px] font-bold text-ink mb-1">{title}</h3>
      {description && <p className="text-xs font-medium text-mute/60 mb-5">{description}</p>}
      {!description && <div className="mb-5" />}
      <div className="flex flex-col gap-4">
        {children}
      </div>
    </section>
  )
}

function Divider() {
  return <div className="border-t border-hairline/40 -mx-6 my-1" />
}

function Row({ label, description, children }: { label: string; description?: string; children: React.ReactNode }) {
  return (
    <div className="flex flex-wrap items-center justify-between gap-x-6 gap-y-2 py-1">
      <div className="flex-1 min-w-[140px]">
        <div className="text-[13px] font-semibold text-ink tracking-tight">{label}</div>
        {description && <div className="text-xs font-medium text-mute/50 mt-1">{description}</div>}
      </div>
      <div className="shrink-0">
        {children}
      </div>
    </div>
  )
}

function SelectInput({ value, onChange, options }: { value: string; onChange: (v: string) => void; options: { value: string; label: string }[] }) {
  return (
    <select
      value={value}
      onChange={(e) => onChange(e.target.value)}
      className="bg-surface-elevated border border-hairline rounded-sm px-3 py-1.5 text-[13px] font-medium text-ink outline-none focus:border-primary/60 min-w-[180px] transition-colors cursor-pointer"
    >
      {options.map(o => (
        <option key={o.value} value={o.value}>{o.label}</option>
      ))}
    </select>
  )
}

function NumberInput({ value, onChange, min, max, step }: { value: number; onChange: (v: number) => void; min?: number; max?: number; step?: number }) {
  return (
    <input
      type="number"
      value={value}
      onChange={(e) => onChange(Number(e.target.value))}
      min={min}
      max={max}
      step={step}
      className="bg-surface-elevated border border-hairline rounded-sm px-3 py-1.5 text-[13px] font-medium text-ink outline-none focus:border-primary/60 w-[80px] text-right"
    />
  )
}

function TextInput({ value, onChange, placeholder, type = 'text', wide }: { value: string; onChange: (v: string) => void; placeholder?: string; type?: string; wide?: boolean }) {
  return (
    <input
      type={type}
      value={value}
      onChange={(e) => onChange(e.target.value)}
      placeholder={placeholder}
      autoComplete="off"
      spellCheck={false}
      className={cn(
        'bg-surface-elevated border border-hairline rounded-sm px-3 py-1.5 text-[13px] font-medium text-ink outline-none focus:border-primary/60 transition-colors',
        wide ? 'min-w-[280px]' : 'min-w-[180px]',
      )}
    />
  )
}

function ColorInput({ value, onChange }: { value: string; onChange: (v: string) => void }) {
  const swatchValue = /^#([0-9a-fA-F]{3}|[0-9a-fA-F]{6})$/.test(value) ? value : '#000000'
  return (
    <div className="flex items-center gap-2">
      <input
        type="color"
        value={swatchValue}
        onChange={(e) => onChange(e.target.value)}
        className="h-7 w-7 rounded-sm border border-hairline bg-surface-elevated cursor-pointer p-0"
      />
      <input
        type="text"
        value={value}
        onChange={(e) => onChange(e.target.value)}
        autoComplete="off"
        spellCheck={false}
        className="bg-surface-elevated border border-hairline rounded-sm px-2 py-1 text-[12px] font-mono text-ink outline-none focus:border-primary/60 w-[92px]"
      />
    </div>
  )
}

function Toggle({ checked, onChange, label }: { checked: boolean; onChange: (v: boolean) => void; label?: string }) {
  return (
    <div className="flex items-center gap-3">
      <button
        type="button"
        role="switch"
        aria-checked={checked}
        onClick={() => onChange(!checked)}
        className={cn(
          'inline-flex h-5 w-9 shrink-0 items-center rounded-full border border-transparent transition-all duration-200',
          checked ? 'bg-primary' : 'bg-surface-elevated border-hairline',
        )}
      >
        <span
          className={cn(
            'pointer-events-none block h-3.5 w-3.5 rounded-full transition-transform duration-200 mx-0.5',
            checked ? 'translate-x-4 bg-primary-foreground' : 'translate-x-0 bg-muted-foreground/60',
          )}
        />
      </button>
      {label && <span className="text-xs font-bold uppercase tracking-wider text-mute/60">{label}</span>}
    </div>
  )
}

function Kbd({ children }: { children: string }) {
  return (
    <kbd className="inline-flex items-center justify-center min-w-[28px] h-6 px-1.5 rounded-xs border border-hairline bg-gradient-to-b from-[#121212] to-[#0d0d0d] text-mute text-xs font-mono font-bold">
      {children}
    </kbd>
  )
}

export { Section, Divider, Row, SelectInput, NumberInput, TextInput, Toggle, Kbd, ColorInput }

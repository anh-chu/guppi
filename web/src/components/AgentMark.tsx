import { cn } from '../lib/utils'
import { toolColors } from '../theme'

const agentLabels: Record<string, string> = {
  claude: 'Cl',
  codex: 'Cx',
  gemini: 'Ge',
  copilot: 'Cp',
  opencode: 'Oc',
  custom: '*>',
}

function normalizeAgent(agentType?: string) {
  const key = (agentType || '').toLowerCase()
  if (key in agentLabels) return key
  return 'custom'
}

export function AgentMark({ agentType, className }: { agentType?: string; className?: string }) {
  const key = normalizeAgent(agentType)
  const color = toolColors[key] || 'var(--primary)'

  return (
    <span
      title={agentType || 'custom'}
      className={cn(
        'inline-flex items-center justify-center rounded-md border text-[10px] font-semibold tracking-wide',
        className,
      )}
      style={{
        color,
        borderColor: `${color}55`,
        background: `${color}18`,
      }}
    >
      {agentLabels[key]}
    </span>
  )
}

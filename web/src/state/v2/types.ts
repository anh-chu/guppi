/**
 * Lean v2 canonical state types.
 *
 * These types mirror pkg/state/ids.go and pkg/state/document.go. They are a
 * design-freeze artifact; runtime code still uses the legacy types in
 * src/hooks/useSessions and src/state/workspaceReducer.
 */

export type OwnerID = string
export type SessionID = string
export type LayoutID = string
export type SplitID = string
export type CommandID = string

export const SCHEMA_VERSION = 2

export type SessionRef = {
  owner: OwnerID | null
  session: SessionID
  window: number
  pane: number
}

export type SplitDirection = 'h' | 'v'

export type PaneNode =
  | { type: 'leaf'; ref: SessionRef }
  | {
      type: 'split'
      id?: SplitID
      direction: SplitDirection
      ratio: number
      first: PaneNode
      second: PaneNode
    }

export type SessionPhase =
  | 'pending'
  | 'starting'
  | 'active'
  | 'crashed'
  | 'cleanly_ended'
  | 'dismissed'

export type DesiredSessionState = 'run' | 'stop' | 'restart'

export type CompatLocalSession = {
  name?: string
  shell?: string
  cwd?: string
  cols?: number
  rows?: number
  daemon_pid?: number
  systemd_unit?: string
  generation?: string
}

export type LocalSessionRecord = {
  id: SessionID
  owner: OwnerID
  ref: SessionRef
  phase: SessionPhase
  desired: DesiredSessionState
  revision: number
  created_at: string
  _compat?: CompatLocalSession
}

export type CompatWorkspace = { name?: string }

export type WorkspaceRecord = {
  id: LayoutID
  owner: OwnerID
  revision: number
  tree: PaneNode
  active_key?: SessionRef
  _compat?: CompatWorkspace
}

export type CompatLayout = { name?: string }

export type LayoutRecord = {
  id: LayoutID
  owner: OwnerID
  order: number
  revision: number
  tree: PaneNode
  _compat?: CompatLayout
}

export type CompatBrowserSession = {
  display_name?: string
  project_path?: string
  prompt_preview?: string
  agent_type?: string
}

export type BrowserSession = {
  ref: SessionRef
  phase: SessionPhase
  revision: number
  _compat?: CompatBrowserSession
}

export type CompatAppDocument = {
  tmux_catalog_revision?: number
}

export type AppDocument = {
  schema: number
  owner: OwnerID
  revision: number
  sessions: LocalSessionRecord[]
  workspaces?: WorkspaceRecord[]
  layouts?: LayoutRecord[]
  commands?: CommandReceipt[]
  _compat?: CompatAppDocument
}

export type OwnerCatalogSnapshot = {
  owner: OwnerID
  revision: number
  sessions: LocalSessionRecord[]
  layouts?: LayoutRecord[]
}

export type PendingCreateRecord = {
  intent_id: CommandID
  ref: SessionRef
  inserted_at: string
}

export type BrowserWorkspaceSnapshot = {
  transport_generation: number
  revision: number
  owner: OwnerID
  sessions: BrowserSession[]
  workspace?: WorkspaceRecord
  pending?: PendingCreateRecord[]
}

export type CreateIntent = {
  id: CommandID
  owner: OwnerID
  kind: string
  target?: SessionRef
  params?: unknown
  inserted_at: string
}

export type CommandReceipt = {
  id: CommandID
  intent_id: CommandID
  seq: number
  created_at: string
}

export type SessionCommand = {
  id: CommandID
  ref: SessionRef
  action: string
  params?: unknown
}

export type WorkspaceCommand = {
  id: CommandID
  layout: LayoutID
  action: string
  params?: unknown
}

// Command policy constants mirror pkg/state/policy.go.
export const MAX_COMMAND_RECEIPT_AGE_MS = 5 * 60 * 1000
export const MAX_PENDING_COMMANDS = 128
export const MAX_PENDING_CREATES = 32
export const CREATE_RETRY_INITIAL_MS = 200
export const CREATE_RETRY_MAX_MS = 30_000
export const MAX_CREATE_RETRIES = 5

// Ratio must be a finite number strictly between 0 and 1.
export function isValidRatio(r: number): boolean {
  return Number.isFinite(r) && r > 0 && r < 1
}

// Canonical SessionRef encoding. Mirrors pkg/state SessionRef.String().
export function encodeSessionRef(ref: SessionRef): string {
  const suffix = `${ref.session}:${ref.window}.${ref.pane}`
  return ref.owner ? `${ref.owner}/${suffix}` : suffix
}

// Parse the canonical SessionRef encoding. Mirrors pkg/state ParseSessionRef().
export function parseSessionRef(s: string): SessionRef {
  if (s === '') {
    throw new Error('empty session reference')
  }
  let owner: OwnerID | null = null
  let rest = s
  const slashIdx = rest.indexOf('/')
  if (slashIdx >= 0) {
    owner = rest.slice(0, slashIdx)
    rest = rest.slice(slashIdx + 1)
    if (owner === '' || rest === '') {
      throw new Error(`invalid session reference ${s}`)
    }
  }
  const colonIdx = rest.indexOf(':')
  if (colonIdx < 0) {
    if (rest === '') {
      throw new Error(`invalid session reference ${s}`)
    }
    return { owner, session: rest, window: 0, pane: 0 }
  }
  const session = rest.slice(0, colonIdx)
  const wp = rest.slice(colonIdx + 1).split('.')
  if (session === '' || wp.length > 2 || wp[0] === '') {
    throw new Error(`invalid session reference ${s}`)
  }
  const window = parseUint16(wp[0])
  const pane = wp.length > 1 ? parseUint16(wp[1]) : 0
  return { owner, session, window, pane }
}

function parseUint16(s: string): number {
  const n = parseInt(s, 10)
  if (!Number.isFinite(n) || n < 0 || n > 65535 || String(n) !== s) {
    throw new Error(`invalid uint16 ${s}`)
  }
  return n
}

export type ErrorCode =
  | 'bad_schema'
  | 'future_schema'
  | 'invalid_identity'
  | 'duplicate_identity'
  | 'unknown_layout'
  | 'duplicate_layout'
  | 'duplicate_layout_order'
  | 'duplicate_leaf'
  | 'malformed_split'
  | 'session_in_multiple_layouts'
  | 'invalid_ratio'
  | 'command_expired'
  | 'too_many_commands'

export type StateError = {
  code: ErrorCode
  field?: string
  detail: string
}

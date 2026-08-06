/**
 * Additive wire types not carried in types.ts (the frozen design artifact).
 * These mirror the concrete JSON shapes emitted by:
 *   - GET  /api/state/bootstrap        (pkg/server/routes_state_v2.go)
 *   - WS   /ws/state              (pkg/ws/state_stream.go)
 */

import type {
  LayoutID,
  LayoutRecord,
  LocalSessionRecord,
  OwnerCatalogSnapshot,
  OwnerID,
  PendingCreateRecord,
  SessionRef,
  WorkspaceRecord,
} from './types'

export type PendingRemoteCreateRecord = {
  intent_id: string
  owner: OwnerID
  requester?: OwnerID
  ref: SessionRef
  display_name?: string
  shell?: string
  cwd?: string
  cols?: number
  rows?: number
  layout_id?: LayoutID
  status: string
  inserted_at: string
  updated_at?: string
  attempts?: number
  next_attempt?: string
  worktree_branch?: string
}

export type HostSnapshot = {
  peer_id: string
  owner_id: OwnerID
  name: string
  version?: string
  local?: boolean
  online: boolean
  last_seen: string
  stats?: Record<string, unknown>
}

// BootstrapResponse mirrors pkg/server/routes_state.go's bootstrapResponse.
// Local carries this node's own owner-authoritative catalog; Remote carries
// the latest cached catalog for every peer this node currently has a
// snapshot for (may be empty/absent). Each entry's revision is independent
// -- never conflated across owners. A remote owner absent from Remote on a
// fresh bootstrap read simply is not currently known (offline, or never
// connected); the LIVE stream (see CatalogOwnerRemovedMessage below) is what
// carries an explicit removal signal distinct from silence. Hosts is the
// complete current host snapshot list, streamed live via HostsSnapshotMessage.
export type SessionRuntime = {
  current_path?: string
  current_command?: string
  daemon_pid?: number
  shell_pid?: number
  prompt_preview?: string
  last_active?: string
  idle_seconds?: number
  total_bytes?: number
}

export type SessionRuntimeSnapshot = {
  ref: SessionRef
  runtime: SessionRuntime
}

export type OwnerRuntimeSnapshot = {
  owner: OwnerID
  snapshots: SessionRuntimeSnapshot[]
}

export type BootstrapResponse = {
  owner: OwnerID
  revision: number
  local: OwnerCatalogSnapshot
  remote?: OwnerCatalogSnapshot[]
  hosts: HostSnapshot[]
  workspace?: WorkspaceRecord
  runtime?: OwnerRuntimeSnapshot[]
  pending: PendingCreateRecord[]
  pending_remote?: PendingRemoteCreateRecord[]
}

// CatalogSnapshotMessage carries one owner's complete catalog. is_local
// tells the browser whether snapshot.owner is this node's own owner (true)
// or a cached remote peer's owner (false) -- carried explicitly rather than
// inferred from message order.
export type CatalogSnapshotMessage = {
  type: 'catalog_snapshot'
  snapshot: OwnerCatalogSnapshot
  is_local: boolean
}

// CatalogOwnerRemovedMessage is an explicit removal signal for a remote
// owner's catalog (e.g. the peer disconnected and was forgotten). Never sent
// for the local owner.
export type CatalogOwnerRemovedMessage = {
  type: 'catalog_owner_removed'
  owner: OwnerID
}

export type WorkspaceSnapshotMessage = {
  type: 'workspace_snapshot'
  workspace: WorkspaceRecord
}

export type HostsSnapshotMessage = {
  type: 'hosts_snapshot'
  hosts: HostSnapshot[]
}

export type RuntimeSnapshotMessage = {
  type: 'runtime_snapshot'
  owner: OwnerID
  snapshots: SessionRuntimeSnapshot[]
}

export type StateStreamMessage =
  | CatalogSnapshotMessage
  | CatalogOwnerRemovedMessage
  | WorkspaceSnapshotMessage
  | HostsSnapshotMessage
  | RuntimeSnapshotMessage

export function isStateStreamMessage(v: unknown): v is StateStreamMessage {
  if (typeof v !== 'object' || v === null) return false
  const t = (v as { type?: unknown }).type
  return t === 'catalog_snapshot' || t === 'catalog_owner_removed' || t === 'workspace_snapshot' || t === 'hosts_snapshot' || t === 'runtime_snapshot'
}

export type ErrorResponse = {
  code: 'invalid_input' | 'not_found' | 'revision_conflict' | 'generation_mismatch'
  field?: string
  message: string
}

// CommandResultWire mirrors pkg/state/session_commands.go's CommandResult
// exactly as it appears on the wire (the response body of POST
// /api/state/session-commands): snake_case keys per its json tags, with `ref`
// carried as SessionRef's canonical STRING form, never as an object -- see
// wireCodec.ts's decodeCommandResult, which is the one place this gets
// turned into the object shape the rest of the browser code expects.
export type CommandResultWire = {
  id: string
  ref?: string
  display_name?: string
  path?: string
  accepted: boolean
}

// CommandResult is the browser-side decoded shape of CommandResultWire: ref
// is the typed SessionRef object (see types.ts), decoded exactly once at the
// CommandClient boundary before the result reaches any caller.
export type CommandResult = {
  id: string
  ref?: SessionRef
  displayName?: string
  path?: string
  accepted: boolean
}

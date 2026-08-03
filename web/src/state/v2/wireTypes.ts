/**
 * Additive v2 wire types not carried in types.ts (the frozen design artifact).
 * These mirror the concrete JSON shapes emitted by:
 *   - GET  /api/v2/bootstrap        (pkg/server/routes_state_v2.go)
 *   - WS   /ws/v2/state              (pkg/ws/state_stream.go)
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

export type PresentationRecord = {
  ref: SessionRef
  selected: boolean
  z_index?: number
}

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

// v2BootstrapResponse mirrors server.v2BootstrapResponse. hosts is left
// unopinionated (interface{} server-side); callers should treat it as
// opaque unless/until a typed host contract is frozen.
export type V2BootstrapResponse = {
  owner: OwnerID
  revision: number
  sessions: LocalSessionRecord[]
  layouts: LayoutRecord[]
  hosts: unknown
  workspace?: WorkspaceRecord
  presentations?: PresentationRecord[]
  pending: PendingCreateRecord[]
  pending_remote?: PendingRemoteCreateRecord[]
}

export type CatalogSnapshotMessage = {
  type: 'catalog_snapshot'
  snapshot: OwnerCatalogSnapshot
}

export type WorkspaceSnapshotMessage = {
  type: 'workspace_snapshot'
  workspace: WorkspaceRecord
}

export type V2StateStreamMessage = CatalogSnapshotMessage | WorkspaceSnapshotMessage

export function isV2StateStreamMessage(v: unknown): v is V2StateStreamMessage {
  if (typeof v !== 'object' || v === null) return false
  const t = (v as { type?: unknown }).type
  return t === 'catalog_snapshot' || t === 'workspace_snapshot'
}

export type V2ErrorResponse = {
  code: 'invalid_input' | 'not_found' | 'revision_conflict' | 'generation_mismatch'
  field?: string
  message: string
}

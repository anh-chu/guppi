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
//
// Local carries this node's own owner-authoritative catalog; Remote carries
// the latest cached catalog for every peer this node currently has a
// snapshot for (may be empty/absent). Each entry's revision is independent
// -- never conflated across owners. A remote owner absent from Remote on a
// fresh bootstrap read simply is not currently known (offline, or never
// connected); the LIVE stream (see CatalogOwnerRemovedMessage below) is what
// carries an explicit removal signal distinct from silence.
export type V2BootstrapResponse = {
  owner: OwnerID
  revision: number
  local: OwnerCatalogSnapshot
  remote?: OwnerCatalogSnapshot[]
  hosts: unknown
  workspace?: WorkspaceRecord
  presentations?: PresentationRecord[]
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

export type V2StateStreamMessage =
  | CatalogSnapshotMessage
  | CatalogOwnerRemovedMessage
  | WorkspaceSnapshotMessage

export function isV2StateStreamMessage(v: unknown): v is V2StateStreamMessage {
  if (typeof v !== 'object' || v === null) return false
  const t = (v as { type?: unknown }).type
  return t === 'catalog_snapshot' || t === 'catalog_owner_removed' || t === 'workspace_snapshot'
}

export type V2ErrorResponse = {
  code: 'invalid_input' | 'not_found' | 'revision_conflict' | 'generation_mismatch'
  field?: string
  message: string
}

// CommandResultWire mirrors pkg/state/session_commands.go's CommandResult
// exactly as it appears on the wire (the response body of POST
// /api/v2/session-commands): snake_case keys per its json tags, with `ref`
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
// V2CommandClient boundary before the result reaches any caller.
export type CommandResult = {
  id: string
  ref?: SessionRef
  displayName?: string
  path?: string
  accepted: boolean
}

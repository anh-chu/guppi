/**
 * Cross-language wire boundary for SessionRef.
 *
 * pkg/state/ids.go's SessionRef has custom MarshalJSON/UnmarshalJSON that
 * encode it as a single canonical STRING ("owner/session:window.pane" or
 * "session:window.pane"), never as a JSON object. types.ts models SessionRef
 * as an OBJECT ({owner, session, window, pane}) for ergonomic use inside the
 * browser store/selectors/UI.
 *
 * Every value that crosses the wire in either direction must be converted at
 * exactly one boundary:
 *   - INCOMING (server -> browser): decode*() functions in this file convert
 *     the raw JSON (where every SessionRef is a plain string) into the typed
 *     object shape from types.ts. Call these immediately after JSON.parse()/
 *     res.json(), before any data reaches the store (stateStream.ts's
 *     handleMessage, useV2State.ts's bootstrap fetch).
 *   - OUTGOING (browser -> server): encode*() functions convert the typed
 *     object shape back into the canonical string before the command body is
 *     serialized (commands.ts).
 *
 * This file must be kept in sync with pkg/state/ids.go (SessionRef string
 * form) and pkg/state/document.go (which struct fields are SessionRef vs.
 * *SessionRef vs. []SessionRef). See wireCodec.test.ts and
 * pkg/state/ids_test.go for the shared golden fixture proving both sides
 * agree on the exact string form.
 */

import { encodeSessionRef, parseSessionRef } from './types'
import type {
  LayoutRecord,
  LocalSessionRecord,
  OwnerCatalogSnapshot,
  PaneNode,
  PendingCreateRecord,
  SessionRef,
  WorkspaceRecord,
} from './types'
import type {
  CatalogOwnerRemovedMessage,
  CatalogSnapshotMessage,
  PendingRemoteCreateRecord,
  PresentationRecord,
  V2BootstrapResponse,
  WorkspaceSnapshotMessage,
} from './wireTypes'

// ---------------------------------------------------------------------------
// Incoming (string -> object)
// ---------------------------------------------------------------------------

/** Decodes a raw wire SessionRef (a JSON string) into the object shape. */
export function decodeSessionRef(raw: unknown): SessionRef {
  if (typeof raw !== 'string') {
    throw new Error(`expected SessionRef wire value to be a string, got ${typeof raw}`)
  }
  return parseSessionRef(raw)
}

function decodeOptionalSessionRef(raw: unknown): SessionRef | undefined {
  if (raw === null || raw === undefined) return undefined
  return decodeSessionRef(raw)
}

/** Recursively decodes a raw PaneNode's leaf refs. Splits pass through. */
export function decodePaneNode(raw: any): PaneNode {
  if (raw.type === 'leaf') {
    return { type: 'leaf', ref: decodeSessionRef(raw.ref) }
  }
  if (raw.type === 'split') {
    return {
      type: 'split',
      id: raw.id,
      direction: raw.direction,
      ratio: raw.ratio,
      first: decodePaneNode(raw.first),
      second: decodePaneNode(raw.second),
    }
  }
  throw new Error(`unknown pane node type ${String(raw.type)}`)
}

export function decodeLocalSessionRecord(raw: any): LocalSessionRecord {
  return { ...raw, ref: decodeSessionRef(raw.ref) }
}

export function decodeLayoutRecord(raw: any): LayoutRecord {
  return { ...raw, tree: decodePaneNode(raw.tree) }
}

export function decodeWorkspaceRecord(raw: any): WorkspaceRecord {
  return {
    ...raw,
    tree: decodePaneNode(raw.tree),
    active_key: decodeOptionalSessionRef(raw.active_key),
  }
}

export function decodePresentationRecord(raw: any): PresentationRecord {
  return { ...raw, ref: decodeSessionRef(raw.ref) }
}

export function decodePendingCreateRecord(raw: any): PendingCreateRecord {
  return { ...raw, ref: decodeSessionRef(raw.ref) }
}

export function decodePendingRemoteCreateRecord(raw: any): PendingRemoteCreateRecord {
  return { ...raw, ref: decodeSessionRef(raw.ref) }
}

export function decodeOwnerCatalogSnapshot(raw: any): OwnerCatalogSnapshot {
  return {
    owner: raw.owner,
    revision: raw.revision,
    sessions: (raw.sessions ?? []).map(decodeLocalSessionRecord),
    layouts: raw.layouts ? raw.layouts.map(decodeLayoutRecord) : undefined,
  }
}

export function decodeBootstrapResponse(raw: any): V2BootstrapResponse {
  return {
    owner: raw.owner,
    revision: raw.revision,
    local: decodeOwnerCatalogSnapshot(raw.local),
    remote: raw.remote ? raw.remote.map(decodeOwnerCatalogSnapshot) : undefined,
    hosts: raw.hosts,
    workspace: raw.workspace ? decodeWorkspaceRecord(raw.workspace) : undefined,
    presentations: raw.presentations ? raw.presentations.map(decodePresentationRecord) : undefined,
    pending: (raw.pending ?? []).map(decodePendingCreateRecord),
    pending_remote: raw.pending_remote ? raw.pending_remote.map(decodePendingRemoteCreateRecord) : undefined,
  }
}

export function decodeCatalogSnapshotMessage(raw: any): CatalogSnapshotMessage {
  return {
    type: 'catalog_snapshot',
    snapshot: decodeOwnerCatalogSnapshot(raw.snapshot),
    is_local: Boolean(raw.is_local),
  }
}

export function decodeCatalogOwnerRemovedMessage(raw: any): CatalogOwnerRemovedMessage {
  return { type: 'catalog_owner_removed', owner: raw.owner }
}

export function decodeWorkspaceSnapshotMessage(raw: any): WorkspaceSnapshotMessage {
  return { type: 'workspace_snapshot', workspace: decodeWorkspaceRecord(raw.workspace) }
}

// ---------------------------------------------------------------------------
// Outgoing (object -> string)
// ---------------------------------------------------------------------------

/** Encodes a SessionRef object into its canonical wire string. */
export function encodeSessionRefWire(ref: SessionRef): string {
  return encodeSessionRef(ref)
}

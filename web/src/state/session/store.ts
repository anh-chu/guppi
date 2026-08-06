/**
 * Normalized canonical browser store.
 *
 * Design rules (Task 11):
 *  - Snapshots REPLACE state completely. A session/layout/presentation
 *    missing from an incoming snapshot is gone, never merged/kept around.
 *  - No wall-clock last-write-wins. Ordering is by (connection generation,
 *    revision) only. Within one connection generation, a revision <= the
 *    last-applied revision for that stream is rejected as stale. A NEW
 *    connection generation always wins regardless of the numeric revision
 *    it carries (a fresh connection starts a fresh authoritative view).
 *  - Disconnecting never clears durable projections; it only marks the
 *    connection offline. Reconnect (bootstrap + first stream snapshots)
 *    replaces state safely once the new generation's data arrives.
 */

import { encodeSessionRef } from './types'
import type {
  LayoutID,
  LayoutRecord,
  LocalSessionRecord,
  OwnerCatalogSnapshot,
  OwnerID,
  SessionPhase,
  SessionRef,
  WorkspaceRecord,
} from './types'
import type { PresentationRecord } from './wireTypes'

// OwnerCatalogMeta tracks the acceptance bookkeeping (revision + connection
// generation) for ONE owner's catalog stream -- the local node's own, or one
// cached remote peer's. Each owner's revision is independent and never
// compared across owners; only same-owner revisions are ever compared.
export type OwnerCatalogMeta = {
  owner: OwnerID
  revision: number
  generation: number
  isLocal: boolean
}

// NormalizedCatalog spans every owner (local + every remote peer this node
// currently has a cached catalog for). Sessions and layouts are kept in one
// flat map each because SessionID/LayoutID are globally unique (server-side
// ULID-style ids, never owner-scoped), so no key collision is possible
// across owners; per-owner replace-not-merge and staleness rejection is
// enforced via ownerMeta, not via separate maps per owner.
export type NormalizedCatalog = {
  // The owner this node itself is (set on the first local snapshot). null
  // until a local snapshot has ever been applied.
  localOwner: OwnerID | null
  ownerMeta: Map<OwnerID, OwnerCatalogMeta>
  sessionsByRef: Map<string, LocalSessionRecord>
  layoutsById: Map<LayoutID, LayoutRecord>
}

export type NormalizedWorkspace = {
  layoutId: LayoutID | null
  revision: number
  generation: number
  record: WorkspaceRecord | null
  presentationsByRef: Map<string, PresentationRecord>
}

export type CatalogDiff = {
  // Session refs (canonical encoding) present in the previous catalog but
  // absent from the new one -- an authoritative removal, not a merge gap.
  removed: string[]
  // True when this replacement crossed into a new connection generation
  // (e.g. after a reconnect), as opposed to an in-generation revision bump.
  generationChanged: boolean
}

export type SessionStoreState = {
  catalog: NormalizedCatalog
  workspace: NormalizedWorkspace
  connectionGeneration: number
  connectionOnline: boolean
  catalogBootstrapped: boolean
  workspaceBootstrapped: boolean
}

export function emptyCatalog(): NormalizedCatalog {
  return {
    localOwner: null,
    ownerMeta: new Map(),
    sessionsByRef: new Map(),
    layoutsById: new Map(),
  }
}

export function emptyWorkspace(): NormalizedWorkspace {
  return {
    layoutId: null,
    revision: -1,
    generation: -1,
    record: null,
    presentationsByRef: new Map(),
  }
}

export function initialSessionStoreState(): SessionStoreState {
  return {
    catalog: emptyCatalog(),
    workspace: emptyWorkspace(),
    connectionGeneration: 0,
    connectionOnline: false,
    catalogBootstrapped: false,
    workspaceBootstrapped: false,
  }
}

/**
 * Replaces ONE owner's slice of the catalog projection with a complete
 * snapshot, applying the generation/revision acceptance rule described
 * above -- scoped to that owner's own ownerMeta entry, never compared
 * against any other owner's revision/generation. isLocal defaults to true
 * so existing single-node call sites (this node's own catalog) are
 * unaffected; pass isLocal=false for a cached remote-owner snapshot.
 *
 * Sessions/layouts belonging to OTHER owners are left completely untouched;
 * only this owner's own entries are replaced (replace-not-merge, scoped to
 * the owner). Returns the (possibly unchanged) next state and a diff
 * describing what was authoritatively removed for this owner. Rejected
 * (stale) snapshots return the same state reference and an empty diff.
 */
export function replaceCatalog(
  state: SessionStoreState,
  snapshot: OwnerCatalogSnapshot,
  connectionGeneration: number,
  isLocal = true,
): { state: SessionStoreState; diff: CatalogDiff } {
  const prev = state.catalog
  const prevMeta = prev.ownerMeta.get(snapshot.owner)
  const isNewGeneration = !prevMeta || connectionGeneration !== prevMeta.generation
  if (!isNewGeneration && snapshot.revision <= prevMeta.revision) {
    // Stale revision within the same connection generation: reject.
    return { state, diff: { removed: [], generationChanged: false } }
  }

  const sessionsByRef = new Map(prev.sessionsByRef)
  const layoutsById = new Map(prev.layoutsById)

  const newKeys = new Set<string>()
  for (const s of snapshot.sessions ?? []) {
    newKeys.add(encodeSessionRef(s.ref))
  }

  const removed: string[] = []
  for (const [key, s] of prev.sessionsByRef) {
    if (s.owner === snapshot.owner && !newKeys.has(key)) {
      sessionsByRef.delete(key)
      removed.push(key)
    }
  }
  for (const s of snapshot.sessions ?? []) {
    sessionsByRef.set(encodeSessionRef(s.ref), s)
  }

  for (const [id, l] of prev.layoutsById) {
    if (l.owner === snapshot.owner) layoutsById.delete(id)
  }
  for (const l of snapshot.layouts ?? []) {
    layoutsById.set(l.id, l)
  }

  const ownerMeta = new Map(prev.ownerMeta)
  ownerMeta.set(snapshot.owner, {
    owner: snapshot.owner,
    revision: snapshot.revision,
    generation: connectionGeneration,
    isLocal,
  })

  const nextCatalog: NormalizedCatalog = {
    localOwner: isLocal ? snapshot.owner : prev.localOwner,
    ownerMeta,
    sessionsByRef,
    layoutsById,
  }

  return {
    state: {
      ...state,
      catalog: nextCatalog,
      catalogBootstrapped: true,
    },
    diff: { removed, generationChanged: isNewGeneration },
  }
}

/**
 * Authoritatively removes one remote owner's catalog entirely -- e.g. the
 * peer disconnected and its cache was explicitly forgotten (a live-stream
 * "catalog_owner_removed" signal, distinct from silence). Never call this
 * for the local owner. A no-op (same state reference) if owner is not
 * currently known.
 */
export function removeOwnerCatalog(
  state: SessionStoreState,
  owner: OwnerID,
): { state: SessionStoreState; diff: CatalogDiff } {
  const prev = state.catalog
  if (!prev.ownerMeta.has(owner)) {
    return { state, diff: { removed: [], generationChanged: false } }
  }

  const sessionsByRef = new Map(prev.sessionsByRef)
  const layoutsById = new Map(prev.layoutsById)
  const removed: string[] = []
  for (const [key, s] of prev.sessionsByRef) {
    if (s.owner === owner) {
      sessionsByRef.delete(key)
      removed.push(key)
    }
  }
  for (const [id, l] of prev.layoutsById) {
    if (l.owner === owner) layoutsById.delete(id)
  }
  const ownerMeta = new Map(prev.ownerMeta)
  ownerMeta.delete(owner)

  const nextCatalog: NormalizedCatalog = {
    ...prev,
    ownerMeta,
    sessionsByRef,
    layoutsById,
  }

  return {
    state: { ...state, catalog: nextCatalog },
    diff: { removed, generationChanged: false },
  }
}

/**
 * Replaces the workspace projection (layout tree + presentations) with a
 * complete snapshot, using the same generation/revision acceptance rule.
 * Presentations are supplied separately (they are not part of
 * WorkspaceRecord on the wire) and always fully replace the prior set.
 */
export function replaceWorkspace(
  state: SessionStoreState,
  snapshot: WorkspaceRecord,
  connectionGeneration: number,
  presentations?: PresentationRecord[],
): { state: SessionStoreState; diff: CatalogDiff } {
  const prev = state.workspace
  const isNewGeneration = connectionGeneration !== prev.generation
  if (!isNewGeneration && snapshot.revision <= prev.revision) {
    return { state, diff: { removed: [], generationChanged: false } }
  }

  const presentationsByRef = new Map<string, PresentationRecord>()
  for (const p of presentations ?? []) {
    presentationsByRef.set(encodeSessionRef(p.ref), p)
  }

  const removed: string[] = []
  for (const key of prev.presentationsByRef.keys()) {
    if (!presentationsByRef.has(key)) removed.push(key)
  }

  const nextWorkspace: NormalizedWorkspace = {
    layoutId: snapshot.id,
    revision: snapshot.revision,
    generation: connectionGeneration,
    record: snapshot,
    presentationsByRef,
  }

  return {
    state: {
      ...state,
      workspace: nextWorkspace,
      workspaceBootstrapped: true,
    },
    diff: { removed, generationChanged: isNewGeneration },
  }
}

/**
 * Marks the connection offline/online without ever touching catalog or
 * workspace projections. Disconnect must never clear durable state.
 */
export function setConnectionOnline(state: SessionStoreState, online: boolean): SessionStoreState {
  if (state.connectionOnline === online) return state
  return { ...state, connectionOnline: online }
}

export function bumpConnectionGeneration(state: SessionStoreState): { state: SessionStoreState; generation: number } {
  const generation = state.connectionGeneration + 1
  return { state: { ...state, connectionGeneration: generation }, generation }
}

/**
 * Mutable store wrapper: holds one SessionStoreState, notifies subscribers on
 * change, and exposes the pure functions above as bound methods so callers
 * (e.g. a future stateStream consumer) don't need to thread state manually.
 */
export class SessionStore {
  private state: SessionStoreState = initialSessionStoreState()
  private listeners = new Set<() => void>()

  getState(): SessionStoreState {
    return this.state
  }

  subscribe(listener: () => void): () => void {
    this.listeners.add(listener)
    return () => this.listeners.delete(listener)
  }

  private setState(next: SessionStoreState) {
    if (next === this.state) return
    this.state = next
    for (const l of this.listeners) l()
  }

  replaceCatalog(snapshot: OwnerCatalogSnapshot, connectionGeneration: number, isLocal = true): CatalogDiff {
    const { state, diff } = replaceCatalog(this.state, snapshot, connectionGeneration, isLocal)
    this.setState(state)
    return diff
  }

  removeOwnerCatalog(owner: OwnerID): CatalogDiff {
    const { state, diff } = removeOwnerCatalog(this.state, owner)
    this.setState(state)
    return diff
  }

  replaceWorkspace(
    snapshot: WorkspaceRecord,
    connectionGeneration: number,
    presentations?: PresentationRecord[],
  ): CatalogDiff {
    const { state, diff } = replaceWorkspace(this.state, snapshot, connectionGeneration, presentations)
    this.setState(state)
    return diff
  }

  setConnectionOnline(online: boolean) {
    this.setState(setConnectionOnline(this.state, online))
  }

  bumpConnectionGeneration(): number {
    const { state, generation } = bumpConnectionGeneration(this.state)
    this.setState(state)
    return generation
  }
}

// Selectors are intentionally free functions over NormalizedCatalog /
// NormalizedWorkspace rather than store methods, so they can be reused
// against any snapshot in a test or a future React binding.
export function selectSessionByRef(
  catalog: NormalizedCatalog,
  ref: SessionRef,
): LocalSessionRecord | undefined {
  return catalog.sessionsByRef.get(encodeSessionRef(ref))
}

export function selectSessionsByOwner(catalog: NormalizedCatalog, owner: OwnerID): LocalSessionRecord[] {
  const out: LocalSessionRecord[] = []
  for (const s of catalog.sessionsByRef.values()) {
    if (s.owner === owner) out.push(s)
  }
  return out
}

export function selectSessionsByLifecycle(
  catalog: NormalizedCatalog,
  phase: SessionPhase,
): LocalSessionRecord[] {
  const out: LocalSessionRecord[] = []
  for (const s of catalog.sessionsByRef.values()) {
    if (s.phase === phase) out.push(s)
  }
  return out
}

export function selectLayout(catalog: NormalizedCatalog, layoutId: LayoutID): LayoutRecord | undefined {
  return catalog.layoutsById.get(layoutId)
}

export function selectAllLayouts(catalog: NormalizedCatalog): LayoutRecord[] {
  return Array.from(catalog.layoutsById.values())
}

// selectRemoteOwners returns every owner currently known to be a remote peer
// (isLocal=false in ownerMeta), i.e. every owner besides this node's own.
export function selectRemoteOwners(catalog: NormalizedCatalog): OwnerID[] {
  const out: OwnerID[] = []
  for (const meta of catalog.ownerMeta.values()) {
    if (!meta.isLocal) out.push(meta.owner)
  }
  return out
}

// selectIsLocalOwner tells whether owner is this node's own owner (as
// opposed to a cached remote peer's), used by the UI to label/group
// sessions by "local" vs a specific remote host.
export function selectIsLocalOwner(catalog: NormalizedCatalog, owner: OwnerID): boolean {
  return catalog.localOwner === owner
}

export function selectPresentation(
  workspace: NormalizedWorkspace,
  ref: SessionRef,
): PresentationRecord | undefined {
  return workspace.presentationsByRef.get(encodeSessionRef(ref))
}

export function selectAllPresentations(workspace: NormalizedWorkspace): PresentationRecord[] {
  return Array.from(workspace.presentationsByRef.values())
}

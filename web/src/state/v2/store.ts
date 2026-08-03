/**
 * Normalized v2 browser store.
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

export type NormalizedCatalog = {
  owner: OwnerID | null
  revision: number
  generation: number
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

export type V2StoreState = {
  catalog: NormalizedCatalog
  workspace: NormalizedWorkspace
  connectionGeneration: number
  connectionOnline: boolean
  catalogBootstrapped: boolean
  workspaceBootstrapped: boolean
}

export function emptyCatalog(): NormalizedCatalog {
  return {
    owner: null,
    revision: -1,
    generation: -1,
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

export function initialV2StoreState(): V2StoreState {
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
 * Replaces the catalog projection with a complete snapshot, applying the
 * generation/revision acceptance rule described above. Returns the (possibly
 * unchanged) next state and a diff describing what was authoritatively
 * removed. Rejected (stale) snapshots return the same state reference and an
 * empty diff.
 */
export function replaceCatalog(
  state: V2StoreState,
  snapshot: OwnerCatalogSnapshot,
  connectionGeneration: number,
): { state: V2StoreState; diff: CatalogDiff } {
  const prev = state.catalog
  const isNewGeneration = connectionGeneration !== prev.generation
  if (!isNewGeneration && snapshot.revision <= prev.revision) {
    // Stale revision within the same connection generation: reject.
    return { state, diff: { removed: [], generationChanged: false } }
  }

  const sessionsByRef = new Map<string, LocalSessionRecord>()
  for (const s of snapshot.sessions ?? []) {
    sessionsByRef.set(encodeSessionRef(s.ref), s)
  }
  const layoutsById = new Map<LayoutID, LayoutRecord>()
  for (const l of snapshot.layouts ?? []) {
    layoutsById.set(l.id, l)
  }

  const removed: string[] = []
  for (const key of prev.sessionsByRef.keys()) {
    if (!sessionsByRef.has(key)) removed.push(key)
  }

  const nextCatalog: NormalizedCatalog = {
    owner: snapshot.owner,
    revision: snapshot.revision,
    generation: connectionGeneration,
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
 * Replaces the workspace projection (layout tree + presentations) with a
 * complete snapshot, using the same generation/revision acceptance rule.
 * Presentations are supplied separately (they are not part of
 * WorkspaceRecord on the wire) and always fully replace the prior set.
 */
export function replaceWorkspace(
  state: V2StoreState,
  snapshot: WorkspaceRecord,
  connectionGeneration: number,
  presentations?: PresentationRecord[],
): { state: V2StoreState; diff: CatalogDiff } {
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
export function setConnectionOnline(state: V2StoreState, online: boolean): V2StoreState {
  if (state.connectionOnline === online) return state
  return { ...state, connectionOnline: online }
}

export function bumpConnectionGeneration(state: V2StoreState): { state: V2StoreState; generation: number } {
  const generation = state.connectionGeneration + 1
  return { state: { ...state, connectionGeneration: generation }, generation }
}

/**
 * Mutable store wrapper: holds one V2StoreState, notifies subscribers on
 * change, and exposes the pure functions above as bound methods so callers
 * (e.g. a future stateStream consumer) don't need to thread state manually.
 */
export class V2Store {
  private state: V2StoreState = initialV2StoreState()
  private listeners = new Set<() => void>()

  getState(): V2StoreState {
    return this.state
  }

  subscribe(listener: () => void): () => void {
    this.listeners.add(listener)
    return () => this.listeners.delete(listener)
  }

  private setState(next: V2StoreState) {
    if (next === this.state) return
    this.state = next
    for (const l of this.listeners) l()
  }

  replaceCatalog(snapshot: OwnerCatalogSnapshot, connectionGeneration: number): CatalogDiff {
    const { state, diff } = replaceCatalog(this.state, snapshot, connectionGeneration)
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

export function selectPresentation(
  workspace: NormalizedWorkspace,
  ref: SessionRef,
): PresentationRecord | undefined {
  return workspace.presentationsByRef.get(encodeSessionRef(ref))
}

export function selectAllPresentations(workspace: NormalizedWorkspace): PresentationRecord[] {
  return Array.from(workspace.presentationsByRef.values())
}

/**
 * Browser projection selectors over the normalized v2 store.
 *
 * Selection logic itself lives next to the normalized maps in store.ts (so
 * NormalizedCatalog/NormalizedWorkspace and their selectors can't drift
 * apart); this module re-exports the selector surface plus small combined
 * views that read from both the catalog and workspace projections at once.
 */

import { encodeSessionRef } from './types'
import type { LocalSessionRecord, SessionRef } from './types'
import type { PresentationRecord } from './wireTypes'
import type { NormalizedCatalog, NormalizedWorkspace } from './store'

export {
  selectSessionByRef,
  selectSessionsByOwner,
  selectSessionsByLifecycle,
  selectLayout,
  selectAllLayouts,
  selectPresentation,
  selectAllPresentations,
  selectRemoteOwners,
  selectIsLocalOwner,
  type CatalogDiff,
  type NormalizedCatalog,
  type NormalizedWorkspace,
  type OwnerCatalogMeta,
} from './store'

import { selectSessionByRef, selectPresentation } from './store'

// PanePresentation combines a session's canonical record with its current
// on-screen presentation, if any. Presentation is undefined when the ref has
// no presentation record (e.g. not part of the streamed workspace).
export type PanePresentation = {
  session: LocalSessionRecord | undefined
  presentation: PresentationRecord | undefined
}

export function selectPanePresentation(
  catalog: NormalizedCatalog,
  workspace: NormalizedWorkspace,
  ref: SessionRef,
): PanePresentation {
  return {
    session: selectSessionByRef(catalog, ref),
    presentation: selectPresentation(workspace, ref),
  }
}

// selectSelectedPresentations returns presentation records currently marked
// selected, in stable ref order.
export function selectSelectedPresentations(workspace: NormalizedWorkspace): PresentationRecord[] {
  const out: PresentationRecord[] = []
  for (const p of workspace.presentationsByRef.values()) {
    if (p.selected) out.push(p)
  }
  out.sort((a, b) => encodeSessionRef(a.ref).localeCompare(encodeSessionRef(b.ref)))
  return out
}

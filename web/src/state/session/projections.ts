/**
 * Browser projection selectors over the normalized canonical store.
 *
 * Selection logic itself lives next to the normalized maps in store.ts (so
 * NormalizedCatalog/NormalizedWorkspace and their selectors can't drift
 * apart); this module re-exports the selector surface for the rest of the
 * browser to consume.
 */

export {
  selectSessionByRef,
  selectSessionsByOwner,
  selectSessionsByLifecycle,
  selectLayout,
  selectAllLayouts,
  selectRemoteOwners,
  selectIsLocalOwner,
  type CatalogDiff,
  type NormalizedCatalog,
  type NormalizedWorkspace,
  type OwnerCatalogMeta,
} from './store'

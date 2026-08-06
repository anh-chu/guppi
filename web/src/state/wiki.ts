/**
 * Wiki-panel target/history types.
 *
 * Extracted out of state/workspaceReducer.ts (the legacy v1 workspace
 * reducer) so that useWikiController.ts -- a hook shared by both AppLegacy
 * and AppV2 -- and AppV2 itself do not need to import a legacy-reducer
 * module purely to get these type names. workspaceReducer.ts re-exports
 * them for backward compatibility with existing legacy imports.
 */

export type WikiTarget = {
  path: string | null
  cwd?: string
  hostId?: string
  session?: string
  nonce: number
}

export interface WikiState {
  target: WikiTarget | null
  history: WikiTarget[]
}

export const WIKI_HISTORY_MAX = 20

/**
 * Wiki-panel target/history types.
 *
 * Originally extracted out of the now-deleted legacy v1 workspace reducer
 * (state/workspaceReducer.ts) so that useWikiController.ts and SessionApp
 * itself didn't need to import a legacy-reducer module purely to get these
 * type names. That reducer is gone; this module is now the sole home of
 * these types.
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

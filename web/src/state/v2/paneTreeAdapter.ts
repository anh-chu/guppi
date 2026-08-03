/**
 * Adapter between the frozen v2 workspace tree model (PaneNode / SessionRef,
 * pkg/state/workspace.go + wireTypes.ts) and the legacy renderer's PaneTree
 * (lib/paneTree.ts, sessionKey strings). Only the single active layout the
 * state stream tracks is represented -- no groups, no singleView, no
 * multi-layout selection. Those legacy-only concepts are intentionally not
 * ported into the v2 path.
 */

import type { PaneNode, SessionRef } from './types'
import type { PaneTree } from '../../lib/paneTree'

/** Mirrors hooks/useSessions.ts sessionKey(): `${host}/${name}` or `name`. */
export function sessionRefToKey(ref: SessionRef): string {
  return ref.owner ? `${ref.owner}/${ref.session}` : ref.session
}

/** Inverse of sessionRefToKey, defaulting window/pane to 0 (single-pane sessions). */
export function keyToSessionRef(key: string, window = 0, pane = 0): SessionRef {
  const idx = key.indexOf('/')
  if (idx === -1) return { owner: null, session: key, window, pane }
  return { owner: key.slice(0, idx), session: key.slice(idx + 1), window, pane }
}

export function paneNodeToPaneTree(node: PaneNode): PaneTree {
  if (node.type === 'leaf') {
    return { type: 'leaf', sessionKey: sessionRefToKey(node.ref) }
  }
  return {
    type: 'split',
    direction: node.direction,
    ratio: node.ratio,
    first: paneNodeToPaneTree(node.first),
    second: paneNodeToPaneTree(node.second),
  }
}

/**
 * Resolves the backend SplitID at a legacy "0/1"-style path (see
 * lib/paneTree.ts updateRatio) by walking the ORIGINAL PaneNode (which,
 * unlike the adapted PaneTree, still carries split ids). Returns undefined
 * if the path does not resolve to a split node or that split has no id
 * (should not happen for server-created splits -- see pkg/state/workspace.go
 * NewSplitID()).
 */
export function splitIdAtPath(node: PaneNode, path: string): string | undefined {
  if (path === '') {
    return node.type === 'split' ? node.id : undefined
  }
  if (node.type !== 'split') return undefined
  const slashIdx = path.indexOf('/')
  const segment = slashIdx >= 0 ? path.slice(0, slashIdx) : path
  const rest = slashIdx >= 0 ? path.slice(slashIdx + 1) : ''
  if (segment === '0') return splitIdAtPath(node.first, rest)
  if (segment === '1') return splitIdAtPath(node.second, rest)
  return undefined
}

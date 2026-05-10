export type LeafPane = {
  type: 'leaf'
  sessionKey: string
}

export type SplitPane = {
  type: 'split'
  direction: 'h' | 'v'
  ratio: number
  first: PaneTree
  second: PaneTree
}

export type PaneTree = LeafPane | SplitPane

/** Returns all session keys in depth-first order. */
export function getLeaves(tree: PaneTree): string[] {
  if (tree.type === 'leaf') return [tree.sessionKey]
  return [...getLeaves(tree.first), ...getLeaves(tree.second)]
}

/** Does the tree contain this session key? */
export function findLeaf(tree: PaneTree, key: string): boolean {
  if (tree.type === 'leaf') return tree.sessionKey === key
  return findLeaf(tree.first, key) || findLeaf(tree.second, key)
}

/**
 * Splits the leaf matching `targetKey`, replacing it with a SplitPane
 * {first: leaf(targetKey), second: leaf(newKey)} with ratio 0.5.
 */
export function splitLeaf(
  tree: PaneTree,
  targetKey: string,
  direction: 'h' | 'v',
  newKey: string,
): PaneTree {
  if (tree.type === 'leaf') {
    if (tree.sessionKey === targetKey) {
      return {
        type: 'split',
        direction,
        ratio: 0.5,
        first: tree,
        second: { type: 'leaf', sessionKey: newKey },
      }
    }
    return tree
  }
  return {
    ...tree,
    first: splitLeaf(tree.first, targetKey, direction, newKey),
    second: splitLeaf(tree.second, targetKey, direction, newKey),
  }
}

/**
 * Removes the leaf with `key`. If the parent split becomes a single child,
 * that child replaces the parent (collapsing the split). Returns null if the
 * tree becomes empty.
 */
export function removeLeaf(tree: PaneTree, key: string): PaneTree | null {
  if (tree.type === 'leaf') {
    if (tree.sessionKey === key) return null
    return tree
  }

  const newFirst = removeLeaf(tree.first, key)
  const newSecond = removeLeaf(tree.second, key)

  // Both sides unchanged
  if (newFirst === tree.first && newSecond === tree.second) return tree

  // One or both sides removed
  if (newFirst === null && newSecond === null) return null
  if (newFirst === null) return newSecond
  if (newSecond === null) return newFirst

  // Both still exist – rebuild
  return {
    ...tree,
    first: newFirst,
    second: newSecond,
  } as SplitPane
}

/**
 * Replaces a leaf's sessionKey (for session rename). If `oldKey` is not found,
 * returns the tree unchanged.
 */
export function replaceLeaf(
  tree: PaneTree,
  oldKey: string,
  newKey: string,
): PaneTree {
  if (tree.type === 'leaf') {
    if (tree.sessionKey === oldKey) {
      return { ...tree, sessionKey: newKey }
    }
    return tree
  }
  return {
    ...tree,
    first: replaceLeaf(tree.first, oldKey, newKey),
    second: replaceLeaf(tree.second, oldKey, newKey),
  }
}

/**
 * Update a split node's ratio at the given path.
 * Path is a "/"-separated string of "0" (first) / "1" (second) segments,
 * e.g. "" = root, "0" = first child, "0/1" = second child of first child.
 */
export function updateRatio(
  tree: PaneTree,
  path: string,
  ratio: number,
): PaneTree {
  if (!path) {
    if (tree.type === 'split') return { ...tree, ratio }
    return tree
  }

  if (tree.type !== 'split') return tree

  const slashIdx = path.indexOf('/')
  const segment = slashIdx >= 0 ? path.slice(0, slashIdx) : path
  const rest = slashIdx >= 0 ? path.slice(slashIdx + 1) : ''

  if (segment === '0') {
    return { ...tree, first: updateRatio(tree.first, rest, ratio) }
  }
  if (segment === '1') {
    return { ...tree, second: updateRatio(tree.second, rest, ratio) }
  }
  return tree
}

/** Returns a fresh single-leaf tree. */
export function popOut(key: string): PaneTree {
  return { type: 'leaf', sessionKey: key }
}

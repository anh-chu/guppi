import { describe, it, expect } from 'vitest'
import {
  type PaneTree,
  getLeaves,
  findLeaf,
  splitLeaf,
  insertBesideLeaf,
  removeLeaf,
  replaceLeaf,
  updateRatio,
  popOut,
  swapLeaves,
  movePane,
} from './paneTree'

function leaf(sessionKey: string): PaneTree {
  return { type: 'leaf', sessionKey }
}

describe('paneTree invariants', () => {
  it('has unique leaves in depth-first order', () => {
    const tree: PaneTree = {
      type: 'split',
      direction: 'h',
      ratio: 0.5,
      first: leaf('a'),
      second: {
        type: 'split',
        direction: 'v',
        ratio: 0.5,
        first: leaf('b'),
        second: leaf('c'),
      },
    }
    const leaves = getLeaves(tree)
    expect(leaves).toEqual(['a', 'b', 'c'])
    expect(new Set(leaves).size).toBe(leaves.length)
  })

  it('never produces an empty split after removal', () => {
    const tree: PaneTree = {
      type: 'split',
      direction: 'h',
      ratio: 0.5,
      first: leaf('a'),
      second: leaf('b'),
    }
    const after = removeLeaf(tree, 'a')
    expect(after).toEqual(leaf('b'))
    expect(after?.type).toBe('leaf')
  })

  it('remove is idempotent', () => {
    const tree: PaneTree = {
      type: 'split',
      direction: 'h',
      ratio: 0.5,
      first: leaf('a'),
      second: leaf('b'),
    }
    const first = removeLeaf(tree, 'a')
    const second = removeLeaf(first ?? leaf('x'), 'a')
    expect(second).toEqual(first)
  })

  it('move is idempotent', () => {
    const tree: PaneTree = {
      type: 'split',
      direction: 'h',
      ratio: 0.5,
      first: leaf('a'),
      second: leaf('b'),
    }
    const once = movePane(tree, 'a', 'b', 'right')
    const twice = movePane(once, 'a', 'b', 'right')
    expect(getLeaves(once)).toEqual(getLeaves(twice))
  })

  it('keeps ratio within bounds', () => {
    const tree: PaneTree = {
      type: 'split',
      direction: 'h',
      ratio: 0.5,
      first: leaf('a'),
      second: leaf('b'),
    }
    const updated = updateRatio(tree, '', 0.9)
    expect((updated as { type: 'split'; ratio: number }).ratio).toBe(0.9)
    const clampedPastLeaf = updateRatio(tree, '0', 0.9)
    expect(clampedPastLeaf).toEqual(tree)
  })

  it('preserves all leaves after a swap', () => {
    const tree: PaneTree = {
      type: 'split',
      direction: 'h',
      ratio: 0.5,
      first: leaf('a'),
      second: {
        type: 'split',
        direction: 'v',
        ratio: 0.5,
        first: leaf('b'),
        second: leaf('c'),
      },
    }
    const swapped = swapLeaves(tree, 'a', 'c')
    expect(getLeaves(swapped).sort()).toEqual(['a', 'b', 'c'])
    expect(findLeaf(swapped, 'c')).toBe(true)
    expect(findLeaf(swapped, 'a')).toBe(true)
  })
})

describe('paneTree operations', () => {
  it('replaceLeaf rekeys a leaf', () => {
    const tree = splitLeaf(popOut('a'), 'a', 'h', 'b')
    const renamed = replaceLeaf(tree, 'a', 'z')
    expect(getLeaves(renamed)).toEqual(['z', 'b'])
  })

  it('insertBesideLeaf places new leaf first when requested', () => {
    const tree = insertBesideLeaf(popOut('a'), 'a', 'h', 'b', true)
    expect(getLeaves(tree)).toEqual(['b', 'a'])
  })
})

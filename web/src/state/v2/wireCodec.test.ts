/**
 * Proves the browser-side SessionRef wire boundary (string <-> object) is
 * exactly consistent with pkg/state/ids.go's MarshalJSON/UnmarshalJSON.
 *
 * The golden fixture (testdata/session_ref_fixtures.json, repo root) is also
 * read by pkg/state/ids_test.go's TestSessionRefGoldenFixture -- both suites
 * must independently agree on the exact wire string for each case. This is
 * the cross-language contract test that catches an object-vs-string
 * SessionRef wire mismatch, which neither language's own suite (each only
 * exercising its own format) can catch alone.
 */

import { describe, expect, it } from 'vitest'
import fixtures from '../../../../testdata/session_ref_fixtures.json'
import commandResultFixture from '../../../../testdata/command_result_fixture.json'
import { encodeSessionRef, parseSessionRef } from './types'
import type { SessionRef } from './types'
import type { CommandResultWire } from './wireTypes'
import {
  decodeBootstrapResponse,
  decodeCatalogOwnerRemovedMessage,
  decodeCatalogSnapshotMessage,
  decodeCommandResult,
  decodeLocalSessionRecord,
  decodePaneNode,
  decodePendingCreateRecord,
  decodePresentationRecord,
  decodeSessionRef,
  decodeWorkspaceRecord,
  decodeWorkspaceSnapshotMessage,
  encodeSessionRefWire,
} from './wireCodec'

type FixtureCase = {
  name: string
  wire: string
  decoded: { owner: string | null; session: string; window: number; pane: number }
}

const cases = fixtures.cases as FixtureCase[]

describe('SessionRef golden fixture (cross-language contract)', () => {
  it('has at least one case', () => {
    expect(cases.length).toBeGreaterThan(0)
  })

  for (const c of cases) {
    it(`decodeSessionRef(${c.wire}) matches fixture`, () => {
      expect(decodeSessionRef(c.wire)).toEqual(c.decoded)
    })

    it(`encodeSessionRefWire matches fixture wire string for ${c.name}`, () => {
      expect(encodeSessionRefWire(c.decoded as SessionRef)).toBe(c.wire)
    })

    it(`parseSessionRef/encodeSessionRef agree with decode/encode for ${c.name}`, () => {
      expect(parseSessionRef(c.wire)).toEqual(c.decoded)
      expect(encodeSessionRef(c.decoded as SessionRef)).toBe(c.wire)
    })
  }
})

// Cross-language golden fixture for CommandResult, the response body of POST
// /api/v2/session-commands (Finding 3: command responses use the wrong wire
// format). pkg/server/routes_state_v2_test.go's TestCommandResultWireMatchesFixture
// posts fixture.request through the REAL route handler and asserts the
// response body equals fixture.wire byte-for-byte (field by field); this test
// takes that exact same 'wire' object and proves decodeCommandResult turns it
// into fixture.decoded -- i.e. an actual Go route response, not a hand-built
// guess at the shape, round-trips correctly through the browser decoder.
describe('CommandResult golden fixture (cross-language contract)', () => {
  it('decodeCommandResult(wire) matches fixture.decoded', () => {
    const result = decodeCommandResult(commandResultFixture.wire as CommandResultWire)
    expect(result).toEqual(commandResultFixture.decoded)
  })
})

describe('decodeSessionRef', () => {
  it('rejects a non-string (object-shaped) wire value', () => {
    // This is exactly the bug: the server never sends an object for a
    // SessionRef. If a caller passes one through unconverted, decode must
    // fail loudly rather than silently producing garbage.
    expect(() => decodeSessionRef({ owner: null, session: 'x', window: 0, pane: 0 } as unknown)).toThrow()
  })
})

describe('nested SessionRef decoding', () => {
  const wire = 'ownerabc/sessionabc:1.2'
  const decoded: SessionRef = { owner: 'ownerabc', session: 'sessionabc', window: 1, pane: 2 }

  it('decodes a leaf PaneNode ref', () => {
    const node = decodePaneNode({ type: 'leaf', ref: wire })
    expect(node).toEqual({ type: 'leaf', ref: decoded })
  })

  it('decodes refs inside both children of a split PaneNode', () => {
    const node = decodePaneNode({
      type: 'split',
      direction: 'h',
      ratio: 0.5,
      first: { type: 'leaf', ref: wire },
      second: { type: 'leaf', ref: 'sessionxyz:0.0' },
    })
    expect(node).toEqual({
      type: 'split',
      id: undefined,
      direction: 'h',
      ratio: 0.5,
      first: { type: 'leaf', ref: decoded },
      second: { type: 'leaf', ref: { owner: null, session: 'sessionxyz', window: 0, pane: 0 } },
    })
  })

  it('decodes LocalSessionRecord.ref', () => {
    const rec = decodeLocalSessionRecord({
      id: 'sessionabc',
      owner: 'ownerabc',
      ref: wire,
      phase: 'active',
      desired: 'run',
      revision: 1,
      created_at: '2025-01-01T00:00:00Z',
    })
    expect(rec.ref).toEqual(decoded)
  })

  it('decodes WorkspaceRecord.tree and active_key', () => {
    const ws = decodeWorkspaceRecord({
      id: 'layoutabc',
      owner: 'ownerabc',
      revision: 1,
      tree: { type: 'leaf', ref: wire },
      active_key: wire,
    })
    expect(ws.tree).toEqual({ type: 'leaf', ref: decoded })
    expect(ws.active_key).toEqual(decoded)
  })

  it('decodes WorkspaceRecord without active_key', () => {
    const ws = decodeWorkspaceRecord({
      id: 'layoutabc',
      owner: 'ownerabc',
      revision: 1,
      tree: { type: 'leaf', ref: wire },
    })
    expect(ws.active_key).toBeUndefined()
  })

  it('decodes PresentationRecord.ref', () => {
    const p = decodePresentationRecord({ ref: wire, selected: true })
    expect(p.ref).toEqual(decoded)
  })

  it('decodes PendingCreateRecord.ref', () => {
    const p = decodePendingCreateRecord({ intent_id: 'cmdabc', ref: wire, inserted_at: '2025-01-01T00:00:00Z' })
    expect(p.ref).toEqual(decoded)
  })

  it('decodes every nested ref in a full bootstrap response, including a remote owner catalog', () => {
    const raw = {
      owner: 'ownerabc',
      revision: 5,
      local: {
        owner: 'ownerabc',
        revision: 5,
        sessions: [
          {
            id: 'sessionabc',
            owner: 'ownerabc',
            ref: wire,
            phase: 'active',
            desired: 'run',
            revision: 1,
            created_at: '2025-01-01T00:00:00Z',
          },
        ],
        layouts: [
          {
            id: 'layoutabc',
            owner: 'ownerabc',
            order: 0,
            revision: 1,
            tree: { type: 'leaf', ref: wire },
          },
        ],
      },
      remote: [
        {
          owner: 'ownerxyz',
          revision: 42,
          sessions: [
            {
              id: 'sessionxyz',
              owner: 'ownerxyz',
              ref: 'ownerxyz/sessionxyz:0.0',
              phase: 'active',
              desired: 'run',
              revision: 1,
              created_at: '2025-01-01T00:00:00Z',
            },
          ],
        },
      ],
      hosts: null,
      workspace: {
        id: 'layoutabc',
        owner: 'ownerabc',
        revision: 1,
        tree: { type: 'leaf', ref: wire },
        active_key: wire,
      },
      presentations: [{ ref: wire, selected: true }],
      pending: [{ intent_id: 'cmdabc', ref: wire, inserted_at: '2025-01-01T00:00:00Z' }],
      pending_remote: [
        {
          intent_id: 'cmdxyz',
          owner: 'ownerabc',
          ref: wire,
          status: 'pending',
          inserted_at: '2025-01-01T00:00:00Z',
        },
      ],
    }
    const decodedBody = decodeBootstrapResponse(raw)
    expect(decodedBody.local.sessions[0]?.ref).toEqual(decoded)
    expect(decodedBody.local.layouts?.[0]?.tree).toEqual({ type: 'leaf', ref: decoded })
    expect(decodedBody.workspace?.tree).toEqual({ type: 'leaf', ref: decoded })
    expect(decodedBody.workspace?.active_key).toEqual(decoded)
    expect(decodedBody.presentations?.[0]?.ref).toEqual(decoded)
    expect(decodedBody.pending[0]?.ref).toEqual(decoded)
    expect(decodedBody.pending_remote?.[0]?.ref).toEqual(decoded)

    // Remote owner catalog decodes independently, distinguishable from local.
    expect(decodedBody.remote).toHaveLength(1)
    expect(decodedBody.remote?.[0]?.owner).toBe('ownerxyz')
    expect(decodedBody.remote?.[0]?.revision).toBe(42)
    expect(decodedBody.remote?.[0]?.sessions[0]?.ref).toEqual({ owner: 'ownerxyz', session: 'sessionxyz', window: 0, pane: 0 })
  })

  it('decodes catalog_snapshot (local and remote) and workspace_snapshot stream messages', () => {
    const catalogMsg = decodeCatalogSnapshotMessage({
      type: 'catalog_snapshot',
      snapshot: { owner: 'ownerabc', revision: 1, sessions: [] },
      is_local: true,
    })
    expect(catalogMsg.snapshot.sessions).toEqual([])
    expect(catalogMsg.is_local).toBe(true)

    const remoteCatalogMsg = decodeCatalogSnapshotMessage({
      type: 'catalog_snapshot',
      snapshot: { owner: 'ownerxyz', revision: 7, sessions: [] },
      is_local: false,
    })
    expect(remoteCatalogMsg.is_local).toBe(false)
    expect(remoteCatalogMsg.snapshot.owner).toBe('ownerxyz')

    const removedMsg = decodeCatalogOwnerRemovedMessage({ type: 'catalog_owner_removed', owner: 'ownerxyz' })
    expect(removedMsg.owner).toBe('ownerxyz')

    const wsMsg = decodeWorkspaceSnapshotMessage({
      type: 'workspace_snapshot',
      workspace: {
        id: 'layoutabc',
        owner: 'ownerabc',
        revision: 1,
        tree: { type: 'leaf', ref: wire },
      },
    })
    expect(wsMsg.workspace.tree).toEqual({ type: 'leaf', ref: decoded })
  })
})

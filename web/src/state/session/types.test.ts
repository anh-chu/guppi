import { describe, it, expect } from 'vitest'
import fixtures from './fixtures.json'
import {
  encodeSessionRef,
  parseSessionRef,
  isValidRatio,
  SCHEMA_VERSION,
  MAX_COMMAND_RECEIPT_AGE_MS,
  MAX_PENDING_COMMANDS,
  type AppDocument,
  type SessionRef,
  type PaneNode,
} from './types'

const fixtureOwner = 'ownerfixture1234567890ab'
const fixtureSession = 'sessionfixture1234567890'
const fixtureRef = `${fixtureOwner}/${fixtureSession}:0.0`

describe('SessionRef canonical encoding', () => {
  it('round-trips a remote ref', () => {
    const ref: SessionRef = {
      owner: fixtureOwner,
      session: fixtureSession,
      window: 0,
      pane: 0,
    }
    const encoded = encodeSessionRef(ref)
    expect(encoded).toBe(fixtureRef)
    expect(parseSessionRef(encoded)).toEqual(ref)
  })

  it('round-trips a local ref', () => {
    const ref: SessionRef = { owner: null, session: 'sessionabc', window: 1, pane: 2 }
    expect(parseSessionRef(encodeSessionRef(ref))).toEqual(ref)
  })

  it('rejects malformed refs', () => {
    for (const s of ['', ':', 'owner/', '/session:0.0', 'owner/session:-1.0', 'owner/session:0.0.0']) {
      expect(() => parseSessionRef(s)).toThrow()
    }
  })
})

describe('Ratio validation', () => {
  it('accepts finite values in (0,1)', () => {
    expect(isValidRatio(0.5)).toBe(true)
    expect(isValidRatio(0.001)).toBe(true)
    expect(isValidRatio(0.999)).toBe(true)
  })

  it('rejects boundary and non-finite values', () => {
    expect(isValidRatio(0)).toBe(false)
    expect(isValidRatio(1)).toBe(false)
    expect(isValidRatio(-0.1)).toBe(false)
    expect(isValidRatio(1.1)).toBe(false)
    expect(isValidRatio(NaN)).toBe(false)
    expect(isValidRatio(Infinity)).toBe(false)
  })
})

describe('Fixtures', () => {
  it('loads the shared schema and ids', () => {
    expect(fixtures.schema).toBe(SCHEMA_VERSION)
    expect(fixtures.owner).toBe(fixtureOwner)
    expect(parseSessionRef((fixtures.session as { ref: string }).ref)).toEqual({
      owner: fixtureOwner,
      session: fixtureSession,
      window: 0,
      pane: 0,
    })
  })

  it('has an oversized malformed id', () => {
    const tooLong = (fixtures.malformed_ids as { too_long: string }).too_long
    expect(tooLong.length).toBeGreaterThan(64)
  })

  it('decodes a document from fixtures', () => {
    // Static type check only; the real parser would be stricter.
    const doc: AppDocument = {
      schema: fixtures.schema,
      owner: fixtures.owner,
      revision: fixtures.revision,
      sessions: [
        {
          id: (fixtures.session as { id: string }).id,
          owner: (fixtures.session as { owner: string }).owner,
          ref: parseSessionRef((fixtures.session as { ref: string }).ref),
          phase: (fixtures.session as { phase: 'active' }).phase,
          desired: (fixtures.session as { desired: 'run' }).desired,
          revision: (fixtures.session as { revision: number }).revision,
          created_at: (fixtures.session as { created_at: string }).created_at,
        },
      ],
      layouts: [
        {
          id: (fixtures.layout as { id: string }).id,
          owner: (fixtures.layout as { owner: string }).owner,
          revision: (fixtures.layout as { revision: number }).revision,
          tree: (fixtures.layout as unknown as { tree: PaneNode }).tree,
        },
      ],
    }
    expect(doc.schema).toBe(3)
    expect(doc.sessions[0]?.id).toBe(fixtureSession)
  })
})

describe('Policy constants', () => {
  it('matches Go policy values', () => {
    expect(MAX_COMMAND_RECEIPT_AGE_MS).toBe(5 * 60 * 1000)
    expect(MAX_PENDING_COMMANDS).toBe(128)
  })
})

import { describe, expect, it } from 'vitest'
import { scanStream } from './kittyGraphics'

const enc = new TextEncoder()
const dec = new TextDecoder()

function textOf(seg: { kind: string; bytes?: Uint8Array }): string {
  return seg.bytes ? dec.decode(seg.bytes) : ''
}

describe('scanStream', () => {
  it('passes plain text through as a single text segment', () => {
    const { segments, carry } = scanStream(enc.encode('hello world'), null)
    expect(carry).toBeNull()
    expect(segments).toHaveLength(1)
    expect(segments[0].kind).toBe('text')
    expect(textOf(segments[0])).toBe('hello world')
  })

  it('parses a complete graphics command (ESC \\ terminator)', () => {
    const { segments, carry } = scanStream(enc.encode('\x1b_Gi=1,a=q,f=24;AAAA\x1b\\'), null)
    expect(carry).toBeNull()
    expect(segments).toHaveLength(1)
    const seg = segments[0]
    expect(seg.kind).toBe('cmd')
    if (seg.kind === 'cmd') {
      expect(seg.control.get('i')).toBe('1')
      expect(seg.control.get('a')).toBe('q')
      expect(seg.control.get('f')).toBe('24')
      expect(seg.payloadB64).toBe('AAAA')
    }
  })

  it('parses a command terminated by BEL', () => {
    const { segments } = scanStream(enc.encode('\x1b_Ga=q;\x07'), null)
    expect(segments).toHaveLength(1)
    expect(segments[0].kind).toBe('cmd')
  })

  it('splits text, command, text in order', () => {
    const { segments } = scanStream(enc.encode('before\x1b_Ga=T,i=5;Zm9v\x1b\\after'), null)
    expect(segments.map((s) => s.kind)).toEqual(['text', 'cmd', 'text'])
    expect(textOf(segments[0])).toBe('before')
    expect(textOf(segments[2])).toBe('after')
  })

  it('carries an incomplete command across calls', () => {
    const first = scanStream(enc.encode('abc\x1b_Gi=1,a=T;AA'), null)
    expect(first.segments).toHaveLength(1)
    expect(first.segments[0].kind).toBe('text')
    expect(textOf(first.segments[0])).toBe('abc')
    expect(first.carry).not.toBeNull()

    const second = scanStream(enc.encode('BB\x1b\\done'), first.carry)
    expect(second.carry).toBeNull()
    expect(second.segments.map((s) => s.kind)).toEqual(['cmd', 'text'])
    const cmd = second.segments[0]
    if (cmd.kind === 'cmd') expect(cmd.payloadB64).toBe('AABB')
    expect(textOf(second.segments[1])).toBe('done')
  })

  it('carries a lone trailing ESC', () => {
    const { segments, carry } = scanStream(enc.encode('hi\x1b'), null)
    expect(textOf(segments[0])).toBe('hi')
    expect(carry).not.toBeNull()
    expect(carry?.length).toBe(1)
  })

  it('does not treat ESC _ (non-G) as a graphics command', () => {
    const { segments, carry } = scanStream(enc.encode('\x1b_Xfoo'), null)
    expect(carry).toBeNull()
    expect(segments.every((s) => s.kind === 'text')).toBe(true)
  })
})

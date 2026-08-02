import { describe, it, expect } from 'vitest'
import { ReplayBuffer, concatU8, MAX_REPLAY_BUFFER_BYTES } from './replayBuffer'

function u8(values: number[]): Uint8Array {
  return new Uint8Array(values)
}

describe('ReplayBuffer', () => {
  it('holds chunks in arrival order', () => {
    const b = new ReplayBuffer()
    expect(b.add(u8([1]))).toBeNull()
    expect(b.add(u8([2, 3]))).toBeNull()
    expect(b.byteLength).toBe(3)

    const flushed = b.flush()!
    expect([...flushed]).toEqual([1, 2, 3])
    expect(b.isEmpty).toBe(true)
    expect(b.byteLength).toBe(0)
  })

  it('flush returns null when empty', () => {
    const b = new ReplayBuffer()
    expect(b.flush()).toBeNull()
  })

  it('reset clears held bytes without flushing', () => {
    const b = new ReplayBuffer()
    b.add(u8([1, 2, 3]))
    b.reset()
    expect(b.byteLength).toBe(0)
    expect(b.flush()).toBeNull()
  })

  it('flush returns to a clean state', () => {
    const b = new ReplayBuffer()
    b.add(u8([1]))
    b.flush()
    expect(b.add(u8([2]))).toBeNull()
    expect([...b.flush()!]).toEqual([2])
  })

  it('flushes immediately when the byte cap is exceeded', () => {
    const b = new ReplayBuffer(10)
    b.add(u8([1, 2, 3]))
    const flush = b.add(u8([4, 5, 6, 7, 8, 9, 10, 11])) // total 11
    expect(flush).not.toBeNull()
    expect(flush!.overflow).toBe(true)
    expect([...flush!.bytes]).toEqual([1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11])
    expect(b.isOverflowed).toBe(true)
  })

  it('exactly at cap does not overflow', () => {
    const b = new ReplayBuffer(5)
    b.add(u8([1, 2, 3]))
    expect(b.add(u8([4, 5]))).toBeNull()
    expect(b.byteLength).toBe(5)
    expect(b.isOverflowed).toBe(false)
  })

  it('passes through further chunks once overflowed', () => {
    const b = new ReplayBuffer(5)
    b.add(u8([1, 2, 3, 4, 5, 6])) // overflow flush of 6 bytes
    const passthrough = b.add(u8([7, 8]))
    expect(passthrough).not.toBeNull()
    expect(passthrough!.overflow).toBe(true)
    expect([...passthrough!.bytes]).toEqual([7, 8])
  })

  it('uses the default 32MB cap', () => {
    const b = new ReplayBuffer()
    b.add(new Uint8Array(MAX_REPLAY_BUFFER_BYTES))
    expect(b.isOverflowed).toBe(false)
    const flush = b.add(new Uint8Array([1]))
    expect(flush).not.toBeNull()
    expect(flush!.bytes.length).toBe(MAX_REPLAY_BUFFER_BYTES + 1)
  })

  it('concatU8 joins parts correctly', () => {
    const out = concatU8([u8([1, 2]), u8([]), u8([3, 4, 5])])
    expect([...out]).toEqual([1, 2, 3, 4, 5])
  })
})

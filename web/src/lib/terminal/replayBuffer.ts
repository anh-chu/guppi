//
// replayBuffer.ts — bounded replay assembly, ordering, overflow, and eviction.
//
// The daemon owns authoritative replay; the browser buffer exists only to
// assemble out-of-order WebSocket frames into one terminal.write() call while
// the replay handshake is in progress.  It is intentionally bounded so a
// misbehaving stream cannot wedge the UI.
//

/**
 * Maximum bytes to buffer while waiting for replay-end.  If a replay stream
 * exceeds this we flush what we have and switch to passthrough so the UI
 * never wedges on an unbounded backlog.
 */
export const MAX_REPLAY_BUFFER_BYTES = 32 * 1024 * 1024

export interface ReplayFlush {
  /** Concatenated bytes that must be written to the terminal. */
  bytes: Uint8Array
  /** True if the cap was exceeded and passthrough should be armed. */
  overflow: boolean
}

/* Concatenate many Uint8Arrays efficiently. */
export function concatU8(parts: Uint8Array[]): Uint8Array {
  let len = 0
  for (const p of parts) len += p.length
  const out = new Uint8Array(len)
  let off = 0
  for (const p of parts) {
    out.set(p, off)
    off += p.length
  }
  return out
}

/**
 * Pure replay byte assembler.  Bytes are kept in arrival order and flushed as
 * one slice.  Crossing the byte cap triggers an immediate flush and arms
 * passthrough so subsequent data is no longer buffered.
 */
export class ReplayBuffer {
  private pending: Uint8Array[] = []
  private bytes = 0
  private overflowed = false

  constructor(private readonly maxBytes = MAX_REPLAY_BUFFER_BYTES) {}

  /** Drop all buffered bytes and leave passthrough armed if it was armed. */
  reset(): void {
    this.pending = []
    this.bytes = 0
    this.overflowed = false
  }

  /**
   * Buffer a chunk, ordered.  Returns a {@link ReplayFlush} if the cap was
   * exceeded (or if passthrough was already armed); otherwise the chunk is
   * held and `null` is returned.
   */
  add(chunk: Uint8Array): ReplayFlush | null {
    if (this.overflowed) {
      return { bytes: chunk, overflow: true }
    }

    this.pending.push(chunk)
    this.bytes += chunk.length

    if (this.bytes > this.maxBytes) {
      const bytes = concatU8(this.pending)
      this.pending = []
      this.bytes = 0
      this.overflowed = true
      return { bytes, overflow: true }
    }

    return null
  }

  /**
   * Take all held bytes and reset the buffer.  Returns `null` when nothing
   * was buffered.
   */
  flush(): Uint8Array | null {
    if (this.pending.length === 0) return null
    const bytes = concatU8(this.pending)
    this.pending = []
    this.bytes = 0
    this.overflowed = false
    return bytes
  }

  get byteLength(): number {
    return this.bytes
  }

  get isOverflowed(): boolean {
    return this.overflowed
  }

  get isEmpty(): boolean {
    return this.pending.length === 0
  }
}

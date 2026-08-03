/**
 * @vitest-environment jsdom
 */
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { renderHook } from '@testing-library/react'
import { useWebSocket } from './useWebSocket'

let sockets: FakeWebSocket[] = []

class FakeWebSocket extends EventTarget {
  static CONNECTING = 0
  static OPEN = 1
  static CLOSED = 3

  readyState = FakeWebSocket.CONNECTING
  url: string

  constructor(url: string) {
    super()
    this.url = url
    sockets.push(this)
  }

  close() {
    if (this.readyState === FakeWebSocket.CLOSED) return
    this.readyState = FakeWebSocket.CLOSED
    this.dispatchEvent(new CloseEvent('close'))
  }
}

describe('useWebSocket', () => {
  beforeEach(() => {
    sockets = []
    vi.useFakeTimers({ shouldAdvanceTime: true })
    globalThis.WebSocket = FakeWebSocket as unknown as typeof WebSocket
  })

  afterEach(() => {
    vi.useRealTimers()
  })

  it('does not reconnect after cleanup', () => {
    const { unmount } = renderHook(() => useWebSocket('/ws', vi.fn()))
    expect(sockets).toHaveLength(1)

    const ws = sockets[0]
    ws.readyState = FakeWebSocket.OPEN
    ws.dispatchEvent(new Event('open'))

    // Cleanup must close the socket without scheduling a reconnect.
    unmount()
    expect(sockets).toHaveLength(1)

    // Even a delayed close event from the old socket must not open a new one.
    ws.close()
    vi.advanceTimersByTime(5000)
    expect(sockets).toHaveLength(1)
  })
})

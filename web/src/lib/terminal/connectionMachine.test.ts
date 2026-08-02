/**
 * @vitest-environment jsdom
 */

import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { ConnectionMachine, type ConnectionMachineOptions } from './connectionMachine'

class FakeWebSocket {
  static CONNECTING = 0
  static OPEN = 1
  static CLOSING = 2
  static CLOSED = 3

  readyState = FakeWebSocket.CONNECTING
  binaryType = 'arraybuffer'
  onopen: ((evt?: any) => void) | null = null
  onclose: ((evt?: any) => void) | null = null
  onerror: ((evt?: any) => void) | null = null
  onmessage: ((evt?: any) => void) | null = null

  sent: any[] = []

  constructor(public url: string) {}

  send(data: any) {
    this.sent.push(data)
  }

  close() {
    if (this.readyState === FakeWebSocket.CLOSED) return
    this.readyState = FakeWebSocket.CLOSED
    this.onclose?.({ code: 1000, reason: '', wasClean: true })
  }

  _open() {
    this.readyState = FakeWebSocket.OPEN
    this.onopen?.({})
  }

  _message(data: any) {
    this.onmessage?.({ data })
  }
}

function createMachine(opts: Partial<ConnectionMachineOptions> = {}, url = 'wss://test/ws') {
  const o: ConnectionMachineOptions = {
    createWebSocket: (u) => new FakeWebSocket(u) as unknown as WebSocket,
    ...opts,
  }
  return new ConnectionMachine(o, url)
}

describe('ConnectionMachine', () => {
  beforeEach(() => {
    vi.useFakeTimers()
  })

  afterEach(() => {
    vi.useRealTimers()
  })

  it('opens a WebSocket and reports connected', () => {
    const onChange = vi.fn()
    const m = createMachine({ onConnectionChange: onChange })
    m.connect()
    const ws = m.socket as unknown as FakeWebSocket
    expect(ws.url).toBe('wss://test/ws')
    expect(onChange).not.toHaveBeenCalled()
    ws._open()
    expect(onChange).toHaveBeenCalledWith(true)
    expect(m.connected).toBe(true)
  })

  it('sends heartbeat pings every 10s', () => {
    const m = createMachine()
    m.connect()
    const ws = m.socket as unknown as FakeWebSocket
    ws._open()
    vi.advanceTimersByTime(10000)
    expect(ws.sent.length).toBeGreaterThanOrEqual(1)
    const ping = JSON.parse(ws.sent[ws.sent.length - 1])
    expect(ping.type).toBe('ping')
    vi.advanceTimersByTime(10000)
    expect(ws.sent.filter(s => s.includes('"type":"ping"')).length).toBeGreaterThanOrEqual(2)
  })

  it('watchdog closes the socket after 25s without a message', () => {
    const m = createMachine()
    m.connect()
    const ws = m.socket as unknown as FakeWebSocket
    ws._open()
    expect(ws.readyState).toBe(FakeWebSocket.OPEN)
    vi.advanceTimersByTime(25000)
    expect(ws.readyState).toBe(FakeWebSocket.CLOSED)
  })

  it('watchdog is reset by any inbound message', () => {
    const m = createMachine()
    m.connect()
    const ws = m.socket as unknown as FakeWebSocket
    ws._open()
    vi.advanceTimersByTime(24000)
    ws._message(new Uint8Array([0x01]).buffer)
    vi.advanceTimersByTime(15000)
    expect(ws.readyState).toBe(FakeWebSocket.OPEN)
    vi.advanceTimersByTime(10000)
    expect(ws.readyState).toBe(FakeWebSocket.CLOSED)
  })

  it('falls back to live after 250ms without replay-start', () => {
    const onFallback = vi.fn()
    const m = createMachine({ onFallback })
    m.connect()
    const ws = m.socket as unknown as FakeWebSocket
    ws._open()
    expect(m.state).toBe('connecting')
    vi.advanceTimersByTime(250)
    expect(m.state).toBe('live')
    expect(onFallback).toHaveBeenCalled()
  })

  it('startReplay moves to replaying and cancels fallback', () => {
    const onReplayStart = vi.fn()
    const m = createMachine({ onReplayStart })
    m.connect()
    const ws = m.socket as unknown as FakeWebSocket
    ws._open()
    vi.advanceTimersByTime(200)
    m.startReplay()
    expect(m.state).toBe('replaying')
    expect(onReplayStart).toHaveBeenCalled()
    // Fallback would have fired at 250ms; after startReplay it does nothing.
    vi.advanceTimersByTime(100)
    expect(m.state).toBe('replaying')
  })

  it('endReplay moves from replaying to live', () => {
    const onReplayEnd = vi.fn()
    const m = createMachine({ onReplayEnd })
    m.connect()
    const ws = m.socket as unknown as FakeWebSocket
    ws._open()
    m.startReplay()
    vi.advanceTimersByTime(10)
    m.endReplay()
    expect(m.state).toBe('live')
    expect(onReplayEnd).toHaveBeenCalled()
  })

  it('startReplay after fallback is ignored', () => {
    const onReplayStart = vi.fn()
    const m = createMachine({ onReplayStart })
    m.connect()
    const ws = m.socket as unknown as FakeWebSocket
    ws._open()
    vi.advanceTimersByTime(250)
    expect(m.state).toBe('live')
    m.startReplay()
    expect(m.state).toBe('live')
    expect(onReplayStart).not.toHaveBeenCalled()
  })

  it('dispatches binary and text messages', () => {
    const onBinary = vi.fn()
    const onText = vi.fn()
    const m = createMachine({ onBinaryMessage: onBinary, onTextMessage: onText })
    m.connect()
    const ws = m.socket as unknown as FakeWebSocket
    ws._open()
    ws._message(new Uint8Array([0x01, 0x02]).buffer)
    expect(onBinary).toHaveBeenCalledWith(new Uint8Array([0x01, 0x02]))
    ws._message('hello')
    expect(onText).toHaveBeenCalledWith('hello')
  })

  it('reconnects after 2s on close', () => {
    const m = createMachine()
    m.connect()
    const ws = m.socket as unknown as FakeWebSocket
    ws._open()
    const firstUrl = ws.url
    ws.close()
    expect(m.state).toBe('backoff')
    vi.advanceTimersByTime(2000)
    const ws2 = m.socket as unknown as FakeWebSocket
    expect(ws2).not.toBe(ws)
    expect(ws2.url).toBe(firstUrl)
    expect(m.state).toBe('connecting')
  })

  it('defers reconnect while document is hidden and resumes on visibilitychange', () => {
    Object.defineProperty(document, 'hidden', { value: true, configurable: true, writable: true })
    const m = createMachine()
    m.connect()
    const ws = m.socket as unknown as FakeWebSocket
    ws._open()
    ws.close()
    vi.advanceTimersByTime(3000)
    expect(ws.readyState).toBe(FakeWebSocket.CLOSED)
    expect(m.state).toBe('backoff')

    ;(document as any).hidden = false
    document.dispatchEvent(new Event('visibilitychange'))
    const ws2 = m.socket as unknown as FakeWebSocket
    expect(ws2).not.toBe(ws)
    expect(m.state).toBe('connecting')
  })

  it('dispose stops timers and does not reconnect', () => {
    const onChange = vi.fn()
    const m = createMachine({ onConnectionChange: onChange })
    m.connect()
    const ws = m.socket as unknown as FakeWebSocket
    ws._open()
    m.dispose()
    expect(m.state).toBe('disposed')
    expect(ws.readyState).toBe(FakeWebSocket.CLOSED)
    vi.advanceTimersByTime(5000)
    expect(m.socket).toBeNull()
    expect(onChange).not.toHaveBeenCalledWith(false)
  })
})

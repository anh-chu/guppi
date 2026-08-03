import { describe, it, expect, vi, afterEach } from 'vitest'
import { StateStreamClient } from './stateStream'

class FakeSocket {
  static OPEN = 1
  static CONNECTING = 0
  static CLOSED = 3
  readyState = FakeSocket.CONNECTING
  onopen: (() => void) | null = null
  onmessage: ((evt: MessageEvent) => void) | null = null
  onclose: (() => void) | null = null
  onerror: ((err: Event) => void) | null = null
  closed = false

  open() {
    this.readyState = FakeSocket.OPEN
    this.onopen?.()
  }
  message(data: unknown) {
    this.onmessage?.({ data: JSON.stringify(data) } as MessageEvent)
  }
  close() {
    if (this.closed) return
    this.closed = true
    this.readyState = FakeSocket.CLOSED
    this.onclose?.()
  }
}
// Make instanceof checks in stateStream.ts (WebSocket.OPEN) work.
;(FakeSocket as unknown as { OPEN: number }).OPEN = 1

describe('StateStreamClient', () => {
  afterEach(() => {
    vi.useRealTimers()
  })

  it('dispatches catalog and workspace snapshots for the current generation', () => {
    const sockets: FakeSocket[] = []
    const onCatalog = vi.fn()
    const onWorkspace = vi.fn()
    const client = new StateStreamClient({
      url: 'ws://x',
      callbacks: { onCatalog, onWorkspace },
      createSocket: () => {
        const s = new FakeSocket()
        sockets.push(s)
        return s as unknown as WebSocket
      },
      isDocumentHidden: () => false,
      addVisibilityListener: () => () => {},
    })
    client.start()
    sockets[0].open()
    sockets[0].message({ type: 'catalog_snapshot', snapshot: { owner: 'o', revision: 1, sessions: [] } })
    sockets[0].message({ type: 'workspace_snapshot', workspace: { id: 'L1', owner: 'o', revision: 1, tree: { type: 'leaf', ref: { owner: 'o', session: 's', window: 0, pane: 0 } } } })

    expect(onCatalog).toHaveBeenCalledTimes(1)
    expect(onWorkspace).toHaveBeenCalledTimes(1)
    expect(onCatalog.mock.calls[0][1]).toBe(client.currentGeneration())
    client.dispose()
  })

  it('old generation callbacks cannot dispatch into a newer generation after reconnect', () => {
    vi.useFakeTimers()
    const sockets: FakeSocket[] = []
    const onCatalog = vi.fn()
    const client = new StateStreamClient({
      url: 'ws://x',
      callbacks: { onCatalog, onWorkspace: vi.fn() },
      createSocket: () => {
        const s = new FakeSocket()
        sockets.push(s)
        return s as unknown as WebSocket
      },
      isDocumentHidden: () => false,
      addVisibilityListener: () => () => {},
      minBackoffMs: 10,
      maxBackoffMs: 20,
      jitter: () => 0,
    })
    client.start()
    const first = sockets[0]
    const genAfterFirstConnect = client.currentGeneration()
    first.open()
    first.close() // triggers scheduleReconnect
    vi.advanceTimersByTime(50)
    expect(sockets.length).toBe(2)
    expect(client.currentGeneration()).toBeGreaterThan(genAfterFirstConnect)

    // A message arriving late on the now-stale first socket must be ignored.
    first.message({ type: 'catalog_snapshot', snapshot: { owner: 'o', revision: 1, sessions: [] } })
    expect(onCatalog).not.toHaveBeenCalled()

    // But the new socket's messages go through.
    sockets[1].open()
    sockets[1].message({ type: 'catalog_snapshot', snapshot: { owner: 'o', revision: 1, sessions: [] } })
    expect(onCatalog).toHaveBeenCalledTimes(1)
    client.dispose()
  })

  it('dispose cancels pending reconnect timers and stops further callbacks', () => {
    vi.useFakeTimers()
    const sockets: FakeSocket[] = []
    const onConnectionChange = vi.fn()
    const client = new StateStreamClient({
      url: 'ws://x',
      callbacks: { onCatalog: vi.fn(), onWorkspace: vi.fn(), onConnectionChange },
      createSocket: () => {
        const s = new FakeSocket()
        sockets.push(s)
        return s as unknown as WebSocket
      },
      isDocumentHidden: () => false,
      addVisibilityListener: () => () => {},
      minBackoffMs: 10,
      maxBackoffMs: 20,
      jitter: () => 0,
    })
    client.start()
    sockets[0].open()
    sockets[0].close()
    client.dispose()
    vi.advanceTimersByTime(1000)
    expect(sockets.length).toBe(1) // no reconnect happened after dispose
  })

  it('defers reconnect while hidden and reconnects immediately on visibility change', () => {
    vi.useFakeTimers()
    const sockets: FakeSocket[] = []
    const visibilityCbHolder: { fn: (() => void) | null } = { fn: null }
    let hidden = false
    const client = new StateStreamClient({
      url: 'ws://x',
      callbacks: { onCatalog: vi.fn(), onWorkspace: vi.fn() },
      createSocket: () => {
        const s = new FakeSocket()
        sockets.push(s)
        return s as unknown as WebSocket
      },
      isDocumentHidden: () => hidden,
      addVisibilityListener: (fn) => {
        visibilityCbHolder.fn = fn
        return () => {
          visibilityCbHolder.fn = null
        }
      },
      minBackoffMs: 10,
      maxBackoffMs: 20,
      jitter: () => 0,
    })
    client.start()
    sockets[0].open()
    hidden = true
    visibilityCbHolder.fn?.()
    sockets[0].close()
    vi.advanceTimersByTime(1000)
    expect(sockets.length).toBe(1) // no reconnect while hidden

    hidden = false
    visibilityCbHolder.fn?.()
    expect(sockets.length).toBe(2) // reconnected immediately on visibility
    client.dispose()
  })
})

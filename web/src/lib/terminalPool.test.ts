//
// terminalPool.test.ts — State-machine tests for TerminalPool
//
// Uses injectable fake factories. Does NOT construct real WebGL/xterm.
//

/**
 * @vitest-environment jsdom
 */

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { TerminalPool, keyFor, __injectTransferNode, MAX_REPLAY_BUFFER_BYTES, concatU8 } from './terminalPool'
import type { PoolFactory, PoolIdentity, TerminalPrefs, CheckoutCallbacks, LeaseToken } from './terminalPool'

// ── Counters ──────────────────────────────────────────────────────────

let terminalCreateCount = 0
let terminalDisposeCount = 0
let addonCreateCount = 0
let addonDisposeCount = 0
let socketOpenCount = 0
let socketCloseCount = 0
let socketSendCount = 0
let transferCallCount = 0

function resetCounters() {
  terminalCreateCount = 0
  terminalDisposeCount = 0
  addonCreateCount = 0
  addonDisposeCount = 0
  socketOpenCount = 0
  socketCloseCount = 0
  socketSendCount = 0
  transferCallCount = 0
}

// ── Fake HTMLElement ──────────────────────────────────────────────────

interface FakeEl extends HTMLElement {
  _fire(type: string, ...args: any[]): void
  _children: HTMLElement[]
  _parent: HTMLElement | null
}

function fakeEl(tag = 'div'): FakeEl {
  const children: HTMLElement[] = []
  let parent: HTMLElement | null = null
  const listeners = new Map<string, Array<(...args: any[]) => void>>()
  const styleObj: Record<string, string> = {}

  const el = {
    tagName: tag.toUpperCase(),
    _children: children,
    _parent: parent,

    get ownerDocument() {
      return {
        body: {
          appendChild(child: HTMLElement) {
            (child as FakeEl)._parent = el as any
            return child
          },
        } as any,
        defaultView: null,
      } as unknown as Document
    },

    appendChild(child: HTMLElement) {
      if ((child as FakeEl)._parent) {
        (child as FakeEl)._parent?.removeChild(child)
      }
      children.push(child)
      ;(child as FakeEl)._parent = el
      return child
    },
    removeChild(child: HTMLElement) {
      const idx = children.indexOf(child)
      if (idx > -1) { children.splice(idx, 1); (child as FakeEl)._parent = null }
      return child
    },
    remove() {
      if (parent) { parent.removeChild(el); parent = null }
    },
    get parentElement() { return parent },
    get children() { return children as unknown as HTMLCollection },

    style: {
      setProperty(k: string, v: string) { styleObj[k] = v },
      getPropertyValue(k: string) { return styleObj[k] || '' },
      position: '', top: '', left: '', width: '', height: '',
      overflow: '', pointerEvents: '', visibility: '', opacity: '',
    } as unknown as CSSStyleDeclaration,

    clientWidth: 800,
    clientHeight: 600,
    scrollTop: 0,

    querySelector(_sel: string): HTMLElement | null { return null },
    getElementsByClassName(): HTMLCollectionOf<Element> { return { length: 0, item: () => null, namedItem: () => null } as any },
    querySelectorAll(): NodeListOf<Element> { return { length: 0, item: () => null, forEach: () => {} } as any },

    addEventListener(type: string, fn: any, _opts?: any) {
      if (!listeners.has(type)) listeners.set(type, [])
      listeners.get(type)!.push(fn)
    },
    removeEventListener(type: string, fn: any) {
      const arr = listeners.get(type)
      if (arr) { const idx = arr.indexOf(fn); if (idx > -1) arr.splice(idx, 1) }
    },
    dispatchEvent(event: any) {
      const arr = listeners.get(event.type as string)
      arr?.forEach(fn => fn(event))
      return true
    },
    _fire(type: string, ...args: any[]) {
      const arr = listeners.get(type)
      arr?.forEach(fn => fn(...args))
    },
  } as unknown as FakeEl

  return el
}

// ── Fake Terminal ─────────────────────────────────────────────────────

class FakeTerminal {
  options: any
  element: HTMLElement
  cols = 80
  rows = 24
  buffer = {
    active: {
      type: 'normal' as const,
      cursorX: 0, cursorY: 0,
      baseY: 100, viewportY: 95, length: 100,
      getLine: (_i: number) => null,
    },
  }
  private _dataListeners: Array<(data: string) => void> = []
  private _resizeListeners: Array<(info: { cols: number; rows: number }) => void> = []

  constructor(options: any) {
    terminalCreateCount++
    this.options = options
    this.element = fakeEl()
  }

  loadAddon(_addon: any) { addonCreateCount++ }
  open(_container: HTMLElement) {}
  dispose() { terminalDisposeCount++ }
  onData(fn: (data: string) => void) {
    this._dataListeners.push(fn)
    return { dispose: () => {
      const idx = this._dataListeners.indexOf(fn)
      if (idx > -1) this._dataListeners.splice(idx, 1)
    }}
  }
  onResize(fn: (info: { cols: number; rows: number }) => void) {
    this._resizeListeners.push(fn)
    return { dispose: () => {
      const idx = this._resizeListeners.indexOf(fn)
      if (idx > -1) this._resizeListeners.splice(idx, 1)
    }}
  }
  onSelectionChange(_fn: () => void) { return { dispose: () => {} } }
  attachCustomKeyEventHandler(_fn: (e: any) => boolean) {}
  getSelection() { return '' }
  clearSelection() {}
  scrollToBottom() {}
  scrollLines(_delta: number) {}
  focus() {}
  refresh(_start: number, _end: number) {}
  writes: { data: Uint8Array | string; cb?: () => void }[] = []
  write(data: any, cb?: () => void) {
    if (data instanceof ArrayBuffer) {
      this.writes.push({ data: new Uint8Array(data), cb })
    } else if (typeof data === 'object' && data && typeof data.length === 'number' && !(data instanceof Uint8Array)) {
      this.writes.push({ data: new Uint8Array(data), cb })
    } else {
      this.writes.push({ data, cb })
    }
    if (cb) cb()
  }

  _fireData(data: string) { this._dataListeners.forEach(fn => fn(data)) }
  _fireResize(cols: number, rows: number) {
    this.cols = cols; this.rows = rows
    this._resizeListeners.forEach(fn => fn({ cols, rows }))
  }
}

// ── Fake Addons ──────────────────────────────────────────────────────

class FakeFitAddon { fit() {} constructor() { addonCreateCount++ } }
class FakeWebLinksAddon { constructor() { addonCreateCount++ } }
class FakeClipboardAddon { constructor() { addonCreateCount++ } }
class FakeWebglAddon {
  private _onContextLoss: (() => void) | null = null
  constructor() { addonCreateCount++ }
  onContextLoss(fn: () => void) { this._onContextLoss = fn }
  dispose() { addonDisposeCount++ }
  _fireContextLoss() { this._onContextLoss?.() }
}
class FakeImageAddon { constructor() { addonCreateCount++ } dispose() { addonDisposeCount++ } }
class FakeUnicodeGraphemesAddon { constructor() { addonCreateCount++ } dispose() { addonDisposeCount++ } }
class FakePredictiveEcho {
  constructor() { addonCreateCount++ }
  canPredict(_data: string) { return false }
  predict(_char: string) {}
  clear() {}
  dispose() { addonDisposeCount++ }
}

// ── Fake WebSocket ───────────────────────────────────────────────────

class FakeWebSocket {
  static CONNECTING = 0
  static OPEN = 1
  static CLOSED = 3

  readyState = FakeWebSocket.CONNECTING
  binaryType = 'arraybuffer'
  url: string
  onopen: ((evt?: any) => void) | null = null
  onclose: ((evt?: any) => void) | null = null
  onerror: ((evt?: any) => void) | null = null
  onmessage: ((evt?: any) => void) | null = null

  constructor(url: string) {
    socketOpenCount++
    this.url = url
  }

  send(_data: any) { socketSendCount++ }
  close() {
    socketCloseCount++
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

// ── Transfer ─────────────────────────────────────────────────────────

function fakeTransferNode(node: HTMLElement, dest: HTMLElement): { crossedDocument: boolean } {
  transferCallCount++
  // In tests we cannot call real jsdom appendChild with fake elements.
  // Track the call for assertions; pool code only needs crossedDocument.
  return { crossedDocument: false }
}

// ── Factory ──────────────────────────────────────────────────────────

function createFakeFactory(): PoolFactory {
  return {
    createTerminal: (options) => new FakeTerminal(options) as unknown as any,
    createFitAddon: () => new FakeFitAddon() as unknown as any,
    createWebLinksAddon: () => new FakeWebLinksAddon() as unknown as any,
    createClipboardAddon: () => new FakeClipboardAddon() as unknown as any,
    createWebglAddon: () => new FakeWebglAddon() as unknown as any,
    createImageAddon: () => new FakeImageAddon() as unknown as any,
    createUnicodeGraphemesAddon: () => new FakeUnicodeGraphemesAddon() as unknown as any,
    createPredictiveEcho: () => new FakePredictiveEcho() as unknown as any,
    createWebSocket: (url) => new FakeWebSocket(url) as unknown as WebSocket,
  }
}

// ── Test helpers ──────────────────────────────────────────────────────

function defPrefs(overrides?: Partial<TerminalPrefs>): TerminalPrefs {
  return {
    theme: 'dark', fontFamily: 'Space Mono', fontSize: 13,
    scrollback: 50000, renderer: 'dom',
    unicodeGraphemes: false, predictiveEcho: false,
    ...overrides,
  }
}

function defId(name = 'test-session', host?: string, backend?: string): PoolIdentity {
  return { sessionName: name, hostId: host, backend }
}

function noopCbs(): CheckoutCallbacks {
  return {
    onConnectionChange: () => {},
    onCtrlModifierChange: () => {},
    onAltModifierChange: () => {},
    onSelectionMenu: () => {},
  }
}

// ── Suite ────────────────────────────────────────────────────────────

describe('TerminalPool', () => {
  let pool: TerminalPool

  beforeEach(() => {
    resetCounters()
    pool = new TerminalPool(createFakeFactory())
    __injectTransferNode(fakeTransferNode)
  })

  afterEach(() => {
    pool.reset()
  })

  // ── Key normalization ──────────────────────────────────────────────

  it('normalizes keys: local session', () => {
    expect(keyFor('mysession', undefined)).toBe('mysession')
    expect(keyFor('mysession', '')).toBe('mysession')
    expect(TerminalPool.keyFor('mysession')).toBe('mysession')
  })

  it('normalizes keys: remote host', () => {
    expect(keyFor('mysession', 'host123')).toBe('host123/mysession')
  })

  // ── Cold checkout ──────────────────────────────────────────────────

  it('first checkout cold-creates one entry', () => {
    const container = fakeEl()
    const lease = pool.checkout(defId('s1'), defPrefs(), container, noopCbs())
    expect(lease.key).toBe('s1')
    expect(lease.generation).toBe(1)
    expect(pool.size).toBe(1)
    expect(terminalCreateCount).toBe(1)
    expect(addonCreateCount).toBeGreaterThanOrEqual(4)
    expect(socketOpenCount).toBe(1)
  })

  // Registration-timing guard. Terminal.tsx registers the file-link provider in
  // a useLayoutEffect immediately after connect(), reading termRef.current —
  // which useTerminal assigns from getTerminalForPaste(lease) during checkout.
  // If checkout ever became async, that ref would be null on the cold path and
  // the provider would silently never register. Pin the synchronous contract.
  it('cold checkout exposes the terminal synchronously (link-provider timing)', () => {
    const container = fakeEl()
    const lease = pool.checkout(defId('s1'), defPrefs(), container, noopCbs())
    const term = pool.getTerminalForPaste(lease)
    // Non-null the moment checkout returns: this is the property the
    // link-provider registration depends on. (The fake factory does not model
    // registerLinkProvider, so the method itself is not asserted here.)
    expect(term).not.toBeNull()
    expect(term).toBeDefined()
  })

  it('warm re-checkout also exposes the terminal synchronously', () => {
    const c1 = fakeEl()
    const l1 = pool.checkout(defId('s1'), defPrefs(), c1, noopCbs())
    const first = pool.getTerminalForPaste(l1)
    pool.checkin(l1)

    const c2 = fakeEl()
    const l2 = pool.checkout(defId('s1'), defPrefs(), c2, noopCbs())
    const second = pool.getTerminalForPaste(l2)
    expect(second).not.toBeNull()
    // Same underlying terminal: a re-registered provider must target this one.
    expect(second).toBe(first)
  })

  it('cold checkout loads WebGL when prefs specify webgl', () => {
    const container = fakeEl()
    pool.checkout(defId('s1'), defPrefs({ renderer: 'webgl' }), container, noopCbs())
    expect(terminalCreateCount).toBe(1)
    expect(addonCreateCount).toBeGreaterThanOrEqual(5)
  })

  // ── Checkin keeps resources alive ──────────────────────────────────

  it('checkin does not dispose terminal/addons/socket', () => {
    const container = fakeEl()
    const lease = pool.checkout(defId('s1'), defPrefs(), container, noopCbs())
    const tdB = terminalDisposeCount, adB = addonDisposeCount, scB = socketCloseCount
    pool.checkin(lease)
    expect(terminalDisposeCount).toBe(tdB)
    expect(addonDisposeCount).toBe(adB)
    expect(socketCloseCount).toBe(scB)
    expect(pool.size).toBe(1)
  })

  // ── Warm checkout ──────────────────────────────────────────────────

  it('repeat checkout reuses terminal (no new creates)', () => {
    const container = fakeEl()
    const l1 = pool.checkout(defId('s1'), defPrefs(), container, noopCbs())
    pool.checkin(l1)
    const tcB = terminalCreateCount, scB = socketOpenCount
    const l2 = pool.checkout(defId('s1'), defPrefs(), container, noopCbs())
    expect(l2.key).toBe('s1')
    expect(l2.generation).toBe(3)
    expect(terminalCreateCount).toBe(tcB)
    expect(socketOpenCount).toBe(scB)
  })

  // ── Different host ─────────────────────────────────────────────────

  it('different host creates distinct entry', () => {
    const container = fakeEl()
    pool.checkout(defId('s1', 'hostA'), defPrefs(), container, noopCbs())
    pool.checkout(defId('s1', 'hostB'), defPrefs(), container, noopCbs())
    expect(pool.size).toBe(2)
    expect(terminalCreateCount).toBe(2)
  })

  // ── Backend mismatch ───────────────────────────────────────────────

  it('backend mismatch disposes old and recreates', () => {
    const container = fakeEl()
    const l = pool.checkout(defId('s1', undefined, 'daemon'), defPrefs(), container, noopCbs())
    pool.checkin(l)
    const tdB = terminalDisposeCount
    pool.checkout(defId('s1', undefined, 'tmux'), defPrefs(), container, noopCbs())
    expect(terminalDisposeCount).toBeGreaterThan(tdB)
    expect(terminalCreateCount).toBe(2)
  })

  // ── Exclusive lease ────────────────────────────────────────────────

  it('latest lease wins: stale checkin no-op', () => {
    const container = fakeEl()
    const l1 = pool.checkout(defId('s1'), defPrefs(), container, noopCbs())
    const l2 = pool.checkout(defId('s1'), defPrefs(), container, noopCbs())
    expect(l2.generation).toBeGreaterThan(l1.generation)
    pool.checkin(l1) // stale
    expect(pool.size).toBe(1)
    pool.checkin(l2) // current
    expect(pool.size).toBe(1)
  })

  it('stale send does nothing', () => {
    const container = fakeEl()
    const l1 = pool.checkout(defId('s1'), defPrefs(), container, noopCbs())
    pool.checkout(defId('s1'), defPrefs(), container, noopCbs()) // l2, invalidates l1
    const scB = socketSendCount
    pool.sendText(l1, 'hello')
    expect(socketSendCount).toBe(scB)
  })

  // ── Input gating ───────────────────────────────────────────────────

  it('active sends pass', () => {
    const container = fakeEl()
    const l = pool.checkout(defId('s1'), defPrefs(), container, noopCbs())
    const entry = pool._debug_entry(l.key)
    ;(entry?.ws as unknown as FakeWebSocket)?._open()
    const scB = socketSendCount
    pool.sendText(l, 'hello')
    expect(socketSendCount).toBeGreaterThan(scB)
  })

  it('checked-in sends rejected', () => {
    const container = fakeEl()
    const l = pool.checkout(defId('s1'), defPrefs(), container, noopCbs())
    // Open socket so sends can go through while lease is active
    const entry = pool._debug_entry(l.key)
    ;(entry?.ws as unknown as FakeWebSocket)?._open()
    // Send while active should work
    const scBeforeCheckin = socketSendCount
    pool.sendText(l, 'hello')
    expect(socketSendCount).toBeGreaterThan(scBeforeCheckin)

    pool.checkin(l)
    // After checkin, send should be rejected
    const scAfterCheckin = socketSendCount
    pool.sendText(l, 'hello')
    expect(socketSendCount).toBe(scAfterCheckin)
  })

  // ── Modifiers ──────────────────────────────────────────────────────

  it('stale sendImage rejected after checkin', async () => {
    const container = fakeEl()
    const l = pool.checkout(defId('s1'), defPrefs(), container, noopCbs())
    const entry = pool._debug_entry(l.key)
    const ws = entry?.ws as unknown as FakeWebSocket
    ws?._open()

    // Create a small fake file
    const file = new File(['fake'], 'test.png', { type: 'image/png' })

    // Now checkin to invalidate lease
    pool.checkin(l)

    const scB = socketSendCount
    await pool.sendImage(l, file, 'image/png')
    // Should have been rejected — generation no longer matches
    expect(socketSendCount).toBe(scB)
  })

  it('modifier toggle persists through checkin/checkout', () => {
    const container = fakeEl()
    let ctrl = false
    const cbs1: CheckoutCallbacks = { ...noopCbs(), onCtrlModifierChange: (a) => { ctrl = a } }
    const l1 = pool.checkout(defId('s1'), defPrefs(), container, cbs1)
    pool.toggleCtrlModifier(l1)
    expect(ctrl).toBe(true)
    pool.checkin(l1)

    let ctrl2 = false
    const cbs2: CheckoutCallbacks = { ...noopCbs(), onCtrlModifierChange: (a) => { ctrl2 = a } }
    const l2 = pool.checkout(defId('s1'), defPrefs(), container, cbs2)
    expect(ctrl2).toBe(true)
    pool.clearCtrlModifier(l2)
    expect(ctrl2).toBe(false)
  })

  // ── Snapshot ───────────────────────────────────────────────────────

  it('getSnapshot null for unknown key', () => {
    expect(pool.getSnapshot('unknown')).toBeNull()
  })

  it('getSnapshot returns connection state after socket open', () => {
    const container = fakeEl()
    const l = pool.checkout(defId('s1'), defPrefs(), container, noopCbs())
    expect(pool.getSnapshot(l.key)!.connected).toBe(false)
    const entry = pool._debug_entry(l.key)
    ;(entry?.ws as unknown as FakeWebSocket)?._open()
    expect(pool.getSnapshot(l.key)!.connected).toBe(true)
  })

  // ── WebGL context loss ─────────────────────────────────────────────

  it('WebGL context loss disposes only WebGL, keeps terminal', () => {
    const container = fakeEl()
    const l = pool.checkout(defId('s1'), defPrefs({ renderer: 'webgl' }), container, noopCbs())
    const entry = pool._debug_entry(l.key)
    const wgl = entry?.webglAddon as unknown as FakeWebglAddon
    expect(wgl).toBeTruthy()
    const tdB = terminalDisposeCount, scB = socketCloseCount, adB = addonDisposeCount
    wgl?._fireContextLoss()
    expect(addonDisposeCount).toBeGreaterThan(adB)
    expect(terminalDisposeCount).toBe(tdB)
    expect(socketCloseCount).toBe(scB)
  })

  // ── Prefs: theme/font preserve terminal ────────────────────────────

  it('theme/font update does not recreate terminal', () => {
    const container = fakeEl()
    const l = pool.checkout(defId('s1'), defPrefs(), container, noopCbs())
    const tcB = terminalCreateCount
    pool.applyGlobalPrefs(defPrefs({ theme: 'light', fontSize: 16 }))
    expect(terminalCreateCount).toBe(tcB)
  })

  // ── Scrollback recreates ───────────────────────────────────────────

  it('scrollback change recreates', () => {
    const container = fakeEl()
    const l = pool.checkout(defId('s1'), defPrefs({ scrollback: 1000 }), container, noopCbs())
    const tdB = terminalDisposeCount, tcB = terminalCreateCount
    pool.applyGlobalPrefs(defPrefs({ scrollback: 2000 }))
    expect(terminalDisposeCount).toBeGreaterThan(tdB)
    expect(terminalCreateCount).toBeGreaterThan(tcB)
  })

  // ── Rekey ──────────────────────────────────────────────────────────

  it('rekey preserves terminal, updates map identity', () => {
    const container = fakeEl()
    const l = pool.checkout(defId('old', 'hostA'), defPrefs(), container, noopCbs())
    const tcB = terminalCreateCount, tdB = terminalDisposeCount
    pool.rekey('hostA/old', 'hostA/new')
    expect(terminalCreateCount).toBe(tcB)
    expect(terminalDisposeCount).toBe(tdB)
    expect(pool._debug_hasEntry('hostA/old')).toBe(false)
    expect(pool._debug_hasEntry('hostA/new')).toBe(true)
  })

  it('rekey collision disposes dest', () => {
    const container = fakeEl()
    const l1 = pool.checkout(defId('a'), defPrefs(), container, noopCbs())
    pool.checkin(l1)
    const l2 = pool.checkout(defId('b'), defPrefs(), container, noopCbs())
    pool.checkin(l2)
    const tdB = terminalDisposeCount
    pool.rekey('a', 'b')
    expect(terminalDisposeCount).toBeGreaterThan(tdB)
    expect(pool.size).toBe(1)
    expect(pool._debug_hasEntry('b')).toBe(true)
  })

  // ── Dispose ────────────────────────────────────────────────────────

  it('dispose tears down entry once', () => {
    const container = fakeEl()
    const l = pool.checkout(defId('s1'), defPrefs(), container, noopCbs())
    pool.checkin(l)
    const tdB = terminalDisposeCount
    pool.dispose('s1')
    expect(terminalDisposeCount).toBeGreaterThan(tdB)
    expect(socketCloseCount).toBeGreaterThan(0)
    expect(pool.size).toBe(0)
  })

  it('dispose absent key no-ops', () => {
    const tdB = terminalDisposeCount
    pool.dispose('nonexistent')
    expect(terminalDisposeCount).toBe(tdB)
  })

  // ── disposeAbsent ─────────────────────────────────────────────────

  it('disposeAbsent removes only missing keys', () => {
    const container = fakeEl()
    const l1 = pool.checkout(defId('s1'), defPrefs(), container, noopCbs())
    pool.checkin(l1)
    const l2 = pool.checkout(defId('s2'), defPrefs(), container, noopCbs())
    pool.checkin(l2)
    expect(pool.size).toBe(2)
    pool.disposeAbsent(new Set(['s1']))
    expect(pool.size).toBe(1)
    expect(pool._debug_hasEntry('s1')).toBe(true)
    expect(pool._debug_hasEntry('s2')).toBe(false)
  })

  it('disposeAbsent with all valid does nothing', () => {
    const container = fakeEl()
    pool.checkout(defId('s1'), defPrefs(), container, noopCbs())
    pool.disposeAbsent(new Set(['s1', 'extra']))
    expect(pool.size).toBe(1)
  })

  // ── Reset ──────────────────────────────────────────────────────────

  it('reset cleans all entries', () => {
    pool.checkout(defId('s1'), defPrefs(), fakeEl(), noopCbs())
    pool.checkout(defId('s2'), defPrefs(), fakeEl(), noopCbs())
    expect(pool.size).toBe(2)
    pool.reset()
    expect(pool.size).toBe(0)
    expect(terminalDisposeCount).toBe(2)
  })

  // ── Concurrent checkout no second terminal ─────────────────────────

  it('same-key concurrent checkout no second terminal', () => {
    const container = fakeEl()
    const l1 = pool.checkout(defId('s1'), defPrefs(), container, noopCbs())
    const tcAfterFirst = terminalCreateCount
    const l2 = pool.checkout(defId('s1'), defPrefs(), container, noopCbs())
    expect(terminalCreateCount).toBe(tcAfterFirst)  // no new terminal
    expect(l2.generation).toBeGreaterThan(l1.generation)
    expect(pool.size).toBe(1)
  })

  // ── No eviction ────────────────────────────────────────────────────

  it('no eviction regardless of entry count', () => {
    const container = fakeEl()
    for (let i = 0; i < 30; i++) {
      pool.checkout(defId(`s${i}`), defPrefs(), container, noopCbs())
    }
    expect(pool.size).toBe(30)
    for (let i = 0; i < 30; i++) {
      expect(pool._debug_hasEntry(`s${i}`)).toBe(true)
    }
  })

  // ── Pane close (checkin) retains entry ─────────────────────────────

  it('pane close equivalent (checkin) retains warm entry', () => {
    const container = fakeEl()
    const l = pool.checkout(defId('s1'), defPrefs(), container, noopCbs())
    pool.checkin(l)
    expect(pool.size).toBe(1)
    expect(pool._debug_hasEntry('s1')).toBe(true)
    expect(terminalDisposeCount).toBe(0)
  })

  // ── Reconnect resize ───────────────────────────────────────────────

  it('checkout sends resize on open WS, checkin sends none, warm checkout sends resize', () => {
    const container = fakeEl()
    // First checkout creates entry with CONNECTING WS
    const l1 = pool.checkout(defId('s1'), defPrefs(), container, noopCbs())
    const entry = pool._debug_entry(l1.key)
    const ws = entry?.ws as unknown as FakeWebSocket
    // Open the socket, which should trigger pending resize from onopen
    ws?._open()
    expect(socketSendCount).toBeGreaterThan(0)

    // Checkin — no resize sent
    pool.checkin(l1)
    const scAfterCheckin = socketSendCount

    // Second checkout (warm) — WS is already OPEN, checkout sends resize
    const l2 = pool.checkout(defId('s1'), defPrefs(), container, noopCbs())
    expect(socketSendCount).toBeGreaterThan(scAfterCheckin)
  })

  // ── Replay & sync helpers ───────────────────────────────────────────

  function openSession(name = 's1', backend?: string) {
    const container = fakeEl()
    const id = defId(name, undefined, backend)
    const l = pool.checkout(id, defPrefs(), container, noopCbs())
    const entry = pool._debug_entry(l.key)!
    const ws = entry.ws as unknown as FakeWebSocket
    ws._open()
    const term = entry.terminal as unknown as FakeTerminal
    term.writes = []
    return { l, entry, ws, term }
  }

  function currentEntry(name: string) {
    const entry = pool._debug_entry(name)!
    return {
      entry,
      ws: entry.ws as unknown as FakeWebSocket,
      term: entry.terminal as unknown as FakeTerminal,
    }
  }

  // ── Replay buffering ───────────────────────────────────────────────

  it('wsUrl includes replay=1 for daemon backend', () => {
    const { ws } = openSession('s1', 'daemon')
    expect(ws.url).toContain('/ws/daemon-session')
    expect(ws.url).toContain('replay=1')
  })

  it('wsUrl includes replay=1 for default session', () => {
    const { ws } = openSession('s1')
    expect(ws.url).toContain('/ws/session')
    expect(ws.url).toContain('replay=1')
  })

  it('wsUrl does NOT include replay=1 for direct-session', () => {
    const { ws } = openSession('direct-pty:test')
    expect(ws.url).toContain('/ws/direct-session')
    expect(ws.url).not.toContain('replay=1')
  })

  it('replay buffers binary and writes once on replay-end', () => {
    const { ws, term } = openSession('s1')
    ws._message(JSON.stringify({ type: 'replay-start' }))
    ws._message(new Uint8Array([0x01, 0x02]).buffer)
    ws._message(new Uint8Array([0x03, 0x04]).buffer)
    expect(term.writes.length).toBe(0)
    ws._message(JSON.stringify({ type: 'replay-end' }))
    expect(term.writes.length).toBe(1)
    const written = term.writes[0].data
    expect(written).toBeInstanceOf(Uint8Array)
    expect([...(written as Uint8Array)]).toEqual([1, 2, 3, 4])
  })

  it('no replay-start -> binary passthrough after fallback timer', () => {
    vi.useFakeTimers()
    const { ws, term } = openSession('s1')
    // Without replay-start, data before the fallback timer is held as
    // replay candidates; after the timer fires it is flushed and subsequent
    // bytes are written immediately.
    ws._message(new Uint8Array([0x01]).buffer)
    const writesBefore = term.writes.length
    vi.advanceTimersByTime(250)
    expect(term.writes.length).toBeGreaterThanOrEqual(writesBefore)
    ws._message(new Uint8Array([0x02]).buffer)
    const payloads = term.writes.map(w => [...(w.data as Uint8Array)])
    expect(payloads.flat()).toContain(2)
    vi.useRealTimers()
  })

  it('late replay-start after fallback is ignored and bytes passthrough', () => {
    vi.useFakeTimers()
    const { ws, term } = openSession('s1')
    vi.advanceTimersByTime(250)
    ws._message(JSON.stringify({ type: 'replay-start' }))
    ws._message(new Uint8Array([0x05]).buffer)
    expect(term.writes.length).toBe(1)
    expect([...(term.writes[0].data as Uint8Array)]).toEqual([5])
    vi.useRealTimers()
  })

  it('reconnect resets replay state and requires fresh replay-start', () => {
    vi.useFakeTimers()
    const { ws } = openSession('s1')
    ws._message(JSON.stringify({ type: 'replay-start' }))
    ws._message(new Uint8Array([0x09]).buffer)
    const oldConnId = pool._debug_entry('s1')!.connId

    // Simulate reconnect by closing and advancing reconnect timer
    ws.close()
    vi.advanceTimersByTime(2500)

    const { ws: ws2, term } = currentEntry('s1')
    expect(pool._debug_entry('s1')!.connId).not.toBeUndefined()
    ws2._open()
    expect(term.writes.length).toBe(0)
    ws2._message(new Uint8Array([0x0a]).buffer)
    vi.advanceTimersByTime(250)
    expect(term.writes.length).toBe(1)
    expect([...(term.writes[0].data as Uint8Array)]).toEqual([0x0a])
    vi.useRealTimers()
  })

  it('replay buffer cap flushes immediately and arms passthrough', () => {
    vi.useFakeTimers()
    const debugSpy = vi.spyOn(console, 'debug').mockImplementation(() => {})
    const { ws, term } = openSession('s1')
    ws._message(JSON.stringify({ type: 'replay-start' }))
    const chunk = new Uint8Array(MAX_REPLAY_BUFFER_BYTES + 1)
    ws._message(chunk.buffer)
    expect(term.writes.length).toBe(1)
    expect((term.writes[0].data as Uint8Array).length).toBe(MAX_REPLAY_BUFFER_BYTES + 1)
    expect(debugSpy).toHaveBeenCalledWith('[termyard-replay] 32MB cap exceeded, passthrough')
    // After cap, bytes should be written immediately (passthroughArmed).
    ws._message(new Uint8Array([0x07]).buffer)
    expect(term.writes.length).toBe(2)
    expect([...(term.writes[1].data as Uint8Array)]).toEqual([7])
    debugSpy.mockRestore()
    vi.useRealTimers()
  })

  // ── BSU/ESU synchronized output ────────────────────────────────────

  it('BSU/ESU wraps content into a single write with markers stripped', () => {
    const { ws, term } = openSession('s1')
    vi.useFakeTimers()
    vi.advanceTimersByTime(250) // arm passthrough so bytes are live
    const bsu = new Uint8Array([0x1b, 0x5b, 0x3f, 0x32, 0x30, 0x32, 0x36, 0x68])
    const chunkA = new Uint8Array([0x61, 0x62])
    const chunkB = new Uint8Array([0x63, 0x64])
    const esu = new Uint8Array([0x1b, 0x5b, 0x3f, 0x32, 0x30, 0x32, 0x36, 0x6c])
    ws._message(concatU8([bsu, chunkA]).buffer)
    ws._message(concatU8([chunkB, esu]).buffer)
    vi.advanceTimersByTime(0)
    const syncWrites = term.writes.filter(w => w.data instanceof Uint8Array && (w.data as Uint8Array).length > 0)
    expect(syncWrites.length).toBe(1)
    expect([...(syncWrites[0].data as Uint8Array)]).toEqual([0x61, 0x62, 0x63, 0x64])
    vi.useRealTimers()
  })

  it('BSU marker straddling chunk boundaries is reassembled', () => {
    const { ws, term } = openSession('s1')
    vi.useFakeTimers()
    vi.advanceTimersByTime(250)
    const part1 = new Uint8Array([0x1b, 0x5b, 0x3f, 0x32, 0x30, 0x32, 0x30]) // \x1b[?2020 — not a marker yet, but ends with \x1b[?202 prefix tail
    const part2prefix = new Uint8Array([0x1b, 0x5b, 0x3f, 0x32, 0x30, 0x32, 0x36, 0x68]) // \x1b[?2026h
    const content = new Uint8Array([0x70, 0x71])
    const esu = new Uint8Array([0x1b, 0x5b, 0x3f, 0x32, 0x30, 0x32, 0x36, 0x6c])
    ws._message(part1.buffer)
    ws._message(concatU8([part2prefix, content, esu]).buffer)
    vi.advanceTimersByTime(0)
    const syncWrites = term.writes.filter(w => w.data instanceof Uint8Array && (w.data as Uint8Array).length > 0)
    // First write carries \x1b[?2020 preceding bytes (marker prefix that turned out not to be a marker).
    expect(syncWrites.length).toBeGreaterThanOrEqual(1)
    expect([...(syncWrites[syncWrites.length - 1].data as Uint8Array)]).toEqual([0x70, 0x71])
    vi.useRealTimers()
  })

  it('ESU without prior BSU is a no-op and bytes pass through', () => {
    const { ws, term } = openSession('s1')
    vi.useFakeTimers()
    vi.advanceTimersByTime(250)
    const payload = new Uint8Array([0x1b, 0x5b, 0x3f, 0x32, 0x30, 0x32, 0x36, 0x6c, 0x77])
    ws._message(payload.buffer)
    vi.advanceTimersByTime(0)
    expect(term.writes.length).toBe(1)
    expect([...(term.writes[0].data as Uint8Array)]).toEqual([0x77])
    vi.useRealTimers()
  })

  it('BSU marker straddling chunk boundaries: no marker bytes leaked', () => {
    const { ws, term } = openSession('s1')
    vi.useFakeTimers()
    vi.advanceTimersByTime(250)
    // part1 ends with a partial prefix tail that is NOT a valid marker prefix,
    // so it must be flushed as plain output. The real marker starts fresh in part2.
    const prefixTail = new Uint8Array([0x1b, 0x5b, 0x3f])
    const part1 = concatU8([new Uint8Array([0x70, 0x71]), prefixTail])
    const part2prefix = new Uint8Array([0x32, 0x30, 0x32, 0x36, 0x68]) // completes BSU
    const content = new Uint8Array([0x72, 0x73])
    const esu = new Uint8Array([0x1b, 0x5b, 0x3f, 0x32, 0x30, 0x32, 0x36, 0x6c])
    ws._message(part1.buffer)
    ws._message(concatU8([part2prefix, content, esu]).buffer)
    vi.advanceTimersByTime(0)
    const syncWrites = term.writes.filter(w => w.data instanceof Uint8Array && (w.data as Uint8Array).length > 0)
    expect(syncWrites.length).toBeGreaterThanOrEqual(1)
    expect([...(syncWrites[syncWrites.length - 1].data as Uint8Array)]).toEqual([0x72, 0x73])
    const nonMarkerBytes = term.writes
      .filter(w => w.data instanceof Uint8Array)
      .flatMap(w => [...(w.data as Uint8Array)])
    expect(nonMarkerBytes.filter(b => b === 0x1b)).toEqual([])
    vi.useRealTimers()
  })

  it('partial marker prefix straddle is reassembled without leaking tail', () => {
    const { ws, term } = openSession('s1')
    vi.useFakeTimers()
    vi.advanceTimersByTime(250)
    const markerPrefix = new Uint8Array([0x1b, 0x5b, 0x3f, 0x32, 0x30, 0x32])
    for (let partialLen = 1; partialLen <= markerPrefix.length; partialLen++) {
      const chunk1 = concatU8([new Uint8Array([0x4c, 0x4f, 0x47, 0x49, 0x4e]), markerPrefix.subarray(0, partialLen)])
      const chunk2 = concatU8([markerPrefix.subarray(partialLen), new Uint8Array([0x36, 0x68, 0x57, 0x4f, 0x52, 0x4c, 0x44])])
      const esu = new Uint8Array([0x1b, 0x5b, 0x3f, 0x32, 0x30, 0x32, 0x36, 0x6c])
      term.writes = []
      ws._message(chunk1.buffer)
      ws._message(concatU8([chunk2, esu]).buffer)
      vi.advanceTimersByTime(0)
      const bytes = term.writes.filter(w => w.data instanceof Uint8Array).flatMap(w => [...(w.data as Uint8Array)])
      expect(bytes).toEqual([0x4c, 0x4f, 0x47, 0x49, 0x4e, 0x57, 0x4f, 0x52, 0x4c, 0x44])
      for (const w of term.writes) {
        const b = w.data instanceof Uint8Array ? w.data : new TextEncoder().encode(String(w.data))
        for (let i = 0; i <= b.length - 3; i++) {
          if (b[i] === 0x1b && b[i + 1] === 0x5b && b[i + 2] === 0x3f) {
            throw new Error(`leaked marker prefix in write at offset ${i}`)
          }
        }
      }
    }
    vi.useRealTimers()
  })

  it('ESU straddling chunk boundaries ends sync normally', () => {
    const { ws, term } = openSession('s1')
    vi.useFakeTimers()
    vi.advanceTimersByTime(250)
    const bsu = new Uint8Array([0x1b, 0x5b, 0x3f, 0x32, 0x30, 0x32, 0x36, 0x68])
    const content = new Uint8Array([0x78, 0x79])
    const esuPart1 = new Uint8Array([0x1b, 0x5b])
    const esuPart2 = new Uint8Array([0x3f, 0x32, 0x30, 0x32, 0x36, 0x6c])
    ws._message(bsu.buffer)
    ws._message(content.buffer)
    ws._message(esuPart1.buffer)
    ws._message(esuPart2.buffer)
    vi.advanceTimersByTime(0)
    const syncWrites = term.writes.filter(w => w.data instanceof Uint8Array)
    expect(syncWrites.length).toBe(1)
    expect([...(syncWrites[0].data as Uint8Array)]).toEqual([0x78, 0x79])
    vi.useRealTimers()
  })

  it('carryover is consumed and next plain chunk writes normally', () => {
    const { ws, term } = openSession('s1')
    vi.useFakeTimers()
    vi.advanceTimersByTime(250)
    const chunk1 = concatU8([
      new Uint8Array([0x41]),
      new Uint8Array([0x1b, 0x5b, 0x3f, 0x32, 0x30]),
    ])
    const chunk2 = concatU8([
      new Uint8Array([0x32, 0x36, 0x68, 0x42]),
      new Uint8Array([0x1b, 0x5b, 0x3f, 0x32, 0x30, 0x32, 0x36, 0x6c]),
    ])
    const chunk3 = new Uint8Array([0x43])
    term.writes = []
    ws._message(chunk1.buffer)
    ws._message(chunk2.buffer)
    ws._message(chunk3.buffer)
    vi.advanceTimersByTime(0)
    const bytes = term.writes.filter(w => w.data instanceof Uint8Array).flatMap(w => [...(w.data as Uint8Array)])
    expect(bytes).toEqual([0x41, 0x42, 0x43])
    const entry = pool._debug_entry('s1')!
    expect(entry.syncCarryover).toBeNull()
    vi.useRealTimers()
  })
})

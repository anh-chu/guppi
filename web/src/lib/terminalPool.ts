//
// terminalPool.ts — Frontend-owned terminal instance pool (facade).
//
// Checkout/checkin lease behavior is still owned here, but connection
// lifecycle, replay assembly, and unavoidable xterm internals are delegated
// to focused modules:
//   - connectionMachine.ts: WebSocket state machine and timers
//   - replayBuffer.ts: bounded replay byte assembly
//   - xtermCompat.ts: the only _core access boundary
//

import { Terminal } from '@xterm/xterm'
import { FitAddon } from '@xterm/addon-fit'
import { WebLinksAddon } from '@xterm/addon-web-links'
import { ClipboardAddon, type IClipboardProvider, type ClipboardSelectionType } from '@xterm/addon-clipboard'
import { WebglAddon } from '@xterm/addon-webgl'
import { ImageAddon } from '@xterm/addon-image'
import { UnicodeGraphemesAddon } from '@xterm/addon-unicode-graphemes'
import { PredictiveEcho } from './predictive-echo'
import { getXtermTheme } from '../theme'
import { ConnectionMachine } from './terminal/connectionMachine'
import { ReplayBuffer } from './terminal/replayBuffer'
import { neutralizeXtermScrollbarFallback, measureXtermCharSize } from './terminal/xtermCompat'
import { transferNode } from './pip'

// Re-export the public contracts owned by the submodules so existing callers
// keep working without importing through the submodule path.
export { MAX_REPLAY_BUFFER_BYTES, concatU8 } from './terminal/replayBuffer'

// Allow tests to inject a different transfer primitive.
let _transferNode: ((node: HTMLElement, dest: HTMLElement) => { crossedDocument: boolean }) | null = transferNode
export function __injectTransferNode(
  fn: (node: HTMLElement, dest: HTMLElement) => { crossedDocument: boolean },
) {
  _transferNode = fn
}

// --- internal helpers --------------------------------------------------

// Clipboard helpers
let pendingClipboard: string | null = null

function execCommandCopy(text: string): boolean {
  const ta = document.createElement('textarea')
  ta.value = text
  ta.style.position = 'fixed'
  ta.style.left = '-9999px'
  ta.style.opacity = '0'
  document.body.appendChild(ta)
  ta.select()
  let ok = false
  try {
    ok = document.execCommand('copy')
  } catch { /* ignored */ }
  document.body.removeChild(ta)
  return ok
}

export function normalizeSelection(text: string): string {
  return text.replace(/[ \t]+$/gm, '')
}

function copyToClipboard(text: string): Promise<void> {
  text = normalizeSelection(text)
  if (navigator.clipboard) {
    return navigator.clipboard.writeText(text).catch(() => {
      if (!execCommandCopy(text)) {
        pendingClipboard = text
      }
    })
  }
  if (!execCommandCopy(text)) {
    pendingClipboard = text
  }
  return Promise.resolve()
}

function flushPendingClipboard(): void {
  if (pendingClipboard !== null) {
    const text = pendingClipboard
    pendingClipboard = null
    if (navigator.clipboard) {
      navigator.clipboard.writeText(text).catch(() => {
        if (!execCommandCopy(text)) {
          pendingClipboard = text
        }
      })
    } else if (!execCommandCopy(text)) {
      pendingClipboard = text
    }
  }
}

function requestClipboardPermission(): void {
  navigator.permissions?.query({ name: 'clipboard-write' as PermissionName }).catch(() => {})
}

const clipboardProvider: IClipboardProvider = {
  readText(selection: ClipboardSelectionType): Promise<string> {
    if (selection !== 'c') return Promise.resolve('')
    return navigator.clipboard?.readText?.() ?? Promise.resolve('')
  },
  writeText(selection: ClipboardSelectionType, text: string): Promise<void> {
    if (selection !== 'c') return Promise.resolve()
    return copyToClipboard(text)
  },
}

const MAX_PASTED_FILE_BYTES = 10 * 1024 * 1024

export function concatU8Legacy(parts: Uint8Array[]): Uint8Array {
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

function indexOfU8(haystack: Uint8Array, needle: Uint8Array, start = 0): number {
  if (needle.length === 0) return start
  outer: for (let i = start; i <= haystack.length - needle.length; i++) {
    for (let j = 0; j < needle.length; j++) {
      if (haystack[i + j] !== needle[j]) continue outer
    }
    return i
  }
  return -1
}

// Shared prefix of the DEC mode 2026 synchronized-update markers.
// BSU = \x1b[?2026h, ESU = \x1b[?2026l. The 6-byte prefix is shared; the
// 7th byte disambiguates start (0x68) vs end (0x6c).
const SYNC_MARKER_PREFIX = new Uint8Array([0x1b, 0x5b, 0x3f, 0x32, 0x30, 0x32, 0x36])

function bytesToBase64(bytes: Uint8Array): string {
  let binary = ''
  const chunkSize = 0x8000
  for (let i = 0; i < bytes.length; i += chunkSize) {
    const chunk = bytes.subarray(i, i + chunkSize)
    binary += String.fromCharCode(...chunk)
  }
  return btoa(binary)
}

async function sendPastedImage(
  ws: WebSocket,
  file: File,
  fallbackType: string,
  entry: PoolEntry,
  gen: number,
): Promise<void> {
  if (file.size > MAX_PASTED_FILE_BYTES) {
    return
  }
  const buffer = await file.arrayBuffer()
  // Re-validate lease after async gap — entry may have been checked in/out
  if (entry.generation !== gen || entry.connection.socket !== ws || ws.readyState !== WebSocket.OPEN) return
  ws.send(JSON.stringify({
    type: 'paste-image',
    data: bytesToBase64(new Uint8Array(buffer)),
    mime: file.type || fallbackType,
    filename: file.name,
  }))
}

// Scroll-preserving fit.
//
// fitAddon.fit() reflows rows and can move the viewport, so we restore the
// user's prior scroll anchor afterward.
//
// CRITICAL: we decide whether to preserve a non-bottom offset using
// entry.userScrolled (set only by real wheel/touch/scrollbar gestures), NOT
// buffer geometry. xterm updates viewportY asynchronously during rapid writes
// (spinner/redraw animations), so capturing `baseY - viewportY` mid-frame
// reads a stale in-between state and "restores" the terminal to a phantom
// offset, pinning it mid-history and flashing on every redraw.
function fitPreservingScroll(
  entry: PoolEntry,
  container: HTMLElement,
  opts?: { refreshAfter?: boolean },
): void {
  const term = entry.terminal
  const fitAddon = entry.fitAddon
  if (container.clientWidth <= 0 || container.clientHeight <= 0) return

  const myEpoch = ++entry.fitEpoch
  const buf = term.buffer.active
  // Only preserve a real user offset. geometry-based distFromBottom is
  // unreliable during async writes.
  const userOffset = entry.userScrolled ? Math.max(0, buf.baseY - buf.viewportY) : 0

  const isStale = () => entry.fitEpoch !== myEpoch

  neutralizeXtermScrollbarFallback(term)
  fitAddon.fit()

  if (opts?.refreshAfter) {
    try { term.refresh(0, term.rows - 1) } catch { /* renderer dispose race */ }
  }

  if (userOffset === 0) {
    // Following output: pin to bottom once. xterm keeps it there on writes.
    try { term.scrollToBottom() } catch { /* renderer dispose race */ }
    const forceDOM = () => {
      if (isStale()) return
      const vp = container.querySelector('.xterm-viewport') as HTMLElement | null
      if (vp && vp.scrollTop + vp.clientHeight < vp.scrollHeight - 5) {
        vp.scrollTop = vp.scrollHeight
      }
    }
    requestAnimationFrame(forceDOM)
    return
  }

  // User is genuinely scrolled up: restore the captured offset, but only
  // across a single deferred frame (xterm recomputes viewport async).
  const restoreOnce = () => {
    if (isStale() || !entry.userScrolled) return
    try {
      const after = term.buffer.active
      if (userOffset > after.baseY) { term.scrollToBottom(); return }
      const target = after.baseY - userOffset
      const delta = target - after.viewportY
      if (delta !== 0) term.scrollLines(delta)
    } catch { /* renderer dispose race */ }
  }
  restoreOnce()
  requestAnimationFrame(restoreOnce)
}

// --- Types -------------------------------------------------------------

/** Connection/modifier snapshot published synchronously on checkout. */
export interface ConnectionSnapshot {
  connected: boolean
  ctrlModifierActive: boolean
  altModifierActive: boolean
}

/** Lease token: exclusive ownership for one React wrapper. */
export interface LeaseToken {
  /** Monotonic generation number for this entry. */
  generation: number
  /** Pool key this lease belongs to. */
  key: string
}

/** Callbacks the active wrapper subscribes to. */
export interface CheckoutCallbacks {
  onConnectionChange: (connected: boolean) => void
  onCtrlModifierChange: (active: boolean) => void
  onAltModifierChange: (active: boolean) => void
  onSelectionMenu: (menu: { x: number; y: number; text: string } | null) => void
}

/** Terminal preferences relevant to the pool. */
export interface TerminalPrefs {
  theme: string
  fontFamily: string
  fontSize: number
  scrollback: number
  renderer: string
  unicodeGraphemes: boolean
  predictiveEcho: boolean
}

/** Identity for a pool entry. */
export interface PoolIdentity {
  /** Display label / legacy route name. Never part of the canonical key. */
  sessionName: string
  hostId?: string
  backend?: string
  /** Stable identity (v2): immutable session ref fields. */
  sessionId?: string
  ownerId?: string
  /** Daemon generation for generation-gated attach; empty = no gate. */
  generation?: string
}

/** Factory callbacks for test injection. */
export interface PoolFactory {
  createTerminal: (options: any) => Terminal
  createFitAddon: () => FitAddon
  createWebLinksAddon: () => WebLinksAddon
  createClipboardAddon: (provider?: IClipboardProvider) => ClipboardAddon
  createWebglAddon: () => WebglAddon | null
  createImageAddon: () => ImageAddon | null
  createUnicodeGraphemesAddon: () => UnicodeGraphemesAddon | null
  createPredictiveEcho: (term: Terminal) => PredictiveEcho | null
  createWebSocket: (url: string) => WebSocket
}

const defaultFactory: PoolFactory = {
  createTerminal: (options) => new Terminal(options),
  createFitAddon: () => new FitAddon(),
  createWebLinksAddon: () => new WebLinksAddon(),
  createClipboardAddon: (provider) => new ClipboardAddon(undefined, provider),
  createWebglAddon: () => {
    try { return new WebglAddon() } catch { return null }
  },
  createImageAddon: () => {
    try {
      return new ImageAddon({
        pixelLimit: 4_000_000,
        sixelSupport: true,
        sixelSizeLimit: 8_000_000,
        storageLimit: 24,
        showPlaceholder: true,
        iipSupport: false,
      })
    } catch { return null }
  },
  createUnicodeGraphemesAddon: () => {
    try { return new UnicodeGraphemesAddon() } catch { return null }
  },
  createPredictiveEcho: (term) => {
    try { return new PredictiveEcho(term) } catch { return null }
  },
  createWebSocket: (url) => new WebSocket(url),
}

// --- Pool entry state --------------------------------------------------

interface PoolEntry {
  // Identity
  key: string
  identity: PoolIdentity

  // Core resources
  terminal: Terminal
  fitAddon: FitAddon
  webglAddon: WebglAddon | null
  imageAddon: ImageAddon | null
  graphemesAddon: UnicodeGraphemesAddon | null
  graphemesLoaded: boolean
  predictiveEcho: PredictiveEcho | null
  predictiveEchoEnabled: boolean

  // Delegates
  connection: ConnectionMachine
  replayBuffer: ReplayBuffer

  // Connection
  connected: boolean

  // Lease
  generation: number

  // Fit-scheduling epoch. Incremented on every fit() and on user scroll so
  // stale deferred scroll-restore callbacks from a prior fit can no-op.
  fitEpoch: number

  // True only when the user has taken control of the viewport via a real
  // gesture (wheel, touch, scrollbar drag). Buffer geometry is NOT a reliable
  // signal: xterm updates viewportY asynchronously during rapid writes.
  userScrolled: boolean

  // Active container state
  activeContainer: HTMLElement | null
  activeCallbacks: CheckoutCallbacks | null
  listenerCleanup: (() => void) | null

  // Hidden host
  hiddenHost: HTMLElement | null

  // Dimensions
  lastCols: number
  lastRows: number
  pendingResizeOnOpen: boolean

  // Modifier state
  ctrlModifierActive: boolean
  altModifierActive: boolean
  suppressedInput: string | null

  // Selection
  selectionMenu: { x: number; y: number; text: string } | null

  // Applied preferences
  appliedPrefs: TerminalPrefs | null

  // Live output sync markers
  syncCarryover: Uint8Array | null
  syncActive: boolean
  syncBuffer: Uint8Array[]

  // Echo/write coordination
  writePending: boolean
}

// --- Pool singleton ----------------------------------------------------

export class TerminalPool {
  private entries = new Map<string, PoolEntry>()
  private factory: PoolFactory
  private poolRoot: HTMLElement | null = null

  constructor(factory?: PoolFactory) {
    this.factory = factory ?? defaultFactory
  }

  // ── public API: key management ──────────────────────────────────────

  /** Canonical key from session identity. */
  static keyFor(sessionName: string, hostId?: string): string {
    return hostId ? `${hostId}/${sessionName}` : sessionName
  }

  keyFor(sessionName: string, hostId?: string): string {
    return TerminalPool.keyFor(sessionName, hostId)
  }

  /** Synchronous snapshot lookup without side effects. */
  getSnapshot(key: string): ConnectionSnapshot | null {
    const entry = this.entries.get(key)
    if (!entry) return null
    return {
      connected: entry.connected,
      ctrlModifierActive: entry.ctrlModifierActive,
      altModifierActive: entry.altModifierActive,
    }
  }

  /** Return entries count (for tests). */
  get size(): number {
    return this.entries.size
  }

  /** All current keys (for tests). */
  get keys(): IterableIterator<string> {
    return this.entries.keys()
  }

  // ── public API: checkout / checkin ──────────────────────────────────

  checkout(
    identity: PoolIdentity,
    prefs: TerminalPrefs,
    container: HTMLElement,
    callbacks: CheckoutCallbacks,
    factory?: PoolFactory,
  ): LeaseToken {
    // Canonical pool identity is the immutable SessionRef (sessionId/ownerId),
    // NOT the display label. A rename must never rekey, dispose, or reconnect
    // an open terminal; label-only changes hit the same pool entry.
    
    // Guard: v2 session with missing sessionId is an invariant violation.
    // If the caller provides ownerId or generation (v2 identity intent), sessionId
    // MUST be present. Do not silently fall back to name-based attach.
    if (identity.backend === 'daemon' && (identity.ownerId || identity.generation) && !identity.sessionId) {
      const msg = `v2 session requires sessionId: ownerId=${identity.ownerId}, generation=${identity.generation}, sessionName=${identity.sessionName}`
      console.error(msg)
      throw new Error(msg)
    }
    
    const canonical = identity.sessionId
      ? TerminalPool.keyFor(identity.sessionId, identity.ownerId ?? identity.hostId)
      : TerminalPool.keyFor(identity.sessionName, identity.hostId)
    const key = canonical
    const ef = factory ?? this.factory
    let entry = this.entries.get(key)

    // Dispose & recreate if backend identity changed for same key.
    if (entry) {
      const backendChanged = entry.identity.backend !== (identity.backend ?? undefined)
      if (backendChanged) {
        this.disposeEntry(entry)
        this.entries.delete(key)
        entry = undefined
      }
    }

    // Cold create
    if (!entry) {
      entry = this.createEntry(key, identity, prefs, ef)
      this.entries.set(key, entry)
    } else {
      // Warm checkout: reconcile generation change if present.
      // If the caller provided a new generation (v2 recovery/restart), reconnect the socket.
      if (identity.generation && entry.identity.generation !== identity.generation) {
        this.updateGeneration(key, identity.generation)
      }
    }

    // Increment lease — invalidate any previous owner
    entry.generation++
    const lease: LeaseToken = { generation: entry.generation, key }

    // Remove old foreground listeners
    if (entry.listenerCleanup) {
      entry.listenerCleanup()
      entry.listenerCleanup = null
    }
    entry.activeCallbacks = callbacks

    // Bind terminal to the foreground container.
    const term = entry.terminal
    const root = term.element as HTMLElement | undefined
    if (root) {
      // Already opened: move the existing DOM node into the new container first.
      (_transferNode ?? transferNode)(root, container)
    }
    try { term.open(container) } catch { /* ignored */ }
    neutralizeXtermScrollbarFallback(term)

    entry.activeContainer = container

    // Attach foreground listeners
    this.attachListeners(entry)

    // Reconcile preferences
    this.reconcilePrefs(entry, prefs)

    // Fit and resize
    if (container.clientWidth > 0 && container.clientHeight > 0) {
      fitPreservingScroll(entry, container, { refreshAfter: true })
      if (entry.connection.connected) {
        const ws = entry.connection.socket
        if (ws && ws.readyState === WebSocket.OPEN) {
          const { cols, rows } = entry.terminal
          ws.send(JSON.stringify({ type: 'resize', cols, rows }))
          entry.lastCols = cols
          entry.lastRows = rows
          entry.pendingResizeOnOpen = false
        }
      } else {
        entry.pendingResizeOnOpen = true
      }
    }

    // Deferred refresh: xterm's IntersectionObserver pauses rendering while the
    // terminal element is off-screen. Schedule a second fit+refresh in rAF so
    // the render fires once the observer has cleared the pause.
    const deferredKey = lease.key
    const deferredGen = entry.generation
    window.requestAnimationFrame(() => {
      const e = this.entries.get(deferredKey)
      if (!e || e.generation !== deferredGen || !e.activeContainer) return
      const c = e.activeContainer
      if (c.clientWidth > 0 && c.clientHeight > 0) {
        fitPreservingScroll(e, c, { refreshAfter: true })
      } else {
        try { e.terminal.refresh(0, e.terminal.rows - 1) } catch { /* ignored */ }
      }
    })

    // Publish initial snapshot
    callbacks.onConnectionChange(entry.connected)
    callbacks.onCtrlModifierChange(entry.ctrlModifierActive)
    callbacks.onAltModifierChange(entry.altModifierActive)

    return lease
  }

  checkin(lease: LeaseToken): void {
    const entry = this.entries.get(lease.key)
    if (!entry) return
    if (entry.generation !== lease.generation) return

    // Invalidate lease so stale token can't operate after checkin
    entry.generation++

    // Mark inactive
    entry.activeCallbacks = null
    entry.activeContainer = null

    // Remove foreground listeners
    if (entry.listenerCleanup) {
      entry.listenerCleanup()
      entry.listenerCleanup = null
    }

    // Capture dimensions from foreground before moving
    if (entry.terminal.element) {
      entry.lastCols = entry.terminal.cols
      entry.lastRows = entry.terminal.rows
    }

    // Move root to hidden host using shared transfer primitive
    const root = entry.terminal.element as HTMLElement | undefined
    if (root) {
      const host = this.ensureHiddenHost(entry)
      const { crossedDocument } = (_transferNode ?? transferNode)(root, host)

      // Cross-document reopen
      if (crossedDocument) {
        try { entry.terminal.open(host) } catch { /* ignored */ }
      }
    }
  }

  // ── public API: lease-gated operations ──────────────────────────────

  fit(lease: LeaseToken): void {
    const entry = this.validateLease(lease)
    if (!entry || !entry.activeContainer) return
    const container = entry.activeContainer
    if (container.clientWidth <= 0 || container.clientHeight <= 0) return
    fitPreservingScroll(entry, container, { refreshAfter: true })
  }

  focus(lease: LeaseToken): void {
    const entry = this.validateLease(lease)
    if (!entry) return
    try { entry.terminal.focus() } catch { /* ignored */ }
  }

  rehost(lease: LeaseToken, container: HTMLElement, forceRebind?: boolean): void {
    const entry = this.validateLease(lease)
    if (!entry) return

    const root = entry.terminal.element as HTMLElement | undefined
    if (!root) return

    const { crossedDocument } = (_transferNode ?? transferNode)(root, container)

    if (crossedDocument || forceRebind) {
      try { entry.terminal.open(container) } catch { /* ignored */ }
      neutralizeXtermScrollbarFallback(entry.terminal)
    }

    entry.activeContainer = container
  }

  sendRawBytes(lease: LeaseToken, bytes: Uint8Array): void {
    const entry = this.validateLease(lease)
    if (!entry) return
    const ws = entry.connection.socket
    if (ws && ws.readyState === WebSocket.OPEN) {
      ws.send(bytes)
    }
  }

  sendText(lease: LeaseToken, text: string): void {
    if (!text) return
    this.sendRawBytes(lease, new TextEncoder().encode(text))
  }

  async sendImage(lease: LeaseToken, file: File, fallbackType: string): Promise<void> {
    const entry = this.validateLease(lease)
    if (!entry) return
    const ws = entry.connection.socket
    if (!ws || ws.readyState !== WebSocket.OPEN) return
    const gen = lease.generation
    try {
      await sendPastedImage(ws, file, fallbackType, entry, gen)
    } catch (err) {
      // Swallow; failed image paste should not crash the terminal.
    }
  }

  toggleCtrlModifier(lease: LeaseToken): void {
    const entry = this.validateLease(lease)
    if (!entry) return
    entry.ctrlModifierActive = !entry.ctrlModifierActive
    entry.activeCallbacks?.onCtrlModifierChange(entry.ctrlModifierActive)
  }

  clearCtrlModifier(lease: LeaseToken): void {
    const entry = this.validateLease(lease)
    if (!entry) return
    entry.ctrlModifierActive = false
    entry.activeCallbacks?.onCtrlModifierChange(false)
  }

  toggleAltModifier(lease: LeaseToken): void {
    const entry = this.validateLease(lease)
    if (!entry) return
    entry.altModifierActive = !entry.altModifierActive
    entry.activeCallbacks?.onAltModifierChange(entry.altModifierActive)
  }

  clearAltModifier(lease: LeaseToken): void {
    const entry = this.validateLease(lease)
    if (!entry) return
    entry.altModifierActive = false
    entry.activeCallbacks?.onAltModifierChange(false)
  }

  setSelectionMenu(lease: LeaseToken, menu: { x: number; y: number; text: string } | null): void {
    const entry = this.validateLease(lease)
    if (!entry) return
    entry.selectionMenu = menu
    entry.activeCallbacks?.onSelectionMenu(menu)
  }

  getTerminalForPaste(lease: LeaseToken): Terminal | null {
    const entry = this.validateLease(lease)
    return entry?.terminal ?? null
  }

  // ── public API: preferences ─────────────────────────────────────────

  /** Reconcile entry against current prefs (idempotent). Call on checkout. */
  reconcilePrefs(entry: PoolEntry, prefs: TerminalPrefs): void {
    // Theme
    const xtermTheme = getXtermTheme(prefs.theme)
    entry.terminal.options.theme = xtermTheme
    try { entry.terminal.refresh(0, entry.terminal.rows - 1) } catch { /* ignored */ }

    // Font
    const fontFamily = `'${prefs.fontFamily}', 'JetBrains Mono', 'Fira Code', Menlo, Monaco, 'Inconsolata LGC Nerd Font Mono', 'DejaVu Sans Mono Symbols', monospace`
    entry.terminal.options.fontSize = prefs.fontSize
    entry.terminal.options.fontFamily = fontFamily
    measureXtermCharSize(entry.terminal)

    // Renderer
    if (prefs.renderer === 'webgl' && !entry.webglAddon) {
      const wa = this.factory.createWebglAddon()
      if (wa) {
        wa.onContextLoss(() => {
          wa.dispose()
          entry.webglAddon = null
        })
        try { entry.terminal.loadAddon(wa) } catch { /* ignored */ }
        entry.webglAddon = wa as WebglAddon
      }
    } else if (prefs.renderer === 'dom' && entry.webglAddon) {
      entry.webglAddon.dispose()
      entry.webglAddon = null
    }

    // Unicode graphemes
    if (prefs.unicodeGraphemes && !entry.graphemesLoaded) {
      const ga = this.factory.createUnicodeGraphemesAddon()
      if (ga) {
        try { entry.terminal.loadAddon(ga) } catch { /* ignored */ }
        entry.graphemesAddon = ga as UnicodeGraphemesAddon
        entry.graphemesLoaded = true
      }
    } else if (!prefs.unicodeGraphemes && entry.graphemesAddon) {
      entry.graphemesAddon.dispose()
      entry.graphemesAddon = null
      entry.graphemesLoaded = false
    }

    // Predictive echo
    if (prefs.predictiveEcho && !entry.predictiveEcho) {
      entry.predictiveEcho = this.factory.createPredictiveEcho(entry.terminal)
    } else if (!prefs.predictiveEcho && entry.predictiveEcho) {
      entry.predictiveEcho.dispose()
      entry.predictiveEcho = null
    }
    entry.predictiveEchoEnabled = prefs.predictiveEcho

    entry.appliedPrefs = { ...prefs }
  }

  /** Apply prefs to all entries (idempotent). */
  applyGlobalPrefs(prefs: TerminalPrefs): void {
    for (const entry of this.entries.values()) {
      const prevScrollback = entry.appliedPrefs?.scrollback
      const newScrollback = prefs.scrollback

      // Scrollback change triggers rebuild
      if (prevScrollback !== undefined && prevScrollback !== newScrollback) {
        // Capture identity before disposal
        const identity = entry.identity
        const key = entry.key
        const wasActive = entry.activeContainer !== null
        const prevContainer = entry.activeContainer

        this.disposeEntry(entry)
        this.entries.delete(key)

        const newEntry = this.createEntry(key, identity, prefs, this.factory)
        this.entries.set(key, newEntry)

        if (wasActive && prevContainer) {
          // Re-checkout into same container
          newEntry.generation++
          newEntry.activeContainer = prevContainer
          const root = newEntry.terminal.element as HTMLElement | undefined
          try { newEntry.terminal.open(prevContainer) } catch { /* ignored */ }
          neutralizeXtermScrollbarFallback(newEntry.terminal)
          if (root) prevContainer.appendChild(root)
          this.attachListeners(newEntry)
          // Load renderer-dependent addons (WebGL) AFTER open() above.
          this.reconcilePrefs(newEntry, prefs)
          fitPreservingScroll(newEntry, prevContainer, { refreshAfter: true })
          // Send resize
          const ws = newEntry.connection.socket
          if (ws && ws.readyState === WebSocket.OPEN) {
            const { cols, rows } = newEntry.terminal
            ws.send(JSON.stringify({ type: 'resize', cols, rows }))
          }
        }
      } else {
        this.reconcilePrefs(entry, prefs)
      }
    }
  }

  // ── public API: dispose / rekey ─────────────────────────────────────

  dispose(key: string): void {
    const entry = this.entries.get(key)
    if (!entry) return
    this.disposeEntry(entry)
    this.entries.delete(key)
  }

  /** Remove entries NOT in validKeys. skip when the server-side catalog is
   *  transient/uncertain (reconnect, empty snapshot) so one stale response
   *  cannot dispose active terminals. */
  disposeAbsent(validKeys: Set<string>, catalogUncertain = false): void {
    if (catalogUncertain) return
    for (const key of this.entries.keys()) {
      if (!validKeys.has(key)) {
        this.dispose(key)
      }
    }
  }

  /** Rename a pool entry, preserving terminal/WS. */
  rekey(oldKey: string, newKey: string): void {
    if (oldKey === newKey) return
    const entry = this.entries.get(oldKey)
    if (!entry) return

    // Dispose destination if it exists
    if (this.entries.has(newKey)) {
      this.dispose(newKey)
    }

    // Move entry to new key
    this.entries.delete(oldKey)
    const sessionName = newKey.includes('/') ? newKey.slice(newKey.indexOf('/') + 1) : newKey
    const hostId = newKey.includes('/') ? newKey.slice(0, newKey.indexOf('/')) : undefined
    const prev = entry.identity
    entry.key = newKey
    // Canonical identity is immutable (`sessionId`/`ownerId`/`generation`); a
    // label change must NOT mutate them, so preserve them across a rename and
    // only update the display key fields.
    entry.identity = {
      sessionName,
      hostId,
      backend: prev.backend,
      sessionId: prev.sessionId,
      ownerId: prev.ownerId,
      generation: prev.generation,
    }
    // Update the lease token for current owner
    entry.generation++ // invalidate previous lease
    this.entries.set(newKey, entry)
  }

  /**
   * Reconnect an existing pool entry in place after its daemon generation
   * changed (recovery/restart). Same terminal instance, scrollback, and lease
   * are preserved; only the WebSocket URL is rebuilt with the new generation.
   * No-op for unknown keys or when the generation is unchanged.
   */
  updateGeneration(key: string, generation: string): void {
    const entry = this.entries.get(key)
    if (!entry || entry.identity.generation === generation) return
    entry.identity = { ...entry.identity, generation }
    entry.connection.connect(this.buildUrl({ key, identity: entry.identity }))
  }

  /** Reset all state (for tests). */
  reset(): void {
    for (const entry of this.entries.values()) {
      this.disposeEntry(entry)
    }
    this.entries.clear()
    if (this.poolRoot) {
      this.poolRoot.remove()
      this.poolRoot = null
    }
    _transferNode = null
  }

  // ── private helpers ─────────────────────────────────────────────────

  private validateLease(lease: LeaseToken): PoolEntry | null {
    const entry = this.entries.get(lease.key)
    if (!entry || entry.generation !== lease.generation) return null
    return entry
  }

  private ensureHiddenHost(entry: PoolEntry): HTMLElement {
    if (entry.hiddenHost) return entry.hiddenHost

    if (!this.poolRoot) {
      const root = document.createElement('div')
      root.setAttribute('data-terminal-pool-root', '')
      root.style.position = 'fixed'
      root.style.top = '-9999px'
      root.style.left = '-9999px'
      root.style.width = '1px'
      root.style.height = '1px'
      root.style.overflow = 'hidden'
      root.style.pointerEvents = 'none'
      root.style.visibility = 'visible' // NOT display:none — keep rAF/WebGL alive
      root.style.opacity = '0'
      document.body.appendChild(root)
      this.poolRoot = root
    }

    const host = document.createElement('div')
    host.setAttribute('data-terminal-pool-host', entry.key)
    host.style.width = `${Math.max(entry.lastCols * 10, 80)}px`
    host.style.height = `${Math.max(entry.lastRows * 18, 24)}px`
    host.style.pointerEvents = 'none'
    host.style.overflow = 'hidden'
    host.style.visibility = 'visible'
    this.poolRoot.appendChild(host)
    entry.hiddenHost = host
    return host
  }

  // ── entry creation ──────────────────────────────────────────────────

  private createEntry(
    key: string,
    identity: PoolIdentity,
    prefs: TerminalPrefs,
    ef: PoolFactory,
  ): PoolEntry {
    const xtermTheme = getXtermTheme(prefs.theme)
    const fontFamily = `'${prefs.fontFamily}', 'JetBrains Mono', 'Fira Code', Menlo, Monaco, 'Inconsolata LGC Nerd Font Mono', 'DejaVu Sans Mono Symbols', monospace`

    const term = ef.createTerminal({
      theme: xtermTheme,
      fontSize: prefs.fontSize,
      fontFamily,
      cursorBlink: true,
      scrollback: prefs.scrollback,
      allowProposedApi: true,
      rightClickSelectsWord: true,
      macOptionClickForcesSelection: true,
      macOptionIsMeta: true,
    })

    const fitAddon = ef.createFitAddon()
    term.loadAddon(fitAddon)
    term.loadAddon(ef.createWebLinksAddon())
    term.loadAddon(ef.createClipboardAddon(clipboardProvider))

    // WebGL renderer is loaded lazily AFTER term.open() (xterm.js requirement).
    let webglAddon: WebglAddon | null = null

    const imageAddon = ef.createImageAddon()
    if (imageAddon) {
      try { term.loadAddon(imageAddon) } catch { /* ignored */ }
    }

    let graphemesAddon: UnicodeGraphemesAddon | null = null
    let graphemesLoaded = false
    if (prefs.unicodeGraphemes) {
      const ga = ef.createUnicodeGraphemesAddon()
      if (ga) {
        try { term.loadAddon(ga) } catch { /* ignored */ }
        graphemesAddon = ga as UnicodeGraphemesAddon
        graphemesLoaded = true
      }
    }

    let predictiveEcho: PredictiveEcho | null = null
    if (prefs.predictiveEcho) {
      predictiveEcho = ef.createPredictiveEcho(term)
    }

    const replayBuffer = new ReplayBuffer()

    // eslint-disable-next-line prefer-const
    let entry!: PoolEntry

    const connection = new ConnectionMachine(
      {
        createWebSocket: ef.createWebSocket,
        onConnecting: () => {
          entry.replayBuffer.reset()
          entry.syncActive = false
          entry.syncBuffer = []
          entry.syncCarryover = null
        },
        onConnectionChange: (connected) => this.handleConnectionChange(entry, connected),
        onOpen: () => this.handleOpen(entry),
        onBinaryMessage: (data) => this.handleBinaryMessage(entry, data),
        onTextMessage: (text) => this.handleTextControl(entry, text),
        onReplayStart: () => {
          entry.replayBuffer.reset()
          entry.syncActive = false
          entry.syncBuffer = []
          entry.syncCarryover = null
        },
        onReplayEnd: () => this.flushReplayBuffer(entry),
        onFallback: () => this.flushReplayBuffer(entry),
      },
      this.buildUrl({ key, identity }),
    )

    entry = {
      key,
      identity,

      terminal: term,
      fitAddon,
      webglAddon,
      imageAddon,
      graphemesAddon,
      graphemesLoaded,
      predictiveEcho,
      predictiveEchoEnabled: prefs.predictiveEcho,

      connection,
      replayBuffer,

      connected: false,

      generation: 0,
      fitEpoch: 0,
      userScrolled: false,

      activeContainer: null,
      activeCallbacks: null,
      listenerCleanup: null,

      hiddenHost: null,

      lastCols: 0,
      lastRows: 0,
      pendingResizeOnOpen: false,

      ctrlModifierActive: false,
      altModifierActive: false,
      suppressedInput: null,

      selectionMenu: null,

      appliedPrefs: { ...prefs },

      syncCarryover: null,
      syncActive: false,
      syncBuffer: [],

      writePending: false,
    }

    connection.connect()
    return entry
  }

  // ── connection event handlers ───────────────────────────────────────

  private buildUrl(target: { key: string; identity: PoolIdentity }): string {
    const term = this.entries.get(target.key)?.terminal
    const cols = term?.cols || 80
    const rows = term?.rows || 24

    const protocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:'
    const sessionName = target.identity.sessionName
    const hostId = target.identity.hostId
    const backend = target.identity.backend

    const hostParam = hostId ? `&host=${encodeURIComponent(hostId)}` : ''
    if (backend === 'daemon') {
      // The legacy `name` param (display label) is required by the remote/
      // peer routing path (handleRemoteSession) and doubles as the daemon key
      // for pre-v2 sessions. The immutable SessionID is emitted only when it
      // is actually known (a real v2 stable id) so legacy display names that
      // are not valid SessionIDs keep routing by name.
      const idParam = target.identity.sessionId
        ? `&sessionID=${encodeURIComponent(target.identity.sessionId)}`
        : ''
      const genParam = target.identity.generation
        ? `&generation=${encodeURIComponent(target.identity.generation)}`
        : ''
      return `${protocol}//${window.location.host}/ws/daemon-session?name=${encodeURIComponent(sessionName)}${idParam}${genParam}&cols=${cols}&rows=${rows}${hostParam}&replay=1`
    }
    if (sessionName.startsWith('direct-pty:')) {
      return `${protocol}//${window.location.host}/ws/direct-session?cols=${cols}&rows=${rows}`
    }
    return `${protocol}//${window.location.host}/ws/session?name=${encodeURIComponent(sessionName)}&cols=${cols}&rows=${rows}${hostParam}&replay=1`
  }

  private handleConnectionChange(entry: PoolEntry, connected: boolean): void {
    entry.connected = connected
    if (connected) {
      entry.activeCallbacks?.onConnectionChange(true)
      return
    }
    // Suppress the disconnected overlay while the tab is hidden; the machine
    // will reconnect automatically when visibility returns.
    if (!document.hidden) {
      entry.activeCallbacks?.onConnectionChange(false)
    }
  }

  private handleOpen(entry: PoolEntry): void {
    entry.syncActive = false
    entry.syncBuffer = []
    entry.syncCarryover = null
    entry.writePending = false

    const ws = entry.connection.socket
    if (entry.pendingResizeOnOpen && entry.activeContainer && ws) {
      entry.pendingResizeOnOpen = false
      const c = entry.terminal.cols
      const r = entry.terminal.rows
      entry.lastCols = c
      entry.lastRows = r
      try { ws.send(JSON.stringify({ type: 'resize', cols: c, rows: r })) } catch { /* ignored */ }
    }
  }

  private handleBinaryMessage(entry: PoolEntry, data: Uint8Array): void {
    if (entry.connection.state === 'replaying') {
      const flush = entry.replayBuffer.add(data)
      if (flush) {
        this.writeRaw(entry, flush.bytes)
        entry.connection.markLive()
      }
      return
    }

    this.dispatchLiveOutput(entry, data)
  }

  private handleTextControl(entry: PoolEntry, text: string): void {
    let ctrl: { type?: string } | null = null
    try {
      ctrl = JSON.parse(text)
    } catch {
      // Not JSON; write the raw string below.
    }

    if (ctrl && ctrl.type === 'pong') return
    if (ctrl && ctrl.type === 'replay-start') {
      entry.connection.startReplay()
      return
    }
    if (ctrl && ctrl.type === 'replay-end') {
      entry.connection.endReplay()
      return
    }

    // Non-control string (or unknown control): write directly.
    this.writeRaw(entry, text)
  }

  private flushReplayBuffer(entry: PoolEntry): void {
    const all = entry.replayBuffer.flush()
    if (all) {
      this.writeRaw(entry, all)
    }
  }

  // Replay / non-control output: no latency measurement; just write and scroll.
  private writeRaw(entry: PoolEntry, data: Uint8Array | string): void {
    entry.predictiveEcho?.clear()
    entry.terminal.write(data, () => {
      if (!entry.userScrolled) {
        try { entry.terminal.scrollToBottom() } catch { /* ignored */ }
      }
    })
  }

  // ── live output dispatcher (BSU/ESU) ────────────────────────────────

  // Live output dispatcher. Handles DEC mode 2026 synchronized-update
  // markers (BSU/ESU) by buffering bytes between markers and flushing them
  // as a single terminal.write. Markers are stripped. Straddling markers are
  // handled via syncCarryover.
  private dispatchLiveOutput(entry: PoolEntry, data: Uint8Array): void {
    if (entry.syncCarryover !== null) {
      data = concatU8Legacy([entry.syncCarryover, data])
      entry.syncCarryover = null
    }

    const out: Uint8Array[] = []
    let cursor = 0

    while (cursor < data.length) {
      const idx = indexOfU8(data, SYNC_MARKER_PREFIX, cursor)
      if (idx === -1) {
        const rest = data.subarray(cursor)
        const tail = this.findSyncMarkerPrefixTail(rest)
        if (tail !== null) {
          if (tail.length < rest.length) {
            out.push(rest.subarray(0, rest.length - tail.length))
          }
          entry.syncCarryover = tail
        } else {
          out.push(rest)
        }
        cursor = data.length
        break
      }

      if (idx + SYNC_MARKER_PREFIX.length >= data.length) {
        // Marker prefix runs right up to (or past) the end; carry the whole
        // tail over to the next chunk.
        out.push(data.subarray(cursor, idx))
        entry.syncCarryover = data.subarray(idx)
        cursor = data.length
        break
      }

      const markerByte = data[idx + SYNC_MARKER_PREFIX.length]
      if (markerByte !== 0x68 && markerByte !== 0x6c) {
        // Looks like the prefix but is not a valid BSU/ESU; treat as ordinary
        // bytes and continue scanning after the prefix.
        out.push(data.subarray(cursor, idx + SYNC_MARKER_PREFIX.length))
        cursor = idx + SYNC_MARKER_PREFIX.length
        continue
      }

      // Slice before the marker.
      if (idx > cursor) {
        out.push(data.subarray(cursor, idx))
      }

      if (markerByte === 0x68) {
        // Begin Synchronized Update.
        this.flushLiveSlices(entry, out)
        out.length = 0
        entry.syncActive = true
        entry.syncBuffer = []
      } else {
        // End Synchronized Update.
        if (entry.syncActive) {
          if (out.length) {
            entry.syncBuffer.push(...out)
            out.length = 0
          }
          const all = concatU8Legacy(entry.syncBuffer)
          entry.predictiveEcho?.clear()
          entry.terminal.write(all, () => {
            if (!entry.userScrolled) {
              try { entry.terminal.scrollToBottom() } catch { /* ignored */ }
            }
          })
          entry.syncBuffer = []
          entry.syncActive = false
        }
        // ESU without matching BSU is a no-op; marker stripped.
      }

      cursor = idx + SYNC_MARKER_PREFIX.length + 1
    }

    this.flushLiveSlices(entry, out)
  }

  private flushLiveSlices(entry: PoolEntry, slices: Uint8Array[]): void {
    if (slices.length === 0) return
    const data = concatU8Legacy(slices)
    slices.length = 0
    if (entry.syncActive) {
      entry.syncBuffer.push(data)
      return
    }
    this.writeLiveRaw(entry, data)
  }

  private writeLiveRaw(entry: PoolEntry, data: Uint8Array): void {
    entry.predictiveEcho?.clear()
    const hadPending = entry.writePending
    entry.writePending = false
    entry.terminal.write(data, () => {
      if (hadPending) {
        entry.writePending = false
      }
      if (!entry.userScrolled) {
        try { entry.terminal.scrollToBottom() } catch { /* ignored */ }
      }
    })
  }

  private findSyncMarkerPrefixTail(data: Uint8Array): Uint8Array | null {
    const marker = SYNC_MARKER_PREFIX
    const max = Math.min(data.length, marker.length - 1)
    for (let len = max; len >= 1; len--) {
      let ok = true
      for (let i = 0; i < len; i++) {
        if (data[data.length - len + i] !== marker[i]) {
          ok = false
          break
        }
      }
      if (ok) return data.subarray(data.length - len)
    }
    return null
  }

  // ── foreground listeners ────────────────────────────────────────────

  private attachListeners(entry: PoolEntry): void {
    const container = entry.activeContainer
    if (!container) return

    const term = entry.terminal

    // Open terminal into container
    try { term.open(container) } catch { /* ignored */ }
    neutralizeXtermScrollbarFallback(term)

    const helperTextarea = container.querySelector('textarea.xterm-helper-textarea') as HTMLTextAreaElement | null

    requestClipboardPermission()

    // Custom key handler
    term.attachCustomKeyEventHandler((e) => {
      if (e.type === 'keydown') flushPendingClipboard()
      if (
        e.type === 'keydown' &&
        entry.ctrlModifierActive &&
        !e.metaKey && !e.ctrlKey && !e.altKey &&
        e.key.length === 1
      ) {
        const key = e.key.toUpperCase()
        if (key >= 'A' && key <= 'Z') {
          entry.suppressedInput = e.key
          this.sendRawBytes(
            { generation: entry.generation, key: entry.key },
            new Uint8Array([key.charCodeAt(0) - 64]),
          )
          entry.ctrlModifierActive = false
          entry.activeCallbacks?.onCtrlModifierChange(false)
          return false
        }
      }
      if (
        e.type === 'keydown' &&
        entry.altModifierActive &&
        !e.metaKey && !e.ctrlKey && !e.altKey &&
        e.key.length === 1
      ) {
        entry.suppressedInput = e.key
        this.sendRawBytes(
          { generation: entry.generation, key: entry.key },
          new Uint8Array([0x1b, ...new TextEncoder().encode(e.key)]),
        )
        entry.altModifierActive = false
        entry.activeCallbacks?.onAltModifierChange(false)
        return false
      }
      if (e.type === 'keydown' && (e.metaKey || e.ctrlKey)) {
        const key = e.key.toLowerCase()
        if (!e.shiftKey) {
          if (key === ',' || key === '\\' || key === '/' || key === '?') return false
        } else {
          if (key === '/' || key === '?' || key === '\\' || key === 'k' ||
              key === 'enter' || key === 'h' || key === 'f' || key === 'g' ||
              e.key === 'ArrowLeft' || e.key === 'ArrowRight') {
            return false
          }
        }
      }
      if ((e.metaKey || e.ctrlKey) && e.key === 'c' && e.type === 'keydown') {
        const selection = term.getSelection()
        if (selection) {
          navigator.clipboard?.writeText(normalizeSelection(selection))
          term.clearSelection()
          return false
        }
        this.sendRawBytes(
          { generation: entry.generation, key: entry.key },
          new Uint8Array([0x03]),
        )
        return false
      }
      if ((e.metaKey || e.ctrlKey) && e.key === 'b' && e.type === 'keydown') {
        this.sendRawBytes(
          { generation: entry.generation, key: entry.key },
          new Uint8Array([0x02]),
        )
        return false
      }
      return true
    })

    // Selection change -> auto copy
    term.onSelectionChange(() => {
      const selection = term.getSelection()
      if (selection) {
        copyToClipboard(selection)
      }
    })

    // Mouse/keyboard for clipboard flush
    const onMouseDown = (e: MouseEvent) => {
      flushPendingClipboard()
      if (e.button === 2 && term.getSelection()) {
        e.preventDefault()
        e.stopPropagation()
        e.stopImmediatePropagation()
      }
    }
    const onKeyDown = () => flushPendingClipboard()
    const onWindowKeyDownCapture = (e: KeyboardEvent) => {
      if (!(e.metaKey || e.ctrlKey) || e.key.toLowerCase() !== 'b') return
      const active = document.activeElement
      const terminalFocused = !!active && (container.contains(active) || active === container)
      if (!terminalFocused) return
      e.preventDefault()
      e.stopPropagation()
      this.sendRawBytes(
        { generation: entry.generation, key: entry.key },
        new Uint8Array([0x02]),
      )
    }
    const onPaste = (e: ClipboardEvent) => {
      const items = Array.from(e.clipboardData?.items ?? [])
      const imageItem = items.find(item => item.type.startsWith('image/'))
      if (!imageItem) {
        // Chrome 150+ strips file/image data from paste events whose target is
        // a hidden editable, so fall back to the async Clipboard API.
        if (items.length === 0 && navigator.clipboard?.read) {
          const currentWs = entry.connection.socket
          if (!currentWs || currentWs.readyState !== WebSocket.OPEN) return
          const gen = entry.generation
          navigator.clipboard.read().then(async (clipItems) => {
            for (const item of clipItems) {
              const imageType = item.types.find(t => t.startsWith('image/'))
              if (!imageType) continue
              const blob = await item.getType(imageType)
              const ext = imageType.split('/')[1]?.split('+')[0] || 'png'
              const file = new File([blob], `pasted-image.${ext}`, { type: imageType })
              await sendPastedImage(currentWs, file, imageType, entry, gen)
              return
            }
          }).catch(() => { /* no permission or no image — nothing to paste */ })
        }
        return
      }
      const file = imageItem.getAsFile()
      const currentWs = entry.connection.socket
      if (!file || !currentWs || currentWs.readyState !== WebSocket.OPEN) return
      e.preventDefault()
      const gen = entry.generation
      sendPastedImage(currentWs, file, imageItem.type, entry, gen).catch(() => {
        // Failed image paste is non-fatal.
      })
    }
    const onContextMenu = (e: MouseEvent) => {
      e.preventDefault()
      const sel = term.getSelection()
      if (sel) {
        const menu = { x: e.clientX, y: e.clientY, text: normalizeSelection(sel) }
        entry.selectionMenu = menu
        entry.activeCallbacks?.onSelectionMenu(menu)
      }
    }

    // User took over the viewport.
    const markUserScroll = () => {
      if (!entry.userScrolled) {
        entry.userScrolled = true
        entry.fitEpoch++
      }
    }
    const onViewportScroll = () => {
      if (!entry.userScrolled || !vpEl) return
      if (vpEl.scrollTop + vpEl.clientHeight >= vpEl.scrollHeight - 2) {
        entry.userScrolled = false
      }
    }
    const onWheel = () => markUserScroll()
    const vpEl = container.querySelector('.xterm-viewport') as HTMLElement | null
    vpEl?.addEventListener('scroll', onViewportScroll, { passive: true })
    container.addEventListener('wheel', onWheel, { passive: true })
    container.addEventListener('touchmove', markUserScroll, { passive: true })

    container.addEventListener('mousedown', onMouseDown, true)
    container.addEventListener('keydown', onKeyDown, true)
    window.addEventListener('keydown', onWindowKeyDownCapture, true)
    helperTextarea?.addEventListener('paste', onPaste)
    container.addEventListener('contextmenu', onContextMenu)

    // onData handler
    const onDataDispose = term.onData((data) => {
      if (!entry.activeCallbacks || !entry.activeContainer) return

      if (entry.suppressedInput !== null && data === entry.suppressedInput) {
        entry.suppressedInput = null
        return
      }
      entry.suppressedInput = null
      const ws = entry.connection.socket
      if (ws && ws.readyState === WebSocket.OPEN) {
        let payload = data
        if (
          data.length > 1 &&
          !data.startsWith('\x1b[200~') &&
          (data.includes('\r') || data.includes('\n'))
        ) {
          payload = '\x1b[200~' + data + '\x1b[201~'
        }
        const encoder = new TextEncoder()
        if (data.length === 1) {
          const code = data.charCodeAt(0)
          if (code >= 0x20 && code <= 0x7e) {
            if (!entry.writePending) {
              entry.writePending = true
            }
          }
        }
        let pe = entry.predictiveEcho
        if (!pe && entry.predictiveEchoEnabled) {
          pe = this.factory.createPredictiveEcho(entry.terminal)
          entry.predictiveEcho = pe
        }
        if (pe && entry.predictiveEchoEnabled) {
          if (pe.canPredict(data)) {
            pe.predict(data)
          } else {
            pe.clear()
          }
        }
        ws.send(encoder.encode(payload))
      }
    })

    // onResize handler — only while checked out
    const onResizeDispose = term.onResize(({ cols, rows }) => {
      if (!entry.activeContainer) return
      entry.predictiveEcho?.clear()
      const ws = entry.connection.socket
      if (ws && ws.readyState === WebSocket.OPEN) {
        ws.send(JSON.stringify({ type: 'resize', cols, rows }))
        entry.lastCols = cols
        entry.lastRows = rows
      }
    })

    entry.listenerCleanup = () => {
      onDataDispose.dispose()
      onResizeDispose.dispose()
      container.removeEventListener('mousedown', onMouseDown, true)
      container.removeEventListener('keydown', onKeyDown, true)
      window.removeEventListener('keydown', onWindowKeyDownCapture, true)
      helperTextarea?.removeEventListener('paste', onPaste)
      container.removeEventListener('contextmenu', onContextMenu)
      if (vpEl) vpEl.removeEventListener('scroll', onViewportScroll)
      container.removeEventListener('wheel', onWheel)
      container.removeEventListener('touchmove', markUserScroll)
    }
  }

  // ── entry disposal ──────────────────────────────────────────────────

  private disposeEntry(entry: PoolEntry): void {
    // Stop the connection machine first so reconnect timers abort.
    entry.connection.dispose()

    // Cleanup listeners
    if (entry.listenerCleanup) {
      entry.listenerCleanup()
      entry.listenerCleanup = null
    }
    entry.activeCallbacks = null
    entry.activeContainer = null

    // Dispose addons
    if (entry.webglAddon) { entry.webglAddon.dispose(); entry.webglAddon = null }
    if (entry.imageAddon) { entry.imageAddon.dispose(); entry.imageAddon = null }
    if (entry.graphemesAddon) { entry.graphemesAddon.dispose(); entry.graphemesAddon = null }
    if (entry.predictiveEcho) { entry.predictiveEcho.dispose(); entry.predictiveEcho = null }

    // Dispose terminal
    try { entry.terminal.dispose() } catch { /* ignored */ }

    // Remove hidden host
    if (entry.hiddenHost) {
      entry.hiddenHost.remove()
      entry.hiddenHost = null
    }

    // Clean up pool root if empty
    if (this.poolRoot && this.poolRoot.children.length === 0) {
      this.poolRoot.remove()
      this.poolRoot = null
    }
  }
}

// Singleton
export const terminalPool = new TerminalPool()

// Re-export keyFor as standalone function
export const keyFor = TerminalPool.keyFor.bind(TerminalPool)

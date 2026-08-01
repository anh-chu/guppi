//
// terminalPool.ts — Frontend-owned terminal instance pool
//
// Owns Terminal, addons, WebSocket, timers, and lifecycle for each
// hostId/sessionName key. Checkout picks an existing warm entry;
// checkin moves the terminal DOM offscreen without disconnecting.
// All input/resize is gated by an exclusive generation lease.
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
// Shared DOM transfer primitive — Group B owns pip.ts.
// Contract: (node: HTMLElement, dest: HTMLElement) => { crossedDocument: boolean }
import { transferNode } from './pip'

// Allow tests to inject a different transfer primitive.
let _transferNode: ((node: HTMLElement, dest: HTMLElement) => { crossedDocument: boolean }) | null = transferNode
export function __injectTransferNode(
  fn: (node: HTMLElement, dest: HTMLElement) => { crossedDocument: boolean },
) {
  _transferNode = fn
}

// --- internal helpers --------------------------------------------------

type XtermWithCore = Terminal & {
  _core?: { viewport?: { scrollBarWidth?: number } }
}

function neutralizeXtermScrollbarFallback(term: Terminal): void {
  const viewport = (term as XtermWithCore)._core?.viewport
  if (viewport && typeof viewport.scrollBarWidth === 'number') {
    viewport.scrollBarWidth = 0
  }
}

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

// Maximum bytes to buffer while waiting for replay-end. If a replay stream
// exceeds this we flush what we have and switch to passthrough so the UI
// never wedges on an unbounded backlog.
export const MAX_REPLAY_BUFFER_BYTES = 32 * 1024 * 1024

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
  entry: PoolEntryState,
  gen: number,
): Promise<void> {
  if (file.size > MAX_PASTED_FILE_BYTES) {
    console.warn(`Pasted image exceeds ${MAX_PASTED_FILE_BYTES} byte limit`)
    return
  }
  const buffer = await file.arrayBuffer()
  // Re-validate lease after async gap — entry may have been checked in/out
  if (entry.generation !== gen || entry.ws !== ws || ws.readyState !== WebSocket.OPEN) return
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
//
// When the user is following output (not scrolled), we just scrollToBottom
// once and let xterm auto-stick on subsequent writes. No multi-frame deferred
// restore pile: that competed with xterm's own scroll-on-write and was itself
// a source of visible flashing during TUI animations.
function fitPreservingScroll(
  entry: PoolEntryState,
  container: HTMLElement,
  opts?: { refreshAfter?: boolean },
): void {
  const term = entry.terminal
  const fitAddon = entry.fitAddon
  if (container.clientWidth <= 0 || container.clientHeight <= 0) return

  const myEpoch = ++entry.fitEpoch
  const buf = term.buffer.active
  // Only preserve a real user offset. geometry-based distFromBottom is
  // unreliable during async writes (see header comment).
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
  // across a single deferred frame (xterm recomputes viewport async). Epoch
  // gating cancels this if the user scrolls again or a newer fit runs.
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
  sessionName: string
  hostId?: string
  backend?: string
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

interface PoolEntryState {
  // Identity
  key: string
  sessionName: string
  hostId?: string
  backend?: string

  // Core resources
  terminal: Terminal
  fitAddon: FitAddon
  webglAddon: WebglAddon | null
  imageAddon: ImageAddon | null
  graphemesAddon: UnicodeGraphemesAddon | null
  graphemesLoaded: boolean
  predictiveEcho: PredictiveEcho | null
  predictiveEchoEnabled: boolean
  ws: WebSocket | null

  // Timers
  reconnectTimer: number | undefined
  heartbeatTimer: number | undefined
  watchdogTimer: number | undefined
  telemetryInterval: number | undefined
  fallbackTimer: number | undefined

  // Connection
  connId: number
  connected: boolean

  // Telemetry
  msgCount: number
  totalBytes: number
  lastSummary: number
  tConnect: number
  pendingInputTs: number | null
  writePending: boolean
  discardedInputs: number
  latencySamples: Array<{ inputToFrameMs: number; inputToWriteCompleteMs: number }>

  // Lease
  generation: number

  // Fit-scheduling epoch. Incremented on every fit() and on user scroll so
  // stale deferred scroll-restore callbacks from a prior fit can no-op.
  fitEpoch: number

  // True only when the user has taken control of the viewport via a real
  // gesture (wheel, touch, scrollbar drag). Buffer geometry (baseY vs
  // viewportY) is NOT a reliable signal: xterm updates viewportY
  // asynchronously during rapid writes (spinner/redraw animations), so the
  // viewport can momentarily lag baseY by a few lines mid-frame. Treating
  // that lag as a user scroll caused fit/restore logic to pin the view
  // mid-history and flash on every redraw. We key all scroll-preserving
  // behavior off this gesture flag instead.
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

  // Reconnect URL identity (may differ from key after rekey)
  reconnectSessionName: string
  reconnectHostId?: string
  reconnectBackend?: string

  // Replay/sync state
  inReplay: boolean
  passthroughArmed: boolean
  replayPending: Uint8Array[]
  replayBytesAccum: number
  syncCarryover: Uint8Array | null
  syncActive: boolean
  syncBuffer: Uint8Array[]
}

// --- Pool singleton ----------------------------------------------------

let nextConnId = 0
const MAX_TELEMETRY_SAMPLES = 1000
const TELEMETRY_INTERVAL_MS = 60000
const HEARTBEAT_MS = 10000
const WATCHDOG_MS = 25000

export class TerminalPool {
  private entries = new Map<string, PoolEntryState>()
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
    const key = TerminalPool.keyFor(identity.sessionName, identity.hostId)
    const ef = factory ?? this.factory
    let entry = this.entries.get(key)

    // Dispose & recreate if backend identity changed for same key.
    if (entry) {
      const backendChanged = entry.backend !== (identity.backend ?? undefined)
      if (backendChanged) {
        this.disposeEntry(entry)
        entry = undefined
      }
    }

    // Cold create
    if (!entry) {
      entry = this.createEntry(key, identity, prefs, ef)
      this.entries.set(key, entry)
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

    // Bind terminal to the foreground container. On a cold entry (just
    // created, never opened) term.element is undefined, so we MUST still call
    // term.open(container) here to create the renderer and viewport — skipping
    // it leaves the terminal with no renderer, and any later fit/refresh/
    // syncScrollArea call throws `this._renderer.value is undefined` and can
    // infinite-recurse in setRenderer. This path is hit exactly when a prior
    // kill disposed the pooled entry and a new session cold-creates one.
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
      if (entry.ws && entry.ws.readyState === WebSocket.OPEN) {
        const { cols, rows } = entry.terminal
        entry.ws.send(JSON.stringify({ type: 'resize', cols, rows }))
        entry.lastCols = cols
        entry.lastRows = rows
        entry.pendingResizeOnOpen = false
      } else {
        entry.pendingResizeOnOpen = true
      }
    }

    // Deferred refresh: xterm's IntersectionObserver pauses rendering while the
    // terminal element is off-screen (hidden pool). The synchronous refresh()
    // calls above set _needsFullRefresh=true internally but don't actually
    // paint — the observer un-pauses asynchronously in the next frame. Schedule
    // a second fit+refresh in rAF so the render fires once the observer has
    // cleared the pause, avoiding a blank frame on warm switches.
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
    // Stale lease — ignore
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

    // Never fit hidden, never emit resize, leave WS/addons/timers active
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
    const ws = entry.ws
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
    const ws = entry.ws
    if (!ws || ws.readyState !== WebSocket.OPEN) return
    const gen = lease.generation
    try {
      await sendPastedImage(ws, file, fallbackType, entry, gen)
    } catch (err) {
      console.error('Failed to send pasted image:', err)
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
  reconcilePrefs(entry: PoolEntryState, prefs: TerminalPrefs): void {
    // Theme
    const xtermTheme = getXtermTheme(prefs.theme)
    entry.terminal.options.theme = xtermTheme
    try { entry.terminal.refresh(0, entry.terminal.rows - 1) } catch { /* ignored */ }

    // Font
    const fontFamily = `'${prefs.fontFamily}', 'JetBrains Mono', 'Fira Code', Menlo, Monaco, 'Inconsolata LGC Nerd Font Mono', 'DejaVu Sans Mono Symbols', monospace`
    entry.terminal.options.fontSize = prefs.fontSize
    entry.terminal.options.fontFamily = fontFamily
    try {
      (entry.terminal as unknown as { _core?: { _charSizeService?: { measure?: () => void } } })
        ._core?._charSizeService?.measure?.()
    } catch { /* ignored */ }

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
        const identity: PoolIdentity = {
          sessionName: entry.reconnectSessionName,
          hostId: entry.reconnectHostId,
          backend: entry.reconnectBackend,
        }
        const key = entry.key
        const wasActive = entry.activeContainer !== null
        const prevContainer = entry.activeContainer

        this.disposeEntry(entry)
        const newEntry = this.createEntry(key, identity, prefs, this.factory)
        this.entries.set(key, newEntry)

        if (wasActive && prevContainer) {
          // Re-checkout into same container
          newEntry.generation++
          newEntry.activeContainer = prevContainer
          const root = newEntry.terminal.element as HTMLElement | undefined
          // newEntry was just createEntry()'d (terminal never opened); always
          // open() it into prevContainer so the renderer is bound, not just
          // when a stale root happens to exist.
          try { newEntry.terminal.open(prevContainer) } catch { /* ignored */ }
          neutralizeXtermScrollbarFallback(newEntry.terminal)
          // (root is only relevant for re-appending the existing DOM node.)
          if (root) prevContainer.appendChild(root)
          this.attachListeners(newEntry)
          // Load renderer-dependent addons (WebGL) AFTER open() above.
          this.reconcilePrefs(newEntry, prefs)
          fitPreservingScroll(newEntry, prevContainer, { refreshAfter: true })
          // Send resize
          if (newEntry.ws && newEntry.ws.readyState === WebSocket.OPEN) {
            const { cols, rows } = newEntry.terminal
            newEntry.ws.send(JSON.stringify({ type: 'resize', cols, rows }))
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

  /** Remove entries NOT in validKeys. */
  disposeAbsent(validKeys: Set<string>): void {
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
    entry.key = newKey
    entry.reconnectSessionName = newKey.includes('/')
      ? newKey.slice(newKey.indexOf('/') + 1)
      : newKey
    entry.reconnectHostId = newKey.includes('/')
      ? newKey.slice(0, newKey.indexOf('/'))
      : undefined
    // Update the lease token for current owner
    entry.generation++ // invalidate previous lease
    this.entries.set(newKey, entry)
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

  // For tests: expose entry internals (snapshot only, not for mutation)
  _debug_entry(key: string): Readonly<PoolEntryState> | undefined {
    return this.entries.get(key)
  }

  _debug_hasEntry(key: string): boolean {
    return this.entries.has(key)
  }

  // ── private helpers ─────────────────────────────────────────────────

  private validateLease(lease: LeaseToken): PoolEntryState | null {
    const entry = this.entries.get(lease.key)
    if (!entry || entry.generation !== lease.generation) return null
    return entry
  }

  private ensureHiddenHost(entry: PoolEntryState): HTMLElement {
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
  ): PoolEntryState {
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

    // WebGL renderer is NOT loaded here: the WebGL addon must be loaded
    // AFTER term.open() (xterm.js requirement). Loading it before open makes
    // setRenderer infinite-recurse ("InternalError: too much recursion"). It
    // is loaded lazily in reconcilePrefs(), which runs after open() in both
    // checkout() and the scrollback-rebuild path.
    let webglAddon: WebglAddon | null = null

    // Image addon
    const imageAddon = ef.createImageAddon()
    if (imageAddon) {
      try { term.loadAddon(imageAddon) } catch { /* ignored */ }
    }

    // Unicode graphemes
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

    // Predictive echo
    let predictiveEcho: PredictiveEcho | null = null
    if (prefs.predictiveEcho) {
      predictiveEcho = ef.createPredictiveEcho(term)
    }

    const connId = ++nextConnId

    const entry: PoolEntryState = {
      key,
      sessionName: identity.sessionName,
      hostId: identity.hostId,
      backend: identity.backend,

      terminal: term,
      fitAddon,
      webglAddon,
      imageAddon,
      graphemesAddon,
      graphemesLoaded,
      predictiveEcho,
      predictiveEchoEnabled: prefs.predictiveEcho,
      ws: null,

      reconnectTimer: undefined,
      heartbeatTimer: undefined,
      watchdogTimer: undefined,
      telemetryInterval: undefined,
      fallbackTimer: undefined,

      connId,
      connected: false,

      msgCount: 0,
      totalBytes: 0,
      lastSummary: 0,
      tConnect: 0,
      pendingInputTs: null,
      writePending: false,
      discardedInputs: 0,
      latencySamples: [],

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

      reconnectSessionName: identity.sessionName,
      reconnectHostId: identity.hostId,
      reconnectBackend: identity.backend,

      inReplay: false,
      passthroughArmed: false,
      replayPending: [],
      replayBytesAccum: 0,
      syncCarryover: null,
      syncActive: false,
      syncBuffer: [],
    }

    // Initiate WebSocket connection
    this.connect(entry)

    return entry
  }

  // ── connection ──────────────────────────────────────────────────────

  private connect(entry: PoolEntryState): void {
    const term = entry.terminal
    entry.userScrolled = false
    // Reset replay/sync state on every (re)connect. Reconnect reuses the
    // same entry, so any in-flight replay must start fresh.
    entry.inReplay = false
    entry.passthroughArmed = false
    entry.replayPending = []
    entry.replayBytesAccum = 0
    entry.syncCarryover = null
    entry.syncActive = false
    entry.syncBuffer = []
    if (entry.fallbackTimer !== undefined) {
      clearTimeout(entry.fallbackTimer)
      entry.fallbackTimer = undefined
    }
    const cols = term.cols || 80
    const rows = term.rows || 24

    const protocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:'
    const sessionName = entry.reconnectSessionName
    const hostId = entry.reconnectHostId
    const backend = entry.reconnectBackend

    const hostParam = hostId ? `&host=${encodeURIComponent(hostId)}` : ''
    let wsUrl: string
    if (backend === 'daemon') {
      wsUrl = `${protocol}//${window.location.host}/ws/daemon-session?name=${encodeURIComponent(sessionName)}&cols=${cols}&rows=${rows}${hostParam}&replay=1`
    } else if (sessionName.startsWith('direct-pty:')) {
      wsUrl = `${protocol}//${window.location.host}/ws/direct-session?cols=${cols}&rows=${rows}`
    } else {
      wsUrl = `${protocol}//${window.location.host}/ws/session?name=${encodeURIComponent(sessionName)}&cols=${cols}&rows=${rows}${hostParam}&replay=1`
    }

    const ws = this.factory.createWebSocket(wsUrl)
    ws.binaryType = 'arraybuffer'
    entry.ws = ws
    const connId = entry.connId
    entry.tConnect = performance.now()

    // Watchdog
    const armWatchdog = () => {
      if (entry.watchdogTimer) clearTimeout(entry.watchdogTimer)
      entry.watchdogTimer = window.setTimeout(() => {
        if (entry.connId !== connId) return
        try { ws.close() } catch { /* ignored */ }
      }, WATCHDOG_MS)
    }

    ws.onopen = () => {
      if (entry.connId !== connId) { ws.close(); return }
      entry.connected = true
      entry.activeCallbacks?.onConnectionChange(true)
      armWatchdog()
      if (entry.heartbeatTimer) clearInterval(entry.heartbeatTimer)
      entry.heartbeatTimer = window.setInterval(() => {
        if (ws.readyState === WebSocket.OPEN) {
          try { ws.send(JSON.stringify({ type: 'ping' })) } catch { /* ignored */ }
        }
      }, HEARTBEAT_MS)

      // If the server does not send a replay-start control within 250ms,
      // assume it is an old server or no replay was requested. Arm
      // passthrough for the lifetime of this connection so late controls are
      // ignored and bytes are written immediately.
      entry.fallbackTimer = window.setTimeout(() => {
        if (entry.connId !== connId) return
        entry.passthroughArmed = true
        entry.inReplay = false
        if (entry.replayPending.length) {
          const concat = concatU8(entry.replayPending)
          entry.replayPending = []
          entry.replayBytesAccum = 0
          entry.predictiveEcho?.clear()
          entry.terminal.write(concat)
          if (!entry.userScrolled) {
            try { entry.terminal.scrollToBottom() } catch { /* ignored */ }
          }
        }
      }, 250) as unknown as number

      // Pending resize
      if (entry.pendingResizeOnOpen && entry.activeContainer) {
        entry.pendingResizeOnOpen = false
        // Re-read from terminal after open
        // Dimensions settled during checkout fit; send them
        const c = entry.terminal.cols
        const r = entry.terminal.rows
        entry.lastCols = c
        entry.lastRows = r
        try { ws.send(JSON.stringify({ type: 'resize', cols: c, rows: r })) } catch { /* ignored */ }
      }
    }

    ws.onmessage = (evt) => {
      armWatchdog()
      if (evt.data instanceof ArrayBuffer) {
        entry.msgCount++
        entry.totalBytes += evt.data.byteLength
        const data = new Uint8Array(evt.data)
        if (entry.inReplay) {
          entry.replayPending.push(data)
          entry.replayBytesAccum += data.byteLength
          if (entry.replayBytesAccum > MAX_REPLAY_BUFFER_BYTES) {
            const all = concatU8(entry.replayPending)
            entry.predictiveEcho?.clear()
            entry.terminal.write(all)
            entry.replayPending = []
            entry.replayBytesAccum = 0
            entry.inReplay = false
            entry.passthroughArmed = true
            console.debug('[termyard-replay] 32MB cap exceeded, passthrough')
          }
          return
        }
        this.dispatchLiveOutput(entry, data)
      } else if (typeof evt.data === 'string') {
        this.handleTextControl(entry, evt.data)
      }
    }

    ws.onclose = (evt) => {
      if (entry.connId !== connId) return
      if (entry.heartbeatTimer) { clearInterval(entry.heartbeatTimer); entry.heartbeatTimer = undefined }
      if (entry.watchdogTimer) { clearTimeout(entry.watchdogTimer); entry.watchdogTimer = undefined }
      if (entry.telemetryInterval) { clearInterval(entry.telemetryInterval); entry.telemetryInterval = undefined }
      if (entry.fallbackTimer !== undefined) { clearTimeout(entry.fallbackTimer); entry.fallbackTimer = undefined }
      if (entry.connected) {
        entry.connected = false
        if (!document.hidden) {
          entry.activeCallbacks?.onConnectionChange(false)
        }
      }
      // Only reconnect if document is visible
      if (document.hidden) {
        const onVisible = () => {
          if (entry.connId !== connId) return
          document.removeEventListener('visibilitychange', onVisible)
          window.removeEventListener('pageshow', onVisible)
          this.connect(entry)
        }
        document.addEventListener('visibilitychange', onVisible)
        window.addEventListener('pageshow', onVisible)
      } else {
        entry.reconnectTimer = window.setTimeout(() => {
          if (entry.connId === connId) {
            this.connect(entry)
          }
        }, 2000) as unknown as number
      }
    }

    ws.onerror = () => {
      // Error is typically followed by onclose; no explicit state change needed here
    }

    // Telemetry
    const emitTelemetry = (reason: string) => {
      if (entry.connId !== connId) return
      if (entry.latencySamples.length === 0) return
      const sortedFrame = entry.latencySamples.map(s => s.inputToFrameMs).sort((a, b) => a - b)
      const sortedWrite = entry.latencySamples.map(s => s.inputToWriteCompleteMs).sort((a, b) => a - b)
      const p = (arr: number[], q: number) => arr[Math.floor(arr.length * q)] ?? 0
      console.debug('[termyard-telemetry]', reason, {
        samples: entry.latencySamples.length,
        discarded: entry.discardedInputs,
        inputToFrameMs: { p50: p(sortedFrame, 0.5), p90: p(sortedFrame, 0.9), p99: p(sortedFrame, 0.99) },
        inputToWriteCompleteMs: { p50: p(sortedWrite, 0.5), p90: p(sortedWrite, 0.9), p99: p(sortedWrite, 0.99) },
      })
    }
    entry.telemetryInterval = window.setInterval(() => emitTelemetry('periodic'), TELEMETRY_INTERVAL_MS)
  }

  private handleTextControl(entry: PoolEntryState, text: string): void {
    let ctrl: { type?: string } | null = null
    try {
      ctrl = JSON.parse(text)
    } catch {
      // Not a JSON control; fall through to terminal.write below.
    }
    if (ctrl && ctrl.type === 'pong') return

    if (ctrl && ctrl.type === 'replay-start') {
      if (entry.passthroughArmed) {
        // Late replay-start after fallback timer; ignore and keep passthrough.
        return
      }
      if (entry.fallbackTimer !== undefined) {
        clearTimeout(entry.fallbackTimer)
        entry.fallbackTimer = undefined
      }
      entry.inReplay = true
      entry.replayPending = []
      entry.replayBytesAccum = 0
      return
    }

    if (ctrl && ctrl.type === 'replay-end') {
      if (!entry.inReplay) return
      const all = concatU8(entry.replayPending)
      entry.predictiveEcho?.clear()
      entry.terminal.write(all)
      entry.replayPending = []
      entry.replayBytesAccum = 0
      entry.inReplay = false
      if (!entry.userScrolled) {
        try { entry.terminal.scrollToBottom() } catch { /* ignored */ }
      }
      return
    }

    // Non-control string (or unknown control): write directly. Do not use
    // per-write polling; rely on userScrolled for scroll decisions.
    entry.terminal.write(text)
    if (!entry.userScrolled) {
      try { entry.terminal.scrollToBottom() } catch { /* ignored */ }
    }
  }

  // Live output dispatcher. Handles DEC mode 2026 synchronized-update
  // markers (BSU/ESU) by buffering bytes between markers and flushing them
  // as a single terminal.write. Markers are stripped. Straddling markers are
  // handled via syncCarryover. Replay bytes bypass this path entirely.
  private dispatchLiveOutput(entry: PoolEntryState, data: Uint8Array): void {
    if (entry.syncCarryover !== null) {
      data = concatU8([entry.syncCarryover, data])
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
        // bytes and continue scanning after the prefix so we do not loop.
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
          // Accumulate any plain bytes that arrived after the BSU and before
          // this ESU into the sync buffer.
          if (out.length) {
            entry.syncBuffer.push(...out)
            out.length = 0
          }
          const all = concatU8(entry.syncBuffer)
          entry.predictiveEcho?.clear()
          entry.terminal.write(all)
          entry.syncBuffer = []
          entry.syncActive = false
          if (!entry.userScrolled) {
            try { entry.terminal.scrollToBottom() } catch { /* ignored */ }
          }
        }
        // ESU without matching BSU is a no-op; marker stripped.
      }

      cursor = idx + SYNC_MARKER_PREFIX.length + 1
    }

    // Handle any remaining plain slices.
    this.flushLiveSlices(entry, out)
  }

  private flushLiveSlices(entry: PoolEntryState, slices: Uint8Array[]): void {
    if (slices.length === 0) return
    const data = concatU8(slices)
    slices.length = 0
    if (entry.syncActive) {
      entry.syncBuffer.push(data)
      return
    }
    this.writeLiveRaw(entry, data)
  }

  private writeLiveRaw(entry: PoolEntryState, data: Uint8Array): void {
    entry.predictiveEcho?.clear()
    if (entry.pendingInputTs !== null) {
      const inputToFrameMs = performance.now() - entry.pendingInputTs
      const capturedTs = entry.pendingInputTs
      entry.pendingInputTs = null
      entry.writePending = true
      entry.terminal.write(data, () => {
        const inputToWriteCompleteMs = performance.now() - capturedTs
        entry.latencySamples.push({ inputToFrameMs, inputToWriteCompleteMs })
        if (entry.latencySamples.length > MAX_TELEMETRY_SAMPLES) {
          entry.latencySamples.shift()
        }
        entry.writePending = false
        if (!entry.userScrolled) {
          try { entry.terminal.scrollToBottom() } catch { /* ignored */ }
        }
      })
    } else {
      entry.terminal.write(data, () => {
        if (!entry.userScrolled) {
          try { entry.terminal.scrollToBottom() } catch { /* ignored */ }
        }
      })
    }
  }

  // Returns the longest non-empty suffix of `data` that is a prefix of the
  // 7-byte marker sequence, or null if none exists. This allows BSU/ESU
  // sequences split across WebSocket frames to be reassembled.
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

  private attachListeners(entry: PoolEntryState): void {
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
          this.sendRawBytes({ generation: entry.generation, key: entry.key },
            new Uint8Array([key.charCodeAt(0) - 64]))
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
        this.sendRawBytes({ generation: entry.generation, key: entry.key },
          new Uint8Array([0x1b, ...new TextEncoder().encode(e.key)]))
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
        this.sendRawBytes({ generation: entry.generation, key: entry.key },
          new Uint8Array([0x03]))
        return false
      }
      if ((e.metaKey || e.ctrlKey) && e.key === 'b' && e.type === 'keydown') {
        this.sendRawBytes({ generation: entry.generation, key: entry.key },
          new Uint8Array([0x02]))
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
      this.sendRawBytes({ generation: entry.generation, key: entry.key },
        new Uint8Array([0x02]))
    }
    const onPaste = (e: ClipboardEvent) => {
      const items = Array.from(e.clipboardData?.items ?? [])
      const imageItem = items.find(item => item.type.startsWith('image/'))
      if (!imageItem) {
        // Chrome 150+ strips file/image data from paste events whose target is
        // a hidden editable (xterm's helper textarea sits at z-index:-5), so an
        // image-only clipboard arrives here with an empty DataTransfer. Fall
        // back to the async Clipboard API, which is not subject to that gating.
        // Text clipboards still carry text/plain and are handled by xterm.
        if (items.length === 0 && navigator.clipboard?.read) {
          const currentWs = entry.ws
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
      const currentWs = entry.ws
      if (!file || !currentWs || currentWs.readyState !== WebSocket.OPEN) return
      e.preventDefault()
      const gen = entry.generation
      sendPastedImage(currentWs, file, imageItem.type, entry, gen).catch((err) => {
        console.error('Failed to read pasted image:', err)
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

    // User took over the viewport. We track this with a gesture-driven flag
    // (NOT buffer geometry, which lies during async writes) so
    // fitPreservingScroll and the reconnect pin-guard know when to stop
    // forcing scroll-to-bottom.
    const markUserScroll = () => {
      if (!entry.userScrolled) {
        entry.userScrolled = true
        entry.fitEpoch++
      }
    }
    // Clear the flag when the user scrolls back to the bottom so output
    // following resumes.
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
      // Check lease — ignore if inactive
      if (!entry.activeCallbacks || !entry.activeContainer) return

      if (entry.suppressedInput !== null && data === entry.suppressedInput) {
        entry.suppressedInput = null
        return
      }
      entry.suppressedInput = null
      const ws = entry.ws
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
            if (entry.pendingInputTs === null && !entry.writePending) {
              entry.pendingInputTs = performance.now()
            } else {
              entry.discardedInputs++
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
      if (!entry.activeContainer) return // not checked out
      entry.predictiveEcho?.clear()
      const ws = entry.ws
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

  private disposeEntry(entry: PoolEntryState): void {
    // Invalidate connection
    entry.connId = ++nextConnId

    // Clear timers
    if (entry.reconnectTimer) { clearTimeout(entry.reconnectTimer); entry.reconnectTimer = undefined }
    if (entry.heartbeatTimer) { clearInterval(entry.heartbeatTimer); entry.heartbeatTimer = undefined }
    if (entry.watchdogTimer) { clearTimeout(entry.watchdogTimer); entry.watchdogTimer = undefined }
    if (entry.telemetryInterval) { clearInterval(entry.telemetryInterval); entry.telemetryInterval = undefined }
    if (entry.fallbackTimer !== undefined) { clearTimeout(entry.fallbackTimer); entry.fallbackTimer = undefined }

    // Close WS
    if (entry.ws) {
      entry.ws.onclose = null
      entry.ws.onerror = null
      entry.ws.onmessage = null
      try { entry.ws.close() } catch { /* ignored */ }
      entry.ws = null
    }

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

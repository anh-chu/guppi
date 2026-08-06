/**
 * Durable state stream client (/ws/state).
 *
 * Generation-safe by construction: every connect() bumps an internal
 * generation counter; every callback captures the generation it was
 * scheduled under and is a no-op if the client has since been disposed or
 * reconnected. This makes it impossible for a stale socket's queued
 * message/close/error events to mutate state belonging to a newer
 * connection.
 *
 * The bootstrap fetch (GET /api/state/bootstrap) is the caller's
 * responsibility -- this client only owns the streaming socket. Callers
 * should fetch bootstrap first, apply it, then create/connect this client;
 * the first catalog/workspace snapshot(s) received on the socket safely
 * replace the bootstrap view (same generation/revision acceptance rule as
 * any other snapshot).
 */

import { isStateStreamMessage } from './wireTypes'
import {
  decodeCatalogOwnerRemovedMessage,
  decodeCatalogSnapshotMessage,
  decodeWorkspaceSnapshotMessage,
} from './wireCodec'
import type { OwnerCatalogSnapshot, OwnerID, WorkspaceRecord } from './types'

export type StateStreamCallbacks = {
  onCatalog: (snapshot: OwnerCatalogSnapshot, generation: number, isLocal: boolean) => void
  // Explicit removal signal for a remote owner's catalog (peer forgotten /
  // disconnected). Never fired for the local owner.
  onCatalogRemoved: (owner: OwnerID, generation: number) => void
  onWorkspace: (snapshot: WorkspaceRecord, generation: number) => void
  // Called whenever the socket transitions open/closed. `generation` is the
  // generation this transition belongs to; callers should ignore it if it
  // does not match the generation they last accepted data from.
  onConnectionChange?: (online: boolean, generation: number) => void
  onError?: (err: unknown) => void
}

export type StateStreamOptions = {
  url: string
  callbacks: StateStreamCallbacks
  // Injectable for tests. Defaults to `new WebSocket(url)`.
  createSocket?: (url: string) => WebSocket
  minBackoffMs?: number
  maxBackoffMs?: number
  // Injectable for tests. Defaults to Math.random.
  jitter?: () => number
  // Injectable document/visibility hooks for tests (defaults to the global
  // `document`, when present).
  isDocumentHidden?: () => boolean
  addVisibilityListener?: (fn: () => void) => () => void
}

const DEFAULT_MIN_BACKOFF_MS = 500
const DEFAULT_MAX_BACKOFF_MS = 15_000

function defaultIsDocumentHidden(): boolean {
  return typeof document !== 'undefined' && document.hidden
}

function defaultAddVisibilityListener(fn: () => void): () => void {
  if (typeof document === 'undefined') return () => {}
  document.addEventListener('visibilitychange', fn)
  return () => document.removeEventListener('visibilitychange', fn)
}

export class StateStreamClient {
  private readonly opts: Required<
    Pick<StateStreamOptions, 'url' | 'callbacks' | 'minBackoffMs' | 'maxBackoffMs' | 'jitter'>
  > &
    Pick<StateStreamOptions, 'createSocket' | 'isDocumentHidden' | 'addVisibilityListener'>

  private generation = 0
  private disposed = false
  private socket: WebSocket | null = null
  private reconnectTimer: ReturnType<typeof setTimeout> | null = null
  private hidden = false
  private removeVisibilityListener: (() => void) | null = null
  private attempt = 0

  constructor(options: StateStreamOptions) {
    this.opts = {
      url: options.url,
      callbacks: options.callbacks,
      minBackoffMs: options.minBackoffMs ?? DEFAULT_MIN_BACKOFF_MS,
      maxBackoffMs: options.maxBackoffMs ?? DEFAULT_MAX_BACKOFF_MS,
      jitter: options.jitter ?? Math.random,
      createSocket: options.createSocket,
      isDocumentHidden: options.isDocumentHidden,
      addVisibilityListener: options.addVisibilityListener,
    }
  }

  /** Current connection generation. Bumped on every connect() call. */
  currentGeneration(): number {
    return this.generation
  }

  start() {
    if (this.disposed) return
    const isHidden = this.opts.isDocumentHidden ?? defaultIsDocumentHidden
    const addListener = this.opts.addVisibilityListener ?? defaultAddVisibilityListener
    this.hidden = isHidden()
    this.removeVisibilityListener = addListener(() => this.onVisibilityChange(isHidden))
    this.connect()
  }

  /** Disposes the client permanently. No further callbacks will fire. */
  dispose() {
    this.disposed = true
    this.clearReconnectTimer()
    if (this.removeVisibilityListener) {
      this.removeVisibilityListener()
      this.removeVisibilityListener = null
    }
    if (this.socket) {
      const s = this.socket
      this.socket = null
      s.onopen = null
      s.onmessage = null
      s.onclose = null
      s.onerror = null
      s.close()
    }
  }

  private connect() {
    if (this.disposed) return
    this.generation++
    const myGeneration = this.generation
    this.clearReconnectTimer()

    const create = this.opts.createSocket ?? ((url: string) => new WebSocket(url))
    let socket: WebSocket
    try {
      socket = create(this.opts.url)
    } catch (err) {
      this.opts.callbacks.onError?.(err)
      this.scheduleReconnect(myGeneration)
      return
    }
    this.socket = socket

    socket.onopen = () => {
      if (this.disposed || this.generation !== myGeneration) return
      this.attempt = 0
      this.opts.callbacks.onConnectionChange?.(true, myGeneration)
    }

    socket.onmessage = (evt: MessageEvent) => {
      if (this.disposed || this.generation !== myGeneration) return
      this.handleMessage(evt.data, myGeneration)
    }

    socket.onclose = () => {
      if (this.disposed || this.generation !== myGeneration) return
      this.opts.callbacks.onConnectionChange?.(false, myGeneration)
      if (this.hidden) {
        // Defer reconnect until the page is visible again.
        return
      }
      this.scheduleReconnect(myGeneration)
    }

    socket.onerror = (err: Event) => {
      if (this.disposed || this.generation !== myGeneration) return
      this.opts.callbacks.onError?.(err)
      socket.close()
    }
  }

  private handleMessage(data: unknown, generation: number) {
    let parsed: unknown
    try {
      parsed = typeof data === 'string' ? JSON.parse(data) : data
    } catch (err) {
      this.opts.callbacks.onError?.(err)
      return
    }
    if (!isStateStreamMessage(parsed)) return
    if (this.generation !== generation) return
    try {
      if (parsed.type === 'catalog_snapshot') {
        const decoded = decodeCatalogSnapshotMessage(parsed)
        this.opts.callbacks.onCatalog(decoded.snapshot, generation, decoded.is_local)
      } else if (parsed.type === 'catalog_owner_removed') {
        const decoded = decodeCatalogOwnerRemovedMessage(parsed)
        this.opts.callbacks.onCatalogRemoved(decoded.owner, generation)
      } else if (parsed.type === 'workspace_snapshot') {
        const decoded = decodeWorkspaceSnapshotMessage(parsed)
        this.opts.callbacks.onWorkspace(decoded.workspace, generation)
      }
    } catch (err) {
      this.opts.callbacks.onError?.(err)
    }
  }

  private scheduleReconnect(forGeneration: number) {
    if (this.disposed || this.generation !== forGeneration) return
    this.clearReconnectTimer()
    const base = Math.min(
      this.opts.maxBackoffMs,
      this.opts.minBackoffMs * Math.pow(2, this.attempt),
    )
    this.attempt++
    const delay = base / 2 + this.opts.jitter() * (base / 2)
    this.reconnectTimer = setTimeout(() => {
      this.reconnectTimer = null
      this.connect()
    }, delay)
  }

  private clearReconnectTimer() {
    if (this.reconnectTimer !== null) {
      clearTimeout(this.reconnectTimer)
      this.reconnectTimer = null
    }
  }

  private onVisibilityChange(isHidden: () => boolean) {
    if (this.disposed) return
    const nowHidden = isHidden()
    if (nowHidden === this.hidden) return
    this.hidden = nowHidden
    if (!nowHidden) {
      // Became visible: reconnect immediately if not already open.
      if (!this.socket || this.socket.readyState !== WebSocket.OPEN) {
        this.clearReconnectTimer()
        this.connect()
      }
    }
  }
}

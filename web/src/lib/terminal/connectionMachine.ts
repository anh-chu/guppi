//
// connectionMachine.ts — clock-driven WebSocket lifecycle state machine.
//
// States: disconnected -> connecting -> (open) -> replaying | live ->
//         backoff -> connecting ... disposed
//
// The machine owns the socket, reconnect scheduling, heartbeat, watchdog,
// and replay-handshake fallback timer.  It does not own terminal I/O; it
// reports events so the pool can wire in replay/live dispatch.
//

const HEARTBEAT_MS = 10000
const WATCHDOG_MS = 25000
const FALLBACK_MS = 250
const RECONNECT_MS = 2000

export type ConnectionState =
  | 'disconnected'
  | 'connecting'
  | 'replaying'
  | 'live'
  | 'backoff'
  | 'disposed'

export interface ConnectionMachineOptions {
  createWebSocket: (url: string) => WebSocket
  onConnecting?: () => void
  onConnectionChange?: (connected: boolean) => void
  onOpen?: () => void
  onBinaryMessage?: (data: Uint8Array) => void
  onTextMessage?: (text: string) => void
  onClose?: () => void
  onReplayStart?: () => void
  onReplayEnd?: () => void
  onFallback?: () => void
}

export class ConnectionMachine {
  state: ConnectionState = 'disconnected'
  connected = false
  socket: WebSocket | null = null

  private generation = 0
  private currentUrl: string
  private watchdogTimer: number | undefined
  private heartbeatTimer: number | undefined
  private fallbackTimer: number | undefined
  private reconnectTimer: number | undefined
  private visibilityHandler: (() => void) | null = null
  private pageshowHandler: (() => void) | null = null

  constructor(
    private readonly options: ConnectionMachineOptions,
    url: string,
  ) {
    this.currentUrl = url
  }

  /** Build a fresh connection to the current or an updated URL. */
  connect(url?: string): void {
    if (this.state === 'disposed') return

    if (url !== undefined) this.currentUrl = url

    this.generation++
    const gen = this.generation

    this._clearTimers()
    this._removeVisibilityListeners()
    this.options.onConnecting?.()

    if (this.socket) {
      const stale = this.socket
      this.socket = null
      this._detach(stale)
      try {
        stale.close()
      } catch {
        /* ignored */
      }
    }

    this.state = 'connecting'

    const ws = this.options.createWebSocket(this.currentUrl)
    ws.binaryType = 'arraybuffer'
    this.socket = ws

    ws.onopen = () => {
      if (this.generation !== gen) {
        ws.close()
        return
      }
      this.connected = true
      this.options.onConnectionChange?.(true)
      this.options.onOpen?.()
      this._armWatchdog(gen)
      this._startHeartbeat(gen, ws)
      this._startFallback(gen)
    }

    ws.onmessage = (evt: MessageEvent) => {
      if (this.generation !== gen) return
      this._armWatchdog(gen)
      if (evt.data instanceof ArrayBuffer) {
        this.options.onBinaryMessage?.(new Uint8Array(evt.data))
      } else if (typeof evt.data === 'string') {
        this.options.onTextMessage?.(evt.data)
      }
    }

    ws.onclose = () => {
      if (this.generation !== gen) return
      this._handleClose()
    }

    ws.onerror = () => {
      // Errors are followed by onclose; state transitions live there.
    }
  }

  /** Permanently stop the machine and release all resources. */
  dispose(): void {
    this.state = 'disposed'
    this._clearTimers()
    this._removeVisibilityListeners()
    this.connected = false

    if (this.socket) {
      const ws = this.socket
      this.socket = null
      this._detach(ws)
      try {
        ws.close()
      } catch {
        /* ignored */
      }
    }
  }

  /** Server announced the start of a replay batch. */
  startReplay(): void {
    if (this.state !== 'connecting' && this.state !== 'replaying') {
      // Late replay-start after fallback/passthrough must be ignored.
      return
    }
    if (this.state === 'connecting') {
      this._clearFallback()
    }
    this.state = 'replaying'
    this.options.onReplayStart?.()
  }

  /** Server announced the end of a replay batch. */
  endReplay(): void {
    if (this.state !== 'replaying') return
    this.state = 'live'
    this.options.onReplayEnd?.()
  }

  /**
   * Forcibly move to the live state and cancel the fallback timer.
   * Used when the replay buffer overflows.
   */
  markLive(): void {
    if (this.state === 'disposed' || this.state === 'backoff') return
    this._clearFallback()
    this.state = 'live'
  }

  private _handleClose(): void {
    this._clearTimers()

    if (this.state !== 'disposed') {
      this.state = 'backoff'
    }

    if (this.connected) {
      this.connected = false
      this.options.onConnectionChange?.(false)
      this.options.onClose?.()
    }

    if (this.state === 'disposed') return

    if (document.hidden) {
      const handler = () => {
        if (this.state === 'disposed') return
        this._removeVisibilityListeners()
        this.connect()
      }
      this.visibilityHandler = handler
      this.pageshowHandler = handler
      document.addEventListener('visibilitychange', handler)
      window.addEventListener('pageshow', handler)
    } else {
      this.reconnectTimer = window.setTimeout(() => {
        if (this.state === 'disposed') return
        this.connect()
      }, RECONNECT_MS)
    }
  }

  private _armWatchdog(gen: number): void {
    if (this.watchdogTimer !== undefined) clearTimeout(this.watchdogTimer)
    this.watchdogTimer = window.setTimeout(() => {
      if (this.generation !== gen || this.state === 'disposed') return
      this.socket?.close()
    }, WATCHDOG_MS)
  }

  private _startHeartbeat(gen: number, ws: WebSocket): void {
    if (this.heartbeatTimer !== undefined) clearInterval(this.heartbeatTimer)
    this.heartbeatTimer = window.setInterval(() => {
      if (this.generation !== gen) {
        if (this.heartbeatTimer !== undefined) clearInterval(this.heartbeatTimer)
        return
      }
      if (ws.readyState === WebSocket.OPEN) {
        try {
          ws.send(JSON.stringify({ type: 'ping' }))
        } catch {
          /* ignored */
        }
      }
    }, HEARTBEAT_MS)
  }

  private _startFallback(gen: number): void {
    if (this.fallbackTimer !== undefined) clearTimeout(this.fallbackTimer)
    this.fallbackTimer = window.setTimeout(() => {
      if (this.generation !== gen || this.state !== 'connecting') return
      this.state = 'live'
      this.options.onFallback?.()
    }, FALLBACK_MS)
  }

  private _clearFallback(): void {
    if (this.fallbackTimer !== undefined) {
      clearTimeout(this.fallbackTimer)
      this.fallbackTimer = undefined
    }
  }

  private _clearTimers(): void {
    if (this.watchdogTimer !== undefined) {
      clearTimeout(this.watchdogTimer)
      this.watchdogTimer = undefined
    }
    if (this.heartbeatTimer !== undefined) {
      clearInterval(this.heartbeatTimer)
      this.heartbeatTimer = undefined
    }
    this._clearFallback()
    if (this.reconnectTimer !== undefined) {
      clearTimeout(this.reconnectTimer)
      this.reconnectTimer = undefined
    }
  }

  private _removeVisibilityListeners(): void {
    if (this.visibilityHandler) {
      document.removeEventListener('visibilitychange', this.visibilityHandler)
      this.visibilityHandler = null
    }
    if (this.pageshowHandler) {
      window.removeEventListener('pageshow', this.pageshowHandler)
      this.pageshowHandler = null
    }
  }

  private _detach(ws: WebSocket): void {
    ws.onopen = null
    ws.onmessage = null
    ws.onclose = null
    ws.onerror = null
  }
}

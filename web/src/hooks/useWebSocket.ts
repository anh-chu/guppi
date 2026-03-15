import { useEffect, useRef, useState } from 'react'

export function useWebSocket(path: string, onMessage: (data: any) => void) {
  const wsRef = useRef<WebSocket | null>(null)
  const reconnectTimer = useRef<number | undefined>(undefined)
  const hiddenRef = useRef(false)
  const hasConnected = useRef(false)
  const [connected, setConnected] = useState<boolean | null>(null) // null = initial connecting
  // Keep a ref to the latest onMessage so the WebSocket always calls the current version
  const onMessageRef = useRef(onMessage)
  onMessageRef.current = onMessage

  useEffect(() => {
    function connect() {
      const protocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:'
      const url = `${protocol}//${window.location.host}${path}`
      const ws = new WebSocket(url)
      wsRef.current = ws

      ws.onopen = () => {
        hasConnected.current = true
        setConnected(true)
      }

      ws.onmessage = (evt) => {
        try {
          const data = JSON.parse(evt.data)
          onMessageRef.current(data)
        } catch {
          // Non-JSON message, pass raw
          onMessageRef.current(evt.data)
        }
      }

      ws.onclose = () => {
        // Don't flash the disconnect banner if the page is just hidden
        // or if we haven't established a connection yet (initial connect)
        if (!hiddenRef.current && hasConnected.current) {
          setConnected(false)
        }
        // If hidden (e.g. iPad app switch), defer reconnect until visible
        if (hiddenRef.current) {
          // Will be reconnected by onVisibilityChange when page becomes visible
          return
        }
        reconnectTimer.current = window.setTimeout(connect, 2000)
      }

      ws.onerror = (err) => {
        console.error(`WS error: ${path}`, err)
        ws.close()
      }
    }

    function onVisibilityChange() {
      if (document.hidden) {
        hiddenRef.current = true
      } else {
        hiddenRef.current = false
        // If disconnected while hidden, reconnect immediately
        if (wsRef.current?.readyState !== WebSocket.OPEN) {
          if (reconnectTimer.current) {
            clearTimeout(reconnectTimer.current)
          }
          connect()
        }
      }
    }

    connect()
    document.addEventListener('visibilitychange', onVisibilityChange)
    window.addEventListener('pageshow', onVisibilityChange)

    return () => {
      document.removeEventListener('visibilitychange', onVisibilityChange)
      window.removeEventListener('pageshow', onVisibilityChange)
      if (reconnectTimer.current) {
        clearTimeout(reconnectTimer.current)
      }
      if (wsRef.current) {
        wsRef.current.close()
      }
    }
  }, [path])

  return { wsRef, connected }
}

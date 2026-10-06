import { useEffect, useRef, useCallback, useState } from 'react'
import { authToken } from '../host/hostBridge'

export type WSMessage = {
  type: string
  payload: any
}

export function useWebSocket(url: string | null) {
  const wsRef = useRef<WebSocket | null>(null)
  const reconnectRef = useRef<ReturnType<typeof setTimeout>>()
  const [connected, setConnected] = useState(false)
  const [lastMessage, setLastMessage] = useState<WSMessage | null>(null)
  const listenersRef = useRef<Map<string, Set<(data: any) => void>>>(new Map())

  const connect = useCallback(() => {
    if (!url) return
    if (wsRef.current?.readyState === WebSocket.OPEN) return

    // Добавляем JWT-токен
    const token = authToken()
    const wsUrl = url.includes('?') ? `${url}&token=${token}` : `${url}?token=${token}`

    const ws = new WebSocket(wsUrl)
    wsRef.current = ws

    ws.onopen = () => {
      setConnected(true)
      // Очищаем таймер реконнекта
      if (reconnectRef.current) clearTimeout(reconnectRef.current)
    }

    ws.onmessage = (event) => {
      try {
        const msg: WSMessage = JSON.parse(event.data)
        setLastMessage(msg)

        // Оповещаем подписчиков
        const listeners = listenersRef.current.get(msg.type)
        if (listeners) {
          listeners.forEach((fn) => fn(msg.payload))
        }
      } catch {
        // игнорируем не-JSON
      }
    }

    ws.onclose = () => {
      setConnected(false)
      // Реконнект через 3 сек
      reconnectRef.current = setTimeout(connect, 3000)
    }

    ws.onerror = () => {
      ws.close()
    }
  }, [url])

  useEffect(() => {
    connect()
    return () => {
      if (reconnectRef.current) clearTimeout(reconnectRef.current)
      if (wsRef.current) {
        wsRef.current.onclose = null // предотвращаем реконнект при размонтировании
        wsRef.current.close()
      }
    }
  }, [connect])

  const subscribe = useCallback((type: string, handler: (data: any) => void) => {
    if (!listenersRef.current.has(type)) {
      listenersRef.current.set(type, new Set())
    }
    listenersRef.current.get(type)!.add(handler)

    return () => {
      listenersRef.current.get(type)?.delete(handler)
    }
  }, [])

  const send = useCallback((data: object) => {
    if (wsRef.current?.readyState === WebSocket.OPEN) {
      wsRef.current.send(JSON.stringify(data))
    }
  }, [])

  return { connected, lastMessage, subscribe, send }
}
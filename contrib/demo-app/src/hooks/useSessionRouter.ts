/**
 * useSessionRouter — low-level hook for the koios WebSocket JSON-RPC
 * transport.
 *
 * Protocol notes:
 *   - Client sends: {"id":"<str>","method":"<m>","params":{...}}
 *   - Server replies: {"id":"<str>","result":{...}} or {"id":"<str>","error":{...}}
 *   - Streaming deltas (no id): {"method":"stream.delta","params":{"req_id":"<str>","content":"<tok>"}}
 *
 * The hook reconnects automatically whenever wsUrl changes (e.g. peer_id switch).
 */
import { useCallback, useEffect, useRef, useState } from 'react'

export type MessageRole = 'user' | 'assistant' | 'system'

export interface ChatMessage {
  role: MessageRole
  content: string
}

export type ConnStatus = 'disconnected' | 'connecting' | 'connected' | 'error'

export interface ConnState {
  status: ConnStatus
  error?: string
}

export interface RuntimeEvent {
  kind: string
  session_key?: string
  message?: string
  attempt?: number
  step?: number
  count?: number
  error?: string
}

export interface SessionPushMessage {
  role: MessageRole
  content: string
}

export interface SessionMessageEvent {
  peer_id?: string
  source?: string
  message?: SessionPushMessage
}

interface Pending {
  resolve: (result: unknown) => void
  reject: (err: Error) => void
  onDelta?: (content: string) => void
  onEvent?: (event: RuntimeEvent) => void
}

function rpcId(): string {
  return Math.random().toString(36).slice(2, 11)
}

export function useSessionRouter(wsUrl: string) {
  const wsRef = useRef<WebSocket | null>(null)
  const pendingRef = useRef(new Map<string, Pending>())
  const [connState, setConnState] = useState<ConnState>({ status: 'disconnected' })
  const sessionMessageListenersRef = useRef(new Set<(event: SessionMessageEvent) => void>())

  useEffect(() => {
    setConnState({ status: 'connecting' })
    const ws = new WebSocket(wsUrl)
    wsRef.current = ws

    ws.onopen = () => setConnState({ status: 'connected' })

    ws.onerror = () =>
      setConnState({ status: 'error', error: 'WebSocket connection failed' })

    ws.onclose = () => {
      setConnState(s => (s.status === 'error' ? s : { status: 'disconnected' }))
      pendingRef.current.forEach(p => p.reject(new Error('connection closed')))
      pendingRef.current.clear()
    }

    ws.onmessage = (ev: MessageEvent<string>) => {
      let frame: Record<string, unknown>
      try {
        frame = JSON.parse(ev.data) as Record<string, unknown>
      } catch {
        return
      }

      // Streaming notification: has method, no id
      if (frame.method === 'stream.delta') {
        const params = frame.params as { req_id?: unknown; content?: unknown } | undefined
        if (params?.req_id != null) {
          const key = String(params.req_id)
          pendingRef.current.get(key)?.onDelta?.(
            typeof params.content === 'string' ? params.content : '',
          )
        }
        return
      }

      if (frame.method === 'stream.event') {
        const params = frame.params as { req_id?: unknown; event?: unknown } | undefined
        if (params?.req_id != null) {
          const key = String(params.req_id)
          const event = params.event as RuntimeEvent | undefined
          if (event && typeof event.kind === 'string') {
            pendingRef.current.get(key)?.onEvent?.(event)
          }
        }
        return
      }

      if (frame.method === 'session.message') {
        const params = frame.params as SessionMessageEvent | undefined
        if (params?.message && typeof params.message.content === 'string') {
          sessionMessageListenersRef.current.forEach(listener => listener(params))
        }
        return
      }

      // Regular response: has id
      const id = frame.id
      if (id == null) return
      const key = String(id)
      const p = pendingRef.current.get(key)
      if (!p) return
      pendingRef.current.delete(key)

      if (frame.error) {
        const e = frame.error as { message?: string }
        p.reject(new Error(e.message ?? 'rpc error'))
      } else {
        p.resolve(frame.result)
      }
    }

    return () => {
      ws.onopen = null
      ws.onerror = null
      ws.onclose = null
      ws.onmessage = null
      ws.close()
      pendingRef.current.forEach(p => p.reject(new Error('connection closed')))
      pendingRef.current.clear()
    }
  }, [wsUrl])

  const call = useCallback(
    (
      method: string,
      params?: unknown,
      onDelta?: (c: string) => void,
      onEvent?: (event: RuntimeEvent) => void,
    ): Promise<unknown> =>
      new Promise((resolve, reject) => {
        const ws = wsRef.current
        if (!ws || ws.readyState !== WebSocket.OPEN) {
          reject(new Error('not connected'))
          return
        }
        const id = rpcId()
        pendingRef.current.set(id, { resolve, reject, onDelta, onEvent })
        try {
          ws.send(JSON.stringify({ id, method, params }))
        } catch (err) {
          pendingRef.current.delete(id)
          reject(err instanceof Error ? err : new Error(String(err)))
        }
      }),
    [],
  )

  const sendChat = useCallback(
    (
      messages: ChatMessage[],
      onDelta?: (c: string) => void,
      onEvent?: (event: RuntimeEvent) => void,
    ) =>
      call('chat', { messages, stream: !!onDelta }, onDelta, onEvent) as Promise<{
        assistant_text: string
        done: boolean
      }>,
    [call],
  )

  const resetSession = useCallback(() => call('session.reset'), [call])

  const onSessionMessage = useCallback((listener: (event: SessionMessageEvent) => void) => {
    sessionMessageListenersRef.current.add(listener)
    return () => {
      sessionMessageListenersRef.current.delete(listener)
    }
  }, [])

  return { connState, sendChat, resetSession, onSessionMessage }
}

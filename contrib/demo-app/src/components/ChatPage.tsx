/**
 * ChatPage — demonstrates koios integration.
 *
 * Key concepts shown:
 *  - Each peer_id owns an isolated conversation history on the server.
 *  - Switching peer_id reconnects the WebSocket and opens a fresh context.
 *  - Streaming replies arrive token-by-token via stream.delta notifications.
 *  - session.reset clears the server-side history for the current peer.
 */
import { useCallback, useEffect, useRef, useState } from 'react'
import { Brain } from 'lucide-react'
import ReactMarkdown from 'react-markdown'
import remarkGfm from 'remark-gfm'
import {
  useSessionRouter,
  type ChatMessage,
  type RuntimeEvent,
  type SessionMessageEvent,
} from '../hooks/useSessionRouter'

interface DisplayMsg {
  id: string
  role: 'user' | 'assistant' | 'system'
  content: string
  streaming?: boolean
  source?: string
  reasoning?: string
  reasoningSummary?: string
  reasoningOpen?: boolean
  provider?: string
  model?: string
  profile?: string
  providerProfile?: string
}

interface ActivityItem {
  id: string
  label: string
  kind: string
}

interface RunMeta {
  provider?: string
  model?: string
  profile?: string
  providerProfile?: string
}

/** peer_id must match the server's IsValidPeerID: alphanumeric + -_.:@ ≤256 */
const PEER_ID_RE = /^[a-zA-Z0-9\-_.:@]{1,256}$/
const STORAGE_KEY = 'sr-demo-peer-id'

function newPeerId(): string {
  return 'peer-' + Math.random().toString(36).slice(2, 10)
}

function normalizeReasoningText(value?: string): string {
  return (value ?? '').replace(/\s+/g, ' ').trim()
}

function reasoningExcerpt(summary?: string, reasoning?: string, maxLength = 140): string {
  const text = normalizeReasoningText(summary) || normalizeReasoningText(reasoning)
  if (text.length <= maxLength) return text
  return `${text.slice(0, maxLength).trimEnd()}…`
}

export function ChatPage() {
  const [peerId, setPeerId] = useState<string>(
    () => sessionStorage.getItem(STORAGE_KEY) ?? newPeerId(),
  )
  const [editPeerId, setEditPeerId] = useState(peerId)
  const [messages, setMessages] = useState<DisplayMsg[]>([])
  const [activity, setActivity] = useState<ActivityItem[]>([])
  const [activeRunMeta, setActiveRunMeta] = useState<RunMeta>({})
  const [input, setInput] = useState('')
  const [busy, setBusy] = useState(false)
  const bottomRef = useRef<HTMLDivElement>(null)
  const configuredReasoningPeerRef = useRef<string | null>(null)

  const protocol = location.protocol === 'https:' ? 'wss' : 'ws'
  const wsUrl = `${protocol}://${location.host}/v1/ws?peer_id=${encodeURIComponent(peerId)}`

  const { connState, sendChat, resetSession, patchSession, onSessionMessage } = useSessionRouter(wsUrl)

  // Keep sessionStorage and the edit field in sync when peerId changes.
  useEffect(() => {
    sessionStorage.setItem(STORAGE_KEY, peerId)
    setEditPeerId(peerId)
  }, [peerId])

  // Auto-scroll to the latest message.
  useEffect(() => {
    bottomRef.current?.scrollIntoView({ behavior: 'smooth' })
  }, [messages])

  useEffect(() => {
    return onSessionMessage((event: SessionMessageEvent) => {
      const pushed = event.message
      if (!pushed?.content) return
      setMessages(prev => [
        ...prev,
        {
          id: `push-${Date.now()}-${Math.random()}`,
          role: pushed.role === 'system' ? 'system' : 'assistant',
          content: pushed.content,
          source: event.source,
        },
      ])
      setActivity(prev => {
        const label = event.source ? `${event.source} update received` : 'Session update received'
        const next = [...prev, { id: `act-${Date.now()}-${Math.random()}`, label, kind: 'session_message' }]
        return next.slice(-8)
      })
    })
  }, [onSessionMessage])

  const switchPeer = useCallback(() => {
    const id = editPeerId.trim()
    if (!id || !PEER_ID_RE.test(id) || id === peerId) return
    configuredReasoningPeerRef.current = null
    setPeerId(id)
    setMessages([])
    setActivity([])
    setActiveRunMeta({})
  }, [editPeerId, peerId])

  const applyRunMetaToMessage = useCallback((messageID: string, meta: RunMeta) => {
    setMessages(prev =>
      prev.map(m =>
        m.id === messageID
          ? {
              ...m,
              provider: meta.provider ?? m.provider,
              model: meta.model ?? m.model,
              profile: meta.profile ?? m.profile,
              providerProfile: meta.providerProfile ?? m.providerProfile,
            }
          : m,
      ),
    )
  }, [])

  const appendReasoningToMessage = useCallback(
    (messageID: string, content: string, kind: 'delta' | 'summary') => {
      if (!content) return
      setMessages(prev =>
        prev.map(m => {
          if (m.id !== messageID) return m
          if (kind === 'summary') {
            return { ...m, reasoningSummary: content }
          }
          return { ...m, reasoning: `${m.reasoning ?? ''}${content}` }
        }),
      )
    },
    [],
  )

  const setReasoningOpen = useCallback((messageID: string, open: boolean) => {
    setMessages(prev => prev.map(m => (m.id === messageID ? { ...m, reasoningOpen: open } : m)))
  }, [])

  const pushActivity = useCallback((event: RuntimeEvent) => {
    const label = (() => {
      switch (event.kind) {
        case 'run_start': {
          const details = [
            event.provider ? `provider ${event.provider}` : '',
            event.model ? `model ${event.model}` : '',
            event.profile ? `profile ${event.profile}` : '',
            event.provider_profile ? `provider profile ${event.provider_profile}` : '',
          ].filter(Boolean)
          return details.length > 0 ? `Run started (${details.join(', ')})` : 'Run started'
        }
        case 'step_start':
          return `Step ${event.step ?? '?'} started`
        case 'context_built':
          return `Context built${event.count ? ` with ${event.count} messages` : ''}`
        case 'memory_injected':
          return `Memory injected${event.count ? ` (${event.count} hits)` : ''}`
        case 'tool_call':
          return `Calling ${event.message ?? 'tool'}`
        case 'tool_result':
          return `Completed ${event.message ?? 'tool'}`
        case 'run_retry':
          return `Retrying${event.attempt ? ` attempt ${event.attempt}` : ''}`
        case 'run_error':
          return event.error ? `Error: ${event.error}` : 'Run error'
        case 'run_finish':
          return 'Reply ready'
        default:
          return event.kind
      }
    })()

    setActivity(prev => {
      const next = [...prev, { id: `${Date.now()}-${Math.random()}`, label, kind: event.kind }]
      return next.slice(-8)
    })
  }, [])

  const ensureReasoningEnabled = useCallback(async () => {
    if (configuredReasoningPeerRef.current === peerId) return
    await patchSession({
      session_key: peerId,
      think_level: 'medium',
      reasoning_visibility: 'full',
    })
    configuredReasoningPeerRef.current = peerId
  }, [patchSession, peerId])

  useEffect(() => {
    if (connState.status !== 'connected' || configuredReasoningPeerRef.current === peerId) return
    void ensureReasoningEnabled().catch(() => {
      // Keep the demo usable even if session policy patching fails.
    })
  }, [connState.status, ensureReasoningEnabled, peerId])

  const handleSend = useCallback(async () => {
    const text = input.trim()
    if (!text || busy || connState.status !== 'connected') return

    const userMsgId = `u-${Date.now()}`
    const asstMsgId = `a-${Date.now()}`

    setMessages(prev => [
      ...prev,
      { id: userMsgId, role: 'user', content: text },
      { id: asstMsgId, role: 'assistant', content: '', streaming: true, reasoningOpen: true },
    ])
    setInput('')
    setBusy(true)

    const msgs: ChatMessage[] = [{ role: 'user', content: text }]
    try {
      let accumulated = ''
      setActivity([])
      await ensureReasoningEnabled()
      await sendChat(
        msgs,
        delta => {
          accumulated += delta
          setMessages(prev =>
            prev.map(m => (m.id === asstMsgId ? { ...m, content: accumulated } : m)),
          )
        },
        event => {
          if (event.kind === 'run_start') {
            const meta = {
              provider: event.provider,
              model: event.model,
              profile: event.profile,
              providerProfile: event.provider_profile,
            }
            setActiveRunMeta(meta)
            applyRunMetaToMessage(asstMsgId, meta)
          } else if (event.kind === 'reasoning_delta' && event.content) {
            appendReasoningToMessage(asstMsgId, event.content, 'delta')
          } else if (event.kind === 'reasoning_summary' && event.content) {
            appendReasoningToMessage(asstMsgId, event.content, 'summary')
          }
          pushActivity(event)
        },
      )
      setMessages(prev =>
        prev.map(m => (m.id === asstMsgId ? { ...m, streaming: false, reasoningOpen: false } : m)),
      )
    } catch (err) {
      const msg = err instanceof Error ? err.message : 'Unknown error'
      setMessages(prev =>
        prev.map(m =>
          m.id === asstMsgId
            ? { ...m, content: `⚠ ${msg}`, streaming: false, reasoningOpen: false }
            : m,
        ),
      )
    } finally {
      setBusy(false)
    }
  }, [input, busy, connState.status, ensureReasoningEnabled, sendChat])

  const handleReset = useCallback(async () => {
    try {
      await resetSession()
      setMessages([])
      setActivity([])
      setActiveRunMeta({})
    } catch {
      // Socket may be temporarily down; swallow and let the user retry.
    }
  }, [resetSession])

  const peerIdValid = PEER_ID_RE.test(editPeerId.trim())
  const isConnected = connState.status === 'connected'

  return (
    <div className="chat-page">
      {/* ── Header ─────────────────────────────────────────── */}
      <div className="chat-header">
        <div className="chat-peer-row">
          <label htmlFor="peer-id-input" className="peer-label">
            peer_id
          </label>
          <input
            id="peer-id-input"
            className="peer-input"
            value={editPeerId}
            onChange={e => setEditPeerId(e.target.value)}
            onKeyDown={e => e.key === 'Enter' && switchPeer()}
            spellCheck={false}
            autoComplete="off"
            placeholder="type a peer id…"
            aria-label="Session peer ID"
          />
          <button
            className="btn-sm"
            onClick={switchPeer}
            disabled={!peerIdValid || editPeerId.trim() === peerId || !editPeerId.trim()}
            title="Connect with this peer ID (isolated session)"
          >
            Switch
          </button>
          <button
            className="btn-sm"
            onClick={() => {
              const id = newPeerId()
              setEditPeerId(id)
              configuredReasoningPeerRef.current = null
              setPeerId(id)
              setMessages([])
              setActivity([])
              setActiveRunMeta({})
            }}
            title="Generate a new random peer ID and start a fresh session"
          >
            New session
          </button>
        </div>

        <div className="chat-status-row">
          <span
            className="conn-dot"
            data-status={connState.status}
            aria-label={`Connection: ${connState.status}`}
          />
          <span className="conn-label">
            {connState.status}
            {connState.error ? ` — ${connState.error}` : ''}
          </span>
          <div className="chat-run-meta" aria-label="Active model and provider">
            {activeRunMeta.provider && <span className="chat-meta-pill">{activeRunMeta.provider}</span>}
            {activeRunMeta.model && <span className="chat-meta-pill">{activeRunMeta.model}</span>}
            {activeRunMeta.profile && <span className="chat-meta-pill">profile: {activeRunMeta.profile}</span>}
            {activeRunMeta.providerProfile && (
              <span className="chat-meta-pill">provider profile: {activeRunMeta.providerProfile}</span>
            )}
          </div>
          <button
            className="btn-sm btn-ghost"
            onClick={handleReset}
            disabled={busy || !isConnected}
            title="Clear server-side history for this peer (sends session.reset)"
          >
            Clear history
          </button>
        </div>
        {activity.length > 0 && (
          <div className="chat-activity" aria-live="polite">
            {activity.map(item => (
              <div key={item.id} className="activity-item" data-kind={item.kind}>
                {item.label}
              </div>
            ))}
          </div>
        )}
      </div>

      {/* ── Messages ───────────────────────────────────────── */}
      <div
        className="chat-messages"
        role="log"
        aria-label="Chat messages"
        aria-live="polite"
      >
        {messages.length === 0 ? (
          <div className="chat-empty">
            <p>Send a message to start chatting.</p>
            <p className="chat-hint">
              Each <code>peer_id</code> has its own isolated session history on the
              server. Switch or create a new session above to see isolation in action.
            </p>
          </div>
        ) : (
          messages.map(msg => {
            const excerpt = reasoningExcerpt(msg.reasoningSummary, msg.reasoning)
            const showReasoningSummary =
              normalizeReasoningText(msg.reasoningSummary) !== '' &&
              normalizeReasoningText(msg.reasoningSummary) !== normalizeReasoningText(msg.reasoning)

            return (
            <div key={msg.id} className={`bubble bubble-${msg.role}`}>
              <span className="bubble-author">
                {msg.role === 'user' ? 'You' : msg.role === 'system' ? (msg.source ? `${msg.source}` : 'System') : 'Assistant'}
              </span>
              <div className="bubble-body">
                {msg.role === 'assistant' && (msg.reasoningSummary || msg.reasoning) && (
                  <details
                    className="bubble-reasoning"
                    open={msg.streaming ? true : !!msg.reasoningOpen}
                    onToggle={e => {
                      if (msg.streaming) return
                      setReasoningOpen(msg.id, (e.currentTarget as HTMLDetailsElement).open)
                    }}
                  >
                    <summary>
                      <span className="bubble-reasoning-toggle">
                        <Brain size={14} strokeWidth={1.8} aria-hidden="true" />
                        {excerpt && <span>{excerpt}</span>}
                      </span>
                    </summary>
                    <div className="bubble-reasoning-body">
                      {showReasoningSummary && (
                        <div className="bubble-reasoning-summary">{msg.reasoningSummary}</div>
                      )}
                      {msg.reasoning && (
                        <ReactMarkdown remarkPlugins={[remarkGfm]}>{msg.reasoning}</ReactMarkdown>
                      )}
                    </div>
                  </details>
                )}
                <div className="bubble-text">
                  <ReactMarkdown remarkPlugins={[remarkGfm]}>{msg.content}</ReactMarkdown>
                  {msg.streaming && (
                    <span className="cursor" aria-hidden="true">
                      ▌
                    </span>
                  )}
                </div>
              </div>
            </div>
          )})
        )}
        <div ref={bottomRef} />
      </div>

      {/* ── Input ──────────────────────────────────────────── */}
      <form
        className="chat-input-row"
        onSubmit={e => {
          e.preventDefault()
          void handleSend()
        }}
      >
        <textarea
          className="chat-textarea"
          value={input}
          onChange={e => setInput(e.target.value)}
          onKeyDown={e => {
            if (e.key === 'Enter' && !e.shiftKey) {
              e.preventDefault()
              void handleSend()
            }
          }}
          placeholder={
            isConnected
              ? 'Message… (Enter to send, Shift+Enter for new line)'
              : 'Waiting for connection…'
          }
          rows={2}
          disabled={busy || !isConnected}
          aria-label="Message input"
        />
        <button
          type="submit"
          className="send-btn"
          disabled={!input.trim() || busy || !isConnected}
          aria-label="Send message"
        >
          {busy ? '…' : 'Send'}
        </button>
      </form>
    </div>
  )
}

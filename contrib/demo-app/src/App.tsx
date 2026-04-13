import { useState } from 'react'
import { ChatPage } from './components/ChatPage'
import './App.css'

type Page = 'home' | 'chat'

export default function App() {
  const [page, setPage] = useState<Page>('home')

  return (
    <>
      <header className="app-header">
        <span className="app-brand">koios</span>
        <nav className="app-nav">
          <button
            className={page === 'home' ? 'nav-link nav-active' : 'nav-link'}
            onClick={() => setPage('home')}
          >
            Home
          </button>
          <button
            className={page === 'chat' ? 'nav-link nav-active' : 'nav-link'}
            onClick={() => setPage('chat')}
          >
            Chat demo
          </button>
        </nav>
      </header>

      {page === 'chat' ? <ChatPage /> : <HomePage onStart={() => setPage('chat')} />}
    </>
  )
}

function HomePage({ onStart }: { onStart: () => void }) {
  return (
    <section className="home-page">
      <h1>koios</h1>
      <p className="home-lead">
        A lightweight AI session proxy that routes chat messages to an upstream LLM
        while maintaining isolated per-peer conversation history.
      </p>
      <ul className="feature-list">
        <li>
          <code>GET /v1/ws?peer_id=…</code> — WebSocket JSON-RPC endpoint
        </li>
        <li>
          Per-peer session isolation — each <code>peer_id</code> owns its own
          conversation history and memory
        </li>
        <li>
          Streaming token delivery via <code>stream.delta</code> notifications
        </li>
        <li>Session reset, scheduled jobs, sub-agents, and semantic memory built-in</li>
      </ul>
      <button className="cta-btn" onClick={onStart}>
        Open chat demo →
      </button>
    </section>
  )
}

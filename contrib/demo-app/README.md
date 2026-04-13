# koios demo app

A React + TypeScript demo showing how to integrate with
[koios](../../README.md) — a lightweight AI session proxy that routes
chat requests to an upstream LLM while maintaining isolated per-peer
conversation history.

## What it demonstrates

- Connecting to the koios WebSocket endpoint (`/v1/ws?peer_id=…`)
- Sending chat messages via JSON-RPC (`chat` method with `stream: true`)
- Receiving streaming token deltas via `stream.delta` notifications
- **Session isolation** — each `peer_id` owns an independent conversation
  history on the server; switch peer IDs to start a completely fresh context
- Resetting server-side history with `session.reset`

## Prerequisites

1. A running **koios** instance on `localhost:8080` (the default).
   See the [root README](../../README.md) for how to start it.
2. Node.js 18+

## Getting started

```
cd contrib/demo-app
npm install
npm run dev
```

The Vite dev server starts at **http://localhost:5173** and automatically
proxies `/v1/ws` to `localhost:8080`, so no CORS configuration is needed.

## Key files

| File | Purpose |
|------|---------|
| `src/hooks/useSessionRouter.ts` | WebSocket connection + JSON-RPC helper |
| `src/components/ChatPage.tsx` | Chat UI with streaming and session controls |
| `vite.config.ts` | Dev-server WebSocket proxy configuration |

## Protocol overview

```
Client                                koios
  │                                         │
  ├─ GET /v1/ws?peer_id=alice  (WS) ───────>│
  │<────────────────────────────────────────┤  (connected)
  │                                         │
  ├─ {"id":"1","method":"chat",             │
  │   "params":{"messages":[               │
  │     {"role":"user","content":"Hi"}],    │
  │   "stream":true}}  ─────────────────── >│
  │                                         │
  │<─ {"method":"stream.delta",             │
  │    "params":{"req_id":"1",              │
  │              "content":"Hello"}}        │
  │   … (more delta frames) …              │
  │<─ {"id":"1","result":                   │
  │    {"assistant_text":"Hello!","done":true}}
```

Session history is stored **server-side per `peer_id`**. Reconnecting with
the same `peer_id` resumes where you left off. Using a different `peer_id`
gives a completely independent context. Call `session.reset` to clear history
without changing the peer ID.

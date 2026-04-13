# koios

A lightweight daemon that exposes a **single WebSocket JSON-RPC control plane** for all peer operations: stateful chat, agent runs, subagent orchestration, long-term semantic memory, standing orders, cron scheduling, and heartbeat. Every request and response flows through one persistent connection per peer.

Its key feature is **per-peer session isolation**: each client is identified by the `peer_id` query parameter on the WebSocket upgrade. The daemon maintains a completely private conversation history per peer — no peer can observe or influence another's session.

---

## Requirements

- Go 1.26.1 or later

---

## Configuration

All configuration is done via environment variables. Copy `.env.sample` to `.env` and fill in the required values:

```sh
cp .env.sample .env
```

| Variable | Required | Default | Description |
|---|---|---|---|
| `LLM_API_KEY` | **Yes** | — | API key for the LLM provider |
| `LLM_MODEL` | **Yes** | — | Model name (e.g. `gpt-4o`, `claude-3-5-sonnet-20241022`) |
| `LLM_PROVIDER` | No | `openai` | Backend: `openai`, `anthropic`, `openrouter`, or `nvidia` |
| `LLM_BASE_URL` | No | provider default | Override base URL (useful for local proxies) |
| `LISTEN_ADDR` | No | `:8080` | TCP address to listen on |
| `SESSION_MAX_MESSAGES` | No | `100` | Max messages per peer session before pruning |
| `SESSION_DIR` | No | — | Directory for JSONL session persistence (omit for in-memory only) |
| `REQUEST_TIMEOUT` | No | `2m` | Max duration for a single LLM round-trip |
| `COMPACTION_THRESHOLD` | No | `0` | Message count that triggers LLM-based session compaction (0 = off) |
| `COMPACTION_RESERVE` | No | `20` | Messages kept verbatim after compaction |
| `MEMORY_DB_PATH` | No | — | SQLite path for long-term semantic memory (omit to disable) |
| `EMBED_MODEL` | No | `text-embedding-3-small` | Embedding model used for memory re-ranking |
| `MEMORY_INJECT` | No | `false` | Prepend top-K memory hits as a system message on each `chat` and `agent.run` request |
| `MEMORY_TOP_K` | No | `3` | Number of memory chunks to inject |
| `SESSION_PRUNE_KEEP_TOOL_MESSAGES` | No | `8` | Number of recent tool-related messages kept in model-visible context |
| `CRON_DIR` | No | — | Directory for cron job persistence (omit to disable scheduler) |
| `CRON_MAX_CONCURRENT` | No | `1` | Max concurrent cron job executions |
| `HEARTBEAT_ENABLED` | No | `true` | Enable per-peer heartbeat goroutines |
| `HEARTBEAT_EVERY` | No | `30m` | Default heartbeat interval |
| `AGENT_DIR` | No | — | Directory for subagent run record persistence |
| `AGENT_MAX_CHILDREN` | No | `4` | Max concurrent child runs per parent |
| `AGENT_RETRY_ATTEMPTS` | No | `3` | Max attempts for agent runtime retries |
| `AGENT_RETRY_INITIAL_BACKOFF` | No | `500ms` | Initial retry backoff |
| `AGENT_RETRY_MAX_BACKOFF` | No | `5s` | Maximum retry backoff cap |

---

## Development

### Build

```sh
go build \
  -ldflags "-X main.version=$(cat VERSION) -X main.gitHash=$(git rev-parse --short HEAD) -X main.buildTime=$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
  -o koios .
```

Or use the release script (recommended — see [Versioning](#versioning) below):

```sh
scripts/release.sh
```

To build without embedding metadata (all fields default to `dev` / `unknown`):

```sh
go build -o koios .
```

### Run locally

Export your environment variables (or use a tool like [`direnv`](https://direnv.net/)):

```sh
export LLM_API_KEY=sk-...
export LLM_MODEL=gpt-4o
export LLM_PROVIDER=openai

go run .
```

Or source from a `.env` file:

```sh
set -a && source .env && set +a
go run .
```

### Run tests

```sh
go test ./...
```

With verbose output and race detection:

```sh
go test -v -race ./...
```

---

## Running the daemon

### Binary

```sh
./koios
```

### With environment file

```sh
set -a && source .env && set +a
./koios
```

### Health check

```sh
curl http://localhost:8080/healthz
# {"status":"ok"}
```

### Version endpoint

```sh
curl http://localhost:8080/
# {"build_time":"2026-03-31T12:00:00Z","git_hash":"a490170","version":"0.1.0"}
```

---

## Versioning

The canonical version lives in the [`VERSION`](VERSION) file at the repository root.

### Bump the version

```sh
# patch: 0.1.0 → 0.1.1
scripts/bump-version.sh patch

# minor: 0.1.0 → 0.2.0
scripts/bump-version.sh minor

# major: 0.1.0 → 1.0.0
scripts/bump-version.sh major
```

The version is embedded in the binary at build time so that `GET /` reports version, git hash, and build time at runtime:

```sh
scripts/release.sh          # builds ./koios
scripts/release.sh dist/sr  # custom output path
```

---

## WebSocket Control Plane

All peer operations go through a single WebSocket endpoint:

```
GET /v1/ws?peer_id=<id>
```

`peer_id` must be non-empty and contain only alphanumeric characters plus `-`, `_`, `.`, `@`, `:` (max 256 chars). The connection carries full session state for that peer for its lifetime.

### Protocol

Every frame is a JSON object.

**Client → Server (request):**
```json
{"id": "1", "method": "chat", "params": { ... }}
```

**Server → Client (success response):**
```json
{"id": "1", "result": { ... }}
```

**Server → Client (error response):**
```json
{"id": "1", "error": {"code": -32000, "message": "..."}}
```

**Server → Client (streaming delta — no `id`):**
```json
{"method": "stream.delta", "params": {"req_id": "1", "content": "token"}}
```

When `"stream": true` is set on `chat` or `agent.run`, server pushes `stream.delta` notifications for each token, then sends the normal response frame as the terminal event.

### Error codes

| Code | Meaning |
|---|---|
| `-32700` | Parse error — malformed JSON |
| `-32600` | Invalid request — missing required field |
| `-32601` | Method not found |
| `-32602` | Invalid params |
| `-32000` | Server error |

---

## Methods

### `ping`

Liveness check.

```json
{"id": "1", "method": "ping"}
```

### `server.capabilities`

Return the currently enabled subsystems, exposed RPC methods, chat tool names, and stream notification types for this peer connection. This is useful for clients that need to know immediately whether features such as cron or memory are available.

```json
{"id": "1a", "method": "server.capabilities"}
```

Example response:

```json
{
  "id": "1a",
  "result": {
    "peer_id": "alice",
    "capabilities": {
      "agent_runtime": true,
      "memory": false,
      "cron": true,
      "standing": true,
      "heartbeat": false,
      "subagents": false
    },
    "methods": ["ping", "server.capabilities", "chat", "session.history", "session.reset", "standing.get", "standing.set", "standing.clear", "agent.run", "agent.start", "agent.get", "agent.wait", "agent.cancel", "cron.list", "cron.create", "cron.get", "cron.update", "cron.delete", "cron.trigger", "cron.runs"],
    "chat_tools": ["time.now", "cron.list", "cron.create", "cron.get", "cron.update", "cron.delete", "cron.trigger", "cron.runs", "session.reset"],
    "stream_notifications": ["stream.delta", "stream.event", "session.message"]
  }
}
```
```json
{"id": "1", "result": {"pong": true}}
```

---

### `chat`

Send a chat turn. Session history is automatically prepended. The server-configured model is always used regardless of any model field in params.

**Params:**

| Field | Type | Description |
|---|---|---|
| `messages` | `Message[]` | **Required.** One or more messages. Roles: `system`, `user`, `assistant`, `tool`, `function` |
| `stream` | `bool` | Stream token deltas via `stream.delta` notifications (default `false`) |
| `max_tokens` | `int` | Optional token limit |
| `temperature` | `float` | Optional sampling temperature |
| `top_p` | `float` | Optional nucleus sampling |

```json
{"id": "2", "method": "chat", "params": {
  "messages": [{"role": "user", "content": "Hello!"}]
}}
```
```json
{"id": "2", "result": {"id": "...", "choices": [{"message": {"role": "assistant", "content": "Hi!"}}], ...}}
```

Streaming example — deltas arrive first, then the terminal response:
```json
{"id": "3", "method": "chat", "params": {"messages": [...], "stream": true}}

{"method": "stream.delta", "params": {"req_id": "3", "content": "Hi"}}
{"method": "stream.delta", "params": {"req_id": "3", "content": "!"}}
{"id": "3", "result": {"assistant_text": "Hi!", "done": true}}
```

---

### `session.history`

Return the stored conversation history for the current peer session. By default this reads the current peer chat session; `session_key` can be supplied to inspect another known session scope.

```json
{"id": "3b", "method": "session.history", "params": {"limit": 50}}
```

Example response:

```json
{
  "id": "3b",
  "result": {
    "peer_id": "alice",
    "session_key": "alice",
    "count": 2,
    "messages": [
      {"role": "user", "content": "hello"},
      {"role": "assistant", "content": "hi"}
    ]
  }
}
```

### `session.reset`

Clear the conversation history for this peer.

```json
{"id": "4", "method": "session.reset"}
```
```json
{"id": "4", "result": {"ok": true}}
```

---

### `agent.run`

Execute an agent turn with scoped session handling, shared context assembly, optional memory injection, tool execution, session-lane serialization, and automatic retry on transient errors (up to `AGENT_RETRY_ATTEMPTS`). Calls for the same resolved session key are serialized through the agent coordinator.

**Params:**

| Field | Type | Description |
|---|---|---|
| `messages` | `Message[]` | **Required.** Turn messages |
| `scope` | `string` | Session scope: `main` (default), `direct`, `isolated`, `global` |
| `sender_id` | `string` | Used with `scope=direct` to key the session |
| `session_key` | `string` | Explicit session key override |
| `stream` | `bool` | Stream token deltas |
| `max_steps` | `int` | Maximum runtime steps; currently defaults to `1` and exits on first successful reply |
| `timeout` | `string` | Go duration string, e.g. `"30s"` |

**Session scopes:**

| Scope | Session key |
|---|---|
| `main` | `{peer_id}::main` |
| `direct` | `{peer_id}::sender::{sender_id}` |
| `isolated` | `{peer_id}::isolated::{uuid}` (fresh per call) |
| `global` | `__global__` (shared across all peers) |

```json
{"id": "5", "method": "agent.run", "params": {
  "messages": [{"role": "user", "content": "Summarise the last session"}],
  "scope": "main"
}}
```

### `agent.start`

Queue an asynchronous agent run and return immediately with the queued run record.

```json
{"id": "5a", "method": "agent.start", "params": {
  "messages": [{"role": "user", "content": "Summarise the last session"}],
  "scope": "main"
}}
```

### `agent.get`

Fetch the current status of a queued or completed agent run.

```json
{"id": "5b", "method": "agent.get", "params": {"id": "<run-id>"}}
```

### `agent.wait`

Wait for a queued agent run to finish and return the final run record.

```json
{"id": "5c", "method": "agent.wait", "params": {"id": "<run-id>", "timeout": "30s"}}
```

---

### `subagent.spawn`

Spawn an asynchronous child agent run. Returns immediately with the run record; poll `subagent.get` for status.

**Params:**

| Field | Type | Description |
|---|---|---|
| `task` | `string` | **Required.** Task description |
| `parent_id` | `string` | ID of the parent run (for child-count limiting) |
| `model` | `string` | Override model for this child |
| `timeout` | `duration` | Timeout for the child run |
| `attachments` | `Attachment[]` | File/text attachments injected into the task prompt |
| `role` | `string` | `main`, `orchestrator`, or `leaf` |
| `max_children` | `int` | Override max children for this run |
| `stream` | `bool` | Stream child output |

```json
{"id": "6", "method": "subagent.spawn", "params": {
  "task": "Research and summarise topic X"
}}
```

---

### `subagent.list`

List all subagent runs for this peer.

```json
{"id": "7", "method": "subagent.list"}
```

---

### `subagent.get`

Get the current state of a run.

```json
{"id": "8", "method": "subagent.get", "params": {"id": "<run-id>"}}
```

---

### `subagent.kill`

Cancel an active run.

```json
{"id": "9", "method": "subagent.kill", "params": {"id": "<run-id>"}}
```

---

### `subagent.steer`

Inject a steering note into a running child's session.

```json
{"id": "10", "method": "subagent.steer", "params": {"id": "<run-id>", "note": "Focus on cost reduction"}}
```

---

### `subagent.transcript`

Read the persisted message transcript for a run.

```json
{"id": "11", "method": "subagent.transcript", "params": {"id": "<run-id>"}}
```

---

### `memory.search`

Search long-term semantic memory for this peer. Requires `MEMORY_DB_PATH` to be set.

```json
{"id": "12", "method": "memory.search", "params": {"q": "project deadline", "limit": 5}}
```

---

### `cron.list` / `cron.create` / `cron.get` / `cron.update` / `cron.delete`

Manage scheduled jobs. Requires `CRON_DIR` to be set.

**Schedule kinds:** `at` (one-shot ISO 8601), `every` (fixed interval in ms), `cron` (5-field cron expression + optional IANA timezone).

**Payload kinds:** `systemEvent` (injects a system message into the session), `agentTurn` (executes an isolated LLM call and appends the result).

```json
{"id": "13", "method": "cron.create", "params": {
  "name": "daily-briefing",
  "schedule": {"kind": "cron", "expr": "0 9 * * *", "tz": "America/New_York"},
  "payload": {"kind": "agentTurn", "message": "What is on my agenda today?"},
  "enabled": true
}}
```

---

### `cron.trigger`

Force an immediate run of a job.

```json
{"id": "14", "method": "cron.trigger", "params": {"id": "<job-id>"}}
```

---

### `cron.runs`

Read the run history for a job.

```json
{"id": "15", "method": "cron.runs", "params": {"id": "<job-id>", "limit": 50}}
```

---

### `standing.get` / `standing.set` / `standing.clear`

Read or update standing orders for this peer.

Standing orders are modeled after OpenClaw's persistent operating instructions:
- If `<workspace>/STANDING_ORDERS.md` exists, it is injected as a system message on each `chat`, `agent.run`, heartbeat run, and cron `agentTurn`.
- Peer-specific standing orders are layered on top of the workspace file.
- Peer-specific standing orders persist only when `CRON_DIR` is set; without it, `standing.get` is read-only and still returns any workspace-level standing orders.

```json
{"id": "15a", "method": "standing.get"}
```

```json
{"id": "15b", "method": "standing.set", "params": {
  "content": "You may triage my inbox and draft concise replies, but do not send messages without asking."
}}
```

```json
{"id": "15c", "method": "standing.clear"}
```

---

### `heartbeat.get` / `heartbeat.set`

Read or update the heartbeat configuration for this peer. Requires `CRON_DIR` and `HEARTBEAT_ENABLED=true`.

Heartbeat runs are modeled as background main-session awareness checks:
- Standing orders are injected before the heartbeat-specific prompt.
- The request is built from the current peer session history plus the configured heartbeat prompt.
- If `<workspace>/HEARTBEAT.md` exists, its contents are injected as a system message for each heartbeat turn.
- `HEARTBEAT_OK` replies are suppressed.
- Non-`HEARTBEAT_OK` replies are appended back into the peer session as `[heartbeat] ...`.
- Saved heartbeat configs are restored on daemon startup, so heartbeats are not gated on a fresh WebSocket connection after restart.

```json
{"id": "16", "method": "heartbeat.set", "params": {
  "enabled": true,
  "every": "1h",
  "prompt": "Check if any tasks need attention.",
  "active_hours": {"start": "08:00", "end": "20:00", "timezone": "UTC"}
}}
```

---

### `heartbeat.wake`

Trigger an immediate out-of-schedule heartbeat run.

```json
{"id": "17", "method": "heartbeat.wake"}
```

---

## Providers

| Provider value | Backend | Notes |
|---|---|---|
| `openai` | [OpenAI](https://platform.openai.com/) | Default |
| `anthropic` | [Anthropic](https://www.anthropic.com/) | Requests are translated from OpenAI format automatically |
| `openrouter` | [OpenRouter](https://openrouter.ai/) | OpenAI-compatible; supports models from many providers |
| `nvidia` | [NVIDIA NIM](https://build.nvidia.com/) | OpenAI-compatible |

To use a local model served by [Ollama](https://ollama.com/) or another OpenAI-compatible server:

```sh
LLM_PROVIDER=openai
LLM_BASE_URL=http://localhost:11434/v1
LLM_API_KEY=ollama   # any non-empty value
LLM_MODEL=llama3.2
```

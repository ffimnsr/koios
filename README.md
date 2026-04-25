# koios

A lightweight daemon that exposes a **single WebSocket JSON-RPC control plane** for all peer operations: stateful chat, agent runs, subagent orchestration, long-term semantic memory, standing orders, cron scheduling, and heartbeat. Every request and response flows through one persistent connection per peer.

Its key feature is **per-peer session isolation**: each client is identified by the `peer_id` query parameter on the WebSocket upgrade. The daemon maintains a completely private conversation history per peer — no peer can observe or influence another's session.

Project policies and contribution guidance live in [LICENSE](LICENSE), [CONTRIBUTING.md](CONTRIBUTING.md), [CODE_OF_CONDUCT.md](CODE_OF_CONDUCT.md), [SECURITY.md](SECURITY.md), and [CHANGELOG.md](CHANGELOG.md).

---

## Requirements

- Go 1.26.1 or later

---

## Configuration

All configuration is now done via TOML.
Generate a starter config:

```sh
koios init
```

If you already have an older config format, migrate it in place:

```sh
koios init --migrate
```

This creates `koios.config.toml` with sane defaults and without private keys.
Key sections:
- `[server]` listen address, request timeout, allowed origins
- `[llm]` provider/model/base URL and optional `api_key`
- `[session]`, `[compaction]`, `[memory]` for context and storage behavior
- `session.prune_keep_tool_messages` prunes older tool chatter from active request context without compacting the whole session
- `session.retention`, `session.max_entries`, `session.idle_reset_after`, and `session.daily_reset_time` control session cleanup and auto-reset
- `[cron]`, `[heartbeat]`, `[agent]` for scheduler and agent runtime settings
- `[tools]` chat-tool profiles, allow/deny lists, and exec approval settings
- `[workspace]` agent sandbox storage (`root`, `per_agent`, `max_file_bytes`)

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

Generate and edit `koios.config.toml`, then run:

```sh
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

The daemon reads `koios.config.toml` from the current working directory.

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

## CLI Commands

Koios now ships with a Cobra-based operator CLI while preserving the existing
daemon behavior: running `koios` with no subcommand still starts the daemon.

Common commands:

```sh
koios --help
koios version
koios status
koios health --verbose
koios agent --peer alice --message "Summarize the last session"
koios agent --peer alice
koios calendar add --peer alice --name Work --path ./work.ics
koios calendar agenda --peer alice --scope today
koios init
koios doctor --deep
koios sessions
koios backup create
koios update status
```

`koios doctor` now checks local config integrity, expected workspace state
directories, sandbox prerequisites such as `bwrap`, enabled MCP server wiring,
and, with `--deep`, the live gateway health and monitor endpoints. Use
`koios doctor --repair` to generate a starter config and create the local state
directory tree when those artifacts are missing.

The `agent` command supports both one-shot calls and an interactive terminal
chat interface. The interactive mode is built with Bubble Tea, which is a
widely used Go TUI framework designed for stateful terminal applications.

Cron management is exposed through the daemon WebSocket API and requires a peer:

```sh
koios cron status --peer alice
koios cron list --peer alice
koios cron add --peer alice --name daily-briefing --cron "0 9 * * *" --tz "Asia/Manila" --message "Summarize today"
koios cron run <job-id> --peer alice
koios cron runs --peer alice --id <job-id>
```

Session inspection reads persisted JSONL sessions from `session.dir`:

```sh
koios sessions
koios sessions --peer alice
koios sessions reset --peer alice
```

---

## Versioning

The canonical version lives in the [`VERSION`](VERSION) file at the repository root. Release notes live in [`CHANGELOG.md`](CHANGELOG.md), and each release entry should match the version in `VERSION`.

### Bump the version

```sh
# patch: 0.1.0 → 0.1.1
scripts/bump-version.sh patch

# minor: 0.1.0 → 0.2.0
scripts/bump-version.sh minor

# major: 0.1.0 → 1.0.0
scripts/bump-version.sh major
```

The version is embedded in the binary at build time so that `GET /` reports version, git hash, and build time at runtime. `scripts/release.sh` also verifies that `CHANGELOG.md` already contains a matching `## [X.Y.Z]` section before it produces a release binary:

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

Each peer also gets a workspace sandbox under `[workspace].root` (when `per_agent=true`), where the agent can create, read, update, and delete files through workspace tools.

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

For side-effecting RPCs, clients may include `"idempotency_key": "<opaque-client-key>"` inside `params`. Koios scopes deduplication by `peer_id + method + idempotency_key`, replays the original terminal response for exact retries, and rejects reuse of the same key with different params. Replayed requests return the stored final response; they do not re-emit prior stream notifications.

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
      "subagents": false,
      "workspace": true,
      "exec": true,
      "web": true
    },
    "methods": ["ping", "server.capabilities", "chat", "session.history", "session.reset", "standing.get", "standing.set", "standing.clear", "agent.run", "agent.start", "agent.get", "agent.wait", "agent.cancel", "memory.search", "memory.insert", "memory.get", "memory.list", "memory.delete", "memory.entity.create", "memory.entity.update", "memory.entity.get", "memory.entity.list", "memory.entity.search", "memory.entity.link_chunk", "memory.entity.relate", "memory.entity.touch", "memory.entity.delete", "memory.entity.unlink_chunk", "memory.entity.unrelate", "workspace.list", "workspace.read", "workspace.head", "workspace.tail", "workspace.grep", "workspace.sort", "workspace.uniq", "workspace.diff", "workspace.write", "workspace.edit", "workspace.mkdir", "workspace.delete", "exec", "exec.pending", "exec.approve", "exec.reject", "web_search", "web_fetch", "cron.list", "cron.create", "cron.get", "cron.update", "cron.delete", "cron.trigger", "cron.runs", "heartbeat.get", "heartbeat.set", "heartbeat.wake", "subagent.list", "subagent.spawn", "subagent.get", "subagent.status", "subagent.kill", "subagent.steer", "subagent.transcript"],
    "chat_tools": ["time.now", "subagent.status", "session.history", "session.list", "session.spawn", "session.send", "session.patch", "memory.search", "memory.insert", "memory.get", "memory.entity.create", "memory.entity.update", "memory.entity.get", "memory.entity.list", "memory.entity.search", "memory.entity.link_chunk", "memory.entity.relate", "memory.entity.touch", "memory.entity.delete", "memory.entity.unlink_chunk", "memory.entity.unrelate", "workspace.list", "workspace.read", "workspace.head", "workspace.tail", "workspace.grep", "workspace.sort", "workspace.uniq", "workspace.diff", "workspace.write", "workspace.edit", "workspace.mkdir", "workspace.delete", "read", "head", "tail", "grep", "sort", "uniq", "diff", "write", "edit", "apply_patch", "exec", "web_search", "web_fetch", "cron.list", "cron.create", "cron.get", "cron.update", "cron.delete", "cron.trigger", "cron.runs", "session.reset"],
    "idempotency": {
      "params_field": "idempotency_key",
      "methods": ["chat", "session.reset", "presence.set", "standing.set", "standing.clear", "agent.run", "agent.start", "agent.cancel", "agent.steer", "subagent.spawn", "subagent.kill", "subagent.steer", "memory.insert", "memory.delete", "memory.entity.create", "memory.entity.update", "memory.entity.link_chunk", "memory.entity.relate", "memory.entity.touch", "memory.entity.delete", "memory.entity.unlink_chunk", "memory.entity.unrelate", "workspace.write", "workspace.edit", "workspace.mkdir", "workspace.delete", "exec", "exec.approve", "exec.reject", "cron.create", "cron.update", "cron.delete", "cron.trigger", "heartbeat.set", "heartbeat.wake", "server.set_log_level"]
    },
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

### Agent Session Tools

These are server-side chat tools exposed to the model rather than JSON-RPC
methods:

- `session.list`: enumerate known sessions for this peer, including persisted
  `reply_back` policy and spawned sub-sessions.
- `session.spawn`: create a sub-session and optionally wait for completion.
  Supports `announce_skip` and `reply_skip`, and also understands
  `ANNOUNCE_SKIP` / `REPLY_SKIP` tokens embedded in the task text.
- `subagent.status`: poll a spawned subagent run by id.
- `session.send`: send a message into another same-peer session by
  `session_key` or `run_id`; active sub-sessions are steered in place, while
  other sessions execute a turn and can mirror replies back to the source
  session.
- `session.patch`: update persisted session policy. Today this supports
  `reply_back`.

Example tool arguments:

```json
{"name":"session.list","arguments":{}}
```

```json
{"name":"session.spawn","arguments":{"task":"Review the failures","wait":true,"reply_back":true,"announce_skip":false,"reply_skip":false}}
```

```json
{"name":"subagent.status","arguments":{"id":"<run-id>"}}
```

```json
{"name":"session.send","arguments":{"session_key":"alice::sender::bob","message":"Summarize the thread","reply_back":true,"wait_timeout_seconds":30}}
```

```json
{"name":"session.patch","arguments":{"session_key":"alice::sender::bob","reply_back":false}}
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

Spawn an asynchronous child agent run. Returns immediately with the run record; poll `subagent.status` for status. `subagent.get` remains available as a compatibility alias.
Per-parent-session concurrency is enforced with a semaphore, so excess child
runs stay queued until a slot is available rather than failing immediately.

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
| `announce_skip` | `bool` | Suppress the initial queued announcement to the source session |
| `reply_skip` | `bool` | Suppress the final mirrored child reply even when `reply_back` is enabled |

```json
{"id": "6", "method": "subagent.spawn", "params": {
  "task": "Research and summarise topic X",
  "announce_skip": false,
  "reply_skip": false
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

### `subagent.status`

Poll the current state of a run by id. This is equivalent to `subagent.get`.
The returned run record now includes a structured `subturn` object with
parent/child coordination metadata, concurrency reservation details, step
counts, tool call counts, and the latest lifecycle event observed for the run.

```json
{"id": "8b", "method": "subagent.status", "params": {"id": "<run-id>"}}
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

### `memory.insert` / `memory.get`

Store a memory chunk for the current peer, or fetch one by id. Memories can optionally declare retention and expiry metadata. `retention_class` accepts `working`, `pinned`, or `archive`. Archived memories remain searchable but are excluded from automatic prompt injection. `exposure_policy` accepts `auto` or `search_only`.

```json
{"id": "12a", "method": "memory.insert", "params": {"content": "Deployment windows are narrow on Fridays.", "retention_class": "pinned", "exposure_policy": "auto", "expires_at": 0}}
```

```json
{"id": "12b", "method": "memory.get", "params": {"id": "<chunk-id>"}}
```

### `memory.entity.*`

Create stable entities for people, projects, places, and ongoing topics, then attach chunks and relationship edges so memory can pivot around durable objects instead of only raw text. Archived turn summaries now auto-extract durable entities and link the stored summary chunk to each extracted entity.

```json
{"id": "12c", "method": "memory.entity.create", "params": {"kind": "project", "name": "Borealis Trip", "aliases": ["summer trip"], "notes": "Annual planning thread"}}
```

```json
{"id": "12d", "method": "memory.entity.link_chunk", "params": {"id": "<entity-id>", "chunk_id": "<chunk-id>"}}
```

```json
{"id": "12e", "method": "memory.entity.relate", "params": {"source_id": "<project-id>", "target_id": "<person-id>", "relation": "owned_by", "notes": "Alice coordinates logistics"}}
```

```json
{"id": "12f", "method": "memory.entity.get", "params": {"id": "<entity-id>"}}
```

```json
{"id": "12g", "method": "memory.entity.unlink_chunk", "params": {"id": "<entity-id>", "chunk_id": "<chunk-id>"}}
```

```json
{"id": "12h", "method": "memory.entity.unrelate", "params": {"source_id": "<project-id>", "target_id": "<person-id>", "relation": "owned_by"}}
```

```json
{"id": "12i", "method": "memory.entity.delete", "params": {"id": "<entity-id>"}}
```

---

### `calendar.source.create` / `calendar.source.list` / `calendar.source.delete`

Register or manage agenda sources backed by local `.ics` files or remote ICS URLs.

```json
{"id": "12j", "method": "calendar.source.create", "params": {
  "name": "Work",
  "path": "/absolute/path/to/work.ics",
  "timezone": "America/New_York",
  "enabled": true
}}
```

```json
{"id": "12k", "method": "calendar.source.create", "params": {
  "name": "Family",
  "url": "https://example.com/family.ics",
  "enabled": true
}}
```

```json
{"id": "12l", "method": "calendar.source.list", "params": {"enabled_only": true}}
```

---

### `calendar.agenda`

Query agenda windows from registered ICS sources. Supported scopes are `today`, `this_week`, and `next_conflict`.

```json
{"id": "12m", "method": "calendar.agenda", "params": {
  "scope": "today",
  "timezone": "America/New_York",
  "limit": 20
}}
```

```json
{"id": "12n", "method": "calendar.agenda", "params": {
  "scope": "next_conflict",
  "timezone": "UTC"
}}
```

---

### `cron.list` / `cron.create` / `cron.get` / `cron.update` / `cron.delete`

Manage scheduled jobs. Requires `cron.dir` to be set.

**Schedule kinds:** `at` (one-shot ISO 8601), `every` (fixed interval in ms), `cron` (5-field cron expression + optional IANA timezone).

**Payload kinds:** `systemEvent` (injects a system message into the session), `agentTurn` (executes an isolated LLM call and appends the result).

`agentTurn` payloads may include `preload_urls` to lazily fetch fresh external text before the scheduled turn. Jobs may also declare `dispatch.defer_if_active=true` to defer while the peer is active and `dispatch.require_approval=true` to gate scheduled runs through the cron approval hook.

```json
{"id": "13", "method": "cron.create", "params": {
  "name": "daily-briefing",
  "schedule": {"kind": "cron", "expr": "0 9 * * *", "tz": "America/New_York"},
  "payload": {
    "kind": "agentTurn",
    "message": "What is on my agenda today?",
    "preload_urls": ["https://example.com/agenda.txt"]
  },
  "dispatch": {"defer_if_active": true, "require_approval": false},
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

### Webhook Events

`POST /v1/webhooks/events` accepts authenticated external events when `KOIOS_WEBHOOK_TOKEN` is configured.

Supported webhook `type` values:
- `message.append`
- `presence.set`
- `cron.trigger`
- `cron.schedule`

Example one-shot webhook scheduling:

```json
{
  "type": "cron.schedule",
  "peer_id": "alice",
  "name": "one-shot-reminder",
  "schedule": {"kind": "at", "at": "2026-04-14T09:30:00Z"},
  "payload": {"kind": "systemEvent", "text": "Reminder from webhook"}
}
```

Hook support includes `before_message`, `after_message`, `message_received`, `before_llm`, `after_llm`, `before_tool_call`, `after_tool_call`, compaction hooks, and `cron_approval`. If `KOIOS_HOOK_INTERCEPTOR_URL` is set, `before_message` and `before_llm` may rewrite in-flight requests by returning a modified event body.

---

### `standing.get` / `standing.set` / `standing.clear` / `standing.profile.*`

Read or update standing orders for this peer.

Standing orders are modeled after OpenClaw's persistent operating instructions:
- If `<workspace>/STANDING_ORDERS.md` exists, it is injected as a system message on each `chat`, `agent.run`, heartbeat run, and cron `agentTurn`.
- Peer-specific standing orders are layered on top of the workspace file.
- Named standing profiles can layer additional instructions, tool policy overrides, and response-style toggles on top of the base standing orders.
- The active profile is stored in session policy, can be switched manually, and can also be overridden by cron jobs or workflow agent-turn steps.
- Peer-specific standing orders persist only when `cron.dir` is set; without it, `standing.get` is read-only and still returns any workspace-level standing orders.

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

```json
{"id": "15d", "method": "standing.profile.set", "params": {
  "name": "focus",
  "content": "Optimize for deep work. Be terse and defer non-urgent tasks.",
  "tool_profile": "minimal",
  "response_style": "Keep responses short and task-oriented.",
  "make_default": true
}}
```

```json
{"id": "15e", "method": "standing.profile.activate", "params": {
  "name": "focus"
}}
```

`standing.get` returns the peer document plus profile metadata such as `default_profile`, `active_profile`, `resolved_profile`, and the defined `profiles` map.

In chat, `/profile` and `/mode` provide a shortcut to list, activate, and clear the current session profile.

---

### `heartbeat.get` / `heartbeat.set`

Read or update the heartbeat configuration for this peer. Requires `cron.dir` and `heartbeat.enabled=true`.

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

### `workspace.list` / `workspace.read` / `workspace.head` / `workspace.tail` / `workspace.grep` / `workspace.sort` / `workspace.uniq` / `workspace.diff` / `workspace.write` / `workspace.edit` / `workspace.mkdir` / `workspace.delete`

Direct workspace RPC methods for peer sandbox operations. These target the same
workspace sandbox used by agent tools.

```json
{"id":"18","method":"workspace.write","params":{"path":"project/readme.md","content":"# Hello","append":false}}
```

```json
{"id":"19","method":"workspace.read","params":{"path":"project/readme.md"}}
```

```json
{"id":"20","method":"workspace.grep","params":{"path":"project","pattern":"Hello","recursive":true,"limit":50,"case_sensitive":true,"regexp":false}}
```

```json
{"id":"21","method":"workspace.head","params":{"path":"project/log.txt","lines":20}}
```

```json
{"id":"22","method":"workspace.tail","params":{"path":"project/log.txt","lines":20}}
```

```json
{"id":"23","method":"workspace.sort","params":{"path":"project/list.txt","reverse":false,"case_sensitive":true}}
```

```json
{"id":"24","method":"workspace.uniq","params":{"path":"project/list.txt","count":true,"case_sensitive":true}}
```

```json
{"id":"25","method":"workspace.diff","params":{"path":"project/readme.md","content":"# Hi\n","context":3}}
```

```json
{"id":"26","method":"workspace.list","params":{"path":"project","recursive":true,"limit":200}}
```

```json
{"id":"27","method":"workspace.edit","params":{"path":"project/readme.md","old_text":"Hello","new_text":"Hi","replace_all":false}}
```

### `exec` / `exec.pending` / `exec.approve` / `exec.reject`

Run shell commands on the host with the peer workspace as the default working directory. Commands that match the configured dangerous-command policy can return `status: "approval_required"` instead of executing; operators can inspect or resolve those pending requests through the companion exec approval RPC methods.

```json
{"id":"28","method":"exec","params":{"command":"go test ./...","workdir":".","timeout_seconds":30}}
```

```json
{"id":"29","method":"exec.pending"}
```

```json
{"id":"30","method":"exec.approve","params":{"id":"<approval-id>"}}
```

### `web_search` / `web_fetch`

Search the public web or fetch page content.

```json
{"id":"31","method":"web_search","params":{"query":"golang context tutorial","limit":5}}
```

```json
{"id":"32","method":"web_fetch","params":{"url":"https://example.com"}}
```

---

### Workspace Tools (Agent)

When workspace is enabled, the agent can call:
- `session.list`
- `session.spawn`
- `session.send`
- `session.patch`
- `workspace.list`
- `workspace.read`
- `workspace.head`
- `workspace.tail`
- `workspace.grep`
- `workspace.sort`
- `workspace.uniq`
- `workspace.diff`
- `workspace.write`
- `workspace.edit`
- `apply_patch`
- `workspace.mkdir`
- `workspace.delete`
- `read`
- `head`
- `tail`
- `grep`
- `sort`
- `uniq`
- `diff`
- `write`
- `edit`
- `exec`
- `web_search`
- `web_fetch`
- `memory.insert`
- `memory.get`

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
[llm]
provider = "openai"
base_url = "http://localhost:11434/v1"
api_key = "ollama" # any non-empty value
model = "llama3.2"
```

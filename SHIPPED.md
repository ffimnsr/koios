# What's Shipped

This page describes the features that are already built into Koios, in plain language. If you want to know what Koios can do today, this is the list. Work that's still on the roadmap lives in [ISSUES.md](ISSUES.md).

## Chat & Conversation

Koios comes with a full set of in-chat controls so you can shape how your assistant behaves:

- **Thinking levels** (`/think`): choose how much "thinking" the model does before answering, from off to extra-high.
- **Verbose and trace modes** (`/verbose`, `/trace`): turn on more detailed progress reporting, or full debug tracing when you need to see every step.
- **Usage footer**: each reply can show token counts so you can keep an eye on cost.
- **Status** (`/status`): see which model is active and current token usage.
- **Fresh starts** (`/new`, `/reset`): start a new conversation or reset the current one at any time.
- **Compaction** (`/compact`): shrink a long conversation's context on demand; Koios flushes memory first so nothing important is lost, and uses provider-side compaction where the model provider supports it (for example OpenAI and Anthropic).
- **Restart** (`/restart`): owners can restart the gateway right from chat.
- **Activation control** (`/activation mention|always`): decide whether the assistant answers every message in a group or only when it's mentioned, with the same policy available per session.
- **Steering mid-reply**: redirect the assistant while it's still writing, using queue modes such as `steer`, `followup`, and `collect`.
- **Block streaming**: replies stream in configurable chunks that are coalesced per channel.
- **Smart retries**: configurable retry counts and status-code filters keep flaky requests from failing silently.
- **Idle protection**: stalled responses time out instead of hanging forever.
- **Silent replies**: the assistant can suppress replies entirely when there's nothing worth saying (for example after internal bookkeeping).
- **Tool summaries**: verbose mode shows inline summaries of what tools did.
- **Run ledger**: background tasks are tracked in one place with status and history.
- **Context upkeep**: the conversation engine prunes idle context in the background to stay within budget.
- **Per-session send policy**: reply-back behavior is configurable and persisted per session.

## Models & Providers

- **Provider support**: OpenAI, Anthropic, OpenRouter, NVIDIA NIM, Google Gemini, plus local and self-hosted options Ollama, vLLM, and LiteLLM proxies.
- **Automatic failover**: if one model fails, Koios falls back to the next in line — preferring fallbacks that match the request's streaming and tool needs.
- **Smart routing**: simple questions can automatically go to a lightweight model to save cost.
- **Per-session model override**: switch models for one conversation without touching global config.
- **Live model catalogs**: Koios fetches the model list directly from your provider instead of relying on stale config.
- **Usage and quota checks**: check current usage and quota through chat or the API.
- **Provider compatibility handling**: quirks between providers (tool formats, endpoints, streaming behavior) are normalized automatically, and model switching clears stale state so the next turn starts fresh.
- **Multiple API keys per profile**: one provider profile can hold several keys, spread across users, with encrypted storage and no plaintext keys in logs. Peer-scoped bring-your-own-key profiles let each user connect their own account, resolved per request.
- **Session status**: see the resolved model, provider profile, and why a particular route was chosen.

## Memory & Knowledge

- **Short-term memory**: recent context is carried across turns with a sliding window.
- **Memory controls**: pin important facts so they're always retrievable, archive old ones, and set expiration for disposable details; memory stats are visible in chat and CLI.
- **Memory isolation**: sessions can have their own private memory or share a global memory space.
- **Auto-injection**: memory is automatically pulled from multiple named sources into relevant conversations.
- **Curation queue**: proposed memories go to a review inbox — confirm, edit, or reject them before they start shaping future replies.
- **Entity graph**: Koios tracks people, projects, places, and ongoing topics as real entities with aliases and notes, so it understands "who is this person" and "what's blocked on this project."
- **Preferences and decisions registry**: stable preferences and decisions ("we chose X because Y") live in a structured store with provenance and confidence, separate from fuzzy memory search.
- **Provenance**: every memory records where it came from, so you can audit and debug.
- **Bookmarks**: save important messages, snippets, or plans for later recall.
- **Task extraction**: action items are pulled out of conversations into a review inbox, then promoted to tracked tasks with due dates and owners.
- **Waiting-on tracker**: separate from your own tasks, Koios tracks things you're waiting on from others, with follow-up dates.
- **Calendar and agenda**: ingest local `.ics` files and remote calendar feeds for agenda-aware planning.
- **Daily brief and weekly review**: compose upcoming events, stale waiting-ons, new memory candidates, recent commitments, and active projects into a single digest.
- **Personas and standing orders**: named profiles layer different instructions, tool allowances, and response styles for different contexts (work, home, deep focus).
- **Commitments dashboard**: a shell/TUI view of open tasks, waiting-ons, upcoming events, and promises.

## Skills & Extensions

- **Skills**: Markdown-based `SKILL.md` skills with metadata load from workspace, project, personal, managed, and bundled locations, with deterministic precedence when the same skill exists in several places.
- **Per-agent allowlists**: a persona can opt into only the skills it should use.
- **Load-time gating**: skills can require a certain OS, environment, binaries, or config before loading.
- **Auto-refresh**: the skills catalog watches for changes and refreshes automatically (or on demand via `/skills refresh`).
- **Overrides**: per-skill config overrides for enablement, naming, targeting, and commands.
- **Safe installs**: managed installs scan for dangerous shell patterns and require approval before a skill is copied in.
- **Skill commands**: skills can ship user-invocable slash commands with templates.
- **Bundled and managed skills**: Koios ships bundled skills, supports a version-gated managed tier, and scans your user-local `~/.koios/workspace/skills/` folder.
- **Prompt documents**: `SOUL.md`, `TOOLS.md`, `BOOTSTRAP.md`, `IDENTITY.md`, and `USER.md` are injected as first-class context.
- **Plugins**: extensions declared with `koios-extension.toml` can add tools, hooks, CLI commands, HTTP routes, and even whole channels, with allow/deny policy controls.

## Tools the Assistant Can Use

- **Files**: read, write, edit, multi-hunk patches (`apply_patch`), line-range reads, and filename search.
- **Safe code execution**: bounded analysis jobs run in an isolated sandbox — no network by default, strict resource limits, workspace confined.
- **Background processes**: manage long-running jobs with status and logs.
- **Exec approvals**: shell commands can require approval, and allowlists are managed via CLI.
- **Browser automation**: a full browser stack — managed profiles, attaching to your already-running Chrome, remote browsers, stable snapshot references for clicking/typing/filling, accessibility snapshots, screenshots, PDF export, cookie and storage inspection, offline/geo/device emulation, and protection against SSRF-style redirect tricks.
- **Screen tools**: capture screenshots and short animated GIF recordings of the active page or element.
- **Canvas**: an in-page visual workspace with push/reset/eval/snapshot tools.
- **Vision**: images and files are automatically encoded and sent to multimodal models.
- **Notifications**: local system notifications via `system.notify`; `system.run` honors permission checks (TCC-aware on macOS).
- **Session tools**: `session.list`, `session.send`, `session.history`, `session.spawn`, and `session.patch` let the agent manage and message sessions, with reply-back support.
- **MCP integrations**: a built-in MCP (Model Context Protocol) client connects to external MCP servers over stdio or HTTP; users can register, test, enable, and disable their own servers without editing config.
- **Personal productivity tools**: tasks, reminders, projects, notes, scratchpads, plans, artifacts, decisions, preferences, briefs, inbox, contacts, git operations, cross-channel messages, approvals, run introspection, usage estimates, and model listing/routing are all available as native tools the assistant can call.
- **Diff viewer**: render and inspect diffs for structured repository work.

## Subagents & Multi-Agent

- **Subagents**: spawn subagents with `subagent.spawn`, poll their state with `subagent.status`, with per-session concurrency limits and announcement controls.
- **SubTurn**: structured subagent coordination with lifecycle events and concurrency control.
- **EventBus**: decoupled messaging between agents.
- **Long-running tasks**: spawn work asynchronously, then poll or await it as tool calls.
- **Fan-out orchestration**: fan a task out across multiple child agents and aggregate their replies, with:
  - barrier and fan-in joins, multi-stage map/reduce pipelines, and partial results streamed in as children finish;
  - structured JSON output contracts that get validated and merged;
  - retry, hedging (parallel prompt variants), and first-success-wins policies;
  - quorum and voting modes for agreement-based decisions;
  - supervisor, verifier, and arbiter roles;
  - dependency-graph execution for DAG-style workflows;
  - budget and deadline controls, plus rich timelines showing per-child durations and tool calls.

## Scheduling & Automation

- **Idle-aware cron**: scheduled runs skip or defer when you're actively chatting.
- **Lazy content loading**: external content is fetched right before a scheduled run.
- **Webhook triggers**: one-shot runs and cron overrides from webhooks.
- **Hooks**: observer hooks around messages and tool calls, interceptor hooks that modify in-flight turns, approval hooks for human-in-the-loop gating, named hooks with payload transforms, wake webhooks for main-session events, and isolated-run webhooks.
- **Workflow engine**: multi-step workflows for automation.

## Channels & Messaging

- **Internal messaging**: sessions and subagents can message each other, with reply-back/ping-pong.
- **Telegram**: a full Telegram channel with bot token, long polling/webhooks, DM policies, and inbox routing.
- **Inbox routing**: channels can route into isolated agents/sessions.
- **Mention gating**: choose whether the assistant replies to everything or only when mentioned.
- **Group rules**: owner-only commands and reply tags per group.
- **Message chunking**: long messages are split according to each channel's limits.
- **Cross-channel messaging**: a shared message tool sends to any channel.
- **Pairing**: unknown senders get a pairing code, and owners approve them via `pairing approve`; per-channel policies (`pairing`, `open`, `closed`) and sender allowlists keep strangers out.

## Security & Safety

- **Sandbox policies**: per-session-type allow/deny lists for tools, with least-privilege defaults.
- **Elevated bash toggle**: `/elevated on|off` per session.
- **Single-instance guard**: a gateway lock file prevents duplicate daemons.
- **Log scrubbing**: credentials and tokens are filtered out of logs.
- **Conversation binding approvals**: conversations must be approved before they're trusted.
- **Plugin safety gates**: path-ownership and world-writable checks for plugins.
- **SSRF protection**: the browser tool blocks redirect-based SSRF bypasses.
- **Strict config validation**: invalid config refuses to boot instead of failing later.

## Operations & Reliability

- **Diagnostics**: a `doctor` command with diagnostics and repair guidance.
- **Migration**: a `migrate` command upgrades older config and state formats.
- **Model switching**: a `model` command to view and switch the default model.
- **Hot reload**: config changes apply without a full restart.
- **Health monitor**: stale-threshold detection with a max-restart policy.
- **Runtime logging**: log level adjustable at runtime.
- **Usage tracking**: per-session token tracking and a usage API (`/v1/usage`).
- **Idempotency**: side-effecting requests are safe to retry.
- **Up-to-date MCP client**: the MCP integration follows the latest protocol — reading resources, using prompts, caching, subscriptions, and pausing to ask for input when a server needs it before continuing.

---

Interested in what's planned next? See [ISSUES.md](ISSUES.md).

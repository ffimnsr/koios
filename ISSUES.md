# Koios Missing Features vs OpenClaw and PicoClaw

This file is a merged checklist for the feature gap between Koios and the reference systems. Finished items have been moved to [shipped.md](shipped.md); items marked `[-]` are explicitly out of scope.

## Tools

- [-] Node-routed exec by default or per-session
	- Research notes: This appears more Koios-specific than a direct parity item. OpenClaw has host/sandbox/browser target routing, but the current search did not surface the same exec-to-node defaulting model. PicoClaw and IronClaw also did not show an obvious equivalent in the current repo searches.
	- References: OpenClaw `docs/tools/browser.md`; PicoClaw no obvious equivalent found in current repo search; IronClaw no obvious equivalent found in current repo search.
- [-] Camera snapshot / clip tool (NOT Implemented)
	- Research notes: PicoClaw's broader device/channel ecosystem and roadmap touches camera-oriented hardware more directly than the other comparison repos, but the current search did not surface a clean agent-callable camera tool. OpenClaw and IronClaw also did not show an obvious equivalent in the searched trees.
	- References: OpenClaw no obvious equivalent found in current repo search; PicoClaw `ROADMAP.md`, `docs/pt-br/chat-apps.md`; IronClaw no obvious equivalent found in current repo search.
- [-] Location tool (`location.get` via node) (NOT IMPLEMENTED)
	- Research notes: This appears more Koios-specific than something strongly represented upstream. None of the three repos surfaced a direct first-class location tool in the current searches.
	- References: OpenClaw no obvious equivalent found in current repo search; PicoClaw no obvious equivalent found in current repo search; IronClaw no obvious equivalent found in current repo search.

## Media and Voice

- [ ] Image analysis tool
	- Research notes: OpenClaw is the clearest direct reference because its provider/plugin runtime already advertises image-analysis capability separately from plain multimodal chat, so Koios can model this as a first-class tool instead of an ad hoc provider flag. PicoClaw has workable multimodal/image-loading patterns, but not as a distinct built-in image-analysis tool. IronClaw already has a dedicated built-in image-analysis tool, which is the strongest implementation reference after OpenClaw.
	- References: OpenClaw `docs/tools/index.md`, `src/plugins/runtime/index.ts`, `src/plugins/types.ts`; PicoClaw `pkg/tools/load_image.go`, `docs/configuration.md`; IronClaw `src/tools/builtin/image_analyze.rs`, `src/tools/registry.rs`.
- [ ] Image generation tool
	- Research notes: OpenClaw has the richest visible upstream surface here because image generation is treated as a provider capability with concrete provider registrations like Fal and MiniMax. PicoClaw does not appear to expose a first-class built-in image generation tool in the current tree. IronClaw already has a dedicated `image_gen` builtin, so it is the best typed runtime reference.
	- References: OpenClaw `docs/tools/index.md`, `extensions/fal/index.ts`, `extensions/minimax/provider-registration.ts`; PicoClaw no obvious equivalent found in current repo search; IronClaw `src/tools/builtin/image_gen.rs`, `src/tools/registry.rs`.
- [ ] Image editing workflow or tool
	- Research notes: OpenClaw's provider runtime is the strongest reference because it already distinguishes media capabilities at the plugin layer, which is the clean place to add image-edit support. PicoClaw did not surface a dedicated edit-image workflow in the current search. IronClaw has the stronger extension/tool capability architecture if Koios wants image editing to land as an extension-owned tool instead of a hardcoded builtin.
	- References: OpenClaw `src/plugins/types.ts`, `docs/plugins/sdk-overview.md`; PicoClaw no obvious equivalent found in current repo search; IronClaw `src/tools/wasm/capabilities_schema.rs`, `src/extensions/mod.rs`.
- [ ] Music generation tool
	- Research notes: OpenClaw is the primary parity reference because its tool and provider docs already call out `music_generate` as a first-class capability. PicoClaw and IronClaw did not surface equally direct built-in music-generation tool implementations in the current searches.
	- References: OpenClaw `docs/tools/index.md`, `src/plugins/types.ts`, `extensions/minimax/provider-registration.ts`; PicoClaw no obvious equivalent found in current repo search; IronClaw no obvious equivalent found in current repo search.
- [ ] Video generation tool
	- Research notes: OpenClaw again has the strongest current shape because `video_generate` is already modeled at the provider-capability layer. PicoClaw and IronClaw did not show comparable built-in video-generation tooling in the current searches.
	- References: OpenClaw `docs/tools/index.md`, `src/plugins/types.ts`, `extensions/vydra/index.ts`; PicoClaw no obvious equivalent found in current repo search; IronClaw no obvious equivalent found in current repo search.
- [ ] Text-to-speech tool
	- Research notes: OpenClaw is the clearest upstream reference because `tts` is already treated as a first-class capability alongside image and video generation. PicoClaw has voice-oriented config and launcher surfaces, but not an equally explicit built-in TTS tool in the current search. IronClaw's current repo search surfaced transcription and extension auth patterns more clearly than a dedicated TTS builtin.
	- References: OpenClaw `docs/tools/index.md`, `src/plugins/types.ts`; PicoClaw `docs/providers.md`, `web/README.md`; IronClaw `src/tools/wasm/capabilities_schema.rs`, `src/config/mod.rs`.
- [ ] Audio transcription tool
	- Research notes: OpenClaw's provider runtime already models speech/media capabilities explicitly, which is the cleanest parity reference. PicoClaw has the strongest visible secondary reference through its voice configuration and provider notes about transcription-capable backends. IronClaw also has a transcription subsystem in config/runtime, which is a good architectural reference if Koios wants transcription to be service-backed instead of embedded in each provider.
	- References: OpenClaw `src/plugins/types.ts`, `docs/tools/index.md`; PicoClaw `docs/providers.md`, `docs/configuration.md`; IronClaw `src/config/mod.rs`, `src/testing/mod.rs`.
- [ ] Video understanding and description capability
	- Research notes: OpenClaw is the best direct reference because its provider/plugin system already separates multimodal capability families and can own video-understanding adapters. PicoClaw's current tree surfaces image loading and voice config more clearly than video understanding. IronClaw's built-in image analysis plus extension capability model are the closest visible comparison points.
	- References: OpenClaw `src/plugins/types.ts`, `docs/plugins/architecture.md`; PicoClaw no obvious equivalent found in current repo search; IronClaw `src/tools/builtin/image_analyze.rs`, `src/tools/wasm/capabilities_schema.rs`.
- [ ] End-to-end media pipeline for image, audio, and video inputs
	- Research notes: OpenClaw is the strongest overall blueprint because it already has a unified capability registry spanning image analysis, generation, speech, and other media tools instead of scattering them across unrelated subsystems. PicoClaw has enough provider and launcher plumbing to inform config and UI wiring, but not the same unified media pipeline in the current search. IronClaw has the best typed runtime model for combining builtins, extensions, auth, and web APIs once Koios defines the media abstraction.
	- References: OpenClaw `src/plugins/runtime/index.ts`, `src/plugins/types.ts`, `docs/tools/index.md`; PicoClaw `web/README.md`, `docs/providers.md`; IronClaw `src/config/mod.rs`, `src/tools/registry.rs`, `src/extensions/mod.rs`.
- [ ] Voice wake support
	- Research notes: None of the three repos surfaced a direct always-listening wake-word subsystem in the current searches. OpenClaw's media capability system is still the best place to borrow overall structure from, while PicoClaw and IronClaw are more useful as references for service wiring and device/runtime integration if Koios pursues this.
	- References: OpenClaw `src/plugins/runtime/index.ts`, `docs/tools/index.md`; PicoClaw `docs/configuration.md`; IronClaw `src/config/mod.rs`.
- [ ] Talk mode and continuous voice workflows
	- Research notes: This is only weakly represented upstream today. OpenClaw has the strongest overall speech/media capability surface, PicoClaw has voice-oriented config examples, and IronClaw has a cleaner runtime split for long-lived services, but none surfaced a fully polished continuous talk mode in the current searches.
	- References: OpenClaw `src/plugins/types.ts`, `docs/tools/index.md`; PicoClaw `docs/providers.md`; IronClaw `src/config/mod.rs`, `src/testing/mod.rs`.
- [ ] Realtime transcription provider abstraction
	- Research notes: OpenClaw's plugin-capability model is the cleanest upstream reference because it can describe realtime transcription as a provider-owned capability instead of baking it into core runtime. PicoClaw has voice/provider configuration patterns that help with config shape. IronClaw's explicit transcription field in runtime dependencies is the strongest clue for how to wire a shared abstraction through the agent stack.
	- References: OpenClaw `src/plugins/types.ts`, `docs/plugins/sdk-overview.md`; PicoClaw `docs/providers.md`, `docs/configuration.md`; IronClaw `src/testing/mod.rs`, `src/config/mod.rs`.
- [ ] Realtime voice provider abstraction
	- Research notes: OpenClaw is again the cleanest architecture reference because voice can fit the same provider-capability registration pattern used for other media features. PicoClaw and IronClaw did not surface a direct realtime voice abstraction in the current searches, but both have enough provider/runtime structure to inform config and service ownership.
	- References: OpenClaw `src/plugins/runtime/index.ts`, `src/plugins/types.ts`; PicoClaw `docs/providers.md`; IronClaw `src/config/mod.rs`, `src/tools/wasm/capabilities_schema.rs`.
- [ ] Speech provider system beyond text-only replies
	- Research notes: OpenClaw is the primary parity target because its capability system already clearly goes beyond text-only chat. PicoClaw shows how voice features can be surfaced in config and launcher UX. IronClaw is the best secondary architectural reference if Koios wants speech providers to participate in the same extension/auth/registry model as other tools.
	- References: OpenClaw `docs/tools/index.md`, `src/plugins/types.ts`; PicoClaw `docs/providers.md`, `web/README.md`; IronClaw `src/config/mod.rs`, `src/tools/wasm/capabilities_schema.rs`.

## Skills & Extensions

- [-] Skill-local env and API key injection
	- Research notes: IronClaw is the strongest architectural reference here because its extension/tool auth model and secrets plumbing are already explicit and typed. OpenClaw also has a strong plugin/skill runtime with secrets and setup surfaces. PicoClaw is useful for practical installer/UI flows, but less explicit about per-skill secret injection than the other two.
	- References: OpenClaw `docs/tools/creating-skills.md`, `docs/plugins/sdk-overview.md`; PicoClaw `pkg/tools/skills_install.go`, `web/backend/api/skills.go`; IronClaw `src/tools/wasm/capabilities_schema.rs`, `src/bridge/auth_manager.rs`, `src/cli/tool.rs`.
- [-] Skills registry search (ClawHub API integration)
	- Research notes: PicoClaw is actually the clearest visible implementation reference because it already has registry search/install UX end to end. OpenClaw is also strong here through its managed-skill and hub-oriented flows. IronClaw has the right catalog architecture, but the current search surfaced less of a public registry UX than PicoClaw.
	- References: OpenClaw `ui/src/ui/views/skills.ts`, `src/cli/skills-cli.ts`; PicoClaw `pkg/skills/clawhub_registry.go`, `web/frontend/src/components/agent/hub/*`; IronClaw `crates/ironclaw_skills/src/catalog.rs`, `src/cli/skills.rs`.
- [-] Pluggable context engines (DON'T IMPLEMENT)
	- Research notes: This is most strongly represented by IronClaw's engine-v2 architecture and OpenClaw's provider/runtime layering rather than a single drop-in context-engine API. PicoClaw has multiple context/compaction engines internally, but the current search showed them more as implementation details than as a pluggable public API.
	- References: OpenClaw `src/plugins/runtime/index.ts`, `docs/plugins/architecture.md`; PicoClaw `pkg/seahorse/short_engine.go`, `pkg/agent/context_manager.go`; IronClaw `docs/internal/engine-v2-architecture.md`, `src/config/mod.rs`.
- [-] Pluggable compaction providers (DON'T IMPLEMENT)
	- Research notes: OpenClaw is the strongest direct reference because it already documents provider-specific compaction strategies. IronClaw is the best secondary reference for a typed engine architecture where compaction implementations can be swapped. PicoClaw currently owns compaction inside the agent stack rather than as a provider plugin.
	- References: OpenClaw `docs/concepts/compaction.md`, `src/config/types.agent-defaults.ts`; PicoClaw `pkg/seahorse/short_engine.go`; IronClaw `src/agent/compaction.rs`, `docs/internal/engine-v2-architecture.md`.

## Scheduling & Automation

- [-] Dedicated webhook auth token separate from gateway auth
	- Research notes: OpenClaw is the clearest reference because it already distinguishes gateway auth, channel auth, and webhook validation surfaces. PicoClaw has launcher/dashboard auth plus separate channel/webhook configuration patterns, which is useful for operator UX. IronClaw has explicit webhook secrets and separate gateway auth/token settings, making it the strongest secondary reference.
	- References: OpenClaw `docs/gateway/configuration-reference.md`, `src/config/zod-schema.ts`; PicoClaw `web/backend/api/auth.go`, `docs/configuration.md`; IronClaw `src/setup/channels.rs`, `src/config/mod.rs`, `docs/drafts/setup/configuration.mdx`.
- [-] Gmail Pub/Sub integration for inbox-driven automation
	- Research notes: this should cover the full Gmail watch lifecycle, not just Gmail API reads. The missing pieces are Pub/Sub topic and subscription provisioning, Gmail watch registration and renewal, callback verification, and routing inbound notifications into a Koios session or scheduled automation path.
	- OpenClaw reference: OpenClaw has a concrete end-to-end implementation here and is the strongest blueprint for Koios. Relevant entry points include `openclaw webhooks gmail setup` and `openclaw webhooks gmail run`, the runtime config in `src/hooks/gmail.ts`, the watcher lifecycle in `src/hooks/gmail-watcher.ts`, and the docs in `docs/automation/cron-jobs.md#gmail-pubsub-integration`.
		- https://github.com/openclaw/openclaw/blob/main/src/cli/webhooks-cli.ts
		- https://github.com/openclaw/openclaw/blob/main/src/hooks/gmail-ops.ts
		- https://github.com/openclaw/openclaw/blob/main/src/hooks/gmail.ts
		- https://github.com/openclaw/openclaw/blob/main/src/hooks/gmail-watcher.ts
		- https://github.com/openclaw/openclaw/blob/main/docs/automation/cron-jobs.md
	- PicoClaw reference: no equivalent Gmail Pub/Sub watcher showed up in the current PicoClaw tree during repo search. The closest reusable patterns are its shared webhook plumbing, automation hooks, and gateway-managed background services, which are useful as structural references but not as a direct Gmail implementation.
		- https://github.com/sipeed/picoclaw/blob/main/pkg/channels/webhook.go
		- https://github.com/sipeed/picoclaw/blob/main/pkg/gateway/gateway.go
		- https://github.com/sipeed/picoclaw/blob/main/pkg/agent/hooks.go
	- IronClaw reference: IronClaw clearly has Gmail access as an OAuth-backed Gmail tool/extension and also has generic webhook and routine infrastructure, but a direct Gmail Pub/Sub watcher path was not obvious from the repo search. That makes it a useful contrast reference for tool-level Gmail access versus inbox-trigger automation.
		- https://github.com/nearai/ironclaw/blob/main/tools-src/gmail/src/lib.rs
		- https://github.com/nearai/ironclaw/blob/main/tools-src/gmail/src/api.rs
		- https://github.com/nearai/ironclaw/blob/main/docs/extensions/google/gmail.md
		- https://github.com/nearai/ironclaw/blob/main/src/webhooks/mod.rs
	- Suggested Koios shape: add a `hooks.gmail` or `automation.gmail` config block in `internal/config/config.go`, a setup command for provisioning topic and subscription state, a long-running watcher service that renews Gmail watch registrations, and a webhook handler that validates the push token before dispatching a bounded automation run.

## Security & Sandboxing

- [ ] Per-session Docker sandbox (non-main sessions run inside Docker)
	- Research notes: OpenClaw is the clearest direct reference because it already distinguishes sandbox behavior by session/runtime type and validates Docker readiness through doctor flows. PicoClaw documents a security sandbox, but the current tree exposes it more as global config than as per-session policy. IronClaw is the strongest typed runtime reference because sandbox policy, resource limits, and orchestrator behavior are already first-class.
	- References: OpenClaw `src/commands/doctor-sandbox.ts`, `src/config/zod-schema.agent-runtime.ts`, `docs/gateway/sandboxing.md`; PicoClaw `docs/configuration.md`; IronClaw `src/sandbox/config.rs`, `src/sandbox/manager.rs`, `src/main.rs`.
- [ ] Reject known-weak credentials at startup
	- Research notes: OpenClaw and PicoClaw are the most practical references because both already run validation during setup/config load. IronClaw's wizard and doctor flows are the strongest typed reference if Koios wants startup validation plus remediation guidance.
	- References: OpenClaw `src/commands/doctor-sandbox.ts`, `src/config/zod-schema.ts`; PicoClaw `web/backend/api/auth.go`, `web/frontend/src/routes/launcher-setup.tsx`; IronClaw `src/setup/wizard.rs`, `src/cli/doctor.rs`, `src/setup/README.md`.
- [ ] More complete gateway auth modes
	- Research notes: OpenClaw is the strongest reference because its control UI already distinguishes `none`, `token`, `password`, and `trusted-proxy` modes. PicoClaw has launcher password and legacy token flows. IronClaw has bearer-token, OAuth, and web gateway auth surfaces that are the best secondary reference if Koios wants a richer matrix.
	- References: OpenClaw `src/config/zod-schema.ts`, `src/gateway/server.auth.shared.ts`; PicoClaw `web/backend/api/auth.go`, `docs/configuration.md`; IronClaw `docs/drafts/setup/configuration.mdx`, `src/channels/web/CLAUDE.md`, `src/config/mod.rs`.
- [ ] Trusted-proxy mode with explicit scope handling
	- Research notes: OpenClaw is the clearest direct reference because trusted-proxy auth is already part of its gateway config and device-auth plumbing. PicoClaw did not surface an equivalent header-scoped trusted-proxy mode in the current search. IronClaw's feature parity matrix explicitly calls out trusted-proxy auth, making it the strongest secondary reference.
	- References: OpenClaw `src/config/zod-schema.ts`, `src/gateway/server.auth.shared.ts`; PicoClaw no obvious equivalent found in current repo search; IronClaw `FEATURE_PARITY.md`, `src/channels/web/CLAUDE.md`.
- [ ] Tailscale identity-aware access modes
	- Research notes: OpenClaw is the clearest direct reference because its gateway auth and device-pair tooling already knows about Tailscale/tailnet contexts. PicoClaw did not surface an equivalent identity-aware Tailscale mode in the current search. IronClaw's parity matrix and tunnel setup are the strongest secondary references.
	- References: OpenClaw `extensions/device-pair/api.ts`, `src/config/zod-schema.ts`, `src/pairing/setup-code.ts`; PicoClaw no obvious equivalent found in current repo search; IronClaw `FEATURE_PARITY.md`, `src/setup/channels.rs`, `src/tunnel/mod.rs`.
- [ ] Secret references from env, file, and exec in config
	- Research notes: OpenClaw is the clearest direct reference because secret inputs are already treated as typed config primitives. PicoClaw is useful for its separation of app config and security config files. IronClaw has the strongest typed secrets subsystem and auth setup flows if Koios wants config references rather than raw inline values.
	- References: OpenClaw `src/config/zod-schema.ts`, `src/config/types.secrets.ts`; PicoClaw `docs/security_configuration.md`, `docs/configuration.md`; IronClaw `src/config/mod.rs`, `src/secrets/*`, `src/cli/tool.rs`.

## Connectivity & Remote Access

- [ ] Tailscale Serve/Funnel auto-configuration (tailnet-only or public HTTPS)
	- Research notes: OpenClaw is the clearest direct reference because tailnet-aware bind and pairing helpers are already present in the device-pairing/tooling stack. PicoClaw did not surface an equivalent Tailscale automation flow in the current search. IronClaw has the strongest secondary reference through its tunnel setup and explicit feature-parity callout for Tailscale integration.
	- References: OpenClaw `extensions/device-pair/api.ts`, `src/pairing/setup-code.ts`; PicoClaw no obvious equivalent found in current repo search; IronClaw `FEATURE_PARITY.md`, `src/setup/channels.rs`, `src/tunnel/mod.rs`.
- [ ] SSH tunnel automation for remote gateway access
	- Research notes: OpenClaw has some sandbox/SSH configuration surfaces, but the current search did not show a polished gateway SSH tunnel helper. PicoClaw likewise did not surface a first-class SSH tunnel workflow. IronClaw is the strongest direct reference because its onboarding/setup already treats externally managed tunnels and hosted deployments as first-class.
	- References: OpenClaw `src/config/zod-schema.agent-runtime.ts`; PicoClaw `docs/configuration.md`; IronClaw `src/setup/channels.rs`, `docs/drafts/install/vps.mdx`.
- [ ] Remote gateway control from local CLI
	- Research notes: OpenClaw and IronClaw are both relevant: OpenClaw already exposes a gateway/control UI plus CLI commands around status and pairing, while IronClaw has a more explicit web API surface that can back remote CLI control. PicoClaw's launcher is more browser-centric in the current tree.
	- References: OpenClaw `src/commands/status.types.ts`, `src/cli/pairing-cli.ts`; PicoClaw `web/README.md`, `web/backend/api/*`; IronClaw `src/channels/web/CLAUDE.md`, `docs/drafts/ops/api.mdx`.
- [ ] Bonjour/mDNS local network discovery
	- Research notes: IronClaw's own feature-parity matrix explicitly calls this out, making it the strongest visible reference that the concept belongs in the gateway layer. OpenClaw and PicoClaw did not surface comparable local discovery flows in the current searches.
	- References: OpenClaw no obvious equivalent found in current repo search; PicoClaw no obvious equivalent found in current repo search; IronClaw `FEATURE_PARITY.md`.
- [ ] Device pairing (QR code + setup code flow over WS)
	- Research notes: OpenClaw is the clearest direct reference because it already has a full device-pair subsystem with setup codes, QR generation, scopes, and approval. PicoClaw did not surface a comparable generalized WS device-pairing flow in the current search. IronClaw has adjacent node/pairing and web auth surfaces, but OpenClaw is the stronger direct blueprint.
	- References: OpenClaw `extensions/device-pair/index.ts`, `src/infra/device-pairing.ts`, `src/pairing/setup-code.ts`; PicoClaw no obvious equivalent found in current repo search; IronClaw `FEATURE_PARITY.md`, `src/channels/web/CLAUDE.md`.
- [ ] Gateway WebSocket node protocol (`node.list`, `node.describe`, `node.invoke`)
	- Research notes: OpenClaw is the clearest direct reference because it already has node-pairing state and multi-client gateway machinery around connected devices. PicoClaw's device/config surfaces are useful for UI inspiration, but not an equivalent node RPC protocol in the current search. IronClaw is the strongest secondary reference if Koios wants to expose this through web APIs, SSE, and orchestrator-backed jobs.
	- References: OpenClaw `src/infra/node-pairing.ts`, `src/gateway/server.auth.shared.ts`; PicoClaw `web/frontend/src/components/config/config-page.tsx`; IronClaw `FEATURE_PARITY.md`, `docs/drafts/ops/api.mdx`, `src/channels/web/CLAUDE.md`.
- [ ] Multi-client WS fan-out (broadcast presence/events to all connected clients)
	- Research notes: IronClaw is the strongest visible implementation reference because it already has SSE broadcast and websocket status/event infrastructure. OpenClaw also has a gateway/control UI and pairing-notify flows that imply fan-out support. PicoClaw's launcher and web chat are useful for UI consumers, but less explicit about cross-client broadcast internals.
	- References: OpenClaw `extensions/device-pair/index.ts`, `src/commands/status.types.ts`; PicoClaw `web/README.md`; IronClaw `FEATURE_PARITY.md`, `src/bridge/router.rs`, `docs/drafts/ops/api.mdx`.

## Nodes and Companion Devices

- [ ] Node pairing and approval flow
	- Research notes: OpenClaw is the clearest direct reference because it already has a distinct node-pairing subsystem with scopes and approval state. PicoClaw did not surface an equivalent general node approval flow in the current search. IronClaw's device and gateway surfaces are useful secondary references, but OpenClaw is the primary blueprint.
	- References: OpenClaw `src/infra/node-pairing.ts`, `extensions/device-pair/index.ts`; PicoClaw no obvious equivalent found in current repo search; IronClaw `FEATURE_PARITY.md`, `src/channels/web/CLAUDE.md`.
- [ ] macOS node mode
	- Research notes: This is mostly a Koios-specific execution target. OpenClaw's node/device pairing architecture is the best structural reference. PicoClaw and IronClaw did not surface dedicated macOS companion-node implementations in the current searches.
	- References: OpenClaw `src/infra/node-pairing.ts`, `extensions/device-pair/index.ts`; PicoClaw no obvious equivalent found in current repo search; IronClaw `FEATURE_PARITY.md`.
- [ ] iOS node support
	- Research notes: OpenClaw is again the best architectural reference because device pairing and tailnet-aware setup are already present. IronClaw's parity matrix is useful because it explicitly calls out APNs push and disconnected iOS wake paths, which matters if Koios wants real mobile-node behavior. PicoClaw did not surface an equivalent mobile node stack.
	- References: OpenClaw `extensions/device-pair/index.ts`, `src/infra/device-pairing.ts`; PicoClaw no obvious equivalent found in current repo search; IronClaw `FEATURE_PARITY.md`.
- [ ] Android node support
	- Research notes: PicoClaw is a useful comparison point because its ecosystem already includes device-facing integrations like MaixCam and hardware-centric channels, even though it did not surface a general Android node. OpenClaw has the stronger pairing architecture. IronClaw did not surface a dedicated Android node implementation in the current search.
	- References: OpenClaw `src/infra/device-pairing.ts`; PicoClaw `docs/chat-apps.md`, `docs/vi/chat-apps.md`; IronClaw no obvious equivalent found in current repo search.
- [ ] Headless node host for remote execution
	- Research notes: OpenClaw is the clearest direct reference because its node/device pairing model and sandbox/runtime separation already point toward remote execution surfaces. IronClaw is the best secondary reference because jobs, sandbox orchestration, and web APIs are already structured around remote execution. PicoClaw is less explicit here in the current search.
	- References: OpenClaw `src/infra/node-pairing.ts`, `extensions/device-pair/api.ts`; PicoClaw `docs/configuration.md`; IronClaw `src/tools/builtin/job.rs`, `src/sandbox/manager.rs`, `docs/drafts/ops/api.mdx`.
- [ ] Node browser proxy mode
	- Research notes: OpenClaw is the strongest conceptual reference because it already separates browser profiles, remote CDP, and agent/browser transport boundaries. PicoClaw and IronClaw did not surface an equivalent node-hosted browser proxy mode in the current searches.
	- References: OpenClaw `extensions/browser/src/browser/profile-capabilities.ts`, `docs/tools/browser.md`; PicoClaw no obvious equivalent found in current repo search; IronClaw `skills/local-test/SKILL.md`.
- [ ] Canvas presentation and snapshots on nodes
	- Research notes: This appears mostly Koios-specific. OpenClaw's browser snapshot system is still the best upstream reference for structured visual surfaces and snapshots. PicoClaw and IronClaw did not surface a direct node-canvas analogue in the current searches.
	- References: OpenClaw `extensions/browser/src/browser/client.ts`, `docs/tools/browser.md`; PicoClaw no obvious equivalent found in current repo search; IronClaw no obvious equivalent found in current repo search.
- [ ] A2UI-style agent-driven visual workspace
	- Research notes: None of the upstreams surfaced a direct equivalent by name. OpenClaw's browser and structured snapshot stack is still the nearest reference for agent-driven visual state. PicoClaw and IronClaw did not show a matching visual workspace system in the current searches.
	- References: OpenClaw `extensions/browser/src/browser/client.ts`, `extensions/browser/src/browser/pw-tools-core.snapshot.ts`; PicoClaw no obvious equivalent found in current repo search; IronClaw no obvious equivalent found in current repo search.
- [ ] Camera snapshot support on nodes
	- Research notes: PicoClaw is the most relevant secondary reference because it already touches hardware/camera-adjacent surfaces such as MaixCam, even though that is not a general node camera API. OpenClaw and IronClaw did not surface equivalent built-in node camera tooling in the current searches.
	- References: OpenClaw no obvious equivalent found in current repo search; PicoClaw `docs/chat-apps.md`, `docs/vi/chat-apps.md`; IronClaw no obvious equivalent found in current repo search.
- [ ] Camera video clip support on nodes
	- Research notes: Same shape as camera snapshots: PicoClaw is the closest hardware-adjacent ecosystem reference, but none of the three repos surfaced a direct general-purpose node video clip tool in the current searches.
	- References: OpenClaw no obvious equivalent found in current repo search; PicoClaw `docs/chat-apps.md`; IronClaw no obvious equivalent found in current repo search.
- [ ] Screen recording on nodes
	- Research notes: OpenClaw's browser/media stack is the best conceptual reference for capture surfaces, but the current search surfaced screenshots more clearly than full recording. PicoClaw and IronClaw did not show direct node screen-record tooling in the current searches.
	- References: OpenClaw `extensions/browser/src/browser/routes/agent.snapshot.ts`; PicoClaw no obvious equivalent found in current repo search; IronClaw `skills/local-test/SKILL.md`.
- [ ] Location tool via nodes
	- Research notes: This remains mostly Koios-specific. None of the three upstreams surfaced a first-class location-via-device tool in the current searches.
	- References: OpenClaw no obvious equivalent found in current repo search; PicoClaw no obvious equivalent found in current repo search; IronClaw no obvious equivalent found in current repo search.
- [ ] Android device command families
	- Research notes: PicoClaw is the most relevant ecosystem comparison because it already spans device-oriented integrations, but the current search did not surface a general Android command family. OpenClaw and IronClaw did not show direct equivalents in the current searches.
	- References: OpenClaw no obvious equivalent found in current repo search; PicoClaw `docs/chat-apps.md`; IronClaw no obvious equivalent found in current repo search.
- [ ] SMS via Android node
	- Research notes: None of the three repos surfaced a direct equivalent in the current searches. This likely needs to be treated as a Koios-native node capability rather than a direct parity item.
	- References: OpenClaw no obvious equivalent found in current repo search; PicoClaw no obvious equivalent found in current repo search; IronClaw no obvious equivalent found in current repo search.
- [ ] Notification tooling via nodes
	- Research notes: OpenClaw's device-pair and multi-client machinery is the best structural reference if Koios wants node-routed notification delivery. PicoClaw and IronClaw did not surface comparable node-notification APIs in the current searches.
	- References: OpenClaw `extensions/device-pair/index.ts`, `src/infra/device-pairing.ts`; PicoClaw no obvious equivalent found in current repo search; IronClaw `FEATURE_PARITY.md`.

## Web UI & Dashboard

- [ ] Control UI served by the gateway
	- Research notes: All three repos are useful, but in different ways. OpenClaw already has a control UI concept wired into gateway config, PicoClaw has the clearest concrete launcher/dashboard product today, and IronClaw has the cleanest typed web API and gateway-module docs. Koios should likely combine OpenClaw's gateway-owned control plane with PicoClaw's operator UX and IronClaw's API discipline.
	- References: OpenClaw `src/config/zod-schema.ts`, `docs/gateway/configuration-reference.md`; PicoClaw `web/README.md`, `web/frontend/src/routeTree.gen.ts`; IronClaw `src/channels/web/CLAUDE.md`, `docs/drafts/ops/api.mdx`.
- [ ] Dashboard for health, sessions, jobs, devices, and agent state
	- Research notes: PicoClaw is the clearest visible UI reference because its launcher already exposes models, credentials, channels, tools, skills, logs, and runtime settings. OpenClaw has the stronger status/run-state runtime concepts. IronClaw is the best secondary reference for jobs, logs, pairing, and live event streams.
	- References: OpenClaw `src/commands/status.types.ts`, `src/agents/pi-embedded-runner/runs.ts`; PicoClaw `web/README.md`, `web/frontend/src/components/config/config-page.tsx`; IronClaw `src/channels/web/CLAUDE.md`, `docs/drafts/ops/api.mdx`, `docs/drafts/ops/logging.mdx`.
- [ ] WebChat embedded chat surface (served from gateway)
	- Research notes: PicoClaw is the strongest direct reference because it already ships a browser-based chat UI backed by the gateway/launcher. OpenClaw has a control UI concept but the current search surfaced less concrete chat-UI code than PicoClaw. IronClaw also has a web gateway with chat-oriented APIs and live log/event channels.
	- References: OpenClaw `src/config/zod-schema.ts`; PicoClaw `web/README.md`, `web/frontend/src/routes/index.tsx`, `web/frontend/src/routes/__root.tsx`; IronClaw `src/channels/web/CLAUDE.md`, `docs/drafts/ops/api.mdx`.
- [ ] Sessions table with collapse / expand in UI
	- Research notes: PicoClaw is the clearest UI reference because its launcher already manages multiple runtime/config pages and session-aware chat state. OpenClaw's status/run metadata is the better data-model reference. IronClaw's web API and SSE streams are the strongest backend reference if Koios wants live session tables.
	- References: OpenClaw `src/commands/status.types.ts`; PicoClaw `web/README.md`, `web/frontend/src/components/*`; IronClaw `docs/drafts/ops/api.mdx`, `src/channels/web/CLAUDE.md`.
- [ ] Skill install/manage UI
	- Research notes: PicoClaw is the strongest direct reference because it already has dedicated skill-management and registry pages in the launcher. OpenClaw also has a strong skills UI/controller stack and managed-skill flows. IronClaw has a typed skills API and drafted UI references, which is the best backend contract reference.
	- References: OpenClaw `ui/src/ui/views/skills.ts`, `ui/src/ui/controllers/skills.ts`, `src/gateway/server-methods/skills.ts`; PicoClaw `web/frontend/src/components/agent/skills/*`, `web/frontend/src/components/agent/hub/*`, `web/backend/api/skills.go`; IronClaw `src/channels/web/handlers/skills.rs`, `docs/drafts/ui-reference/skills.mdx`.
- [ ] Model switching UI
	- Research notes: PicoClaw is the clearest visible reference because `/models` is already a first-class launcher flow. OpenClaw has the better runtime/status references for per-session model state and provider catalogs. IronClaw is the strongest secondary reference if Koios wants model switching to align with typed provider/model registries.
	- References: OpenClaw `docs/providers/models.md`, `src/commands/status.summary.ts`; PicoClaw `web/README.md`, `web/frontend/src/routes/models.tsx`; IronClaw `docs/capabilities/llm-providers.md`, `src/llm/registry.rs`.
- [ ] Active memory diagnostics panel (`/trace` toggle)
	- Research notes: OpenClaw is the clearest direct reference because it already has trace/verbose/status concepts suitable for a diagnostics panel. PicoClaw's launcher logs and debug flows are a useful UX reference, but not as memory-specific. IronClaw has the strongest live logging and SSE/event infrastructure for powering a diagnostics panel.
	- References: OpenClaw `src/auto-reply/status.ts`, `src/tui/tui-command-handlers.ts`; PicoClaw `web/README.md`, `docs/debug.md`; IronClaw `docs/drafts/ops/logging.mdx`, `src/bridge/router.rs`, `src/channels/web/CLAUDE.md`.
- [ ] Config RPCs for get, patch, apply, and schema lookup
	- Research notes: PicoClaw is the strongest direct reference because its launcher already exposes config fetch/patch APIs used by the frontend. OpenClaw's config schema and control UI settings are the better validation reference. IronClaw's web API discipline is the strongest secondary reference if Koios wants config RPCs to be part of a broader ops API.
	- References: OpenClaw `src/config/zod-schema.ts`, `docs/gateway/configuration-reference.md`; PicoClaw `web/frontend/src/api/channels.ts`, `web/backend/api/*`; IronClaw `docs/drafts/ops/api.mdx`, `src/channels/web/CLAUDE.md`.
- [ ] Agent-accessible gateway tool for runtime config and ops
	- Research notes: OpenClaw is the clearest conceptual reference because gateway state, status, browser, pairing, and skills are already part of the same runtime family. PicoClaw provides concrete config APIs that such a tool could wrap. IronClaw is the best typed ops/API reference if Koios wants this tool to sit on top of authenticated REST/SSE endpoints.
	- References: OpenClaw `src/commands/status.types.ts`, `src/gateway/server-methods/skills.ts`; PicoClaw `web/frontend/src/api/channels.ts`, `web/backend/api/*`; IronClaw `docs/drafts/ops/api.mdx`, `src/channels/web/CLAUDE.md`.
- [ ] Config include and splitting support
	- Research notes: OpenClaw is the clearest reference because its config system is already large and schema-driven, so include/split support would fit naturally there. PicoClaw is a useful UX reference because launcher config and app config are already separated. IronClaw's large typed settings surface is the best secondary reference if Koios wants split config with clear ownership boundaries.
	- References: OpenClaw `docs/gateway/configuration.md`, `src/config/zod-schema.ts`; PicoClaw `web/backend/launcherconfig/config.go`, `docs/configuration.md`; IronClaw `src/config/mod.rs`, `docs/drafts/setup/configuration.mdx`.

## Onboarding & Operations

- [ ] Guided onboarding wizard (`onboard` command with interactive step-by-step)
	- Research notes: IronClaw is the strongest direct reference because it already has a full multi-step onboarding wizard covering providers, channels, extensions, sandbox, and background tasks. OpenClaw also has onboarding helpers for channels and skills, but not as unified a wizard in the current search. PicoClaw's launcher-guided setup is the strongest UI reference.
	- References: OpenClaw `src/commands/onboard-skills.ts`, `src/flows/channel-setup.prompts.ts`; PicoClaw `README.fr.md`, `web/frontend/src/routes/launcher-setup.tsx`; IronClaw `src/setup/wizard.rs`, `src/setup/README.md`.
- [ ] Interactive onboarding flow
	- Research notes: PicoClaw is the clearest interactive UX reference because the launcher already walks users through setup, login, config, and gateway startup. IronClaw is the best CLI/TUI-style reference because its wizard is structured and heavily documented. OpenClaw's setup flows are the strongest modular channel-by-channel reference.
	- References: OpenClaw `src/flows/channel-setup.prompts.ts`, `extensions/*/src/setup-surface.ts`; PicoClaw `web/README.md`, `web/frontend/src/routes/launcher-setup.tsx`, `web/frontend/src/routes/launcher-login.tsx`; IronClaw `src/setup/wizard.rs`, `src/setup/channels.rs`.
- [ ] Config wizard
	- Research notes: PicoClaw is the strongest direct reference because it already exposes a practical config form backed by patch APIs. OpenClaw is the stronger validation/schema reference. IronClaw is the strongest secondary reference if Koios wants wizard output to map cleanly onto typed settings and secrets storage.
	- References: OpenClaw `src/config/zod-schema.ts`, `src/flows/channel-setup.prompts.ts`; PicoClaw `web/frontend/src/components/config/config-page.tsx`, `web/frontend/src/components/config/form-model.ts`; IronClaw `src/setup/wizard.rs`, `src/config/mod.rs`.
- [ ] Update command with release channel selection
	- Research notes: OpenClaw and IronClaw are stronger operational references than PicoClaw here because both lean more toward daemon/CLI operations and lifecycle hygiene. PicoClaw's launcher packaging is still useful for desktop-style update UX. No single perfect upstream reference surfaced in the current search, so this remains partly Koios-specific.
	- References: OpenClaw `docs/channels/pairing.md` (update hygiene cross-links), `docs/gateway/security/index.md`; PicoClaw `README.fr.md`, `web/README.md`; IronClaw `src/setup/README.md`, `docs/drafts/install/vps.mdx`.
- [ ] Nix packaging flow
	- Research notes: None of the three repos surfaced a strong Nix-specific packaging flow in the current searches. This is largely Koios-specific operational work.
	- References: OpenClaw no obvious equivalent found in current repo search; PicoClaw no obvious equivalent found in current repo search; IronClaw no obvious equivalent found in current repo search.
- [ ] Docker-first packaged install flow
	- Research notes: PicoClaw is the clearest visible reference because its docs explicitly include Docker-based first-run/install flows. IronClaw is the stronger backend/runtime reference because Docker is central to sandbox/orchestrator operation. OpenClaw's sandboxing and doctor checks help define prerequisites and repair behavior.
	- References: OpenClaw `docs/gateway/sandboxing.md`, `src/commands/doctor-sandbox.ts`; PicoClaw `README.fr.md`, `web/README.md`; IronClaw `src/sandbox/manager.rs`, `src/main.rs`, `docs/drafts/install/vps.mdx`.
- [ ] Daemon and service install workflow
	- Research notes: PicoClaw is the clearest visible reference for desktop/service launcher behavior. IronClaw is the stronger daemon/backend reference because it has a more explicit service-style operational model. OpenClaw is useful mainly for gateway and doctor prerequisites.
	- References: OpenClaw `docs/gateway/security/index.md`; PicoClaw `web/README.md`, `web/backend/launcherconfig/config.go`; IronClaw `src/main.rs`, `src/cli/doctor.rs`, `docs/drafts/install/vps.mdx`.
- [ ] `auth login` - OAuth device code flow for providers
	- Research notes: All three repos are useful here. OpenClaw already has provider auth complexity, PicoClaw has concrete browser and device-code OAuth helpers, and IronClaw has explicit OAuth tool and provider auth flows. PicoClaw and IronClaw are especially strong if Koios wants both browser and device-code variants.
	- References: OpenClaw `docs/providers/index.md`, `extensions/google/gemini-cli-provider.ts`; PicoClaw `pkg/auth/oauth.go`, `docs/ANTIGRAVITY_AUTH.md`, `web/frontend/src/components/credentials/credentials-page.tsx`; IronClaw `src/cli/tool.rs`, `src/llm/openai_codex_session.rs`, `src/bridge/auth_manager.rs`.

## Provider Ergonomics

- [ ] Multiple auth profiles per provider
	- Status note: Koios already ships peer-scoped BYOK LLM provider profiles with named profile selection, per-peer/session activation, encrypted storage, profile CRUD/test operations, and multi-key support inside a profile. That covers multiple LLM credential profiles, but it does not yet look like the broader provider-auth/auth-manager shape implied here (for example OAuth-backed provider auth profiles, provider-owned onboarding, or a generic credentials UX beyond the LLM BYOK path).
	- Research notes: PicoClaw and IronClaw are the strongest references here. PicoClaw's provider auth plugin structure and credentials UI already assume multiple profiles and OAuth-backed providers. IronClaw's auth manager and tool auth flows are the strongest typed reference. OpenClaw is still useful for provider-owned onboarding and aliasing, but the current search surfaced auth-profile mechanics less explicitly than the other two.
	- References: OpenClaw `docs/providers/index.md`, `extensions/xiaomi/onboard.ts`; PicoClaw `docs/ANTIGRAVITY_AUTH.md`, `web/frontend/src/components/credentials/credentials-page.tsx`; IronClaw `src/bridge/auth_manager.rs`, `src/cli/tool.rs`, `src/tools/wasm/capabilities_schema.rs`.

## Analytics & Observability

- [-] Structured metrics export (Prometheus / OpenTelemetry) (DON'T IMPLEMENT)
	- Research notes: IronClaw is the strongest visible reference because its observability and ops API layers are more explicit, even though the current search surfaced SSE/logging more clearly than Prometheus exporters. OpenClaw is a useful secondary reference for rich status/run metrics. PicoClaw provides practical dashboard/log surfaces, but not an obvious dedicated metrics exporter in the current search.
	- References: OpenClaw `src/commands/status.types.ts`, `src/agents/pi-embedded-runner/runs.ts`; PicoClaw `web/README.md`; IronClaw `src/config/mod.rs`, `docs/drafts/ops/api.mdx`, `docs/drafts/ops/logging.mdx`.
- [x] Per-run timing breakdown in run records
	- Implementation details:
		- Extend the durable run record schema/model with a timing summary object that captures `queued_at`, `started_at`, `completed_at`, total wall-clock duration, queue latency, model time, tool-execution time, and finalization/persistence time.
		- Add per-phase timing events at the runner boundary rather than inside presentation code so CLI/API/status consumers all read the same persisted data.
		- Aggregate timing from turn/tool/model spans into the run record when a run completes, and persist partial timing data for failed, canceled, or interrupted runs.
		- Expose the timing breakdown in run status/detail responses without adding fields that conflict with Monaco API schemas; verify request/response fields against `https://docs.0xmonaco.com/proto-openapi/api/openapi.yaml` before wiring any Monaco-facing payloads.
		- Update status/detail rendering to show concise timing fields while keeping older run records readable with zero-value/omitted timing data handled explicitly.
		- Add tests for successful runs, failed/canceled runs with partial timings, schema migration/backfill behavior, and status/detail serialization.
	- Research notes: OpenClaw is the clearest direct reference because active runs and status summaries are already modeled explicitly. PicoClaw has per-turn/runtime metadata, but the current search surfaced less of a durable per-run ledger. IronClaw is the strongest secondary reference because job events, SSE streams, and structured events already exist.
	- References: OpenClaw `src/agents/pi-embedded-runner/runs.ts`, `src/commands/status.types.ts`; PicoClaw `pkg/agent/turn.go`; IronClaw `crates/ironclaw_common/src/event.rs`, `src/channels/channel.rs`, `src/tools/builtin/job.rs`.
- [x] Model performance logging
	- Implementation details:
		- Add structured model-call performance records at the provider/client boundary, including run ID, turn ID when available, provider, model, operation type, request start/end timestamps, latency, streaming first-token latency, completion status, retry count, and token usage fields already returned by the provider.
		- Keep prompt, completion text, tool arguments, credentials, and other sensitive payloads out of logs by default; log only IDs, metadata, timings, counts, and sanitized error classes/messages.
		- Wire logs through the existing diagnostics/logging path instead of ad hoc stdout output, and gate any verbose model performance logging behind `koios.config.toml` configuration fields declared in `internal/config/config.go`.
		- Correlate model performance records with run timing breakdowns so aggregate model duration in run records can be derived from the same source events.
		- Include retry and streaming behavior in the log model so slow provider setup, first-token delay, interrupted streams, and provider errors are distinguishable.
		- Add tests covering logging enable/disable configuration, redaction of sensitive data, provider success/failure records, retries, streaming first-token latency, and aggregation into per-run timing summaries.
	- Research notes: IronClaw and OpenClaw are the strongest references here. OpenClaw already tracks provider attribution and run state. IronClaw has the better observability/logging architecture for exposing model timings and behavior over APIs. PicoClaw provides a useful dashboard/logging surface but less explicit performance instrumentation in the current search.
	- References: OpenClaw `src/agents/provider-attribution.ts`, `src/commands/status.types.ts`; PicoClaw `web/README.md`; IronClaw `docs/drafts/ops/logging.mdx`, `docs/drafts/ops/api.mdx`, `src/config/mod.rs`.

## External Service Channels Backlog

- [ ] Discord channel (bot + intents)
	- Research notes: OpenClaw models Discord as a full plugin channel with guild/channel allowlists and per-channel mention rules. PicoClaw already has a simpler Discord bot path with `allow_from` and `group_trigger`; IronClaw has a stronger parity point for DM pairing and Gateway message intake, but still lags OpenClaw on some richer per-guild controls.
	- References: OpenClaw `extensions/discord/src/channel.ts`, `docs/gateway/configuration-reference.md` (Discord); PicoClaw `docs/channels/discord/README.md`, `docs/chat-apps.md`; IronClaw `channels-src/discord/src/lib.rs`, `channels-src/discord/README.md`, `FEATURE_PARITY.md`.
	- Research notes: OpenClaw uses WhatsApp as a first-class channel with DM policy, group allowlists, and per-channel chunking controls. PicoClaw already supports WhatsApp natively or through a bridge, so it is useful for QR/session lifecycle ideas. IronClaw's visible implementation is WhatsApp Cloud API and is more webhook-oriented than OpenClaw's Baileys-style model.
	- References: OpenClaw `extensions/whatsapp/src/channel.ts`, `docs/gateway/configuration-reference.md` (WhatsApp); PicoClaw `docs/chat-apps.md#whatsapp`; IronClaw `channels-src/whatsapp/src/lib.rs`.
- [ ] Slack channel (Bolt / Socket Mode)
	- Research notes: OpenClaw has the most complete reference for Socket Mode Slack with channel allowlists, mention gating, streaming, and exec-approval targeting. PicoClaw already exposes a basic Slack channel with `allow_from`. IronClaw also has Slack channel and message-tool support, including DM pairing logic, but appears less opinionated around route allowlists than OpenClaw.
	- References: OpenClaw `extensions/slack/src/channel.ts`, `docs/channels/slack.md`, `docs/gateway/configuration-reference.md` (Slack); PicoClaw `docs/chat-apps.md`, `pkg/migrate/sources/openclaw/openclaw_config.go`; IronClaw `channels-src/slack/src/lib.rs`, `tools-src/slack/src/lib.rs`.
	- Research notes: OpenClaw includes Nextcloud Talk in its bundled plugin list and pairing docs. PicoClaw and IronClaw did not show equivalents in the current repo search.
	- References: OpenClaw `docs/concepts/features.md`, `docs/channels/pairing.md`; PicoClaw no obvious equivalent found in current repo search; IronClaw no obvious equivalent found in current repo search.
- [ ] Discord native slash commands / text command tools
	- Research notes: OpenClaw already has a mature Discord channel implementation, making it the best place to study message command handling and channel-specific UX. PicoClaw and IronClaw both support Discord channels, but the current searches did not surface a distinct Discord-native tool-command layer beyond channel integration.
	- References: OpenClaw `extensions/discord/src/channel.ts`, `docs/channels/discord.md`; PicoClaw `docs/channels/discord/README.md`; IronClaw `channels-src/discord/src/lib.rs`, `channels-src/discord/README.md`.
- [ ] Slack action tools
	- Research notes: OpenClaw's Slack channel/action model is the clearest comparison point for channel-native outbound and interactive actions. PicoClaw supports Slack as a channel but the current search did not surface a matching action-tool layer. IronClaw has Slack channel support and WASM tool patterns, but no obvious direct Slack-action tool surface in the current search.
	- References: OpenClaw `extensions/slack/src/channel.ts`, `docs/channels/slack.md`; PicoClaw `docs/chat-apps.md`; IronClaw `channels-src/slack/src/lib.rs`, `tools-src/slack/README.md`.
- [ ] `channels login` - QR / credential flow per channel
	- Research notes: OpenClaw is the clearest direct reference because many channel setup flows already expose QR or credential login commands. PicoClaw is a strong secondary reference because the launcher exposes QR-based channel setup helpers for WeChat and WeCom. IronClaw's setup wizard is the strongest typed CLI reference for channel-specific credential collection and tunnel validation.
	- References: OpenClaw `docs/channels/whatsapp.md`, `extensions/*/src/setup-surface.ts`, `src/flows/channel-setup.prompts.ts`; PicoClaw `web/README.md`, `web/frontend/src/api/channels.ts`; IronClaw `src/setup/channels.rs`, `docs/drafts/help/troubleshooting.mdx`.

## Personal Agent Research Backlog

- [ ] Proactive briefing feed instead of only scheduled summaries
	- Research notes: Current personal assistants are shifting from reactive chat toward proactive briefing surfaces that curate useful updates before the user asks. OpenAI's ChatGPT Pulse is a strong example: it delivers personalized update cards based on chats, feedback, and connected apps such as calendar, and is explicitly framed as a move toward a proactive assistant rather than a question-answering bot. Koios already has cron, workflows, memory, and session history, but it does not yet appear to have a first-class briefing feed that accumulates and ranks proactive work over time.
	- Suggested Koios shape: add a `briefs` or `inbox` subsystem that stores generated briefing cards, allows save-for-later and dismissal actions, and supports sources such as calendar changes, waiting-on reminders, task deadlines, project drift, and digest-worthy external updates.
	- References: OpenAI `Introducing ChatGPT Pulse` (published September 17, 2025), https://openai.com/index/introducing-chatgpt-pulse/ ; OpenAI Help `Tasks in ChatGPT`, https://help.openai.com/en/articles/10291617-scheduled-tasks-in-chatgpt
- [ ] User-facing task center with recurring, pausable, and save-for-later tasks
	- Research notes: A strong pattern in recent assistant UX is that scheduled automation is exposed as a user-managed task system, not just as an implementation detail like cron. OpenAI's current task model supports one-off and recurring tasks, editing, pausing, deleting, and a central task list. Koios already has scheduler and workflow primitives, but a personal agent benefits from a more approachable layer that tracks the user's requested automations as durable objects with lifecycle controls and delivery preferences.
	- Suggested Koios shape: build a `tasks` center above cron and workflows with chat-created tasks, active/paused/completed states, notification preferences, and clear links back to the originating conversation or automation recipe.
	- References: OpenAI Help `Tasks in ChatGPT`, https://help.openai.com/en/articles/10291617-scheduled-tasks-in-chatgpt
- [ ] Memory import/export and backup portability
	- Research notes: Personal assistants need portability if they are going to hold meaningful user context over time. Claude's 2025-2026 memory rollout paired memory with import/export support, which is an important trust feature because it gives users a path to audit, back up, migrate, or reset their personal context without vendor lock-in. Koios currently has memory persistence, but memory portability does not appear to be called out as a first-class user feature in the backlog.
	- Suggested Koios shape: provide export/import for long-term memory, preference stores, and memory summaries in a documented format, with selective export by namespace, category, or time range.
	- References: Claude release notes, https://support.claude.com/en/articles/12138966-release-notes ; Claude memory announcement, https://claude.com/blog/memory
- [ ] Incognito or no-memory session mode
	- Research notes: As assistants get more persistent, privacy controls become more important. Claude explicitly introduced incognito chats so a user can get help without affecting history or memory. For a personal agent, this is useful not just for confidentiality, but also for tasks where the user wants a clean slate or does not want transient brainstorming to pollute the agent's future behavior. Koios has session isolation, but that is different from an intentional "do not learn from this conversation" mode.
	- Suggested Koios shape: add a session flag or conversation mode that bypasses memory writes, skips standing-order mutation, omits durable session history where configured, and clearly signals this state in the UI and API.
	- References: Claude memory announcement, https://claude.com/blog/memory ; Claude release notes, https://support.claude.com/en/articles/12138966-release-notes
- [ ] Layered personalization model: global preferences, project instructions, and styles
	- Research notes: Claude's current personalization model is useful because it separates account-wide preferences, project-specific instructions, and response styles instead of merging everything into one generic memory surface. That separation reduces prompt clutter and makes it clearer which instructions are behavioral defaults versus task-specific context versus output formatting preferences. Koios already has standing orders, but it would likely benefit from splitting them into similarly distinct layers.
	- Suggested Koios shape: preserve standing orders as one layer, but add explicit profile preferences, workspace or project instructions, and named output styles that can be combined or overridden independently.
	- Concrete plan:
		- Inventory the current standing-order and memory injection path, including where instructions are stored, loaded, ranked, merged into prompts, displayed to users, and mutated by tools.
		- Define explicit layer types and precedence: built-in system defaults, standing orders, user profile preferences, workspace/project instructions, named response styles, session overrides, and task-local instructions.
		- Add a persisted personalization model with stable IDs, layer type, scope, enabled/disabled state, priority, source/provenance, timestamps, and optional expiration or review metadata.
		- Implement a deterministic resolver that combines layers into the final instruction bundle, preserves provenance per emitted instruction, detects conflicting guidance, and applies narrower scopes after broader scopes.
		- Keep standing orders as a compatibility layer by migrating or adapting existing records into the new resolver without silently changing their priority.
		- Add CRUD surfaces for each layer: CLI commands first, then API/UI hooks where existing settings or memory management surfaces already exist.
		- Add named output styles as reusable style records that can be selected per request or session and overridden without changing global or project preferences.
		- Add prompt assembly changes so the model receives compact grouped sections such as `Global preferences`, `Project instructions`, `Style`, and `Session overrides` instead of one flattened memory block.
		- Add observability and audit support: show which layers contributed to a response, why an instruction was included, and which source wins when two layers conflict.
		- Document privacy and safety behavior, especially which layers are user-editable, project-scoped, exported/imported, or excluded from no-memory/incognito sessions.
	- Acceptance criteria:
		- Users can create, list, update, disable, and delete profile preferences, project instructions, and named styles independently.
		- A request can select a named style without mutating standing orders, profile preferences, or project instructions.
		- Project instructions override or narrow global preferences only for matching workspaces/projects.
		- The final prompt context is grouped by personalization layer and includes provenance for debugging or UI display.
		- Existing standing orders continue to apply through the new resolver and have an explicit migration or compatibility story.
		- Conflicting instructions produce deterministic resolution with visible provenance instead of order-dependent prompt clutter.
	- Test plan:
		- Unit-test resolver precedence, scope matching, disabled entries, conflict handling, and provenance output.
		- Add migration or compatibility tests covering existing standing-order records.
		- Add CLI/API tests for layer CRUD and style selection.
		- Add prompt-assembly golden tests proving grouped sections are emitted compactly and in stable order.
		- Add privacy tests proving no-memory/incognito sessions do not create or mutate durable personalization records.
	- References: Claude personalization features, https://support.claude.com/en/articles/10185728-understanding-claude-s-personalization-features ; Claude memory announcement, https://claude.com/blog/memory
- [ ] Auto-referenced connectors with visible provenance
	- Research notes: A notable improvement in ChatGPT's 2025 connector rollout is that connected Gmail, Google Calendar, and Google Contacts data can be referenced automatically in chat when relevant, instead of requiring explicit manual fetch each time. This pattern matters because personal assistants become much more fluid when they can opportunistically ground responses in the user's actual tools. The tradeoff is that automatic connector use must be explainable and scoped carefully.
	- Suggested Koios shape: support connector auto-reference policies per tool and per session, plus response annotations that show when calendar, contacts, email, or docs were pulled into a reply and why they were considered relevant.
	- References: OpenAI ChatGPT release notes (August 12-13, 2025 connector updates), https://help.openai.com/en/articles/6825453-chatgpt-release-notes%3F.ejs
- [ ] Calendar-native habit and focus scheduling
	- Research notes: Reclaim's strongest product pattern is that it schedules habits, tasks, and focus time directly into the user's calendar, then reschedules them automatically as conflicts appear. That is more useful than a generic todo or reminder system because it respects actual availability and protects time rather than merely naming intentions. Koios already has scheduling automation, but not a calendar-native habit and focus planner in the backlog.
	- Suggested Koios shape: create smart events for habits, focus blocks, and personal routines that can be assigned priorities, scheduling windows, privacy levels, and auto-rescheduling rules across one or more calendars.
	- References: Reclaim product page, https://reclaim.ai/ ; Reclaim Habits, https://reclaim.ai/features/habits
- [ ] Automatic waiting-on detection from outbound email or messages
	- Research notes: Superhuman's recent auto-reminders feature highlights a strong personal-assistant behavior: detecting when an outbound message likely requires a follow-up and resurfacing it if no reply arrives. This is stronger than a manually created reminder because it is inferred from communication patterns and tied to unresolved threads. Koios already has hooks and planned inbox-triggered automation, so this would fit naturally once message connectors exist.
	- Suggested Koios shape: infer waiting-on records from outbound communication, allow a configurable default follow-up window, and optionally prepare a draft reply or reminder when the item resurfaces.
	- References: Superhuman `Reminders on Autopilot` (updated January 28, 2026), https://help.superhuman.com/hc/en-us/articles/45270478397203-Reminders-on-Autopilot
- [ ] Meeting memory with extracted decisions, action items, and source linkage
	- Research notes: Meeting assistants such as Notion AI Meeting Notes and Otter emphasize not only transcription, but also durable extraction of summaries, decisions, and action items that remain searchable later. Notion also makes the connection to task systems explicit and highlights searchable access across past meeting content. For a personal agent, the differentiator is less about "can it transcribe" and more about "can it turn meetings into reliable future context."
	- Suggested Koios shape: support meeting imports or live capture, then store summaries, decisions, assignments, and slide or artifact references as searchable records linked back to the original meeting source.
	- References: Notion AI Meeting Notes, https://www.notion.com/en-US/product/ai-meeting-notes ; OtterPilot, https://get.otter.ai/ai-meeting-agent/
- [ ] Meeting consent and privacy workflow
	- Research notes: If Koios moves into meeting capture, consent cannot be treated as an afterthought. Notion explicitly calls out one-click consent collection before meetings start, which is a useful product benchmark because meeting memory is materially different from normal chat memory. This feature is especially important for a personal agent that may blur work and life contexts.
	- Suggested Koios shape: add meeting-capture policies, consent prompts, audit fields on captured meeting records, and hard blocks that prevent storage when consent requirements are not satisfied.
	- References: Notion AI Meeting Notes, https://www.notion.com/en-US/product/ai-meeting-notes
- [ ] Context-aware mobile and local-device actions
	- Research notes: Claude's September 3, 2025 release notes explicitly call out location, maps, calendar access, and reminders on mobile platforms. This pattern matters because personal agents are most useful when they can handle small, high-frequency actions in context, not only long-form reasoning. Koios currently has notification and local execution primitives, but a more direct personal-action layer for reminders, maps, and event drafting is not clearly listed.
	- Suggested Koios shape: add a node-backed or local-device action layer for reminders, draft calendar events, quick location lookups, commute prep, and other high-frequency personal actions with strong permission boundaries.
	- References: Claude release notes (September 3, 2025), https://support.claude.com/en/articles/12138966-release-notes
- [ ] Resumable long-running personal workflows with contextual suggestions
	- Research notes: Claude's Chrome expansion introduced long-running workflows, slash-command reuse, and contextual prompt suggestions tied to the current website. The important product lesson is that assistants are becoming more stateful and situational: they can keep working in the background and offer relevant next actions based on where the user is. Koios already has workflows and subagents, but not an explicitly personal pattern for resumable background help with contextual prompts.
	- Suggested Koios shape: allow long-running workflows to preserve state across interruptions, surface suggested next actions based on active session context, and support reusable prompt macros for common personal routines.
	- References: Claude release notes (September 16, 2025), https://support.claude.com/en/articles/12138966-release-notes
- [ ] Record mode or voice-note capture into editable summaries
	- Research notes: ChatGPT's record mode, rolled out in 2025, reflects a useful personal-agent pattern: quick capture of live conversations or voice notes into an editable summary artifact. This is different from formal meeting capture because it also supports casual personal note-taking, ad hoc planning, and mobile capture. Koios already has some multimodal groundwork, but I do not see a dedicated backlog item for voice capture into structured notes.
	- Suggested Koios shape: support short voice-note ingestion and meeting-style recording imports that generate editable summaries, action items, and optional memory candidates rather than only raw transcripts.
	- References: OpenAI ChatGPT release notes (June 18 and August 13, 2025 record mode entries), https://help.openai.com/en/articles/6825453-chatgpt-release-notes%3F.ejs
- [ ] Searchable library for generated artifacts and personal outputs
	- Research notes: Personal assistants increasingly generate reports, summaries, images, slides, and other artifacts that become valuable over time. OpenAI's 2025 image Library is a narrow example, but the broader product pattern is a durable artifact shelf rather than leaving generated work buried inside conversation threads. Koios already has workflows and session persistence, so a searchable library for generated outputs would make personal-agent work much more reusable.
	- Suggested Koios shape: index generated reports, meeting summaries, exported files, briefs, and bookmarks in a dedicated artifact library with provenance, tags, and links back to the originating session or workflow.
	- References: OpenAI ChatGPT release notes (April 2025 Library entry), https://help.openai.com/en/articles/6825453-chatgpt-release-notes%3F.ejs ; Claude release notes (file creation/editing updates), https://support.claude.com/en/articles/12138966-release-notes

## Miscellaneous Platform Gaps

- [-] X / Twitter search tool (DON'T IMPLEMENT no need its using paid API)
	- Research notes: None of the three repos surfaced a direct built-in X/Twitter search tool in the current searches. This is likely a Koios-native tool or an extension opportunity rather than a direct parity item.
	- References: OpenClaw no obvious equivalent found in current repo search; PicoClaw no obvious equivalent found in current repo search; IronClaw no obvious equivalent found in current repo search.
- [ ] Structured JSON-only LLM task tool
	- Research notes: OpenClaw and IronClaw are the strongest references because both already model provider capability and typed tool contracts. PicoClaw has provider abstractions, but the current search did not surface a dedicated JSON-only task tool. This likely fits naturally as a Koios builtin once tool-schema normalization is in place.
	- References: OpenClaw `src/agents/provider-attribution.ts`, `src/plugins/types.ts`; PicoClaw `pkg/providers/types.go`; IronClaw `src/tools/wasm/capabilities_schema.rs`, `src/llm/registry.rs`.
- [ ] Protocol typing and codegen from a single schema source
	- Research notes: IronClaw is the strongest architectural reference because much of its web, extension, and capability surface is already explicitly typed. OpenClaw is the best secondary reference for schema-driven config and plugin/runtime contracts. PicoClaw is less explicit here in the current search.
	- References: OpenClaw `src/config/zod-schema.ts`, `src/plugins/types.ts`; PicoClaw `web/frontend/src/api/*`; IronClaw `src/tools/wasm/capabilities_schema.rs`, `src/channels/wasm/schema.rs`, `src/channels/web/types.rs`.
- [-] Config env substitution and richer secret handling (DON'T IMPLEMENT only need config)
	- Research notes: OpenClaw is the clearest direct reference because secret inputs and config validation are already first-class. PicoClaw is also useful because it splits security config from app config. IronClaw has the strongest typed secrets subsystem and config resolution stack if Koios wants richer secret references and validation.
	- References: OpenClaw `src/config/types.secrets.ts`, `src/config/zod-schema.ts`; PicoClaw `docs/security_configuration.md`, `docs/configuration.md`; IronClaw `src/config/mod.rs`, `src/secrets/*`.

## MCP SDK Migration Plan

Goal: replace Koios' hand-rolled MCP client transport and protocol implementation with the official `github.com/modelcontextprotocol/go-sdk` while preserving Koios' existing manager, registry, tool-prefixing, handler, and runtime behavior.

Overview: migrate behind the existing `internal/mcp.Client` boundary first, keep `internal/mcp.Manager` as the orchestration layer, add SDK-backed tests for every supported transport and MCP asset type, then remove the legacy stdio/http JSON-RPC implementations after parity is proven.

### Phase 1 — Add the official MCP SDK behind Koios' existing client boundary

Goal: introduce the official SDK dependency and a new SDK-backed client implementation without changing handler, registry, or runtime call sites.

Overview: keep Koios-facing types and the `Client` interface stable, add conversion helpers between SDK types and Koios types, and implement stdio tool listing/calling first so the smallest useful path is testable.

Rules:
- Keep `internal/mcp.Manager` behavior unchanged in this phase.
- Keep Koios-facing structs in `internal/mcp/client.go` so SDK types do not leak into handlers or agent runtime code.
- Add only the official `github.com/modelcontextprotocol/go-sdk` dependency for MCP protocol support.
- Do not add a config feature flag or legacy fallback path; the SDK adapter should become the implementation once tests pass.
- Do not delete `internal/mcp/stdio.go` or `internal/mcp/http.go` in this phase.
- Use `context.Context` for all SDK connection and request operations.
- Wrap SDK errors with Koios server names and operation names using `%w`.

- [ ] Add the official MCP Go SDK dependency.
	- [ ] Add `github.com/modelcontextprotocol/go-sdk` to `go.mod`.
	- [ ] Run module resolution so `go.sum` contains the SDK checksums.
	- [ ] Ensure no unrelated dependency is added as a direct requirement.
- [ ] Add `internal/mcp/sdk_client.go`.
	- [ ] Define an unexported `sdkClient` type that implements the existing `internal/mcp.Client` interface.
		- [ ] Store the MCP server runtime name.
		- [ ] Store the original `config.MCPServerConfig`.
		- [ ] Store the parsed per-request timeout.
		- [ ] Store the SDK client/session state needed to make requests after initialization.
		- [ ] Store a close function or transport/session handle so `Close` shuts down subprocesses and network resources.
		- [ ] Protect mutable session and notification state with a mutex when the SDK state is not explicitly concurrency-safe.
	- [ ] Add `NewSDKClient(c config.MCPServerConfig) Client`.
		- [ ] Parse `c.Timeout` using the same default behavior as the existing factory.
		- [ ] Normalize `c.Transport` using the existing accepted values: `stdio` and `http`.
		- [ ] Return a client that delays network or subprocess startup until `Initialize`.
		- [ ] Return an SDK client instance for both supported transports without adding a legacy compatibility branch.
- [ ] Implement SDK-backed stdio initialization.
	- [ ] Build the SDK command transport from `MCPServerConfig.Command` and `MCPServerConfig.Args`.
	- [ ] Pass `MCPServerConfig.Env` to the subprocess transport.
	- [ ] Preserve current stderr handling semantics by ensuring server stderr is not parsed as MCP JSON-RPC stdout.
	- [ ] Connect the SDK client during `Initialize`.
	- [ ] Store the connected SDK session on the `sdkClient` instance.
	- [ ] Make repeated `Initialize` calls idempotent for an already-connected session.
	- [ ] Return a contextual error when `Initialize` is called for `stdio` with an empty command.
	- [ ] Ensure `Close` terminates the stdio session and subprocess resources.
	- [ ] Ensure `Close` clears the stored SDK session state.
- [ ] Implement SDK-to-Koios conversion helpers for tool support.
	- [ ] Add `fromSDKTool` to convert SDK tool metadata into `mcp.Tool`.
		- [ ] Preserve `name`.
		- [ ] Preserve `title` when available.
		- [ ] Preserve `description`.
		- [ ] Preserve `inputSchema` as `json.RawMessage`.
		- [ ] Preserve `outputSchema` as `json.RawMessage` when available.
		- [ ] Preserve `annotations` as `json.RawMessage` when available.
	- [ ] Add `fromSDKToolResult` to convert SDK tool call results into `mcp.ToolResult`.
		- [ ] Preserve text content blocks.
		- [ ] Preserve image, audio, blob, and resource content blocks when exposed by the SDK.
		- [ ] Preserve `mimeType`, `uri`, `blob`, `data`, `resource`, and `annotations` fields when present.
		- [ ] Preserve `structuredContent` when exposed by the SDK.
		- [ ] Preserve `isError`.
		- [ ] Preserve raw extension fields needed by Koios when SDK types expose or retain them.
- [ ] Implement `ListTools` using the SDK session.
	- [ ] Return an error when `ListTools` is called before successful initialization.
	- [ ] Request all tool pages through the SDK API.
	- [ ] Convert all SDK tools into Koios `Tool` values.
	- [ ] Reuse `filterValidTools` before returning tools.
	- [ ] Wrap listing errors as `mcp sdk <server>: tools/list: <err>`.
- [ ] Implement `CallTool` and basic `CallToolWithInput`.
	- [ ] Return an error when tool calls are attempted before successful initialization.
	- [ ] Marshal `map[string]any` arguments into the SDK call parameter shape.
	- [ ] Call the SDK tool invocation method.
	- [ ] Convert the SDK result to Koios `ToolResult`.
	- [ ] Return normal tool errors through the existing `ToolResult.IsError` behavior when the SDK represents tool failures as successful MCP responses.
	- [ ] Return transport and protocol errors as Go errors.
	- [ ] Implement `CallToolWithInput` as the same basic path when `inputResponses` and `requestState` are nil.
- [ ] Add stdio SDK client tests.
	- [ ] Add a fake stdio MCP server test fixture that uses the official SDK server APIs.
	- [ ] Test that `Initialize` connects to the fake stdio server.
	- [ ] Test that calling `Initialize` twice does not create a second active session.
	- [ ] Test that `ListTools` returns a valid Koios `Tool`.
	- [ ] Test that invalid tools are filtered the same way as the legacy client.
	- [ ] Test that `CallTool` returns text content from a fake tool.
	- [ ] Test that `CallTool` preserves `IsError` for tool-level failures.
	- [ ] Test that calling `ListTools` before `Initialize` returns an error.
	- [ ] Test that calling `CallTool` before `Initialize` returns an error.
	- [ ] Test that `Close` can be called after successful initialization.
	- [ ] Test that `Close` can be called after failed initialization without panic or leaked state.

Acceptance criteria:
- [ ] `go test ./internal/mcp` passes with the SDK dependency added.
- [ ] The new SDK stdio client satisfies the existing `internal/mcp.Client` interface at compile time.
- [ ] No handler, registry, browser, extension, or agent runtime call site imports the official SDK directly.
- [ ] Existing legacy stdio/http files remain present until later parity phases.

### Phase 2 — Add Streamable HTTP support with Koios header behavior

Goal: implement SDK-backed HTTP MCP connections while preserving Koios' configured header and timeout behavior.

Overview: support `transport = "http"` through the SDK adapter, keep `MCPServerConfig.Headers` semantics, and test that headers reach the server on initialize, list, and call requests.

Rules:
- Preserve `MCPServerConfig.Headers` exactly as configured.
- Preserve per-request timeout behavior from `MCPServerConfig.Timeout`.
- Keep `sse` rejected in validation; do not reintroduce deprecated SSE transport support.
- Preserve existing per-tool dynamic header behavior when it is currently implemented by Koios.
- Implement a custom SDK transport using `github.com/modelcontextprotocol/go-sdk/jsonrpc` only if the official SDK HTTP transport cannot attach required headers.

- [ ] Implement SDK-backed HTTP initialization.
	- [ ] Create the SDK Streamable HTTP transport from `MCPServerConfig.URL`.
	- [ ] Attach `MCPServerConfig.Headers` to every SDK HTTP request.
	- [ ] Apply the parsed timeout to HTTP requests.
	- [ ] Connect the SDK client during `Initialize`.
	- [ ] Store the connected SDK session on the `sdkClient` instance.
	- [ ] Make repeated `Initialize` calls idempotent for an already-connected session.
	- [ ] Return a contextual error when `Initialize` is called for `http` with an empty URL.
	- [ ] Make `Close` release HTTP transport and session resources.
- [ ] Preserve static configured HTTP headers.
	- [ ] Send configured headers on initialization.
	- [ ] Send configured headers on `tools/list`.
	- [ ] Send configured headers on `tools/call`.
	- [ ] Send configured headers on `resources/list`.
	- [ ] Send configured headers on `resources/read`.
	- [ ] Send configured headers on `prompts/list`.
	- [ ] Send configured headers on `prompts/get`.
- [ ] Preserve per-tool argument-derived headers when invoking HTTP tools.
	- [ ] Move the existing dynamic header extraction logic into SDK-compatible request construction.
	- [ ] Apply dynamic headers only to `tools/call` requests.
	- [ ] Ensure configured static headers are still present when dynamic headers are added.
	- [ ] Ensure dynamic headers override static headers only when the existing legacy client already allowed that behavior.
	- [ ] Ensure invalid header names are rejected or skipped consistently with the current validation behavior.
- [ ] Add HTTP SDK client tests.
	- [ ] Add an in-process HTTP MCP test server using the official SDK server APIs or a minimal MCP-compatible handler.
	- [ ] Test that `Initialize` connects over HTTP.
	- [ ] Test that calling `Initialize` twice over HTTP does not create a second active session.
	- [ ] Test that `ListTools` works over HTTP.
	- [ ] Test that `CallTool` works over HTTP.
	- [ ] Test that static headers are present on initialization.
	- [ ] Test that static headers are present on tool listing.
	- [ ] Test that static headers are present on tool calls.
	- [ ] Test that static headers are present on resource requests.
	- [ ] Test that static headers are present on prompt requests.
	- [ ] Test that per-tool dynamic headers are present on tool calls.
	- [ ] Test that invalid per-tool dynamic header names do not produce unsafe HTTP requests.
	- [ ] Test that the configured timeout is applied to a delayed server response.
	- [ ] Test that failed HTTP responses return contextual Go errors.

Acceptance criteria:
- [ ] `go test ./internal/mcp` passes for both stdio and HTTP SDK-backed clients.
- [ ] `MCPServerConfig.Headers` behavior is covered by automated tests.
- [ ] `ValidateServerConfig` still rejects `sse`.
- [ ] No HTTP behavior required by existing tests is dropped.

### Phase 3 — Implement resources, resource templates, prompts, and result caching compatibility

Goal: complete SDK-backed support for non-tool MCP assets used by Koios.

Overview: implement resources and prompts through the SDK adapter while preserving manager-level cache behavior and existing handler APIs.

Rules:
- Keep cache ownership in `internal/mcp.Manager`.
- Keep `ReadResource` TTL caching behavior in the manager.
- Convert SDK asset types into Koios types before returning them from `Client`.
- Treat resources and prompts as optional capabilities, preserving current manager behavior when a server does not support them.
- Preserve opaque JSON fields rather than normalizing away information Koios already surfaces.

- [ ] Implement `ListResources`.
	- [ ] Return an error when `ListResources` is called before successful initialization.
	- [ ] Call the SDK resources list API.
	- [ ] Convert each SDK resource into Koios `Resource`.
		- [ ] Preserve `uri`.
		- [ ] Preserve `name`.
		- [ ] Preserve `title`.
		- [ ] Preserve `description`.
		- [ ] Preserve `mimeType`.
		- [ ] Preserve `size` when available.
		- [ ] Preserve `annotations` when available.
	- [ ] Return unsupported-capability errors in a form that `isOptionalCapabilityError` recognizes.
- [ ] Implement `ListResourceTemplates`.
	- [ ] Return an error when `ListResourceTemplates` is called before successful initialization.
	- [ ] Call the SDK resource template list API.
	- [ ] Convert each SDK resource template into Koios `ResourceTemplate`.
		- [ ] Preserve `uriTemplate`.
		- [ ] Preserve `name`.
		- [ ] Preserve `title`.
		- [ ] Preserve `description`.
		- [ ] Preserve `mimeType`.
		- [ ] Preserve `annotations` when available.
	- [ ] Return unsupported-capability errors in a form that `isOptionalCapabilityError` recognizes.
- [ ] Implement `ReadResource`.
	- [ ] Return an error when `ReadResource` is called before successful initialization.
	- [ ] Call the SDK resource read API.
	- [ ] Convert returned contents into Koios `ResourceContent`.
		- [ ] Preserve text contents.
		- [ ] Preserve blob contents.
		- [ ] Preserve `uri`.
		- [ ] Preserve `name`.
		- [ ] Preserve `mimeType`.
		- [ ] Preserve `annotations` when available.
	- [ ] Preserve `ttlMs` and `cacheScope` when exposed by the SDK or raw response metadata.
	- [ ] Return a Koios `ResourceReadResult` compatible with manager caching.
- [ ] Implement `ListPrompts`.
	- [ ] Return an error when `ListPrompts` is called before successful initialization.
	- [ ] Call the SDK prompts list API.
	- [ ] Convert each SDK prompt into Koios `Prompt`.
		- [ ] Preserve `name`.
		- [ ] Preserve `title`.
		- [ ] Preserve `description`.
		- [ ] Preserve arguments.
		- [ ] Preserve argument `required` flags.
- [ ] Implement `GetPrompt`.
	- [ ] Return an error when `GetPrompt` is called before successful initialization.
	- [ ] Call the SDK prompt get API with the provided argument map.
	- [ ] Convert returned prompt messages into Koios `PromptMessage`.
	- [ ] Convert prompt message content into Koios `Content`.
	- [ ] Preserve `description`.
	- [ ] Preserve `ttlMs` and `cacheScope` when exposed by the SDK or raw response metadata.
- [ ] Add asset conversion tests.
	- [ ] Test resource listing conversion.
	- [ ] Test resource template listing conversion.
	- [ ] Test resource read conversion for text content.
	- [ ] Test resource read conversion for blob content.
	- [ ] Test resource read TTL metadata reaches `ResourceReadResult`.
	- [ ] Test prompt listing conversion.
	- [ ] Test prompt argument conversion.
	- [ ] Test prompt get message conversion.
- [ ] Add manager compatibility tests for SDK-backed assets.
	- [ ] Test `Manager.ListResources` returns cached resources after startup.
	- [ ] Test `Manager.ListResourceTemplates` returns cached templates after startup.
	- [ ] Test `Manager.ReadResource` caches a TTL-backed resource read.
	- [ ] Test `Manager.ListPrompts` returns cached prompts after startup.
	- [ ] Test `Manager.GetPrompt` resolves a prompt through the SDK-backed client.
	- [ ] Test servers without resource support still connect when tools are available.
	- [ ] Test servers without prompt support still connect when tools are available.

Acceptance criteria:
- [ ] `go test ./internal/mcp` passes with SDK-backed tools, resources, resource templates, and prompts.
- [ ] Manager cache behavior remains covered by automated tests.
- [ ] Optional MCP capabilities remain optional.
- [ ] Koios-facing asset structs remain stable for handler consumers.

### Phase 4 — Implement notifications, cancellation, and manager invalidation behavior

Goal: preserve Koios' runtime freshness behavior for changing MCP tools, resources, and prompts.

Overview: convert SDK notification or callback behavior into Koios' existing `Notification` channel so `internal/mcp.Manager.consumeNotifications` can remain the central invalidation path.

Rules:
- Keep `Manager.consumeNotifications` and `Manager.applyNotification` as the owner of cache invalidation.
- Convert SDK notifications into Koios `Notification` values.
- Keep notification listening tied to the manager listener context.
- Ensure listener shutdown happens when a server is stopped, removed, updated, or the manager closes.
- Preserve cancellation support through the existing `Client.Cancel` method.

- [ ] Implement `Listen` on the SDK client.
	- [ ] Return a receive-only channel of Koios `Notification`.
	- [ ] Start SDK notification handling only after the client is initialized.
	- [ ] Forward `notifications/tools/list_changed`.
	- [ ] Forward `notifications/resources/list_changed`.
	- [ ] Forward `notifications/resources/updated`.
	- [ ] Forward `notifications/prompts/list_changed`.
	- [ ] Preserve raw notification params as `json.RawMessage`.
	- [ ] Close the notification channel when the listener context is canceled.
	- [ ] Close the notification channel when the SDK session closes.
	- [ ] Return a contextual error when `Listen` is called before initialization.
- [ ] Preserve manager invalidation behavior.
	- [ ] Test `notifications/tools/list_changed` clears cached tools and marks the server cache stale.
	- [ ] Test `notifications/resources/list_changed` clears cached resources, templates, and resource reads.
	- [ ] Test `notifications/resources/updated` clears only the matching cached resource read when a URI is present.
	- [ ] Test `notifications/resources/updated` clears all resource reads when the URI is missing or invalid.
	- [ ] Test `notifications/prompts/list_changed` clears cached prompts.
- [ ] Implement `Cancel`.
	- [ ] Send MCP cancellation through the official SDK when the SDK exposes a cancellation API.
	- [ ] Use the SDK JSON-RPC layer to send the cancellation notification when no typed API exists.
	- [ ] Preserve Koios' existing method-level behavior by accepting `requestID any` and `reason string`.
	- [ ] Wrap cancellation errors with server and request ID context.
	- [ ] Return a contextual error when `Cancel` is called before initialization.
- [ ] Add cancellation tests.
	- [ ] Test `Cancel` sends a cancellation notification with the request ID.
	- [ ] Test `Cancel` includes the cancellation reason.
	- [ ] Test `Cancel` returns a contextual error when the server/session is closed.
- [ ] Add listener lifecycle tests.
	- [ ] Test `StopServer` cancels the listener.
	- [ ] Test `RemoveServer` cancels the listener.
	- [ ] Test `UpdateServer` cancels the previous listener before reconnecting.
	- [ ] Test `Manager.Close` cancels all listeners.

Acceptance criteria:
- [ ] `go test ./internal/mcp` passes with SDK-backed notification forwarding.
- [ ] Existing manager invalidation tests pass or are replaced by equivalent SDK-backed tests.
- [ ] Cancellation behavior is covered by automated tests.
- [ ] Listener goroutines terminate when servers are stopped, removed, updated, or closed.

### Phase 5 — Implement MRTR and elicitation-compatible tool calls

Goal: preserve Koios' `CallToolWithInput` behavior for input-required MCP tool flows.

Overview: map Koios' `inputResponses` and `requestState` fields into the SDK request path, preserve input-required result metadata, and keep handler-level retry behavior unchanged.

Rules:
- Preserve the existing `CallToolWithInput` signature.
- Preserve Koios result fields needed by `mcp.call`.
- Do not drop opaque `requestState`.
- Keep input-required support testable without live external MCP servers.
- Use raw JSON extension fields when the SDK does not expose typed fields.

- [ ] Implement non-nil `inputResponses` support.
	- [ ] Validate that non-empty `inputResponses` contains valid JSON before sending the request.
	- [ ] Include `inputResponses` in the SDK tool call request when provided.
	- [ ] Preserve `inputResponses` as opaque JSON.
	- [ ] Reject malformed `inputResponses` with a contextual error before sending the request.
- [ ] Implement non-nil `requestState` support.
	- [ ] Validate that non-empty `requestState` contains valid JSON before sending the request.
	- [ ] Include `requestState` in the SDK tool call request when provided.
	- [ ] Preserve `requestState` as opaque JSON.
	- [ ] Reject malformed `requestState` with a contextual error before sending the request.
- [ ] Preserve input-required result fields.
	- [ ] Populate `ToolResult.ResultType` when the server returns an input-required result.
	- [ ] Populate `ToolResult.RequestID` when present.
	- [ ] Populate `ToolResult.RequestState` when present.
	- [ ] Populate `ToolResult.Elicitation` when present.
	- [ ] Populate `ToolResult.TTLMs` when present.
	- [ ] Populate `ToolResult.CacheScope` when present.
- [ ] Add MRTR unit tests.
	- [ ] Test a tool call that returns `resultType = "input_required"`.
	- [ ] Test that `requestId` is preserved.
	- [ ] Test that `requestState` is preserved exactly.
	- [ ] Test that `elicitation` is preserved exactly.
	- [ ] Test a follow-up tool call with `inputResponses`.
	- [ ] Test a follow-up tool call with both `inputResponses` and `requestState`.
	- [ ] Test malformed `inputResponses` returns an error without making a tool call.
	- [ ] Test malformed `requestState` returns an error without making a tool call.
- [ ] Add handler integration tests for MRTR preservation.
	- [ ] Test `mcp.call` passes `input_responses` into `Manager.CallToolResultWithInput`.
	- [ ] Test `mcp.call` passes `request_state` into `Manager.CallToolResultWithInput`.
	- [ ] Test returned input-required metadata is visible in the handler response.

Acceptance criteria:
- [ ] `go test ./internal/mcp ./internal/handler` passes with MRTR coverage.
- [ ] Existing `mcp.call` behavior remains compatible with input-required results.
- [ ] Opaque request state and elicitation JSON round-trip unchanged.

### Phase 6 — Switch the default MCP client factory to the SDK adapter

Goal: make all configured MCP servers use the official SDK adapter by default.

Overview: replace the old factory implementation with `NewSDKClient`, keep config validation unchanged, and run the full MCP/handler integration suite against the SDK path.

Rules:
- Do not add a runtime config switch for legacy MCP clients.
- Keep `ValidateServerConfig` behavior unchanged unless tests prove a necessary SDK compatibility adjustment.
- Keep static, extension, and user-managed server merge behavior unchanged.
- Keep runtime tool prefix behavior unchanged.
- Keep `Manager` as the owner of server status, cache state, runtime names, and source-layer merging.

- [ ] Replace `newClient` in `internal/mcp/manager.go`.
	- [ ] Return `NewSDKClient(c)` for all supported transports.
	- [ ] Keep timeout parsing inside `NewSDKClient`.
	- [ ] Remove direct calls to `NewStdioClient` and `NewHTTPClient` from the factory.
- [ ] Update manager startup tests to use the SDK default path.
	- [ ] Test static configured stdio servers connect through `Manager.Start`.
	- [ ] Test static configured HTTP servers connect through `Manager.Start`.
	- [ ] Test extension-provided MCP servers connect through `Manager.Start`.
	- [ ] Test user-managed MCP servers connect through `Manager.AddServer`.
	- [ ] Test disabled servers are skipped.
	- [ ] Test failed servers record `LastError` and do not prevent other servers from starting.
- [ ] Update handler MCP tests to use the SDK default path where practical.
	- [ ] Test `mcp.server.list` includes SDK-connected server status.
	- [ ] Test `mcp.server.test` succeeds against a fake SDK-backed MCP server.
	- [ ] Test `mcp.server.enable` adds a user-managed server to the runtime manager.
	- [ ] Test `mcp.server.disable` removes a user-managed server from the runtime manager.
	- [ ] Test `mcp.search` returns SDK-discovered tools, resources, templates, and prompts.
	- [ ] Test `mcp.call` invokes an SDK-discovered tool.
- [ ] Preserve tool-prefix compatibility.
	- [ ] Test `ToolPrefix` still returns `mcp__<server>__`.
	- [ ] Test `ToolName` still returns `mcp__<server>__<tool>`.
	- [ ] Test `PluginToolPrefix` remains unchanged.
	- [ ] Test `ParseToolName` parses existing prefixed tool names.
	- [ ] Test handler tool definitions expose prefixed SDK-discovered tool names.

Acceptance criteria:
- [ ] `go test ./internal/mcp ./internal/mcpregistry ./internal/handler` passes with the SDK adapter as the default factory.
- [ ] No production code path creates `NewStdioClient` or `NewHTTPClient`.
- [ ] Static, extension, and user-managed MCP server paths all use the SDK adapter.
- [ ] Runtime tool names remain backward-compatible.

### Phase 7 — Remove legacy MCP transport and JSON-RPC implementation

Goal: delete the hand-rolled stdio/http JSON-RPC client code after SDK parity is covered.

Overview: remove obsolete transport files and local JSON-RPC helper code while retaining Koios-facing protocol structs, manager orchestration, validation, and conversion helpers.

Rules:
- Delete all Koios-owned legacy MCP transport, protocol, and handshake compatibility code made obsolete by the official SDK.
- Keep only Koios-facing types that handlers, manager, tests, or registry code still use as stable internal DTOs.
- Keep validation and merge logic owned by Koios.
- Keep tests behavior-focused rather than tied to exact SDK error text.
- Do not keep a legacy fallback client, feature flag, compatibility shim, or duplicate JSON-RPC path after the SDK-backed path is the default.
- Do not keep deprecated MCP protocol branches in Koios code when the official SDK owns protocol-version negotiation.

- [ ] Delete legacy stdio client implementation.
	- [ ] Remove `internal/mcp/stdio.go`.
	- [ ] Remove tests that only verify legacy pending-map or scanner internals.
	- [ ] Replace removed legacy-specific tests with SDK behavior tests when coverage would otherwise be lost.
- [ ] Delete legacy HTTP client implementation.
	- [ ] Remove `internal/mcp/http.go`.
	- [ ] Remove tests that only verify legacy raw HTTP JSON-RPC internals.
	- [ ] Keep or replace tests for configured headers, timeouts, and tool calls.
- [ ] Trim obsolete JSON-RPC helpers from `internal/mcp/client.go`.
	- [ ] Remove `rpcRequest` if no longer used.
	- [ ] Remove `rpcResponse` if no longer used.
	- [ ] Remove `rpcError` if no longer used.
	- [ ] Remove legacy initialize/discover parameter structs if no longer used.
	- [ ] Keep Koios-facing MCP asset/result structs.
	- [ ] Keep protocol constants that are still shown in status or tests.
- [ ] Remove Koios-owned legacy handshake and protocol compatibility shims.
	- [ ] Remove `legacyProtocolVersion` if it is no longer referenced by SDK-backed code.
	- [ ] Remove legacy `initialize` parameter/result helpers if SDK negotiation owns initialization.
	- [ ] Remove Koios-owned `server/discover` fallback logic if SDK negotiation owns discovery and initialization.
	- [ ] Remove `notifications/initialized` compatibility notification construction if SDK initialization owns it.
	- [ ] Remove method-not-found fallback handling that existed only to switch between legacy and modern handshakes.
	- [ ] Remove tests that assert Koios' old legacy-handshake fallback behavior.
	- [ ] Replace removed fallback tests with SDK negotiation behavior tests where coverage is still needed.
- [ ] Remove unreachable legacy helper functions.
	- [ ] Remove old encode/decode helpers that only supported legacy clients.
	- [ ] Remove old method-not-found handling if the SDK adapter no longer needs it.
	- [ ] Remove old HTTP header helper code only after SDK header tests pass.
	- [ ] Remove old pagination helpers only after SDK pagination tests pass.
	- [ ] Remove raw JSON-RPC request/response plumbing that only existed for the old local client implementation.
- [ ] Remove compatibility references from public-facing MCP docs and comments.
	- [ ] Remove comments saying Koios prefers `server/discover` and falls back to legacy `initialize` when that behavior is no longer Koios-owned.
	- [ ] Update package comments to describe the official SDK as the protocol compatibility layer.
	- [ ] Keep validation text that rejects deprecated `sse` transport.
- [ ] Update package documentation.
	- [ ] Update the `internal/mcp` package comment to say Koios uses the official MCP Go SDK for client transport/protocol handling.
	- [ ] Keep documentation of Koios-specific manager responsibilities.
- [ ] Run compile-focused cleanup.
	- [ ] Remove unused imports from all modified files.
	- [ ] Remove unused constants, structs, and helper functions.
	- [ ] Run `gofmt` on modified Go files.
	- [ ] Run `gopls check` on each modified Go file.

Acceptance criteria:
- [ ] No legacy stdio or HTTP MCP client implementation remains.
- [ ] No Koios-owned legacy MCP handshake fallback remains outside the official SDK.
- [ ] No legacy MCP client feature flag or fallback factory remains.
- [ ] No unused JSON-RPC helper structs remain in `internal/mcp`.
- [ ] No production code path constructs or references `NewStdioClient`, `NewHTTPClient`, legacy initialize helpers, or local raw JSON-RPC request/response helpers.
- [ ] `go test ./internal/mcp ./internal/mcpregistry ./internal/handler` passes.
- [ ] `gopls check` passes for every modified Go file.

### Phase 8 — Full integration validation and regression coverage

Goal: prove the SDK migration is complete across Koios' MCP-facing runtime surfaces.

Overview: run the broad test suite, add regression coverage for all migrated behavior, and ensure no unrelated runtime behavior changed.

Rules:
- Treat MCP SDK migration as complete only when broad tests pass.
- Fix regressions caused by the SDK migration before closing the checklist.
- Do not fix unrelated failing tests as part of this migration unless they block compilation of changed packages.
- Do not change Monaco API request or response payloads as part of this MCP migration.
- Keep all validation automated through Go tests, `gopls check`, and compile-time checks.

- [ ] Add regression tests for MCP manager lifecycle.
	- [ ] Test `Manager.Start` connects multiple servers concurrently.
	- [ ] Test one failed MCP server does not prevent another server from connecting.
	- [ ] Test `EnsureServer` connects an initially disconnected configured server.
	- [ ] Test `StopServer` resets connection state and clears caches.
	- [ ] Test `UpdateServer` replaces configuration and reconnects when enabled.
	- [ ] Test `RemoveServer` deletes runtime state and closes resources.
	- [ ] Test `Close` shuts down all connected SDK clients.
- [ ] Add regression tests for user-managed MCP registry integration.
	- [ ] Test registry records convert to `MCPServerConfig` values accepted by `ValidateServerConfig`.
	- [ ] Test `MergeServerConfigs` preserves static, extension, and user-managed source behavior.
	- [ ] Test duplicate runtime names are rejected.
	- [ ] Test user-managed disabled servers do not connect until enabled.
- [ ] Add regression tests for handler-visible MCP tools.
	- [ ] Test SDK-discovered tools appear in `ToolDefinitionsForRun`.
	- [ ] Test hidden MCP tools do not appear in agent-visible definitions.
	- [ ] Test hidden MCP tools remain callable by full runtime name for internal callers.
	- [ ] Test tool mutation classification remains unchanged for MCP tool names.
	- [ ] Test MCP tool execution records runtime-managed tool results when applicable.
- [ ] Add regression tests for MCP search and asset surfaces.
	- [ ] Test `mcp.search` finds tools by name.
	- [ ] Test `mcp.search` finds tools by description.
	- [ ] Test `mcp.search` finds resources by URI.
	- [ ] Test `mcp.search` finds resource templates by URI template.
	- [ ] Test `mcp.search` finds prompts by name.
	- [ ] Test `mcp.search` respects the limit argument.
- [ ] Run targeted package tests.
	- [ ] Run `go test ./internal/mcp`.
	- [ ] Run `go test ./internal/mcpregistry`.
	- [ ] Run `go test ./internal/handler`.
	- [ ] Run `go test ./internal/agent`.
- [ ] Run full repository tests.
	- [ ] Run `go test ./...`.
	- [ ] Fix SDK-migration regressions found by the full suite.
	- [ ] Leave unrelated pre-existing failures unchanged and documented in final implementation notes.
- [ ] Run Go language-server checks.
	- [ ] Run `gopls check` for each modified file under `internal/mcp`.
	- [ ] Run `gopls check` for each modified file under `internal/handler`.
	- [ ] Run `gopls check` for each modified file under `internal/config` if config code changed.
	- [ ] Run `gopls check` for each modified test file.

Acceptance criteria:
- [ ] Targeted MCP, registry, handler, and agent tests pass.
- [ ] `go test ./...` passes or only unrelated pre-existing failures remain.
- [ ] `gopls check` passes for all modified Go files.
- [ ] The official MCP SDK is the only production MCP transport/protocol implementation used by Koios.
- [ ] Existing Koios MCP config, runtime tool names, registry flows, and handler APIs remain backward-compatible.

# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this repo is

A fork of [One API](https://github.com/songquanpeng/one-api) customized as a lab/team-internal LLM gateway ("aixlab"). The defining difference from upstream: **quota and ratio billing are removed**. Access is no longer gated on balance, and Token consumption is recorded as an observable *usage fact* instead of derived from price. Multi-protocol entry points (`/v1/responses`, `/v1/messages`) were added on top of Chat Completions.

The design contract lives in `docs/` and takes precedence over intuition when they disagree:

- `docs/one-api-modernization-plan.md` — goals, principles, boundaries (§ numbering is referenced throughout the code comments).
- `docs/one-api-modernization-implementation.md` — execution rules and per-file responsibility map.
- `docs/modernization-progress-log.md` — stage-by-stage record, §13 acceptance checklist, known-issue baseline.
- `docs/frontend-modernization-plan.md` / `.md` implementation — the frontend stages (`web/default`).
- `docs/lab-internal-migration.md` — earlier auth/registration spec.

## Commands

The machine's `~/.cache` is not writable, so builds use repo-local caches (`.gocache/`, `.gopath/`), which are gitignored. Prefix `GOCACHE=$PWD/.gocache` if a build fails with a cache permission error.

```bash
# Backend
GOCACHE=$PWD/.gocache go build ./...              # compile everything
GOCACHE=$PWD/.gocache go build -o one-api .       # build the server binary
GOCACHE=$PWD/.gocache go vet ./...
gofmt -l .                                        # must be empty
GOCACHE=$PWD/.gocache go test ./...               # full suite (see known failure below)

# Single package / single test
GOCACHE=$PWD/.gocache go test ./relay/protocol/ -v
GOCACHE=$PWD/.gocache go test ./relay/sse/ -run TestState_ToolArgsGroupedByIndex -v

# Everything except the pre-existing broken package
GOCACHE=$PWD/.gocache go test $(go list ./... | grep -v common/image)

# Run locally (SQLite by default, creates ./one-api.db; first root user is root/123456)
PORT=3000 ./one-api
```

**Known red test:** `common/image` `TestDecode/Decode:jpeg` fails with `image: unknown format` and then panics on a nil pointer. This is a pre-existing upstream defect recorded in the progress log's baseline (§0.3) — do not fold a fix into an unrelated commit.

```bash
# Frontend (web/default) — must run from that directory
cd web/default
npm install
npm start                    # dev server on :3001, proxies /api and /v1 to localhost:3000
CI=false npm run build       # -> web/build/default, which main.go embeds via //go:embed
npm test                     # react-scripts test (jest)
```

`npm run build` uses `rm -rf` / `mv`, so it needs a Unix-like shell.

```bash
# End-to-end smoke test: builds the binary, boots a mock upstream, asserts quota-free
# relay + usage_records + protocol endpoints. RESULT line reports pass/fail counts.
bash scripts/smoke/run_smoke.sh

# Migration matrix against a real DB engine (needs the container from the script's docs):
ENGINE=postgres bash scripts/smoke/migration_matrix.sh
ENGINE=mysql    bash scripts/smoke/migration_matrix.sh
```

## Architecture

### Startup (`main.go`)

`common.Init()` (flags/env) → logger → `model.InitDB()` → `model.InitLogDB()` → root account → Redis → `model.InitOptionMap()` → optional in-memory channel cache + sync goroutines → `router.SetRouter` → `gin.Run`. `config.IsMasterNode` gates migrations: non-master nodes skip `migrateDB()` and run no DDL.

### Relay request pipeline

`router/relay.go` puts `/v1/*` behind `RelayPanicRecover → TokenAuth → Distribute`. `Distribute` (in `middleware/distributor.go`) picks the channel via `model.CacheGetRandomSatisfiedChannel`, then `SetupContextForSelectedChannel` writes channel type/id, base URL, model mapping, system prompt, the upstream `Authorization` header, and the parsed `ChannelConfig` into the gin context. `relay/meta.GetByContext` reads that back into `*meta.Meta`, which is the single carrier threaded through the whole relay.

`controller.Relay` dispatches on `relaymode.GetByPath` through `relayHelper`:

- text/embeddings/audio/moderations → `relay/controller/text.go:RelayTextHelper`
- `/v1/responses` → `RelayResponsesHelper`, `/v1/messages` → `RelayMessagesHelper` (both via `relayProtocolHelper`)
- images → `RelayImageHelper`, proxy → `RelayProxyHelper`

The two protocol entry points share `relayProtocolHelper`, which branches three ways before parsing anything: an upstream protocol that cannot serve this client protocol → named 400; the **same** upstream protocol → `relayPassthrough` (the client's own bytes, never through `entry.Parse`); a **different** one → `upstreamCodecFor` picks a codec, and only then does `entry.Parse` run. The branch order matters — the capability answer does not depend on the parsed form, and the passthrough branch must be reached with the client's body untouched.

`Relay` also owns retry: on a retryable `bizErr` it re-selects a different channel, re-seeds `c.Request.Body` from a buffered copy, and re-enters `relayHelper`. Error paths emit metrics and may auto-disable the channel via `monitor`. Channel selection is filtered by client protocol — a group may mix upstream protocols, and a channel that cannot serve the client's protocol is never selected (so a 400 from a mismatched channel does not need to be retried: it cannot happen).

Both the chat path and the protocol path end by writing the usage fact (`recordUsage` → `usagelog.Default().RecordOnce`) and the legacy consumption log. These run in goroutines after the response is written.

### Adaptors — the upstream extension point

`relay/adaptor/interface.go` defines the 9-method `Adaptor`. `relay.GetAdaptor(apiType)` maps `relay/apitype` → implementation. One `Adaptor` per upstream family covers many channel types; e.g. `relay/adaptor/openai/adaptor.go` switches on `meta.ChannelType` inside `GetRequestURL` to serve Azure, Minimax, Doubao, Novita, BaiduV2, AliBailian, GeminiOpenAICompatible, etc. **To add an upstream, implement `Adaptor` and register it in both `relay/adaptor.go:GetAdaptor` and `relay/apitype/define.go`.**

`ConvertRequest` translates a normalized `model.GeneralOpenAIRequest` into the upstream shape; `DoResponse` parses the upstream reply back into `*model.Usage` plus an error. `getRequestBody` in `text.go` has a fast path that forwards the raw client body untouched when no conversion is needed — it is skipped when the model name is mapped, the channel is Baichuan, a forced system prompt exists, `ENFORCE_INCLUDE_USAGE` is on, or a per-model override mutated the request.

One adaptor family has a companion package that is *not* an adaptor: `relay/adaptor/openai_responses/` is a sink-agnostic conversion library (chat shape ⇄ Responses native shape) with no `Adaptor` implementation and no gin/meta awareness, so the chat path and the protocol path's codec both drive the same translation. `relay/adaptor/anthropic` plays the same role for Messages, but through its adaptor methods.

### Client protocols vs upstream protocols

There are three of each, and a client protocol may be served by any upstream protocol — a 3×3 matrix. The upstream side is `relay/upstream.Protocol`: `openai-chat`, `openai-responses`, `anthropic-messages`, plus `compatible` (chat-shaped but not passthrough-safe) and a zero value `unsupported`. `Resolve(configured, apiType)` picks it — an explicit `ChannelConfig.upstream_protocol` wins, otherwise it is derived from `apitype`, otherwise `openai-chat`. `CanServe(client, up)` is the capability check that replaced the old hardcoded `meta.APIType == apitype.OpenAI` gate; `unsupported` can still serve `/v1/chat/completions` through its own adaptor and nothing else. Unsupported pairings are refused with `upstream_protocol_unsupported`, never guessed at.

`relayProtocolHelper` then does one of three things (see "Relay request pipeline"): **passthrough** when `client == up` (`relay/controller/protocol_passthrough.go`), or a **codec** translation otherwise (`relay/controller/protocol_codec.go`).

Passthrough forwards the client's bytes and the upstream's bytes, both untouched, so `cache_control`, `thinking`, hosted tool declarations, `previous_response_id` and unknown fields survive byte-for-byte. The two rewrites the gateway still owes the channel are *point patches* on a `map[string]json.RawMessage` — the model name, and the forced system prompt merged into the protocol's own top-level field (`system` for Messages, `instructions` for Responses). Nothing else is re-encoded. A configured override that cannot be applied to a native body is a named refusal, not a silent skip.

Codec translation parses into the `GeneralOpenAIRequest` intermediate and renders back into the client's envelope via `entry.Render` / `entry.NewStream`, which only ever consume normalized forms — a codec's job is to produce `*openai.TextResponse` and `sse.Chunk` from whatever the upstream spoke. Translation is lossy in principle (`GeneralOpenAIRequest` has no slot for `cache_control` or `thinking`), so each conversion is expected to either map a field or refuse it by name. Hosted tools were a third casualty of that sentence until the hosted-tool work gave them a slot and a policy of their own — see "Hosted tools".

Streaming goes through `relay/sse` (`Reader`/`Writer`) and the typed emitters in `relay/protocol/stream_messages.go` / `stream_responses.go`, which the `protocolEntry.NewStream` factory selects. **Unsupported fields are rejected with a named error, never silently dropped.** The one remaining blanket exception is `store=true` on a Responses client, ignored-with-debug-log because clients (e.g. Codex) set it by default; hosted tools, the other former exception, are no longer blanket-dropped.

### Hosted tools

A **hosted tool** is one the *upstream* runs itself: Anthropic's `web_search_20250305` and MCP servers, Responses' `web_search` / `mcp`. The client declares it, the gateway never executes it, and the upstream reports what it did as its own block (`server_tool_use` + `web_search_tool_result`, `mcp_tool_use` + `mcp_tool_result`) or output item (`web_search_call`, `mcp_call`). Chat Completions has no vocabulary for any of it — only the `web_search_options` toggle — which is what makes this a cross-protocol problem rather than a field-mapping one.

`relay/model/hosted.go` holds the neutral form and type-tables and depends on no adaptor, which is what lets `relay/protocol` (which depends only on `relay/model` + `relay/sse`) consult the kind tables without transitively pulling in an adaptor package. Adaptor-aware work lives in `relay/controller/hosted_attach.go` (request direction) and `relay/controller/hosted_bridge.go` (response direction, streaming).

Neutral data rides on the three existing normalized types as `json:"-"` fields — `GeneralOpenAIRequest.Hosted`, `openai.TextResponse.Hosted`, `sse.Chunk.Hosted` — so `Parse` / `RequestBody` / `Render` / `protocolStream` signatures are unchanged and nothing leaks into an upstream body. The guard: `sse.ParseChunk` never populates `Hosted` and `StreamState.Absorb` ignores it, so a hosted stream event can only be written by a codec and never round-trips through JSON.

**Only two kinds map across protocols** — `web_search` and `mcp` (`HostedKindFromResponsesToolType`, `HostedKindFromClaudeToolType`, `HostedClaudeBlockName`, `HostedResponsesItemType`). Every other kind (computer use, bash, text editor, code execution, file search, image generation, web fetch, url context) is `native` and goes through the policy instead. This narrow table is the reference implementation's, deliberately: `code_execution` has no Responses outbound form without a `container_id`, and inventing one is worse than naming the loss.

**The policy is per channel** — `ChannelConfig.tool_loss_policy` inside the existing `channels.config` JSON blob (values `allow` / `safe` / `strict`; empty or absent = `allow`; zero schema change, no `model/migration/` entry). `controller/channel.go` rejects an unknown value on save so a typo cannot silently pick a policy. Semantics mirror new-api's `convmeta.Options.ToolLossPolicy`:

- `allow` (default) — an artifact the target cannot express is **dropped, named in a diagnostic, and the request succeeds**. The client is not told.
- `safe` — the same diagnostics become a **400** (`hosted_tool_loss`) when any carries severity `error`.
- `strict` — a 400 for any diagnostic at all.

**`safe` and `strict` bind the request phase only; the response and stream phases never refuse under any policy.** By then the response is already streaming and the upstream has already done the work, so a refusal would discard a completed search rather than protect anyone.

This is the single recorded exception to the "never silently drop" convention below, and it is bounded on both ends: request-phase losses become a named 400 under `safe`/`strict`, and every phase — `allow` included — writes one operator-visible line per loss through `logHostedLosses` (`logger.SysWarnf`, into `oneapi.log`): `channel %d hosted_tool_loss: phase=%s code=%s path=%s %s`, with `phase` one of `request` / `response` / `stream`. "The client does not know" is deliberate and matches new-api; "the operator does not know" is not.

Diagnostic codes are constants in `relay/model/hosted.go`. Two of them are easy to confuse and are not interchangeable:

- **`web_search_result_continuation_lost` is a request-phase loss on the replay path.** A second turn replays the first turn's hosted history, and the two protocols disagree about what a search record carries: a Responses `web_search_call` has an action but no results slot, and Claude rejects a `server_tool_use` with no matching result block (so `web_search_tool_result_error(unavailable)` is substituted); a Claude `web_search_tool_result`'s results have nowhere to go in a Responses item. Both are named at the point of replay (`relay/adaptor/anthropic/main.go`, `relay/adaptor/openai_responses/request.go`).
- **`web_search_response_unrepresentable` is a response-phase loss.** A Responses `web_search_call` arriving for a Claude client cannot be rendered at all — Claude's result block must carry provider-encrypted results only Anthropic can issue, and a call block without one is not a shape the API accepts — so the whole artifact is dropped and named (`relay/protocol/messages.go:messagesHostedArtifactOf`). The reference implementation drops it here too.

MCP is the artifact that survives both directions; `web_search` survives only where it can be spelled (Claude in the request direction, Responses in both). Smoke cells 16h/16i pin each cell of that matrix.

### Usage fact layer

`model/usage.go` owns the `usage_records` table. `request_id` is UNIQUE and `InsertUsageRecordOnce` makes writes idempotent, so retries and stream-finalize races cannot double-count. It is first-write-wins *except* in one direction: an existing `error` row is overwritten by a later `success`/`partial` row for the same `request_id`, because a retry that succeeded must not leave the attempt's failure as the recorded terminal state. `relay/model/misc.go:NormalizeUsage` is the single normalizer for cache fields and is applied at record time.

The invariant worth remembering: **`0`, `null` and `invalid` are three different things.**

- cache fields present and consistent → `cache_usage_status = reported` (a reported `0` is still `reported`)
- upstream reported nothing → `not_reported` — never "0", never a miss
- negative, or read+write > prompt → `invalid`, with the original values preserved (not clamped)
- `usage_source`: `reported` | `estimated` | `legacy`; historical `used_quota` is never converted into Tokens

Aggregation (`SummarizeUsage`, `SummarizeUsageByDay`, `SummarizeUsageBy`) computes rates from summed numerator/denominator, never as a per-call average. Rate columns in `UsageSummaryData` are pointers so a non-computable metric serializes as `null`, and the frontend renders `null` as `—`. Rate SQL is dialect-branched for SQLite/MySQL/Postgres; group-by dimensions are whitelisted in `usageGroupColumns`.

### Schema migrations

`model/main.go` runs `AutoMigrate` for the **legacy** tables only. Every new table, column or index must be a versioned entry in `model/migration/` (`Migrations` registry) with per-dialect DDL, applied by `migration.Migrate` and bookkept in `schema_migrations`. `migration.Check` is the non-mutating pre-flight; `reportMigrationCheck` logs it at boot. The registry is main-database-only — the separate log DB (`LOG_SQL_DSN`) has no version registry yet.

### Channel configuration

`model.Channel.Config` is a JSON blob parsed into `ChannelConfig`: `proxy_url` (channel-scoped outbound proxy, empty = process default), `upstream_protocol` (the protocol the channel speaks, validated against `relay/upstream`'s name list on save — a typo must be an error, since silently falling back to `openai-chat` would divert the channel onto the translating path and quietly change what happens to hosted tools), `tool_loss_policy` (`allow` / `safe` / `strict`, empty = `allow` — see "Hosted tools"), and `model_configs` (per-model overrides keyed by post-mapping model name). Every field lives inside the existing JSON blob, so none needs a `model/migration/` entry. They are backend/config-only for now — `web/default` has no UI for them. `relay/controller/model_config.go:applyModelConfigOverrides` enforces them with strict "client wins" semantics — a value the client sent is never replaced and a blank admin field is never invented; `context_window` and disallowed input modalities are hard rejections, `max_output_tokens` is fill-in-only. When no override exists the relay keeps its untouched fast path. `controller/channel-fetch-models.go` probes the upstream `/models` endpoint to populate the whitelist (admin-only, persists nothing).

### Frontend (`web/default`)

React 18 + react-scripts + Semantic UI + Recharts. Key conventions:

- `src/routes/routeMeta.js` centralizes title, nav group and required role per route; `PrivateRoute` takes `requiredRole` and falls back with `state.from` preserved.
- `src/services/usage.js` is the only place that decides admin (`/api/usage/*`) vs self (`/api/usage/self/*`) scope. The frontend never derives cost, ratios or hit rates, and keeps nullable metrics as `null` rather than coercing to `0`.
- `src/helpers/api.js` is the shared axios instance; its interceptor shows the error *and* re-rejects, so pages can distinguish 401/403/network failures.
- i18n dictionaries are `src/locales/{zh,en}`; new user-facing strings go in both.

`web/build.sh` builds each theme listed in `web/THEMES` (currently just `default`) into `web/build/<theme>`, which `main.go` embeds. **`web/modern/` is an unbuilt Vite + Tailwind prototype** committed accidentally-ish alongside the channel work: it is not in `web/THEMES`, nothing builds it, and its `vite.config.js` `outDir` is `../build/default` — the same path as the real theme. Do not run its build without intending to overwrite the shipped frontend; treat `web/default` as the current frontend unless told otherwise.

## Project conventions

These come from the implementation docs and are enforced, not stylistic preferences:

- User-facing error messages are in Chinese; code comments, JSON field names and new API docs are in English. API handlers return `gin.H{"success", "message", "data"}`.
- Never reintroduce quota, top-up, model/group ratio, or currency display. Legacy columns (`users.quota`, `tokens.remain_quota`, `logs.quota`, …) are retained read-only for rollback compatibility — do not delete them, and do not let new logic read them.
- Never coerce an unreported or invalid cache field to `0`.
- Never silently drop a tool, image, cache field or stream event — reject with a named error instead. **One recorded exception**: a hosted tool the target protocol cannot express is dropped under the default `tool_loss_policy: allow`, with a named diagnostic on every drop (`logHostedLosses`) and a named 400 instead under `safe`/`strict`. See "Hosted tools" — that exception covers nothing else, and the response and stream phases never refuse at all.
- Never let `AutoMigrate` own a new table.
- Land migration / protocol / UI changes as separate commits so each stage is independently revertible.
- The frontend constraint set (no new UI framework, no state library, no icon library or web font, no framework/build-tool migration, `web/berry` and `web/air` are gone and `web/default` is the only theme) is written out in `docs/frontend-modernization-implementation.md` §2 — read it before touching the frontend.

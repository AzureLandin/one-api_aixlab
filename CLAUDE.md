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

`Relay` also owns retry: on a retryable `bizErr` it re-selects a different channel, re-seeds `c.Request.Body` from a buffered copy, and re-enters `relayHelper`. Error paths emit metrics and may auto-disable the channel via `monitor`.

Both the chat path and the protocol path end by writing the usage fact (`recordUsage` → `usagelog.Default().RecordOnce`) and the legacy consumption log. These run in goroutines after the response is written.

### Adaptors — the upstream extension point

`relay/adaptor/interface.go` defines the 9-method `Adaptor`. `relay.GetAdaptor(apiType)` maps `relay/apitype` → implementation. One `Adaptor` per upstream family covers many channel types; e.g. `relay/adaptor/openai/adaptor.go` switches on `meta.ChannelType` inside `GetRequestURL` to serve Azure, Minimax, Doubao, Novita, BaiduV2, AliBailian, GeminiOpenAICompatible, etc. **To add an upstream, implement `Adaptor` and register it in both `relay/adaptor.go:GetAdaptor` and `relay/apitype/define.go`.**

`ConvertRequest` translates a normalized `model.GeneralOpenAIRequest` into the upstream shape; `DoResponse` parses the upstream reply back into `*model.Usage` plus an error. `getRequestBody` in `text.go` has a fast path that forwards the raw client body untouched when no conversion is needed — it is skipped when the model name is mapped, the channel is Baichuan, a forced system prompt exists, `ENFORCE_INCLUDE_USAGE` is on, or a per-model override mutated the request.

### Client protocols vs upstream protocols

`/v1/responses` (OpenAI Responses) and `/v1/messages` (Anthropic Messages) are **client-facing** protocols. `relay/protocol/*.go` parses them into the same `GeneralOpenAIRequest` intermediate, then `relayProtocolHelper` rewrites the call as a canonical `/v1/chat/completions` request and requires `meta.APIType == apitype.OpenAI` — non-OpenAI-compatible upstreams are rejected with `upstream_protocol_unsupported` rather than guessed at. Responses are rendered back into the client's envelope by `protocol.RenderMessages` / `RenderResponses`.

Streaming goes through `relay/sse` (`Reader`/`Writer`) and the typed emitters in `relay/protocol/stream_messages.go` / `stream_responses.go`, which the `protocolEntry.NewStream` factory selects. **Unsupported fields are rejected with a named error, never silently dropped** — the two documented exceptions are Responses' hosted tools and `store=true`, both dropped-with-debug-log because clients (e.g. Codex) set them by default.

### Usage fact layer

`model/usage.go` owns the `usage_records` table. `request_id` is UNIQUE and `InsertUsageRecordOnce` makes writes idempotent, so retries and stream-finalize races cannot double-count. `relay/model/misc.go:NormalizeUsage` is the single normalizer for cache fields and is applied at record time.

The invariant worth remembering: **`0`, `null` and `invalid` are three different things.**

- cache fields present and consistent → `cache_usage_status = reported` (a reported `0` is still `reported`)
- upstream reported nothing → `not_reported` — never "0", never a miss
- negative, or read+write > prompt → `invalid`, with the original values preserved (not clamped)
- `usage_source`: `reported` | `estimated` | `legacy`; historical `used_quota` is never converted into Tokens

Aggregation (`SummarizeUsage`, `SummarizeUsageByDay`, `SummarizeUsageBy`) computes rates from summed numerator/denominator, never as a per-call average. Rate columns in `UsageSummaryData` are pointers so a non-computable metric serializes as `null`, and the frontend renders `null` as `—`. Rate SQL is dialect-branched for SQLite/MySQL/Postgres; group-by dimensions are whitelisted in `usageGroupColumns`.

### Schema migrations

`model/main.go` runs `AutoMigrate` for the **legacy** tables only. Every new table, column or index must be a versioned entry in `model/migration/` (`Migrations` registry) with per-dialect DDL, applied by `migration.Migrate` and bookkept in `schema_migrations`. `migration.Check` is the non-mutating pre-flight; `reportMigrationCheck` logs it at boot. The registry is main-database-only — the separate log DB (`LOG_SQL_DSN`) has no version registry yet.

### Channel configuration

`model.Channel.Config` is a JSON blob parsed into `ChannelConfig`: `proxy_url` (channel-scoped outbound proxy, empty = process default) and `model_configs` (per-model overrides keyed by post-mapping model name). `relay/controller/model_config.go:applyModelConfigOverrides` enforces them with strict "client wins" semantics — a value the client sent is never replaced and a blank admin field is never invented; `context_window` and disallowed input modalities are hard rejections, `max_output_tokens` is fill-in-only. When no override exists the relay keeps its untouched fast path. `controller/channel-fetch-models.go` probes the upstream `/models` endpoint to populate the whitelist (admin-only, persists nothing).

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
- Never silently drop a tool, image, cache field or stream event — reject with a named error instead.
- Never let `AutoMigrate` own a new table.
- Land migration / protocol / UI changes as separate commits so each stage is independently revertible.
- The frontend constraint set (no new UI framework, no state library, no icon library or web font, no framework/build-tool migration, `web/berry` and `web/air` are gone and `web/default` is the only theme) is written out in `docs/frontend-modernization-implementation.md` §2 — read it before touching the frontend.

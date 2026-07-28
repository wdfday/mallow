# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

A polyglot algorithmic trading system. Evaluates technical-analysis-based buy/sell strategies using event-driven backtesting and live signal generation.

**Stack:** Rust (backtesting/signal engine — `almanac` workspace), Go (api-gateway, identity, helm, hist-data, strategist), Next.js (mallow-client), NATS (message broker), PostgreSQL + Redis, Docker Compose (orchestration).

**Workspace:** Go services in `go.work` are `api-gateway`, `hist-data`, `identity`, `helm`, `pkg`. The `thstrategist/` module has its own `go.mod` and is not in the workspace — it is deployed/built independently. The `mallow-client/` is a separate Next.js project (`pnpm`/`npm`).

```
go.work uses: api-gateway, hist-data, identity, helm, pkg
```

## Deployment Model — Hosted Hub + Self-Hosted Tenancy

The system is designed to run in two complementary modes that share **one identity provider** and the same NATS protocol. A user can stay fully managed, or take execution into their own infrastructure without forking the codebase.

### Hosted Hub (managed)

A central deployment we operate. It supplies:

- **identity** — auth (OAuth2, password, Telegram), JWT signing, JWKS endpoint, the source of truth for users.
- **api-gateway** — single public HTTP/WS edge, JWT enforcement, reverse proxy to all backends.
- **strategist** — Google ADK agentic UI (chat / canvas / Telegram bot).
- **mallow-client** — Next.js web UI (strategies, backtests, dashboards).
- **herald (shared)** — public market data + the backtest engine. Free-tier users run their backtests here.
- **NATS hub** — central message broker. Leaf nodes from self-hosted tenants connect to it for fan-out.

A managed user never runs anything locally — sign in, build strategies, backtest, optionally trade through a managed `helm` instance.

### Self-Hosted Tenancy

A user can run **their own herald + helm pair** in their own infrastructure (home, VPS, colo) while still authenticating through the hosted identity and reaching the UI through the hosted gateway. Typical reasons: keep exchange API keys on-prem, get closer to a specific exchange (lower latency), or use private data feeds.

What lives in a tenancy:

- **herald (tenant)** — own websocket ingestion + ledger + signal engine. Optionally registered to ingest a private symbol list.
- **helm (tenant)** — own broker credentials, account/portfolio state, position log. Receives signals from its herald and executes against the broker.
- **NATS leaf** — connects up to the hosted NATS hub with the user's leaf credentials. Subjects in the user's tenancy stream up to the hub for the UI / strategist to consume.

The boundary is the NATS protocol — see [Service Map](#service-map) below. As long as `helm.helms.*`, `helm.hands.*`, `signals`, `bars.*`, `trade.filled.*`, `helm.events.*` flow with the right `caller_user_id`, both modes are indistinguishable to the UI.

**Identity is always central.** Self-hosted tenants do not run their own identity service — they validate JWTs issued by the hosted identity service via JWKS. The user logs into the hosted gateway, and the tenant's gateway/helm proxies trust the same JWKS.

### Auth Flow (both modes)

```
user → mallow-client (hosted) → api-gateway (hosted)
                                    │
                                    ├─ /api/v1/auth/* → identity (hosted, signs JWT)
                                    │
                                    └─ /api/v1/* (JWT) → backend (hosted OR tenant)
                                                          │
                                                          └─ validates via identity JWKS
```

JWT contains `sub` (user id) and is propagated to all downstream services as `X-User-ID` etc. by the gateway's `InjectUserHeaders` middleware. NATS req/rep payloads carry the same identity as `CallerMeta { caller_user_id, caller_svc }`.

## Service Map

```mermaid
graph LR
  subgraph Hub["Hosted Hub (cloud)"]
    AGW[api-gateway]
    IDN[identity]
    STR[strategist]
    UI[mallow-client]
    HER0[herald — public/shared]
    NATS0[(NATS hub)]
  end

  subgraph T1["Tenancy A (self-hosted)"]
    HER1[herald]
    HLM1[helm]
    NL1[NATS leaf]
    HLM1 --> BRK1[Broker A]
  end

  subgraph T2["Tenancy B (self-hosted)"]
    HER2[herald]
    HLM2[helm]
    NL2[NATS leaf]
    HLM2 --> BRK2[Broker B]
  end

  UI --> AGW
  AGW -->|auth proxy| IDN
  AGW -->|JWKS| IDN
  AGW -->|HTTP proxy| STR
  AGW -->|HTTP proxy| HER0
  AGW -->|HTTP proxy| HLM1
  AGW -->|HTTP proxy| HLM2

  HER1 -->|"bars + signals"| HLM1
  HER2 -->|"bars + signals"| HLM2

  NL1 -. "NATS leaf" .-> NATS0
  NL2 -. "NATS leaf" .-> NATS0
  STR -. "helm req/rep" .-> NATS0
  HLM1 -. "helm.events / trade.filled" .-> NATS0
  HLM2 -. "helm.events / trade.filled" .-> NATS0
  HER0 -. "signals / bars.query" .-> NATS0
```

Helm ↔ herald communication (bars + signals + register / deregister) stays the same as before — purely NATS, payloads defined in `proto/market.proto`. The only thing that changes between hosted and self-hosted is **where the NATS connection terminates**: directly in the hub, or via a leaf node back to the hub.

### Services

| Service | Dir | Module | Where it runs | Role |
| --- | --- | --- | --- | --- |
| **api-gateway** | `api-gateway/` | `gateway` | Hub | Gin HTTP/WS gateway. JWT auth (JWKS), CORS, rate-limit (Redis), reverse proxy to identity / helm / herald / strategist. |
| **identity** | `identity/` | `mallow/identity` | Hub only | Auth (JWT/OAuth2/Telegram), user management, JWKS endpoint, internal blacklist check, broker-sync scheduling, email/Telegram notifications. |
| **helm** | `helm/` | `mallow/helm` | Hub **or** tenant | Trade execution + account ownership. Owns broker connections, accounts, helms (account container), hands (signal-following bots), portfolio + position log, exchange adapters. Replaced the old `investment/` service — broker creds, accounts, transactions, watchlists are now all here. |
| **hist-data** | `hist-data/` | `hist-data` | Hub | Historical data crawler (US stocks + crypto) → Parquet files; Wire DI. |
| **strategist** | `thstrategist/` | `strategist` | Hub | Google ADK multi-agent AI; chat / WebSocket / Telegram; realtime canvas; Gemini/Claude/OpenAI providers. |
| **herald** | `almanac/crates/herald/` | (Rust binary `alm-herald`) | Hub **and** tenant | WebSocket ingestion (Binance, OKX) → Ledger + Registry → NATS; HTTP API (`/api/v1/...`) for live data, backtests, strategies. (No SSE/streaming or watch — live streaming moved to gateway/NATS, indicators to WASM.) |
| **pkg** | `pkg/` | `mallow/pkg` | (lib) | Shared Go utilities (errors, response helpers, telemetry, validation). |
| **mallow-client** | `mallow-client/` | (Next.js + Tailwind + Copilotkit) | Hub | Web UI. Authenticates against the hosted gateway; serves auth/, dashboard/. |

> **Historical changes from earlier revisions:** `orchestrator/` (Go) was replaced by `helm/`; `stream-data` (Go) was removed (herald ingests WebSocket feeds directly); `investment/` was folded into `helm/` (account, broker-connection, transaction, portfolio, watchlist are all owned by helm now).

## Protobuf / Message Schema

Single source of truth: `proto/market.proto`. Generated Go code in `helm/internal/infra/engine/`; Rust via `prost-build` in `alm-core/build.rs`.

### Messages

| Message | Purpose |
| --- | --- |
| `TickMsg` | Raw trade event: `t` (Unix ms), `s` (symbol), `class`, `src`, `p` (price), `v` (volume), `bid`, `ask` |
| `BarMsg` | OHLCV candle: `t`, `s`, `o`, `h`, `l`, `c`, `v` |
| `SignalMsg` | Strategy output: `t`, `s`, `dir` (long/short/exit), `strength` [0–1], optional: `price`, `target_price`, `stop_price`, `is_offset`, `reason`, `atr`, `pattern_kind`, `confidence` |
| `SignalResponse` | Herald → helm envelope: repeated `SignalMsg` + `orch_id` (helm_id) + `bot_id` (hand_id) |
| `RegisterMsg` | Hand registration into herald registry: `bot_id`, `symbol`, `strategy`, `params_json`, `orch_id`, optional expiry/targets/timeframe |
| `DeregisterMsg` | Hand removal: `bot_id` (empty = remove all) |

### NATS Subjects

| Subject | Direction | Purpose |
| --- | --- | --- |
| `bars.{symbol}` | publish | OHLCV bars from herald → helm (BarAggregator → tick router) |
| `signals` | publish | `SignalResponse` from herald → helm (orch_id + bot_id in protobuf payload) |
| `engine.register` / `engine.deregister` / `engine.list` / `engine.ping` / `engine.heartbeat` | req/rep | Hand registration / control against herald registry |
| `engine.ready` | JetStream publish | Herald announces readiness on startup |
| `helm.helms.*` | req/rep | Helm CRUD + control: `list` `get` `update` `enable` `disable` `pause` `resume` `kill` `halt.reset` `portfolio` `positions` `trades` `orders` |
| `helm.hands.*` | req/rep | Hand CRUD + control: `list` `get` `create` `update` `start` `stop` `kill` (no `delete`/`pause`/`resume`/`restart` — hands are kept for review) |
| `helm.accounts.unlinked` | publish | Broker account unlinked → helm auto-deletes Helm |
| `helm.events.{helm_id}` | publish | Helm lifecycle / runtime events (status transitions, errors) |
| `helm.pos.>` | JetStream | Position event log (replayable on restart) |
| `helm.trades.>` | JetStream | Per-trade audit |
| `helm.equity.>` | JetStream | Equity curve snapshots |
| `trade.filled.{account_id}` | JetStream | Fill audit from helm → consumers (UI, strategist) |
| `portfolio.synced.{account_id}` | publish | Portfolio sync notification after equity update |
| `portfolio.>` | JetStream | Portfolio snapshot history (helm-level + hand-level) |
| `user.telegram.linked` / `user.telegram.unlinked` | JetStream durable | Identity binding sync → strategist |
| `signals.query.{symbol}` / `.history` / `.active` | req/rep | Signal queries from strategist |
| `bars.query.{symbol}` / `.latest` / `bars.query.symbols` | req/rep | Bar queries from strategist |
| `mail.send` | publish | Identity → SMTP worker (notification fan-out) |

**Envelopes:** Protobuf (binary) for high-volume pub/sub (`bars.*`, `signals`). JSON `{ok, data, error}` envelopes for req/rep. User-scoped requests carry `CallerMeta { caller_user_id, caller_svc }` injected from the validated JWT — the NATS internal network treats this as trusted.

## Building and Running

### Hub stack (Docker)

```bash
just infra              # nats + postgres + redis + identity (+ cloudflared)
just up                 # full stack: gateway, identity, herald, helm, strategist
just up-mon             # full stack + monitoring (Grafana, Prometheus, Loki, Tempo, Pyroscope)
just logs herald
just down               # stop
just down-v             # stop + drop volumes
```

`deployment/docker-compose.yml` is the source of truth. The root `justfile` wraps it with `docker compose -f deployment/docker-compose.yml`.

### Rust workspace — run from `almanac/`

```bash
cargo build
cargo test
cargo test -p alm-strategy
cargo run -p alm-herald                          # live signal engine + HTTP API
RUST_LOG=herald=debug cargo run -p alm-herald    # with debug logging
cargo watch -x 'run -p alm-herald'               # hot-reload (requires cargo-watch)
cargo run -p alm-engine                          # backtest example (main.rs)
cargo run --bin benchmark                        # quick backtest CLI
cargo run --bin compare                          # bar-by-bar strategy comparison
cargo run --bin tournament                       # strategy tournament on synthetic data
```

Herald HTTP listens at `0.0.0.0:8090` (configurable via `HERALD_HTTP_ADDR`).

### Go services — each has a justfile

```bash
just run                # go run ./cmd/<service>/
just dev                # air (hot reload)
just test               # go test ./...
just up                 # docker compose up -d --build
just down               # docker compose down
just logs               # docker compose logs -f <service>
```

### hist-data

```bash
just wire               # go generate ./cmd/us-data/  (Wire DI regeneration)
just build              # go build -o main ./cmd/us-data/
```

### alm-py (Python bindings)

```bash
cd almanac/crates/alm-py
maturin develop         # editable install into current venv
maturin build --release # build wheel
```

### swagger (identity / helm)

```bash
just swagger            # swag init (generate docs)
just swagger-check      # validate docs are committed
```

### mallow-client

```bash
cd mallow-client
pnpm install            # or npm/yarn
pnpm dev                # next dev --port 5173
pnpm build && pnpm start
```

## Architecture: Rust Almanac Workspace

Crates in `almanac/crates/`:

| Crate | Package | Role |
| --- | --- | --- |
| `core` | `alm-core` | Shared types: `Bar`, `Tick`, `Signal`, `Order`, `Portfolio`, `Trade`, `Timeframe`, events, traits (`Strategy`, `MtfStrategy`, `RiskManager`, `Component`), `RegimeState`, `ExitRules`. Compiles proto via prost-build. |
| `data` | `alm-data` | `BarFeed` trait + CSV/Parquet data loaders (Arrow/Parquet), `BarAggregator`, `RowGroupFeed`, `BarVecFeed`. |
| `indicator` | `alm-indicator` | ~66 stateful incremental indicators (Trend/MA, Momentum, Volume, Channel, Pattern, Viewing, Regime, Risk). |
| `ledger` | `alm-ledger` | Live market-state container: `Ledger` (DashMap of per-symbol indicator state), `IndicatorHandle` (refcounted), warm-set bootstrapping from Parquet on startup. |
| `strategy` | `alm-strategy` | ~80 named strategies + `factory::build_strategy(name, params)` + V1 `ScriptStrategy` (single-TF Rhai) + V2 `MtfScriptStrategy` (multi-TF Rhai) + `catalog`. See `script/SPEC.md`, `script/v1/SPEC.md`, `script/v2/SPEC.md`, `script/USER_GUIDE.md`. |
| `engine` | `alm-engine` | Event-driven engine (`Engine<S,R,B>`); `SyncBus`; `MultiEngine` (multi-symbol time-merge); `MtfEngine` (multi-TF feed merge); `WalkForward`; `backtest::run()`; binaries: `alm-engine`, `benchmark`, `compare`, `tournament`, `try_strategy`. |
| `report` | `alm-report` | `BacktestReport`: Sharpe, Sortino, Calmar, max drawdown, win rate, profit factor, P&L; `BuyHoldBenchmark`; `monte_carlo`; `portfolio_analytics`. |
| `pattern` | `alm-pattern` | Technical pattern detection (bull flag, ascending triangle, etc.). |
| `herald` | `alm-herald` | **Binary**: WebSocket ingestion → `Ledger` → `Registry` → NATS signals; 24h `BarRing`; HTTP API (Axum, port 8090); optional PostgreSQL strategy store. |
| `alm-py` | `alm_py` | PyO3 Python bindings: `run_backtest`, `kalman`, `monte_carlo`, `list_strategies`. |
| `alm-wasm` | `alm_wasm` | WASM bindings for browser-side backtesting. |

**Engine event flow:** `MarketEvent → Strategy → SignalEvent → RiskManager → OrderEvent → SimBroker → FillEvent`

**Herald architecture:**
```text
feed::binance  ─┐
feed::okx      ─┤  mpsc → Handler::run
                ▼
         Ledger::advance ──→ NATS publish bars.{symbol}
                │
          LedgerObserver(s)
                │
          Registry (evaluate bots)
                │
         signal_publisher ──→ NATS "signals"
```

> Live streaming to browsers (bars/signals) is **not** served by herald — it moves to
> the gateway, fed from NATS. Indicators are computed client-side (WASM), so herald
> emits only raw bars + signals, never indicator values.

**Herald HTTP API** — all under `/api/v1/`:

| Group | Routes |
|-------|--------|
| **Health / docs** | `GET /health` · OpenAPI at `/api-doc/openapi.json` (utoipa) |
| **Live** | `GET /api/v1/symbols` (symbol list) · `GET /api/v1/indicators` (indicator-type catalog, static) · `POST /api/v1/data/:source/:symbol` (OHLCV; transparent DuckDB Parquet fallback for historical pages) · `POST /api/v1/data/duckdb` |
| **Backtest** | `GET /api/v1/strategies` · `POST /api/v1/backtest` · `POST /api/v1/backtest/estimate` · `POST /api/v1/backtest/script` · `POST /api/v1/backtest/mtf` (MTF named strategies via `MtfEngine`) |
| **Script** | `POST /api/v1/script/validate` (Monaco-style lint with diagnostics + autocomplete scope) |
| **Strategy store** | `GET\|POST /api/v1/strategy/strategies` · `GET\|PUT\|DELETE /api/v1/strategy/strategies/:id` · `GET /api/v1/strategy/strategies/:id/chain` (version chain) · `GET /api/v1/strategy/my` (user's strategies) |

> **No SSE / no live streaming in herald** (removed 2026-05-31). The `/api/v1/stream/*`
> endpoints, the `/api/v1/watch` admin warm-set, and the `?indicators=true` query on
> `/symbols` were dropped: live bar/signal streaming will be served by the gateway from
> NATS, and indicators are computed client-side in WASM. Herald only publishes raw bars +
> signals to NATS now.

**Strategy versioning (store):** Each strategy version is immutable. `previous_id` parent-pointer links versions like a git commit chain. `POST /backtest/script` always creates a new version via `upsert_strategy`: if `strategy_id` is provided it compares scripts (same → reuse, different → new version with `previous_id = strategy_id`); otherwise deduplicates by `spec_hash` globally.

**DuckDB Parquet fallback (`POST /api/v1/data/:source/:symbol`):** When `candles.before` predates the live ledger window, the handler runs a historical compute pass via DuckDB row-group zone-map filtering on Parquet files. OKX symbols normalized (`BTC-USDT` → `BTCUSDT`) before lookup.

**Rhai script engine:** Two versions sharing one DSL. V1 (`script::v1::ScriptStrategy`) is single-TF and used wherever an `alm_engine::Engine` runs. V2 (`script::v2::MtfScriptStrategy`) implements `MtfStrategy` for `MtfEngine` and supports real multi-feed evaluation with `name_live` / `name_fill` companion variables. See `almanac/crates/strategy/src/script/{SPEC.md,USER_GUIDE.md,v1/SPEC.md,v2/SPEC.md}`.

## Architecture: Go Services

### api-gateway

```text
internal/
├── config/         # Viper config (JWT, NATS, Redis, upstream URLs)
├── handler/        # health, reverse-proxy handlers, swagger
├── middleware/     # CORS, rate-limit (Redis), JWT (JWKS validation + cached blacklist), InjectUserHeaders
├── service/        # IdentityClient (blacklist check), StrategistClient
├── ws/             # WebSocket bridge
└── app/            # Uber FX wiring + router build
```

- JWT validated against identity's JWKS endpoint (`JWT_JWKS_URL`; falls back to `${IDENTITY_URL}/.well-known/jwks.json`). HMAC `JWT_SECRET` is a fallback for local dev.
- `IdentityClient` does a fallback blacklist check via `GET /api/v1/internal/blacklist/check` (auth: `X-Service-Secret`) when Redis is cold.
- `InjectUserHeaders` puts `user_id` / `role` from the validated JWT into request headers so downstream services can trust them without re-parsing the token.

**Route table** (`api-gateway/internal/app/app.go`):

| Gateway path | Upstream | Notes |
|---|---|---|
| `Any /api/v1/auth/*path`              | identity   | public (no JWT) |
| `Any /api/v1/swagger/*path`           | identity   | public |
| `GET /swagger/herald/*any` / `/api-doc/openapi.*` | herald | public |
| `Any /swagger/helm/*path`             | helm       | public |
| `Any /api/v1/helms[/*path]`           | helm       | JWT required |
| `Any /api/v1/hands[/*path]`           | helm       | JWT required |
| `Any /api/v1/accounts[/*path]`        | helm       | JWT required (account, transactions, watchlist live in helm now) |
| `Any /api/v1/broker-connections[/*path]` | helm    | JWT required |
| `Any /api/v1/strategist/*path`        | strategist | JWT required |
| `GET /api/v1/symbols`                 | herald     | JWT required |
| `GET /api/v1/indicators`              | herald     | JWT required |
| `POST /api/v1/data/:source/:symbol`   | herald     | JWT required |
| `POST /api/v1/data/duckdb`            | herald     | JWT required |
| `GET /api/v1/strategies`              | herald     | JWT required |
| `POST /api/v1/backtest` / `/backtest/estimate` / `/backtest/script` | herald | JWT required |
| `POST /api/v1/script/validate`        | herald     | JWT required |
| `Any /api/v1/strategy/*path`          | herald     | JWT required (strategy store) |
| (any other `/api/v1/*`)               | identity (NoRoute) | JWT required — catch-all dispatches to identity for endpoints not explicitly mapped |

### helm

The trade execution service — also owns account / broker-connection / portfolio data (folded in from the old `investment/` service).

```text
helm/
├── cmd/helm/         # main entry + swagger annotations
├── internal/
│   ├── app/          # Uber FX wiring, lifecycle, exchange factory, server
│   ├── config/       # Viper config (API_ADDR, POSTGRES_URL, NATS_URL, SYNC_INTERVAL, …)
│   ├── infra/
│   │   ├── engine/   # SignalClient (NATS protobuf → bars + signals), BarAggregator
│   │   ├── exchange/ # Broker adapters: alpaca, binance, bybit, fbinance (Binance futures), okx
│   │   ├── marketdata/ # alpaca/oanda market-data listeners — separate per-account-credential path, not wired by default (see fleet/market for the live public feed)
│   │   ├── nats/     # NATS + JetStream setup (streams: user.>, signals, trade.filled.>, helm.pos.>, helm.trades.>, helm.equity.>, portfolio.>)
│   │   ├── natsapi/  # NATS req/rep protocol (subjects, CallerMeta, envelopes)
│   │   ├── perflog/  # Per-helm and per-hand performance log (portfolio + trades)
│   │   ├── poslog/   # Position event log: JetStream-backed, crash-resilient (`helm.pos.>`)
│   │   └── postgres/ # PostgreSQL persistence
│   ├── module/
│   │   ├── account/  # Account aggregation (replaces investment/account)
│   │   ├── broker/   # Broker connections — encrypted API key storage, paper flag, status
│   │   ├── helm/     # Helm CRUD + account linking + NATS handler
│   │   └── hand/     # Hand CRUD + lifecycle + NATS handler
│   └── fleet/        # Registry (orchestrator) + actor/ (HelmRuntime, Hand, reconciler) + dispatcher/ (SignalDispatcher)
```

**Core concepts:**
- **Broker Connection** (`module/broker`): user-scoped encrypted credentials for a single exchange account. Statuses: `pending` / `active` / `disconnected` / `error`. Paper-trading flag.
- **Account** (`module/account`): aggregated view of a user's positions / cash / transactions per broker. Surfaces SSE `events` endpoint that streams `trade.filled.{account_id}` for the UI.
- **Helm**: account-level execution container. One Helm per broker account. Owns capital budget, portfolio config, risk circuit-breakers. Auto-created synchronously when a broker connection is activated (same request).
- **Hand** (`fleet/actor.Hand`): autonomous signal-following bot. Owns a script/named strategy, position sizing, exit rules, JetStream poslog. Multiple hands per helm.
- **HelmRuntime** (`fleet/actor.HelmRuntime`): in-memory execution context shared by all hands under one helm (exchange, portfolio, order book, poslog).
- **poslog**: JetStream-backed write-ahead log of position events (`order_placed`, `order_filled`, `order_cancelled`, `position_orphaned`). Replayed on restart by the reconciler.
- **SignalDispatcher** (`fleet/dispatcher`): routes incoming `SignalResponse` from herald to the correct Hand channel by `bot_id`. Depends only on `fleet/actor/core/strategy` (Layer 1) — never on `fleet` or `fleet/actor` — and is wired to `Registry` only at the `app/` composition root via the `SignalSink` interface it defines.
- **Registry** (`fleet.Registry`): in-memory map of `helm_id → HelmRuntime`. `SpawnAll` on startup; `Get` for dispatch. One-directional dependency onto `fleet/actor` — nothing in `fleet/actor` references `Registry`, so `fleet` (the orchestrator) is a thin package around the `actor` engine it owns.
- **market** (`fleet/market`): single source of truth for public exchange data — price, symbol filters/notional, L2 order book. One self-connecting WebSocket per exchange (binance/okx/bybit), sole writer; everything else (HelmRuntime, hands) only reads. Independent of herald/NATS.

**Hand lifecycle states:** `stopped → running → stopped` via start/stop. Plus: `kill` (stop + flatten this hand's positions at exchange) and `release` (stop + emit `position_orphaned` poslog events, leaving positions live at exchange). Hands are **never hard-deleted** — kept for review; there is no delete/pause/resume/restart for a hand (pausing is a helm-level cascade-stop).

**Helm lifecycle states:** `active` / `paused` (user or broker-deactivation; cascade-stops all hands; resume via `POST /resume`) / `halted` (risk circuit-breaker or `/kill`; positions flattened; reset via `POST /halt/reset`) / `error` (exchange rejected credentials 3× consecutive; helm self-pauses; recover via `POST /broker-connections/:id/rotate-key` which auto-resumes) / `disabled` (user called `/disable`; positions flattened, runtime torn down; re-enable via `POST /enable`).

**HTTP API** (Gin, port `API_ADDR` default `localhost:8084`; proxied at `/api/v1/...` by the gateway):
- `GET|PUT /api/v1/helms/:id` · `GET /api/v1/helms`
- `POST /api/v1/helms/:id/{enable,disable,pause,resume,kill}` · `POST /api/v1/helms/:id/halt/reset`
- `GET /api/v1/helms/:id/{portfolio,positions,trades,fills,snapshots,equity,stats,orders}` · `GET /api/v1/helms/:id/orders/history` · `GET /api/v1/helms/:id/events/history` (paged, backward time cursor)
- `GET /api/v1/helms/:id/exchange/{account,price,metrics,ping}` · `POST|GET|DELETE /api/v1/helms/:id/exchange/orders`
- `POST /api/v1/hands` · `GET /api/v1/hands` · `GET|PUT /api/v1/hands/:id` (no DELETE — hands kept for review) · `GET /api/v1/hands/:id/{activity,trades,stats,equity}`
- `POST /api/v1/hands/:id/{start,stop,kill,release}` · `POST /api/v1/hands/:id/allocate-capital`
- `GET /api/v1/accounts` · `GET /api/v1/accounts/:id` (read-only; derived from broker-connections) · `GET /api/v1/accounts/:id/{portfolio,positions,trades,fills,snapshots,equity}`
- broker-connections: `GET /providers` · `POST /api/v1/broker-connections/{okx,binance,alpaca,bybit}` · `GET|PUT|DELETE /api/v1/broker-connections/:id` · `POST /api/v1/broker-connections/:id/{activate,deactivate,test,rotate-key,rebroker}`

> Live streaming (bars, helm/trade/account events) is served by the **gateway WebSocket** `/api/v1/stream`, not by per-service SSE. The old SSE endpoints (`helms/:id/events`, `accounts/:id/{events,stream/*}`) were removed.
- `GET /metrics` (Prometheus) · `GET /health` · `GET /swagger/*`

**NATS API**: same operations exposed via subjects under `helm.helms.*` / `helm.hands.*`. CallerMeta (`caller_user_id`, `caller_svc`) embedded in all user-scoped request payloads.

**Startup sequence (Uber FX lifecycle):**
1. `hydrateRuntimes` — load all helm configs from DB, spawn `HelmRuntime` per row.
2. `hydrateHands` — load persisted hands, wire into service (depends on runtimes being ready).
3. `subscribeSignals` — subscribe NATS `signals` via SignalDispatcher.
4. `startNATSAPI` — subscribe all `helm.*` NATS subjects.
5. `runOrchestrator` — start HTTP server + market-data listener + bar builder + tick router.

### identity

```text
internal/
├── app/            # Uber FX wiring
├── config/         # Viper config
├── infra/          # GORM (Postgres/SQLite), NATS, Redis, Swagger gen
├── middleware/     # CORS, logging, JWT, rate-limit, Telegram auth, ServiceAuth
├── module/
│   ├── auth/       # OAuth2 (Google), password, Telegram, sessions, JWT issuance, blacklist; InternalHandler
│   ├── notification/ # Email + Telegram notifications (consumes `mail.send`)
│   ├── profile/    # User profile
│   └── user/       # User CRUD
├── router/         # Gin routes
├── server/         # HTTP server
├── service/        # Encryption helper (AES-GCM)
├── seed/           # First-run seeds (admin user, etc.)
└── telegram/       # Telegram bot
```

- Publishes `user.telegram.linked` / `user.telegram.unlinked` to NATS JetStream.
- Exposes `/.well-known/jwks.json` for gateway/helm JWT validation.
- `InternalHandler`: service-to-service endpoint protected by `X-Service-Secret`.
- Broker-sync scheduler: triggers periodic refresh of broker connections — see `BROKER_SYNC_*` envs.
- Swagger docs via swaggo/swag.

### hist-data

```text
internal/
├── app/            # App init + Wire DI
├── crawl/          # Core interfaces: job, producer, runner, progress, report
├── model/          # Domain models
├── provider/       # Data source adapters:
│   ├── binance/    # Binance REST API (requires API key)
│   ├── binanceflat/ # Binance Vision CDN flat-files (no API key, ZIP/CSV klines)
│   ├── okx/        # OKX REST API
│   ├── polygon/    # Polygon.io (US stocks)
│   ├── twelvedata/ # TwelveData (US stocks)
│   └── vci/        # VCI (Vietnam)
└── saver/          # Parquet persistence
```

- Wire DI; regenerate with `just wire`.
- Symbols config shared with herald via `deployment/symbols.yaml`.
- `binanceflat`: downloads ZIP archives from `data.binance.vision` CDN — no API key.

### strategist (`thstrategist/`)

```text
thstrategist/internal/
├── app/                # FX wiring, lifecycle, NATS identity sync
├── config/             # Viper config
├── module/
│   ├── domain/         # Pure types (zero internal imports)
│   ├── service/        # ChatService, AgentGateway, RealtimeService
│   ├── repo/           # ConversationRepo — postgres/ and noop/ impls
│   ├── handler/        # Gin handlers: chat.go, telegram.go, realtime.go
│   └── dto/
├── infra/
│   ├── agent/          # ADK agent: root + analyst + commander sub-agents
│   ├── agentruntime/   # ADKGateway (Reply, ResetSession, toMessageParts)
│   ├── llm/            # LLM abstraction (gemini | claude | openai)
│   ├── orchestrator/   # HelmClient — NATS req/rep to helm (subjects: helm.helms.*, helm.hands.*)
│   ├── signal/         # SignalClient interface (NATS req/rep)
│   ├── marketdata/     # MarketDataClient interface (NATS req/rep)
│   ├── simulation/     # BacktestClient — nats | http | grpc | mock transport
│   └── mock/           # Static mock data for dev mode
└── telegram/           # Bot, handlers, format, renderer, identity_bindings
```

- `infra/orchestrator/` is the helm client — NATS subjects match `helm/internal/infra/natsapi`.
- Agent architecture: root → analyst (backtests) or commander (helm/hand/portfolio management).
- `write_artifact` tool pushes markdown to the realtime canvas (WebSocket at `/realtime/ws`).
- Telegram session id = `tg_{chatID}`; ADK sessions are in-memory only.

### mallow-client (`mallow-client/`)

Next.js 14 + Tailwind + shadcn/ui + Copilotkit + Monaco. App router under `app/`:

```text
mallow-client/
├── app/
│   ├── api/         # Route handlers (server-side bridges)
│   ├── auth/        # Auth pages (login, oauth callback)
│   ├── dashboard/   # Strategy builder, backtest UI, helm + hand consoles
│   ├── layout.tsx
│   └── page.tsx
├── components/, hooks/, lib/, service/, styles/, public/
```

Auth: calls hosted gateway `/api/v1/auth/*` (OAuth2 / password / Telegram). Bearer token stored client-side; `service/` wraps fetch calls and injects the JWT.

## Environment Variables

### System-wide

```env
NATS_URL                    # default: nats://localhost:4222
JWT_SECRET                  # HMAC fallback (dev only)
POSTGRES_PASSWORD           # only with --profile storage
REDIS_URL                   # default: redis://localhost:6379
RUST_LOG                    # e.g. herald=info,alm_ledger=info
ENCRYPTION_KEY              # required by helm + identity for AES-GCM encrypting broker creds
```

### herald env

```env
HERALD_HTTP_ADDR            # default: 0.0.0.0:8090
HERALD_TF                   # timeframe: M1 (default), M5, M15, M30, H1, H4, D1, W1
HERALD_SYMBOLS_FILE         # path to symbols.yaml (priority over per-exchange vars)
HERALD_BINANCE_SYMBOLS      # comma-separated (fallback)
HERALD_OKX_SYMBOLS          # comma-separated
HERALD_DATA_DIR             # parquet directory for bootstrap (default: ./data)
HERALD_WARM_BARS            # M1 bars to load per symbol on startup (default: 5000 ≈ 3.5 days, 0 = skip)
HERALD_MAX_BACKTESTS        # concurrency cap for backtest API (default: 4)
HERALD_DATABASE_URL         # postgres://... (empty = in-memory strategy store)
NATS_URL
NATS_USER / NATS_PASS
```

### helm env

```env
API_ADDR                    # default: localhost:8084
POSTGRES_URL                # postgres://... (required)
NATS_URL
ENCRYPTION_KEY              # AES-GCM key for broker credentials
PYROSCOPE_URL               # empty = profiling disabled
SYNC_INTERVAL               # portfolio sync interval (default: 5m)
```

Public market-data streaming (price, symbol filters/notional, L2 order book) is not env-configured — `fleet/market` self-connects one WebSocket per exchange (binance/okx/bybit), derived automatically from which exchanges have live hands (`handservice.SymbolsByExchange()`), independent of herald/NATS. Alpaca/oanda market data remains a separate per-account-credential path (`infra/marketdata/{alpaca,oanda}`), not wired by default.

### api-gateway env

```env
PORT                        # default: 8080
NATS_URL
REDIS_URL
JWT_JWKS_URL                # identity JWKS endpoint (empty → ${IDENTITY_URL}/.well-known/jwks.json)
JWT_SECRET                  # HMAC fallback
JWT_PUBLIC_KEY              # PEM-encoded public key fallback
JWT_ISSUER                  # accepted iss
JWT_JWKS_CACHE_TTL          # default: 5m
IDENTITY_URL                # http://identity:8082
HELM_URL                    # http://helm:8084
HERALD_URL                  # http://herald:8090
STRATEGIST_URL              # http://strategist:8081
SERVICE_SECRET              # for identity InternalHandler calls
RATE_LIMIT_PER_MINUTE       # default: 500
CORS_ORIGINS                # comma-separated list
```

### identity env

```env
SERVER_PORT                 # default: 8082 (8080 is api-gateway's port)
POSTGRES_URL                # default: postgres://mallow:mallow-dev@localhost:5432/identity?sslmode=disable
REDIS_URL
NATS_URL
JWT_SECRET                  # HMAC fallback
JWT_PRIVATE_KEY / JWT_PUBLIC_KEY  # EdDSA (Ed25519) key pair
JWT_KEY_ID                  # default: identity-ed25519-k1
JWT_ISSUER                  # default: http://localhost:8082
JWT_ACCESS_TTL              # default: 24h
JWT_REFRESH_TTL             # default: 168h
COOKIE_SECURE / COOKIE_SAME_SITE / COOKIE_MAX_AGE
ENCRYPTION_KEY              # AES-GCM key for sensitive fields
CORS_ORIGINS                # default: http://localhost:3000
BROKER_SYNC_ENABLED         # default: true
BROKER_SYNC_INTERVAL_MIN    # default: 60
BROKER_SYNC_MAX_CONCURRENT  # default: 5
BROKER_SYNC_TIMEOUT_MIN     # default: 10
SERVICE_SECRET              # X-Service-Secret for /api/v1/internal/* endpoints
SMTP_HOST / SMTP_PORT / SMTP_USERNAME / SMTP_PASSWORD / SMTP_FROM / SMTP_FROM_NAME
MAIL_NATS_SUBJECT           # default: mail.send
MAIL_WORKER_CONCURRENCY     # default: 3
TELEGRAM_BOT_TOKEN
```

### strategist env

```env
PORT                        # default: 8080
NATS_URL
LLM_PROVIDER                # gemini (default) | claude | openai
LLM_MODEL                   # override default model per provider
GOOGLE_API_KEY
ANTHROPIC_API_KEY
OPENAI_API_KEY
DATABASE_URL                # postgres://... (empty = no persistence)
PERSIST_CONVERSATIONS       # true (default) | false
ORCHESTRATOR_URL            # helm URL — default: http://localhost:8084
BACKTEST_TRANSPORT          # nats (default) | http | grpc | mock
BACKTEST_HTTP_PATH          # default: /api/backtest
BACKTEST_TIMEOUT_SEC        # default: 30
TELEGRAM_BOT_TOKEN
TELEGRAM_ALLOWED_CHATS      # comma-separated (empty = allow all)
MOCK                        # true = static data, no external calls
DISABLE_EXTERNAL_SERVICES   # true = MOCK + no Telegram + no DB
LOG_LEVEL                   # debug | info | warn | error
```

## Key Conventions

- NATS subjects: `{class}.{symbol}` for data (e.g. `bars.BTCUSDT`); `{service}.{resource}.{action}` for control (e.g. `helm.hands.start`); event streams namespaced under `helm.events.*`, `helm.pos.*`, `helm.trades.*`, `helm.equity.*`, `portfolio.*`.
- Protobuf (binary) for high-volume pub/sub. JSON `{ok, data, error}` for NATS req/rep. `CallerMeta` (`caller_user_id`, `caller_svc`) embedded in user-scoped requests — populated from JWT by the caller, trusted on the NATS internal network.
- `alm-core` is the Rust lingua franca — all almanac crates share it.
- Go workspace (`go.work`) at root: in-workspace modules can import `mallow/pkg` locally. `thstrategist/` lives outside the workspace and pulls dependencies on its own.
- Each Go service has its own `go.mod` (independently deployable).
- Secrets always in env vars, never in config files or committed `.env` files. Broker API keys are AES-GCM encrypted with `ENCRYPTION_KEY` before being stored.
- `deployment/symbols.yaml` is the single source of truth for live-ingestion symbol lists — shared by herald and hist-data.
- In strategist: `module/domain` has zero internal imports — everything else may depend on it.
- `shared/` in each Go service delegates to `mallow/pkg/shared` (error codes, response helpers).
- Herald strategy store uses PostgreSQL when `HERALD_DATABASE_URL` is set, otherwise in-memory.
- Legacy `DynamicStrategy` (declarative JSON) is deprecated — use the Rhai V1 / V2 script engine (`script/SPEC.md`).
- Herald signals are published to the flat `signals` subject (not `signals.{symbol}`); `orch_id` + `bot_id` are in the protobuf `SignalResponse` payload.
- Hand IDs and Helm IDs are `uuid.UUID` throughout the Go runtime; converted to `.String()` only at protobuf / poslog / NATS boundaries.
- Self-hosted tenants share the hosted identity's JWKS — they do not run their own identity service. The boundary between hosted and tenant is **NATS subjects + JWT validation**, not new protocols.
- Detailed API docs: `docs/herald-api.md` (herald HTTP), `docs/helm-api.md` (helm HTTP + NATS).
- Script engine docs: `almanac/crates/strategy/src/script/{SPEC.md, USER_GUIDE.md, v1/SPEC.md, v2/SPEC.md}`.

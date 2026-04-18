# Investment Service

Portfolio tracking service built on **Event Sourcing** + **CQRS**. Records investment transactions (manual input or broker sync), maintains read-model projections, and serves point-in-time portfolio queries.

## Table of Contents

- [Architecture](#architecture)
- [Event Model](#event-model)
- [Database Schema](#database-schema)
- [Data Flows](#data-flows)
- [NATS Streams](#nats-streams)
- [API Reference](#api-reference)
- [Environment Variables](#environment-variables)
- [Running Locally](#running-locally)

---

## Architecture

```
                    ┌─────────────────────────────────────────────────┐
                    │               investment service                 │
                    │                                                  │
  POST /investment/ │   CommandHandler                                 │
  transactions  ────┼──► HandleRecordTransaction                      │
                    │         │                                        │
  NATS JetStream    │         │  1. Load Portfolio aggregate           │
  investment.       │         │     (replay investment_events)         │
  transactions.> ───┼──►      │  2. portfolio.RecordTransaction()     │
  (pull consumer)   │         │     → Raise(TransactionRecorded{})    │
                    │         │  3. EventStore.Append()               │
  ticks.> ──────────┼──►      │     → INSERT investment_events        │
  (core NATS,       │         │  4. Projectors.Project() [sync]       │
   batch 30s)       │         │     ├─ PositionProjector              │
                    │         │     ├─ TransactionProjector           │
                    │         │     ├─ CashFlowProjector              │
                    │         │     ├─ DerivativeProjector            │
                    │         │     └─ SnapshotProjector              │
                    │         │  5. Publisher.Publish() [best-effort] │
                    │         │     → NATS investment.events.{userID} │
                    │                                                  │
                    │   Read Handlers (GORM queries on projections)    │
  GET /investment/* │   ◄── portfolio_positions                       │
  ─────────────────►│   ◄── portfolio_transactions                    │
                    │   ◄── derivative_positions                      │
                    │   ◄── portfolio_cash_flows                      │
                    │   ◄── portfolio_snapshots                       │
                    │   ◄── investment_watchlist                      │
                    └─────────────────────────────────────────────────┘
```

**Key design decisions:**

| Decision | Choice | Reason |
|----------|--------|--------|
| Write path | Append-only event log | Full audit trail; projections are always rebuildable |
| Read path | GORM queries on projection tables | Simple, fast, no in-memory aggregation |
| Projections | Synchronous, after `EventStore.Append` | No eventual-consistency lag for the API caller |
| Publisher | Best-effort JetStream publish | Event is already committed; publish failure is non-fatal |
| Price updates | Batched 30s flush from `ticks.>` | Avoid per-tick event storm; ticks are high-frequency |
| Broker sync idempotency | Partial unique index on `external_id` + `ON CONFLICT DO NOTHING` | Broker syncs can repeat without double-counting |

---

## Event Model

**Aggregate Root:** `Portfolio` — one per user (`aggregate_id = user_id`)

### Event Types

| EventType | Triggered by | Projectors affected |
|-----------|-------------|---------------------|
| `PortfolioInitialized` | First transaction for a user | — (metadata only) |
| `TransactionRecorded` | `RecordTransaction` command | Position, Transaction, CashFlow, Derivative |
| `AssetPriceUpdated` | Price feed flush | Position (current_price, unrealized_pnl) |
| `PortfolioSnapshotTaken` | `TakeSnapshot` command | Snapshot |

### TransactionRecorded — sub-types

| `type` | Position effect | CashFlow effect |
|--------|----------------|-----------------|
| `buy` | UPSERT position: qty ↑, avg_cost recalculated | — |
| `sell` | UPDATE position: qty ↓, realized_pnl calculated | — |
| `dividend` | total_dividends ↑ | INSERT (flow_type=dividend) |
| `deposit` | — | INSERT (flow_type=deposit) |
| `withdrawal` | — | INSERT (flow_type=withdrawal) |
| `fee` | — | INSERT (flow_type=fee) |
| `derivative_open` | — | INSERT derivative_positions |
| `derivative_close` | — | UPDATE derivative_positions, INSERT CashFlow |

### Event Store row (`investment_events`)

```go
type InvestmentEvent struct {
    ID            uuid.UUID       // uuidv7 PK
    AggregateID   uuid.UUID       // = user_id
    AggregateType string          // always "Portfolio"
    EventType     EventType       // see table above
    EventVersion  int             // payload schema version
    Sequence      int64           // monotonic per aggregate (UNIQUE with aggregate_id)
    Payload       json.RawMessage // event-specific data
    Metadata      json.RawMessage // source, broker, ip, etc.
    OccurredAt    time.Time
}
```

---

## Database Schema

All tables live in the **`investment`** PostgreSQL database.

```
investment_events          ← Source of truth (append-only)
    │
    ├──► portfolio_positions      (spot holdings, UPSERT per user+symbol)
    ├──► portfolio_transactions   (full history)
    ├──► portfolio_cash_flows     (dividends, deposits, withdrawals, fees)
    ├──► derivative_positions     (futures/options/perps/swaps)
    └──► portfolio_snapshots      (daily/weekly/monthly performance)

investment_watchlist       ← Simple CRUD (NOT event-sourced)
```

### Unique Constraints

| Index | Columns | Purpose |
|-------|---------|---------|
| `uidx_investment_events_seq` | `(aggregate_id, sequence)` | Optimistic concurrency / replay order |
| `uidx_portfolio_positions_user_symbol` | `(user_id, symbol)` | One position row per holding |
| `uidx_portfolio_snapshots_user_date_type` | `(user_id, snapshot_date, snapshot_type)` | No duplicate snapshots |
| `uidx_investment_watchlist_user_symbol` | `(user_id, symbol)` | No duplicate watchlist entries |
| `uidx_portfolio_transactions_external_id` | `(external_id) WHERE external_id IS NOT NULL AND external_id != ''` | Broker sync idempotency |

---

## Data Flows

### A — Internal/manual ingestion (system command path)

```
Internal command or NATS message
  { "type":"buy", "symbol":"AAPL", "qty":10, "price":175.5, "currency":"USD", "tx_date":"2026-03-01" }
  → CommandHandler.HandleRecordTransaction
    → Load Portfolio (replay investment_events for this user)
    → portfolio.RecordTransaction() → Raise(TransactionRecorded{})
    → EventStore.Append() → INSERT investment_events (next sequence)
    → PositionProjector  → UPSERT portfolio_positions
    → TransactionProjector → INSERT portfolio_transactions
    → Publisher → investment.events.{userID} (best-effort)
  ← 201 Created
```

### B — Automated fill from orchestrator bot

```
orchestrator detects order fill
  → Publish: investment.transactions.{userID}
    { "type":"buy", "symbol":"BTCUSDT", "qty":0.5, "price":42000, "currency":"USDT",
      "broker":"binance", "external_id":"binance:fill:abc123" }

investment pull consumer (batch=10, maxWait=500ms) receives message
  → consumer.process() → CommandHandler.HandleRecordTransaction
    (same pipeline as A)
  → msg.Ack()
```

### C — Broker connection sync (identity service → investment service)

```
User triggers sync (or scheduler runs):
  identity SyncService.SyncBrokerConnection()
    ├─ syncAccountBalance() → UPDATE accounts.current_balance (stays in identity)
    │
    ├─ syncPositionsToInvestment()          [if connection.SyncAssets]
    │    BrokerClient.GetPositions()
    │    for each position with qty > 0:
    │      Publish: investment.transactions.{userID}
    │        { "type":"buy", "external_id":"{broker_type}:pos:{symbol}", "source":"sync", ... }
    │
    └─ syncTransactionsToInvestment()       [if connection.SyncTransactions]
         BrokerClient.GetTransactions(last 30 days)
         for each transaction:
           Publish: investment.transactions.{userID}
             { "type":"{normalized}", "external_id":"{broker_type}:tx:{ext_id}", "source":"sync", ... }

investment pull consumer picks up messages → same pipeline as B
  Idempotency: TransactionProjector uses ON CONFLICT DO NOTHING on external_id
```

### D — Price update (ticks → batched flush)

```
stream-data publishes: ticks.BTCUSDT { "symbol":"BTCUSDT", "price":43500 }

PriceFeedSubscriber.onTick() → batch.pending["BTCUSDT"] = 43500  (in-memory)

Every 30 seconds — PriceFeedSubscriber.flush():
  SELECT user_id, symbol FROM portfolio_positions
  WHERE symbol IN (batched symbols) AND status = 'active'
  for each (user_id, symbol):
    CommandHandler.HandleUpdatePrice()
      → Raise(AssetPriceUpdated{})
      → EventStore.Append()
      → PositionProjector: UPDATE current_price, current_value, unrealized_pnl
```

### E — Daily snapshot (internal/system trigger)

```
00:00 UTC — scheduler invokes internal snapshot command
  → CommandHandler.HandleTakeSnapshot()
    → Raise(PortfolioSnapshotTaken{ total_value, unrealized_pnl, allocation, ... })
    → EventStore.Append()
    → SnapshotProjector → INSERT portfolio_snapshots (UPSERT on conflict)
```

---

## NATS Streams

### INVESTMENT_TRANSACTIONS (incoming)

| Property | Value |
|----------|-------|
| Stream name | `INVESTMENT_TRANSACTIONS` |
| Subjects | `investment.transactions.>` |
| Storage | File |
| MaxAge | 7 days |
| Consumer | `investment-svc` (durable pull, batch=10, maxWait=500ms) |
| MaxDeliver | 3 |
| AckWait | 30s |

**Producers:** orchestrator bots, identity SyncService

### INVESTMENT_EVENTS (outgoing / audit)

| Property | Value |
|----------|-------|
| Stream name | `INVESTMENT_EVENTS` |
| Subjects | `investment.events.>` |
| Storage | File |
| MaxAge | 30 days |

**Producer:** investment service (after EventStore.Append). Useful for downstream consumers (e.g. gateway WebSocket push, analytics).

### ticks.> (price feed)

Core NATS (no persistence). Published by `stream-data`. Consumed by `PriceFeedSubscriber` — batched, no JetStream overhead.

---

## API Reference

All public routes are prefixed `/api/v1/investment` and require the `X-User-ID` header (set by the gateway after JWT validation).

Public API policy:
- `broker-connections` is the primary write surface for end users.
- `accounts` and `portfolio/*` are read-only projections.
- `watchlist` remains user-managed CRUD.
- Transaction recording and snapshot triggering are internal workflows, not public REST endpoints.

### Broker Connections

```
GET /api/v1/investment/broker-connections/providers
  Response: available broker provider metadata

POST /api/v1/investment/broker-connections/{provider}
  Providers: ssi|okx|binance|alpaca|bybit
  Response: 201 Created

GET /api/v1/investment/broker-connections
GET /api/v1/investment/broker-connections/:id
PUT /api/v1/investment/broker-connections/:id
DELETE /api/v1/investment/broker-connections/:id
POST /api/v1/investment/broker-connections/:id/activate
POST /api/v1/investment/broker-connections/:id/deactivate
POST /api/v1/investment/broker-connections/:id/refresh-token
POST /api/v1/investment/broker-connections/:id/test
POST /api/v1/investment/broker-connections/:id/sync
```

### Accounts

```
GET /api/v1/investment/accounts
  Response: broker-backed accounts discovered and maintained by sync

GET /api/v1/investment/accounts/:id
  Response: single account projection
```

### Positions

```
GET /api/v1/investment/positions
  Query: status=active|closed  (default: active)
  Response: [{ symbol, name, asset_type, quantity, avg_cost, current_price, unrealized_pnl, portfolio_weight, ... }]

GET /api/v1/investment/positions/:symbol
  Response: single PortfolioPosition
```

### Transactions

```
GET /api/v1/investment/transactions
  Query: symbol=AAPL  type=buy|sell|dividend  limit=50  offset=0
  Response: [{ id, symbol, tx_type, quantity, price, amount, fees, currency, tx_date, broker, source, ... }]
```

### Derivatives

```
GET /api/v1/investment/derivatives
  Query: status=open|closed  (default: open)
  Response: [{ id, symbol, underlying, instr_type, side, quantity, entry_price, current_price, unrealized_pnl, ... }]
```

### Cash Flows

```
GET /api/v1/investment/cash-flows
  Query: flow_type=dividend|deposit|withdrawal|fee  limit=50  offset=0
  Response: [{ id, flow_type, amount, currency, symbol, description, occurred_at }]
```

### Snapshots

```
GET /api/v1/investment/snapshots
  Query: snapshot_type=daily|weekly|monthly|manual  limit=90  offset=0
  Response: [{ id, snapshot_date, snapshot_type, total_value, unrealized_pnl, realized_pnl,
               total_return_pct, day_change_pct, spot_allocation, metrics, ... }]
```

### Watchlist

```
GET /api/v1/investment/watchlist
  Response: [{ id, symbol, name, asset_type, target_price, notes, added_at }]

POST /api/v1/investment/watchlist
  Body: { "symbol": "NVDA", "name": "NVIDIA", "asset_type": "stock", "target_price": 900, "notes": "" }
  Response: 201 Created

DELETE /api/v1/investment/watchlist/:symbol
  Response: 204 No Content
```

### Health

```
GET /health
  Response: { "status": "ok", "service": "investment" }
```

---

## Environment Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `PORT` | `8083` | HTTP listen port |
| `GIN_MODE` | `debug` | Gin mode (`debug`/`release`) |
| `POSTGRES_DSN` | `host=localhost user=mallow password=mallow-dev dbname=investment sslmode=disable` | PostgreSQL connection string |
| `NATS_URL` | `nats://localhost:4222` | NATS server URL |
| `CORS_ORIGINS` | `http://localhost:3000` | Comma-separated allowed origins |

---

## Running Locally

### Prerequisites

- PostgreSQL 18 with `investment` database created
- NATS server with JetStream enabled

```bash
# Start infra
cd deployment && docker compose up -d postgres nats

# Create investment database (if not done via init.sql)
psql -U mallow -c "CREATE DATABASE investment;"
psql -U mallow -c "GRANT ALL PRIVILEGES ON DATABASE investment TO mallow;"
```

### Build & run

```bash
cd investment
go build ./...
go run ./cmd/investment/
```

### Read API smoke test

```bash
TOKEN="your-jwt-token"

curl http://localhost:8083/api/v1/investment/positions \
  -H "Authorization: Bearer $TOKEN" \
  -H "X-User-ID: 00000000-0000-0000-0000-000000000001" \
  -H "Content-Type: application/json"
```

### Inspect the event store

```bash
psql -U mallow -d investment -c "
  SELECT sequence, event_type, payload->>'type' AS tx_type, occurred_at
  FROM investment_events
  ORDER BY sequence;"
```

### Inspect projections

```bash
psql -U mallow -d investment -c "
  SELECT symbol, quantity, current_price, unrealized_pnl, portfolio_weight
  FROM portfolio_positions
  WHERE status = 'active'
  ORDER BY portfolio_weight DESC;"
```

### Docker

```bash
cd deployment
docker compose up -d investment
docker compose logs -f investment
```

---

## Source Layout

```
investment/
├── cmd/investment/main.go           FX application entry point
├── go.mod                           module: mallow/investment, Go 1.26
├── Dockerfile
└── internal/
    ├── config/config.go             env-var config (Port, DSN, NATS, CORS)
    ├── infra/
    │   ├── database/gorm.go         AutoMigrate 7 tables + unique indexes
    │   ├── nats/nats.go             JetStream stream setup
    │   └── module.go                FX infra module
    │
    ├── module/
    │   ├── portfolio/               Event Sourcing core
    │   │   ├── event/               Domain event types + InvestmentEvent row
    │   │   ├── store/               EventStore interface + GORM implementation
    │   │   ├── aggregate/           Portfolio aggregate (Raise/Replay/ClearUncommitted)
    │   │   ├── command/             Commands + CommandHandler (load→apply→append→project→publish)
    │   │   ├── projection/          5 projectors + Runner
    │   │   ├── publisher/           EventPublisher interface + JetStream impl
    │   │   └── consumer/            JetStream pull consumer (investment.transactions.>)
    │   │
    │   ├── price_feed/subscriber.go ticks.> batch updater (30s flush)
    │   ├── position/                Read model — spot holdings
    │   ├── transaction/             Read model — transaction history
    │   ├── derivative/              Read model — futures/options positions
    │   ├── cash_flow/               Read model — dividends/deposits/withdrawals/fees
    │   ├── snapshot/                Read model — portfolio performance snapshots
    │   └── watchlist/               Simple CRUD — tracked symbols
    │
    ├── api/router/router.go         Gin routes + X-User-ID middleware
    └── server/server.go             HTTP server lifecycle
```

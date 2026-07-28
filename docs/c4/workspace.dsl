workspace "Mallow" "Polyglot algorithmic trading system — event-driven backtesting and live signal generation" {

    !identifiers hierarchical

    model {
        trader = person "Trader" "Builds strategies, backtests, and runs live trading via the web UI or Telegram"

        exchange = softwareSystem "Exchange APIs" "Alpaca / Binance / OKX / Bybit / OANDA — order placement, fills, market data" "External"
        google = softwareSystem "Google OAuth2" "Login" "External"
        telegramApi = softwareSystem "Telegram Bot API" "Account linking, notifications, bot chat" "External"

        mallow = softwareSystem "Mallow" "Event-driven backtesting + live signal generation + signal-following trade execution" {
            webUI = container "mallow-client" "Strategy builder, backtest UI, helm/hand consoles" "Next.js" "Web Browser"
            gateway = container "api-gateway" "JWT auth (JWKS), CORS, rate-limit, reverse proxy, realtime WS bridge" "Go / Gin"
            identity = container "identity" "Auth (JWT/OAuth2/Telegram), JWKS endpoint, user mgmt, broker-sync scheduling" "Go"
            helm = container "helm" "Trade execution: signal routing, order placement, position tracking, poslog WAL" "Go"
            herald = container "herald" "WebSocket ingestion, ledger, signal engine, backtest API" "Rust"
            histData = container "hist-data" "Historical data crawler -> Parquet files" "Go"
            nats = container "NATS + JetStream" "Message broker: pub/sub (bars, signals) + durable streams (fills, positions, equity)" "NATS" "Database"
            postgres = container "PostgreSQL" "One logical database per service: identity, helm, herald" "PostgreSQL" "Database"
            redis = container "Redis" "Sessions, rate-limit counters, token blacklist cache" "Redis" "Database"
        }

        # ── Actors ──────────────────────────────────────────────────────────
        trader -> mallow.webUI "Uses" "HTTPS"
        trader -> telegramApi "Chats with"
        telegramApi -> mallow.identity "Webhook"

        # ── Edge ────────────────────────────────────────────────────────────
        mallow.webUI -> mallow.gateway "REST / WS" "HTTPS"
        mallow.gateway -> mallow.identity "Auth proxy + JWKS validation"
        mallow.gateway -> mallow.helm "HTTP proxy" "/api/v1/helms, /hands, /accounts, /broker-connections"
        mallow.gateway -> mallow.herald "HTTP proxy" "/api/v1/symbols, /data, /backtest, /strategy, /script"
        mallow.gateway -> mallow.nats "Realtime WS fan-out" "bars + scoped account events"
        mallow.gateway -> mallow.redis "Rate-limit counters"

        # ── Signal path ─────────────────────────────────────────────────────
        mallow.herald -> mallow.nats "Publish" "bars.{symbol}, signals"
        mallow.nats -> mallow.helm "Push" "SignalResponse protobuf"
        mallow.helm -> mallow.nats "Register / deregister / heartbeat" "engine.*"

        # ── Execution ────────────────────────────────────────────────────────
        mallow.helm -> exchange "REST + WebSocket" "place order, cancel, fills, price"
        mallow.helm -> mallow.nats "Publish" "trade.filled.{account_id}, helm.pos.>, helm.events.>"

        # ── Identity ─────────────────────────────────────────────────────────
        mallow.identity -> google "OAuth2 code flow"
        mallow.identity -> telegramApi "Bot API"
        mallow.identity -> mallow.nats "Publish" "mail.send, user.telegram.linked/unlinked"
        mallow.identity -> mallow.redis "Session cache, blacklist"

        # ── Persistence ──────────────────────────────────────────────────────
        mallow.identity -> mallow.postgres "Read/write" "identity DB"
        mallow.helm -> mallow.postgres "Read/write" "helm DB"
        mallow.herald -> mallow.postgres "Read/write (optional)" "herald DB, HERALD_DATABASE_URL"

        # ── Data pipeline ────────────────────────────────────────────────────
        mallow.histData -> mallow.herald "Shares Parquet dir + symbols.yaml" "warm-set bootstrap"
    }

    views {
        systemContext mallow "SystemContext" {
            include *
            autoLayout
            description "Mallow as a single system, its user, and the external systems it talks to."
        }

        container mallow "Containers" {
            include *
            autoLayout
            description "All deployable containers inside Mallow, per CLAUDE.md's Service Map. thstrategist/ is deliberately omitted — zero commits in git history as of 2026-07-28, never actually built despite being documented as a planned Hub service."
        }

        styles {
            element "Software System" {
                background #1168bd
                color #ffffff
            }
            element "External" {
                background #999999
                color #ffffff
            }
            element "Person" {
                background #08427b
                color #ffffff
                shape Person
            }
            element "Container" {
                background #438dd5
                color #ffffff
            }
            element "Database" {
                shape Cylinder
            }
            element "Web Browser" {
                shape WebBrowser
            }
        }
    }
}

-- Helm database schema.
-- Source of truth for dev: docker volume reset + docker compose up recreates from here.
-- Production: GORM AutoMigrate runs on every service start.
--
-- Tables owned by helm:
--   broker_connections — encrypted exchange credentials per user
--   accounts           — broker sub-accounts (spot / futures / unified); 1 account → 1 helm
--   helms              — runtime container per account
--   hands              — autonomous signal-following bots within a helm
--
-- Design:
--   - Positions NOT stored here — live state built from poslog (NATS JetStream)
--   - Equity & trade history NOT stored here — stored in NATS JetStream HELM_SNAPSHOTS stream

-- ── broker_connections ────────────────────────────────────────────────────────

CREATE TABLE IF NOT EXISTS broker_connections (
    id                      UUID         PRIMARY KEY DEFAULT uuidv7(),
    user_id                 UUID         NOT NULL,
    broker_type             VARCHAR(20)  NOT NULL,                       -- okx | binance | alpaca | bybit
    broker_name             VARCHAR(100) NOT NULL,
    status                  VARCHAR(20)  NOT NULL DEFAULT 'pending',    -- pending | active | disconnected | error
    api_key                 TEXT,                                        -- encrypted
    api_secret              TEXT,                                        -- encrypted
    passphrase              TEXT,                                        -- OKX only; encrypted
    is_paper                BOOL         NOT NULL DEFAULT false,
    external_account_id     VARCHAR(100),
    external_account_number VARCHAR(100),
    external_account_name   VARCHAR(255),
    notes                   TEXT,
    created_at              TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    updated_at              TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    deleted_at              TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_broker_connections_user_id ON broker_connections(user_id);
CREATE INDEX IF NOT EXISTS idx_broker_connections_type    ON broker_connections(broker_type);
CREATE INDEX IF NOT EXISTS idx_broker_connections_deleted ON broker_connections(deleted_at);

-- ── accounts ──────────────────────────────────────────────────────────────────
-- Broker sub-accounts only: spot | futures_usdm | futures_coinm | unified | options
-- One account → one helm (enforced by helms.account_id UNIQUE).

CREATE TABLE IF NOT EXISTS accounts (
    id                   UUID          PRIMARY KEY DEFAULT uuidv7(),
    user_id              UUID          NOT NULL,
    account_name         VARCHAR(255)  NOT NULL,
    account_type         VARCHAR(50)   NOT NULL,                        -- spot | futures_usdm | futures_coinm | unified | options
    institution_name     VARCHAR(255),
    currency             VARCHAR(3)    NOT NULL DEFAULT 'USD',
    is_active            BOOL          NOT NULL DEFAULT true,
    include_in_net_worth BOOL          NOT NULL DEFAULT true,
    is_auto_sync         BOOL          NOT NULL DEFAULT false,
    last_synced_at       TIMESTAMPTZ,
    sync_status          VARCHAR(20),
    sync_error_message   TEXT,
    broker_connection_id UUID          REFERENCES broker_connections(id) ON DELETE SET NULL,
    created_at           TIMESTAMPTZ   NOT NULL DEFAULT NOW(),
    updated_at           TIMESTAMPTZ   NOT NULL DEFAULT NOW(),
    deleted_at           TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_accounts_user_id_created ON accounts(user_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_accounts_broker          ON accounts(broker_connection_id);
CREATE INDEX IF NOT EXISTS idx_accounts_deleted         ON accounts(deleted_at);

-- ── helms ─────────────────────────────────────────────────────────────────────

CREATE TABLE IF NOT EXISTS helms (
    id               UUID            PRIMARY KEY,
    user_id          UUID            NOT NULL,                -- → identity.users.id
    account_id       UUID            NOT NULL UNIQUE REFERENCES accounts(id),
    name             TEXT            NOT NULL,
    broker_type      TEXT            NOT NULL DEFAULT '',     -- alpaca | binance | okx | bybit
    account_type     TEXT            NOT NULL DEFAULT '',     -- spot | futures_usdm | futures_coinm | unified
    -- RiskConfig: account-level guards (all opt-in, 0 = disabled): max_positions,
    -- daily_loss_limit_pct, max_drawdown_pct, max_gross_exposure_pct. PortfolioConfig was
    -- folded in here — there is no separate portfolio_config column.
    risk_config      JSONB           NOT NULL DEFAULT '{}',
    status           TEXT            NOT NULL DEFAULT 'active'
                     CHECK (status IN ('active', 'paused', 'halted', 'disabled')),
    last_synced_at   TIMESTAMPTZ,                             -- last successful REST sync
    created_at       TIMESTAMPTZ     NOT NULL DEFAULT NOW(),
    updated_at       TIMESTAMPTZ     NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_helms_user_id    ON helms(user_id);
CREATE INDEX IF NOT EXISTS idx_helms_account_id ON helms(account_id);

-- ── hands ─────────────────────────────────────────────────────────────────────

CREATE TABLE IF NOT EXISTS hands (
    id       UUID    PRIMARY KEY,   -- app-generated UUIDv7 (hand repo GenerateID)
    helm_id  UUID    NOT NULL REFERENCES helms(id) ON DELETE CASCADE,
    name     TEXT    NOT NULL,
    type     TEXT    NOT NULL DEFAULT 'signal_follower'
             CHECK (type IN ('signal_follower', 'manual', 'dca', 'grid')),
    market   TEXT    NOT NULL DEFAULT 'spot'
             CHECK (market IN ('spot', 'futures')),
    symbols  JSONB   NOT NULL DEFAULT '[]',  -- []string
    strategy JSONB   NOT NULL DEFAULT '{}',  -- StrategySpec
    position JSONB   NOT NULL DEFAULT '{}',  -- PositionConfig: per-trade sizing only
    risk     JSONB   NOT NULL DEFAULT '{}',  -- HandGuardConfig: per-hand edge-degradation circuit breaker (json key kept as "risk")
    futures  JSONB,                          -- FuturesConfig; NULL when market = 'spot'
    -- Capital budget: first-class column so it is queryable and aggregatable.
    -- Zero = hand draws from full helm equity without isolation.
    allocated_capital  NUMERIC(20,8) NOT NULL DEFAULT 0,
    -- Signal staleness gate: max age in seconds before a signal is discarded.
    -- 0 = default (10 s); negative = disable check.
    signal_ttl_sec     INTEGER       NOT NULL DEFAULT 0,
    -- Entry order type and limit-order lifecycle fields.
    order_type         TEXT          NOT NULL DEFAULT 'market'
                       CHECK (order_type IN ('market', 'limit')),
    limit_timeout_sec  INTEGER       NOT NULL DEFAULT 0,
    limit_fallback     TEXT          NOT NULL DEFAULT 'cancel'
                       CHECK (limit_fallback IN ('cancel', 'market')),
    status   TEXT    NOT NULL DEFAULT 'stopped'
             CHECK (status IN ('running', 'stopped', 'paused', 'killed', 'released')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_hands_helm_id ON hands(helm_id);
CREATE INDEX IF NOT EXISTS idx_hands_status  ON hands(status);



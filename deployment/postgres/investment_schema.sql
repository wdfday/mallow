-- Investment database schema.
-- Run manually: psql -U mallow -d investment -f investment_schema.sql

CREATE EXTENSION IF NOT EXISTS citext;
CREATE EXTENSION IF NOT EXISTS "uuid-ossp";

-- ── Event store ───────────────────────────────────────────────────────────────

CREATE TABLE IF NOT EXISTS investment_events (
    id             UUID         PRIMARY KEY DEFAULT uuidv7(),
    aggregate_id   UUID         NOT NULL,
    aggregate_type VARCHAR(50)  NOT NULL DEFAULT 'Account',
    event_type     VARCHAR(100) NOT NULL,
    event_version  INT          NOT NULL DEFAULT 1,
    sequence       BIGINT       NOT NULL,
    payload        JSONB        NOT NULL,
    metadata       JSONB,
    occurred_at    TIMESTAMP    NOT NULL DEFAULT NOW()
);

CREATE INDEX        IF NOT EXISTS idx_investment_events_aggregate  ON investment_events(aggregate_id);
CREATE INDEX        IF NOT EXISTS idx_investment_events_type       ON investment_events(event_type);
CREATE UNIQUE INDEX IF NOT EXISTS uidx_investment_events_seq       ON investment_events(aggregate_id, sequence);

CREATE TABLE IF NOT EXISTS aggregate_snapshots (
    id             UUID        PRIMARY KEY DEFAULT uuidv7(),
    aggregate_id   UUID        NOT NULL,
    aggregate_type VARCHAR(50) NOT NULL DEFAULT 'Account',
    sequence       BIGINT      NOT NULL,
    state          JSONB       NOT NULL,
    created_at     TIMESTAMP   NOT NULL DEFAULT NOW()
);

CREATE INDEX        IF NOT EXISTS idx_aggregate_snapshots_aggregate ON aggregate_snapshots(aggregate_id);
CREATE UNIQUE INDEX IF NOT EXISTS uidx_aggregate_snapshots_latest   ON aggregate_snapshots(aggregate_id, sequence);

-- ── Broker connections ────────────────────────────────────────────────────────

CREATE TABLE IF NOT EXISTS broker_connections (
    id                      UUID        PRIMARY KEY DEFAULT uuidv7(),
    user_id                 UUID        NOT NULL,
    broker_type             VARCHAR(20) NOT NULL,
    broker_name             VARCHAR(100) NOT NULL,
    status                  VARCHAR(20) NOT NULL DEFAULT 'pending',
    api_key                 TEXT,
    api_secret              TEXT,
    passphrase              TEXT,
    access_token            TEXT,
    refresh_token           TEXT,
    token_expires_at        TIMESTAMP,
    last_refreshed_at       TIMESTAMP,
    auto_sync               BOOL        NOT NULL DEFAULT true,
    sync_frequency          INT         NOT NULL DEFAULT 60,
    last_sync_at            TIMESTAMP,
    last_sync_status        VARCHAR(20),
    last_sync_error         TEXT,
    total_syncs             INT         NOT NULL DEFAULT 0,
    successful_syncs        INT         NOT NULL DEFAULT 0,
    failed_syncs            INT         NOT NULL DEFAULT 0,
    sync_assets             BOOL        NOT NULL DEFAULT true,
    sync_transactions       BOOL        NOT NULL DEFAULT true,
    sync_prices             BOOL        NOT NULL DEFAULT true,
    sync_balance            BOOL        NOT NULL DEFAULT true,
    external_account_id     VARCHAR(100),
    external_account_number VARCHAR(100),
    external_account_name   VARCHAR(255),
    notes                   TEXT,
    created_at              TIMESTAMP   NOT NULL DEFAULT NOW(),
    updated_at              TIMESTAMP   NOT NULL DEFAULT NOW(),
    deleted_at              TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_broker_connections_user_id   ON broker_connections(user_id);
CREATE INDEX IF NOT EXISTS idx_broker_connections_type      ON broker_connections(broker_type);
CREATE INDEX IF NOT EXISTS idx_broker_connections_deleted   ON broker_connections(deleted_at);

-- ── Accounts ──────────────────────────────────────────────────────────────────

CREATE TABLE IF NOT EXISTS accounts (
    id                      UUID         PRIMARY KEY DEFAULT uuidv7(),
    user_id                 UUID         NOT NULL,
    account_name            VARCHAR(255) NOT NULL,
    account_type            VARCHAR(50)  NOT NULL,
    institution_name        VARCHAR(255),
    current_balance         DECIMAL(15,2) NOT NULL DEFAULT 0,
    available_balance       DECIMAL(15,2),
    currency                VARCHAR(3)   NOT NULL DEFAULT 'VND',
    account_number_masked   VARCHAR(50),
    account_number_encrypted TEXT,
    credit_limit            DECIMAL(15,2),
    is_active               BOOL         NOT NULL DEFAULT true,
    is_primary              BOOL         NOT NULL DEFAULT false,
    include_in_net_worth    BOOL         NOT NULL DEFAULT true,
    is_auto_sync            BOOL         NOT NULL DEFAULT false,
    last_synced_at          TIMESTAMP,
    sync_status             VARCHAR(20),
    sync_error_message      TEXT,
    broker_connection_id    UUID         REFERENCES broker_connections(id) ON DELETE SET NULL,
    created_at              TIMESTAMP    NOT NULL DEFAULT NOW(),
    updated_at              TIMESTAMP    NOT NULL DEFAULT NOW(),
    deleted_at              TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_accounts_user_id_created ON accounts(user_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_accounts_broker          ON accounts(broker_connection_id);
CREATE INDEX IF NOT EXISTS idx_accounts_deleted         ON accounts(deleted_at);

-- ── Positions ─────────────────────────────────────────────────────────────────

CREATE TABLE IF NOT EXISTS portfolio_positions (
    id               UUID          PRIMARY KEY DEFAULT uuidv7(),
    account_id       UUID          NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    user_id          UUID          NOT NULL,
    symbol           VARCHAR(20)   NOT NULL,
    name             VARCHAR(255),
    asset_type       VARCHAR(30),
    asset_class      VARCHAR(50),
    exchange         VARCHAR(50),
    currency         VARCHAR(3)    NOT NULL DEFAULT 'USD',
    quantity         DECIMAL(20,8) NOT NULL DEFAULT 0,
    avg_cost         DECIMAL(15,2) NOT NULL DEFAULT 0,
    total_cost       DECIMAL(15,2) NOT NULL DEFAULT 0,
    current_price    DECIMAL(15,2) NOT NULL DEFAULT 0,
    current_value    DECIMAL(15,2) NOT NULL DEFAULT 0,
    unrealized_pnl   DECIMAL(15,2) NOT NULL DEFAULT 0,
    unrealized_pct   DECIMAL(10,4) NOT NULL DEFAULT 0,
    realized_pnl     DECIMAL(15,2) NOT NULL DEFAULT 0,
    total_dividends  DECIMAL(15,2) NOT NULL DEFAULT 0,
    portfolio_weight DECIMAL(10,4) NOT NULL DEFAULT 0,
    status           VARCHAR(20)   NOT NULL DEFAULT 'active',
    last_seq         BIGINT        NOT NULL DEFAULT 0,
    opened_at        TIMESTAMP,
    closed_at        TIMESTAMP,
    updated_at       TIMESTAMP     NOT NULL DEFAULT NOW()
);

CREATE INDEX        IF NOT EXISTS idx_portfolio_positions_account ON portfolio_positions(account_id);
CREATE INDEX        IF NOT EXISTS idx_portfolio_positions_user    ON portfolio_positions(user_id);
CREATE UNIQUE INDEX IF NOT EXISTS uidx_portfolio_positions_sym    ON portfolio_positions(account_id, symbol);

-- ── Transactions ──────────────────────────────────────────────────────────────

CREATE TABLE IF NOT EXISTS portfolio_transactions (
    id             UUID          PRIMARY KEY DEFAULT uuidv7(),
    account_id     UUID          NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    user_id        UUID          NOT NULL,
    symbol         VARCHAR(20),
    tx_type        VARCHAR(30)   NOT NULL,
    currency       VARCHAR(3)    NOT NULL DEFAULT 'USD',
    quantity       DECIMAL(20,8),
    price          DECIMAL(15,2),
    amount         DECIMAL(15,2) NOT NULL,
    fees           DECIMAL(15,2) NOT NULL DEFAULT 0,
    commission     DECIMAL(15,2) NOT NULL DEFAULT 0,
    tax            DECIMAL(15,2) NOT NULL DEFAULT 0,
    realized_pnl   DECIMAL(15,2),
    broker         VARCHAR(100),
    external_id    VARCHAR(255),
    source         VARCHAR(20),
    bot_id         VARCHAR(100),
    notes          TEXT,
    source_event_id UUID,
    tx_date        TIMESTAMP     NOT NULL,
    created_at     TIMESTAMP     NOT NULL DEFAULT NOW()
);

CREATE INDEX        IF NOT EXISTS idx_portfolio_transactions_account  ON portfolio_transactions(account_id);
CREATE INDEX        IF NOT EXISTS idx_portfolio_transactions_user     ON portfolio_transactions(user_id);
CREATE INDEX        IF NOT EXISTS idx_portfolio_transactions_type     ON portfolio_transactions(tx_type);
CREATE INDEX        IF NOT EXISTS idx_portfolio_transactions_bot      ON portfolio_transactions(bot_id);
CREATE INDEX        IF NOT EXISTS idx_portfolio_transactions_date     ON portfolio_transactions(account_id, tx_date);
CREATE UNIQUE INDEX IF NOT EXISTS uidx_portfolio_transactions_ext_id  ON portfolio_transactions(external_id) WHERE external_id IS NOT NULL AND external_id != '';
CREATE UNIQUE INDEX IF NOT EXISTS uidx_portfolio_transactions_src_evt ON portfolio_transactions(source_event_id) WHERE source_event_id IS NOT NULL;

-- ── Derivatives ───────────────────────────────────────────────────────────────

CREATE TABLE IF NOT EXISTS derivative_positions (
    id                UUID          PRIMARY KEY DEFAULT uuidv7(),
    account_id        UUID          NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    user_id           UUID          NOT NULL,
    symbol            VARCHAR(20)   NOT NULL,
    underlying        VARCHAR(20),
    instrument_type   VARCHAR(20)   NOT NULL,
    side              VARCHAR(10)   NOT NULL,
    currency          VARCHAR(3)    NOT NULL DEFAULT 'USD',
    quantity          DECIMAL(20,8) NOT NULL,
    entry_price       DECIMAL(15,2) NOT NULL,
    current_price     DECIMAL(15,2) NOT NULL DEFAULT 0,
    leverage          DECIMAL(10,2) NOT NULL DEFAULT 1,
    margin_used       DECIMAL(15,2) NOT NULL DEFAULT 0,
    liquidation_price DECIMAL(15,2),
    contract_size     DECIMAL(15,4) NOT NULL DEFAULT 1,
    strike_price      DECIMAL(15,2),
    option_type       VARCHAR(10),
    expiry_date       DATE,
    unrealized_pnl    DECIMAL(15,2) NOT NULL DEFAULT 0,
    realized_pnl      DECIMAL(15,2) NOT NULL DEFAULT 0,
    status            VARCHAR(20)   NOT NULL DEFAULT 'open',
    opened_at         TIMESTAMP     NOT NULL,
    closed_at         TIMESTAMP,
    open_event_id     UUID,
    close_event_id    UUID,
    updated_at        TIMESTAMP     NOT NULL DEFAULT NOW()
);

CREATE INDEX        IF NOT EXISTS idx_derivative_positions_account   ON derivative_positions(account_id);
CREATE INDEX        IF NOT EXISTS idx_derivative_positions_user      ON derivative_positions(user_id);
CREATE UNIQUE INDEX IF NOT EXISTS uidx_derivative_positions_open_evt ON derivative_positions(open_event_id) WHERE open_event_id IS NOT NULL;

-- ── Cash flows ────────────────────────────────────────────────────────────────

CREATE TABLE IF NOT EXISTS portfolio_cash_flows (
    id             UUID          PRIMARY KEY DEFAULT uuidv7(),
    account_id     UUID          NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    user_id        UUID          NOT NULL,
    flow_type      VARCHAR(20)   NOT NULL,
    amount         DECIMAL(15,2) NOT NULL,
    currency       VARCHAR(3)    NOT NULL DEFAULT 'USD',
    symbol         VARCHAR(20),
    description    TEXT,
    source_event_id UUID,
    occurred_at    TIMESTAMP     NOT NULL,
    created_at     TIMESTAMP     NOT NULL DEFAULT NOW()
);

CREATE INDEX        IF NOT EXISTS idx_portfolio_cash_flows_account   ON portfolio_cash_flows(account_id);
CREATE INDEX        IF NOT EXISTS idx_portfolio_cash_flows_date      ON portfolio_cash_flows(account_id, occurred_at);
CREATE INDEX        IF NOT EXISTS idx_portfolio_cash_flows_type      ON portfolio_cash_flows(flow_type);
CREATE UNIQUE INDEX IF NOT EXISTS uidx_portfolio_cash_flows_src_evt  ON portfolio_cash_flows(source_event_id) WHERE source_event_id IS NOT NULL;

-- ── Snapshots ─────────────────────────────────────────────────────────────────

CREATE TABLE IF NOT EXISTS portfolio_snapshots (
    id                   UUID          PRIMARY KEY DEFAULT uuidv7(),
    account_id           UUID          NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    user_id              UUID          NOT NULL,
    snapshot_date        TIMESTAMP     NOT NULL,
    snapshot_type        VARCHAR(20)   NOT NULL,
    total_value          DECIMAL(15,2) NOT NULL,
    cash_balance         DECIMAL(15,2) NOT NULL DEFAULT 0,
    spot_value           DECIMAL(15,2) NOT NULL DEFAULT 0,
    derivative_value     DECIMAL(15,2) NOT NULL DEFAULT 0,
    total_cost           DECIMAL(15,2) NOT NULL,
    unrealized_pnl       DECIMAL(15,2) NOT NULL,
    realized_pnl         DECIMAL(15,2) NOT NULL,
    total_dividends      DECIMAL(15,2) NOT NULL DEFAULT 0,
    total_fees           DECIMAL(15,2) NOT NULL DEFAULT 0,
    total_return         DECIMAL(15,2) NOT NULL,
    total_return_pct     DECIMAL(10,4) NOT NULL,
    day_change           DECIMAL(15,2) NOT NULL DEFAULT 0,
    day_change_pct       DECIMAL(10,4) NOT NULL DEFAULT 0,
    cash_inflow          DECIMAL(15,2) NOT NULL DEFAULT 0,
    cash_outflow         DECIMAL(15,2) NOT NULL DEFAULT 0,
    spot_allocation      JSONB,
    derivative_allocation JSONB,
    sector_allocation    JSONB,
    metrics              JSONB,
    source_event_id      UUID,
    created_at           TIMESTAMP     NOT NULL DEFAULT NOW(),
    updated_at           TIMESTAMP     NOT NULL DEFAULT NOW()
);

CREATE INDEX        IF NOT EXISTS idx_portfolio_snapshots_account   ON portfolio_snapshots(account_id);
CREATE INDEX        IF NOT EXISTS idx_portfolio_snapshots_date      ON portfolio_snapshots(snapshot_date);
CREATE INDEX        IF NOT EXISTS idx_portfolio_snapshots_type      ON portfolio_snapshots(snapshot_type);
CREATE UNIQUE INDEX IF NOT EXISTS uidx_portfolio_snapshots_key      ON portfolio_snapshots(account_id, snapshot_date, snapshot_type);

-- ── Watchlist ─────────────────────────────────────────────────────────────────

CREATE TABLE IF NOT EXISTS investment_watchlist (
    id           UUID         PRIMARY KEY DEFAULT uuidv7(),
    user_id      UUID         NOT NULL,
    symbol       VARCHAR(20)  NOT NULL,
    name         VARCHAR(255),
    asset_type   VARCHAR(30),
    target_price DECIMAL(15,2),
    notes        TEXT,
    added_at     TIMESTAMP    NOT NULL DEFAULT NOW(),
    updated_at   TIMESTAMP    NOT NULL DEFAULT NOW()
);

CREATE INDEX        IF NOT EXISTS idx_investment_watchlist_user   ON investment_watchlist(user_id);
CREATE UNIQUE INDEX IF NOT EXISTS uidx_investment_watchlist_sym   ON investment_watchlist(user_id, symbol);

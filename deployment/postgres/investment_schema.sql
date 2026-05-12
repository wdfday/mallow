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

CREATE INDEX        IF NOT EXISTS idx_investment_events_aggregate ON investment_events(aggregate_id);
CREATE INDEX        IF NOT EXISTS idx_investment_events_type      ON investment_events(event_type);
CREATE UNIQUE INDEX IF NOT EXISTS uidx_investment_events_seq      ON investment_events(aggregate_id, sequence);

-- Aggregate snapshot store (used to avoid full event replay on load)
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
    id                      UUID         PRIMARY KEY DEFAULT uuidv7(),
    user_id                 UUID         NOT NULL,
    broker_type             VARCHAR(20)  NOT NULL,                       -- okx | binance | alpaca | bybit
    broker_name             VARCHAR(100) NOT NULL,
    status                  VARCHAR(20)  NOT NULL DEFAULT 'pending',    -- pending | active | disconnected | error
    api_key                 TEXT,                                        -- encrypted
    api_secret              TEXT,                                        -- encrypted
    passphrase              TEXT,                                        -- OKX only
    auto_sync               BOOL         NOT NULL DEFAULT true,
    sync_frequency          INT          NOT NULL DEFAULT 60,           -- minutes
    last_sync_at            TIMESTAMP,
    last_sync_status        VARCHAR(20),
    last_sync_error         TEXT,
    total_syncs             INT          NOT NULL DEFAULT 0,
    successful_syncs        INT          NOT NULL DEFAULT 0,
    failed_syncs            INT          NOT NULL DEFAULT 0,
    sync_assets             BOOL         NOT NULL DEFAULT true,
    sync_transactions       BOOL         NOT NULL DEFAULT true,
    sync_prices             BOOL         NOT NULL DEFAULT true,
    sync_balance            BOOL         NOT NULL DEFAULT true,
    is_paper                BOOL         NOT NULL DEFAULT false,
    external_account_id     VARCHAR(100),
    external_account_number VARCHAR(100),
    external_account_name   VARCHAR(255),
    notes                   TEXT,
    created_at              TIMESTAMP    NOT NULL DEFAULT NOW(),
    updated_at              TIMESTAMP    NOT NULL DEFAULT NOW(),
    deleted_at              TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_broker_connections_user_id ON broker_connections(user_id);
CREATE INDEX IF NOT EXISTS idx_broker_connections_type    ON broker_connections(broker_type);
CREATE INDEX IF NOT EXISTS idx_broker_connections_deleted ON broker_connections(deleted_at);

-- ── Accounts ──────────────────────────────────────────────────────────────────
-- Broker sub-accounts only: spot | futures_usdm | futures_coinm | unified | options

CREATE TABLE IF NOT EXISTS accounts (
    id                   UUID          PRIMARY KEY DEFAULT uuidv7(),
    user_id              UUID          NOT NULL,
    account_name         VARCHAR(255)  NOT NULL,
    account_type         VARCHAR(50)   NOT NULL,                        -- spot | futures_usdm | futures_coinm | unified | options
    institution_name     VARCHAR(255),
    currency             VARCHAR(3)    NOT NULL DEFAULT 'USD',
    current_balance      DECIMAL(15,2) NOT NULL DEFAULT 0,
    available_balance    DECIMAL(15,2),
    equity               DECIMAL(15,2) NOT NULL DEFAULT 0,              -- futures/unified only
    margin_used          DECIMAL(15,2) NOT NULL DEFAULT 0,              -- futures/unified only
    is_active            BOOL          NOT NULL DEFAULT true,
    include_in_net_worth BOOL          NOT NULL DEFAULT true,
    is_auto_sync         BOOL          NOT NULL DEFAULT false,
    last_synced_at       TIMESTAMP,
    sync_status          VARCHAR(20),                                   -- active | error | paused
    sync_error_message   TEXT,
    broker_connection_id UUID          REFERENCES broker_connections(id) ON DELETE SET NULL,
    created_at           TIMESTAMP     NOT NULL DEFAULT NOW(),
    updated_at           TIMESTAMP     NOT NULL DEFAULT NOW(),
    deleted_at           TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_accounts_user_id_created ON accounts(user_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_accounts_broker          ON accounts(broker_connection_id);
CREATE INDEX IF NOT EXISTS idx_accounts_deleted         ON accounts(deleted_at);

-- ── Positions (read-model) ────────────────────────────────────────────────────

CREATE TABLE IF NOT EXISTS positions (
    id               UUID          PRIMARY KEY DEFAULT uuidv7(),
    account_id       UUID          NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    user_id          UUID          NOT NULL,
    symbol           VARCHAR(20)   NOT NULL,
    name             VARCHAR(255),
    asset_type       VARCHAR(30),                                       -- stock | crypto | etf | forex | commodity
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
    status           VARCHAR(20)   NOT NULL DEFAULT 'active',          -- active | closed
    last_seq         BIGINT        NOT NULL DEFAULT 0,                  -- idempotency guard
    opened_at        TIMESTAMP,
    closed_at        TIMESTAMP,
    updated_at       TIMESTAMP     NOT NULL DEFAULT NOW()
);

CREATE INDEX        IF NOT EXISTS idx_positions_account ON positions(account_id);
CREATE INDEX        IF NOT EXISTS idx_positions_user    ON positions(user_id);
CREATE UNIQUE INDEX IF NOT EXISTS uidx_positions_sym    ON positions(account_id, symbol);

-- ── Trades (read-model) ───────────────────────────────────────────────────────

CREATE TABLE IF NOT EXISTS trades (
    id              UUID          PRIMARY KEY DEFAULT uuidv7(),
    account_id      UUID          NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    user_id         UUID          NOT NULL,
    symbol          VARCHAR(20),
    tx_type         VARCHAR(30)   NOT NULL,                            -- buy|sell|dividend|deposit|withdrawal|fee|derivative_open|derivative_close
    currency        VARCHAR(3)    NOT NULL DEFAULT 'USD',
    quantity        DECIMAL(20,8),
    price           DECIMAL(15,2),
    amount          DECIMAL(15,2) NOT NULL,
    fees            DECIMAL(15,2) NOT NULL DEFAULT 0,
    commission      DECIMAL(15,2) NOT NULL DEFAULT 0,
    tax             DECIMAL(15,2) NOT NULL DEFAULT 0,
    realized_pnl    DECIMAL(15,2),
    external_id     VARCHAR(255),
    source          VARCHAR(20),                                       -- manual | sync
    bot_id          VARCHAR(100),
    notes           TEXT,
    source_event_id UUID,
    tx_date         TIMESTAMP     NOT NULL,
    created_at      TIMESTAMP     NOT NULL DEFAULT NOW()
);

CREATE INDEX        IF NOT EXISTS idx_trades_account  ON trades(account_id);
CREATE INDEX        IF NOT EXISTS idx_trades_user     ON trades(user_id);
CREATE INDEX        IF NOT EXISTS idx_trades_type     ON trades(tx_type);
CREATE INDEX        IF NOT EXISTS idx_trades_bot      ON trades(bot_id);
CREATE INDEX        IF NOT EXISTS idx_trades_date     ON trades(account_id, tx_date);
CREATE UNIQUE INDEX IF NOT EXISTS uidx_trades_ext_id  ON trades(external_id) WHERE external_id IS NOT NULL AND external_id != '';
CREATE UNIQUE INDEX IF NOT EXISTS uidx_trades_src_evt ON trades(source_event_id) WHERE source_event_id IS NOT NULL;

-- ── Cash flows (read-model) ───────────────────────────────────────────────────

CREATE TABLE IF NOT EXISTS cash_flows (
    id              UUID          PRIMARY KEY DEFAULT uuidv7(),
    account_id      UUID          NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    user_id         UUID          NOT NULL,
    flow_type       VARCHAR(20)   NOT NULL,                            -- dividend | deposit | withdrawal | fee
    amount          DECIMAL(15,2) NOT NULL,
    currency        VARCHAR(3)    NOT NULL DEFAULT 'USD',
    symbol          VARCHAR(20),                                       -- set for dividend
    description     TEXT,
    transfer_id     UUID,                                              -- links paired spot↔futures rows (double-entry)
    source_event_id UUID,
    occurred_at     TIMESTAMP     NOT NULL,
    created_at      TIMESTAMP     NOT NULL DEFAULT NOW()
);

CREATE INDEX        IF NOT EXISTS idx_cash_flows_account   ON cash_flows(account_id);
CREATE INDEX        IF NOT EXISTS idx_cash_flows_date      ON cash_flows(account_id, occurred_at);
CREATE INDEX        IF NOT EXISTS idx_cash_flows_type      ON cash_flows(flow_type);
CREATE INDEX        IF NOT EXISTS idx_cash_flows_transfer  ON cash_flows(transfer_id) WHERE transfer_id IS NOT NULL;
CREATE UNIQUE INDEX IF NOT EXISTS uidx_cash_flows_src_evt  ON cash_flows(source_event_id) WHERE source_event_id IS NOT NULL;

-- ── Contract positions / derivatives (read-model) ─────────────────────────────

CREATE TABLE IF NOT EXISTS contract_positions (
    id                UUID          PRIMARY KEY DEFAULT uuidv7(),
    account_id        UUID          NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    user_id           UUID          NOT NULL,
    symbol            VARCHAR(20)   NOT NULL,
    underlying        VARCHAR(20),
    instrument_type   VARCHAR(20)   NOT NULL,                          -- futures | perp | options | swap
    side              VARCHAR(10)   NOT NULL,                          -- long | short
    currency          VARCHAR(3)    NOT NULL DEFAULT 'USD',
    quantity          DECIMAL(20,8) NOT NULL,
    entry_price       DECIMAL(15,2) NOT NULL,
    current_price     DECIMAL(15,2) NOT NULL DEFAULT 0,
    leverage          DECIMAL(10,2) NOT NULL DEFAULT 1,
    margin_used       DECIMAL(15,2) NOT NULL DEFAULT 0,
    liquidation_price DECIMAL(15,2),
    contract_size     DECIMAL(15,4) NOT NULL DEFAULT 1,
    strike_price      DECIMAL(15,2),                                   -- options only
    option_type       VARCHAR(10),                                     -- call | put
    expiry_date       DATE,
    unrealized_pnl    DECIMAL(15,2) NOT NULL DEFAULT 0,
    realized_pnl      DECIMAL(15,2) NOT NULL DEFAULT 0,
    status            VARCHAR(20)   NOT NULL DEFAULT 'active',         -- active | closed
    opened_at         TIMESTAMP     NOT NULL,
    closed_at         TIMESTAMP,
    open_event_id     UUID,
    close_event_id    UUID,
    updated_at        TIMESTAMP     NOT NULL DEFAULT NOW()
);

CREATE INDEX        IF NOT EXISTS idx_contract_positions_account   ON contract_positions(account_id);
CREATE INDEX        IF NOT EXISTS idx_contract_positions_user      ON contract_positions(user_id);
CREATE UNIQUE INDEX IF NOT EXISTS uidx_contract_positions_open_evt ON contract_positions(open_event_id) WHERE open_event_id IS NOT NULL;

-- ── Account snapshots (read-model) ────────────────────────────────────────────

CREATE TABLE IF NOT EXISTS account_snapshots (
    id                   UUID          PRIMARY KEY DEFAULT uuidv7(),
    account_id           UUID          NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    user_id              UUID          NOT NULL,
    snapshot_date        TIMESTAMP     NOT NULL,
    snapshot_type        VARCHAR(20)   NOT NULL,                        -- daily | weekly | monthly | manual
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
    metrics              JSONB,                                         -- {sharpe, sortino, beta, max_drawdown, volatility}
    source_event_id      UUID,
    created_at           TIMESTAMP     NOT NULL DEFAULT NOW(),
    updated_at           TIMESTAMP     NOT NULL DEFAULT NOW()
);

CREATE INDEX        IF NOT EXISTS idx_account_snapshots_account ON account_snapshots(account_id);
CREATE INDEX        IF NOT EXISTS idx_account_snapshots_date    ON account_snapshots(snapshot_date);
CREATE INDEX        IF NOT EXISTS idx_account_snapshots_type    ON account_snapshots(snapshot_type);
CREATE UNIQUE INDEX IF NOT EXISTS uidx_account_snapshots_key    ON account_snapshots(account_id, snapshot_date, snapshot_type);

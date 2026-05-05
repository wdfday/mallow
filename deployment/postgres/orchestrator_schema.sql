-- Helm (formerly orchestrator) database schema.
-- Run manually: psql -U mallow -d orchestrator -f orchestrator_schema.sql
-- Applied automatically by helm on startup.
--
-- Design notes:
--   - credentials NOT stored here — fetched from investment service on startup
--   - positions NOT stored here — log-based via NATS JetStream (stream: HELM_ORDERS)
--   - hands inherit position_config + exit_config from helm; NULL = use helm default

CREATE TABLE IF NOT EXISTS helm (
    id                      TEXT    PRIMARY KEY,
    user_id                 TEXT    NOT NULL,
    account_id              TEXT    NOT NULL UNIQUE,   -- investment broker account ref
    name                    TEXT    NOT NULL,
    exchange                TEXT    NOT NULL           -- 'binance' | 'okx' | 'alpaca'
                            CHECK (exchange IN ('binance', 'okx', 'alpaca', 'oanda', 'bybit')),
    -- global defaults inherited by all hands (overridable per hand)
    default_position_config JSONB   NOT NULL DEFAULT '{}',
    default_exit_config     JSONB,
    -- risk guard applied at helm level across all hands
    risk_config             JSONB   NOT NULL DEFAULT '{}',
    status                  TEXT    NOT NULL DEFAULT 'active'
                            CHECK (status IN ('active', 'paused', 'halted')),
    last_synced_at          BIGINT,
    created_at              BIGINT  NOT NULL,
    updated_at              BIGINT  NOT NULL
);

CREATE TABLE IF NOT EXISTS hands (
    id              TEXT    PRIMARY KEY,
    helm_id         TEXT    NOT NULL REFERENCES helm(id) ON DELETE CASCADE,
    name            TEXT    NOT NULL,
    symbol          TEXT    NOT NULL,
    timeframe       TEXT    NOT NULL DEFAULT 'M1',
    exchange        TEXT    NOT NULL,                  -- can differ from helm (paper vs live)
    -- strategy: mirrors herald StrategySpec
    strategy_name   TEXT    NOT NULL,                  -- 'ema_cross' | 'cel' | 'rhai' | ...
    strategy_spec   JSONB   NOT NULL DEFAULT '{}',
    -- NULL = inherit from helm.default_*
    position_config JSONB,
    exit_config     JSONB,
    status          TEXT    NOT NULL DEFAULT 'stopped'
                    CHECK (status IN ('active', 'paused', 'stopped')),
    created_at      BIGINT  NOT NULL,
    updated_at      BIGINT  NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_helm_user_id     ON helm(user_id);
CREATE INDEX IF NOT EXISTS idx_helm_account_id  ON helm(account_id);
CREATE INDEX IF NOT EXISTS idx_hands_helm_id    ON hands(helm_id);
CREATE INDEX IF NOT EXISTS idx_hands_status     ON hands(status);
CREATE INDEX IF NOT EXISTS idx_hands_symbol     ON hands(symbol);

-- Orchestrator database schema.
-- Run manually: psql -U mallow -d orchestrator -f orchestrator_schema.sql

CREATE EXTENSION IF NOT EXISTS "uuid-ossp";

CREATE TABLE IF NOT EXISTS orchestrators (
    id               UUID             PRIMARY KEY,
    user_id          UUID             NOT NULL,
    account_id       UUID             NOT NULL UNIQUE,
    name             TEXT             NOT NULL,
    capital          DOUBLE PRECISION NOT NULL DEFAULT 0,
    exchange_config  JSONB            NOT NULL DEFAULT '{}',
    portfolio_config JSONB            NOT NULL DEFAULT '{}',
    risk_config      JSONB            NOT NULL DEFAULT '{}',
    enabled          BOOL             NOT NULL DEFAULT false,
    status           TEXT             NOT NULL DEFAULT 'active',
    last_synced_at   TIMESTAMPTZ,
    created_at       TIMESTAMPTZ      NOT NULL DEFAULT NOW(),
    updated_at       TIMESTAMPTZ      NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_orchestrators_user_id ON orchestrators(user_id);

CREATE TABLE IF NOT EXISTS bots (
    id               TEXT        PRIMARY KEY,
    orchestrator_id  UUID        NOT NULL REFERENCES orchestrators(id) ON DELETE CASCADE,
    name             TEXT        NOT NULL,
    type             TEXT        NOT NULL DEFAULT 'signal_follower',
    market           TEXT        NOT NULL DEFAULT 'spot',
    symbols          JSONB       NOT NULL DEFAULT '[]',
    strategy         JSONB       NOT NULL DEFAULT '{}',
    position         JSONB       NOT NULL DEFAULT '{}',
    risk             JSONB       NOT NULL DEFAULT '{}',
    futures          JSONB,
    status           TEXT        NOT NULL DEFAULT 'stopped',
    created_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_bots_orchestrator_id ON bots(orchestrator_id);
CREATE INDEX IF NOT EXISTS idx_bots_status          ON bots(status);

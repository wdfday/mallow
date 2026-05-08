-- Helm database schema.
-- Source of truth for dev: docker volume reset + docker compose up recreates from here.
-- Production uses autoMigrate in helm/internal/infra/postgres/postgres.go.
--
-- Two databases share this schema namespace:
--   helm   — helm/hand config, portfolio config, equity log
--
-- Design:
--   - Exchange credentials NOT stored here — copied from investment on account.linked
--   - Positions NOT stored here — live state built from poslog (NATS JetStream)
--   - hand_equity_log is append-only, dedup via PRIMARY KEY (hand_id, ts)

-- ── helms ─────────────────────────────────────────────────────────────────────

CREATE TABLE IF NOT EXISTS helms (
    id              UUID            PRIMARY KEY,
    user_id         UUID            NOT NULL,                -- → identity.users.id
    account_id      UUID            NOT NULL UNIQUE,         -- → investment.accounts.id
    name            TEXT            NOT NULL,
    capital         DOUBLE PRECISION NOT NULL DEFAULT 0,     -- total capital budget (quote currency)
    exchange_config JSONB           NOT NULL DEFAULT '{}',   -- ExchangeConfig: broker creds
    portfolio       JSONB           NOT NULL DEFAULT '{}',   -- PortfolioConfig: allocation rules
    risk_config     JSONB           NOT NULL DEFAULT '{}',   -- RiskConfig: circuit-breakers
    enabled         BOOLEAN         NOT NULL DEFAULT FALSE,  -- user toggle; gates hand CRUD
    status          TEXT            NOT NULL DEFAULT 'active'
                    CHECK (status IN ('active', 'paused', 'halted')),
    last_synced_at  TIMESTAMPTZ,                             -- last successful REST sync
    created_at      TIMESTAMPTZ     NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ     NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_helms_user_id    ON helms(user_id);
CREATE INDEX IF NOT EXISTS idx_helms_account_id ON helms(account_id);

-- ── hands ─────────────────────────────────────────────────────────────────────

CREATE TABLE IF NOT EXISTS hands (
    id       UUID    PRIMARY KEY,
    helm_id  UUID    NOT NULL REFERENCES helms(id) ON DELETE CASCADE,
    name     TEXT    NOT NULL,
    type     TEXT    NOT NULL DEFAULT 'signal_follower'
             CHECK (type IN ('signal_follower', 'manual', 'dca', 'grid')),
    market   TEXT    NOT NULL DEFAULT 'spot'
             CHECK (market IN ('spot', 'futures')),
    symbols  JSONB   NOT NULL DEFAULT '[]',  -- []string
    strategy JSONB   NOT NULL DEFAULT '{}',  -- StrategySpec
    position JSONB   NOT NULL DEFAULT '{}',  -- PositionConfig: sizing + allocation
    risk     JSONB   NOT NULL DEFAULT '{}',  -- HandRiskConfig: exit rules
    futures  JSONB,                          -- FuturesConfig; NULL when market = 'spot'
    status   TEXT    NOT NULL DEFAULT 'stopped'
             CHECK (status IN ('running', 'stopped', 'paused')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_hands_helm_id ON hands(helm_id);
CREATE INDEX IF NOT EXISTS idx_hands_status  ON hands(status);

-- ── hand_equity_log ───────────────────────────────────────────────────────────
-- Append-only mark-to-market snapshots written after each bar close.
-- Dedup contract: same (hand_id, ts) from multiple processes → ON CONFLICT DO NOTHING.

CREATE TABLE IF NOT EXISTS hand_equity_log (
    hand_id UUID    NOT NULL REFERENCES hands(id) ON DELETE CASCADE,
    ts      BIGINT  NOT NULL,  -- Unix ms; bar close time (dedup key with hand_id)
    equity  FLOAT8  NOT NULL,
    PRIMARY KEY (hand_id, ts)
);

CREATE INDEX IF NOT EXISTS idx_hand_equity_log_hand_ts ON hand_equity_log(hand_id, ts);

//! Schema migration for the herald database.
//!
//! Uses `CREATE TABLE IF NOT EXISTS` / `ADD COLUMN IF NOT EXISTS` DDL so it is
//! safe to re-run on every startup — no migration tool needed.

use anyhow::Result;
use sqlx::PgPool;

const SCHEMA: &str = r#"
CREATE TABLE IF NOT EXISTS strategies (
    id          TEXT    PRIMARY KEY,
    spec_hash   TEXT    UNIQUE,
    name        TEXT    NOT NULL,
    version     INT     NOT NULL DEFAULT 1,
    label       TEXT    NOT NULL,
    kind        TEXT    NOT NULL CHECK (kind IN ('cel', 'rhai')),
    spec        JSONB   NOT NULL,
    exit_config JSONB,
    notes       TEXT,
    created_at  BIGINT  NOT NULL,
    UNIQUE (name, version)
);

CREATE TABLE IF NOT EXISTS backtest_cases (
    id          TEXT    PRIMARY KEY,
    strategy_id TEXT    REFERENCES strategies(id) ON DELETE RESTRICT,
    label       TEXT    NOT NULL,
    symbol      TEXT    NOT NULL,
    timeframe   TEXT,
    from_ms     BIGINT,
    to_ms       BIGINT,
    data_source TEXT,
    capital     JSONB   NOT NULL DEFAULT '{}',
    execution   JSONB   NOT NULL DEFAULT '{}',
    created_at  BIGINT  NOT NULL,
    updated_at  BIGINT  NOT NULL
);

CREATE TABLE IF NOT EXISTS backtest_results (
    id               TEXT    PRIMARY KEY,
    case_id          TEXT    NOT NULL REFERENCES backtest_cases(id) ON DELETE CASCADE,
    status           TEXT    NOT NULL DEFAULT 'pending'
                             CHECK (status IN ('pending', 'running', 'done', 'failed')),
    error            TEXT,
    s3_key           TEXT,
    bar_count        BIGINT,
    started_at       BIGINT,
    finished_at      BIGINT,
    initial_capital  FLOAT8,
    final_equity     FLOAT8,
    total_return_pct FLOAT8,
    cagr_pct         FLOAT8,
    sharpe_ratio     FLOAT8,
    sortino_ratio    FLOAT8,
    calmar_ratio     FLOAT8,
    max_drawdown_pct FLOAT8,
    profit_factor    FLOAT8,
    win_rate_pct     FLOAT8,
    expectancy       FLOAT8,
    total_trades     BIGINT,
    created_at       BIGINT  NOT NULL
);

CREATE TABLE IF NOT EXISTS watch_entries (
    id           TEXT    PRIMARY KEY,
    symbols      JSONB   NOT NULL,
    timeframe    TEXT,
    spec         JSONB   NOT NULL,
    exit_config  JSONB,
    webhook_url  TEXT,
    nats_subject TEXT,
    created_at   BIGINT  NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_strategies_name_version  ON strategies(name, version DESC);
CREATE INDEX IF NOT EXISTS idx_strategies_spec_hash     ON strategies(spec_hash) WHERE spec_hash IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_cases_strategy_id        ON backtest_cases(strategy_id);
CREATE INDEX IF NOT EXISTS idx_cases_created            ON backtest_cases(created_at DESC);
CREATE INDEX IF NOT EXISTS idx_results_case_id          ON backtest_results(case_id);
CREATE INDEX IF NOT EXISTS idx_results_created          ON backtest_results(created_at DESC);
CREATE INDEX IF NOT EXISTS idx_results_pending          ON backtest_results(status) WHERE status IN ('pending', 'running');
CREATE INDEX IF NOT EXISTS idx_watch_created            ON watch_entries(created_at DESC);

-- ── Symbol config (shared by herald + hist-data) ─────────────────────────────

CREATE TABLE IF NOT EXISTS providers (
    id         TEXT    PRIMARY KEY,
    slug       TEXT    NOT NULL UNIQUE,  -- 'binance' | 'okx' | 'alpaca' | 'massive' | 'twelvedata' | 'vci'
    name       TEXT    NOT NULL,
    kind       TEXT    NOT NULL CHECK (kind IN ('exchange', 'data_provider')),
    created_at BIGINT  NOT NULL
);

CREATE TABLE IF NOT EXISTS symbols (
    id            TEXT    PRIMARY KEY,
    provider_id   TEXT    NOT NULL REFERENCES providers(id) ON DELETE RESTRICT,
    symbol        TEXT    NOT NULL,
    asset_class   TEXT    NOT NULL DEFAULT 'crypto',  -- 'crypto' | 'stock' | 'forex'
    live_enabled  BOOL    NOT NULL DEFAULT true,   -- herald WebSocket ingestion
    crawl_enabled BOOL    NOT NULL DEFAULT true,   -- hist-data historical crawl
    created_at    BIGINT  NOT NULL,
    UNIQUE (provider_id, symbol)
);

CREATE TABLE IF NOT EXISTS symbol_frames (
    id             TEXT     PRIMARY KEY,
    symbol_id      TEXT     NOT NULL REFERENCES symbols(id) ON DELETE CASCADE,
    frame          TEXT     NOT NULL,  -- 'M1' | 'H4' | 'D1' ...
    backfill_years INT      NOT NULL DEFAULT 0,  -- 0 = full history
    sink_frames    TEXT[]   NOT NULL DEFAULT '{}',
    UNIQUE (symbol_id, frame)
);

CREATE TABLE IF NOT EXISTS crawl_state (
    symbol_id       TEXT    NOT NULL REFERENCES symbols(id) ON DELETE CASCADE,
    frame           TEXT    NOT NULL,
    last_success_at BIGINT,
    updated_at      BIGINT  NOT NULL,
    PRIMARY KEY (symbol_id, frame)
);

CREATE INDEX IF NOT EXISTS idx_symbols_provider    ON symbols(provider_id);
CREATE INDEX IF NOT EXISTS idx_symbol_frames_sym   ON symbol_frames(symbol_id);
CREATE INDEX IF NOT EXISTS idx_crawl_state_sym     ON crawl_state(symbol_id);

-- ── Seed providers ────────────────────────────────────────────────────────────

INSERT INTO providers (id, slug, name, kind, created_at) VALUES
    ('01000000-0000-7000-8000-000000000001', 'binance',    'Binance',     'exchange',      0),
    ('01000000-0000-7000-8000-000000000002', 'okx',        'OKX',         'exchange',      0),
    ('01000000-0000-7000-8000-000000000003', 'alpaca',     'Alpaca',      'exchange',      0),
    ('01000000-0000-7000-8000-000000000004', 'massive',    'Polygon.io',  'data_provider', 0),
    ('01000000-0000-7000-8000-000000000005', 'twelvedata', 'TwelveData',  'data_provider', 0),
    ('01000000-0000-7000-8000-000000000006', 'vci',        'Vietcap',     'data_provider', 0)
ON CONFLICT (slug) DO NOTHING;

-- ── Seed Binance symbols ──────────────────────────────────────────────────────

INSERT INTO symbols (id, provider_id, symbol, asset_class, live_enabled, crawl_enabled, created_at)
SELECT
    '01000000-0000-7001-8000-' || LPAD(ROW_NUMBER() OVER ()::TEXT, 12, '0'),
    '01000000-0000-7000-8000-000000000001',
    sym, 'crypto', true, true, 0
FROM UNNEST(ARRAY[
    'BTCUSDT','ETHUSDT','BNBUSDT','SOLUSDT','XRPUSDT',
    'ADAUSDT','AVAXUSDT','DOTUSDT','LINKUSDT','MATICUSDT'
]) AS sym
ON CONFLICT (provider_id, symbol) DO NOTHING;

-- ── Seed Binance symbol_frames (M1 → M1/M5/M15/M30/H1/H4, D1) ───────────────

INSERT INTO symbol_frames (id, symbol_id, frame, backfill_years, sink_frames)
SELECT
    '01000000-0000-7011-8000-' || LPAD((ROW_NUMBER() OVER () * 2 - 1)::TEXT, 12, '0'),
    s.id, 'M1', 0, ARRAY['M1','M5','M15','M30','H1','H4']
FROM symbols s JOIN providers p ON p.id = s.provider_id WHERE p.slug = 'binance'
ON CONFLICT (symbol_id, frame) DO NOTHING;

INSERT INTO symbol_frames (id, symbol_id, frame, backfill_years, sink_frames)
SELECT
    '01000000-0000-7011-8000-' || LPAD((ROW_NUMBER() OVER () * 2)::TEXT, 12, '0'),
    s.id, 'D1', 0, ARRAY['D1']
FROM symbols s JOIN providers p ON p.id = s.provider_id WHERE p.slug = 'binance'
ON CONFLICT (symbol_id, frame) DO NOTHING;

-- ── Seed OKX symbols ─────────────────────────────────────────────────────────

INSERT INTO symbols (id, provider_id, symbol, asset_class, live_enabled, crawl_enabled, created_at)
SELECT
    '01000000-0000-7002-8000-' || LPAD(ROW_NUMBER() OVER ()::TEXT, 12, '0'),
    '01000000-0000-7000-8000-000000000002',
    sym, 'crypto', true, true, 0
FROM UNNEST(ARRAY[
    'BTC-USDT','ETH-USDT','BNB-USDT','SOL-USDT','XRP-USDT',
    'ADA-USDT','AVAX-USDT','DOT-USDT','LINK-USDT','POL-USDT'
]) AS sym
ON CONFLICT (provider_id, symbol) DO NOTHING;

-- ── Seed OKX symbol_frames ────────────────────────────────────────────────────

INSERT INTO symbol_frames (id, symbol_id, frame, backfill_years, sink_frames)
SELECT
    '01000000-0000-7012-8000-' || LPAD((ROW_NUMBER() OVER () * 2 - 1)::TEXT, 12, '0'),
    s.id, 'M1', 0, ARRAY['M1','M5','M15','M30','H1','H4']
FROM symbols s JOIN providers p ON p.id = s.provider_id WHERE p.slug = 'okx'
ON CONFLICT (symbol_id, frame) DO NOTHING;

INSERT INTO symbol_frames (id, symbol_id, frame, backfill_years, sink_frames)
SELECT
    '01000000-0000-7012-8000-' || LPAD((ROW_NUMBER() OVER () * 2)::TEXT, 12, '0'),
    s.id, 'D1', 0, ARRAY['D1']
FROM symbols s JOIN providers p ON p.id = s.provider_id WHERE p.slug = 'okx'
ON CONFLICT (symbol_id, frame) DO NOTHING;
"#;

pub async fn run(pool: &PgPool) -> Result<()> {
    sqlx::raw_sql(SCHEMA).execute(pool).await?;
    tracing::info!("herald store: schema migration complete");
    Ok(())
}

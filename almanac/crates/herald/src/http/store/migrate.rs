//! Schema migration for the herald database.
//!
//! Uses `CREATE TABLE IF NOT EXISTS` / `ADD COLUMN IF NOT EXISTS` DDL so it is
//! safe to re-run on every startup — no migration tool needed.

use anyhow::Result;
use sqlx::PgPool;

const SCHEMA: &str = r#"
CREATE TABLE IF NOT EXISTS strategies (
    id         TEXT    PRIMARY KEY,
    name       TEXT    NOT NULL,
    version    INT     NOT NULL DEFAULT 1,
    label      TEXT    NOT NULL,
    kind       TEXT    NOT NULL CHECK (kind IN ('cel', 'rhai')),
    spec       JSONB   NOT NULL,
    notes      TEXT,
    created_at BIGINT  NOT NULL,
    UNIQUE (name, version)
);

CREATE TABLE IF NOT EXISTS backtest_cases (
    id          TEXT    PRIMARY KEY,
    strategy_id TEXT    NOT NULL REFERENCES strategies(id) ON DELETE RESTRICT,
    label       TEXT    NOT NULL,
    symbol      TEXT    NOT NULL,
    timeframe   TEXT,
    from_ms     BIGINT,
    to_ms       BIGINT,
    data_source TEXT,
    capital     JSONB   NOT NULL DEFAULT '{}',
    execution   JSONB   NOT NULL DEFAULT '{}',
    exit_config JSONB,
    created_at  BIGINT  NOT NULL,
    updated_at  BIGINT  NOT NULL
);

CREATE TABLE IF NOT EXISTS backtest_results (
    id               TEXT    PRIMARY KEY,
    case_id          TEXT    NOT NULL REFERENCES backtest_cases(id) ON DELETE CASCADE,
    ran_at           BIGINT  NOT NULL,
    s3_key           TEXT,
    total_return_pct FLOAT8  NOT NULL DEFAULT 0,
    sharpe_ratio     FLOAT8  NOT NULL DEFAULT 0,
    max_drawdown_pct FLOAT8  NOT NULL DEFAULT 0,
    win_rate_pct     FLOAT8  NOT NULL DEFAULT 0,
    total_trades     BIGINT  NOT NULL DEFAULT 0,
    created_at       BIGINT  NOT NULL
);

CREATE TABLE IF NOT EXISTS watch_entries (
    id           TEXT    PRIMARY KEY,
    symbols      JSONB   NOT NULL,
    timeframe    TEXT,
    spec         JSONB   NOT NULL,
    webhook_url  TEXT,
    nats_subject TEXT,
    created_at   BIGINT  NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_strategies_name_version  ON strategies(name, version DESC);
CREATE INDEX IF NOT EXISTS idx_cases_strategy_id        ON backtest_cases(strategy_id);
CREATE INDEX IF NOT EXISTS idx_cases_created            ON backtest_cases(created_at);
CREATE INDEX IF NOT EXISTS idx_results_case_id          ON backtest_results(case_id);
CREATE INDEX IF NOT EXISTS idx_results_ran_at           ON backtest_results(ran_at DESC);
CREATE INDEX IF NOT EXISTS idx_watch_created            ON watch_entries(created_at);
"#;

pub async fn run(pool: &PgPool) -> Result<()> {
    sqlx::raw_sql(SCHEMA).execute(pool).await?;
    tracing::info!("herald store: schema migration complete");
    Ok(())
}

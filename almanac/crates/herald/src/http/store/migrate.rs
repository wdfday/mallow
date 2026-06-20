//! Schema migration for the herald strategy store.
//!
//! Uses `CREATE TABLE IF NOT EXISTS` / `ADD COLUMN IF NOT EXISTS` DDL so it is
//! safe to re-run on every startup — no migration tool needed.
//!
//! Only the `strategies` table is managed here.  Other schema designs that were
//! explored but not yet activated live in `migrate_intent.rs`.

use anyhow::Result;
use sqlx::PgPool;

const SCHEMA: &str = r#"
CREATE TABLE IF NOT EXISTS strategies (
    id          UUID    PRIMARY KEY,
    spec_hash   TEXT,
    name        TEXT    NOT NULL,
    version     INT     NOT NULL DEFAULT 1,
    previous_id UUID    REFERENCES strategies(id),
    label       TEXT    NOT NULL,
    spec        JSONB   NOT NULL,
    notes       TEXT,
    user_id     TEXT,
    created_at  BIGINT  NOT NULL,
    UNIQUE (name, version)
);

CREATE INDEX IF NOT EXISTS idx_strategies_name_version ON strategies(name, version DESC);
"#;

/// One-time migrations that alter existing tables (safe to re-run).
const MIGRATIONS: &str = r#"
-- Drop kind column (always 'rhai', no information content).
DO $$ BEGIN
    ALTER TABLE strategies DROP CONSTRAINT IF EXISTS strategies_kind_check;
EXCEPTION WHEN undefined_object THEN NULL;
END $$;
ALTER TABLE strategies DROP COLUMN IF EXISTS kind;

ALTER TABLE strategies ADD COLUMN IF NOT EXISTS previous_id UUID REFERENCES strategies(id);
ALTER TABLE strategies ADD COLUMN IF NOT EXISTS user_id TEXT;

-- Drop all spec_hash unique constraints (dedup removed — every save is a new version).
DO $$ BEGIN
    ALTER TABLE strategies DROP CONSTRAINT IF EXISTS strategies_spec_hash_key;
EXCEPTION WHEN undefined_object THEN NULL;
END $$;
DO $$ BEGIN
    ALTER TABLE strategies DROP CONSTRAINT IF EXISTS strategies_spec_hash_user_id_key;
EXCEPTION WHEN undefined_object THEN NULL;
END $$;
"#;

pub async fn run(pool: &PgPool) -> Result<()> {
    sqlx::raw_sql(SCHEMA).execute(pool).await?;
    sqlx::raw_sql(MIGRATIONS).execute(pool).await?;
    tracing::info!("herald strategy store: schema migration complete");
    Ok(())
}

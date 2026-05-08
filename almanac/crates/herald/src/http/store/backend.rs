//! `StoreBackend` — dispatch layer between in-memory and PostgreSQL stores.

use std::collections::HashMap;
use std::sync::Arc;

use anyhow::{anyhow, Result};
use sha2::{Digest, Sha256};
use parking_lot::RwLock;
use serde_json::Value;
use sqlx::{PgPool, Row};
use uuid::Uuid;

use super::types::{
    BacktestCase, BacktestResult, ExecutionConfig, PositionConfig, Strategy, StrategySpec,
    UpdateCaseReq, UpdateStrategyReq,
};
use crate::http::watch::WatchEntry;

// ── In-memory store ───────────────────────────────────────────────────────────

#[derive(Default)]
pub struct MemStore {
    pub strategies: HashMap<String, Strategy>,
    pub cases:      HashMap<String, BacktestCase>,
    pub results:    HashMap<String, BacktestResult>,
}

// ── StoreBackend ──────────────────────────────────────────────────────────────

#[derive(Clone)]
pub enum StoreBackend {
    Mem(Arc<RwLock<MemStore>>),
    Pg(PgPool),
}

impl StoreBackend {
    pub fn in_memory() -> Self {
        Self::Mem(Arc::new(RwLock::new(MemStore::default())))
    }

    pub fn postgres(pool: PgPool) -> Self {
        Self::Pg(pool)
    }
}

// ── Helpers ───────────────────────────────────────────────────────────────────

fn now_ms() -> i64 { chrono::Utc::now().timestamp_millis() }
fn new_id() -> String { Uuid::now_v7().to_string() }

/// SHA-256 of the canonical (sorted-key) JSON encoding of the spec script.
fn compute_spec_hash(spec: &StrategySpec) -> String {
    let mut hasher = Sha256::new();
    hasher.update(spec.script.as_bytes());
    hex::encode(hasher.finalize())
}

fn pg_strategy(row: &sqlx::postgres::PgRow) -> Result<Strategy> {
    let spec_val: Value = row.try_get("spec").map_err(|e| anyhow!("{e}"))?;
    Ok(Strategy {
        id:          row.try_get("id").map_err(|e| anyhow!("{e}"))?,
        name:        row.try_get("name").map_err(|e| anyhow!("{e}"))?,
        version:     row.try_get("version").map_err(|e| anyhow!("{e}"))?,
        previous_id: row.try_get("previous_id").map_err(|e| anyhow!("{e}"))?,
        label:       row.try_get("label").map_err(|e| anyhow!("{e}"))?,
        spec:        serde_json::from_value(spec_val).map_err(|e| anyhow!("spec: {e}"))?,
        notes:       row.try_get("notes").map_err(|e| anyhow!("{e}"))?,
        created_at:  row.try_get("created_at").map_err(|e| anyhow!("{e}"))?,
    })
}

fn pg_case(row: &sqlx::postgres::PgRow) -> Result<BacktestCase> {
    let cap_val:  Value = row.try_get("capital").map_err(|e| anyhow!("{e}"))?;
    let exec_val: Value = row.try_get("execution").map_err(|e| anyhow!("{e}"))?;
    Ok(BacktestCase {
        id:          row.try_get("id").map_err(|e| anyhow!("{e}"))?,
        strategy_id: row.try_get("strategy_id").map_err(|e| anyhow!("{e}"))?,
        label:       row.try_get("label").map_err(|e| anyhow!("{e}"))?,
        symbol:      row.try_get("symbol").map_err(|e| anyhow!("{e}"))?,
        timeframe:   row.try_get("timeframe").map_err(|e| anyhow!("{e}"))?,
        from_ms:     row.try_get("from_ms").map_err(|e| anyhow!("{e}"))?,
        to_ms:       row.try_get("to_ms").map_err(|e| anyhow!("{e}"))?,
        data_source: row.try_get("data_source").map_err(|e| anyhow!("{e}"))?,
        capital:     serde_json::from_value(cap_val).map_err(|e| anyhow!("capital: {e}"))?,
        execution:   serde_json::from_value(exec_val).map_err(|e| anyhow!("execution: {e}"))?,
        created_at:  row.try_get("created_at").map_err(|e| anyhow!("{e}"))?,
        updated_at:  row.try_get("updated_at").map_err(|e| anyhow!("{e}"))?,
    })
}

fn pg_result(row: &sqlx::postgres::PgRow) -> Result<BacktestResult> {
    Ok(BacktestResult {
        id:               row.try_get("id").map_err(|e| anyhow!("{e}"))?,
        case_id:          row.try_get("case_id").map_err(|e| anyhow!("{e}"))?,
        ran_at:           row.try_get("ran_at").map_err(|e| anyhow!("{e}"))?,
        s3_key:           row.try_get("s3_key").map_err(|e| anyhow!("{e}"))?,
        total_return_pct: row.try_get("total_return_pct").map_err(|e| anyhow!("{e}"))?,
        sharpe_ratio:     row.try_get("sharpe_ratio").map_err(|e| anyhow!("{e}"))?,
        max_drawdown_pct: row.try_get("max_drawdown_pct").map_err(|e| anyhow!("{e}"))?,
        win_rate_pct:     row.try_get("win_rate_pct").map_err(|e| anyhow!("{e}"))?,
        total_trades:     row.try_get("total_trades").map_err(|e| anyhow!("{e}"))?,
        created_at:       row.try_get("created_at").map_err(|e| anyhow!("{e}"))?,
    })
}

// ── Strategy CRUD ─────────────────────────────────────────────────────────────

impl StoreBackend {
    /// All versions of all strategies, sorted (name ASC, version DESC).
    pub async fn list_strategies(&self) -> Result<Vec<Strategy>> {
        match self {
            StoreBackend::Mem(store) => {
                let r = store.read();
                let mut items: Vec<Strategy> = r.strategies.values().cloned().collect();
                items.sort_by(|a, b| a.name.cmp(&b.name).then(b.version.cmp(&a.version)));
                Ok(items)
            }
            StoreBackend::Pg(pool) => {
                let rows = sqlx::query(
                    "SELECT id, name, version, previous_id, label, spec, notes, created_at \
                     FROM strategies ORDER BY name ASC, version DESC",
                )
                .fetch_all(pool)
                .await?;
                rows.iter().map(pg_strategy).collect()
            }
        }
    }

    /// All versions of a single strategy name.
    pub async fn list_strategy_versions(&self, name: &str) -> Result<Vec<Strategy>> {
        match self {
            StoreBackend::Mem(store) => {
                let r = store.read();
                let mut items: Vec<Strategy> = r.strategies.values()
                    .filter(|s| s.name == name)
                    .cloned()
                    .collect();
                items.sort_by(|a, b| b.version.cmp(&a.version));
                Ok(items)
            }
            StoreBackend::Pg(pool) => {
                let rows = sqlx::query(
                    "SELECT id, name, version, previous_id, label, spec, notes, created_at \
                     FROM strategies WHERE name = $1 ORDER BY version DESC",
                )
                .bind(name)
                .fetch_all(pool)
                .await?;
                rows.iter().map(pg_strategy).collect()
            }
        }
    }

    /// Create a new strategy version. If `version` is `None`, auto-increments
    /// from the highest existing version for that `name` (starts at 1).
    pub async fn create_strategy(
        &self,
        name: String,
        version: Option<i32>,
        previous_id: Option<String>,
        label: String,
        spec: StrategySpec,
        notes: Option<String>,
    ) -> Result<Strategy> {
        let id  = new_id();
        let now = now_ms();

        let version = match version {
            Some(v) => v,
            None => self.next_version(&name).await?,
        };

        let saved = Strategy { id: id.clone(), name, version, previous_id, label, spec, notes, created_at: now };

        match self {
            StoreBackend::Mem(store) => {
                // check uniqueness
                let exists = store.read().strategies.values()
                    .any(|s| s.name == saved.name && s.version == saved.version);
                if exists {
                    return Err(anyhow!("strategy '{}' version {} already exists", saved.name, saved.version));
                }
                store.write().strategies.insert(id, saved.clone());
            }
            StoreBackend::Pg(pool) => {
                let spec_json = serde_json::to_value(&saved.spec)?;
                sqlx::query(
                    "INSERT INTO strategies \
                     (id, name, version, previous_id, label, spec, notes, created_at) \
                     VALUES ($1, $2, $3, $4, $5, $6, $7, $8)",
                )
                .bind(&saved.id)
                .bind(&saved.name)
                .bind(saved.version)
                .bind(&saved.previous_id)
                .bind(&saved.label)
                .bind(&spec_json)
                .bind(&saved.notes)
                .bind(saved.created_at)
                .execute(pool)
                .await?;
            }
        }
        Ok(saved)
    }

    pub async fn get_strategy(&self, id: &str) -> Result<Option<Strategy>> {
        match self {
            StoreBackend::Mem(store) => Ok(store.read().strategies.get(id).cloned()),
            StoreBackend::Pg(pool) => {
                let row = sqlx::query(
                    "SELECT id, name, version, previous_id, label, spec, notes, created_at \
                     FROM strategies WHERE id = $1",
                )
                .bind(id)
                .fetch_optional(pool)
                .await?;
                row.map(|r| pg_strategy(&r)).transpose()
            }
        }
    }

    /// Update mutable fields only: label and notes.
    /// spec is immutable — create a new version to change it.
    pub async fn update_strategy(
        &self,
        id: &str,
        req: UpdateStrategyReq,
    ) -> Result<Option<Strategy>> {
        match self {
            StoreBackend::Mem(store) => {
                let mut w = store.write();
                let Some(s) = w.strategies.get_mut(id) else { return Ok(None) };
                if let Some(label) = req.label { s.label = label; }
                if req.notes.is_some() { s.notes = req.notes; }
                Ok(Some(s.clone()))
            }
            StoreBackend::Pg(pool) => {
                let Some(current) = self.get_strategy(id).await? else { return Ok(None) };
                let new_label = req.label.unwrap_or(current.label);
                let new_notes = if req.notes.is_some() { req.notes } else { current.notes };
                let row = sqlx::query(
                    "UPDATE strategies SET label = $2, notes = $3 WHERE id = $1 \
                     RETURNING id, name, version, label, spec, notes, created_at",
                )
                .bind(id)
                .bind(&new_label)
                .bind(&new_notes)
                .fetch_optional(pool)
                .await?;
                row.map(|r| pg_strategy(&r)).transpose()
            }
        }
    }

    /// Delete a strategy version. Fails if any backtest_cases reference it.
    pub async fn delete_strategy(&self, id: &str) -> Result<bool> {
        match self {
            StoreBackend::Mem(store) => {
                let referenced = store.read().cases.values().any(|c| c.strategy_id == id);
                if referenced {
                    return Err(anyhow!("strategy is referenced by one or more backtest cases"));
                }
                Ok(store.write().strategies.remove(id).is_some())
            }
            StoreBackend::Pg(pool) => {
                // RESTRICT FK will cause a DB error — surface it cleanly.
                let result = sqlx::query("DELETE FROM strategies WHERE id = $1")
                    .bind(id)
                    .execute(pool)
                    .await
                    .map_err(|e| {
                        if e.to_string().contains("foreign key") || e.to_string().contains("restrict") {
                            anyhow!("strategy is referenced by one or more backtest cases")
                        } else {
                            anyhow!("{e}")
                        }
                    })?;
                Ok(result.rows_affected() > 0)
            }
        }
    }

    pub async fn strategy_exists(&self, id: &str) -> Result<bool> {
        match self {
            StoreBackend::Mem(store) => Ok(store.read().strategies.contains_key(id)),
            StoreBackend::Pg(pool) => {
                let row = sqlx::query("SELECT 1 FROM strategies WHERE id = $1")
                    .bind(id)
                    .fetch_optional(pool)
                    .await?;
                Ok(row.is_some())
            }
        }
    }

    /// Create or return existing strategy version for this (name, script) pair.
    ///
    /// Two resolution paths depending on `strategy_id`:
    ///
    /// **`strategy_id = Some(id)`** — compare against that specific version:
    /// - Same script → return the referenced version (no new row).
    /// - Different script → create next version; `previous_id = strategy_id`.
    ///
    /// **`strategy_id = None`** — global spec_hash dedup across all versions of `name`:
    /// - Matching hash found → return existing row unchanged.
    /// - No match → create next version; `previous_id` points to the latest
    ///   existing version for `name` (None if this is the first version).
    pub async fn upsert_strategy(
        &self,
        name: String,
        label: String,
        spec: StrategySpec,
        notes: Option<String>,
        strategy_id: Option<String>,
    ) -> Result<Strategy> {
        let hash = compute_spec_hash(&spec);

        // ── Path A: compare against a specific version ───────────────────────
        if let Some(ref sid) = strategy_id {
            let existing = self.get_strategy(sid).await?
                .ok_or_else(|| anyhow!("strategy_id '{}' not found", sid))?;

            if existing.name != name {
                return Err(anyhow!(
                    "strategy_id '{}' belongs to '{}', not '{}'",
                    sid, existing.name, name
                ));
            }

            // Same script → reuse that version, no new row.
            if existing.spec.script == spec.script {
                return Ok(existing);
            }

            // Different script → new version branching from sid.
            return self.create_strategy_with_hash(
                name, label, spec, notes, Some(sid.clone()), hash,
            ).await;
        }

        // ── Path B: global spec_hash dedup ───────────────────────────────────
        match self {
            StoreBackend::Mem(store) => {
                if let Some(existing) = store.read().strategies.values()
                    .find(|s| s.name == name && s.spec.script == spec.script)
                    .cloned()
                {
                    return Ok(existing);
                }
            }
            StoreBackend::Pg(pool) => {
                let row = sqlx::query(
                    "SELECT id, name, version, previous_id, label, spec, notes, created_at \
                     FROM strategies WHERE spec_hash = $1",
                )
                .bind(&hash)
                .fetch_optional(pool)
                .await?;
                if let Some(r) = row {
                    return pg_strategy(&r);
                }
            }
        }

        // New script — find previous_id (latest version for this name).
        let previous_id = match self {
            StoreBackend::Mem(store) => {
                store.read().strategies.values()
                    .filter(|s| s.name == name)
                    .max_by_key(|s| s.version)
                    .map(|s| s.id.clone())
            }
            StoreBackend::Pg(pool) => {
                let row = sqlx::query(
                    "SELECT id FROM strategies WHERE name = $1 ORDER BY version DESC LIMIT 1",
                )
                .bind(&name)
                .fetch_optional(pool)
                .await?;
                row.map(|r| r.try_get::<String, _>("id").unwrap_or_default())
            }
        };

        self.create_strategy_with_hash(name, label, spec, notes, previous_id, hash).await
    }

    /// Internal: create a new strategy version with a pre-computed spec_hash.
    async fn create_strategy_with_hash(
        &self,
        name: String,
        label: String,
        spec: StrategySpec,
        notes: Option<String>,
        previous_id: Option<String>,
        hash: String,
    ) -> Result<Strategy> {
        let version = self.next_version(&name).await?;
        let id  = new_id();
        let now = now_ms();
        let saved = Strategy {
            id: id.clone(), name, version, previous_id, label, spec: spec.clone(), notes,
            created_at: now,
        };

        match self {
            StoreBackend::Mem(store) => {
                store.write().strategies.insert(id, saved.clone());
            }
            StoreBackend::Pg(pool) => {
                let spec_json = serde_json::to_value(&saved.spec)?;
                sqlx::query(
                    "INSERT INTO strategies \
                     (id, name, version, previous_id, label, spec, notes, spec_hash, created_at) \
                     VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)",
                )
                .bind(&saved.id)
                .bind(&saved.name)
                .bind(saved.version)
                .bind(&saved.previous_id)
                .bind(&saved.label)
                .bind(&spec_json)
                .bind(&saved.notes)
                .bind(&hash)
                .bind(saved.created_at)
                .execute(pool)
                .await?;
            }
        }
        Ok(saved)
    }

    /// Create or update a backtest case.
    ///
    /// - `case_id = Some(id)` → UPDATE that case in-place (params may have changed),
    ///   return the updated row. Returns error if id not found.
    /// - `case_id = None`     → CREATE new case row.
    pub async fn upsert_case(
        &self,
        case_id: Option<String>,
        strategy_id: String,
        label: String,
        symbol: String,
        timeframe: Option<String>,
        from_ms: Option<i64>,
        to_ms: Option<i64>,
        data_source: Option<String>,
        capital: PositionConfig,
        execution: ExecutionConfig,
    ) -> Result<BacktestCase> {
        if let Some(ref id) = case_id {
            let req = UpdateCaseReq {
                label: Some(label),
                strategy_id: Some(strategy_id),
                symbol: Some(symbol),
                timeframe,
                from_ms,
                to_ms,
                data_source,
                capital: Some(capital),
                execution: Some(execution),
            };
            match self.update_case(id, req).await? {
                Some(c) => return Ok(c),
                None    => return Err(anyhow!("case '{}' not found", id)),
            }
        }
        self.create_case(strategy_id, label, symbol, timeframe, from_ms, to_ms, data_source, capital, execution).await
    }

    async fn next_version(&self, name: &str) -> Result<i32> {
        match self {
            StoreBackend::Mem(store) => {
                let max = store.read().strategies.values()
                    .filter(|s| s.name == name)
                    .map(|s| s.version)
                    .max()
                    .unwrap_or(0);
                Ok(max + 1)
            }
            StoreBackend::Pg(pool) => {
                let row = sqlx::query(
                    "SELECT COALESCE(MAX(version), 0) AS max_v FROM strategies WHERE name = $1",
                )
                .bind(name)
                .fetch_one(pool)
                .await?;
                let max_v: i32 = row.try_get("max_v").unwrap_or(0);
                Ok(max_v + 1)
            }
        }
    }
}

// ── BacktestCase CRUD ─────────────────────────────────────────────────────────

impl StoreBackend {
    pub async fn list_cases(&self) -> Result<Vec<BacktestCase>> {
        match self {
            StoreBackend::Mem(store) => {
                let r = store.read();
                let mut items: Vec<BacktestCase> = r.cases.values().cloned().collect();
                items.sort_by(|a, b| a.created_at.cmp(&b.created_at));
                Ok(items)
            }
            StoreBackend::Pg(pool) => {
                let rows = sqlx::query(
                    "SELECT id, strategy_id, label, symbol, timeframe, from_ms, to_ms, \
                            data_source, capital, execution, created_at, updated_at \
                     FROM backtest_cases ORDER BY created_at ASC",
                )
                .fetch_all(pool)
                .await?;
                rows.iter().map(pg_case).collect()
            }
        }
    }

    pub async fn create_case(
        &self,
        strategy_id: String,
        label: String,
        symbol: String,
        timeframe: Option<String>,
        from_ms: Option<i64>,
        to_ms: Option<i64>,
        data_source: Option<String>,
        capital: PositionConfig,
        execution: ExecutionConfig,
    ) -> Result<BacktestCase> {
        let id  = new_id();
        let now = now_ms();
        let saved = BacktestCase {
            id: id.clone(), strategy_id, label, symbol, timeframe,
            from_ms, to_ms, data_source, capital, execution,
            created_at: now, updated_at: now,
        };

        match self {
            StoreBackend::Mem(store) => {
                store.write().cases.insert(id, saved.clone());
            }
            StoreBackend::Pg(pool) => {
                let cap_json  = serde_json::to_value(&saved.capital)?;
                let exec_json = serde_json::to_value(&saved.execution)?;
                sqlx::query(
                    "INSERT INTO backtest_cases \
                     (id, strategy_id, label, symbol, timeframe, from_ms, to_ms, data_source, \
                      capital, execution, created_at, updated_at) \
                     VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)",
                )
                .bind(&saved.id).bind(&saved.strategy_id).bind(&saved.label)
                .bind(&saved.symbol).bind(&saved.timeframe)
                .bind(saved.from_ms).bind(saved.to_ms)
                .bind(&saved.data_source)
                .bind(&cap_json).bind(&exec_json)
                .bind(saved.created_at).bind(saved.updated_at)
                .execute(pool)
                .await?;
            }
        }
        Ok(saved)
    }

    pub async fn get_case(&self, id: &str) -> Result<Option<BacktestCase>> {
        match self {
            StoreBackend::Mem(store) => Ok(store.read().cases.get(id).cloned()),
            StoreBackend::Pg(pool) => {
                let row = sqlx::query(
                    "SELECT id, strategy_id, label, symbol, timeframe, from_ms, to_ms, \
                            data_source, capital, execution, created_at, updated_at \
                     FROM backtest_cases WHERE id = $1",
                )
                .bind(id)
                .fetch_optional(pool)
                .await?;
                row.map(|r| pg_case(&r)).transpose()
            }
        }
    }

    pub async fn update_case(&self, id: &str, req: UpdateCaseReq) -> Result<Option<BacktestCase>> {
        match self {
            StoreBackend::Mem(store) => {
                let mut w = store.write();
                let Some(c) = w.cases.get_mut(id) else { return Ok(None) };
                if let Some(v) = req.label       { c.label       = v; }
                if let Some(v) = req.strategy_id { c.strategy_id = v; }
                if let Some(v) = req.symbol       { c.symbol       = v; }
                if req.timeframe.is_some()    { c.timeframe    = req.timeframe;    }
                if req.from_ms.is_some()      { c.from_ms      = req.from_ms;      }
                if req.to_ms.is_some()        { c.to_ms        = req.to_ms;        }
                if req.data_source.is_some()  { c.data_source  = req.data_source;  }
                if let Some(v) = req.capital   { c.capital   = v; }
                if let Some(v) = req.execution { c.execution = v; }
                c.updated_at = now_ms();
                Ok(Some(c.clone()))
            }
            StoreBackend::Pg(pool) => {
                let now = now_ms();
                let Some(cur) = self.get_case(id).await? else { return Ok(None) };
                if let Some(ref sid) = req.strategy_id {
                    if !self.strategy_exists(sid).await? {
                        return Err(anyhow!("strategy_id not found"));
                    }
                }
                let new_sid    = req.strategy_id.unwrap_or(cur.strategy_id);
                let new_label  = req.label.unwrap_or(cur.label);
                let new_symbol = req.symbol.unwrap_or(cur.symbol);
                let new_tf     = if req.timeframe.is_some()   { req.timeframe   } else { cur.timeframe   };
                let new_from   = if req.from_ms.is_some()     { req.from_ms     } else { cur.from_ms     };
                let new_to     = if req.to_ms.is_some()       { req.to_ms       } else { cur.to_ms       };
                let new_ds     = if req.data_source.is_some() { req.data_source } else { cur.data_source };
                let new_cap    = req.capital.unwrap_or(cur.capital);
                let new_exec   = req.execution.unwrap_or(cur.execution);
                let cap_json   = serde_json::to_value(&new_cap)?;
                let exec_json  = serde_json::to_value(&new_exec)?;

                let row = sqlx::query(
                    "UPDATE backtest_cases \
                     SET strategy_id=$2, label=$3, symbol=$4, timeframe=$5, \
                         from_ms=$6, to_ms=$7, data_source=$8, \
                         capital=$9, execution=$10, updated_at=$11 \
                     WHERE id=$1 \
                     RETURNING id, strategy_id, label, symbol, timeframe, from_ms, to_ms, \
                               data_source, capital, execution, created_at, updated_at",
                )
                .bind(id).bind(&new_sid).bind(&new_label).bind(&new_symbol)
                .bind(&new_tf).bind(new_from).bind(new_to).bind(&new_ds)
                .bind(&cap_json).bind(&exec_json).bind(now)
                .fetch_optional(pool)
                .await?;
                row.map(|r| pg_case(&r)).transpose()
            }
        }
    }

    pub async fn delete_case(&self, id: &str) -> Result<bool> {
        match self {
            StoreBackend::Mem(store) => Ok(store.write().cases.remove(id).is_some()),
            StoreBackend::Pg(pool) => {
                let r = sqlx::query("DELETE FROM backtest_cases WHERE id = $1")
                    .bind(id).execute(pool).await?;
                Ok(r.rows_affected() > 0)
            }
        }
    }

    /// Resolve a case + its linked strategy version.
    pub async fn resolve_case(&self, id: &str) -> Result<Option<(BacktestCase, Strategy)>> {
        let Some(case) = self.get_case(id).await? else { return Ok(None) };
        let Some(strategy) = self.get_strategy(&case.strategy_id).await? else {
            return Err(anyhow!("linked strategy '{}' not found", case.strategy_id));
        };
        Ok(Some((case, strategy)))
    }
}

// ── BacktestResult CRUD ───────────────────────────────────────────────────────

impl StoreBackend {
    pub async fn list_results(&self, case_id: &str) -> Result<Vec<BacktestResult>> {
        match self {
            StoreBackend::Mem(store) => {
                let r = store.read();
                let mut items: Vec<BacktestResult> = r.results.values()
                    .filter(|r| r.case_id == case_id)
                    .cloned()
                    .collect();
                items.sort_by(|a, b| b.ran_at.cmp(&a.ran_at));
                Ok(items)
            }
            StoreBackend::Pg(pool) => {
                let rows = sqlx::query(
                    "SELECT id, case_id, ran_at, s3_key, total_return_pct, sharpe_ratio, \
                            max_drawdown_pct, win_rate_pct, total_trades, created_at \
                     FROM backtest_results WHERE case_id = $1 ORDER BY ran_at DESC",
                )
                .bind(case_id)
                .fetch_all(pool)
                .await?;
                rows.iter().map(pg_result).collect()
            }
        }
    }

    pub async fn get_result(&self, id: &str) -> Result<Option<BacktestResult>> {
        match self {
            StoreBackend::Mem(store) => Ok(store.read().results.get(id).cloned()),
            StoreBackend::Pg(pool) => {
                let row = sqlx::query(
                    "SELECT id, case_id, ran_at, s3_key, total_return_pct, sharpe_ratio, \
                            max_drawdown_pct, win_rate_pct, total_trades, created_at \
                     FROM backtest_results WHERE id = $1",
                )
                .bind(id)
                .fetch_optional(pool)
                .await?;
                row.map(|r| pg_result(&r)).transpose()
            }
        }
    }

    pub async fn save_result(
        &self,
        case_id: String,
        ran_at: i64,
        s3_key: Option<String>,
        total_return_pct: f64,
        sharpe_ratio: f64,
        max_drawdown_pct: f64,
        win_rate_pct: f64,
        total_trades: i64,
    ) -> Result<BacktestResult> {
        let id  = new_id();
        let now = now_ms();
        let saved = BacktestResult {
            id: id.clone(), case_id, ran_at, s3_key,
            total_return_pct, sharpe_ratio, max_drawdown_pct, win_rate_pct, total_trades,
            created_at: now,
        };

        match self {
            StoreBackend::Mem(store) => {
                store.write().results.insert(id, saved.clone());
            }
            StoreBackend::Pg(pool) => {
                sqlx::query(
                    "INSERT INTO backtest_results \
                     (id, case_id, ran_at, s3_key, total_return_pct, sharpe_ratio, \
                      max_drawdown_pct, win_rate_pct, total_trades, created_at) \
                     VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)",
                )
                .bind(&saved.id).bind(&saved.case_id).bind(saved.ran_at)
                .bind(&saved.s3_key)
                .bind(saved.total_return_pct).bind(saved.sharpe_ratio)
                .bind(saved.max_drawdown_pct).bind(saved.win_rate_pct)
                .bind(saved.total_trades).bind(saved.created_at)
                .execute(pool)
                .await?;
            }
        }
        Ok(saved)
    }

    pub async fn delete_result(&self, id: &str) -> Result<bool> {
        match self {
            StoreBackend::Mem(store) => Ok(store.write().results.remove(id).is_some()),
            StoreBackend::Pg(pool) => {
                let r = sqlx::query("DELETE FROM backtest_results WHERE id = $1")
                    .bind(id).execute(pool).await?;
                Ok(r.rows_affected() > 0)
            }
        }
    }
}

// ── WatchEntry persistence ────────────────────────────────────────────────────

fn pg_watch_entry(row: &sqlx::postgres::PgRow) -> Result<WatchEntry> {
    let symbols_val: Value         = row.try_get("symbols").map_err(|e| anyhow!("{e}"))?;
    let spec_val:    Value         = row.try_get("spec").map_err(|e| anyhow!("{e}"))?;
    Ok(WatchEntry {
        id:                row.try_get("id").map_err(|e| anyhow!("{e}"))?,
        symbols:           serde_json::from_value(symbols_val).map_err(|e| anyhow!("symbols: {e}"))?,
        timeframe:         row.try_get("timeframe").map_err(|e| anyhow!("{e}"))?,
        spec:              serde_json::from_value(spec_val).map_err(|e| anyhow!("spec: {e}"))?,
        webhook_url:       row.try_get("webhook_url").map_err(|e| anyhow!("{e}"))?,
        nats_subject:      row.try_get("nats_subject").map_err(|e| anyhow!("{e}"))?,
        pinned_indicators: 0, // recomputed after handle re-acquisition
        created_at:        row.try_get("created_at").map_err(|e| anyhow!("{e}"))?,
    })
}

impl StoreBackend {
    /// Load all persisted watch entries (serializable fields only).
    /// Caller is responsible for re-acquiring indicator handles.
    pub async fn list_watch_entries(&self) -> Result<Vec<WatchEntry>> {
        match self {
            StoreBackend::Mem(_) => Ok(vec![]),
            StoreBackend::Pg(pool) => {
                let rows = sqlx::query(
                    "SELECT id, symbols, timeframe, spec, webhook_url, nats_subject, created_at \
                     FROM watch_entries ORDER BY created_at ASC",
                )
                .fetch_all(pool)
                .await?;
                rows.iter().map(pg_watch_entry).collect()
            }
        }
    }

    pub async fn save_watch_entry(&self, entry: &WatchEntry) -> Result<()> {
        match self {
            StoreBackend::Mem(_) => Ok(()), // in-memory: WatchStore is the source of truth
            StoreBackend::Pg(pool) => {
                let symbols_json = serde_json::to_value(&entry.symbols)?;
                let spec_json    = serde_json::to_value(&entry.spec)?;
                sqlx::query(
                    "INSERT INTO watch_entries \
                     (id, symbols, timeframe, spec, webhook_url, nats_subject, created_at) \
                     VALUES ($1,$2,$3,$4,$5,$6,$7) \
                     ON CONFLICT (id) DO NOTHING",
                )
                .bind(&entry.id)
                .bind(&symbols_json)
                .bind(&entry.timeframe)
                .bind(&spec_json)
                .bind(&entry.webhook_url)
                .bind(&entry.nats_subject)
                .bind(entry.created_at)
                .execute(pool)
                .await?;
                Ok(())
            }
        }
    }

    pub async fn update_watch_entry(&self, entry: &WatchEntry) -> Result<()> {
        match self {
            StoreBackend::Mem(_) => Ok(()),
            StoreBackend::Pg(pool) => {
                let symbols_json = serde_json::to_value(&entry.symbols)?;
                let spec_json    = serde_json::to_value(&entry.spec)?;
                sqlx::query(
                    "UPDATE watch_entries \
                     SET symbols=$2, timeframe=$3, spec=$4, webhook_url=$5, nats_subject=$6 \
                     WHERE id=$1",
                )
                .bind(&entry.id)
                .bind(&symbols_json)
                .bind(&entry.timeframe)
                .bind(&spec_json)
                .bind(&entry.webhook_url)
                .bind(&entry.nats_subject)
                .execute(pool)
                .await?;
                Ok(())
            }
        }
    }

    pub async fn delete_watch_entry(&self, id: &str) -> Result<()> {
        match self {
            StoreBackend::Mem(_) => Ok(()),
            StoreBackend::Pg(pool) => {
                sqlx::query("DELETE FROM watch_entries WHERE id = $1")
                    .bind(id)
                    .execute(pool)
                    .await?;
                Ok(())
            }
        }
    }
}

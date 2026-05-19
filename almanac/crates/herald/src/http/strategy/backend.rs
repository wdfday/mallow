//! `StoreBackend` — dispatch layer between in-memory and PostgreSQL stores.

use std::collections::HashMap;
use std::sync::Arc;

use anyhow::{anyhow, Result};
use sha2::{Digest, Sha256};
use parking_lot::RwLock;
use serde_json::Value;
use sqlx::{PgPool, Row};
use uuid::Uuid;

use super::types::{Strategy, StrategySpec, UpdateStrategyReq};
use crate::http::watch::WatchEntry;

// ── In-memory store ───────────────────────────────────────────────────────────

#[derive(Default)]
pub struct MemStore {
    pub strategies: HashMap<String, Strategy>,
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
        let id   = new_id();
        let now  = now_ms();
        let hash = compute_spec_hash(&spec);

        let version = match version {
            Some(v) => v,
            None => self.next_version(&name).await?,
        };

        let saved = Strategy { id: id.clone(), name, version, previous_id, label, spec, notes, created_at: now };

        match self {
            StoreBackend::Mem(store) => {
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

    /// Delete all versions of the strategy that owns `id` (the full version chain).
    pub async fn delete_strategy(&self, id: &str) -> Result<bool> {
        match self {
            StoreBackend::Mem(store) => {
                let name = store.read().strategies.get(id).map(|s| s.name.clone());
                let Some(name) = name else { return Ok(false) };
                store.write().strategies.retain(|_, s| s.name != name);
                Ok(true)
            }
            StoreBackend::Pg(pool) => {
                let result = sqlx::query(
                    "DELETE FROM strategies \
                     WHERE name = (SELECT name FROM strategies WHERE id = $1)",
                )
                .bind(id)
                .execute(pool)
                .await?;
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

            if existing.spec.script == spec.script {
                return Ok(existing);
            }

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

// ── WatchEntry persistence ────────────────────────────────────────────────────

fn pg_watch_entry(row: &sqlx::postgres::PgRow) -> Result<WatchEntry> {
    let symbols_val: Value = row.try_get("symbols").map_err(|e| anyhow!("{e}"))?;
    let spec_val:    Value = row.try_get("spec").map_err(|e| anyhow!("{e}"))?;
    Ok(WatchEntry {
        id:                row.try_get("id").map_err(|e| anyhow!("{e}"))?,
        symbols:           serde_json::from_value(symbols_val).map_err(|e| anyhow!("symbols: {e}"))?,
        timeframe:         row.try_get("timeframe").map_err(|e| anyhow!("{e}"))?,
        spec:              serde_json::from_value(spec_val).map_err(|e| anyhow!("spec: {e}"))?,
        webhook_url:       row.try_get("webhook_url").map_err(|e| anyhow!("{e}"))?,
        nats_subject:      row.try_get("nats_subject").map_err(|e| anyhow!("{e}"))?,
        pinned_indicators: 0,
        created_at:        row.try_get("created_at").map_err(|e| anyhow!("{e}"))?,
    })
}

impl StoreBackend {
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
            StoreBackend::Mem(_) => Ok(()),
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

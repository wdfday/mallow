//! Thin SQLite registry at `~/Fathom/.data/fathom.db` — two tables:
//! `sources` (mounted external data directories, read-in-place) and `symbols`
//! (the user's research universe; the on-demand loader syncs data for these).
//!
//! Connections are opened per call — registry traffic is rare (user actions),
//! so there's nothing to gain from a pooled/managed connection, and it keeps
//! the module free of Tauri state.

use rusqlite::Connection;
use serde::Serialize;

use super::home;

fn open() -> Result<Connection, String> {
    Connection::open(home::db_path()?).map_err(|e| format!("open fathom.db: {e}"))
}

pub fn ensure_schema() -> Result<(), String> {
    let conn = open()?;
    conn.execute_batch(
        r#"
        CREATE TABLE IF NOT EXISTS sources (
            id         INTEGER PRIMARY KEY AUTOINCREMENT,
            name       TEXT NOT NULL,
            kind       TEXT NOT NULL DEFAULT 'mount' CHECK(kind IN ('mount')),
            path       TEXT NOT NULL UNIQUE,
            created_at TEXT NOT NULL DEFAULT (datetime('now'))
        );
        CREATE TABLE IF NOT EXISTS symbols (
            id        INTEGER PRIMARY KEY AUTOINCREMENT,
            symbol    TEXT NOT NULL UNIQUE,
            provider  TEXT NOT NULL DEFAULT 'binance',
            status    TEXT NOT NULL DEFAULT 'pending'
                      CHECK(status IN ('pending','syncing','ready','error')),
            error     TEXT,
            added_at  TEXT NOT NULL DEFAULT (datetime('now')),
            last_sync TEXT
        );
        "#,
    )
    .map_err(|e| format!("init schema: {e}"))
}

#[derive(Debug, Clone, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct MountSource {
    pub id: i64,
    pub name: String,
    pub path: String,
    pub created_at: String,
}

#[derive(Debug, Clone, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct UniverseSymbol {
    pub id: i64,
    pub symbol: String,
    pub provider: String,
    pub status: String,
    pub error: Option<String>,
    pub added_at: String,
    pub last_sync: Option<String>,
}

pub fn list_mounts() -> Result<Vec<MountSource>, String> {
    let conn = open()?;
    let mut stmt = conn
        .prepare("SELECT id, name, path, created_at FROM sources ORDER BY id")
        .map_err(|e| e.to_string())?;
    let rows = stmt
        .query_map([], |r| {
            Ok(MountSource { id: r.get(0)?, name: r.get(1)?, path: r.get(2)?, created_at: r.get(3)? })
        })
        .map_err(|e| e.to_string())?;
    rows.collect::<Result<Vec<_>, _>>().map_err(|e| e.to_string())
}

#[tauri::command]
pub fn sources_list() -> Result<Vec<MountSource>, String> {
    list_mounts()
}

#[tauri::command]
pub fn sources_add_mount(path: String, name: Option<String>) -> Result<MountSource, String> {
    let dir = std::path::Path::new(&path);
    if !dir.is_dir() {
        return Err(format!("'{path}' is not a directory"));
    }
    let name = name.unwrap_or_else(|| {
        dir.file_name().map(|n| n.to_string_lossy().into_owned()).unwrap_or_else(|| path.clone())
    });
    let conn = open()?;
    conn.execute(
        "INSERT INTO sources (name, path) VALUES (?1, ?2)
         ON CONFLICT(path) DO UPDATE SET name = excluded.name",
        rusqlite::params![name, path],
    )
    .map_err(|e| e.to_string())?;
    let mut stmt = conn
        .prepare("SELECT id, name, path, created_at FROM sources WHERE path = ?1")
        .map_err(|e| e.to_string())?;
    stmt.query_row([&path], |r| {
        Ok(MountSource { id: r.get(0)?, name: r.get(1)?, path: r.get(2)?, created_at: r.get(3)? })
    })
    .map_err(|e| e.to_string())
}

#[tauri::command]
pub fn sources_remove(id: i64) -> Result<(), String> {
    let conn = open()?;
    conn.execute("DELETE FROM sources WHERE id = ?1", [id]).map_err(|e| e.to_string())?;
    Ok(())
}

#[tauri::command]
pub fn symbols_list() -> Result<Vec<UniverseSymbol>, String> {
    let conn = open()?;
    let mut stmt = conn
        .prepare(
            "SELECT id, symbol, provider, status, error, added_at, last_sync
             FROM symbols ORDER BY symbol",
        )
        .map_err(|e| e.to_string())?;
    let rows = stmt
        .query_map([], |r| {
            Ok(UniverseSymbol {
                id: r.get(0)?,
                symbol: r.get(1)?,
                provider: r.get(2)?,
                status: r.get(3)?,
                error: r.get(4)?,
                added_at: r.get(5)?,
                last_sync: r.get(6)?,
            })
        })
        .map_err(|e| e.to_string())?;
    rows.collect::<Result<Vec<_>, _>>().map_err(|e| e.to_string())
}

/// Insert (or revive) a universe symbol in `pending` state, clearing any stale error.
pub fn upsert_symbol(symbol: &str, provider: &str) -> Result<(), String> {
    let conn = open()?;
    conn.execute(
        "INSERT INTO symbols (symbol, provider) VALUES (?1, ?2)
         ON CONFLICT(symbol) DO UPDATE SET status = 'pending', error = NULL",
        rusqlite::params![symbol, provider],
    )
    .map_err(|e| e.to_string())?;
    Ok(())
}

pub fn set_symbol_status(symbol: &str, status: &str, error: Option<&str>) -> Result<(), String> {
    let conn = open()?;
    conn.execute(
        "UPDATE symbols SET status = ?2, error = ?3,
                last_sync = CASE WHEN ?2 = 'ready' THEN datetime('now') ELSE last_sync END
         WHERE symbol = ?1",
        rusqlite::params![symbol, status, error],
    )
    .map_err(|e| e.to_string())?;
    Ok(())
}

pub fn delete_symbol(symbol: &str) -> Result<(), String> {
    let conn = open()?;
    conn.execute("DELETE FROM symbols WHERE symbol = ?1", [symbol]).map_err(|e| e.to_string())?;
    Ok(())
}

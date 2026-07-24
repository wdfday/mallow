//! Fathom home (`~/Fathom/`) — created on first run, owns everything Fathom keeps on the
//! user's machine: `.data/` (SQLite registry + downloaded data lake), `.agent/` (reserved),
//! and it's the default location for project folders. See docs/VISION.md.

use std::path::PathBuf;

pub fn fathom_home() -> Result<PathBuf, String> {
    dirs::home_dir()
        .map(|h| h.join("Fathom"))
        .ok_or_else(|| "cannot determine home directory".to_string())
}

/// `~/Fathom/.data` — doubles as the lake root: `{Source}/{TF}/{SYMBOL}/*.parquet`,
/// the same layout `alm-engine`'s parquet discovery indexes.
pub fn lake_dir() -> Result<PathBuf, String> {
    Ok(fathom_home()?.join(".data"))
}

pub fn agent_dir() -> Result<PathBuf, String> {
    Ok(fathom_home()?.join(".agent"))
}

pub fn db_path() -> Result<PathBuf, String> {
    Ok(lake_dir()?.join("fathom.db"))
}

/// Idempotent first-run setup: directory skeleton + SQLite schema.
pub fn bootstrap() -> Result<(), String> {
    for dir in [lake_dir()?, agent_dir()?] {
        std::fs::create_dir_all(&dir).map_err(|e| format!("create {}: {e}", dir.display()))?;
    }
    super::registry::ensure_schema()
}

/// FE uses this as the default parent directory for New Project.
#[tauri::command]
pub fn fathom_home_path() -> Result<String, String> {
    Ok(fathom_home()?.to_string_lossy().into_owned())
}

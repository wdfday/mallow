//! Data catalog + bar resolution across the three source tiers (see docs/VISION.md):
//! 1. project `data/` (flat `{SYMBOL}.parquet|csv` imports, or a full tree)
//! 2. mounted external directories (read-in-place)
//! 3. the `~/Fathom/.data` lake (on-demand loader output)
//!
//! Tree layout is almanac's convention `{Source}/{TF}/{SYMBOL}/*.parquet` — the same
//! one `alm-engine`'s parquet discovery walks, so any root that resolves here can also
//! be handed directly to `alm_engine::backtest::run` as its `data_dir`.

use std::path::{Path, PathBuf};

use alm_core::{Bar, Timeframe};
// Deprecated for *MTF-engine* use (multiple real feeds beat resampling there); for
// materializing a TF ladder / serving a chart TF from finer bars — exactly this module's
// job — its calendar-aligned bucketing is still the right tool.
#[allow(deprecated)]
use alm_data::aggregator::StandaloneAggregator;
use alm_data::{BarFeed, ParquetFeed};
use serde::Serialize;

use super::{home, registry};

#[derive(Debug, Clone)]
pub struct DataRoot {
    /// `"project"` | `"mount"` | `"lake"`
    pub kind: &'static str,
    pub name: String,
    pub path: PathBuf,
}

/// Resolution-ordered roots: project `data/` → mounts (registration order) → lake.
pub fn data_roots(project_path: Option<&str>) -> Vec<DataRoot> {
    let mut roots = Vec::new();
    if let Some(p) = project_path {
        roots.push(DataRoot { kind: "project", name: "project".into(), path: Path::new(p).join("data") });
    }
    if let Ok(mounts) = registry::list_mounts() {
        for m in mounts {
            roots.push(DataRoot { kind: "mount", name: m.name, path: PathBuf::from(m.path) });
        }
    }
    if let Ok(lake) = home::lake_dir() {
        roots.push(DataRoot { kind: "lake", name: "Fathom".into(), path: lake });
    }
    roots
}

#[derive(Debug, Clone)]
pub struct TreeFile {
    pub provider: String,
    pub timeframe: String, // lowercase dir name, e.g. "m1"
    pub symbol: String,    // lowercase dir name
    pub path: PathBuf,
}

/// Walk one root in the `{Source}/{TF}/{SYMBOL}/*.parquet` layout. Mirrors the walk in
/// `alm_engine::data` (including the `_eod` skip); dot-entries (e.g. the lake's
/// `fathom.db`) are skipped implicitly by the is-dir checks and the extension filter.
pub fn walk_tree(root: &Path) -> Vec<TreeFile> {
    let mut out = Vec::new();
    let Ok(sources) = std::fs::read_dir(root) else { return out };
    for src in sources.flatten() {
        if !src.file_type().map(|t| t.is_dir()).unwrap_or(false) { continue }
        let Some(provider) = src.file_name().to_str().map(|s| s.to_lowercase()) else { continue };
        if provider.starts_with('.') || provider.ends_with("_eod") { continue }
        let Ok(tfs) = std::fs::read_dir(src.path()) else { continue };
        for tf in tfs.flatten() {
            if !tf.file_type().map(|t| t.is_dir()).unwrap_or(false) { continue }
            let Some(timeframe) = tf.file_name().to_str().map(|s| s.to_lowercase()) else { continue };
            let Ok(syms) = std::fs::read_dir(tf.path()) else { continue };
            for sym in syms.flatten() {
                if !sym.file_type().map(|t| t.is_dir()).unwrap_or(false) { continue }
                let Some(symbol) = sym.file_name().to_str().map(|s| s.to_lowercase()) else { continue };
                let Ok(files) = std::fs::read_dir(sym.path()) else { continue };
                for f in files.flatten() {
                    let path = f.path();
                    if path.extension().and_then(|e| e.to_str()) == Some("parquet") {
                        out.push(TreeFile {
                            provider: provider.clone(),
                            timeframe: timeframe.clone(),
                            symbol: symbol.clone(),
                            path,
                        });
                    }
                }
            }
        }
    }
    out
}

// ── Catalog (UI listing) ────────────────────────────────────────────────────

#[derive(Debug, Clone, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct CatalogEntry {
    pub symbol: String,    // uppercase for display
    pub timeframe: String, // uppercase, or "?" for flat files (detected on load)
    pub provider: String,
    pub root_kind: String,
    pub root_name: String,
    pub files: usize,
}

#[tauri::command]
pub fn data_catalog(project_path: Option<String>) -> Result<Vec<CatalogEntry>, String> {
    use std::collections::BTreeMap;
    let mut grouped: BTreeMap<(String, String, String, String, String), usize> = BTreeMap::new();

    for root in data_roots(project_path.as_deref()) {
        for f in walk_tree(&root.path) {
            let key = (
                f.symbol.to_uppercase(),
                f.timeframe.to_uppercase(),
                f.provider.clone(),
                root.kind.to_string(),
                root.name.clone(),
            );
            *grouped.entry(key).or_insert(0) += 1;
        }
        // Flat files only make sense in the project's own data/ (import convention).
        if root.kind == "project" {
            for (sym, _, _) in flat_files(&root.path) {
                let key = (sym.to_uppercase(), "?".into(), "file".into(), root.kind.to_string(), root.name.clone());
                *grouped.entry(key).or_insert(0) += 1;
            }
        }
    }

    Ok(grouped
        .into_iter()
        .map(|((symbol, timeframe, provider, root_kind, root_name), files)| CatalogEntry {
            symbol, timeframe, provider, root_kind, root_name, files,
        })
        .collect())
}

/// `data/{SYMBOL}.parquet|csv` flat imports: (stem, path, kind).
fn flat_files(dir: &Path) -> Vec<(String, PathBuf, &'static str)> {
    let mut out = Vec::new();
    let Ok(entries) = std::fs::read_dir(dir) else { return out };
    for e in entries.flatten() {
        let path = e.path();
        if !path.is_file() { continue }
        let Some(stem) = path.file_stem().and_then(|s| s.to_str()) else { continue };
        match path.extension().and_then(|x| x.to_str()) {
            Some("parquet") => out.push((stem.to_string(), path, "parquet")),
            Some("csv") => out.push((stem.to_string(), path, "csv")),
            _ => {}
        }
    }
    out
}

// ── Bar resolution ──────────────────────────────────────────────────────────

#[derive(Debug)]
pub struct ResolvedBars {
    pub bars: Vec<Bar>,
    /// Human-readable origin, e.g. `"lake:binanceflat"` or `"project:BTCUSDT.csv"`.
    pub source: String,
    pub file_timeframe: Timeframe,
    pub resampled: bool,
}

/// Aggregate bars up to `target` with the stock `StandaloneAggregator`.
#[allow(deprecated)]
pub fn resample(bars: impl IntoIterator<Item = Bar>, target: Timeframe) -> Vec<Bar> {
    let mut agg = StandaloneAggregator::new(target);
    let mut out = Vec::new();
    for b in bars {
        if let Some(done) = agg.update(&b) {
            out.push(done);
        }
    }
    if let Some(tail) = agg.flush() {
        out.push(tail);
    }
    out
}

fn drain(mut feed: impl BarFeed) -> Vec<Bar> {
    std::iter::from_fn(|| feed.next()).collect()
}

/// Does `root` hold an exact `{*}/{tf}/{symbol}/` tree match? (Used by the backtest
/// path to decide whether the root can be handed to the engine as `data_dir` as-is.)
pub fn has_exact_tree(root: &Path, symbol: &str, tf: Timeframe) -> bool {
    let sym = symbol.to_lowercase();
    let tf_str = tf.to_string().to_lowercase();
    walk_tree(root).iter().any(|f| f.symbol == sym && f.timeframe == tf_str)
}

/// Resolve bars for `symbol` at `target_tf` across the tiers, in order. Within each
/// root: exact-TF tree files → project flat file → resample from the finest lower-TF
/// tree files. Returns `None` when no tier has the symbol at all.
pub fn resolve_bars(
    project_path: Option<&str>,
    symbol: &str,
    target_tf: Timeframe,
    from_ms: Option<i64>,
    to_ms: Option<i64>,
) -> Result<Option<ResolvedBars>, String> {
    let sym_lower = symbol.to_lowercase();
    let tf_lower = target_tf.to_string().to_lowercase();

    for root in data_roots(project_path) {
        let tree = walk_tree(&root.path);
        let sym_files: Vec<&TreeFile> = tree.iter().filter(|f| f.symbol == sym_lower).collect();

        // 1. Exact TF match.
        let exact: Vec<&Path> = sym_files
            .iter()
            .filter(|f| f.timeframe == tf_lower)
            .map(|f| f.path.as_path())
            .collect();
        if !exact.is_empty() {
            let provider = sym_files.iter().find(|f| f.timeframe == tf_lower).unwrap().provider.clone();
            let feed = ParquetFeed::load_many_filtered(&exact, symbol, from_ms, to_ms)
                .map_err(|e| format!("load {symbol} {target_tf}: {e}"))?;
            return Ok(Some(ResolvedBars {
                bars: drain(feed),
                source: format!("{}:{provider}", root.name),
                file_timeframe: target_tf,
                resampled: false,
            }));
        }

        // 2. Project flat file.
        if root.kind == "project" {
            if let Some((_, path, kind)) = flat_files(&root.path)
                .into_iter()
                .find(|(stem, _, _)| stem.to_lowercase() == sym_lower)
            {
                let bars = match kind {
                    "parquet" => {
                        let feed = ParquetFeed::load(&path, symbol)
                            .map_err(|e| format!("load {}: {e}", path.display()))?;
                        drain(feed)
                    }
                    _ => super::read_csv_bars(&path, symbol)?,
                };
                let file_tf = Timeframe::detect(&bars.iter().map(|b| b.timestamp).collect::<Vec<_>>());
                let name = path.file_name().map(|n| n.to_string_lossy().into_owned()).unwrap_or_default();
                if file_tf.duration_ms() < target_tf.duration_ms() {
                    let bars = window(resample(bars, target_tf), from_ms, to_ms);
                    return Ok(Some(ResolvedBars {
                        bars,
                        source: format!("{}:{name}", root.name),
                        file_timeframe: file_tf,
                        resampled: true,
                    }));
                }
                // Same TF (or coarser than asked — can't downsample; serve what exists).
                return Ok(Some(ResolvedBars {
                    bars: window(bars, from_ms, to_ms),
                    source: format!("{}:{name}", root.name),
                    file_timeframe: file_tf,
                    resampled: false,
                }));
            }
        }

        // 3. Resample from the finest lower TF available in this root.
        let mut lower: Vec<(&&TreeFile, Timeframe)> = sym_files
            .iter()
            .filter_map(|f| {
                let tf: Timeframe = f.timeframe.to_uppercase().parse().ok()?;
                (tf.duration_ms() < target_tf.duration_ms()).then_some((f, tf))
            })
            .collect();
        if !lower.is_empty() {
            lower.sort_by_key(|(_, tf)| tf.duration_ms());
            let base_tf = lower[0].1;
            let base_tf_lower = base_tf.to_string().to_lowercase();
            let paths: Vec<&Path> = sym_files
                .iter()
                .filter(|f| f.timeframe == base_tf_lower)
                .map(|f| f.path.as_path())
                .collect();
            let provider = sym_files.iter().find(|f| f.timeframe == base_tf_lower).unwrap().provider.clone();
            // Widen the read window one target-bar back so the first aggregated bar is complete.
            let widened_from = from_ms.map(|f| f - target_tf.duration_ms());
            let feed = ParquetFeed::load_many_filtered(&paths, symbol, widened_from, to_ms)
                .map_err(|e| format!("load {symbol} {base_tf}: {e}"))?;
            let bars = window(resample(drain(feed), target_tf), from_ms, to_ms);
            return Ok(Some(ResolvedBars {
                bars,
                source: format!("{}:{provider}", root.name),
                file_timeframe: base_tf,
                resampled: true,
            }));
        }
    }

    Ok(None)
}

fn window(bars: Vec<Bar>, from_ms: Option<i64>, to_ms: Option<i64>) -> Vec<Bar> {
    if from_ms.is_none() && to_ms.is_none() {
        return bars;
    }
    bars.into_iter()
        .filter(|b| from_ms.map_or(true, |f| b.timestamp >= f) && to_ms.map_or(true, |t| b.timestamp <= t))
        .collect()
}

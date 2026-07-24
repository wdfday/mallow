//! The one native backtest path — both the user's "Run Backtest" button and the agent's
//! `run_backtest` tool call [`run_for_project`], so they can never disagree. Runs
//! `alm_engine::backtest::run` (full dataset, real warm-up handling, MTF auto-detect)
//! against data resolved through the catalog tiers (project → mounts → lake).

use std::collections::HashMap;
use std::path::Path;

use alm_core::Timeframe;
use alm_engine::backtest as engine_backtest;
use alm_engine::backtest::loader::compute_warmup_bars;
use alm_engine::data::parse_date_ms;
use alm_engine::types::{BacktestRequest, ScriptBacktestRequest};
use alm_strategy::script::probe_script_htfs;
use serde_json::{json, Value};

use super::catalog;

pub fn run_for_project(
    project_path: &str,
    script: &str,
    symbol: &str,
    timeframe: Option<&str>,
    from: Option<&str>,
    to: Option<&str>,
    config: Option<&Value>,
) -> Result<Value, String> {
    let tf_str = timeframe.unwrap_or("M1").to_uppercase();
    let target_tf: Timeframe = tf_str.parse().map_err(|_| format!("invalid timeframe: {tf_str}"))?;

    let mut req_json = json!({
        "symbol": symbol,
        "script": script,
        "timeframe": tf_str,
        "from": from,
        "to": to,
    });
    // Extra ScriptBacktestRequest fields from the Config tab (sizing, reverse_policy, capital,
    // fees, …) — merged flat so this command doesn't re-enumerate the request schema.
    if let Some(Value::Object(cfg)) = config {
        let base = req_json.as_object_mut().expect("req_json is an object literal");
        for (k, v) in cfg {
            base.insert(k.clone(), v.clone());
        }
    }
    let script_req: ScriptBacktestRequest =
        serde_json::from_value(req_json).map_err(|e| e.to_string())?;
    let mut req: BacktestRequest = script_req.into();

    // Preferred path: a root whose tree has the exact {*}/{TF}/{SYMBOL}/ layout can be
    // handed to the engine as data_dir directly — its own loader then does discovery,
    // date filtering and warm-up loading (`load_bars_warmed`) exactly like herald.
    for root in catalog::data_roots(Some(project_path)) {
        if catalog::has_exact_tree(&root.path, symbol, target_tf) {
            let resp = engine_backtest::run(req, &root.path).map_err(|e| e.to_string())?;
            return serde_json::to_value(resp).map_err(|e| e.to_string());
        }
    }

    // Fallback: flat project files / resample-only roots. Load + resample ourselves and
    // feed the engine through history_overrides. The engine still applies the warm-up
    // gate (`with_warmup_until(from)`), but override bars are taken as-is — so we mirror
    // `load_bars_warmed`'s window: [from − warmup×tf, to].
    let resolved = catalog::resolve_bars(Some(project_path), symbol, target_tf, None, None)?
        .ok_or_else(|| {
            format!(
                "no data found for {symbol} — import a file into the project's data/, \
                 mount a data folder, or add the symbol in the Data panel"
            )
        })?;

    let from_ms = req.from.as_deref().and_then(parse_date_ms);
    let to_ms = req.to.as_deref().and_then(|s| parse_date_ms(s).map(|ms| ms + 86_400_000 - 1));
    let warmup = compute_warmup_bars(&req) as i64;
    let load_from = from_ms.map(|f| f - warmup * target_tf.duration_ms());

    let mut overrides: HashMap<String, Vec<alm_core::Bar>> = HashMap::new();
    // HTF feeds for MTF scripts are resampled from the *full* resolved history — HTF
    // warm-up reaches much further back in calendar time than the base window.
    for htf in probe_script_htfs(script) {
        overrides.insert(htf.to_string(), catalog::resample(resolved.bars.iter().cloned(), htf));
    }
    let base: Vec<alm_core::Bar> = resolved
        .bars
        .into_iter()
        .filter(|b| load_from.map_or(true, |f| b.timestamp >= f) && to_ms.map_or(true, |t| b.timestamp <= t))
        .collect();
    overrides.insert(tf_str, base);
    req.history_overrides = Some(overrides);

    let data_dir = Path::new(project_path).join("data"); // unused when overrides hit, still must exist as a path
    let resp = engine_backtest::run(req, &data_dir).map_err(|e| e.to_string())?;
    serde_json::to_value(resp).map_err(|e| e.to_string())
}

/// User-facing command. `(async)` so the engine run lands on a background thread — a
/// long backtest must not freeze the webview's IPC.
#[tauri::command(async)]
pub fn backtest_run(
    project_path: String,
    script: String,
    symbol: String,
    timeframe: Option<String>,
    from: Option<String>,
    to: Option<String>,
    config: Option<Value>,
) -> Result<Value, String> {
    run_for_project(
        &project_path,
        &script,
        &symbol,
        timeframe.as_deref(),
        from.as_deref(),
        to.as_deref(),
        config.as_ref(),
    )
}

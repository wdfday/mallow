use alm_core::Timeframe;
use alm_strategy::script::script_lint;
use serde_json::{json, Value};

/// The two tools available to the self-correcting loop — matches the research doc's decision
/// ("agent viết Rhai... self-correcting loop: sửa code → validate → backtest lại"). Both are
/// native Rust calls into the same crates herald links (not WASM — that's browser-only), so the
/// agent gets the identical production-grade lint/backtest behavior the Editor panel already
/// exposes to the user.
pub fn tool_specs() -> Vec<Value> {
    vec![
        json!({
            "name": "lint_script",
            "description": "Validate a Rhai strategy script for syntax/semantic errors before backtesting it.",
            "parameters": {
                "type": "object",
                "properties": {
                    "script": { "type": "string", "description": "Full Rhai script source" },
                    "base_tf": { "type": "string", "description": "Base timeframe, e.g. M15, H1 (optional)" }
                },
                "required": ["script"]
            }
        }),
        json!({
            "name": "run_backtest",
            "description": "Run the Rhai script through the real alm-engine backtester against the project's local OHLCV data (data/{symbol}.parquet or .csv) and return performance metrics + trades.",
            "parameters": {
                "type": "object",
                "properties": {
                    "script": { "type": "string" },
                    "symbol": { "type": "string" },
                    "timeframe": { "type": "string", "description": "Bar timeframe, e.g. M1, M15, H1 (default M1)" },
                    "from": { "type": "string", "description": "YYYY-MM-DD, optional" },
                    "to": { "type": "string", "description": "YYYY-MM-DD, optional" }
                },
                "required": ["script", "symbol"]
            }
        }),
    ]
}

pub fn execute(name: &str, args: &Value, project_path: &str) -> Result<Value, String> {
    match name {
        "lint_script" => run_lint(args),
        "run_backtest" => run_backtest(args, project_path),
        other => Err(format!("unknown tool: {other}")),
    }
}

fn run_lint(args: &Value) -> Result<Value, String> {
    let script = args.get("script").and_then(|v| v.as_str()).ok_or("missing 'script'")?;
    let base_tf = args
        .get("base_tf")
        .and_then(|v| v.as_str())
        .and_then(|s| s.parse::<Timeframe>().ok());
    let (diagnostics, _scope) = script_lint(script, base_tf);
    serde_json::to_value(diagnostics).map_err(|e| e.to_string())
}

fn run_backtest(args: &Value, project_path: &str) -> Result<Value, String> {
    let script = args.get("script").and_then(|v| v.as_str()).ok_or("missing 'script'")?;
    let symbol = args.get("symbol").and_then(|v| v.as_str()).ok_or("missing 'symbol'")?;
    let timeframe = args.get("timeframe").and_then(|v| v.as_str());
    let from = args.get("from").and_then(|v| v.as_str());
    let to = args.get("to").and_then(|v| v.as_str());

    // Same function the user's Run Backtest button invokes (`data::backtest::backtest_run`)
    // — tiered data resolution (project → mounts → lake) + native alm-engine run. No config
    // override: the agent's runs use the strategy file's own frontmatter + engine defaults.
    crate::data::backtest::run_for_project(project_path, script, symbol, timeframe, from, to, None)
}

/// Short human-readable line for the live `agent://step` transcript — the full JSON result still
/// goes back to the model, this is only what `ToolStepRow` in `AgentPanel.tsx` renders inline.
pub fn summarize(name: &str, result: &Value) -> String {
    match name {
        "lint_script" => {
            let errs = result.as_array().map(|a| a.iter().filter(|d| d.get("severity").and_then(|s| s.as_str()) == Some("error")).count()).unwrap_or(0);
            let warns = result.as_array().map(|a| a.len()).unwrap_or(0).saturating_sub(errs);
            format!("{errs} errors, {warns} warnings")
        }
        "run_backtest" => {
            let ret = result.pointer("/returns/total_pct").and_then(|v| v.as_f64());
            match ret {
                Some(r) => format!("backtest done — return {r:.2}%"),
                None => "backtest done".to_string(),
            }
        }
        _ => "done".to_string(),
    }
}

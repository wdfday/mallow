//! Rhai-powered stream evaluator for herald SSE.
//!
//! Unlike [`RhaiStrategy`] which manages its own indicator state, `RhaiStreamEval`
//! reads indicator values from an already-warmed external source (the ledger).
//! This eliminates cold-start: a new SSE connection gets correct indicator values
//! from the first bar because the ledger has been running continuously.

use std::collections::HashMap;
use std::sync::{Arc, Mutex};

use alm_core::{Bar, Timeframe};
use anyhow::Result;
use rhai::{Array, Dynamic, Engine, Scope, AST};
use serde_json::json;

use super::rhai_strategy::{
    build_engine, extract_max_lookback, map_indicator_type, try_parse_indicator_line,
    PlotBuf, BAR_FIELDS, DEFAULT_BUF_DEPTH,
};

/// One indicator declaration parsed from the script.
///
/// Herald uses `spec_config` + `source_tf` to build an `IndicatorSpec` and
/// call `ledger.acquire_indicator`. We don't depend on `alm-ledger` here to
/// keep the dependency graph acyclic.
pub struct StreamDecl {
    /// Variable name in the script, e.g. `"ema20"` from `let ema20 = ind.ema(20)`.
    pub var_name:   String,
    /// JSON config to pass to `IndicatorSpec::from_config`.
    pub spec_config: serde_json::Value,
    /// Optional MTF source timeframe string, e.g. `"H1"`.
    pub source_tf:  Option<Timeframe>,
    /// Output field to read from the cell, e.g. `"value"`, `"histogram"`.
    pub field:      String,
    /// Number of historical values the script needs (default 2 for crossover).
    pub buf_depth:  usize,
}

pub type PlotResult = HashMap<String, f64>;

/// Stateless Rhai evaluator backed by ledger-provided indicator arrays.
///
/// Call [`RhaiStreamEval::run`] once per bar with fresh indicator values; it
/// returns the `plot()` outputs from the script for that bar.
pub struct RhaiStreamEval {
    engine:        Engine,
    ast:           AST,
    plot_buf:      PlotBuf,
    decls:         Vec<StreamDecl>,
    bar_buf_depth: usize,
}

impl RhaiStreamEval {
    pub fn from_script(script: &str) -> Result<Self> {
        let mut decls_raw   = Vec::new();
        let mut clean_lines: Vec<&str> = Vec::new();

        for line in script.lines() {
            match try_parse_indicator_line(line) {
                Some(d) => decls_raw.push(d),
                None    => clean_lines.push(line),
            }
        }

        let cleaned = clean_lines.join("\n");
        let plot_buf: PlotBuf = Arc::new(Mutex::new(Vec::new()));
        let engine  = build_engine(Arc::clone(&plot_buf));
        let ast     = engine
            .compile(&cleaned)
            .map_err(|e| anyhow::anyhow!("Rhai compile error: {e}"))?;

        let lookback = extract_max_lookback(&cleaned);
        let bar_buf_depth = decls_raw.iter()
            .map(|d| d.buf_depth)
            .max()
            .unwrap_or(DEFAULT_BUF_DEPTH)
            .max(lookback)
            .max(DEFAULT_BUF_DEPTH);

        let mut decls = Vec::with_capacity(decls_raw.len());
        for d in decls_raw {
            let (ind_type, field) = map_indicator_type(&d.ind_type);
            let spec_config = make_spec_config(&ind_type, d.period);
            decls.push(StreamDecl {
                var_name: d.var_name,
                spec_config,
                source_tf: d.timeframe,
                field,
                buf_depth: d.buf_depth,
            });
        }

        Ok(Self { engine, ast, plot_buf, decls, bar_buf_depth })
    }

    /// Parsed indicator declarations — acquire ledger handles from these.
    pub fn decls(&self) -> &[StreamDecl] {
        &self.decls
    }

    pub fn bar_buf_depth(&self) -> usize {
        self.bar_buf_depth
    }

    /// Run the script for one bar.
    ///
    /// `indicator_arrays`: keyed by `StreamDecl::var_name`, newest-first values
    /// read from the ledger cell column (length = decl.buf_depth).
    /// `bar_history`: newest-first OHLCV bars (length = bar_buf_depth).
    ///
    /// Returns the `plot("name", value)` outputs emitted by the script.
    pub fn run(
        &self,
        indicator_arrays: &HashMap<String, Vec<f64>>,
        bar_history: &[Bar],
    ) -> PlotResult {
        if let Ok(mut buf) = self.plot_buf.lock() {
            buf.clear();
        }

        let mut scope = Scope::new();

        for decl in &self.decls {
            let arr: Array = indicator_arrays
                .get(&decl.var_name)
                .map(|vals| vals.iter().map(|&v| Dynamic::from_float(v)).collect())
                .unwrap_or_default();
            scope.push_dynamic(decl.var_name.as_str(), Dynamic::from_array(arr));
        }

        for field in BAR_FIELDS {
            let arr: Array = bar_history.iter().map(|b| {
                Dynamic::from_float(match *field {
                    "open"   => b.open,
                    "high"   => b.high,
                    "low"    => b.low,
                    "close"  => b.close,
                    "volume" => b.volume,
                    _        => 0.0,
                })
            }).collect();
            scope.push_dynamic(*field, Dynamic::from_array(arr));
        }

        // Run the compiled script — errors are silently ignored (indicator may
        // not yet be warm enough for the script's conditionals to fire).
        let _ = self.engine.run_ast_with_scope(&mut scope, &self.ast);

        self.plot_buf.lock()
            .map(|buf| buf.iter().cloned().collect())
            .unwrap_or_default()
    }
}

fn make_spec_config(ind_type: &str, period: usize) -> serde_json::Value {
    match ind_type {
        "macd"          => json!({"type": "macd",          "fast": period, "slow": 26, "signal": 9}),
        "bbands"        => json!({"type": "bbands",        "period": period, "multiplier": 2.0}),
        "stochastic"    => json!({"type": "stochastic",    "k_period": period, "d_period": 3}),
        "stoch_rsi"     => json!({"type": "stoch_rsi",     "rsi_period": period, "smooth_d": 3}),
        "supertrend"    => json!({"type": "supertrend",    "period": period, "multiplier": 3.0}),
        "donchian"      => json!({"type": "donchian",      "period": period}),
        "parabolic_sar" => json!({"type": "parabolic_sar", "step": 0.02, "max": 0.2}),
        "kama"          => json!({"type": "kama",          "er_period": period}),
        "obv"           => json!({"type": "obv"}),
        "vwap"          => json!({"type": "vwap"}),
        "ao"            => json!({"type": "ao",            "fast": 5,    "slow": 34}),
        "bop"           => json!({"type": "bop"}),
        "coppock"       => json!({"type": "coppock"}),
        "uo"            => json!({"type": "uo",            "fast": 7, "medium": 14, "slow": 28}),
        "vortex"        => json!({"type": "vortex",        "period": period}),
        t               => json!({"type": t, "period": period}),
    }
}

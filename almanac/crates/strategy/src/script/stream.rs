//! Script-driven stream evaluator for herald SSE.
//!
//! Unlike [`ScriptStrategy`] which manages its own indicator state, `ScriptStreamEval`
//! reads indicator values from an already-warmed external source (the ledger).
//! This eliminates cold-start: a new SSE connection gets correct indicator values
//! from the first bar because the ledger has been running continuously.

use std::collections::HashMap;
use std::sync::{Arc, Mutex};

use alm_core::{Bar, Timeframe};
use anyhow::Result;
use rhai::{Array, Dynamic, Engine, Scope, AST};

use super::engine::{build_engine, extract_max_lookback, PlotBuf, BAR_FIELDS, DEFAULT_BUF_DEPTH};
use super::parse::{indicator_json_config, try_parse_indicator_line, IndicatorKind};

// ── StreamDecl ────────────────────────────────────────────────────────────────

/// One indicator declaration parsed from the script.
///
/// Herald uses `spec_config` + `source_tf` to build an `IndicatorSpec` and
/// call `ledger.acquire_indicator`.
pub struct StreamDecl {
    /// Variable name in the script, e.g. `"ema20"` from `let ema20 = ind.ema(20)`.
    pub var_name:    String,
    /// JSON config to pass to `IndicatorSpec::from_config`.
    pub spec_config: serde_json::Value,
    /// Optional MTF source timeframe string, e.g. `"H1"`.
    pub source_tf:   Option<Timeframe>,
    /// Output field to read from the cell, e.g. `"value"`, `"histogram"`.
    /// `None` = multi-output indicator — herald must supply all fields as a Map.
    pub field:       Option<String>,
    /// For multi-output indicators: the semantic primary field used when the
    /// element is compared directly as a number (e.g. `"macd"` for macd,
    /// `"middle"` for bbands). `None` for single-output indicators.
    pub primary:     Option<String>,
    /// Number of historical values the script needs (default 2 for crossover).
    pub buf_depth:   usize,
}

// ── IndicatorSnapshot ─────────────────────────────────────────────────────────

/// Per-variable indicator data supplied to [`ScriptStreamEval::run`].
///
/// Herald reads values from the ledger cell and packages them here.
pub enum IndicatorSnapshot {
    /// Single field — newest value at index 0.
    Single(Vec<f64>),
    /// Multi-field — newest snapshot at index 0; keys are IndicatorBox field names.
    Multi(Vec<HashMap<String, f64>>),
}

pub type PlotResult = HashMap<String, f64>;

// ── ScriptStreamEval ────────────────────────────────────────────────────────────

/// Stateless script evaluator backed by ledger-provided indicator snapshots.
///
/// Call [`ScriptStreamEval::run`] once per bar with fresh indicator values; it
/// returns the `plot()` outputs from the script for that bar.
pub struct ScriptStreamEval {
    engine:        Engine,
    ast:           AST,
    plot_buf:      PlotBuf,
    decls:         Vec<StreamDecl>,
    bar_buf_depth: usize,
}

impl ScriptStreamEval {
    pub fn from_script(script: &str) -> Result<Self> {
        let mut raw_decls    = Vec::new();
        let mut cleaned_lines: Vec<&str> = Vec::new();

        for line in script.lines() {
            match try_parse_indicator_line(line) {
                Some(d) => raw_decls.push(d),
                None    => cleaned_lines.push(line),
            }
        }

        let cleaned   = cleaned_lines.join("\n");
        let plot_buf: PlotBuf = Arc::new(Mutex::new(Vec::new()));
        let engine    = build_engine(Arc::clone(&plot_buf));
        let ast       = engine
            .compile(&cleaned)
            .map_err(|e| anyhow::anyhow!("script compile error: {e}"))?;

        let lookback = extract_max_lookback(&cleaned);
        let bar_buf_depth = raw_decls.iter()
            .map(|d| d.buf_depth)
            .max()
            .unwrap_or(DEFAULT_BUF_DEPTH)
            .max(lookback)
            .max(DEFAULT_BUF_DEPTH);

        let decls = raw_decls.into_iter().map(|d| {
            let (field, primary) = match d.kind {
                IndicatorKind::Single(f)  => (Some(f), None),
                IndicatorKind::Multi(p)   => (None, Some(p)),
            };
            StreamDecl {
                spec_config: indicator_json_config(&d.ind_type, d.period),
                var_name:    d.var_name,
                source_tf:   d.timeframe,
                field,
                primary,
                buf_depth:   d.buf_depth,
            }
        }).collect();

        Ok(Self { engine, ast, plot_buf, decls, bar_buf_depth })
    }

    /// Parsed indicator declarations — herald acquires ledger handles from these.
    pub fn decls(&self) -> &[StreamDecl] { &self.decls }

    pub fn bar_buf_depth(&self) -> usize { self.bar_buf_depth }

    /// Run the script for one bar.
    ///
    /// `indicators`: keyed by `StreamDecl::var_name`.
    ///   - `Single` → newest-first `Vec<f64>`, length = decl.buf_depth.
    ///   - `Multi`  → newest-first `Vec<HashMap<String,f64>>`, length = decl.buf_depth.
    /// `bar_history`: newest-first OHLCV bars (length = bar_buf_depth).
    ///
    /// Returns the `plot("name", value)` outputs emitted by the script.
    pub fn run(
        &self,
        indicators:  &HashMap<String, IndicatorSnapshot>,
        bar_history: &[Bar],
    ) -> PlotResult {
        if let Ok(mut buf) = self.plot_buf.lock() { buf.clear(); }

        let mut scope = Scope::new();

        for decl in &self.decls {
            let dyn_arr: Array = match indicators.get(&decl.var_name) {
                Some(IndicatorSnapshot::Single(vals)) =>
                    vals.iter().map(|&v| Dynamic::from_float(v)).collect(),
                Some(IndicatorSnapshot::Multi(snapshots)) => {
                    let primary = decl.primary.clone().unwrap_or_else(|| "value".to_string());
                    snapshots.iter().map(|fields| {
                        Dynamic::from(super::binding::MEntry::new(
                            fields.iter().map(|(k, &v)| (k.clone(), v)).collect(),
                            primary.clone(),
                        ))
                    }).collect()
                }
                None => Array::new(),
            };
            scope.push_dynamic(decl.var_name.as_str(), Dynamic::from_array(dyn_arr));
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

        let _ = self.engine.run_ast_with_scope(&mut scope, &self.ast);

        self.plot_buf.lock()
            .map(|buf| buf.iter().cloned().collect())
            .unwrap_or_default()
    }
}

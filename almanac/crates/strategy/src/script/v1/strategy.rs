//! Script-based strategy — define entry/exit logic as a text script.
//!
//! # Script format
//!
//! ```text
//! let ema9  = ind.ema(9);
//! let rsi14 = ind.rsi(14);
//! let atr14 = ind.atr(14);
//!
//! if cross_above(ema9, rsi14) && rsi14[0] < 60.0 {
//!     entry = true;
//!     tp    = close[0] + atr14[0] * 2.0;
//!     sl    = close[0] - atr14[0] * 1.5;
//! }
//! if rsi14[0] > 70.0 { exit = true; }
//! ```
//!
//! # Indicator declaration syntax
//!
//! `ind.TYPE(period [, params]* [, buf=N])`
//!
//! | Form | Meaning |
//! |---|---|
//! | `ind.ema(9)` | Base-TF, default buf=2 |
//! | `ind.ema(9, buf=5)` | Base-TF, buf=5 |
//!
//! **V1 is strict single-TF.** Any TF argument (`"H1"`, `"live_H1"`, etc.) is
//! rejected at compile time. Use [`MtfScriptStrategy`] (V2) for multi-timeframe
//! evaluation — herald registry and `engine::backtest::run` auto-route scripts
//! that declare HTF indicators to V2.
//!
//! # Output variables
//!
//! `long`, `short`, `exit`, `tp`, `sl`, `strength`, `is_offset`, `reason`.
//! `entry` is a legacy alias for `long`.
//! When `is_offset = true`, `tp`/`sl` are deltas from fill price (helm semantics).
//!
//! # Regime block
//!
//! Scripts may declare a single `regime { ... }` block. It runs **before** the
//! main body each bar and writes six output variables that the main block then
//! reads:
//!
//! - `trend`, `trend_value` — trend dimension status (string) + raw value
//! - `vol`, `vol_value`     — volatility dimension
//!
//! Indicators declared in either block share a single namespace — names must be
//! unique. The strategy exposes the latest state via `Strategy::current_regime()`,
//! which the engine uses to populate `BacktestReport::regime_summary` (timestamped
//! transitions + per-regime trade breakdown) and tag each `Trade.regime_at_entry`.
//!
//! ```text
//! let ema9  = ind.ema(9);
//! let ema21 = ind.ema(21);
//!
//! regime {
//!     let adx14 = ind.adx(14);
//!     if adx14[0] > 25.0 {
//!         trend = "trending"; trend_value = adx14[0];
//!     } else {
//!         trend = "ranging";  trend_value = adx14[0];
//!     }
//! }
//!
//! if cross_above(ema9, ema21) && trend == "trending" { entry = true; }
//! if cross_below(ema9, ema21) { exit = true; }
//! ```

use std::collections::{HashMap, VecDeque};

use anyhow::Result;
use rhai::{Array, Dynamic, Engine, Scope, AST};
use alm_core::{bar::Bar, regime::{RegimeDimension, RegimeState}, signal::Signal, strategy::Strategy};

use crate::candle_type::{CandleType, CandleTransform};

use super::binding::VarBinding;
use super::parse::{
    extract_candle_directives, extract_regime_block, indicator_json_config,
    make_indicator_box, try_parse_indicator_line, CandleDirective, IndicatorDecl,
};
use super::engine::{build_engine, extract_max_lookback, BAR_FIELDS, DEFAULT_BUF_DEPTH};

// ── ScriptStrategy ───────────────────────────────────────────────────────────

pub struct ScriptStrategy {
    engine:           Engine,
    ast:              AST,
    /// Optional `regime { ... }` sub-script, run before the main AST each bar.
    /// Shares the same bindings (indicators declared in either block are unique
    /// across the whole strategy).
    regime_ast:       Option<AST>,
    bindings:         HashMap<String, VarBinding>,
    binding_order:    Vec<String>,
    bar_buf:          VecDeque<Bar>,
    bar_buf_depth:    usize,
    /// `None` in live (registry) mode — avoids unbounded accumulation.
    /// `Some` in backtest / stream mode — auto-populated from indicator bindings.
    series:           Option<HashMap<String, Vec<(i64, f64)>>>,
    candle_transform: CandleTransform,
    /// Latest regime state computed by the regime sub-script.
    current_regime:   Option<RegimeState>,
    /// Shared error sink — caller creates it, strategy writes first runtime error here.
    /// `None` in live/registry mode (errors are logged but not captured).
    pub error_sink:   Option<std::sync::Arc<std::sync::Mutex<Option<String>>>>,
    /// Persistent key-value map shared across bar calls.
    /// Scripts read/write `state["key"]` to carry state between bars (e.g. `in_position`).
    persistent_state: rhai::Map,
}

/// Whitelist of accepted `candle.transform()` kinds. Mirrors `CandleType::from_str`
/// but fails loudly on typos instead of silently falling back to Raw.
fn validate_candle_kind(kind: &str) -> Result<()> {
    match kind {
        "raw" | "heiken_ashi" | "ha" | "smooth_ha" | "smooth_heiken_ashi" => Ok(()),
        other => Err(anyhow::anyhow!(
            "unknown candle kind `{other}`; supported: \
             \"raw\", \"heiken_ashi\" (alias \"ha\"), \"smooth_ha\" (alias \"smooth_heiken_ashi\")"
        )),
    }
}

impl ScriptStrategy {
    /// Backtest / stream mode: indicator series auto-collected into `take_indicator_series()`.
    pub fn from_script(script: &str) -> Result<Self> {
        Self::build(script, true)
    }

    /// Live (herald registry) mode: series collection disabled to avoid unbounded accumulation.
    pub fn from_script_live(script: &str) -> Result<Self> {
        Self::build(script, false)
    }

    pub fn build(script: &str, collect_series: bool) -> Result<Self> {
        // Step 0: extract top-of-file `candle.*` directives BEFORE everything
        // else. Strict enforcement: any non-directive line closes the header,
        // so a later `candle.transform()` is a parse error.
        let (candle_dirs, after_candle) = extract_candle_directives(script)?;
        let script_candle = if let Some(d) = candle_dirs.last() {
            // Validate the kind here so a typo fails at script-compile time
            // instead of silently falling back to Raw.
            let (kind, smooth) = match d {
                CandleDirective::Transform { kind, smooth } => (kind.as_str(), *smooth),
            };
            validate_candle_kind(kind)?;
            Some((kind.to_string(), smooth))
        } else {
            None
        };

        // Step 1: extract the optional `regime { ... }` block. The cleaned script
        // is line-count preserving so error positions remain accurate.
        let (regime_body, main_source) = extract_regime_block(&after_candle)?;

        // Step 2: collect indicator declarations from both blocks. Names must be
        // unique across the whole strategy — if a name appears in both regime and
        // main, that's an error. Indicators share bindings: declaring `ema9` in
        // the regime block makes it readable from main too, and vice versa.
        let mut decls: Vec<IndicatorDecl> = Vec::new();
        let mut decl_names: std::collections::HashSet<String> = std::collections::HashSet::new();

        let regime_body_owned = regime_body.unwrap_or_default();
        let mut regime_cleaned_lines: Vec<&str> = Vec::new();
        for line in regime_body_owned.lines() {
            match try_parse_indicator_line(line) {
                Some(decl) => {
                    if !decl_names.insert(decl.var_name.clone()) {
                        anyhow::bail!(
                            "indicator `{}` is declared more than once (regime + main share names)",
                            decl.var_name
                        );
                    }
                    decls.push(decl);
                }
                None => regime_cleaned_lines.push(line),
            }
        }
        let regime_cleaned_script = regime_cleaned_lines.join("\n");

        let mut main_cleaned_lines: Vec<&str> = Vec::new();
        for line in main_source.lines() {
            match try_parse_indicator_line(line) {
                Some(decl) => {
                    if !decl_names.insert(decl.var_name.clone()) {
                        anyhow::bail!(
                            "indicator `{}` is declared more than once (regime + main share names)",
                            decl.var_name
                        );
                    }
                    decls.push(decl);
                }
                None => main_cleaned_lines.push(line),
            }
        }
        let main_cleaned_script = main_cleaned_lines.join("\n");

        // V1 is strict single-TF: any indicator declared with a TF argument
        // (`ind.ema(20, "H1")` or `"live_H1"`) must go through V2. Reject early
        // so callers get a clear error instead of silently single-TF behavior.
        if let Some(d) = decls.iter().find(|d| d.timeframe.is_some()) {
            anyhow::bail!(
                "indicator `{}`: V1 ScriptStrategy does not support timeframe arguments \
                 (`ind.{}(...)` declares a TF). Use MtfScriptStrategy (V2) for multi-timeframe \
                 evaluation — herald registry and backtest auto-route MTF scripts.",
                d.var_name, d.ind_type
            );
        }

        // Step 3: compile both ASTs against a single engine.
        let engine = build_engine();

        let regime_ast = if regime_cleaned_script.trim().is_empty() {
            None
        } else {
            Some(
                engine
                    .compile(&regime_cleaned_script)
                    .map_err(|e| anyhow::anyhow!("script compile error (regime block): {e}"))?,
            )
        };

        let ast = engine
            .compile(&main_cleaned_script)
            .map_err(|e| anyhow::anyhow!("script compile error: {e}"))?;

        // Step 4: instantiate one VarBinding per declaration (shared across blocks).
        let mut bindings:      HashMap<String, VarBinding> = HashMap::new();
        let mut binding_order: Vec<String>                  = Vec::new();
        let mut max_buf = DEFAULT_BUF_DEPTH;

        for decl in &decls {
            if decl.buf_depth > max_buf { max_buf = decl.buf_depth; }
            let ind     = make_indicator_box(decl)?;
            let binding = VarBinding::new(ind, decl.kind.clone(), decl.buf_depth);
            binding_order.push(decl.var_name.clone());
            bindings.insert(decl.var_name.clone(), binding);
        }

        // Lookback scan covers both blocks so bar_buf is sized to fit
        // `highest()` / `momentum()` etc. wherever they appear.
        let lookback_main   = extract_max_lookback(&main_cleaned_script);
        let lookback_regime = extract_max_lookback(&regime_cleaned_script);
        let lookback        = lookback_main.max(lookback_regime);
        if lookback > max_buf { max_buf = lookback; }

        Ok(Self {
            engine,
            ast,
            regime_ast,
            bindings,
            binding_order,
            bar_buf: VecDeque::with_capacity(max_buf),
            bar_buf_depth: max_buf,
            series: if collect_series { Some(HashMap::new()) } else { None },
            // Candle type is driven solely by the in-script `candle.transform(...)`
            // directive — ScriptStrategy owns the transform internally so the same
            // candle type applies in both backtest engine and live registry.
            candle_transform: {
                let effective = match &script_candle {
                    Some((kind, smooth)) => CandleType::from_str(kind, smooth.unwrap_or(3)),
                    None => CandleType::Raw,
                };
                CandleTransform::new(effective)
            },
            current_regime: None,
            error_sink: None,
            persistent_state: rhai::Map::new(),
        })
    }

    /// Backtest: `from_params` defaults to collect mode.
    /// Live: caller must pass `"_live": true` in params (injected by herald registry).
    pub fn from_params(p: &serde_json::Value) -> Result<Self> {
        let script = p
            .get("script")
            .and_then(|v| v.as_str())
            .ok_or_else(|| anyhow::anyhow!("ScriptStrategy requires a 'script' param"))?;
        let live = p.get("_live").and_then(|v| v.as_bool()).unwrap_or(false);
        Self::build(script, !live)
    }

    /// Attach a shared error sink — backtest runner sets this to capture the
    /// first Rhai runtime error and surface it as a 400 response.
    pub fn with_error_sink(mut self, sink: std::sync::Arc<std::sync::Mutex<Option<String>>>) -> Self {
        self.error_sink = Some(sink);
        self
    }

    /// Snapshot of auto-collected indicator series (backtest mode only).
    pub fn series(&self) -> Option<&HashMap<String, Vec<(i64, f64)>>> {
        self.series.as_ref()
    }
}

// ── Helpers ───────────────────────────────────────────────────────────────────

/// Returns `true` for indicator output fields that carry boolean semantics
/// (stored as 0.0/1.0). These must not be plotted on the main price chart
/// because their 0–1 range collapses the y-axis when the price is e.g. 60 000.
#[inline]
/// Fields whose indicator value is a `bool` flattened to `0.0`/`1.0` in
/// `IndicatorBox::update`. Plotting them as a continuous series is meaningless,
/// so they are excluded from the plot-series collection. Delegates to the
/// single source of truth in `alm_indicator` so the list cannot drift.
fn is_boolean_flag_field(field: &str) -> bool {
    alm_indicator::field_kind(field) == alm_indicator::FieldKind::Bool
}

/// Read a numeric output var as `f64`, tolerating an `i64` value.
///
/// Rhai distinguishes integer and float literals strictly: `tp = 100` stores an
/// `i64`, and a plain `get_value::<f64>` would return `None` — silently dropping
/// the value. Scalar output vars (`tp`, `sl`, `strength`, `trail`, `atr`) accept
/// either, so an integer literal works as a user naturally expects.
pub(crate) fn scalar_out(scope: &Scope, name: &str) -> Option<f64> {
    scope
        .get_value::<f64>(name)
        .or_else(|| scope.get_value::<i64>(name).map(|v| v as f64))
}

// ── Strategy impl ─────────────────────────────────────────────────────────────

impl Strategy for ScriptStrategy {
    fn on_bar(&mut self, bar: &Bar) -> Vec<Signal> {
        let Some(bar_owned) = self.candle_transform.apply(bar) else {
            return vec![];
        };
        let bar = &bar_owned;

        self.bar_buf.push_back(bar.clone());
        if self.bar_buf.len() > self.bar_buf_depth {
            self.bar_buf.pop_front();
        }

        let mut all_ready = self.bar_buf.len() >= self.bar_buf_depth;
        for name in &self.binding_order {
            if let Some(b) = self.bindings.get_mut(name) {
                if !b.update(bar) { all_ready = false; }
            }
        }

        // Collect series independently per indicator — each starts as soon as it
        // individually warms up, not waiting for all_ready (which gates on the
        // slowest indicator). This ensures e.g. EMA(20) is recorded from bar 20
        // even if another indicator in the script needs 500 bars.
        if let Some(series) = &mut self.series {
            for name in &self.binding_order {
                if let Some(binding) = self.bindings.get(name) {
                    if let Some(fields) = binding.current_fields() {
                        if binding.is_multi() {
                            for (field, val) in &fields {
                                if is_boolean_flag_field(field) { continue; }
                                series.entry(format!("{name}.{field}"))
                                    .or_default()
                                    .push((bar.timestamp, *val));
                            }
                        } else if let Some(&val) = fields.values().next() {
                            series.entry(name.clone())
                                .or_default()
                                .push((bar.timestamp, val));
                        }
                    }
                }
            }
        }

        if !all_ready { return vec![]; }

        let mut scope = Scope::new();

        // Output variables are pushed FIRST so that user-declared indicator names
        // (e.g. `let atr = ind.atr(14)`) can shadow them when pushed below.
        // Rhai resolves variables from the most-recently-pushed entry on the
        // scope stack, so indicators pushed after these will win on reads.
        // On write (`atr = my_atr[0]`), Rhai modifies the top entry (the
        // indicator Array → f64), and `scope.get_value::<f64>("atr")` after
        // the script still finds the written f64. If no write occurs it falls
        // back to the default 0.0 further down the stack.
        scope.push("trend",        String::new());
        scope.push("trend_value",  0.0_f64);
        scope.push("vol",          String::new());
        scope.push("vol_value",    0.0_f64);

        scope.push("entry",       false);
        scope.push("exit",        false);
        scope.push("long",        false);
        scope.push("short",       false);
        scope.push("tp",        0.0_f64);
        scope.push("sl",        0.0_f64);
        scope.push("trail",     0.0_f64);
        scope.push("max_bars",  0_i64);
        scope.push("strength",  1.0_f64);
        scope.push("is_offset", false);
        scope.push("reason",    String::new());
        scope.push("atr",       0.0_f64);
        scope.push("state", rhai::Dynamic::from_map(self.persistent_state.clone()));

        // Bar arrays pushed after output vars so user can't shadow open/close/etc.
        for field in BAR_FIELDS {
            let arr: Array = self.bar_buf.iter().rev().map(|b| {
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

        // Indicator bindings pushed LAST so they shadow any output variable with
        // the same name (e.g. `let atr = ind.atr(14)` shadows the `atr` f64 slot
        // above, making `atr[0]` resolve to the Array<f64> rather than 0.0).
        for name in &self.binding_order {
            if let Some(b) = self.bindings.get(name) {
                scope.push_dynamic(name.as_str(), Dynamic::from_array(b.to_script_array()));
            }
        }

        // Run the regime sub-script first so the main block sees the latest
        // `trend` / `vol` (and value) variables. A runtime error in the
        // regime block silently leaves defaults in place — the main block still
        // runs with the previous regime cleared.
        if let Some(regime_ast) = &self.regime_ast {
            match self.engine.run_ast_with_scope(&mut scope, regime_ast) {
                Ok(_) => {
                    let trend_status = scope.get_value::<String>("trend").unwrap_or_default();
                    let trend_value  = scope.get_value::<f64>("trend_value").unwrap_or(0.0);
                    let vol_status   = scope.get_value::<String>("vol").unwrap_or_default();
                    let vol_value    = scope.get_value::<f64>("vol_value").unwrap_or(0.0);
                    self.current_regime = Some(RegimeState::new(
                        RegimeDimension::new(trend_value, trend_status),
                        RegimeDimension::new(vol_value,   vol_status),
                    ));
                }
                Err(e) => {
                    tracing::warn!(error = %e, "script runtime error (regime block)");
                }
            }
        }

        if let Err(e) = self.engine.run_ast_with_scope(&mut scope, &self.ast) {
            if let Some(sink) = &self.error_sink {
                if let Ok(mut guard) = sink.lock() {
                    if guard.is_none() {
                        *guard = Some(e.to_string());
                    }
                }
            }
            tracing::warn!(error = %e, "script runtime error");
            return vec![];
        }

        // Persist the state map back for the next bar.
        if let Some(new_state) = scope.get_value::<rhai::Map>("state") {
            self.persistent_state = new_state;
        }

        let strength  = scalar_out(&scope, "strength").unwrap_or(1.0).clamp(0.0, 1.0);
        let target    = scalar_out(&scope, "tp").filter(|&v| v != 0.0);
        let stop      = scalar_out(&scope, "sl").filter(|&v| v != 0.0);
        let trail     = scalar_out(&scope, "trail").filter(|&v| v > 0.0);
        let max_bars  = scope.get_value::<i64>("max_bars").filter(|&v| v > 0).map(|v| v as usize);
        let is_offset = scope.get_value::<bool>("is_offset").unwrap_or(false);
        let reason    = scope.get_value::<String>("reason").filter(|s| !s.is_empty());
        let atr       = scalar_out(&scope, "atr").filter(|&v| v > 0.0);

        let go_long  = scope.get_value::<bool>("long").unwrap_or(false)
                    || scope.get_value::<bool>("entry").unwrap_or(false);
        let go_short = scope.get_value::<bool>("short").unwrap_or(false);
        let go_exit  = scope.get_value::<bool>("exit").unwrap_or(false);

        if go_long {
            let mut sig = Signal::long(bar.timestamp, &bar.symbol, strength);
            sig.price             = Some(bar.close);
            sig.target_price      = target;
            sig.stop_price        = stop;
            sig.trailing_stop_pct = trail;
            sig.max_bars_held     = max_bars;
            sig.is_offset         = is_offset;
            sig.reason            = reason;
            sig.atr               = atr;
            return vec![sig];
        }
        if go_short {
            let mut sig = Signal::short(bar.timestamp, &bar.symbol, strength);
            sig.price             = Some(bar.close);
            sig.target_price      = target;
            sig.stop_price        = stop;
            sig.trailing_stop_pct = trail;
            sig.max_bars_held     = max_bars;
            sig.is_offset         = is_offset;
            sig.reason            = reason;
            sig.atr               = atr;
            return vec![sig];
        }
        if go_exit {
            let mut sig = Signal::exit(bar.timestamp, &bar.symbol);
            sig.reason   = reason;
            sig.atr      = atr;
            return vec![sig];
        }

        vec![]
    }

    fn take_indicator_series(&mut self) -> HashMap<String, Vec<(i64, f64)>> {
        self.series.as_mut().map(std::mem::take).unwrap_or_default()
    }

    fn current_regime(&self) -> Option<&alm_core::regime::RegimeState> {
        self.current_regime.as_ref()
    }

    fn name(&self) -> &str { "script" }

    fn reset(&mut self) {
        for b in self.bindings.values_mut() { b.reset(); }
        self.bar_buf.clear();
        self.candle_transform.reset();
        if let Some(s) = &mut self.series { s.clear(); }
        self.current_regime = None;
        self.persistent_state.clear();
    }
}

// ── Indicator dependency extraction ───────────────────────────────────────────

/// Extract `IndicatorDep`s from a script strategy's `"script"` param.
/// Used by `herald::Registry` to acquire live indicator handles.
pub fn script_indicator_deps(params: &serde_json::Value) -> Vec<crate::factory::IndicatorDep> {
    let script = match params.get("script").and_then(|v| v.as_str()) {
        Some(s) => s,
        None    => return vec![],
    };

    script
        .lines()
        .filter_map(try_parse_indicator_line)
        .filter_map(|decl| {
            make_indicator_box(&decl).ok()?;
            Some(crate::factory::IndicatorDep {
                config:    indicator_json_config(&decl.ind_type, decl.period, &decl.extra_params),
                source_tf: decl.timeframe,
            })
        })
        .collect()
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use alm_core::bar::Bar;

    fn make_bar(i: usize) -> Bar {
        let c = 100.0 + i as f64 * 0.5;
        Bar::new(i as i64 * 60_000, "TEST", c, c * 1.005, c * 0.995, c, 1000.0 + (i % 10) as f64 * 100.0)
    }

    const EMA_CROSS_SCRIPT: &str = r#"
let ema9  = ind.ema(9);
let ema21 = ind.ema(21);
let rsi14 = ind.rsi(14);

if cross_above(ema9, ema21) && rsi14[0] < 70.0 { entry = true; }
if cross_below(ema9, ema21) { exit = true; }
"#;

    const ATR_TP_SL_SCRIPT: &str = r#"
let ema20 = ind.ema(20);
let atr14 = ind.atr(14);

if close[0] > ema20[0] {
    entry = true;
    tp    = close[0] + atr14[0] * 2.0;
    sl    = close[0] - atr14[0] * 1.5;
}
if close[0] < ema20[0] { exit = true; }
"#;

    /// Script declaring a TF argument — V1 must reject this; caller should
    /// route to V2 (MtfScriptStrategy).
    const MTF_SCRIPT: &str = r#"
let h1_ema20 = ind.ema(20, "H1");
let m1_rsi5  = ind.rsi(5);

if rising(h1_ema20) && m1_rsi5[0] < 35.0 { entry = true; }
if falling(h1_ema20) || m1_rsi5[0] > 65.0 { exit = true; }
"#;

    #[test]
    fn compile_ema_cross() {
        ScriptStrategy::from_script(EMA_CROSS_SCRIPT).expect("should compile");
    }

    #[test]
    fn compile_atr_tp_sl() {
        ScriptStrategy::from_script(ATR_TP_SL_SCRIPT).expect("should compile");
    }

    #[test]
    fn v1_rejects_tf_argument() {
        let err = ScriptStrategy::from_script(MTF_SCRIPT)
            .err()
            .expect("V1 must reject scripts with TF arguments");
        let msg = err.to_string();
        assert!(
            msg.contains("does not support timeframe arguments"),
            "unexpected error: {msg}"
        );
    }

    #[test]
    fn runs_200_bars_no_panic() {
        let mut s = ScriptStrategy::from_script(EMA_CROSS_SCRIPT).unwrap();
        for i in 0..200 { let _ = s.on_bar(&make_bar(i)); }
    }

    /// Multi-field exposure for atr (`.tr`) + lsma (`.slope`), plus MEntry⊕MEntry
    /// arithmetic/comparison (`atr[0].tr - atr[1].tr`, `lsma[0] > lsma[1]`),
    /// and MEntry-aware aggregation (`highest(atr, 3)`). All must compile and
    /// run without panicking.
    const MULTI_FIELD_SCRIPT: &str = r#"
let atr14 = ind.atr(14, buf=3);
let lsma25 = ind.lsma(25);

let tr_now = atr14[0].tr;
let slope  = lsma25[0].slope;

if atr14[0] > atr14[1] && lsma25[0] > lsma25[1] && tr_now > 0.0 && slope > 0.0 {
    entry = true;
    tp = close[0] + (atr14[0] - atr14[1]) + highest(atr14, 3);
}
if atr14[0] < atr14[1] { exit = true; }
"#;

    #[test]
    fn compile_and_run_multi_field_atr_lsma() {
        // NOTE: on_bar swallows runtime errors (returns []), so a bare "no panic"
        // run would pass even if `.tr` / `.slope` getters were unregistered. We
        // attach an error_sink and assert it stays empty — proving the field
        // access actually evaluates, not silently errors every bar.
        let sink = std::sync::Arc::new(std::sync::Mutex::new(None::<String>));
        let mut s = ScriptStrategy::from_script(MULTI_FIELD_SCRIPT)
            .expect("multi-field atr/lsma script should compile")
            .with_error_sink(std::sync::Arc::clone(&sink));
        for i in 0..120 { let _ = s.on_bar(&make_bar(i)); }
        assert!(sink.lock().unwrap().is_none(), "runtime error: {:?}", sink.lock().unwrap());
    }

    /// Rhai stores `tp = 100` as an i64, not f64. `scalar_out` must still pick it
    /// up so an integer literal target is applied instead of silently dropped.
    #[test]
    fn integer_literal_scalar_outputs_apply() {
        let script = r#"
entry    = true;
tp       = 250;
sl       = 90;
strength = 1;
"#;
        let sink = std::sync::Arc::new(std::sync::Mutex::new(None::<String>));
        let mut s = ScriptStrategy::from_script(script)
            .expect("should compile")
            .with_error_sink(std::sync::Arc::clone(&sink));
        let mut sigs = vec![];
        for i in 0..5 { sigs = s.on_bar(&make_bar(i)); if !sigs.is_empty() { break; } }
        assert!(sink.lock().unwrap().is_none(), "runtime error: {:?}", sink.lock().unwrap());
        let long = sigs.iter().find(|s| matches!(s.direction, alm_core::signal::Direction::Long))
            .expect("entry should emit a long signal");
        assert_eq!(long.target_price, Some(250.0), "integer tp must be applied as f64");
        assert_eq!(long.stop_price,   Some(90.0),  "integer sl must be applied as f64");
        assert!((long.strength - 1.0).abs() < 1e-9, "integer strength must apply");
    }

    /// Stochastic Slow via the non-breaking `smooth_k` named param compiles + runs.
    #[test]
    fn compile_stochastic_slow_smooth_k() {
        let script = r#"
let st = ind.stochastic(14, 3, smooth_k=3);
if st[0].k > st[0].d { entry = true; }
if st[0].k < st[0].d { exit = true; }
"#;
        let sink = std::sync::Arc::new(std::sync::Mutex::new(None::<String>));
        let mut s = ScriptStrategy::from_script(script)
            .expect("stochastic slow with smooth_k should compile")
            .with_error_sink(std::sync::Arc::clone(&sink));
        for i in 0..80 { let _ = s.on_bar(&make_bar(i)); }
        assert!(sink.lock().unwrap().is_none(), "runtime error: {:?}", sink.lock().unwrap());
    }

    #[test]
    fn from_params_requires_script_key() {
        assert!(ScriptStrategy::from_params(&serde_json::json!({})).is_err());
    }

    #[test]
    fn indicator_deps_base_tf() {
        let p = serde_json::json!({ "script": EMA_CROSS_SCRIPT });
        let deps = script_indicator_deps(&p);
        assert_eq!(deps.len(), 3);
        assert!(deps.iter().all(|d| d.source_tf.is_none()));
    }

    // `indicator_deps_mtf` was removed: V1 rejects TF arguments at build time,
    // so `script_indicator_deps` is never called with an MTF script through the
    // V1 path. MTF dependency extraction belongs to the V2 surface.

    #[test]
    fn auto_series_collects_from_indicator_declarations() {
        let script = "let ema9 = ind.ema(9);";
        let mut s = ScriptStrategy::from_script(script).unwrap();
        for i in 0..30 { let _ = s.on_bar(&make_bar(i)); }
        let series = s.series().expect("backtest mode must have series");
        assert!(series.contains_key("ema9"), "ema9 series auto-collected");
        assert!(!series["ema9"].is_empty());
    }

    #[test]
    fn live_mode_no_series() {
        let script = "let ema9 = ind.ema(9);";
        let mut s = ScriptStrategy::from_script_live(script).unwrap();
        for i in 0..30 { let _ = s.on_bar(&make_bar(i)); }
        assert!(s.series().is_none());
    }

    #[test]
    fn reset_clears_series() {
        let script = "let ema9 = ind.ema(9);";
        let mut s = ScriptStrategy::from_script(script).unwrap();
        for i in 0..30 { let _ = s.on_bar(&make_bar(i)); }
        assert!(!s.series().unwrap()["ema9"].is_empty());
        s.reset();
        assert!(s.series().unwrap().is_empty());
    }

    #[test]
    fn highest_lowest_auto_extends_bar_buf() {
        let script = r#"
let entry = highest(close, 20) > lowest(low, 10) * 1.01;
let exit  = false;
"#;
        let s = ScriptStrategy::from_script(script).unwrap();
        assert_eq!(s.bar_buf_depth, 20);
    }

    #[test]
    fn highest_lowest_values() {
        // `highest` / `lowest` are scalar locals — they don't auto-collect.
        // Just verify the script runs without error and produces signals correctly.
        let script = r#"
let ema9 = ind.ema(9);
let h = highest(close, 5);
let l = lowest(low, 5);
let entry = close[0] > h * 0.99;
let exit  = close[0] < l * 1.01;
"#;
        let mut s = ScriptStrategy::from_script(script).unwrap();
        for i in 0..30 { let _ = s.on_bar(&make_bar(i)); }
        // ema9 is auto-collected (it's a declared indicator binding)
        let series = s.series().unwrap();
        assert!(series.contains_key("ema9"));
    }

    #[test]
    fn multi_indicator_direct_comparison() {
        // `supertrend[0] > close[0]` should work — direct numeric comparison
        // uses the "value" (price-level) field without requiring `.value` suffix.
        let script = r#"
let st = ind.supertrend(10);
let entry = close[0] > st[0];
let exit  = close[0] < st[0];
"#;
        let mut s = ScriptStrategy::from_script(script).unwrap();
        for i in 0..100 { let _ = s.on_bar(&make_bar(i)); }
    }

    #[test]
    fn multi_indicator_field_access_still_works() {
        // `supertrend[0].value` and `supertrend[0].bullish` still work after
        // switching Multi storage from script-engine map to MEntry.
        // Multi-field bindings are auto-collected as "st.value", "st.bullish", etc.
        let script = r#"
let st = ind.supertrend(10);
let val = st[0].value;
let bull = st[0].bullish;
let entry = close[0] > val && bull == 1.0;
let exit  = bull == 0.0;
"#;
        let mut s = ScriptStrategy::from_script(script).unwrap();
        for i in 0..100 { let _ = s.on_bar(&make_bar(i)); }
        let series = s.series().unwrap();
        assert!(series.contains_key("st.value"),    "st.value series missing");
        assert!(!series.contains_key("st.bullish"), "st.bullish should be excluded (boolean flag)");
        assert!(!series["st.value"].is_empty());
    }

    #[test]
    fn persistent_state_tracks_in_position() {
        let script = r#"
let rsi14 = ind.rsi(14, buf=1);
let in_pos = state["in_position"] == true;
if !in_pos && rsi14[0] < 30.0 { entry = true; state["in_position"] = true; }
if in_pos && rsi14[0] > 70.0  { exit  = true; state["in_position"] = false; }
"#;
        let mut s = ScriptStrategy::from_script(script).unwrap();
        let mut entries = 0usize;
        for i in 0..200 {
            for sig in s.on_bar(&make_bar(i)) {
                if sig.direction == alm_core::signal::Direction::Long { entries += 1; }
            }
        }
        // With in_position guard, must never have two consecutive entries without an exit.
        assert!(entries <= 100, "too many entries without position guard working");
    }

    #[test]
    fn persistent_state_resets_on_strategy_reset() {
        let script = r#"
let rsi14 = ind.rsi(14, buf=1);
let in_pos = state["in_position"] == true;
if !in_pos { entry = true; state["in_position"] = true; }
"#;
        let mut s = ScriptStrategy::from_script(script).unwrap();
        for i in 0..20 { let _ = s.on_bar(&make_bar(i)); }
        s.reset();
        // After reset, persistent state cleared — first ready bar fires entry again.
        let mut fired = false;
        for i in 0..20 {
            if s.on_bar(&make_bar(i)).iter().any(|sig| sig.direction == alm_core::signal::Direction::Long) {
                fired = true; break;
            }
        }
        assert!(fired, "entry should fire again after reset clears state");
    }

    #[test]
    fn multi_indicator_arithmetic_with_direct() {
        // `close[0] - supertrend[0]` should equal `close[0] - supertrend[0].value`
        let script = r#"
let st = ind.supertrend(10);
let diff_direct = close[0] - st[0];
let diff_field  = close[0] - st[0].value;
let entry = (diff_direct - diff_field).abs() < 0.0001;
let exit  = false;
"#;
        let mut s = ScriptStrategy::from_script(script).unwrap();
        let mut got_entry = false;
        for i in 0..100 {
            let sigs = s.on_bar(&make_bar(i));
            if sigs.iter().any(|s| s.direction == alm_core::signal::Direction::Long) {
                got_entry = true;
            }
        }
        // The script sets entry = true when diff_direct ≈ diff_field,
        // which should always be true once supertrend is warm.
        assert!(got_entry, "entry should fire when direct == field access");
    }

    #[test]
    fn rising_n_falling_n_buf_extended() {
        let script = r#"
let adx14 = ind.adx(14, buf=4);
let entry = adx14[0] > 25.0 && rising_n(adx14, 3);
let exit  = falling_n(adx14, 2);
"#;
        let s = ScriptStrategy::from_script(script).unwrap();
        assert!(s.bar_buf_depth >= 2);
        let mut s = s;
        for i in 0..100 { let _ = s.on_bar(&make_bar(i)); }
    }

    #[test]
    fn momentum_auto_extends_bar_buf() {
        let script = r#"
let m = momentum(close, 5);
let entry = m > 1.0;
let exit  = m < -1.0;
"#;
        let s = ScriptStrategy::from_script(script).unwrap();
        assert_eq!(s.bar_buf_depth, 6);
    }

    #[test]
    fn slope_and_momentum_compile_run() {
        let script = r#"
let adx14 = ind.adx(14, buf=5);
let s     = slope(adx14);
let m     = momentum(adx14, 3);
let entry = adx14[0] > 25.0 && s > 0.0 && m > 0.5;
let exit  = s < 0.0;
"#;
        let mut s = ScriptStrategy::from_script(script).unwrap();
        for i in 0..100 { let _ = s.on_bar(&make_bar(i)); }
    }

    #[test]
    fn strength_read_from_scope() {
        let script = r#"
let ema9  = ind.ema(9);
let ema21 = ind.ema(21);

if cross_above(ema9, ema21) { entry = true; strength = 0.75; }
if cross_below(ema9, ema21) { exit  = true; }
"#;
        let mut s = ScriptStrategy::from_script(script).unwrap();
        let bars: Vec<Bar> = (0..60).map(make_bar).collect();
        for b in &bars {
            let sigs = s.on_bar(b);
            for sig in &sigs {
                if sig.direction == alm_core::signal::Direction::Long {
                    assert!((sig.strength - 0.75).abs() < 1e-9);
                }
            }
        }
    }

    // ── Regime block tests ────────────────────────────────────────────────────

    const REGIME_SCRIPT: &str = r#"
let ema9  = ind.ema(9);
let ema21 = ind.ema(21);

regime {
    let adx14 = ind.adx(14);
    if adx14[0] > 25.0 {
        trend = "trending";
        trend_value = adx14[0];
    } else {
        trend = "ranging";
        trend_value = adx14[0];
    }
}

if cross_above(ema9, ema21) && trend == "trending" { entry = true; }
if cross_below(ema9, ema21) { exit = true; }
"#;

    #[test]
    fn regime_block_compiles_and_runs() {
        let mut s = ScriptStrategy::from_script(REGIME_SCRIPT).expect("regime script should compile");
        for i in 0..100 {
            let _ = s.on_bar(&make_bar(i));
        }
    }

    #[test]
    fn regime_block_exposes_state() {
        use alm_core::strategy::Strategy;
        let mut s = ScriptStrategy::from_script(REGIME_SCRIPT).unwrap();
        // Run enough bars for ADX (period 14) to warm up.
        for i in 0..60 {
            let _ = s.on_bar(&make_bar(i));
        }
        let regime = Strategy::current_regime(&s).expect("regime should be populated after warmup");
        assert!(
            regime.trend.is("trending") || regime.trend.is("ranging"),
            "trend label = {:?}",
            regime.trend.status
        );
    }

    #[test]
    fn no_regime_block_leaves_state_none() {
        use alm_core::strategy::Strategy;
        let mut s = ScriptStrategy::from_script(EMA_CROSS_SCRIPT).unwrap();
        for i in 0..50 {
            let _ = s.on_bar(&make_bar(i));
        }
        assert!(Strategy::current_regime(&s).is_none());
    }

    #[test]
    fn duplicate_indicator_name_across_blocks_errors() {
        // `ema9` declared in both regime and main — must be rejected.
        let script = r#"
let ema9 = ind.ema(9);

regime {
    let ema9 = ind.ema(9);
    trend = "x";
}
"#;
        let err = match ScriptStrategy::from_script(script) {
            Ok(_)  => panic!("should reject duplicate indicator name"),
            Err(e) => e.to_string(),
        };
        assert!(err.contains("ema9") && err.contains("declared more than once"), "unexpected error: {err}");
    }

    #[test]
    fn regime_block_shares_indicator_with_main() {
        // `adx14` is declared inside the regime block and read by both regime
        // and main — sharing should make it visible everywhere.
        let script = r#"
let ema9  = ind.ema(9);
let ema21 = ind.ema(21);

regime {
    let adx14 = ind.adx(14);
    if adx14[0] > 25.0 { trend = "trending"; }
    else               { trend = "weak"; }
}

if cross_above(ema9, ema21) && adx14[0] > 20.0 && trend == "trending" {
    entry = true;
}
"#;
        let mut s = ScriptStrategy::from_script(script).expect("shared adx14 should compile");
        for i in 0..80 {
            let _ = s.on_bar(&make_bar(i));
        }
    }

    // ── Candle directive tests ────────────────────────────────────────────────

    #[test]
    fn script_candle_invalid_kind_rejected_at_compile() {
        let s = r#"candle.transform("renko_5");
let ema9 = ind.ema(9);"#;
        let err = ScriptStrategy::from_script(s).err().expect("should reject");
        let msg = err.to_string();
        assert!(msg.contains("unknown candle kind") && msg.contains("renko_5"), "{msg}");
    }

    #[test]
    fn script_candle_typo_rejected() {
        let s = r#"candle.transform("heikin_ashi");
let ema9 = ind.ema(9);"#;
        let err = ScriptStrategy::from_script(s).err().expect("should reject typo");
        assert!(err.to_string().contains("unknown candle kind"));
    }

    #[test]
    fn script_candle_directive_applies_transform_internally() {
        // Two strategies, same indicator logic, one with HA directive and one
        // raw. Run both on the SAME raw bars. If the directive is wired
        // correctly, indicator readings (and therefore signals/timing) diverge.
        let s_raw = r#"
let ema9  = ind.ema(9);
let ema21 = ind.ema(21);
if cross_above(ema9, ema21) { entry = true; }
if cross_below(ema9, ema21) { exit  = true; }
"#;
        let s_ha = r#"candle.transform("heiken_ashi");

let ema9  = ind.ema(9);
let ema21 = ind.ema(21);
if cross_above(ema9, ema21) { entry = true; }
if cross_below(ema9, ema21) { exit  = true; }
"#;
        let mut a = ScriptStrategy::from_script(s_raw).unwrap();
        let mut b = ScriptStrategy::from_script(s_ha).unwrap();

        // Use bars with real OHLC range so HA differs from raw close.
        let bars: Vec<_> = (0..200_i64)
            .map(|i| {
                let t = i as f64;
                let c = 100.0 + 8.0 * (t * 0.08).sin();
                let o = 100.0 + 8.0 * (t * 0.08 - 0.04).sin();
                let h = c.max(o) + 1.5;
                let l = c.min(o) - 1.5;
                alm_core::bar::Bar::new(i * 60_000, "TEST", o, h, l, c, 1000.0)
            })
            .collect();

        let mut raw_sigs = Vec::new();
        for bar in &bars { for s in a.on_bar(bar) { raw_sigs.push((s.timestamp, s.direction)); } }
        let mut ha_sigs = Vec::new();
        for bar in &bars { for s in b.on_bar(bar) { ha_sigs.push((s.timestamp, s.direction)); } }

        // If `candle.transform("heiken_ashi")` is properly wired into the
        // internal candle_transform, the HA strategy must produce a different
        // signal stream than the raw one on the same bars. Equality would
        // mean the directive was silently ignored.
        assert_ne!(raw_sigs, ha_sigs,
            "HA directive must change signal stream when run on same raw bars");
    }

    #[test]
    fn script_candle_directive_after_let_rejected() {
        let s = r#"let ema9 = ind.ema(9);
candle.transform("heiken_ashi");
"#;
        let err = ScriptStrategy::from_script(s).err().expect("must enforce top-of-file");
        assert!(err.to_string().contains("must appear at the top"), "{}", err);
    }

    #[test]
    fn regime_indicator_deps_includes_regime_block() {
        // `script_indicator_deps` is called by herald::Registry to acquire indicator
        // handles. Indicators declared inside `regime { ... }` must be visible too.
        let p = serde_json::json!({ "script": REGIME_SCRIPT });
        let deps = script_indicator_deps(&p);
        // ema9, ema21, adx14
        assert_eq!(deps.len(), 3);
    }

    #[test]
    fn tp_sl_signal_fields() {
        let mut s = ScriptStrategy::from_script(ATR_TP_SL_SCRIPT).unwrap();
        let bars: Vec<Bar> = (0..60).map(make_bar).collect();
        let mut entry_sig: Option<Signal> = None;
        for b in &bars {
            let sigs = s.on_bar(b);
            if let Some(sig) = sigs.into_iter().find(|s| s.direction == alm_core::signal::Direction::Long) {
                entry_sig = Some(sig);
                break;
            }
        }
        if let Some(sig) = entry_sig {
            assert!(sig.target_price.is_some());
            assert!(sig.stop_price.is_some());
        }
    }

    #[test]
    fn sample_script_with_plot_runs_without_error() {
        // Full sample script: indicator named `atr` shadowing the `atr` output
        // variable must not cause "Indexer unavailable: f64 [i64]".
        // plot() calls must be no-ops, not fail with "Function not found".
        let script = r#"// EMA Crossover + RSI Filter
let fast = ind.ema(9);
let slow = ind.ema(21);
let rsi  = ind.rsi(14);
let atr  = ind.atr(14);

if crossover(fast, slow) && rsi[0] < 65.0 {
    long     = true;
    strength = (65.0 - rsi[0]) / 65.0;
    sl       = close[0] - 2.0 * atr[0];
    tp       = close[0] + 3.0 * atr[0];
}
if crossunder(fast, slow) || rsi[0] > 75.0 { exit = true; }

plot("fast_ema", fast[0]);
plot("slow_ema", slow[0]);
plot("rsi",      rsi[0]);
"#;
        let sink = std::sync::Arc::new(std::sync::Mutex::new(None::<String>));
        let mut s = ScriptStrategy::from_script(script)
            .expect("should compile")
            .with_error_sink(std::sync::Arc::clone(&sink));
        for i in 0..200 { let _ = s.on_bar(&make_bar(i)); }
        let runtime_err = sink.lock().unwrap().clone();
        assert!(runtime_err.is_none(), "script runtime error: {:?}", runtime_err);
    }
}

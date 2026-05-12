//! Rhai scripting strategy — define entry/exit logic as a Rhai script.
//!
//! # Script format
//!
//! ```text
//! let ema9   = ind.ema(9);
//! let h1_ema = ind.ema(20, "H1");     // H1 timeframe
//! let rsi14  = ind.rsi(14);
//! let atr14  = ind.atr(14, "H1", 3); // H1, keep 3 bars
//!
//! if cross_above(ema9, h1_ema) && rsi14[0] < 60.0 {
//!     entry = true;
//!     tp    = close[0] + atr14[0] * 2.0;
//!     sl    = close[0] - atr14[0] * 1.5;
//! }
//! if cross_below(ema9, h1_ema) { exit = true; }
//! ```
//!
//! # Indicator declaration syntax
//!
//! `ind.TYPE(period [, tf_or_buf [, buf]])`
//!
//! | Form | Meaning |
//! |---|---|
//! | `ind.ema(9)` | Base-TF, default buf=2 |
//! | `ind.ema(9, 5)` | Base-TF, buf=5 |
//! | `ind.ema(20, "H1")` | H1 confirmed (fires only at H1 close), buf=2 |
//! | `ind.ema(20, "H1", 3)` | H1 confirmed, buf=3 |
//! | `ind.rsi(5, "live_H1")` | H1 **live** — `[0]`=confirmed, `_live`=forming scalar |
//! | `ind.rsi(5, "live_H1", 3)` | H1 live, confirmed buf=3 |
//!
//! **Live semantics**: `{name}_live` = current forming-bar value, `{name}_fill` = fill ratio 0..=1.
//!
//! # Output variables
//!
//! `long`, `short`, `exit`, `tp`, `sl`, `strength`, `is_offset`, `reason`.
//! `entry` is a legacy alias for `long`.
//! When `is_offset = true`, `tp`/`sl` are deltas from fill price (helm semantics).

use std::collections::{HashMap, VecDeque};
use std::sync::{Arc, Mutex};

use anyhow::Result;
use rhai::{Array, Dynamic, Engine, Scope, AST};
use alm_core::{bar::Bar, signal::Signal, strategy::Strategy};

use crate::candle_type::{CandleType, CandleTransform};

use super::binding::VarBinding;
use super::parse::{make_indicator_box, IndicatorDecl, try_parse_indicator_line, indicator_json_config};
use super::engine::{build_engine, extract_max_lookback, PlotBuf, BAR_FIELDS, DEFAULT_BUF_DEPTH};

// ── Public re-exports ─────────────────────────────────────────────────────────

pub use super::lint::{
    rhai_lint, DeclaredIndicator, LintDiagnostic, RhaiLintScope, KNOWN_INDICATOR_TYPES,
};

// ── RhaiStrategy ─────────────────────────────────────────────────────────────

pub struct RhaiStrategy {
    engine:           Engine,
    ast:              AST,
    bindings:         HashMap<String, VarBinding>,
    binding_order:    Vec<String>,
    bar_buf:          VecDeque<Bar>,
    bar_buf_depth:    usize,
    plot_buf:         PlotBuf,
    /// `None` in live mode — plot() calls are silently flushed and discarded.
    series:           Option<HashMap<String, Vec<(i64, f64)>>>,
    candle_transform: CandleTransform,
}

impl RhaiStrategy {
    /// Backtest mode: `plot()` calls accumulate in `series()` / `take_indicator_series()`.
    pub fn from_script(script: &str) -> Result<Self> {
        Self::build(script, true, CandleType::Raw)
    }

    /// Live (herald) mode: `plot()` calls are registered but immediately discarded.
    pub fn from_script_live(script: &str) -> Result<Self> {
        Self::build(script, false, CandleType::Raw)
    }

    fn build(script: &str, collect_series: bool, candle_type: CandleType) -> Result<Self> {
        let mut decls: Vec<IndicatorDecl> = Vec::new();
        let mut cleaned_lines: Vec<&str>  = Vec::new();

        for line in script.lines() {
            match try_parse_indicator_line(line) {
                Some(decl) => decls.push(decl),
                None       => cleaned_lines.push(line),
            }
        }

        let cleaned_script = cleaned_lines.join("\n");
        let plot_buf: PlotBuf = Arc::new(Mutex::new(Vec::new()));
        let engine = build_engine(Arc::clone(&plot_buf));
        let ast = engine
            .compile(&cleaned_script)
            .map_err(|e| anyhow::anyhow!("Rhai compile error: {e}"))?;

        let mut bindings:      HashMap<String, VarBinding> = HashMap::new();
        let mut binding_order: Vec<String>                  = Vec::new();
        let mut max_buf = DEFAULT_BUF_DEPTH;

        for decl in &decls {
            if decl.buf_depth > max_buf { max_buf = decl.buf_depth; }
            let ind      = make_indicator_box(decl)?;
            let live_ind = if decl.live { Some(make_indicator_box(decl)?) } else { None };
            let binding  = VarBinding::new(ind, live_ind, decl.kind.clone(), decl.buf_depth, decl.timeframe, decl.live);
            binding_order.push(decl.var_name.clone());
            bindings.insert(decl.var_name.clone(), binding);
        }

        let lookback = extract_max_lookback(&cleaned_script);
        if lookback > max_buf { max_buf = lookback; }

        Ok(Self {
            engine,
            ast,
            bindings,
            binding_order,
            bar_buf: VecDeque::with_capacity(max_buf),
            bar_buf_depth: max_buf,
            plot_buf,
            series: if collect_series { Some(HashMap::new()) } else { None },
            candle_transform: CandleTransform::new(candle_type),
        })
    }

    /// Backtest: `from_params` defaults to collect mode.
    /// Live: caller must pass `"_live": true` in params (injected by herald registry).
    pub fn from_params(p: &serde_json::Value) -> Result<Self> {
        let script = p
            .get("script")
            .and_then(|v| v.as_str())
            .ok_or_else(|| anyhow::anyhow!("RhaiStrategy requires a 'script' param"))?;
        let live = p.get("_live").and_then(|v| v.as_bool()).unwrap_or(false);
        let candle_type = {
            let ct = p.get("candle_type").and_then(|v| v.as_str()).unwrap_or("raw");
            let sp = p.get("smooth_period").and_then(|v| v.as_u64()).unwrap_or(3) as usize;
            CandleType::from_str(ct, sp)
        };
        if live { Self::build(script, false, candle_type) } else { Self::build(script, true, candle_type) }
    }

    /// Snapshot of collected plot series (backtest mode only).
    pub fn series(&self) -> Option<&HashMap<String, Vec<(i64, f64)>>> {
        self.series.as_ref()
    }
}

// ── Strategy impl ─────────────────────────────────────────────────────────────

impl Strategy for RhaiStrategy {
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
        if !all_ready { return vec![]; }

        let mut scope = Scope::new();

        for name in &self.binding_order {
            if let Some(b) = self.bindings.get(name) {
                scope.push_dynamic(name.as_str(), Dynamic::from_array(b.to_rhai_array()));
                if b.live {
                    if b.is_multi() {
                        scope.push_dynamic(
                            format!("{name}_live").as_str(),
                            Dynamic::from_map(b.live_map_as_rhai()),
                        );
                    } else {
                        scope.push(format!("{name}_live").as_str(), b.live_val);
                    }
                    scope.push(format!("{name}_fill").as_str(), b.fill_ratio);
                }
            }
        }

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

        scope.push("entry",     false);
        scope.push("exit",      false);
        scope.push("long",      false);
        scope.push("short",     false);
        scope.push("tp",        0.0_f64);
        scope.push("sl",        0.0_f64);
        scope.push("strength",  1.0_f64);
        scope.push("is_offset", false);
        scope.push("reason",    String::new());

        if self.engine.run_ast_with_scope(&mut scope, &self.ast).is_err() {
            return vec![];
        }

        if let Ok(mut buf) = self.plot_buf.lock() {
            if let Some(series) = &mut self.series {
                for (name, value) in buf.drain(..) {
                    series.entry(name).or_default().push((bar.timestamp, value));
                }
            } else {
                buf.clear();
            }
        }

        let strength  = scope.get_value::<f64>("strength").unwrap_or(1.0).clamp(0.0, 1.0);
        let target    = scope.get_value::<f64>("tp").filter(|&v| v != 0.0);
        let stop      = scope.get_value::<f64>("sl").filter(|&v| v != 0.0);
        let is_offset = scope.get_value::<bool>("is_offset").unwrap_or(false);
        let reason    = scope.get_value::<String>("reason").filter(|s| !s.is_empty());

        let go_long  = scope.get_value::<bool>("long").unwrap_or(false)
                    || scope.get_value::<bool>("entry").unwrap_or(false);
        let go_short = scope.get_value::<bool>("short").unwrap_or(false);
        let go_exit  = scope.get_value::<bool>("exit").unwrap_or(false);

        if go_long {
            let mut sig = Signal::long(bar.timestamp, &bar.symbol, strength);
            sig.price        = Some(bar.close);
            sig.target_price = target;
            sig.stop_price   = stop;
            sig.is_offset    = is_offset;
            sig.reason       = reason;
            return vec![sig];
        }
        if go_short {
            let mut sig = Signal::short(bar.timestamp, &bar.symbol, strength);
            sig.price        = Some(bar.close);
            sig.target_price = target;
            sig.stop_price   = stop;
            sig.is_offset    = is_offset;
            sig.reason       = reason;
            return vec![sig];
        }
        if go_exit {
            return vec![Signal::close(bar.timestamp, &bar.symbol)];
        }

        vec![]
    }

    fn take_indicator_series(&mut self) -> HashMap<String, Vec<(i64, f64)>> {
        self.series.as_mut().map(std::mem::take).unwrap_or_default()
    }

    fn name(&self) -> &str { "rhai" }

    fn reset(&mut self) {
        for b in self.bindings.values_mut() { b.reset(); }
        self.bar_buf.clear();
        self.candle_transform.reset();
        if let Some(s) = &mut self.series { s.clear(); }
        if let Ok(mut buf) = self.plot_buf.lock() { buf.clear(); }
    }
}

// ── Indicator dependency extraction ───────────────────────────────────────────

/// Extract `IndicatorDep`s from a rhai strategy's `"script"` param.
/// Used by `herald::Registry` to acquire live indicator handles.
pub fn rhai_indicator_deps(params: &serde_json::Value) -> Vec<crate::factory::IndicatorDep> {
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
                config:    indicator_json_config(&decl.ind_type, decl.period),
                source_tf: decl.timeframe,
            })
        })
        .collect()
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use alm_core::{bar::Bar, Timeframe};

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

    const MTF_SCRIPT: &str = r#"
let h1_ema20 = ind.ema(20, "H1");
let m1_rsi5  = ind.rsi(5);

if rising(h1_ema20) && m1_rsi5[0] < 35.0 { entry = true; }
if falling(h1_ema20) || m1_rsi5[0] > 65.0 { exit = true; }
"#;

    #[test]
    fn compile_ema_cross() {
        RhaiStrategy::from_script(EMA_CROSS_SCRIPT).expect("should compile");
    }

    #[test]
    fn compile_atr_tp_sl() {
        RhaiStrategy::from_script(ATR_TP_SL_SCRIPT).expect("should compile");
    }

    #[test]
    fn compile_mtf_script() {
        RhaiStrategy::from_script(MTF_SCRIPT).expect("MTF script should compile");
    }

    #[test]
    fn runs_200_bars_no_panic() {
        let mut s = RhaiStrategy::from_script(EMA_CROSS_SCRIPT).unwrap();
        for i in 0..200 { let _ = s.on_bar(&make_bar(i)); }
    }

    #[test]
    fn mtf_runs_200_m1_bars_no_panic() {
        let mut s = RhaiStrategy::from_script(MTF_SCRIPT).unwrap();
        for i in 0..200 { let _ = s.on_bar(&make_bar(i)); }
    }

    #[test]
    fn from_params_requires_script_key() {
        assert!(RhaiStrategy::from_params(&serde_json::json!({})).is_err());
    }

    #[test]
    fn indicator_deps_base_tf() {
        let p = serde_json::json!({ "script": EMA_CROSS_SCRIPT });
        let deps = rhai_indicator_deps(&p);
        assert_eq!(deps.len(), 3);
        assert!(deps.iter().all(|d| d.source_tf.is_none()));
    }

    #[test]
    fn indicator_deps_mtf() {
        let p = serde_json::json!({ "script": MTF_SCRIPT });
        let deps = rhai_indicator_deps(&p);
        assert_eq!(deps.len(), 2);
        assert!(deps.iter().any(|d| d.source_tf == Some(Timeframe::H1)));
        assert!(deps.iter().any(|d| d.source_tf.is_none()));
    }

    #[test]
    fn plot_collects_series() {
        let script = r#"
let ema9 = ind.ema(9);
plot("ema9", ema9[0]);
"#;
        let mut s = RhaiStrategy::from_script(script).unwrap();
        for i in 0..30 { let _ = s.on_bar(&make_bar(i)); }
        let series = s.series().expect("backtest mode must have series");
        assert!(series.contains_key("ema9"));
        assert!(!series["ema9"].is_empty());
    }

    #[test]
    fn plot_live_mode_no_series() {
        let script = r#"
let ema9 = ind.ema(9);
plot("ema9", ema9[0]);
"#;
        let mut s = RhaiStrategy::from_script_live(script).unwrap();
        for i in 0..30 { let _ = s.on_bar(&make_bar(i)); }
        assert!(s.series().is_none());
    }

    #[test]
    fn plot_reset_clears_series() {
        let script = r#"
let ema9 = ind.ema(9);
plot("ema9", ema9[0]);
"#;
        let mut s = RhaiStrategy::from_script(script).unwrap();
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
        let s = RhaiStrategy::from_script(script).unwrap();
        assert_eq!(s.bar_buf_depth, 20);
    }

    #[test]
    fn highest_lowest_values() {
        let script = r#"
let h = highest(close, 5);
let l = lowest(low, 5);
let entry = close[0] > h * 0.99;
let exit  = close[0] < l * 1.01;
plot("h", h);
plot("l", l);
"#;
        let mut s = RhaiStrategy::from_script(script).unwrap();
        for i in 0..30 { let _ = s.on_bar(&make_bar(i)); }
        let series = s.series().unwrap();
        assert!(series.contains_key("h") && series.contains_key("l"));
    }

    #[test]
    fn in_position_exposed_in_scope() {
        let script = r#"
let ema9 = ind.ema(9);
if !in_position && ema9[0] > 0.0 { entry = true; }
if  in_position && ema9[0] < 0.0 { exit  = true; }
"#;
        let mut s = RhaiStrategy::from_script(script).unwrap();
        for i in 0..30 { let _ = s.on_bar(&make_bar(i)); }
    }

    #[test]
    fn rising_n_falling_n_buf_extended() {
        let script = r#"
let adx14 = ind.adx(14, 4);
let entry = adx14[0] > 25.0 && rising_n(adx14, 3);
let exit  = falling_n(adx14, 2);
"#;
        let s = RhaiStrategy::from_script(script).unwrap();
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
        let s = RhaiStrategy::from_script(script).unwrap();
        assert_eq!(s.bar_buf_depth, 6);
    }

    #[test]
    fn slope_and_momentum_compile_run() {
        let script = r#"
let adx14 = ind.adx(14, 5);
let s     = slope(adx14);
let m     = momentum(adx14, 3);
let entry = adx14[0] > 25.0 && s > 0.0 && m > 0.5;
let exit  = s < 0.0;
"#;
        let mut s = RhaiStrategy::from_script(script).unwrap();
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
        let mut s = RhaiStrategy::from_script(script).unwrap();
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

    #[test]
    fn tp_sl_signal_fields() {
        let mut s = RhaiStrategy::from_script(ATR_TP_SL_SCRIPT).unwrap();
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
}

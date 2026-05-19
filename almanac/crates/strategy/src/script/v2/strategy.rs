//! Multi-bus Rhai script strategy — implements [`MtfStrategy`] for use with
//! [`alm_engine::MtfEngine`].
//!
//! # Key differences from v1 [`ScriptStrategy`]
//!
//! | Concern | v1 | **v2** |
//! |---|---|---|
//! | HTF source | Internal M1→HTF resampler | Real HTF feed via MtfEngine |
//! | Live forming | Separate `live_ind` copy | Clone-and-project |
//! | Drift | Possible (two indicator states) | None |
//! | Engine trait | `Strategy::on_bar` | `MtfStrategy::on_bars` |
//!
//! # Script syntax
//!
//! Identical to v1 — same `ind.TYPE(period [, tf_or_buf [, buf]])` declarations,
//! same `entry/exit/long/short/tp/sl/strength` output variables, same
//! `regime { }` block, same `candle.transform()` directive.
//!
//! In v2, when a script declares `ind.ema(20, "H1")`, the caller **must** register
//! an H1 feed with `MtfEngine::add_feed(Timeframe::H1, h1_feed)`. Use
//! [`MtfScriptStrategy::declared_htfs`] to discover which TFs the script needs.

use std::collections::{HashMap, HashSet, VecDeque};
use std::sync::Arc;

use anyhow::Result;
use rhai::{Array, Dynamic, Engine, Scope, AST};
use alm_core::{bar::Bar, regime::{RegimeDimension, RegimeState}, signal::Signal, Timeframe};
use alm_core::{MtfSnapshot, MtfStrategy};

use crate::candle_type::{CandleType, CandleTransform};

use super::feed_binding::FeedVarBinding;
use super::parse::{
    extract_candle_directives, extract_max_lookback, extract_regime_block,
    make_indicator_box, parse_timeframe_str,
    try_parse_indicator_line, CandleDirective, DEFAULT_BUF_DEPTH,
};
use super::engine::{build_engine, BAR_FIELDS, PlotBuf};
use crate::script::v1::MEntry;

// ── MtfScriptStrategy ─────────────────────────────────────────────────────────

pub struct MtfScriptStrategy {
    engine:           Engine,
    ast:              AST,
    regime_ast:       Option<AST>,
    bindings:         HashMap<String, FeedVarBinding>,
    binding_order:    Vec<String>,
    bar_buf:          VecDeque<Bar>,
    bar_buf_depth:    usize,
    plot_buf:         PlotBuf,
    series:           Option<HashMap<String, Vec<(i64, f64)>>>,
    candle_transform: CandleTransform,
    current_regime:   Option<RegimeState>,
    script_candle:    Option<(String, Option<usize>)>,
}

fn validate_candle_kind(kind: &str) -> Result<()> {
    match kind {
        "raw" | "heiken_ashi" | "ha" | "smooth_ha" | "smooth_heiken_ashi" => Ok(()),
        other => Err(anyhow::anyhow!(
            "unknown candle kind `{other}`; supported: \
             \"raw\", \"heiken_ashi\" (alias \"ha\"), \"smooth_ha\""
        )),
    }
}

impl MtfScriptStrategy {
    /// Backtest mode — collects plot series.
    pub fn from_script(script: &str, base_tf: Timeframe) -> Result<Self> {
        Self::build(script, true, CandleType::Raw, base_tf)
    }

    /// Live (herald) mode — plot() calls are discarded.
    pub fn from_script_live(script: &str, base_tf: Timeframe) -> Result<Self> {
        Self::build(script, false, CandleType::Raw, base_tf)
    }

    /// Build from JSON params (`"script"`, `"base_tf"`, `"_live"`, etc.).
    pub fn from_params(p: &serde_json::Value) -> Result<Self> {
        let script = p.get("script").and_then(|v| v.as_str())
            .ok_or_else(|| anyhow::anyhow!("MtfScriptStrategy requires a 'script' param"))?;
        let live = p.get("_live").and_then(|v| v.as_bool()).unwrap_or(false);
        let candle_type = {
            let ct = p.get("candle_type").and_then(|v| v.as_str()).unwrap_or("raw");
            let sp = p.get("smooth_period").and_then(|v| v.as_u64()).unwrap_or(3) as usize;
            CandleType::from_str(ct, sp)
        };
        let base_tf = p.get("base_tf")
            .and_then(|v| v.as_str())
            .and_then(parse_timeframe_str)
            .unwrap_or(Timeframe::M1);
        if live {
            Self::build(script, false, candle_type, base_tf)
        } else {
            Self::build(script, true, candle_type, base_tf)
        }
    }

    fn build(
        script: &str,
        collect_series: bool,
        candle_type: CandleType,
        base_tf: Timeframe,
    ) -> Result<Self> {
        let base_tf_ms = base_tf.duration_ms();

        // ── Step 0: candle directives ─────────────────────────────────────────
        let (candle_dirs, after_candle) = extract_candle_directives(script)?;
        let script_candle = if let Some(d) = candle_dirs.last() {
            let (kind, smooth) = match d {
                CandleDirective::Transform { kind, smooth } => (kind.as_str(), *smooth),
            };
            validate_candle_kind(kind)?;
            Some((kind.to_string(), smooth))
        } else {
            None
        };

        // ── Step 1: regime block ──────────────────────────────────────────────
        let (regime_body, main_source) = extract_regime_block(&after_candle)?;

        // ── Step 2: collect indicator declarations from both blocks ───────────
        let mut decls: Vec<_> = Vec::new();
        let mut decl_names = HashSet::new();

        let regime_body_owned = regime_body.unwrap_or_default();
        let mut regime_cleaned_lines: Vec<&str> = Vec::new();
        for line in regime_body_owned.lines() {
            match try_parse_indicator_line(line) {
                Some(decl) => {
                    if !decl_names.insert(decl.var_name.clone()) {
                        anyhow::bail!(
                            "indicator `{}` is declared more than once \
                             (regime + main share names)",
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
                            "indicator `{}` is declared more than once \
                             (regime + main share names)",
                            decl.var_name
                        );
                    }
                    decls.push(decl);
                }
                None => main_cleaned_lines.push(line),
            }
        }
        let main_cleaned_script = main_cleaned_lines.join("\n");

        // ── Step 3: compile ASTs ──────────────────────────────────────────────
        let plot_buf: PlotBuf = Arc::new(std::sync::Mutex::new(Vec::new()));
        let engine = build_engine(Arc::clone(&plot_buf));

        let regime_ast = if regime_cleaned_script.trim().is_empty() {
            None
        } else {
            Some(
                engine
                    .compile(&regime_cleaned_script)
                    .map_err(|e| anyhow::anyhow!("script compile error (regime): {e}"))?,
            )
        };
        let ast = engine
            .compile(&main_cleaned_script)
            .map_err(|e| anyhow::anyhow!("script compile error: {e}"))?;

        // ── Step 4: instantiate bindings ──────────────────────────────────────
        let mut bindings: HashMap<String, FeedVarBinding> = HashMap::new();
        let mut binding_order: Vec<String> = Vec::new();
        let mut max_buf = DEFAULT_BUF_DEPTH;

        for decl in &decls {
            if decl.buf_depth > max_buf { max_buf = decl.buf_depth; }
            let ind = make_indicator_box(decl)?;
            // In v2, all HTF bindings automatically get live projection;
            // base-TF bindings get live only when `live_` prefix was used.
            let enable_live = decl.timeframe.is_some() || decl.live;
            let binding = FeedVarBinding::new(
                ind,
                decl.timeframe,
                decl.extract.clone(),
                decl.buf_depth,
                enable_live,
                base_tf_ms,
            ).map_err(|e| anyhow::anyhow!(
                "indicator '{}': {e}", decl.var_name
            ))?;
            binding_order.push(decl.var_name.clone());
            bindings.insert(decl.var_name.clone(), binding);
        }

        // Size bar_buf to cover `highest()`/`momentum()` lookbacks as well.
        let lookback = extract_max_lookback(&main_cleaned_script)
            .max(extract_max_lookback(&regime_cleaned_script));
        if lookback > max_buf { max_buf = lookback; }

        Ok(Self {
            engine,
            ast,
            regime_ast,
            bindings,
            binding_order,
            bar_buf: VecDeque::with_capacity(max_buf),
            bar_buf_depth: max_buf,
            plot_buf,
            series: if collect_series { Some(HashMap::new()) } else { None },
            candle_transform: {
                let effective = match &script_candle {
                    Some((kind, smooth)) => CandleType::from_str(kind, smooth.unwrap_or(3)),
                    None => candle_type,
                };
                CandleTransform::new(effective)
            },
            current_regime: None,
            script_candle,
        })
    }

    /// Drain collected plot series (backtest mode only). Returns empty map in live mode.
    pub fn take_series(&mut self) -> HashMap<String, Vec<(i64, f64)>> {
        self.series.take().unwrap_or_default()
    }

    /// All HTFs declared by `ind.*(period, "TF")` lines in the script.
    /// Pass these to `MtfEngine::add_feed` when setting up the backtest.
    pub fn declared_htfs(&self) -> Vec<Timeframe> {
        let mut seen = HashSet::new();
        self.bindings
            .values()
            .filter_map(|b| b.target_tf())
            .filter(|tf| seen.insert(*tf))
            .collect()
    }

    pub fn script_candle_spec(&self) -> Option<(String, Option<usize>)> {
        self.script_candle.clone()
    }
}

// ── MtfStrategy impl ──────────────────────────────────────────────────────────

impl MtfStrategy for MtfScriptStrategy {
    fn name(&self) -> &str { "mtf_script" }

    fn reset(&mut self) {
        for b in self.bindings.values_mut() { b.reset(); }
        self.bar_buf.clear();
        self.candle_transform.reset();
        if let Some(s) = &mut self.series { s.clear(); }
        if let Ok(mut buf) = self.plot_buf.lock() { buf.clear(); }
        self.current_regime = None;
    }

    fn on_bars(&mut self, snap: MtfSnapshot<'_>) -> Vec<Signal> {
        // ── 1. Advance all HTF bindings that closed at this tick ──────────────
        // (heap already sorted largest-TF-first, so this order is deterministic)
        for ev in snap.events {
            if ev.tf == snap.base_tf { continue; }
            for name in &self.binding_order {
                if let Some(b) = self.bindings.get_mut(name) {
                    if b.target_tf() == Some(ev.tf) {
                        b.feed_confirmed(ev.bar);
                    }
                }
            }
        }

        // ── 2. Require a base bar to continue ─────────────────────────────────
        // HTF-only ticks don't drive the script; we just updated confirmed state.
        let Some(raw_base_bar) = snap.base_bar() else {
            return vec![];
        };

        // Apply candle transform (heiken-ashi, etc.) before feeding anything.
        let Some(base_bar_owned) = self.candle_transform.apply(raw_base_bar) else {
            return vec![];
        };
        let base_bar = &base_bar_owned;

        // ── 3. Advance base-TF bindings ────────────────────────────────────────
        for name in &self.binding_order {
            if let Some(b) = self.bindings.get_mut(name) {
                if b.target_tf().is_none() {
                    b.feed_confirmed(base_bar);
                }
            }
        }

        // ── 4. Update live projections for all HTF bindings ───────────────────
        for name in &self.binding_order {
            if let Some(b) = self.bindings.get_mut(name) {
                if b.target_tf().is_some() {
                    b.tick_live(base_bar);
                }
            }
        }

        // ── 5. Update bar buffer ───────────────────────────────────────────────
        self.bar_buf.push_back(base_bar.clone());
        if self.bar_buf.len() > self.bar_buf_depth {
            self.bar_buf.pop_front();
        }

        // ── 6. Warmth check ────────────────────────────────────────────────────
        if self.bar_buf.len() < self.bar_buf_depth {
            return vec![];
        }
        if !self.binding_order.iter()
            .all(|n| self.bindings.get(n).map_or(true, |b| b.is_warm()))
        {
            return vec![];
        }

        // ── 7. Build Rhai scope ────────────────────────────────────────────────
        let mut scope = Scope::new();

        for name in &self.binding_order {
            if let Some(b) = self.bindings.get(name) {
                scope.push_dynamic(name.as_str(), Dynamic::from_array(b.to_script_array()));
                // Expose {name}_live and {name}_fill for HTF bindings with live.
                if let Some(ratio) = b.live_fill_ratio() {
                    if b.is_multi() {
                        let primary = b.live_primary().unwrap_or("value").to_string();
                        let map = b.live_map().cloned().unwrap_or_default();
                        scope.push_dynamic(
                            format!("{name}_live").as_str(),
                            Dynamic::from(MEntry::new(map, primary)),
                        );
                    } else {
                        scope.push(format!("{name}_live").as_str(), b.live_val().unwrap_or(0.0));
                    }
                    scope.push(format!("{name}_fill").as_str(), ratio);
                }
            }
        }

        // Bar field arrays (newest bar at index [0]).
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

        // Regime output slots (regime block writes these; main block reads).
        scope.push("trend",       String::new());
        scope.push("trend_value", 0.0_f64);
        scope.push("vol",         String::new());
        scope.push("vol_value",   0.0_f64);
        scope.push("liq",         String::new());
        scope.push("liq_value",   0.0_f64);

        // Signal output slots.
        scope.push("entry",     false);
        scope.push("exit",      false);
        scope.push("long",      false);
        scope.push("short",     false);
        scope.push("tp",        0.0_f64);
        scope.push("sl",        0.0_f64);
        scope.push("strength",  1.0_f64);
        scope.push("is_offset", false);
        scope.push("reason",    String::new());
        scope.push("atr",       0.0_f64);

        // ── 8. Regime block ────────────────────────────────────────────────────
        if let Some(regime_ast) = &self.regime_ast {
            if self.engine.run_ast_with_scope(&mut scope, regime_ast).is_ok() {
                let trend_status = scope.get_value::<String>("trend").unwrap_or_default();
                let trend_value  = scope.get_value::<f64>("trend_value").unwrap_or(0.0);
                let vol_status   = scope.get_value::<String>("vol").unwrap_or_default();
                let vol_value    = scope.get_value::<f64>("vol_value").unwrap_or(0.0);
                let liq_status   = scope.get_value::<String>("liq").unwrap_or_default();
                let liq_value    = scope.get_value::<f64>("liq_value").unwrap_or(0.0);
                self.current_regime = Some(RegimeState::new(
                    RegimeDimension::new(trend_value, trend_status),
                    RegimeDimension::new(vol_value,   vol_status),
                    RegimeDimension::new(liq_value,   liq_status),
                ));
            }
        }

        // ── 9. Main AST ────────────────────────────────────────────────────────
        if self.engine.run_ast_with_scope(&mut scope, &self.ast).is_err() {
            return vec![];
        }

        // ── 10. Plot series drain ──────────────────────────────────────────────
        if let Ok(mut buf) = self.plot_buf.lock() {
            if let Some(series) = &mut self.series {
                for (n, v) in buf.drain(..) {
                    series.entry(n).or_default().push((base_bar.timestamp, v));
                }
            } else {
                buf.clear();
            }
        }

        // ── 11. Extract signals ────────────────────────────────────────────────
        let symbol    = base_bar.symbol.as_str();
        let strength  = scope.get_value::<f64>("strength").unwrap_or(1.0).clamp(0.0, 1.0);
        let target    = scope.get_value::<f64>("tp").filter(|&v| v != 0.0);
        let stop      = scope.get_value::<f64>("sl").filter(|&v| v != 0.0);
        let is_offset = scope.get_value::<bool>("is_offset").unwrap_or(false);
        let reason    = scope.get_value::<String>("reason").filter(|s| !s.is_empty());
        let atr       = scope.get_value::<f64>("atr").filter(|&v| v > 0.0);

        let go_long  = scope.get_value::<bool>("long").unwrap_or(false)
                    || scope.get_value::<bool>("entry").unwrap_or(false);
        let go_short = scope.get_value::<bool>("short").unwrap_or(false);
        let go_exit  = scope.get_value::<bool>("exit").unwrap_or(false);

        if go_long {
            let mut sig = Signal::long(base_bar.timestamp, symbol, strength);
            sig.price        = Some(base_bar.close);
            sig.target_price = target;
            sig.stop_price   = stop;
            sig.is_offset    = is_offset;
            sig.reason       = reason;
            sig.atr          = atr;
            return vec![sig];
        }
        if go_short {
            let mut sig = Signal::short(base_bar.timestamp, symbol, strength);
            sig.price        = Some(base_bar.close);
            sig.target_price = target;
            sig.stop_price   = stop;
            sig.is_offset    = is_offset;
            sig.reason       = reason;
            sig.atr          = atr;
            return vec![sig];
        }
        if go_exit {
            let mut sig = Signal::exit(base_bar.timestamp, symbol);
            sig.reason = reason;
            sig.atr    = atr;
            return vec![sig];
        }

        vec![]
    }
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;

    fn mk_bar(ts: i64, c: f64) -> Bar {
        Bar::new(ts, "TEST", c, c * 1.005, c * 0.995, c, 1000.0)
    }

    // ── Compilation ──────────────────────────────────────────────────────────

    #[test]
    fn from_script_base_tf_only_compiles() {
        let script = r#"
let ema9  = ind.ema(9);
let ema21 = ind.ema(21);
if cross_above(ema9, ema21) { entry = true; }
if cross_below(ema9, ema21) { exit  = true; }
"#;
        MtfScriptStrategy::from_script(script, Timeframe::M1).expect("should compile");
    }

    #[test]
    fn from_script_with_htf_compiles() {
        let script = r#"
let h1_ema20 = ind.ema(20, "H1");
let m1_rsi5  = ind.rsi(5);
if rising(h1_ema20) && m1_rsi5[0] < 40.0 { entry = true; }
if falling(h1_ema20) { exit = true; }
"#;
        let s = MtfScriptStrategy::from_script(script, Timeframe::M1).expect("should compile");
        assert!(s.declared_htfs().contains(&Timeframe::H1));
    }

    #[test]
    fn from_params_requires_script_key() {
        assert!(MtfScriptStrategy::from_params(&serde_json::json!({})).is_err());
    }

    #[test]
    fn duplicate_indicator_name_errors() {
        let script = r#"
let ema9 = ind.ema(9);
regime {
    let ema9 = ind.ema(9);
    trend = "x";
}
"#;
        let err = MtfScriptStrategy::from_script(script, Timeframe::M1)
            .err()
            .expect("should reject duplicate name");
        assert!(err.to_string().contains("ema9"), "{err}");
    }

    #[test]
    fn declared_htfs_empty_for_base_only_script() {
        let script = "let ema9 = ind.ema(9);";
        let s = MtfScriptStrategy::from_script(script, Timeframe::M1).unwrap();
        assert!(s.declared_htfs().is_empty());
    }

    #[test]
    fn declared_htfs_multi_tf() {
        let script = r#"
let h1_ema = ind.ema(20, "H1");
let h4_atr = ind.atr(14, "H4");
"#;
        let s = MtfScriptStrategy::from_script(script, Timeframe::M1).unwrap();
        let htfs = s.declared_htfs();
        assert!(htfs.contains(&Timeframe::H1));
        assert!(htfs.contains(&Timeframe::H4));
    }

    // ── Regime block ─────────────────────────────────────────────────────────

    #[test]
    fn regime_block_compiles() {
        let script = r#"
let ema9  = ind.ema(9);
let ema21 = ind.ema(21);

regime {
    let adx14 = ind.adx(14);
    if adx14[0].adx > 25.0 { trend = "trending"; trend_value = adx14[0].adx; }
    else                   { trend = "ranging";  trend_value = adx14[0].adx; }
}

if cross_above(ema9, ema21) && trend == "trending" { entry = true; }
if cross_below(ema9, ema21) { exit = true; }
"#;
        MtfScriptStrategy::from_script(script, Timeframe::M1).expect("regime should compile");
    }

    /// Helper: run the strategy through N base bars using direct on_bars calls.
    /// Returns the last set of signals produced.
    fn run_strategy_on_bars(
        strategy: &mut MtfScriptStrategy,
        bars: &[Bar],
        base_tf: Timeframe,
    ) -> Vec<Signal> {
        use std::collections::{HashMap, VecDeque};
        use alm_core::{MtfSnapshot, TfBarEvent, TfView};

        let mut last_signals = vec![];
        let mut confirmed: VecDeque<Bar> = VecDeque::new();

        for bar in bars {
            confirmed.push_back(bar.clone());
            let events_vec = vec![TfBarEvent { tf: base_tf, bar }];
            let mut views: HashMap<Timeframe, TfView<'_>> = HashMap::new();
            views.insert(base_tf, TfView { tf: base_tf, confirmed: &confirmed });

            let snap = MtfSnapshot {
                base_tf,
                close_ts: bar.timestamp + base_tf.duration_ms(),
                events: &events_vec,
                views: &views,
            };
            last_signals = strategy.on_bars(snap);
        }
        last_signals
    }

    #[test]
    fn strategy_runs_200_bars_no_panic() {
        let script = r#"
let ema9  = ind.ema(9);
let ema21 = ind.ema(21);
if cross_above(ema9, ema21) { entry = true; }
if cross_below(ema9, ema21) { exit  = true; }
"#;
        let mut s = MtfScriptStrategy::from_script(script, Timeframe::M1).unwrap();
        let bars: Vec<Bar> = (0..200_i64)
            .map(|i| mk_bar(i * 60_000, 100.0 + i as f64 * 0.5))
            .collect();
        run_strategy_on_bars(&mut s, &bars, Timeframe::M1);
        // No panic = pass.
    }

    #[test]
    fn strategy_does_not_fire_before_warmup() {
        let script = r#"
let ema21 = ind.ema(21);
if ema21[0] > close[0] { entry = true; }
"#;
        let mut s = MtfScriptStrategy::from_script(script, Timeframe::M1).unwrap();
        // Send fewer bars than DEFAULT_BUF_DEPTH — no signals expected.
        let bar = mk_bar(0, 100.0);
        let events = vec![alm_core::TfBarEvent { tf: Timeframe::M1, bar: &bar }];
        let mut win = std::collections::VecDeque::new();
        win.push_back(bar.clone());
        let views = HashMap::from([(Timeframe::M1, alm_core::TfView {
            tf: Timeframe::M1, confirmed: &win,
        })]);
        let snap = alm_core::MtfSnapshot {
            base_tf: Timeframe::M1,
            close_ts: 60_000,
            events: &events,
            views: &views,
        };
        let sigs = s.on_bars(snap);
        // Only 1 bar, buf_depth = 2, not warm yet.
        assert!(sigs.is_empty(), "should not fire before warmup");
    }

    #[test]
    fn reset_clears_state() {
        let script = r#"
let ema9 = ind.ema(9);
if ema9[0] > close[0] { entry = true; }
"#;
        let mut s = MtfScriptStrategy::from_script(script, Timeframe::M1).unwrap();
        let bars: Vec<Bar> = (0..30_i64).map(|i| mk_bar(i * 60_000, 100.0)).collect();
        run_strategy_on_bars(&mut s, &bars, Timeframe::M1);
        s.reset();
        // After reset, bar_buf is empty → not warm → no signals on first bar.
        let bar = mk_bar(0, 100.0);
        let events = vec![alm_core::TfBarEvent { tf: Timeframe::M1, bar: &bar }];
        let mut win = std::collections::VecDeque::new();
        win.push_back(bar.clone());
        let views = HashMap::from([(Timeframe::M1, alm_core::TfView {
            tf: Timeframe::M1, confirmed: &win,
        })]);
        let snap = alm_core::MtfSnapshot {
            base_tf: Timeframe::M1,
            close_ts: 60_000,
            events: &events,
            views: &views,
        };
        let sigs = s.on_bars(snap);
        assert!(sigs.is_empty(), "after reset, should not be warm on first bar");
    }

    // ── Integration with MtfEngine ────────────────────────────────────────────

    #[test]
    fn end_to_end_base_tf_only_with_engine() {
        use alm_data::BarVecFeed;
        use alm_engine::MtfEngine;
        use crate::FixedFractional;

        let script = r#"
let ema9  = ind.ema(9);
let ema21 = ind.ema(21);
if cross_above(ema9, ema21) { entry = true; }
if cross_below(ema9, ema21) { exit  = true; }
"#;
        let bars: Vec<Bar> = (0..200_i64)
            .map(|i| mk_bar(i * 60_000, 100.0 + i as f64 * 0.5))
            .collect();
        let strategy = MtfScriptStrategy::from_script(script, Timeframe::M1).unwrap();
        let mut engine = MtfEngine::sync(
            10_000.0, strategy, FixedFractional::fractional(0.95, 1), 0.0, 0.0,
        );
        engine.add_feed(Timeframe::M1, BarVecFeed::new(bars, "TEST".into()));
        let _ = engine.run(0.0);
        assert!(!engine.portfolio.equity_curve.is_empty(), "equity curve must have entries");
    }

    #[test]
    fn end_to_end_with_h1_filter_engine() {
        use alm_data::BarVecFeed;
        use alm_engine::MtfEngine;
        use crate::FixedFractional;

        let script = r#"
let h1_ema = ind.ema(20, "H1");
let rsi    = ind.rsi(14);
if rising(h1_ema) && rsi[0] < 40.0 { entry = true; }
if falling(h1_ema) || rsi[0] > 70.0 { exit = true; }
"#;
        let m1_bars: Vec<Bar> = (0..4 * 60_i64)
            .map(|i| mk_bar(i * 60_000, 100.0 + i as f64 * 0.1))
            .collect();
        let h1_bars: Vec<Bar> = (0..4_i64)
            .map(|i| mk_bar(i * 3_600_000, 100.0 + i as f64 * 6.0))
            .collect();

        let strategy = MtfScriptStrategy::from_script(script, Timeframe::M1).unwrap();
        assert!(strategy.declared_htfs().contains(&Timeframe::H1));

        let mut engine = MtfEngine::sync(
            10_000.0, strategy, FixedFractional::fractional(0.95, 1), 0.0, 0.0,
        ).with_base_tf(Timeframe::M1);
        engine.add_feed(Timeframe::M1, BarVecFeed::new(m1_bars, "TEST".into()));
        engine.add_feed(Timeframe::H1, BarVecFeed::new(h1_bars, "TEST".into()));
        let _ = engine.run(0.0);
        assert!(!engine.portfolio.equity_curve.is_empty());
    }
}

//! Stateful chart object for browser-side rendering.
//!
//! `ChartState` holds a bar series, a set of named-indicator configs, and an
//! optional Rhai script evaluated for indicator-only output.
//!
//! Bar mutations are incremental where possible:
//!
//! - `add_tail` / `load_tail` — feed new bars through already-warm indicator
//!   instances and append to the cached series. O(new_bars).
//! - `add_head` / `load_head` — historical pagination; prepending invalidates
//!   indicator state, so all instances are reset and replayed from scratch. O(n).
//! - `set_indicators` — rebuilds slots and replays all current bars. O(n).
//!
//! # API shape (mirrors herald `/api/v1/data/:source/:symbol`)
//!
//! ## Named indicator configs (`set_indicators`)
//! ```json
//! [{"type":"ema","period":20,"label":"ema20"}, {"type":"macd","period":12}]
//! ```
//!
//! ## Script indicators (`set_script`)
//! Pass a Rhai script — indicator series are auto-extracted from `ind.TYPE(period)`
//! declarations exactly as the herald `/api/v1/data` endpoint does. `candle.transform`
//! directives inside the script are honoured. Signals (`long`/`short`/`exit`,
//! with resolved TP/SL levels) are collected live into the snapshot's `signals`
//! array as bars stream in — for entry/exit markers + TP/SL overlays.
//!
//! ## Snapshot output
//! ```json
//! {
//!   "bars": { "t":[...], "o":[...], "h":[...], "l":[...], "c":[...], "v":[...] },
//!   "indicators": {
//!     "ema20":           { "value": [null, 101.5, 102.1, ...] },
//!     "macd14.macd":     { "value": [...] },
//!     "macd14.signal":   { "value": [...] },
//!     "macd14.histogram":{ "value": [...] }
//!   }
//! }
//! ```
//!
//! `null` entries represent bars where the indicator has not yet warmed up.
//! Named indicators and script indicators are merged; script keys take priority
//! on collision.
//!
//! ## Backtest (explicit)
//! Call `backtest(script, config_json)` or `backtest_named(name, params, config_json)`
//! separately — never triggered by bar mutations.

use std::collections::HashMap;

use serde::Serialize;
use serde_json::Value;
use wasm_bindgen::prelude::*;

use alm_core::{Bar, Direction, Signal, Strategy};
use alm_indicator::{HeikenAshi, IndicatorBox};
use alm_strategy::ScriptStrategy;

use crate::{build_bars, js_error, parse_config, run_strategy};

// ── Snapshot types ─────────────────────────────────────────────────────────────

/// Full chart snapshot returned after every bar mutation.
#[derive(Serialize)]
struct ChartSnapshot<'a> {
    bars:       BarsOut<'a>,
    indicators: HashMap<String, HashMap<String, Vec<Value>>>,
    /// Live signals emitted by the script slot as bars stream in (entry/exit +
    /// resolved TP/SL levels). Empty when no script is set.
    signals:    &'a [SignalOut],
}

/// One live signal emitted by the script, shaped for chart overlay: entry/exit
/// markers + TP/SL boxes. `tp`/`sl` are resolved to **absolute price levels**
/// (offset signals are converted using `price`) so the FE can draw directly.
#[derive(Serialize, Clone)]
struct SignalOut {
    /// Bar timestamp (ms).
    t:        f64,
    /// `"long"` | `"short"` | `"exit"`.
    dir:      &'static str,
    /// Signal price (bar close at emission).
    #[serde(skip_serializing_if = "Option::is_none")]
    price:    Option<f64>,
    /// Take-profit, absolute price level.
    #[serde(skip_serializing_if = "Option::is_none")]
    tp:       Option<f64>,
    /// Stop-loss, absolute price level.
    #[serde(skip_serializing_if = "Option::is_none")]
    sl:       Option<f64>,
    /// Time-based exit hint: hold at most this many bars. Metadata only — the
    /// live eval does not act on it; the FE may draw it as a time-stop / box cap.
    #[serde(skip_serializing_if = "Option::is_none")]
    max_bars: Option<usize>,
    strength: f64,
    #[serde(skip_serializing_if = "Option::is_none")]
    reason:   Option<String>,
}

impl SignalOut {
    fn from_signal(s: &Signal) -> Self {
        let dir = match s.direction {
            Direction::Long  => "long",
            Direction::Short => "short",
            Direction::Exit  => "exit",
        };
        // Resolve offset TP/SL to absolute levels so the FE draws price lines/boxes directly.
        // Offsets are magnitudes; the DIRECTION decides which side they land on:
        //   long  → TP above (price + d), SL below (price − d)
        //   short → TP below (price − d), SL above (price + d)
        let (tp, sl) = if s.is_offset {
            let long = matches!(s.direction, Direction::Long);
            let tp = s.price.zip(s.target_price)
                .map(|(p, d)| if long { p + d.abs() } else { p - d.abs() });
            let sl = s.price.zip(s.stop_price)
                .map(|(p, d)| if long { p - d.abs() } else { p + d.abs() });
            (tp, sl)
        } else {
            (s.target_price, s.stop_price)
        };
        SignalOut {
            t: s.timestamp as f64,
            dir,
            price: s.price,
            tp,
            sl,
            max_bars: s.max_bars_held,
            strength: s.strength,
            reason: s.reason.clone(),
        }
    }
}


#[derive(Serialize)]
struct BarsOut<'a> {
    t: Vec<f64>,
    o: Vec<&'a f64>,
    h: Vec<&'a f64>,
    l: Vec<&'a f64>,
    c: Vec<&'a f64>,
    v: Vec<&'a f64>,
}

// ── IndSlot — stateful indicator instance with cached series ──────────────────

/// One indicator binding: live `IndicatorBox` + accumulated per-field series.
///
/// Series length always equals the number of bars fed so far.
/// Null entries represent bars where the indicator had not yet warmed up.
struct IndSlot {
    label:       String,
    config:      Value,
    ind:         IndicatorBox,
    /// field_name → Vec<Value> aligned to bars (null = not yet warm).
    field_series: HashMap<String, Vec<Value>>,
    /// Bars seen before the first non-null output (warmup length).
    null_prefix: usize,
    /// True once the first non-null output has been received.
    warmed:      bool,
}

impl IndSlot {
    fn new(label: String, config: Value) -> Option<Self> {
        let ind = IndicatorBox::from_config(&config).ok()?;
        Some(Self {
            label,
            config,
            ind,
            field_series: HashMap::new(),
            null_prefix: 0,
            warmed: false,
        })
    }

    /// Feed one bar and update the cached series.
    fn feed(&mut self, bar: &Bar) {
        let result = self.ind.update(bar);
        if !self.warmed {
            match result {
                None => self.null_prefix += 1,
                Some(fields) => {
                    self.warmed = true;
                    // Initialize each field with the null prefix, then push the first value.
                    let mut field_names: Vec<_> = fields.keys().cloned().collect();
                    field_names.sort();
                    for k in &field_names {
                        let v = fields[k];
                        let mut series = vec![Value::Null; self.null_prefix];
                        series.push(f64_to_json(v));
                        self.field_series.insert(k.clone(), series);
                    }
                }
            }
        } else {
            match result {
                None => {
                    for series in self.field_series.values_mut() {
                        series.push(Value::Null);
                    }
                }
                Some(fields) => {
                    for (k, v) in &fields {
                        self.field_series
                            .entry(k.clone())
                            .or_default()
                            .push(f64_to_json(*v));
                    }
                }
            }
        }
    }

    /// Reset indicator state and clear cached series.
    fn reset(&mut self) {
        self.ind.reset();
        self.field_series.clear();
        self.null_prefix = 0;
        self.warmed = false;
    }

    /// Full replay: reset then feed every bar.
    fn rebuild(&mut self, bars: &[Bar]) {
        self.reset();
        for bar in bars {
            self.feed(bar);
        }
    }

    /// Rebuild the `IndicatorBox` from config (in case of config change).
    fn reconfigure(&mut self) -> bool {
        match IndicatorBox::from_config(&self.config) {
            Ok(ind) => { self.ind = ind; true }
            Err(_) => false,
        }
    }
}

fn f64_to_json(v: f64) -> Value {
    if v.is_finite() {
        serde_json::Number::from_f64(v)
            .map(Value::Number)
            .unwrap_or(Value::Null)
    } else {
        Value::Null
    }
}

// ── ScriptSlot — Rhai script evaluated for indicator output only ───────────────

/// Wraps a `ScriptStrategy` in collection mode and replays raw bars through it.
///
/// Indicator series are extracted for plotting; signals (`long`/`short`/`exit`,
/// with resolved TP/SL) are collected live into `signals` for chart overlays.
///
/// Script-level `candle.transform` directives are honoured by `ScriptStrategy`
/// internally, independent of `ChartState`'s chart-level candle transform.
struct ScriptSlot {
    strategy: ScriptStrategy,
    /// Live signals emitted so far, in bar order. Drives chart overlays
    /// (entry/exit markers + TP/SL boxes). Grows on `feed`, cleared on `rebuild`.
    signals:  Vec<SignalOut>,
}

impl ScriptSlot {
    fn new(script: &str) -> Result<Self, String> {
        let strategy = ScriptStrategy::from_script(script)
            .map_err(|e| e.to_string())?;
        Ok(Self { strategy, signals: Vec::new() })
    }

    /// Feed one raw bar through the strategy and collect any emitted signal.
    fn feed(&mut self, bar: &Bar) {
        for sig in self.strategy.on_bar(bar) {
            self.signals.push(SignalOut::from_signal(&sig));
        }
    }

    /// Reset and replay all raw bars from scratch (signals re-collected).
    fn rebuild(&mut self, bars: &[Bar]) {
        self.strategy.reset();
        self.signals.clear();
        for bar in bars {
            self.feed(bar);
        }
    }

    /// Build indicator series aligned to the display bars (matched by timestamp).
    ///
    /// Keys: `var_name` for single-output, `var_name.field` for multi-output —
    /// matching the server-side `take_indicator_series` naming convention.
    fn indicator_map(&self, display_bars: &[Bar]) -> HashMap<String, HashMap<String, Vec<Value>>> {
        let mut out: HashMap<String, HashMap<String, Vec<Value>>> = HashMap::new();
        let Some(series) = self.strategy.series() else { return out };

        for (key, vals) in series {
            let val_map: HashMap<i64, f64> = vals.iter().cloned().collect();
            let mut v: Vec<Value> = Vec::with_capacity(display_bars.len());
            for bar in display_bars {
                if let Some(&val) = val_map.get(&bar.timestamp) {
                    v.push(f64_to_json(val));
                } else {
                    v.push(Value::Null);
                }
            }

            // Flatten into the snapshot's indicator map format:
            // top-level key = full series key, inner map = {"value": [...]}
            out.entry(key.clone())
                .or_default()
                .insert("value".to_string(), v);
        }
        out
    }
}

// ── ChartState ────────────────────────────────────────────────────────────────

/// Stateful chart object — holds bars + live indicator instances.
///
/// Create via `ChartState::new(symbol)`, push bars, then read indicator series.
/// Backtest runs on-demand; it never fires automatically on bar mutations.
#[wasm_bindgen]
pub struct ChartState {
    symbol: String,
    /// Raw OHLCV bars — always kept intact for backtest and head ops.
    bars:   Vec<Bar>,
    /// Candle transform: 0 = raw, 1 = plain HA, N>1 = smooth HA (EMA period N).
    transform_period: usize,
    /// Live Heikin-Ashi state for incremental tail updates. None when raw.
    ha:     Option<HeikenAshi>,
    /// Transformed bars (HA / smooth HA output). Empty when raw.
    /// Aligned to `bars` after warmup; may be shorter during smooth-HA warmup.
    t_bars: Vec<Bar>,
    slots:  Vec<IndSlot>,
    /// Optional Rhai script evaluated for indicator series only (no backtest).
    /// Uses raw bars; script's own `candle.transform` directive is honoured.
    script_slot: Option<ScriptSlot>,
}

#[wasm_bindgen]
impl ChartState {
    // ── Constructor ───────────────────────────────────────────────────────────

    #[wasm_bindgen(constructor)]
    pub fn new(symbol: &str) -> ChartState {
        ChartState {
            symbol:           symbol.to_string(),
            bars:             Vec::new(),
            transform_period: 0,
            ha:               None,
            t_bars:           Vec::new(),
            slots:            Vec::new(),
            script_slot:      None,
        }
    }

    // ── Candle transform ──────────────────────────────────────────────────────

    /// Set the candle transform applied before indicator computation.
    ///
    /// `kind`: `"raw"` | `"ha"` | `"smooth_ha"`
    /// `period`: EMA smoothing period for `"smooth_ha"` (ignored otherwise).
    ///
    /// Triggers a full indicator replay with transformed bars. O(n × indicators).
    pub fn set_candle_transform(&mut self, kind: &str, period: usize) -> JsValue {
        self.transform_period = match kind {
            "ha"        => 1,
            "smooth_ha" => period.max(2),
            _           => 0,   // "raw" or unknown → no transform
        };
        self.rebuild_transform();
        JsValue::NULL
    }

    // ── Script indicators ─────────────────────────────────────────────────────

    /// Set a Rhai script for indicator-only evaluation.
    ///
    /// Indicator series are auto-extracted from `ind.TYPE(period)` declarations
    /// exactly as the herald `/api/v1/data` endpoint does. Signal outputs
    /// (`long`/`short`/`exit` + TP/SL) are collected into the snapshot's
    /// `signals` array as bars stream in.
    ///
    /// The script/backtest ALWAYS evaluate on **raw** OHLCV, independent of the
    /// chart-level candle transform (HA toggle is display-only). This keeps the
    /// on-chart strategy result identical to the deep backtest. The script's own
    /// `candle.transform` directive is handled internally by `ScriptStrategy` on
    /// the raw bars — that's separate from the display toggle.
    ///
    /// Replays all current bars through the script. O(n).
    /// Returns `null` on success, `{error}` on script compile failure.
    pub fn set_script(&mut self, script: &str) -> JsValue {
        let mut slot = match ScriptSlot::new(script) {
            Ok(s)  => s,
            Err(e) => return js_error(&e),
        };
        slot.rebuild(&self.bars);
        self.script_slot = Some(slot);
        JsValue::NULL
    }

    /// Remove the current script; named indicator slots are unaffected.
    pub fn clear_script(&mut self) {
        self.script_slot = None;
    }

    // ── Indicator configs ─────────────────────────────────────────────────────

    /// Set indicator configs. Replaces the previous set, replays all current bars.
    ///
    /// `config_json`: JSON array of indicator specs, e.g.
    /// `[{"type":"ema","period":20,"label":"ema20"}, {"type":"rsi","period":14}]`
    ///
    /// Returns `null` on success, `{error}` on parse/validate failure.
    pub fn set_indicators(&mut self, config_json: &str) -> JsValue {
        let arr: Vec<Value> = match serde_json::from_str(config_json) {
            Ok(Value::Array(a)) => a,
            Ok(_) => return js_error("indicators must be a JSON array"),
            Err(e) => return js_error(&e.to_string()),
        };

        let mut slots = Vec::with_capacity(arr.len());
        for cfg in &arr {
            let label = cfg.get("label")
                .and_then(|v| v.as_str())
                .map(|s| s.to_string())
                .unwrap_or_else(|| {
                    let t = cfg.get("type").and_then(|v| v.as_str()).unwrap_or("ind");
                    let p = cfg.get("period").and_then(|v| v.as_u64()).unwrap_or(0);
                    if p > 0 { format!("{t}{p}") } else { t.to_string() }
                });
            match IndSlot::new(label, cfg.clone()) {
                Some(slot) => slots.push(slot),
                None => {
                    let t = cfg.get("type").and_then(|v| v.as_str()).unwrap_or("?");
                    return js_error(&format!("indicator '{t}': failed to build"));
                }
            }
        }

        self.slots = slots;
        let effective: Vec<Bar> = self.effective_bars().to_vec();
        for slot in &mut self.slots {
            slot.rebuild(&effective);
        }
        JsValue::NULL
    }

    /// Add a single indicator. Replays it through existing bars without touching
    /// other slots. O(n) for this indicator only.
    ///
    /// `config_json`: single indicator spec, e.g. `{"type":"ema","period":20,"label":"ema20"}`
    ///
    /// Returns `null` on success, `{error}` on failure.
    pub fn add_indicator(&mut self, config_json: &str) -> JsValue {
        let cfg: Value = match serde_json::from_str(config_json) {
            Ok(v) => v,
            Err(e) => return js_error(&e.to_string()),
        };
        let label = cfg.get("label")
            .and_then(|v| v.as_str())
            .map(|s| s.to_string())
            .unwrap_or_else(|| {
                let t = cfg.get("type").and_then(|v| v.as_str()).unwrap_or("ind");
                let p = cfg.get("period").and_then(|v| v.as_u64()).unwrap_or(0);
                if p > 0 { format!("{t}{p}") } else { t.to_string() }
            });
        let mut slot = match IndSlot::new(label, cfg) {
            Some(s) => s,
            None => {
                return js_error("failed to build indicator");
            }
        };
        let effective: Vec<Bar> = self.effective_bars().to_vec();
        slot.rebuild(&effective);
        self.slots.push(slot);
        JsValue::NULL
    }

    /// Update the config of an existing indicator by label. Replays only that
    /// slot through all bars. O(n × 1). No-op (returns error) if label not found.
    ///
    /// `config_json`: new indicator spec — the label field is ignored, the
    /// existing slot's label is kept.
    pub fn update_indicator(&mut self, label: &str, config_json: &str) -> JsValue {
        let cfg: Value = match serde_json::from_str(config_json) {
            Ok(v) => v,
            Err(e) => return js_error(&e.to_string()),
        };
        // Collect effective bars before the mutable borrow on slots.
        let effective: Vec<Bar> = self.effective_bars().to_vec();
        match self.slots.iter_mut().find(|s| s.label == label) {
            None => js_error(&format!("indicator '{label}' not found")),
            Some(slot) => {
                slot.config = cfg;
                if !slot.reconfigure() {
                    return js_error(&format!("indicator '{label}': failed to build"));
                }
                slot.rebuild(&effective);
                JsValue::NULL
            }
        }
    }

    /// Remove the indicator with the given label. O(1). No-op if not found.
    pub fn remove_indicator(&mut self, label: &str) {
        self.slots.retain(|s| s.label != label);
    }

    // ── Bar mutations ─────────────────────────────────────────────────────────

    /// Append a single bar. Feeds it through the candle transform (if any)
    /// incrementally, then through existing indicator instances. O(indicators).
    pub fn add_tail(&mut self, t: f64, o: f64, h: f64, l: f64, c: f64, v: f64) -> JsValue {
        let timestamp = if t < 10_000_000_000.0 {
            (t * 1000.0) as i64
        } else {
            t as i64
        };
        let bar = Bar::new(timestamp, &self.symbol, o, h, l, c, v);

        // If the new bar has the same timestamp as the last bar, it represents an update
        // to the active forming bar (live ticking). Update in-place and rebuild indicators.
        if let Some(last) = self.bars.last_mut() {
            if last.timestamp == timestamp {
                *last = bar;
                self.rebuild_transform();
                return self.snapshot();
            }
        }

        self.bars.push(bar.clone());
        // Script slot always gets RAW bars (display transform must not affect it).
        if let Some(ss) = &mut self.script_slot { ss.feed(&bar); }
        if self.transform_period > 0 {
            if let Some(ha) = &mut self.ha {
                if let Some(hb) = ha.update(bar.open, bar.high, bar.low, bar.close) {
                    let tb = Bar::new(bar.timestamp, &bar.symbol,
                        hb.open, hb.high, hb.low, hb.close, bar.volume);
                    self.t_bars.push(tb.clone());
                    for slot in &mut self.slots {
                        slot.feed(&tb);
                    }
                }
                // else: still in HA warmup — no output yet, slots not fed
            }
        } else {
            for slot in &mut self.slots {
                slot.feed(&bar);
            }
        }
        self.snapshot()
    }

    /// Prepend a single bar (historical pagination). Resets and replays all
    /// indicator instances from scratch. O(n × indicators).
    pub fn add_head(&mut self, t: f64, o: f64, h: f64, l: f64, c: f64, v: f64) -> JsValue {
        let timestamp = if t < 10_000_000_000.0 {
            (t * 1000.0) as i64
        } else {
            t as i64
        };
        let bar = Bar::new(timestamp, &self.symbol, o, h, l, c, v);
        self.bars.insert(0, bar);
        self.rebuild_transform();
        self.snapshot()
    }

    /// Append many bars at once. Feeds each through the transform incrementally.
    /// O(new × indicators).
    pub fn load_tail(
        &mut self,
        t: &[f64], o: &[f64], h: &[f64], l: &[f64], c: &[f64], v: &[f64],
    ) -> JsValue {
        let new_bars = build_bars(&self.symbol, t, o, h, l, c, v);
        // Script slot always gets RAW bars (display transform must not affect it).
        if let Some(ss) = &mut self.script_slot {
            for bar in &new_bars { ss.feed(bar); }
        }
        if self.transform_period > 0 {
            if let Some(ha) = &mut self.ha {
                for bar in &new_bars {
                    if let Some(hb) = ha.update(bar.open, bar.high, bar.low, bar.close) {
                        let tb = Bar::new(bar.timestamp, &bar.symbol,
                            hb.open, hb.high, hb.low, hb.close, bar.volume);
                        self.t_bars.push(tb.clone());
                        for slot in &mut self.slots {
                            slot.feed(&tb);
                        }
                    }
                }
            }
        } else {
            for bar in &new_bars {
                for slot in &mut self.slots {
                    slot.feed(bar);
                }
            }
        }
        self.bars.extend(new_bars);
        self.snapshot()
    }

    /// Prepend many bars (historical pagination). Resets and replays everything.
    /// O(n × indicators).
    pub fn load_head(
        &mut self,
        t: &[f64], o: &[f64], h: &[f64], l: &[f64], c: &[f64], v: &[f64],
    ) -> JsValue {
        let new_bars = build_bars(&self.symbol, t, o, h, l, c, v);
        let old = std::mem::replace(&mut self.bars, new_bars);
        self.bars.extend(old);
        self.rebuild_transform();
        self.snapshot()
    }

    // ── State management ──────────────────────────────────────────────────────

    /// Clear bars and reset all indicator instances; keep configs and script.
    pub fn reset_bars(&mut self) {
        self.bars.clear();
        self.t_bars.clear();
        self.ha = if self.transform_period > 0 {
            Some(HeikenAshi::new(self.transform_period))
        } else {
            None
        };
        for slot in &mut self.slots {
            slot.reset();
        }
        if let Some(ss) = &mut self.script_slot {
            ss.rebuild(&[]);
        }
    }

    /// Clear everything — bars, indicator instances, configs, and script.
    pub fn reset(&mut self) {
        self.bars.clear();
        self.t_bars.clear();
        self.ha = None;
        self.slots.clear();
        self.script_slot = None;
    }

    /// Returns the number of displayable bars (post-transform).
    pub fn bar_count(&self) -> usize {
        self.effective_bars().len()
    }

    /// Returns the number of raw bars loaded.
    pub fn raw_bar_count(&self) -> usize {
        self.bars.len()
    }

    pub fn symbol(&self) -> String {
        self.symbol.clone()
    }

    // ── Snapshot (reads cached state, no recompute) ────────────────────────────

    /// Return the current snapshot. Reads from cached series — O(1) build.
    /// Useful after `set_indicators` to get initial state before any bar arrives.
    pub fn snapshot(&self) -> JsValue {
        let snap = self.build_snapshot();
        crate::to_js(&snap)
    }

    // ── Backtest (explicit, never auto) ───────────────────────────────────────

    /// Run a script backtest over the current bar series.
    ///
    /// `config_json`: `{"initial_capital":10000,"position_size_pct":1.0,...}`
    pub fn backtest(&self, script: &str, config_json: &str) -> JsValue {
        if self.bars.is_empty() {
            return js_error("no bars loaded");
        }
        // On-chart is single-timeframe — HTF scripts can't load/warm extra feeds here.
        let htfs = alm_strategy::probe_script_htfs(script);
        if !htfs.is_empty() {
            let tfs = htfs.iter().map(|t| format!("{t:?}")).collect::<Vec<_>>().join(", ");
            return js_error(&format!(
                "Multi-timeframe script (uses {tfs}) — on-chart is single-timeframe only. Run it with Deep backtest."
            ));
        }
        let strategy = match ScriptStrategy::from_script(script) {
            Ok(s) => s,
            Err(e) => return js_error(&format!("script: {e}")),
        };
        let cfg = parse_config(config_json);
        // Backtest ALWAYS runs on RAW bars — the chart-level candle transform is
        // display-only and must not change the result. (Same data as deep backtest.)
        let out = run_strategy(&self.symbol, &self.bars, Box::new(strategy), &cfg);
        crate::to_js(&out)
    }

    /// Run a named-strategy backtest over the current bar series.
    pub fn backtest_named(
        &self,
        strategy_name: &str,
        params_json: &str,
        config_json: &str,
    ) -> JsValue {
        if self.bars.is_empty() {
            return js_error("no bars loaded");
        }
        let params: Value = match serde_json::from_str(params_json) {
            Ok(v) => v,
            Err(e) => return js_error(&format!("params: {e}")),
        };
        let strategy = match alm_strategy::build_strategy(strategy_name, &params) {
            Ok(s) => s,
            Err(e) => return js_error(&format!("strategy '{strategy_name}': {e}")),
        };
        let cfg = parse_config(config_json);
        // Backtest ALWAYS runs on RAW bars (see backtest()).
        let out = run_strategy(&self.symbol, &self.bars, strategy, &cfg);
        crate::to_js(&out)
    }
}

// ── Internal helpers ──────────────────────────────────────────────────────────

impl ChartState {
    /// Returns transformed bars when a transform is active, otherwise raw bars.
    fn effective_bars(&self) -> &[Bar] {
        if self.transform_period > 0 && !self.t_bars.is_empty() {
            &self.t_bars
        } else if self.transform_period > 0 {
            // Transform active but no output yet (smooth HA still warming up).
            &[]
        } else {
            &self.bars
        }
    }

    /// Full replay of raw bars through the HA transform, then rebuild all slots
    /// (named indicators + script slot).
    ///
    /// ## Design intent — indicators compute on transformed candles
    ///
    /// Named indicator slots are rebuilt on the *transformed* bars (HA / smooth HA)
    /// so that indicator values match what the user sees visually. For example, an
    /// EMA overlaid on a Heikin-Ashi chart smooths the HA close values, not the raw
    /// close — this is the conventional expectation on most charting platforms.
    ///
    /// Script slot is an exception: it always replays raw bars regardless of the
    /// chart-level transform, because scripts are evaluated by the strategy engine
    /// which always operates on raw OHLCV (same data the backtest engine uses).
    ///
    /// ## Deferred: option to pin indicators to raw prices
    ///
    /// If we ever want indicators to always compute on raw prices (transform = display
    /// only), pass `&self.bars` to all `slot.rebuild()` calls and keep `t_bars` for
    /// display only. Not done yet — evaluate when the distinction becomes user-visible.
    fn rebuild_transform(&mut self) {
        // Script slot always replays RAW bars — the chart-level transform is
        // display-only and must not change the strategy result.
        if let Some(ss) = &mut self.script_slot {
            ss.rebuild(&self.bars);
        }
        if self.transform_period == 0 {
            self.ha = None;
            self.t_bars.clear();
            for slot in &mut self.slots {
                slot.rebuild(&self.bars);
            }
        } else {
            let mut ha = HeikenAshi::new(self.transform_period);
            self.t_bars.clear();
            for bar in &self.bars {
                if let Some(hb) = ha.update(bar.open, bar.high, bar.low, bar.close) {
                    self.t_bars.push(Bar::new(
                        bar.timestamp, &bar.symbol,
                        hb.open, hb.high, hb.low, hb.close, bar.volume,
                    ));
                }
            }
            self.ha = Some(ha);
            for slot in &mut self.slots {
                slot.rebuild(&self.t_bars);
            }
        }
    }
}

// ── Internal snapshot builder ─────────────────────────────────────────────────

impl ChartState {
    fn build_snapshot(&self) -> ChartSnapshot<'_> {
        let display = self.effective_bars();
        let bars_out = BarsOut {
            t: display.iter().map(|b| (b.timestamp / 1000) as f64).collect(),
            o: display.iter().map(|b| &b.open).collect(),
            h: display.iter().map(|b| &b.high).collect(),
            l: display.iter().map(|b| &b.low).collect(),
            c: display.iter().map(|b| &b.close).collect(),
            v: display.iter().map(|b| &b.volume).collect(),
        };

        // Named indicator slots — padded to display bar count.
        let bar_count = display.len();
        let mut indicators: HashMap<String, HashMap<String, Vec<Value>>> = self.slots.iter()
            .map(|slot| {
                let mut fields = slot.field_series.clone();
                // Ensure every field series is padded to bar count with Null
                // (handles the case where the slot never warmed up at all).
                for series in fields.values_mut() {
                    while series.len() < bar_count {
                        series.push(Value::Null);
                    }
                }
                (slot.label.clone(), fields)
            })
            .collect();

        // Script slot indicators — keyed against display bar count; merged last so
        // script keys take priority on collision with named slots.
        if let Some(ss) = &self.script_slot {
            for (key, inner) in ss.indicator_map(display) {
                indicators.insert(key, inner);
            }
        }

        let signals = self.script_slot.as_ref()
            .map(|ss| ss.signals.as_slice())
            .unwrap_or(&[]);

        ChartSnapshot { bars: bars_out, indicators, signals }
    }
}

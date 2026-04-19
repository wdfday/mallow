pub mod condition;

use std::collections::HashMap;

use anyhow::{bail, Result};
use alm_core::{Bar, Signal, Strategy};
use alm_indicator::{Atr, IndicatorBox};
use serde_json::Value;

use self::condition::ConditionGroup;
use crate::bar_resampler::{TimeBarResampler, parse_timeframe_ms};
use crate::candle_type::{CandleTransform, CandleType};

/// Internal state for one named indicator.
struct IndicatorState {
    box_: IndicatorBox,
    /// Time-based resampler — present when `"tf"` is set in indicator config.
    resampler: Option<TimeBarResampler>,
    current: HashMap<String, f64>,
    previous: HashMap<String, f64>,
}

impl IndicatorState {
    fn new(box_: IndicatorBox) -> Self {
        Self { box_, resampler: None, current: HashMap::new(), previous: HashMap::new() }
    }

    /// Attach a time-based resampler using a TradingView-style timeframe string.
    /// `"H1"` → H1 bars, `"M15"` → M15 bars, `"D1"` → daily bars.
    /// Returns `self` unchanged if `tf` is unrecognised.
    fn with_tf(mut self, tf: &str) -> Self {
        if let Some(ms) = parse_timeframe_ms(tf) {
            self.resampler = Some(TimeBarResampler::new(ms));
        }
        self
    }

    fn update(&mut self, bar: &Bar) {
        // Time-based MTF: feed indicator only when the resampler emits a HTF bar.
        // Between HTF bars: hold current (no update) so ready-check stays true.
        let agg = match &mut self.resampler {
            Some(rs) => rs.push(bar),
            None => Some(bar.clone()),
        };
        if let Some(b) = agg {
            if let Some(vals) = self.box_.update(&b) {
                self.previous = std::mem::replace(&mut self.current, vals);
            }
        }
    }

    fn reset(&mut self) {
        self.box_.reset();
        if let Some(rs) = &mut self.resampler { rs.reset(); }
        self.current.clear();
        self.previous.clear();
    }
}

/// A strategy defined entirely by a JSON config — no Rust code needed for new combinations.
///
/// # JSON shape
/// ```json
/// {
///   "strategy": "dynamic",
///   "params": {
///     "candle_type": "heiken_ashi",
///     "indicators": {
///       "rsi14":  { "type": "rsi",  "period": 14 },
///       "ema200": { "type": "ema",  "period": 200 },
///       "macd":   { "type": "macd", "fast": 12, "slow": 26, "signal": 9 }
///     },
///     "entry": {
///       "logic": "and",
///       "rules": [
///         { "source": "rsi14", "field": "value",     "op": "lt",          "value": 30 },
///         { "source": "close", "field": "value",     "op": "gt",          "compare": "ema200" },
///         { "source": "macd",  "field": "histogram", "op": "cross_above", "value": 0 }
///       ]
///     },
///     "exit": {
///       "logic": "or",
///       "rules": [
///         { "source": "rsi14", "field": "value",     "op": "gt", "value": 70 },
///         { "source": "macd",  "field": "histogram", "op": "lt", "value": 0 }
///       ]
///     }
///   }
/// }
/// ```
///
/// ## `candle_type` options
/// - `"raw"` (default) — dùng nến gốc
/// - `"heiken_ashi"` — transform sang Heiken Ashi trước khi feed indicators
/// - `"smooth_ha"` + `"ha_smooth": N` — Smooth Heiken Ashi (EMA N trước HA)
///
/// ## Optional TP / SL
///
/// All modes stack — whichever fires first exits. All stack on top of any `"exit"` condition group.
///
/// ```json
/// { "tp": 0.05, "sl": 0.02, "tp_atr": 2.0, "sl_atr": 1.0, "atr_period": 14 }
/// ```
///
/// - `"tp"` / `"sl"` — fixed fraction of entry price
/// - `"tp_atr"` / `"sl_atr"` — multiples of ATR at the moment of entry
///
/// `"atr_period"` controls the internal ATR used by all ATR-based modes (default 14).
pub struct DynamicStrategy {
    indicators: HashMap<String, IndicatorState>,
    entry: ConditionGroup,
    exit: Option<ConditionGroup>,
    in_position: bool,
    entry_price: f64,
    /// Accumulated indicator series — drained by `take_indicator_series()`.
    /// Key = `"name.field"` (e.g. `"macd.histogram"`), value = `(timestamp_ms, value)`.
    indicator_series: HashMap<String, Vec<(i64, f64)>>,
    // fixed %
    tp_pct: Option<f64>,
    sl_pct: Option<f64>,
    // ATR-based (computed at entry)
    tp_atr_mult: Option<f64>,
    sl_atr_mult: Option<f64>,
    tp_atr_level: f64,
    sl_atr_level: f64,
    // internal ATR (always running when any ATR mode is active)
    atr: Option<Atr>,
    last_atr: f64,
    transform: CandleTransform,
}

/// Extract the indicator dependencies declared in a DynamicStrategy params block.
///
/// Walks `params.indicators` and returns one `IndicatorDep` per entry — skipping
/// any indicator configured with a non-empty `"tf"` field (MTF stays internal to
/// the strategy until the ledger supports resampling).
///
/// Called by `alm_strategy::factory::build_strategy_with_deps`.
pub fn dynamic_indicator_deps(params: &Value) -> Vec<crate::factory::IndicatorDep> {
    let Some(ind_map) = params.get("indicators").and_then(Value::as_object) else {
        return Vec::new();
    };
    let mut deps = Vec::new();
    for (_name, cfg) in ind_map {
        let tf = cfg.get("tf").and_then(Value::as_str).unwrap_or("");
        if !tf.is_empty() {
            continue;
        }
        let mut clean = cfg.clone();
        if let Some(obj) = clean.as_object_mut() {
            obj.remove("tf");
        }
        deps.push(crate::factory::IndicatorDep { config: clean, source_tf: None });
    }
    deps
}

impl DynamicStrategy {
    pub fn from_params(params: &Value) -> Result<Self> {
        // ── Candle type ───────────────────────────────────────────────────────
        let candle_type_str = params
            .get("candle_type")
            .and_then(Value::as_str)
            .unwrap_or("raw");
        let ha_smooth = params
            .get("ha_smooth")
            .and_then(Value::as_f64)
            .map(|v| v as usize)
            .unwrap_or(2);
        let transform = CandleTransform::new(CandleType::from_str(candle_type_str, ha_smooth));

        // ── Indicators ────────────────────────────────────────────────────────
        let ind_map = params
            .get("indicators")
            .and_then(Value::as_object)
            .ok_or_else(|| anyhow::anyhow!("DynamicStrategy params missing 'indicators' object"))?;

        let mut indicators = HashMap::new();
        for (name, cfg) in ind_map {
            let tf = cfg.get("tf").and_then(Value::as_str).unwrap_or("");
            let box_ = IndicatorBox::from_config(cfg)?;
            let state = IndicatorState::new(box_);
            let state = if tf.is_empty() { state } else { state.with_tf(tf) };
            indicators.insert(name.clone(), state);
        }

        // ── Entry group ───────────────────────────────────────────────────────
        let entry_val = params
            .get("entry")
            .ok_or_else(|| anyhow::anyhow!("DynamicStrategy params missing 'entry'"))?;
        let entry = ConditionGroup::from_value(entry_val)?;

        // ── Optional exit group ───────────────────────────────────────────────
        let exit = params
            .get("exit")
            .map(ConditionGroup::from_value)
            .transpose()?;

        // ── Optional TP / SL ─────────────────────────────────────────────────
        let tp_pct       = params.get("tp").and_then(Value::as_f64);
        let sl_pct       = params.get("sl").and_then(Value::as_f64);
        let tp_atr_mult  = params.get("tp_atr").and_then(Value::as_f64);
        let sl_atr_mult  = params.get("sl_atr").and_then(Value::as_f64);

        let needs_atr = tp_atr_mult.is_some() || sl_atr_mult.is_some();
        let atr = if needs_atr {
            let period = params.get("atr_period").and_then(Value::as_f64).map(|v| v as usize).unwrap_or(14);
            Some(Atr::new(period))
        } else {
            None
        };

        if indicators.is_empty() {
            bail!("DynamicStrategy requires at least one indicator");
        }

        Ok(Self {
            indicators,
            entry,
            exit,
            in_position: false,
            entry_price: 0.0,
            tp_pct,
            sl_pct,
            tp_atr_mult,
            sl_atr_mult,
            tp_atr_level: 0.0,
            sl_atr_level: 0.0,
            atr,
            last_atr: 0.0,
            transform,
            indicator_series: HashMap::new(),
        })
    }

    /// Build snapshot maps for condition evaluation.
    fn snapshots(
        &self,
        use_current: bool,
    ) -> HashMap<String, HashMap<String, f64>> {
        self.indicators
            .iter()
            .map(|(name, state)| {
                let fields = if use_current {
                    state.current.clone()
                } else {
                    state.previous.clone()
                };
                (name.clone(), fields)
            })
            .collect()
    }

    fn bar_fields(bar: &Bar) -> HashMap<String, f64> {
        let mut m = HashMap::new();
        m.insert("open".into(), bar.open);
        m.insert("high".into(), bar.high);
        m.insert("low".into(), bar.low);
        m.insert("close".into(), bar.close);
        m.insert("volume".into(), bar.volume);
        m
    }
}

impl Strategy for DynamicStrategy {
    fn on_bar(&mut self, bar: &Bar) -> Vec<Signal> {
        // Update internal ATR on every raw bar so it's warm when HA finishes warming up
        if let Some(atr) = &mut self.atr {
            if let Some(v) = atr.update(bar.high, bar.low, bar.close) {
                self.last_atr = v.atr;
            }
        }

        // Transform candle if needed (HA, etc.)
        let effective = match self.transform.apply(bar) {
            Some(b) => b,
            None => return vec![], // HA warmup period
        };

        // Advance all indicators
        for state in self.indicators.values_mut() {
            state.update(&effective);
        }

        // Check if any indicator is still warming up (empty current map)
        let ready = self
            .indicators
            .values()
            .all(|s| !s.current.is_empty());
        if !ready {
            return vec![];
        }

        // Collect indicator series (after warmup), aligned to effective bar timestamp
        for (name, state) in &self.indicators {
            for (field, &val) in &state.current {
                let key = format!("{name}.{field}");
                self.indicator_series
                    .entry(key)
                    .or_default()
                    .push((effective.timestamp, val));
            }
        }

        let current = self.snapshots(true);
        let previous = self.snapshots(false);
        // bar_fields dùng raw bar để price conditions reflect giá thực tế
        let bf = Self::bar_fields(bar);

        // Exit check first
        if self.in_position {
            let tp_hit = self.tp_pct
                .map(|pct| bar.close >= self.entry_price * (1.0 + pct))
                .unwrap_or(false)
                || (self.tp_atr_level > 0.0 && bar.close >= self.tp_atr_level);

            let sl_hit = self.sl_pct
                .map(|pct| bar.close <= self.entry_price * (1.0 - pct))
                .unwrap_or(false)
                || (self.sl_atr_level > 0.0 && bar.close <= self.sl_atr_level);

            let cond_hit = self.exit.as_ref()
                .map(|g| g.evaluate(&current, &previous, &bf))
                .unwrap_or(false);

            if tp_hit || sl_hit || cond_hit {
                self.in_position = false;
                self.entry_price = 0.0;
                self.tp_atr_level = 0.0;
                self.sl_atr_level = 0.0;
                return vec![Signal::close(bar.timestamp, bar.symbol.clone())];
            }
            return vec![];
        }

        // Entry check
        if self.entry.evaluate(&current, &previous, &bf) {
            self.in_position = true;
            self.entry_price = bar.close;
            // Compute ATR-based fixed levels at entry
            self.tp_atr_level = self.tp_atr_mult
                .filter(|_| self.last_atr > 0.0)
                .map(|m| bar.close + m * self.last_atr)
                .unwrap_or(0.0);
            self.sl_atr_level = self.sl_atr_mult
                .filter(|_| self.last_atr > 0.0)
                .map(|m| bar.close - m * self.last_atr)
                .unwrap_or(0.0);
            return vec![Signal::long(bar.timestamp, bar.symbol.clone(), 1.0)];
        }

        vec![]
    }

    fn name(&self) -> &str {
        "dynamic"
    }

    fn reset(&mut self) {
        for state in self.indicators.values_mut() {
            state.reset();
        }
        if let Some(atr) = &mut self.atr { atr.reset(); }
        self.transform.reset();
        self.in_position = false;
        self.entry_price = 0.0;
        self.tp_atr_level = 0.0;
        self.sl_atr_level = 0.0;
        self.last_atr = 0.0;
    }

    fn take_indicator_series(&mut self) -> std::collections::HashMap<String, Vec<(i64, f64)>> {
        std::mem::take(&mut self.indicator_series)
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use alm_core::signal::Direction;
    use serde_json::json;

    /// Bar với high/low đối xứng quanh close — ATR ≈ close * 0.01 * sqrt(period-ish)
    fn bar(ts: i64, close: f64) -> Bar {
        Bar::new(ts, "T", close * 1.005, close * 1.01, close * 0.99, close, 1000.0)
    }

    fn run(s: &mut DynamicStrategy, bars: &[Bar]) -> Vec<(i64, Direction)> {
        bars.iter()
            .flat_map(|b| s.on_bar(b))
            .map(|s| (s.timestamp, s.direction))
            .collect()
    }

    /// Tạo strategy RSI đơn giản: entry khi rsi < oversold
    fn rsi_strat(extra: serde_json::Value) -> DynamicStrategy {
        let mut params = json!({
            "indicators": { "rsi": { "type": "rsi", "period": 14 } },
            "entry": { "logic": "and", "rules": [
                { "source": "rsi", "field": "value", "op": "lt", "value": 30.0 }
            ]}
        });
        // merge extra fields vào params
        if let (Some(obj), Some(extra_obj)) = (params.as_object_mut(), extra.as_object()) {
            for (k, v) in extra_obj { obj.insert(k.clone(), v.clone()); }
        }
        DynamicStrategy::from_params(&params).unwrap()
    }

    /// Bars: giảm mạnh (→ RSI oversold → entry) rồi tăng
    fn entry_bars() -> Vec<Bar> {
        // 20 bars giảm để RSI oversold
        let mut bars: Vec<Bar> = (0..20).map(|i| bar(i, 100.0 - i as f64 * 3.0)).collect();
        // 40 bars tăng
        bars.extend((20..60).map(|i| bar(i, 40.0 + (i - 20) as f64 * 2.5)));
        bars
    }

    // ── fixed % TP ───────────────────────────────────────────────────────────

    #[test]
    fn tp_pct_closes_position() {
        let mut s = rsi_strat(json!({ "tp": 0.10 })); // TP +10%
        let sigs = run(&mut s, &entry_bars());

        // phải có entry rồi sau đó có close
        let dirs: Vec<_> = sigs.iter().map(|(_, d)| d).collect();
        assert!(dirs.contains(&&Direction::Long), "no entry signal");
        assert!(dirs.contains(&&Direction::Close), "tp did not fire");

        // close phải đến SAU entry
        let entry_ts = sigs.iter().find(|(_, d)| *d == Direction::Long).unwrap().0;
        let close_ts = sigs.iter().find(|(_, d)| *d == Direction::Close).unwrap().0;
        assert!(close_ts > entry_ts, "close before entry");
    }

    // ── fixed % SL ───────────────────────────────────────────────────────────

    #[test]
    fn sl_pct_closes_position() {
        // Bars: giảm → RSI oversold → entry, rồi tiếp tục giảm → SL hit
        let mut bars: Vec<Bar> = (0..20).map(|i| bar(i, 100.0 - i as f64 * 3.0)).collect();
        bars.extend((20..50).map(|i| bar(i, 42.0 - (i - 20) as f64 * 0.5)));

        let mut s = rsi_strat(json!({ "sl": 0.05 })); // SL -5%
        let sigs = run(&mut s, &bars);

        let dirs: Vec<_> = sigs.iter().map(|(_, d)| d).collect();
        assert!(dirs.contains(&&Direction::Long),  "no entry");
        assert!(dirs.contains(&&Direction::Close), "sl did not fire");
    }

    // ── ATR-based TP ─────────────────────────────────────────────────────────

    #[test]
    fn tp_atr_closes_position() {
        // ATR warm-up cần 14 bars → dùng nhiều bars hơn
        let mut bars: Vec<Bar> = (0..25).map(|i| bar(i, 100.0 - i as f64 * 2.0)).collect();
        bars.extend((25..80).map(|i| bar(i, 52.0 + (i - 25) as f64 * 1.5)));

        let mut s = rsi_strat(json!({ "tp_atr": 1.5, "atr_period": 14 }));
        let sigs = run(&mut s, &bars);

        let dirs: Vec<_> = sigs.iter().map(|(_, d)| d).collect();
        assert!(dirs.contains(&&Direction::Long),  "no entry");
        assert!(dirs.contains(&&Direction::Close), "tp_atr did not fire");
    }

    // ── ATR-based SL ─────────────────────────────────────────────────────────

    #[test]
    fn sl_atr_closes_position() {
        let mut bars: Vec<Bar> = (0..25).map(|i| bar(i, 100.0 - i as f64 * 2.0)).collect();
        // sau entry tiếp tục giảm để hit SL
        bars.extend((25..60).map(|i| bar(i, 52.0 - (i - 25) as f64 * 0.8)));

        let mut s = rsi_strat(json!({ "sl_atr": 0.5, "atr_period": 14 }));
        let sigs = run(&mut s, &bars);

        let dirs: Vec<_> = sigs.iter().map(|(_, d)| d).collect();
        assert!(dirs.contains(&&Direction::Long),  "no entry");
        assert!(dirs.contains(&&Direction::Close), "sl_atr did not fire");
    }

    // ── reset ────────────────────────────────────────────────────────────────

    #[test]
    fn reset_clears_all_state() {
        let bars = entry_bars();
        let mut s = rsi_strat(json!({ "tp": 0.10, "sl": 0.05 }));

        let r1 = run(&mut s, &bars);
        s.reset();
        let r2 = run(&mut s, &bars);

        assert_eq!(r1, r2, "reset parity failed");
    }

    // ── nested condition groups ───────────────────────────────────────────────

    #[test]
    fn nested_groups_or_of_ands() {
        // entry: (rsi < 25) OR (rsi < 35 AND close < 60)
        // bars: giảm xuống RSI < 35, giá < 60 → group 2 fires
        let params = json!({
            "indicators": {
                "rsi":  { "type": "rsi", "period": 14 },
                "ema50": { "type": "ema", "period": 50 }
            },
            "entry": {
                "logic": "or",
                "groups": [
                    {
                        "logic": "and",
                        "rules": [
                            { "source": "rsi", "field": "value", "op": "lt", "value": 25.0 }
                        ]
                    },
                    {
                        "logic": "and",
                        "rules": [
                            { "source": "rsi",  "field": "value", "op": "lt", "value": 35.0 },
                            { "source": "close",              "op": "lt", "value": 60.0  }
                        ]
                    }
                ]
            }
        });
        let mut s = DynamicStrategy::from_params(&params).unwrap();
        let bars: Vec<Bar> = (0..60).map(|i| bar(i, 100.0 - i as f64 * 1.5)).collect();
        let sigs: Vec<_> = bars.iter().flat_map(|b| s.on_bar(b)).collect();
        assert!(sigs.iter().any(|s| s.direction == Direction::Long), "nested OR-of-ANDs failed to entry");
    }

    // ── no exit condition — only TP/SL ───────────────────────────────────────

    #[test]
    fn tp_works_without_exit_condition() {
        // strategy chỉ có entry + tp, không có exit group
        let params = json!({
            "indicators": { "rsi": { "type": "rsi", "period": 14 } },
            "entry": { "logic": "and", "rules": [
                { "source": "rsi", "field": "value", "op": "lt", "value": 30.0 }
            ]},
            "tp": 0.08
        });
        let mut s = DynamicStrategy::from_params(&params).unwrap();
        let sigs = run(&mut s, &entry_bars());

        assert!(sigs.iter().any(|(_, d)| *d == Direction::Long),  "no entry");
        assert!(sigs.iter().any(|(_, d)| *d == Direction::Close), "tp did not fire without exit group");
    }
}

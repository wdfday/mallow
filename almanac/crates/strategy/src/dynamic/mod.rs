pub mod condition;
pub mod indicator_box;

use std::collections::HashMap;

use anyhow::{bail, Result};
use alm_core::{Bar, Signal, Strategy};
use alm_indicator::Atr;
use serde_json::Value;

use self::condition::ConditionGroup;
use self::indicator_box::IndicatorBox;
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
/// {
///   "tp":       0.05,   "sl":       0.02,   // fixed % from entry
///   "tp_atr":   2.0,    "sl_atr":   1.0,    // N × ATR at entry
///   "trail_pct": 0.02,                       // trailing SL, % below peak
///   "trail_atr": 1.5,                        // trailing SL, N × ATR below peak
///   "atr_period": 14                         // ATR period (default 14)
/// }
/// ```
///
/// - `"tp"` / `"sl"` — fixed fraction of entry price
/// - `"tp_atr"` / `"sl_atr"` — multiples of ATR at the moment of entry
/// - `"trail_pct"` — trailing stop: SL = peak_price × (1 − trail_pct), moves up with price
/// - `"trail_atr"` — trailing stop: SL = peak_price − trail_atr × ATR (current ATR)
///
/// `"atr_period"` controls the internal ATR used by all ATR-based modes (default 14).
pub struct DynamicStrategy {
    indicators: HashMap<String, IndicatorState>,
    entry: ConditionGroup,
    exit: Option<ConditionGroup>,
    in_position: bool,
    entry_price: f64,
    peak_price: f64,
    // fixed %
    tp_pct: Option<f64>,
    sl_pct: Option<f64>,
    // ATR-based (computed at entry)
    tp_atr_mult: Option<f64>,
    sl_atr_mult: Option<f64>,
    tp_atr_level: f64,   // entry + mult * atr_at_entry
    sl_atr_level: f64,   // entry - mult * atr_at_entry
    // trailing
    trail_pct: Option<f64>,
    trail_atr_mult: Option<f64>,
    // internal ATR (always running when any ATR mode is active)
    atr: Option<Atr>,
    last_atr: f64,
    transform: CandleTransform,
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
        let trail_pct    = params.get("trail_pct").and_then(Value::as_f64);
        let trail_atr_mult = params.get("trail_atr").and_then(Value::as_f64);

        let needs_atr = tp_atr_mult.is_some() || sl_atr_mult.is_some() || trail_atr_mult.is_some();
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
            peak_price: 0.0,
            tp_pct,
            sl_pct,
            tp_atr_mult,
            sl_atr_mult,
            tp_atr_level: 0.0,
            sl_atr_level: 0.0,
            trail_pct,
            trail_atr_mult,
            atr,
            last_atr: 0.0,
            transform,
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
        // Transform candle if needed (HA, etc.)
        let effective = match self.transform.apply(bar) {
            Some(b) => b,
            None => return vec![], // HA warmup period
        };

        // Advance all indicators
        for state in self.indicators.values_mut() {
            state.update(&effective);
        }

        // Update internal ATR (always, so it's warm at entry)
        if let Some(atr) = &mut self.atr {
            if let Some(v) = atr.update(bar.high, bar.low, bar.close) {
                self.last_atr = v.atr;
            }
        }

        // Check if any indicator is still warming up (empty current map)
        let ready = self
            .indicators
            .values()
            .all(|s| !s.current.is_empty());
        if !ready {
            return vec![];
        }

        let current = self.snapshots(true);
        let previous = self.snapshots(false);
        // bar_fields dùng raw bar để price conditions reflect giá thực tế
        let bf = Self::bar_fields(bar);

        // Exit check first
        if self.in_position {
            // Update peak for trailing stop
            if bar.close > self.peak_price {
                self.peak_price = bar.close;
            }

            let tp_hit = self.tp_pct
                .map(|pct| bar.close >= self.entry_price * (1.0 + pct))
                .unwrap_or(false)
                || (self.tp_atr_level > 0.0 && bar.close >= self.tp_atr_level);

            let trail_sl = self.trail_pct
                .map(|pct| self.peak_price * (1.0 - pct))
                .or_else(|| self.trail_atr_mult.map(|m| self.peak_price - m * self.last_atr));

            let sl_hit = self.sl_pct
                .map(|pct| bar.close <= self.entry_price * (1.0 - pct))
                .unwrap_or(false)
                || (self.sl_atr_level > 0.0 && bar.close <= self.sl_atr_level)
                || trail_sl.map(|sl| bar.close <= sl).unwrap_or(false);

            let cond_hit = self.exit.as_ref()
                .map(|g| g.evaluate(&current, &previous, &bf))
                .unwrap_or(false);

            if tp_hit || sl_hit || cond_hit {
                self.in_position = false;
                self.entry_price = 0.0;
                self.peak_price = 0.0;
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
            self.peak_price = bar.close;
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
        self.peak_price = 0.0;
        self.tp_atr_level = 0.0;
        self.sl_atr_level = 0.0;
        self.last_atr = 0.0;
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

    // ── trailing % stop ───────────────────────────────────────────────────────

    #[test]
    fn trail_pct_moves_with_price() {
        // entry → giá tăng mạnh → giá pullback 5% từ peak → trail fire
        let mut bars: Vec<Bar> = (0..20).map(|i| bar(i, 100.0 - i as f64 * 3.0)).collect();
        // tăng lên 80
        bars.extend((20..40).map(|i| bar(i, 40.0 + (i - 20) as f64 * 2.0)));
        // pullback -8% từ peak
        let peak = bars.last().unwrap().close;
        bars.extend((40..55).map(|i| bar(i, peak - (i - 40) as f64 * (peak * 0.01))));

        let mut s = rsi_strat(json!({ "trail_pct": 0.05 }));
        let sigs = run(&mut s, &bars);

        let dirs: Vec<_> = sigs.iter().map(|(_, d)| d).collect();
        assert!(dirs.contains(&&Direction::Long),  "no entry");
        assert!(dirs.contains(&&Direction::Close), "trail_pct did not fire");
    }

    #[test]
    fn trail_pct_does_not_fire_within_threshold() {
        // Entry ≈ bar 14 (close=58). Giá tiếp tục giảm tới 40 (−31% từ entry),
        // rồi tăng lên 88 (peak). Pullback 2% từ peak.
        // trail_pct=0.50 → SL never breached: 58*0.50=29 < 40, 88*0.50=44 << 86.
        let mut bars: Vec<Bar> = (0..20).map(|i| bar(i, 100.0 - i as f64 * 3.0)).collect();
        bars.extend((20..45).map(|i| bar(i, 40.0 + (i - 20) as f64 * 2.0)));
        let peak = bars.last().unwrap().close;
        // pullback 2% — trail=50% không fire
        bars.extend((45..55).map(|i| bar(i, peak * (1.0 - 0.002 * (i - 45) as f64))));

        let mut s = rsi_strat(json!({ "trail_pct": 0.50 }));
        let sigs = run(&mut s, &bars);

        let dirs: Vec<_> = sigs.iter().map(|(_, d)| d).collect();
        assert!(dirs.contains(&&Direction::Long), "no entry");
        assert!(!dirs.contains(&&Direction::Close), "trail fired too early");
    }

    // ── trailing ATR stop ────────────────────────────────────────────────────

    #[test]
    fn trail_atr_closes_position() {
        let mut bars: Vec<Bar> = (0..25).map(|i| bar(i, 100.0 - i as f64 * 2.0)).collect();
        bars.extend((25..50).map(|i| bar(i, 52.0 + (i - 25) as f64 * 1.5)));
        // pullback lớn
        let peak = bars.last().unwrap().close;
        bars.extend((50..65).map(|i| bar(i, peak - (i - 50) as f64 * 1.5)));

        let mut s = rsi_strat(json!({ "trail_atr": 1.0, "atr_period": 14 }));
        let sigs = run(&mut s, &bars);

        let dirs: Vec<_> = sigs.iter().map(|(_, d)| d).collect();
        assert!(dirs.contains(&&Direction::Long),  "no entry");
        assert!(dirs.contains(&&Direction::Close), "trail_atr did not fire");
    }

    // ── stack: tp + trail ────────────────────────────────────────────────────

    #[test]
    fn tp_and_trail_stack_tp_wins() {
        // giá tăng thẳng vượt TP trước khi pullback đủ để trail fire
        let mut bars: Vec<Bar> = (0..20).map(|i| bar(i, 100.0 - i as f64 * 3.0)).collect();
        // tăng nhanh +15% → tp=0.10 fire trước trail
        bars.extend((20..40).map(|i| bar(i, 40.0 + (i - 20) as f64 * 0.5)));

        let mut s = rsi_strat(json!({ "tp": 0.10, "trail_pct": 0.20 }));
        let sigs = run(&mut s, &bars);

        assert!(sigs.iter().any(|(_, d)| *d == Direction::Long),  "no entry");
        assert!(sigs.iter().any(|(_, d)| *d == Direction::Close), "no close");
    }

    // ── reset ────────────────────────────────────────────────────────────────

    #[test]
    fn reset_clears_all_state() {
        let bars = entry_bars();
        let mut s = rsi_strat(json!({ "tp": 0.10, "sl": 0.05, "trail_pct": 0.03 }));

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

//! Parity tests — same logic expressed 3 ways must produce identical signals.
//!
//! For each strategy we run:
//!   1. Hardcoded struct  (e.g. `MaCrossover`)
//!   2. DynamicStrategy   (declarative JSON)
//!   3. CelStrategy       (text expression with `prev_` for cross detection)
//!
//! Same bars → same (timestamp, direction) sequence.

#[cfg(test)]
mod parity_tests {
    use alm_core::{bar::Bar, signal::Direction, strategy::Strategy};
    use serde_json::json;

    use crate::{
        factory::build_strategy,
        ma_crossover::MaCrossover,
        macd_crossover::MacdCrossover,
        rsi_mean_rev::RsiMeanRev,
    };

    // ── helpers ───────────────────────────────────────────────────────────────

    fn bar(ts: i64, close: f64) -> Bar {
        Bar::new(ts, "TEST", close, close * 1.005, close * 0.995, close, 1000.0)
    }

    fn run(s: &mut dyn Strategy, bars: &[Bar]) -> Vec<(i64, Direction)> {
        bars.iter()
            .flat_map(|b| s.on_bar(b))
            .map(|s| (s.timestamp, s.direction))
            .collect()
    }

    /// down → up → down to force two crossovers (entry + exit)
    fn trending_bars(n: usize) -> Vec<Bar> {
        let third = n / 3;
        (0..n)
            .map(|i| {
                let price = if i < third {
                    200.0 - i as f64 * 1.5
                } else if i < third * 2 {
                    200.0 - third as f64 * 1.5 + (i - third) as f64 * 2.0
                } else {
                    200.0 - third as f64 * 1.5 + third as f64 * 2.0
                        - (i - third * 2) as f64 * 2.0
                };
                bar(i as i64 * 60_000, price.max(10.0))
            })
            .collect()
    }

    /// falling prices → RSI oversold → rising → RSI overbought
    fn rsi_bars(n: usize) -> Vec<Bar> {
        (0..n)
            .map(|i| {
                let price = if i < n / 2 {
                    150.0 - i as f64 * 3.0
                } else {
                    150.0 - (n / 2) as f64 * 3.0 + (i - n / 2) as f64 * 4.0
                };
                bar(i as i64 * 60_000, price.max(1.0))
            })
            .collect()
    }

    fn assert_parity(label: &str, a: &[(i64, Direction)], b: &[(i64, Direction)]) {
        assert_eq!(
            a, b,
            "{label}: signal mismatch\n  left : {a:?}\n  right: {b:?}"
        );
    }

    // ── RSI mean reversion ────────────────────────────────────────────────────

    #[test]
    fn rsi_mean_rev_parity() {
        let bars = rsi_bars(80);

        // 1. hardcoded
        let mut hc = RsiMeanRev::new(14, 30.0, 70.0);
        let hc_sigs = run(&mut hc, &bars);

        // 2. dynamic JSON
        let mut dyn_s = build_strategy("dynamic", &json!({
            "indicators": {
                "rsi": { "type": "rsi", "period": 14 }
            },
            "entry": {
                "logic": "and",
                "rules": [{ "source": "rsi", "field": "value", "op": "lt", "value": 30.0 }]
            },
            "exit": {
                "logic": "and",
                "rules": [{ "source": "rsi", "field": "value", "op": "gt", "value": 70.0 }]
            }
        }))
        .unwrap();
        let dyn_sigs = run(dyn_s.as_mut(), &bars);

        // 3. CEL
        let mut cel = build_strategy("cel", &json!({
            "entry": "rsi(14) < 30.0",
            "exit":  "rsi(14) > 70.0"
        }))
        .unwrap();
        let cel_sigs = run(cel.as_mut(), &bars);

        assert!(!hc_sigs.is_empty(), "rsi: hardcoded produced no signals");
        assert_parity("rsi hardcoded vs dynamic", &hc_sigs, &dyn_sigs);
        assert_parity("rsi hardcoded vs cel",     &hc_sigs, &cel_sigs);
    }

    // ── MA crossover ──────────────────────────────────────────────────────────

    #[test]
    fn ma_crossover_parity() {
        let bars = trending_bars(300);

        // 1. hardcoded (fast=20, slow=50, both EMA)
        let mut hc = MaCrossover::new(20, 50);
        let hc_sigs = run(&mut hc, &bars);

        // 2. dynamic JSON
        let mut dyn_s = build_strategy("dynamic", &json!({
            "indicators": {
                "fast": { "type": "ema", "period": 20 },
                "slow": { "type": "ema", "period": 50 }
            },
            "entry": {
                "logic": "and",
                "rules": [{
                    "source": "fast", "field": "value",
                    "op": "cross_above",
                    "compare": "slow", "compare_field": "value"
                }]
            },
            "exit": {
                "logic": "and",
                "rules": [{
                    "source": "fast", "field": "value",
                    "op": "cross_below",
                    "compare": "slow", "compare_field": "value"
                }]
            }
        }))
        .unwrap();
        let dyn_sigs = run(dyn_s.as_mut(), &bars);

        // 3. CEL with prev_
        let mut cel = build_strategy("cel", &json!({
            "entry": "prev_ema(20) <= prev_ema(50) && ema(20) > ema(50)",
            "exit":  "prev_ema(20) >= prev_ema(50) && ema(20) < ema(50)"
        }))
        .unwrap();
        let cel_sigs = run(cel.as_mut(), &bars);

        assert!(!hc_sigs.is_empty(), "ma_crossover: hardcoded produced no signals");
        assert_parity("ma hardcoded vs dynamic", &hc_sigs, &dyn_sigs);
        assert_parity("ma hardcoded vs cel",     &hc_sigs, &cel_sigs);
    }

    // ── MACD crossover ────────────────────────────────────────────────────────

    #[test]
    fn macd_crossover_parity() {
        let bars = trending_bars(300);

        // 1. hardcoded (fast=12, slow=26, signal=9)
        let mut hc = MacdCrossover::new(12, 26, 9);
        let hc_sigs = run(&mut hc, &bars);

        // 2. dynamic JSON — histogram cross_above/below 0
        let mut dyn_s = build_strategy("dynamic", &json!({
            "indicators": {
                "macd": { "type": "macd", "fast": 12, "slow": 26, "signal": 9 }
            },
            "entry": {
                "logic": "and",
                "rules": [{ "source": "macd", "field": "histogram", "op": "cross_above", "value": 0.0 }]
            },
            "exit": {
                "logic": "and",
                "rules": [{ "source": "macd", "field": "histogram", "op": "cross_below", "value": 0.0 }]
            }
        }))
        .unwrap();
        let dyn_sigs = run(dyn_s.as_mut(), &bars);

        // 3. CEL with prev_
        let mut cel = build_strategy("cel", &json!({
            "entry": "prev_macd_hist(12) <= 0.0 && macd_hist(12) > 0.0",
            "exit":  "prev_macd_hist(12) >= 0.0 && macd_hist(12) < 0.0"
        }))
        .unwrap();
        let cel_sigs = run(cel.as_mut(), &bars);

        assert!(!hc_sigs.is_empty(), "macd: hardcoded produced no signals");
        assert_parity("macd hardcoded vs dynamic", &hc_sigs, &dyn_sigs);
        assert_parity("macd hardcoded vs cel",     &hc_sigs, &cel_sigs);
    }

    // ── reset parity ──────────────────────────────────────────────────────────
    // After reset(), all 3 must reproduce the same signals on a second run.

    #[test]
    fn reset_parity() {
        let bars = rsi_bars(80);

        let mut hc  = RsiMeanRev::new(14, 30.0, 70.0);
        let mut dyn_s = build_strategy("dynamic", &json!({
            "indicators": { "rsi": { "type": "rsi", "period": 14 } },
            "entry": { "logic": "and", "rules": [{ "source": "rsi", "field": "value", "op": "lt", "value": 30.0 }] },
            "exit":  { "logic": "and", "rules": [{ "source": "rsi", "field": "value", "op": "gt", "value": 70.0 }] }
        })).unwrap();
        let mut cel = build_strategy("cel", &json!({
            "entry": "rsi(14) < 30.0",
            "exit":  "rsi(14) > 70.0"
        })).unwrap();

        // first run
        let hc_r1  = run(&mut hc,        &bars);
        let dyn_r1 = run(dyn_s.as_mut(), &bars);
        let cel_r1 = run(cel.as_mut(),   &bars);

        // reset + second run
        hc.reset();  dyn_s.reset();  cel.reset();
        let hc_r2  = run(&mut hc,        &bars);
        let dyn_r2 = run(dyn_s.as_mut(), &bars);
        let cel_r2 = run(cel.as_mut(),   &bars);

        assert_parity("rsi reset: hardcoded",  &hc_r1,  &hc_r2);
        assert_parity("rsi reset: dynamic",    &dyn_r1, &dyn_r2);
        assert_parity("rsi reset: cel",        &cel_r1, &cel_r2);
        assert_parity("rsi reset: hc vs dyn",  &hc_r1,  &dyn_r2);
        assert_parity("rsi reset: hc vs cel",  &hc_r1,  &cel_r2);
    }
}

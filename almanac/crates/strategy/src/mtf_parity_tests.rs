//! Parity tests for multi-timeframe (MTF), Heiken Ashi candle transforms,
//! TP/SL exits, and Layered strategy composition.
//!
//! Coverage added here (complementing the per-strategy named/*.rs tests):
//!
//! | Group | Tests |
//! |-------|-------|
//! | **MTF CEL ↔ Dynamic** | H1 RSI threshold, H1 EMA cross, H1 filter + M1 signal, M5 + H1 hierarchy |
//! | **MTF semantics** | confirmed fires only at H1 boundary, live vs confirmed differ, reset parity |
//! | **Heiken Ashi** | HaColor named ↔ CEL (HA candle type), CEL ↔ Dynamic HA EMA cross |
//! | **TP / SL** | fixed-% TP, fixed-% SL, TP fires before RSI exit, ATR-based TP/SL |
//! | **Layered (real strategies)** | CEL filter gates CEL signal, reset parity |

#![cfg(test)]

use alm_core::{Bar, Strategy};
use alm_core::signal::Direction;
use serde_json::json;

use crate::factory::build_strategy;
use crate::test_utils::{bar, run, assert_parity, trending_bars};

// ─────────────────────────────────────────────────────────────────────────────
// Shared bar generators
// ─────────────────────────────────────────────────────────────────────────────

/// `n` hours of M1 bars at 60 s intervals, plus one extra bar to trigger the
/// final H1 close — total = `n * 60 + 1` bars.
///
/// Price shape: smooth downtrend from 200→100 in the first half, then uptrend
/// back to 200 in the second half.  Each H1 bar therefore has a clear trend
/// direction, which drives RSI and EMA into oversold/overbought territory.
fn m1_hours(n: usize) -> Vec<Bar> {
    let total = n * 60 + 1;
    let half  = n as f64 / 2.0;
    (0..total)
        .map(|i| {
            let t = i as f64 / 60.0;
            let price = if t < half {
                200.0 - (t / half) * 100.0
            } else {
                100.0 + ((t - half) / half) * 100.0
            };
            bar(i as i64 * 60_000, price.max(1.0))
        })
        .collect()
}

/// M1 bars with a brief dip-within-uptrend pattern, suitable for testing
/// combined H1-filter + M1-signal strategies.
///
/// Structure (hours):
/// - 0-11  : slow rise 100→160  (H1 RSI warms up above 50)
/// - 12-14 : 3-hour dip 160→100 (M1 RSI briefly oversold; H1 RSI stays > 50
///            for the first bar of the dip before dropping)
/// - 15-19 : recovery 100→160
/// - 20    : trigger bar (closes the final H1 bucket)
fn m1_dip_in_uptrend() -> Vec<Bar> {
    let mut ts = 0i64;
    let mut bars = vec![];
    // Slow rise
    for i in 0..720u32 {
        bars.push(bar(ts, 100.0 + i as f64 * (60.0 / 720.0)));
        ts += 60_000;
    }
    // Sharp dip within M1 bars of hours 12-14 (180 bars)
    for i in 0..180u32 {
        bars.push(bar(ts, (160.0 - i as f64 * (60.0 / 180.0)).max(1.0)));
        ts += 60_000;
    }
    // Recovery
    for i in 0..300u32 {
        bars.push(bar(ts, 100.0 + i as f64 * (60.0 / 300.0)));
        ts += 60_000;
    }
    // Trigger bar for final H1 close
    bars.push(bar(ts, 160.0));
    bars
}

// ─────────────────────────────────────────────────────────────────────────────
// MTF parity: CEL ↔ Dynamic
// ─────────────────────────────────────────────────────────────────────────────

// [deprecated: dynamic] /// H1 RSI level threshold — simplest MTF case: no cross detection needed.
// [deprecated: dynamic] /// Both frameworks must fire at the exact same M1 bar timestamps.
// [deprecated: dynamic] 
// [deprecated: dynamic] /// H1 RSI level threshold — simplest MTF case: no cross detection needed.
// [deprecated: dynamic] /// Both frameworks must fire at the exact same M1 bar timestamps.
// [deprecated: dynamic] #[test]
// [deprecated: dynamic] fn mtf_h1_rsi_threshold_parity() {
// [deprecated: dynamic]     let bars = m1_hours(30);
// [deprecated: dynamic] 
// [deprecated: dynamic]     let mut cel = build_strategy("cel", &json!({
// [deprecated: dynamic]         "entry": "H1.rsi(5) < 35.0",
// [deprecated: dynamic]         "exit":  "H1.rsi(5) > 65.0"
// [deprecated: dynamic]     })).unwrap();
// [deprecated: dynamic]     let cel_sigs = run(cel.as_mut(), &bars);
// [deprecated: dynamic] 
// [deprecated: dynamic]     let mut dyn_s = build_strategy("dynamic", &json!({
// [deprecated: dynamic]         "indicators": { "h1_rsi": { "type": "rsi", "period": 5, "tf": "H1" } },
// [deprecated: dynamic]         "entry": { "logic": "and", "rules": [
// [deprecated: dynamic]             { "source": "h1_rsi", "field": "value", "op": "lt", "value": 35 }
// [deprecated: dynamic]         ]},
// [deprecated: dynamic]         "exit": { "logic": "and", "rules": [
// [deprecated: dynamic]             { "source": "h1_rsi", "field": "value", "op": "gt", "value": 65 }
// [deprecated: dynamic]         ]}
// [deprecated: dynamic]     })).unwrap();
// [deprecated: dynamic]     let dyn_sigs = run(dyn_s.as_mut(), &bars);
// [deprecated: dynamic] 
// [deprecated: dynamic]     assert!(!cel_sigs.is_empty(), "mtf_h1_rsi_threshold: expected signals");
// [deprecated: dynamic]     assert_parity("mtf H1 RSI threshold: cel vs dynamic", &cel_sigs, &dyn_sigs);
// [deprecated: dynamic] }

// [deprecated: dynamic] /// H1 EMA(3) cross H1 EMA(8).
// [deprecated: dynamic] /// CEL uses `prev_` / current cross pattern; Dynamic uses `cross_above` / `cross_below`.
// [deprecated: dynamic] /// Both should fire at the same M1 bar timestamps (the first bar of each new H1 bucket
// [deprecated: dynamic] /// in which a cross is detected).
// [deprecated: dynamic] 
// [deprecated: dynamic] /// H1 EMA(3) cross H1 EMA(8).
// [deprecated: dynamic] /// CEL uses `prev_` / current cross pattern; Dynamic uses `cross_above` / `cross_below`.
// [deprecated: dynamic] /// Both should fire at the same M1 bar timestamps (the first bar of each new H1 bucket
// [deprecated: dynamic] /// in which a cross is detected).
// [deprecated: dynamic] #[test]
// [deprecated: dynamic] fn mtf_h1_ema_cross_parity() {
// [deprecated: dynamic]     let bars = m1_hours(40);
// [deprecated: dynamic] 
// [deprecated: dynamic]     let mut cel = build_strategy("cel", &json!({
// [deprecated: dynamic]         "entry": "prev_H1.ema(3) <= prev_H1.ema(8) && H1.ema(3) > H1.ema(8)",
// [deprecated: dynamic]         "exit":  "prev_H1.ema(3) >= prev_H1.ema(8) && H1.ema(3) < H1.ema(8)"
// [deprecated: dynamic]     })).unwrap();
// [deprecated: dynamic]     let cel_sigs = run(cel.as_mut(), &bars);
// [deprecated: dynamic] 
// [deprecated: dynamic]     let mut dyn_s = build_strategy("dynamic", &json!({
// [deprecated: dynamic]         "indicators": {
// [deprecated: dynamic]             "fast": { "type": "ema", "period": 3, "tf": "H1" },
// [deprecated: dynamic]             "slow": { "type": "ema", "period": 8, "tf": "H1" }
// [deprecated: dynamic]         },
// [deprecated: dynamic]         "entry": { "logic": "and", "rules": [
// [deprecated: dynamic]             { "source": "fast", "field": "value", "op": "cross_above", "compare": "slow" }
// [deprecated: dynamic]         ]},
// [deprecated: dynamic]         "exit": { "logic": "and", "rules": [
// [deprecated: dynamic]             { "source": "fast", "field": "value", "op": "cross_below", "compare": "slow" }
// [deprecated: dynamic]         ]}
// [deprecated: dynamic]     })).unwrap();
// [deprecated: dynamic]     let dyn_sigs = run(dyn_s.as_mut(), &bars);
// [deprecated: dynamic] 
// [deprecated: dynamic]     assert!(!cel_sigs.is_empty(), "mtf_h1_ema_cross: expected signals");
// [deprecated: dynamic]     assert_parity("mtf H1 EMA cross: cel vs dynamic", &cel_sigs, &dyn_sigs);
// [deprecated: dynamic] }

// [deprecated: dynamic] /// H1 RSI trend filter combined with M1 RSI mean-reversion signal.
// [deprecated: dynamic] /// Entry: H1 RSI(5) > 50 (uptrend) AND M1 RSI(5) < 35 (oversold dip).
// [deprecated: dynamic] /// Exit: H1 RSI(5) < 50 OR M1 RSI(5) > 65.
// [deprecated: dynamic] 
// [deprecated: dynamic] /// H1 RSI trend filter combined with M1 RSI mean-reversion signal.
// [deprecated: dynamic] /// Entry: H1 RSI(5) > 50 (uptrend) AND M1 RSI(5) < 35 (oversold dip).
// [deprecated: dynamic] /// Exit: H1 RSI(5) < 50 OR M1 RSI(5) > 65.
// [deprecated: dynamic] #[test]
// [deprecated: dynamic] fn mtf_h1_filter_m1_signal_parity() {
// [deprecated: dynamic]     let bars = m1_dip_in_uptrend();
// [deprecated: dynamic] 
// [deprecated: dynamic]     let mut cel = build_strategy("cel", &json!({
// [deprecated: dynamic]         "entry": "H1.rsi(5) > 50.0 && rsi(5) < 35.0",
// [deprecated: dynamic]         "exit":  "H1.rsi(5) < 50.0 || rsi(5) > 65.0"
// [deprecated: dynamic]     })).unwrap();
// [deprecated: dynamic]     let cel_sigs = run(cel.as_mut(), &bars);
// [deprecated: dynamic] 
// [deprecated: dynamic]     let mut dyn_s = build_strategy("dynamic", &json!({
// [deprecated: dynamic]         "indicators": {
// [deprecated: dynamic]             "h1_rsi": { "type": "rsi", "period": 5, "tf": "H1" },
// [deprecated: dynamic]             "m1_rsi": { "type": "rsi", "period": 5 }
// [deprecated: dynamic]         },
// [deprecated: dynamic]         "entry": { "logic": "and", "rules": [
// [deprecated: dynamic]             { "source": "h1_rsi", "field": "value", "op": "gt", "value": 50 },
// [deprecated: dynamic]             { "source": "m1_rsi", "field": "value", "op": "lt", "value": 35 }
// [deprecated: dynamic]         ]},
// [deprecated: dynamic]         "exit": { "logic": "or", "rules": [
// [deprecated: dynamic]             { "source": "h1_rsi", "field": "value", "op": "lt", "value": 50 },
// [deprecated: dynamic]             { "source": "m1_rsi", "field": "value", "op": "gt", "value": 65 }
// [deprecated: dynamic]         ]}
// [deprecated: dynamic]     })).unwrap();
// [deprecated: dynamic]     let dyn_sigs = run(dyn_s.as_mut(), &bars);
// [deprecated: dynamic] 
// [deprecated: dynamic]     assert_parity("mtf H1 filter + M1 signal: cel vs dynamic", &cel_sigs, &dyn_sigs);
// [deprecated: dynamic] }

// [deprecated: dynamic] /// Three-timeframe hierarchy: base M1, M5 for fast signal, H1 for trend filter.
// [deprecated: dynamic] /// Both CEL and Dynamic handle M5 via `TimeBarResampler(300_000 ms)`.
// [deprecated: dynamic] 
// [deprecated: dynamic] /// Three-timeframe hierarchy: base M1, M5 for fast signal, H1 for trend filter.
// [deprecated: dynamic] /// Both CEL and Dynamic handle M5 via `TimeBarResampler(300_000 ms)`.
// [deprecated: dynamic] #[test]
// [deprecated: dynamic] fn mtf_m5_h1_hierarchy_parity() {
// [deprecated: dynamic]     let bars = m1_hours(40);
// [deprecated: dynamic] 
// [deprecated: dynamic]     let mut cel = build_strategy("cel", &json!({
// [deprecated: dynamic]         "entry": "H1.rsi(14) > 50.0 && M5.rsi(5) < 35.0",
// [deprecated: dynamic]         "exit":  "H1.rsi(14) < 50.0 || M5.rsi(5) > 65.0"
// [deprecated: dynamic]     })).unwrap();
// [deprecated: dynamic]     let cel_sigs = run(cel.as_mut(), &bars);
// [deprecated: dynamic] 
// [deprecated: dynamic]     let mut dyn_s = build_strategy("dynamic", &json!({
// [deprecated: dynamic]         "indicators": {
// [deprecated: dynamic]             "h1_rsi": { "type": "rsi", "period": 14, "tf": "H1" },
// [deprecated: dynamic]             "m5_rsi": { "type": "rsi", "period": 5,  "tf": "M5" }
// [deprecated: dynamic]         },
// [deprecated: dynamic]         "entry": { "logic": "and", "rules": [
// [deprecated: dynamic]             { "source": "h1_rsi", "field": "value", "op": "gt", "value": 50 },
// [deprecated: dynamic]             { "source": "m5_rsi", "field": "value", "op": "lt", "value": 35 }
// [deprecated: dynamic]         ]},
// [deprecated: dynamic]         "exit": { "logic": "or", "rules": [
// [deprecated: dynamic]             { "source": "h1_rsi", "field": "value", "op": "lt", "value": 50 },
// [deprecated: dynamic]             { "source": "m5_rsi", "field": "value", "op": "gt", "value": 65 }
// [deprecated: dynamic]         ]}
// [deprecated: dynamic]     })).unwrap();
// [deprecated: dynamic]     let dyn_sigs = run(dyn_s.as_mut(), &bars);
// [deprecated: dynamic] 
// [deprecated: dynamic]     assert_parity("mtf M5+H1 hierarchy: cel vs dynamic", &cel_sigs, &dyn_sigs);
// [deprecated: dynamic] }

// [deprecated: dynamic] /// H1 EMA(5) cross H1 EMA(20) — longer periods, more warmup bars needed.
// [deprecated: dynamic] /// Tests that both frameworks agree across a wider EMA span.
// [deprecated: dynamic] 
// [deprecated: dynamic] /// H1 EMA(5) cross H1 EMA(20) — longer periods, more warmup bars needed.
// [deprecated: dynamic] /// Tests that both frameworks agree across a wider EMA span.
// [deprecated: dynamic] #[test]
// [deprecated: dynamic] fn mtf_h1_ema_long_cross_parity() {
// [deprecated: dynamic]     let bars = m1_hours(60);
// [deprecated: dynamic] 
// [deprecated: dynamic]     let mut cel = build_strategy("cel", &json!({
// [deprecated: dynamic]         "entry": "prev_H1.ema(5) <= prev_H1.ema(20) && H1.ema(5) > H1.ema(20)",
// [deprecated: dynamic]         "exit":  "prev_H1.ema(5) >= prev_H1.ema(20) && H1.ema(5) < H1.ema(20)"
// [deprecated: dynamic]     })).unwrap();
// [deprecated: dynamic]     let cel_sigs = run(cel.as_mut(), &bars);
// [deprecated: dynamic] 
// [deprecated: dynamic]     let mut dyn_s = build_strategy("dynamic", &json!({
// [deprecated: dynamic]         "indicators": {
// [deprecated: dynamic]             "fast": { "type": "ema", "period": 5,  "tf": "H1" },
// [deprecated: dynamic]             "slow": { "type": "ema", "period": 20, "tf": "H1" }
// [deprecated: dynamic]         },
// [deprecated: dynamic]         "entry": { "logic": "and", "rules": [
// [deprecated: dynamic]             { "source": "fast", "field": "value", "op": "cross_above", "compare": "slow" }
// [deprecated: dynamic]         ]},
// [deprecated: dynamic]         "exit": { "logic": "and", "rules": [
// [deprecated: dynamic]             { "source": "fast", "field": "value", "op": "cross_below", "compare": "slow" }
// [deprecated: dynamic]         ]}
// [deprecated: dynamic]     })).unwrap();
// [deprecated: dynamic]     let dyn_sigs = run(dyn_s.as_mut(), &bars);
// [deprecated: dynamic] 
// [deprecated: dynamic]     assert_parity("mtf H1 EMA(5/20) cross: cel vs dynamic", &cel_sigs, &dyn_sigs);
// [deprecated: dynamic] }

// [deprecated: dynamic] /// M15 RSI(7) signal filtered by H1 EMA(10) direction.
// [deprecated: dynamic] /// Exercises the full M1 → M15 → H1 resampling chain in both frameworks.
// [deprecated: dynamic] 
// [deprecated: dynamic] /// M15 RSI(7) signal filtered by H1 EMA(10) direction.
// [deprecated: dynamic] /// Exercises the full M1 → M15 → H1 resampling chain in both frameworks.
// [deprecated: dynamic] #[test]
// [deprecated: dynamic] fn mtf_m15_h1_parity() {
// [deprecated: dynamic]     let bars = m1_hours(50);
// [deprecated: dynamic] 
// [deprecated: dynamic]     let mut cel = build_strategy("cel", &json!({
// [deprecated: dynamic]         "entry": "H1.ema(10) > H1.ema(20) && M15.rsi(7) < 35.0",
// [deprecated: dynamic]         "exit":  "H1.ema(10) < H1.ema(20) || M15.rsi(7) > 65.0"
// [deprecated: dynamic]     })).unwrap();
// [deprecated: dynamic]     let cel_sigs = run(cel.as_mut(), &bars);
// [deprecated: dynamic] 
// [deprecated: dynamic]     let mut dyn_s = build_strategy("dynamic", &json!({
// [deprecated: dynamic]         "indicators": {
// [deprecated: dynamic]             "h1_fast": { "type": "ema", "period": 10, "tf": "H1" },
// [deprecated: dynamic]             "h1_slow": { "type": "ema", "period": 20, "tf": "H1" },
// [deprecated: dynamic]             "m15_rsi": { "type": "rsi", "period": 7,  "tf": "M15" }
// [deprecated: dynamic]         },
// [deprecated: dynamic]         "entry": { "logic": "and", "rules": [
// [deprecated: dynamic]             { "source": "h1_fast", "field": "value", "op": "gt", "compare": "h1_slow" },
// [deprecated: dynamic]             { "source": "m15_rsi", "field": "value", "op": "lt", "value": 35 }
// [deprecated: dynamic]         ]},
// [deprecated: dynamic]         "exit": { "logic": "or", "rules": [
// [deprecated: dynamic]             { "source": "h1_fast", "field": "value", "op": "lt", "compare": "h1_slow" },
// [deprecated: dynamic]             { "source": "m15_rsi", "field": "value", "op": "gt", "value": 65 }
// [deprecated: dynamic]         ]}
// [deprecated: dynamic]     })).unwrap();
// [deprecated: dynamic]     let dyn_sigs = run(dyn_s.as_mut(), &bars);
// [deprecated: dynamic] 
// [deprecated: dynamic]     assert_parity("mtf M15+H1: cel vs dynamic", &cel_sigs, &dyn_sigs);
// [deprecated: dynamic] }

// ─────────────────────────────────────────────────────────────────────────────
// MTF semantics: confirmed vs live, reset
// ─────────────────────────────────────────────────────────────────────────────

/// **Confirmed** H1 signals must only fire at timestamps that are exact multiples
/// of the H1 interval (3 600 000 ms).  These are the first M1 bars of each new
/// H1 bucket — the only moments the `TimeBarResampler` emits a closed H1 bar.
#[test]
fn mtf_confirmed_signals_at_h1_boundary() {
    const H1_MS: i64 = 3_600_000;
    let bars = m1_hours(30);

    let mut cel = build_strategy("cel", &json!({
        "entry": "H1.rsi(5) < 35.0",
        "exit":  "H1.rsi(5) > 65.0"
    })).unwrap();
    let sigs = run(cel.as_mut(), &bars);

    assert!(!sigs.is_empty(), "mtf_confirmed_boundary: expected at least one signal");
    for (ts, _) in &sigs {
        assert_eq!(
            ts % H1_MS, 0,
            "confirmed H1 signal at ts={ts} ms should be at an H1 bucket boundary \
             (multiple of {H1_MS} ms); got remainder {}",
            ts % H1_MS
        );
    }
}

// [deprecated: dynamic] /// Same check for Dynamic: all H1-indicator signals fire at H1 boundaries.
// [deprecated: dynamic] 
// [deprecated: dynamic] /// Same check for Dynamic: all H1-indicator signals fire at H1 boundaries.
// [deprecated: dynamic] #[test]
// [deprecated: dynamic] fn mtf_confirmed_dynamic_at_h1_boundary() {
// [deprecated: dynamic]     const H1_MS: i64 = 3_600_000;
// [deprecated: dynamic]     let bars = m1_hours(30);
// [deprecated: dynamic] 
// [deprecated: dynamic]     let mut dyn_s = build_strategy("dynamic", &json!({
// [deprecated: dynamic]         "indicators": { "h1_rsi": { "type": "rsi", "period": 5, "tf": "H1" } },
// [deprecated: dynamic]         "entry": { "logic": "and", "rules": [
// [deprecated: dynamic]             { "source": "h1_rsi", "field": "value", "op": "lt", "value": 35 }
// [deprecated: dynamic]         ]},
// [deprecated: dynamic]         "exit": { "logic": "and", "rules": [
// [deprecated: dynamic]             { "source": "h1_rsi", "field": "value", "op": "gt", "value": 65 }
// [deprecated: dynamic]         ]}
// [deprecated: dynamic]     })).unwrap();
// [deprecated: dynamic]     let sigs = run(dyn_s.as_mut(), &bars);
// [deprecated: dynamic] 
// [deprecated: dynamic]     assert!(!sigs.is_empty(), "mtf_confirmed_dynamic_boundary: expected signals");
// [deprecated: dynamic]     for (ts, _) in &sigs {
// [deprecated: dynamic]         assert_eq!(
// [deprecated: dynamic]             ts % H1_MS, 0,
// [deprecated: dynamic]             "dynamic confirmed H1 signal at ts={ts} should be at H1 boundary"
// [deprecated: dynamic]         );
// [deprecated: dynamic]     }
// [deprecated: dynamic] }

/// `live_H1.rsi(5)` updates every M1 bar (uses forming HTF bar), so its signals
/// CAN fire at non-H1-boundary timestamps.  Confirmed `H1.rsi(5)` signals must
/// only fire at H1 boundaries.  The two strategies should therefore produce
/// different signal timestamps when the threshold is crossed intra-H1-bucket.
#[test]
fn mtf_live_vs_confirmed_differ() {
    const H1_MS: i64 = 3_600_000;
    // Use a more volatile pattern so the RSI threshold is hit intra-bucket.
    let bars = m1_hours(30);

    let mut confirmed = build_strategy("cel", &json!({
        "entry": "H1.rsi(5) < 35.0",
        "exit":  "H1.rsi(5) > 65.0"
    })).unwrap();
    let confirmed_sigs = run(confirmed.as_mut(), &bars);

    let mut live = build_strategy("cel", &json!({
        "entry": "live_H1.rsi(5) < 35.0",
        "exit":  "live_H1.rsi(5) > 65.0"
    })).unwrap();
    let live_sigs = run(live.as_mut(), &bars);

    // Confirmed: every signal must be at an H1 boundary.
    for (ts, _) in &confirmed_sigs {
        assert_eq!(ts % H1_MS, 0, "confirmed signal not at H1 boundary: ts={ts}");
    }
    // Live: signals differ from confirmed (live can fire intra-bucket).
    // They won't always differ (if the threshold is crossed right at the boundary)
    // but the confirmed subset must be at H1 boundaries while live is unconstrained.
    // The two sets should not be strictly identical across 30 hours of trend data.
    assert_ne!(
        confirmed_sigs, live_sigs,
        "live_ and confirmed H1 strategies should produce different signal timestamps \
         when the RSI threshold is crossed intra-bucket"
    );
}

/// CEL MTF strategy: `reset()` must reproduce the exact same signals when
/// fed the same bars a second time.
#[test]
fn mtf_reset_parity_cel() {
    let bars = m1_hours(30);

    let mut cel = build_strategy("cel", &json!({
        "entry": "H1.rsi(5) < 35.0",
        "exit":  "H1.rsi(5) > 65.0"
    })).unwrap();

    let r1 = run(cel.as_mut(), &bars);
    cel.reset();
    let r2 = run(cel.as_mut(), &bars);

    assert!(!r1.is_empty(), "mtf_reset_cel: expected signals");
    assert_parity("mtf CEL reset: run1 vs run2", &r1, &r2);
}

// [deprecated: dynamic] /// Dynamic MTF strategy: same reset guarantee.
// [deprecated: dynamic] 
// [deprecated: dynamic] /// Dynamic MTF strategy: same reset guarantee.
// [deprecated: dynamic] #[test]
// [deprecated: dynamic] fn mtf_reset_parity_dynamic() {
// [deprecated: dynamic]     let bars = m1_hours(30);
// [deprecated: dynamic] 
// [deprecated: dynamic]     let mut dyn_s = build_strategy("dynamic", &json!({
// [deprecated: dynamic]         "indicators": {
// [deprecated: dynamic]             "fast": { "type": "ema", "period": 3, "tf": "H1" },
// [deprecated: dynamic]             "slow": { "type": "ema", "period": 8, "tf": "H1" }
// [deprecated: dynamic]         },
// [deprecated: dynamic]         "entry": { "logic": "and", "rules": [
// [deprecated: dynamic]             { "source": "fast", "field": "value", "op": "cross_above", "compare": "slow" }
// [deprecated: dynamic]         ]},
// [deprecated: dynamic]         "exit": { "logic": "and", "rules": [
// [deprecated: dynamic]             { "source": "fast", "field": "value", "op": "cross_below", "compare": "slow" }
// [deprecated: dynamic]         ]}
// [deprecated: dynamic]     })).unwrap();
// [deprecated: dynamic] 
// [deprecated: dynamic]     let r1 = run(dyn_s.as_mut(), &bars);
// [deprecated: dynamic]     dyn_s.reset();
// [deprecated: dynamic]     let r2 = run(dyn_s.as_mut(), &bars);
// [deprecated: dynamic] 
// [deprecated: dynamic]     assert!(!r1.is_empty(), "mtf_reset_dynamic: expected signals");
// [deprecated: dynamic]     assert_parity("mtf Dynamic reset: run1 vs run2", &r1, &r2);
// [deprecated: dynamic] }

/// Mixed MTF + base-TF reset: both `H1.ema` and `rsi` (base) state must be
/// cleared by `reset()`.
#[test]
fn mtf_mixed_tf_reset_parity() {
    let bars = m1_dip_in_uptrend();

    let mut cel = build_strategy("cel", &json!({
        "entry": "H1.rsi(5) > 50.0 && rsi(5) < 35.0",
        "exit":  "H1.rsi(5) < 50.0 || rsi(5) > 65.0"
    })).unwrap();

    let r1 = run(cel.as_mut(), &bars);
    cel.reset();
    let r2 = run(cel.as_mut(), &bars);

    assert_parity("mtf mixed TF reset: run1 vs run2", &r1, &r2);
}

// ─────────────────────────────────────────────────────────────────────────────
// Heiken Ashi: named ↔ CEL, CEL ↔ Dynamic
// ─────────────────────────────────────────────────────────────────────────────

/// HaColor (named) vs CEL with `candle_type: heiken_ashi`.
///
/// HaColor logic:
///   entry = HA candle flips bullish  (close >= open after close < open)
///   exit  = HA candle flips bearish  (close < open  after close >= open)
///
/// In CEL with HA candle type `close` and `open` are the HA values, so
/// `close >= open` tests bullishness.  `close[1]` and `open[1]` give the
/// previous HA bar, enabling flip detection with history indexing.
#[test]
fn ha_color_named_vs_cel_parity() {
    use crate::named::heiken_ashi_strategy::HaColor;

    let bars = trending_bars(300);

    let mut named = HaColor::new(1);
    let named_sigs = run(&mut named, &bars);

    let mut cel = build_strategy("cel", &json!({
        "candle_type": "heiken_ashi",
        "entry": "close >= open && close[1] < open[1]",
        "exit":  "close < open && close[1] >= open[1]"
    })).unwrap();
    let cel_sigs = run(cel.as_mut(), &bars);

    assert!(!named_sigs.is_empty(), "ha_color: expected signals from named strategy");
    assert_parity("HaColor named vs CEL", &named_sigs, &cel_sigs);
}

/// HaColor reset: named strategy and CEL HA both reproduce the same signals
/// after a `reset()` call.
#[test]
fn ha_color_reset_parity() {
    use crate::named::heiken_ashi_strategy::HaColor;

    let bars = trending_bars(300);

    let mut named = HaColor::new(1);
    let r1 = run(&mut named, &bars);
    named.reset();
    let r2 = run(&mut named, &bars);
    assert_parity("HaColor named reset", &r1, &r2);

    let mut cel = build_strategy("cel", &json!({
        "candle_type": "heiken_ashi",
        "entry": "close >= open && close[1] < open[1]",
        "exit":  "close < open && close[1] >= open[1]"
    })).unwrap();
    let c1 = run(cel.as_mut(), &bars);
    cel.reset();
    let c2 = run(cel.as_mut(), &bars);
    assert_parity("HaColor CEL reset", &c1, &c2);
}

// [deprecated: dynamic] /// CEL vs Dynamic: EMA cross on Heiken Ashi bars.
// [deprecated: dynamic] ///
// [deprecated: dynamic] /// Both compute EMA(3) and EMA(8) on HA-transformed bars; both detect a cross.
// [deprecated: dynamic] /// Signals must be identical because both transforms feed the same indicator
// [deprecated: dynamic] /// code via the same `IndicatorBox`.
// [deprecated: dynamic] ///
// [deprecated: dynamic] /// Note: the `close` price field behaves differently between CEL (HA close) and
// [deprecated: dynamic] /// Dynamic (raw close), so this test deliberately avoids any `close` comparison
// [deprecated: dynamic] /// and relies only on pure EMA cross logic.
// [deprecated: dynamic] 
// [deprecated: dynamic] /// CEL vs Dynamic: EMA cross on Heiken Ashi bars.
// [deprecated: dynamic] ///
// [deprecated: dynamic] /// Both compute EMA(3) and EMA(8) on HA-transformed bars; both detect a cross.
// [deprecated: dynamic] /// Signals must be identical because both transforms feed the same indicator
// [deprecated: dynamic] /// code via the same `IndicatorBox`.
// [deprecated: dynamic] ///
// [deprecated: dynamic] /// Note: the `close` price field behaves differently between CEL (HA close) and
// [deprecated: dynamic] /// Dynamic (raw close), so this test deliberately avoids any `close` comparison
// [deprecated: dynamic] /// and relies only on pure EMA cross logic.
// [deprecated: dynamic] #[test]
// [deprecated: dynamic] fn ha_ema_cross_cel_vs_dynamic_parity() {
// [deprecated: dynamic]     let bars = trending_bars(300);
// [deprecated: dynamic] 
// [deprecated: dynamic]     let mut cel = build_strategy("cel", &json!({
// [deprecated: dynamic]         "candle_type": "heiken_ashi",
// [deprecated: dynamic]         "entry": "prev_ema(3) <= prev_ema(8) && ema(3) > ema(8)",
// [deprecated: dynamic]         "exit":  "prev_ema(3) >= prev_ema(8) && ema(3) < ema(8)"
// [deprecated: dynamic]     })).unwrap();
// [deprecated: dynamic]     let cel_sigs = run(cel.as_mut(), &bars);
// [deprecated: dynamic] 
// [deprecated: dynamic]     let mut dyn_s = build_strategy("dynamic", &json!({
// [deprecated: dynamic]         "candle_type": "heiken_ashi",
// [deprecated: dynamic]         "indicators": {
// [deprecated: dynamic]             "fast": { "type": "ema", "period": 3 },
// [deprecated: dynamic]             "slow": { "type": "ema", "period": 8 }
// [deprecated: dynamic]         },
// [deprecated: dynamic]         "entry": { "logic": "and", "rules": [
// [deprecated: dynamic]             { "source": "fast", "field": "value", "op": "cross_above", "compare": "slow" }
// [deprecated: dynamic]         ]},
// [deprecated: dynamic]         "exit": { "logic": "and", "rules": [
// [deprecated: dynamic]             { "source": "fast", "field": "value", "op": "cross_below", "compare": "slow" }
// [deprecated: dynamic]         ]}
// [deprecated: dynamic]     })).unwrap();
// [deprecated: dynamic]     let dyn_sigs = run(dyn_s.as_mut(), &bars);
// [deprecated: dynamic] 
// [deprecated: dynamic]     assert!(!cel_sigs.is_empty(), "ha_ema_cross: expected signals");
// [deprecated: dynamic]     assert_parity("HA EMA cross: cel vs dynamic", &cel_sigs, &dyn_sigs);
// [deprecated: dynamic] }

// [deprecated: dynamic] /// Smooth Heiken Ashi (EMA-smoothed before HA) — CEL vs Dynamic.
// [deprecated: dynamic] /// Uses `smooth_ha` / `smooth_heiken_ashi` with period 3.
// [deprecated: dynamic] 
// [deprecated: dynamic] /// Smooth Heiken Ashi (EMA-smoothed before HA) — CEL vs Dynamic.
// [deprecated: dynamic] /// Uses `smooth_ha` / `smooth_heiken_ashi` with period 3.
// [deprecated: dynamic] #[test]
// [deprecated: dynamic] fn smooth_ha_ema_cross_cel_vs_dynamic_parity() {
// [deprecated: dynamic]     let bars = trending_bars(300);
// [deprecated: dynamic] 
// [deprecated: dynamic]     let mut cel = build_strategy("cel", &json!({
// [deprecated: dynamic]         "candle_type": "smooth_ha",
// [deprecated: dynamic]         "ha_smooth": 3,
// [deprecated: dynamic]         "entry": "prev_ema(3) <= prev_ema(8) && ema(3) > ema(8)",
// [deprecated: dynamic]         "exit":  "prev_ema(3) >= prev_ema(8) && ema(3) < ema(8)"
// [deprecated: dynamic]     })).unwrap();
// [deprecated: dynamic]     let cel_sigs = run(cel.as_mut(), &bars);
// [deprecated: dynamic] 
// [deprecated: dynamic]     let mut dyn_s = build_strategy("dynamic", &json!({
// [deprecated: dynamic]         "candle_type": "smooth_ha",
// [deprecated: dynamic]         "ha_smooth": 3,
// [deprecated: dynamic]         "indicators": {
// [deprecated: dynamic]             "fast": { "type": "ema", "period": 3 },
// [deprecated: dynamic]             "slow": { "type": "ema", "period": 8 }
// [deprecated: dynamic]         },
// [deprecated: dynamic]         "entry": { "logic": "and", "rules": [
// [deprecated: dynamic]             { "source": "fast", "field": "value", "op": "cross_above", "compare": "slow" }
// [deprecated: dynamic]         ]},
// [deprecated: dynamic]         "exit": { "logic": "and", "rules": [
// [deprecated: dynamic]             { "source": "fast", "field": "value", "op": "cross_below", "compare": "slow" }
// [deprecated: dynamic]         ]}
// [deprecated: dynamic]     })).unwrap();
// [deprecated: dynamic]     let dyn_sigs = run(dyn_s.as_mut(), &bars);
// [deprecated: dynamic] 
// [deprecated: dynamic]     assert_parity("smooth HA EMA cross: cel vs dynamic", &cel_sigs, &dyn_sigs);
// [deprecated: dynamic] }

// ─────────────────────────────────────────────────────────────────────────────
// TP / SL exits (Dynamic)
// ─────────────────────────────────────────────────────────────────────────────

// [deprecated: dynamic] /// Fixed-% TP: after entering long, the strategy must emit a Close when
// [deprecated: dynamic] /// `bar.close >= entry_price * (1 + tp)`.
// [deprecated: dynamic] ///
// [deprecated: dynamic] /// The RSI exit condition is set to an impossible threshold (> 100) so that
// [deprecated: dynamic] /// TP is the *only* possible exit — this makes the assertion unambiguous.
// [deprecated: dynamic] 
// [deprecated: dynamic] /// Fixed-% TP: after entering long, the strategy must emit a Close when
// [deprecated: dynamic] /// `bar.close >= entry_price * (1 + tp)`.
// [deprecated: dynamic] ///
// [deprecated: dynamic] /// The RSI exit condition is set to an impossible threshold (> 100) so that
// [deprecated: dynamic] /// TP is the *only* possible exit — this makes the assertion unambiguous.
// [deprecated: dynamic] #[test]
// [deprecated: dynamic] fn tp_fixed_pct_fires() {
// [deprecated: dynamic]     // Prices fall to oversold region then recover.  Entry fires during the fall.
// [deprecated: dynamic]     // TP = 10% above entry.  The recovery raises prices well past the TP level.
// [deprecated: dynamic]     let mut ts = 0i64;
// [deprecated: dynamic]     let mut bars = vec![];
// [deprecated: dynamic]     for i in 0..30u32 { bars.push(bar(ts, 100.0 - i as f64 * 2.2)); ts += 60_000; }
// [deprecated: dynamic]     for _ in 0..5     { bars.push(bar(ts, 34.0)); ts += 60_000; }
// [deprecated: dynamic]     for i in 0..60u32 { bars.push(bar(ts, 34.0 + i as f64 * 1.2)); ts += 60_000; }
// [deprecated: dynamic] 
// [deprecated: dynamic]     // RSI exit threshold = 101 (impossible) — only TP can trigger.
// [deprecated: dynamic]     let mut dyn_s = build_strategy("dynamic", &json!({
// [deprecated: dynamic]         "indicators": { "rsi": { "type": "rsi", "period": 7 } },
// [deprecated: dynamic]         "entry": { "logic": "and", "rules": [
// [deprecated: dynamic]             { "source": "rsi", "field": "value", "op": "lt", "value": 30 }
// [deprecated: dynamic]         ]},
// [deprecated: dynamic]         "exit": { "logic": "and", "rules": [
// [deprecated: dynamic]             { "source": "rsi", "field": "value", "op": "gt", "value": 101 }
// [deprecated: dynamic]         ]},
// [deprecated: dynamic]         "tp": 0.10
// [deprecated: dynamic]     })).unwrap();
// [deprecated: dynamic]     let sigs = run(dyn_s.as_mut(), &bars);
// [deprecated: dynamic] 
// [deprecated: dynamic]     let entry = sigs.iter().find(|(_, d)| *d == Direction::Long);
// [deprecated: dynamic]     let close = sigs.iter().find(|(_, d)| *d == Direction::Close);
// [deprecated: dynamic]     assert!(entry.is_some(), "tp_fixed_pct: expected Long entry");
// [deprecated: dynamic]     assert!(close.is_some(), "tp_fixed_pct: expected Close (TP) exit");
// [deprecated: dynamic] 
// [deprecated: dynamic]     let entry_ts = entry.unwrap().0;
// [deprecated: dynamic]     let close_ts = close.unwrap().0;
// [deprecated: dynamic]     let entry_price = bars.iter().find(|b| b.timestamp == entry_ts).unwrap().close;
// [deprecated: dynamic]     let exit_price  = bars.iter().find(|b| b.timestamp == close_ts).unwrap().close;
// [deprecated: dynamic] 
// [deprecated: dynamic]     assert!(
// [deprecated: dynamic]         exit_price >= entry_price * 1.099,  // 10% TP minus float tolerance
// [deprecated: dynamic]         "TP exit price {exit_price:.4} should be >= {:.4} (10% above entry {entry_price:.4})",
// [deprecated: dynamic]         entry_price * 1.099
// [deprecated: dynamic]     );
// [deprecated: dynamic] }

// [deprecated: dynamic] /// Fixed-% SL: after entering long, the strategy must emit a Close when
// [deprecated: dynamic] /// `bar.close <= entry_price * (1 - sl)`.
// [deprecated: dynamic] 
// [deprecated: dynamic] /// Fixed-% SL: after entering long, the strategy must emit a Close when
// [deprecated: dynamic] /// `bar.close <= entry_price * (1 - sl)`.
// [deprecated: dynamic] #[test]
// [deprecated: dynamic] fn sl_fixed_pct_fires() {
// [deprecated: dynamic]     // Entry fires at oversold (price ~34), then the bar keeps falling — SL at -4%.
// [deprecated: dynamic]     let mut ts = 0i64;
// [deprecated: dynamic]     let mut bars = vec![];
// [deprecated: dynamic]     for i in 0..30u32 { bars.push(bar(ts, 100.0 - i as f64 * 2.2)); ts += 60_000; }
// [deprecated: dynamic]     for _ in 0..5     { bars.push(bar(ts, 34.0)); ts += 60_000; }
// [deprecated: dynamic]     for i in 0..20u32 { bars.push(bar(ts, (34.0 - i as f64 * 1.0).max(1.0))); ts += 60_000; }
// [deprecated: dynamic] 
// [deprecated: dynamic]     let mut dyn_s = build_strategy("dynamic", &json!({
// [deprecated: dynamic]         "indicators": { "rsi": { "type": "rsi", "period": 7 } },
// [deprecated: dynamic]         "entry": { "logic": "and", "rules": [
// [deprecated: dynamic]             { "source": "rsi", "field": "value", "op": "lt", "value": 30 }
// [deprecated: dynamic]         ]},
// [deprecated: dynamic]         "exit": { "logic": "and", "rules": [
// [deprecated: dynamic]             { "source": "rsi", "field": "value", "op": "gt", "value": 90 }
// [deprecated: dynamic]         ]},
// [deprecated: dynamic]         "sl": 0.04
// [deprecated: dynamic]     })).unwrap();
// [deprecated: dynamic]     let sigs = run(dyn_s.as_mut(), &bars);
// [deprecated: dynamic] 
// [deprecated: dynamic]     let entry = sigs.iter().find(|(_, d)| *d == Direction::Long);
// [deprecated: dynamic]     let close = sigs.iter().find(|(_, d)| *d == Direction::Close);
// [deprecated: dynamic]     assert!(entry.is_some(), "sl_fixed_pct: expected Long entry");
// [deprecated: dynamic]     assert!(close.is_some(), "sl_fixed_pct: expected Close (SL) exit");
// [deprecated: dynamic] 
// [deprecated: dynamic]     let entry_ts = entry.unwrap().0;
// [deprecated: dynamic]     let close_ts = close.unwrap().0;
// [deprecated: dynamic]     let entry_price = bars.iter().find(|b| b.timestamp == entry_ts).unwrap().close;
// [deprecated: dynamic]     let exit_price  = bars.iter().find(|b| b.timestamp == close_ts).unwrap().close;
// [deprecated: dynamic] 
// [deprecated: dynamic]     assert!(
// [deprecated: dynamic]         exit_price <= entry_price * 0.961,  // 4% SL plus float tolerance
// [deprecated: dynamic]         "SL exit price {exit_price:.4} should be <= {:.4} (4% below entry {entry_price:.4})",
// [deprecated: dynamic]         entry_price * 0.961
// [deprecated: dynamic]     );
// [deprecated: dynamic] }

// [deprecated: dynamic] /// With both TP and SL set, the one that fires first wins.
// [deprecated: dynamic] /// In a rising-price scenario the TP fires before the SL.
// [deprecated: dynamic] 
// [deprecated: dynamic] /// With both TP and SL set, the one that fires first wins.
// [deprecated: dynamic] /// In a rising-price scenario the TP fires before the SL.
// [deprecated: dynamic] #[test]
// [deprecated: dynamic] fn tp_wins_over_sl_when_price_rises() {
// [deprecated: dynamic]     let mut ts = 0i64;
// [deprecated: dynamic]     let mut bars = vec![];
// [deprecated: dynamic]     for i in 0..30u32 { bars.push(bar(ts, 100.0 - i as f64 * 2.2)); ts += 60_000; }
// [deprecated: dynamic]     for _ in 0..5     { bars.push(bar(ts, 34.0)); ts += 60_000; }
// [deprecated: dynamic]     for i in 0..40u32 { bars.push(bar(ts, 34.0 + i as f64 * 1.8)); ts += 60_000; }
// [deprecated: dynamic] 
// [deprecated: dynamic]     // TP = 6%, SL = 20% (far away — should never fire on a rising bar)
// [deprecated: dynamic]     let mut with_tp_sl = build_strategy("dynamic", &json!({
// [deprecated: dynamic]         "indicators": { "rsi": { "type": "rsi", "period": 7 } },
// [deprecated: dynamic]         "entry": { "logic": "and", "rules": [
// [deprecated: dynamic]             { "source": "rsi", "field": "value", "op": "lt", "value": 30 }
// [deprecated: dynamic]         ]},
// [deprecated: dynamic]         "exit": { "logic": "and", "rules": [
// [deprecated: dynamic]             { "source": "rsi", "field": "value", "op": "gt", "value": 95 }
// [deprecated: dynamic]         ]},
// [deprecated: dynamic]         "tp": 0.06,
// [deprecated: dynamic]         "sl": 0.20
// [deprecated: dynamic]     })).unwrap();
// [deprecated: dynamic]     // Without TP: exit only via RSI > 95 (happens later)
// [deprecated: dynamic]     let mut no_tp = build_strategy("dynamic", &json!({
// [deprecated: dynamic]         "indicators": { "rsi": { "type": "rsi", "period": 7 } },
// [deprecated: dynamic]         "entry": { "logic": "and", "rules": [
// [deprecated: dynamic]             { "source": "rsi", "field": "value", "op": "lt", "value": 30 }
// [deprecated: dynamic]         ]},
// [deprecated: dynamic]         "exit": { "logic": "and", "rules": [
// [deprecated: dynamic]             { "source": "rsi", "field": "value", "op": "gt", "value": 95 }
// [deprecated: dynamic]         ]}
// [deprecated: dynamic]     })).unwrap();
// [deprecated: dynamic] 
// [deprecated: dynamic]     let tp_sl_sigs = run(with_tp_sl.as_mut(), &bars);
// [deprecated: dynamic]     let no_tp_sigs = run(no_tp.as_mut(), &bars);
// [deprecated: dynamic] 
// [deprecated: dynamic]     let tp_exit_ts = tp_sl_sigs.iter().find(|(_, d)| *d == Direction::Close).map(|(t, _)| *t);
// [deprecated: dynamic]     let plain_exit_ts = no_tp_sigs.iter().find(|(_, d)| *d == Direction::Close).map(|(t, _)| *t);
// [deprecated: dynamic] 
// [deprecated: dynamic]     assert!(tp_exit_ts.is_some(), "tp_wins: expected TP/SL close");
// [deprecated: dynamic]     if let (Some(tp_ts), Some(plain_ts)) = (tp_exit_ts, plain_exit_ts) {
// [deprecated: dynamic]         assert!(
// [deprecated: dynamic]             tp_ts <= plain_ts,
// [deprecated: dynamic]             "TP exit at ts={tp_ts} should be earlier than RSI-only exit at ts={plain_ts}"
// [deprecated: dynamic]         );
// [deprecated: dynamic]     }
// [deprecated: dynamic] }

// [deprecated: dynamic] /// ATR-based TP: `tp_atr: 3.0` means TP = entry_price + 3 * ATR at entry.
// [deprecated: dynamic] /// After a sharp rise the strategy should exit via ATR-TP.
// [deprecated: dynamic] 
// [deprecated: dynamic] /// ATR-based TP: `tp_atr: 3.0` means TP = entry_price + 3 * ATR at entry.
// [deprecated: dynamic] /// After a sharp rise the strategy should exit via ATR-TP.
// [deprecated: dynamic] #[test]
// [deprecated: dynamic] fn tp_atr_based_fires() {
// [deprecated: dynamic]     let mut ts = 0i64;
// [deprecated: dynamic]     let mut bars = vec![];
// [deprecated: dynamic]     for i in 0..30u32 { bars.push(bar(ts, 100.0 - i as f64 * 2.0)); ts += 60_000; }
// [deprecated: dynamic]     for _ in 0..5     { bars.push(bar(ts, 40.0)); ts += 60_000; }
// [deprecated: dynamic]     for i in 0..50u32 { bars.push(bar(ts, 40.0 + i as f64 * 2.0)); ts += 60_000; }
// [deprecated: dynamic] 
// [deprecated: dynamic]     let mut dyn_s = build_strategy("dynamic", &json!({
// [deprecated: dynamic]         "indicators": { "rsi": { "type": "rsi", "period": 7 } },
// [deprecated: dynamic]         "entry": { "logic": "and", "rules": [
// [deprecated: dynamic]             { "source": "rsi", "field": "value", "op": "lt", "value": 30 }
// [deprecated: dynamic]         ]},
// [deprecated: dynamic]         "exit": { "logic": "and", "rules": [
// [deprecated: dynamic]             { "source": "rsi", "field": "value", "op": "gt", "value": 95 }
// [deprecated: dynamic]         ]},
// [deprecated: dynamic]         "tp_atr": 3.0,
// [deprecated: dynamic]         "atr_period": 7
// [deprecated: dynamic]     })).unwrap();
// [deprecated: dynamic]     let sigs = run(dyn_s.as_mut(), &bars);
// [deprecated: dynamic] 
// [deprecated: dynamic]     assert!(sigs.iter().any(|(_, d)| *d == Direction::Long),  "atr_tp: expected entry");
// [deprecated: dynamic]     assert!(sigs.iter().any(|(_, d)| *d == Direction::Close), "atr_tp: expected exit");
// [deprecated: dynamic] }

// [deprecated: dynamic] /// ATR-based SL: `sl_atr: 2.0` means SL = entry_price − 2 * ATR.
// [deprecated: dynamic] /// After entry the price drops, triggering ATR-SL before RSI-exit.
// [deprecated: dynamic] 
// [deprecated: dynamic] /// ATR-based SL: `sl_atr: 2.0` means SL = entry_price − 2 * ATR.
// [deprecated: dynamic] /// After entry the price drops, triggering ATR-SL before RSI-exit.
// [deprecated: dynamic] #[test]
// [deprecated: dynamic] fn sl_atr_based_fires() {
// [deprecated: dynamic]     let mut ts = 0i64;
// [deprecated: dynamic]     let mut bars = vec![];
// [deprecated: dynamic]     for i in 0..30u32 { bars.push(bar(ts, 100.0 - i as f64 * 2.0)); ts += 60_000; }
// [deprecated: dynamic]     for _ in 0..5     { bars.push(bar(ts, 40.0)); ts += 60_000; }
// [deprecated: dynamic]     for i in 0..20u32 { bars.push(bar(ts, (40.0 - i as f64 * 1.5).max(1.0))); ts += 60_000; }
// [deprecated: dynamic] 
// [deprecated: dynamic]     let mut dyn_s = build_strategy("dynamic", &json!({
// [deprecated: dynamic]         "indicators": { "rsi": { "type": "rsi", "period": 7 } },
// [deprecated: dynamic]         "entry": { "logic": "and", "rules": [
// [deprecated: dynamic]             { "source": "rsi", "field": "value", "op": "lt", "value": 30 }
// [deprecated: dynamic]         ]},
// [deprecated: dynamic]         "exit": { "logic": "and", "rules": [
// [deprecated: dynamic]             { "source": "rsi", "field": "value", "op": "gt", "value": 95 }
// [deprecated: dynamic]         ]},
// [deprecated: dynamic]         "sl_atr": 2.0,
// [deprecated: dynamic]         "atr_period": 7
// [deprecated: dynamic]     })).unwrap();
// [deprecated: dynamic]     let sigs = run(dyn_s.as_mut(), &bars);
// [deprecated: dynamic] 
// [deprecated: dynamic]     assert!(sigs.iter().any(|(_, d)| *d == Direction::Long),  "atr_sl: expected entry");
// [deprecated: dynamic]     assert!(sigs.iter().any(|(_, d)| *d == Direction::Close), "atr_sl: expected exit");
// [deprecated: dynamic] }

// [deprecated: dynamic] /// TP/SL reset: after `reset()` the same bars produce the same TP/SL exits.
// [deprecated: dynamic] 
// [deprecated: dynamic] /// TP/SL reset: after `reset()` the same bars produce the same TP/SL exits.
// [deprecated: dynamic] #[test]
// [deprecated: dynamic] fn tp_sl_reset_parity() {
// [deprecated: dynamic]     let mut ts = 0i64;
// [deprecated: dynamic]     let mut bars = vec![];
// [deprecated: dynamic]     for i in 0..30u32 { bars.push(bar(ts, 100.0 - i as f64 * 2.2)); ts += 60_000; }
// [deprecated: dynamic]     for _ in 0..5     { bars.push(bar(ts, 34.0)); ts += 60_000; }
// [deprecated: dynamic]     for i in 0..40u32 { bars.push(bar(ts, 34.0 + i as f64 * 1.8)); ts += 60_000; }
// [deprecated: dynamic] 
// [deprecated: dynamic]     let mut dyn_s = build_strategy("dynamic", &json!({
// [deprecated: dynamic]         "indicators": { "rsi": { "type": "rsi", "period": 7 } },
// [deprecated: dynamic]         "entry": { "logic": "and", "rules": [
// [deprecated: dynamic]             { "source": "rsi", "field": "value", "op": "lt", "value": 30 }
// [deprecated: dynamic]         ]},
// [deprecated: dynamic]         "exit": { "logic": "and", "rules": [
// [deprecated: dynamic]             { "source": "rsi", "field": "value", "op": "gt", "value": 95 }
// [deprecated: dynamic]         ]},
// [deprecated: dynamic]         "tp": 0.06,
// [deprecated: dynamic]         "sl": 0.04
// [deprecated: dynamic]     })).unwrap();
// [deprecated: dynamic] 
// [deprecated: dynamic]     let r1 = run(dyn_s.as_mut(), &bars);
// [deprecated: dynamic]     dyn_s.reset();
// [deprecated: dynamic]     let r2 = run(dyn_s.as_mut(), &bars);
// [deprecated: dynamic] 
// [deprecated: dynamic]     assert_parity("TP/SL reset: run1 vs run2", &r1, &r2);
// [deprecated: dynamic] }

// ─────────────────────────────────────────────────────────────────────────────
// Layered strategy with real strategies
// ─────────────────────────────────────────────────────────────────────────────

/// Layered: CEL trend filter gates a CEL mean-reversion signal.
/// Without the filter the signal would fire during both uptrend and downtrend.
/// With the filter active only during the uptrend, downtrend signals are blocked.
#[test]
fn layered_cel_filter_gates_cel_signal() {
    use crate::layered::LayeredStrategy;

    let bars = m1_hours(40);

    // Run signal-only to confirm it fires in both halves.
    let mut signal_only = build_strategy("cel", &json!({
        "entry": "rsi(5) < 35.0",
        "exit":  "rsi(5) > 65.0"
    })).unwrap();
    let signal_sigs = run(signal_only.as_mut(), &bars);
    assert!(!signal_sigs.is_empty(), "layered_gate: signal-only must produce signals");

    // Build layered: H1 EMA filter (only passes when uptrend) + M1 RSI signal.
    let filter = build_strategy("cel", &json!({
        "entry": "H1.ema(3) > H1.ema(8)",
        "exit":  "H1.ema(3) < H1.ema(8)"
    })).unwrap();
    let signal = build_strategy("cel", &json!({
        "entry": "rsi(5) < 35.0",
        "exit":  "rsi(5) > 65.0"
    })).unwrap();
    let mut layered = LayeredStrategy::new(filter, signal);
    let layered_sigs = run(&mut layered, &bars);

    // Layered should produce fewer or equal signals than signal-only
    // (gated by the filter, so downtrend signals are dropped).
    let long_signal_only  = signal_sigs.iter().filter(|(_, d)| *d == Direction::Long).count();
    let long_layered = layered_sigs.iter().filter(|(_, d)| *d == Direction::Long).count();
    assert!(
        long_layered <= long_signal_only,
        "layered gate should block some signals: layered={long_layered}, unfiltered={long_signal_only}"
    );
}

/// Layered: `reset()` clears both filter and signal layers.
#[test]
fn layered_reset_parity() {
    use crate::layered::LayeredStrategy;

    let bars = m1_hours(40);

    let make_layered = || {
        let filter = build_strategy("cel", &json!({
            "entry": "H1.rsi(5) > 50.0",
            "exit":  "H1.rsi(5) < 50.0"
        })).unwrap();
        let signal = build_strategy("cel", &json!({
            "entry": "rsi(5) < 35.0",
            "exit":  "rsi(5) > 65.0"
        })).unwrap();
        LayeredStrategy::new(filter, signal)
    };

    let mut layered = make_layered();
    let r1 = run(&mut layered, &bars);
    layered.reset();
    let r2 = run(&mut layered, &bars);

    assert_parity("layered reset: run1 vs run2", &r1, &r2);
}

/// Layered with a named strategy as the signal layer (ma_crossover).
/// Ensures LayeredStrategy composes correctly with factory-built named strategies.
#[test]
fn layered_cel_filter_named_signal() {
    use crate::layered::LayeredStrategy;

    let bars = m1_hours(40);

    let filter = build_strategy("cel", &json!({
        "entry": "H1.rsi(5) > 50.0",
        "exit":  "H1.rsi(5) < 50.0"
    })).unwrap();
    let signal = build_strategy("ma_crossover", &json!({
        "fast": 5, "slow": 20
    })).unwrap();
    let mut layered = LayeredStrategy::new(filter, signal);

    let sigs = run(&mut layered, &bars);
    // Just check it runs without panicking.
    let _ = sigs;
}

// ─────────────────────────────────────────────────────────────────────────────
// MTF + TP/SL combined
// ─────────────────────────────────────────────────────────────────────────────

// [deprecated: dynamic] /// H1 EMA trend filter on Dynamic + TP/SL: verify the strategy runs without
// [deprecated: dynamic] /// panicking and that signals are reproducible after reset.
// [deprecated: dynamic] 
// [deprecated: dynamic] /// H1 EMA trend filter on Dynamic + TP/SL: verify the strategy runs without
// [deprecated: dynamic] /// panicking and that signals are reproducible after reset.
// [deprecated: dynamic] #[test]
// [deprecated: dynamic] fn mtf_with_tp_sl_reset_parity() {
// [deprecated: dynamic]     let bars = m1_hours(30);
// [deprecated: dynamic] 
// [deprecated: dynamic]     let mut dyn_s = build_strategy("dynamic", &json!({
// [deprecated: dynamic]         "indicators": {
// [deprecated: dynamic]             "h1_ema_fast": { "type": "ema", "period": 5,  "tf": "H1" },
// [deprecated: dynamic]             "h1_ema_slow": { "type": "ema", "period": 10, "tf": "H1" },
// [deprecated: dynamic]             "m1_rsi":      { "type": "rsi", "period": 7 }
// [deprecated: dynamic]         },
// [deprecated: dynamic]         "entry": { "logic": "and", "rules": [
// [deprecated: dynamic]             { "source": "h1_ema_fast", "field": "value", "op": "gt", "compare": "h1_ema_slow" },
// [deprecated: dynamic]             { "source": "m1_rsi",      "field": "value", "op": "lt", "value": 35 }
// [deprecated: dynamic]         ]},
// [deprecated: dynamic]         "exit": { "logic": "or", "rules": [
// [deprecated: dynamic]             { "source": "h1_ema_fast", "field": "value", "op": "lt", "compare": "h1_ema_slow" },
// [deprecated: dynamic]             { "source": "m1_rsi",      "field": "value", "op": "gt", "value": 65 }
// [deprecated: dynamic]         ]},
// [deprecated: dynamic]         "tp": 0.05,
// [deprecated: dynamic]         "sl": 0.03
// [deprecated: dynamic]     })).unwrap();
// [deprecated: dynamic] 
// [deprecated: dynamic]     let r1 = run(dyn_s.as_mut(), &bars);
// [deprecated: dynamic]     dyn_s.reset();
// [deprecated: dynamic]     let r2 = run(dyn_s.as_mut(), &bars);
// [deprecated: dynamic] 
// [deprecated: dynamic]     assert_parity("mtf + tp/sl reset", &r1, &r2);
// [deprecated: dynamic] }

// [deprecated: dynamic] /// MTF Heiken Ashi + TP: HA EMA cross on H1 bars with TP.
// [deprecated: dynamic] /// Verifies HA transform, MTF resampling, and TP/SL all compose correctly.
// [deprecated: dynamic] 
// [deprecated: dynamic] /// MTF Heiken Ashi + TP: HA EMA cross on H1 bars with TP.
// [deprecated: dynamic] /// Verifies HA transform, MTF resampling, and TP/SL all compose correctly.
// [deprecated: dynamic] #[test]
// [deprecated: dynamic] fn mtf_ha_tp_reset_parity() {
// [deprecated: dynamic]     let bars = m1_hours(40);
// [deprecated: dynamic] 
// [deprecated: dynamic]     let mut dyn_s = build_strategy("dynamic", &json!({
// [deprecated: dynamic]         "candle_type": "heiken_ashi",
// [deprecated: dynamic]         "indicators": {
// [deprecated: dynamic]             "fast": { "type": "ema", "period": 3, "tf": "H1" },
// [deprecated: dynamic]             "slow": { "type": "ema", "period": 8, "tf": "H1" }
// [deprecated: dynamic]         },
// [deprecated: dynamic]         "entry": { "logic": "and", "rules": [
// [deprecated: dynamic]             { "source": "fast", "field": "value", "op": "cross_above", "compare": "slow" }
// [deprecated: dynamic]         ]},
// [deprecated: dynamic]         "exit": { "logic": "and", "rules": [
// [deprecated: dynamic]             { "source": "fast", "field": "value", "op": "cross_below", "compare": "slow" }
// [deprecated: dynamic]         ]},
// [deprecated: dynamic]         "tp": 0.07,
// [deprecated: dynamic]         "sl": 0.04
// [deprecated: dynamic]     })).unwrap();
// [deprecated: dynamic] 
// [deprecated: dynamic]     let r1 = run(dyn_s.as_mut(), &bars);
// [deprecated: dynamic]     dyn_s.reset();
// [deprecated: dynamic]     let r2 = run(dyn_s.as_mut(), &bars);
// [deprecated: dynamic] 
// [deprecated: dynamic]     assert_parity("mtf HA + tp/sl reset", &r1, &r2);
// [deprecated: dynamic] }

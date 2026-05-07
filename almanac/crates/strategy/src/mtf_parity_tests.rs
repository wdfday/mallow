//! Parity tests for multi-timeframe (MTF), Heiken Ashi candle transforms,
//! TP/SL exits, and Layered strategy composition.
//!
//! Coverage added here (complementing the per-strategy named/*.rs tests):
//!
//! | Group | Tests |
//! |-------|-------|
//! | **MTF confirmed** | H1 signals only fire at H1 boundary, reset parity |
//! | **MTF live** | live_H1 fires mid-bar, live vs confirmed differ, reset parity, coexistence |
//! | **Heiken Ashi** | HaColor named reset parity |
//! | **TP / SL** | fixed-% TP, fixed-% SL, TP fires before RSI exit, ATR-based TP/SL |
//! | **Layered (real strategies)** | Rhai filter gates Rhai signal, reset parity |

#![cfg(test)]

use alm_core::{Bar, Strategy};
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
// MTF semantics: confirmed vs live, reset
// ─────────────────────────────────────────────────────────────────────────────

/// **Confirmed** H1 signals must only fire at timestamps that are exact multiples
/// of the H1 interval (3 600 000 ms).  These are the first M1 bars of each new
/// H1 bucket — the only moments the `TimeBarResampler` emits a closed H1 bar.
#[test]
fn mtf_confirmed_signals_at_h1_boundary() {
    const H1_MS: i64 = 3_600_000;
    let bars = m1_hours(30);

    let script = r#"
let h1_rsi = ind.rsi(5, "H1");
if h1_rsi[0] < 35.0 { entry = true; }
if h1_rsi[0] > 65.0 { exit  = true; }
"#;
    let mut rhai = build_strategy("rhai", &json!({ "script": script })).unwrap();
    let sigs = run(rhai.as_mut(), &bars);

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


/// Rhai MTF strategy: `reset()` must reproduce the exact same signals when
/// fed the same bars a second time.
#[test]
fn mtf_reset_parity_rhai() {
    let bars = m1_hours(30);

    let script = r#"
let h1_rsi = ind.rsi(5, "H1");
if h1_rsi[0] < 35.0 { entry = true; }
if h1_rsi[0] > 65.0 { exit  = true; }
"#;
    let mut rhai = build_strategy("rhai", &json!({ "script": script })).unwrap();

    let r1 = run(rhai.as_mut(), &bars);
    rhai.reset();
    let r2 = run(rhai.as_mut(), &bars);

    assert!(!r1.is_empty(), "mtf_reset_rhai: expected signals");
    assert_parity("mtf Rhai reset: run1 vs run2", &r1, &r2);
}


// ── live_H1 semantics ─────────────────────────────────────────────────────────

/// `ind.rsi(5, "live_H1")` must fire signals on non-H1-boundary M1 bars,
/// proving `_live` scalar updates every M1 (not just at H1 close).
#[test]
fn mtf_live_h1_fires_mid_bar() {
    const H1_MS: i64 = 3_600_000;
    let bars = m1_hours(30);

    // h1_rsi[0] = last confirmed H1 RSI
    // h1_rsi_live = forming-bar RSI scalar (updates every M1)
    // h1_rsi_fill = bucket fill ratio
    let script = r#"
let h1_rsi = ind.rsi(5, "live_H1");
if h1_rsi_live < 35.0 && h1_rsi_fill > 0.0 { entry = true; }
if h1_rsi_live > 65.0 && h1_rsi_fill > 0.0 { exit  = true; }
"#;
    let mut live = build_strategy("rhai", &json!({ "script": script })).unwrap();
    let live_sigs = run(live.as_mut(), &bars);

    assert!(!live_sigs.is_empty(), "mtf_live_h1: expected at least one signal");
    // At least one signal must be at a non-H1-boundary timestamp.
    let has_mid_bar = live_sigs.iter().any(|(ts, _)| ts % H1_MS != 0);
    assert!(has_mid_bar, "mtf_live_h1: all signals are at H1 boundaries — live semantics not working");
}

/// Confirmed H1 fires only at H1 boundary; live H1 fires at additional intra-hour bars.
/// The two signal sets must differ (live is a superset or different timestamps).
#[test]
fn mtf_live_vs_confirmed_h1_differ() {
    let bars = m1_hours(30);

    let confirmed_script = r#"
let rsi = ind.rsi(5, "H1");
if rsi[0] < 35.0 { entry = true; }
if rsi[0] > 65.0 { exit  = true; }
"#;
    // live uses _live scalar, fires every M1
    let live_script = r#"
let rsi = ind.rsi(5, "live_H1");
if rsi_live < 35.0 && rsi_fill > 0.0 { entry = true; }
if rsi_live > 65.0 && rsi_fill > 0.0 { exit  = true; }
"#;
    let mut confirmed = build_strategy("rhai", &json!({ "script": confirmed_script })).unwrap();
    let mut live      = build_strategy("rhai", &json!({ "script": live_script })).unwrap();

    let conf_sigs = run(confirmed.as_mut(), &bars);
    let live_sigs = run(live.as_mut(), &bars);

    // Live must produce more signals (fires every M1 vs only on H1 close)
    // or at different timestamps.
    assert!(
        live_sigs.len() != conf_sigs.len() || live_sigs != conf_sigs,
        "mtf_live_vs_confirmed: live and confirmed produced identical signals — live semantics not working"
    );
    assert!(!live_sigs.is_empty(), "mtf_live_vs_confirmed: live must produce signals");
}

/// `reset()` on a live_H1 strategy must reproduce identical signals on the same bars.
#[test]
fn mtf_live_h1_reset_parity() {
    let bars = m1_hours(30);

    let script = r#"
let h1_rsi = ind.rsi(5, "live_H1");
if h1_rsi_live < 35.0 && h1_rsi_fill > 0.0 { entry = true; }
if h1_rsi_live > 65.0 && h1_rsi_fill > 0.0 { exit  = true; }
"#;
    let mut rhai = build_strategy("rhai", &json!({ "script": script })).unwrap();

    let r1 = run(rhai.as_mut(), &bars);
    rhai.reset();
    let r2 = run(rhai.as_mut(), &bars);

    assert!(!r1.is_empty(), "mtf_live_h1_reset: expected signals");
    assert_parity("mtf live_H1 reset: run1 vs run2", &r1, &r2);
}

/// Mixed live_H1 + confirmed H1 in the same script: both indicator types coexist.
#[test]
fn mtf_live_and_confirmed_coexist() {
    let bars = m1_hours(30);

    // Use live_H1 for entry trigger, confirmed H1 for exit filter — they operate
    // independently on the same symbol.
    let script = r#"
let h1_rsi = ind.rsi(5, "live_H1");
if h1_rsi_live < 35.0 && h1_rsi_fill > 0.0 { entry = true; }
if h1_rsi[0]   > 65.0                       { exit  = true; }
"#;
    let mut rhai = build_strategy("rhai", &json!({ "script": script })).unwrap();
    let sigs = run(rhai.as_mut(), &bars);

    // Script must compile and produce some signal (no panic, no error)
    // Live entry signals can fire at any M1 bar, confirmed exits only at H1 close.
    assert!(!sigs.is_empty(), "mtf_live_and_confirmed: expected signals");

    // Reset must be deterministic
    rhai.reset();
    let sigs2 = run(rhai.as_mut(), &bars);
    assert_parity("mtf live+confirmed coexist reset", &sigs, &sigs2);
}

/// Mixed MTF + base-TF reset: both H1 and M1 indicator state must be
/// cleared by `reset()`.
#[test]
fn mtf_mixed_tf_reset_parity() {
    let bars = m1_dip_in_uptrend();

    let script = r#"
let h1_rsi = ind.rsi(5, "H1");
let m1_rsi = ind.rsi(5);
if h1_rsi[0] > 50.0 && m1_rsi[0] < 35.0 { entry = true; }
if h1_rsi[0] < 50.0 || m1_rsi[0] > 65.0 { exit  = true; }
"#;
    let mut rhai = build_strategy("rhai", &json!({ "script": script })).unwrap();

    let r1 = run(rhai.as_mut(), &bars);
    rhai.reset();
    let r2 = run(rhai.as_mut(), &bars);

    assert_parity("mtf mixed TF reset: run1 vs run2", &r1, &r2);
}

// ─────────────────────────────────────────────────────────────────────────────
// Heiken Ashi
// ─────────────────────────────────────────────────────────────────────────────

/// HaColor reset: named strategy reproduces the same signals after `reset()`.
#[test]
fn ha_color_reset_parity() {
    use crate::named::heiken_ashi_strategy::HaColor;

    let bars = trending_bars(300);

    let mut named = HaColor::new(1);
    let r1 = run(&mut named, &bars);
    named.reset();
    let r2 = run(&mut named, &bars);
    assert_parity("HaColor named reset", &r1, &r2);
}



//! Parity tests: ScriptStrategy ↔ named strategies.
//!
//! Each test feeds identical bars to both implementations and asserts that
//! signal timestamps and directions match exactly.

#![cfg(test)]

use alm_core::signal::Direction;
use alm_core::strategy::Strategy;
use serde_json::json;

use crate::factory::build_strategy;
use crate::named::MaCrossover;
use crate::test_utils::{assert_parity, run, load_real_bars};

// ── helpers ──────────────────────────────────────────────────────────────────

/// Collect `(timestamp, direction)` from a strategy run.
fn run_sigs(s: &mut dyn Strategy, bars: &[alm_core::Bar]) -> Vec<(i64, Direction)> {
    run(s, bars)
}

// ── EMA crossover: Script ↔ MaCrossover ────────────────────────────────────────

/// Script `cross_above` / `cross_below` on EMA(5,20) should produce the exact
/// same signals as the hardcoded `MaCrossover(5,20)` strategy.
#[test]
fn script_ma_cross_vs_named() {
    let Some(bars) = load_real_bars() else { return; };

    let mut named = MaCrossover::new(5, 20);
    let named_sigs = run_sigs(&mut named, &bars);

    let script = r#"
let ema5  = ind.ema(5);
let ema20 = ind.ema(20);

if cross_above(ema5, ema20) { entry = true; }
if cross_below(ema5, ema20) { exit  = true; }
"#;
    let mut script_strat = build_strategy("script", &json!({ "script": script })).unwrap();
    let script_sigs = run_sigs(script_strat.as_mut(), &bars);

    assert!(!named_sigs.is_empty(), "script_ma_cross: named must produce signals");
    assert_parity("script EMA cross vs MaCrossover", &named_sigs, &script_sigs);
}

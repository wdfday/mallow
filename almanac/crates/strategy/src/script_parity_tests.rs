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

// ── EMA crossover: ind.* ↔ ta.* ─────────────────────────────────────────────

/// `ta.ema(period, close[0])` vs `ind.ema(period)` on the same EMA-cross
/// strategy.
///
/// **Known, confirmed divergence at the start** (first_diff_idx = 0 on real
/// data) — `ind.ema` seeds via SMA over `period` bars before emitting a real
/// value (and the whole script is gated behind `all_ready`, so it never even
/// *runs* until every declared indicator's SMA-seed warmup is done); `ta.ema`
/// seeds immediately on the first value it sees, once `bar_buf_depth`
/// (independent of period) is satisfied — so it starts producing (slightly
/// different) crossover signals a few bars earlier. This is a deliberate
/// design difference, not a bug: see `ta.rs`'s module doc for why `ta.*`
/// exists (arbitrary src, not `ind.*`'s fixed-field warmup contract).
///
/// **Confirmed convergence**: once both have run long enough for the
/// different seeds' influence to decay (EMA weights old values
/// geometrically), `cross_above`/`cross_below` — which only cares about
/// *relative* ordering — produces byte-identical signals. Verified on 20k
/// real bars: the tail (last 200 signals) matches exactly even though the
/// very first signal doesn't. That's the actual regression-worthy
/// invariant this test asserts.
#[test]
fn ta_ema_cross_converges_to_ind_ema_cross_after_warmup() {
    let Some(bars) = load_real_bars() else { return; };

    let ind_script = r#"
let ema5  = ind.ema(5);
let ema20 = ind.ema(20);
if cross_above(ema5, ema20) { entry = true; }
if cross_below(ema5, ema20) { exit  = true; }
"#;
    let ta_script = r#"
let ema5  = ta.ema(5, close[0]);
let ema20 = ta.ema(20, close[0]);
if cross_above(ema5, ema20) { entry = true; }
if cross_below(ema5, ema20) { exit  = true; }
"#;
    let mut ind_strat = build_strategy("script", &json!({ "script": ind_script })).unwrap();
    let mut ta_strat  = build_strategy("script", &json!({ "script": ta_script })).unwrap();

    let ind_sigs = run_sigs(ind_strat.as_mut(), &bars);
    let ta_sigs  = run_sigs(ta_strat.as_mut(),  &bars);

    assert!(!ind_sigs.is_empty(), "ind.* EMA cross must produce signals on real data");
    assert!(!ta_sigs.is_empty(),  "ta.* EMA cross must produce signals on real data");

    let tail_n = 200.min(ind_sigs.len()).min(ta_sigs.len());
    let ind_tail = &ind_sigs[ind_sigs.len() - tail_n..];
    let ta_tail  = &ta_sigs[ta_sigs.len() - tail_n..];
    assert_eq!(
        ind_tail, ta_tail,
        "ta.* and ind.* EMA cross must agree once both are past warmup \
         (ind: {} total signals, ta: {} total signals)",
        ind_sigs.len(), ta_sigs.len()
    );
}

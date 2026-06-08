//! Real-data script parity tests for every named strategy.
//!
//! Each entry maps a named strategy to its script equivalent and asserts that
//! both produce the **exact same** `(timestamp, Direction)` signal stream
//! when fed real BTCUSDT M1 bars. This complements the per-strategy synth
//! parity tests by exercising long contiguous price histories.
//!
//! When parquet test data is missing (CI without committed data), all tests
//! print a skip message and exit cleanly.
//!
//! ## Coverage rule
//!
//! Any newly added named strategy **must** appear here with either:
//!   * a `(name, params, script)` row → strict parity test, OR
//!   * a row in `UNTRANSLATABLE` with a one-line rationale.
//!
//! The `coverage_is_exhaustive` test fails the build if a strategy is missing.

#![cfg(test)]

use std::path::PathBuf;

use alm_core::Bar;
use alm_core::signal::Direction;
use alm_data::{BarFeed, ParquetFeed};
use serde_json::{json, Value};

use crate::factory::build_strategy;

// ── Test data loader ─────────────────────────────────────────────────────────

/// Load a real BTCUSDT M1 day from `crates/data/testdata/`.
/// Returns `None` if the parquet file is not present (e.g. in CI without data).
fn load_btcusdt_m1() -> Option<Vec<Bar>> {
    let path = PathBuf::from(env!("CARGO_MANIFEST_DIR"))
        .parent()
        .unwrap()
        .join("data/testdata/BTCUSDT/M1/BTCUSDT_M1_2026-01.parquet");
    if !path.exists() {
        eprintln!("[parity] testdata missing at {}, skipping", path.display());
        return None;
    }
    let mut feed = ParquetFeed::load(&path, "BTCUSDT").ok()?;
    let mut bars: Vec<Bar> = std::iter::from_fn(|| feed.next()).collect();
    bars.truncate(20000);
    if bars.len() < 200 {
        eprintln!("[parity] only {} bars in parquet, skipping", bars.len());
        return None;
    }
    Some(bars)
}

// ── Run helpers ──────────────────────────────────────────────────────────────

fn run_named(name: &str, params: &Value, bars: &[Bar]) -> Vec<(i64, Direction)> {
    let mut s = build_strategy(name, params)
        .unwrap_or_else(|e| panic!("build_strategy({name}) failed: {e}"));
    bars.iter()
        .flat_map(|b| s.on_bar(b))
        .map(|s| (s.timestamp, s.direction))
        .collect()
}

fn run_script(script: &str, bars: &[Bar]) -> Vec<(i64, Direction)> {
    let mut s = build_strategy("script", &json!({ "script": script }))
        .expect("script compile");
    bars.iter()
        .flat_map(|b| s.on_bar(b))
        .map(|s| (s.timestamp, s.direction))
        .collect()
}

const UNTRANSLATABLE: &[(&str, &str)] = &[
    ("kitchen_sink",           "factory does not accept 'kitchen_sink' (demonstrator only)"),
    ("pattern_breakout",       "uses on_window with alm-pattern detector; script has no window hook"),
    ("price_action_swing",     "uses raw OHLC pivot detection + ATR-trailed stop not expressible in script DSL"),
    ("orb_breakout",           "session-aware opening-range needs explicit timestamp-gap state"),
];

// ── Strategy → script mapping ──────────────────────────────────────────────────
//
// Each entry: (named_key, params_json, script_src).
// Scripts mirror the per-named unit tests already in each file.

fn translation_rows() -> Vec<(&'static str, Value, &'static str)> {
    vec![
        // ── MA / EMA cross ───────────────────────────────────────────────────
        ("ma_crossover", json!({"fast": 20, "slow": 50}), r#"
let e20 = ind.ema(20);
let e50 = ind.ema(50);
if cross_above(e20, e50) { entry = true; }
if cross_below(e20, e50) { exit = true; }
"#),
        ("triple_ema", json!({"ema1": 10, "ema2": 20, "ema3": 50}), r#"
let e10 = ind.ema(10);
let e20 = ind.ema(20);
let e50 = ind.ema(50);
let bull_now  = e10[0] > e20[0] && e20[0] > e50[0];
let bull_prev = e10[1] > e20[1] && e20[1] > e50[1];
if !bull_prev && bull_now  { entry = true; }
if bull_prev  && !bull_now { exit  = true; }
"#),
        ("hma_crossover", json!({"fast": 16, "slow": 49}), r#"
let hma16 = ind.hma(16);
let hma49 = ind.hma(49);
if cross_above(hma16, hma49) { entry = true; }
if cross_below(hma16, hma49) { exit  = true; }
"#),
        ("dema_crossover", json!({"fast": 12, "slow": 26}), r#"
let dema12 = ind.dema(12);
let dema26 = ind.dema(26);
if cross_above(dema12, dema26) { entry = true; }
if cross_below(dema12, dema26) { exit  = true; }
"#),
        ("tema_crossover", json!({"fast": 8, "slow": 21}), r#"
let tf = ind.tema(8);
let ts = ind.tema(21);
if cross_above(tf, ts) { entry = true; }
if tf[1] > ts[1] && tf[0] <= ts[0] { exit = true; }
"#),
        ("alma_cross", json!({"fast": 9, "slow": 21}), r#"
let a9 = ind.alma(9);
let a21 = ind.alma(21);
if cross_above(a9, a21) { entry = true; }
if cross_below(a9, a21) { exit = true; }
"#),
        ("lsma_cross", json!({"fast": 20, "slow": 50}), r#"
let l20 = ind.lsma(20);
let l50 = ind.lsma(50);
if cross_above(l20, l50) { entry = true; }
if cross_below(l20, l50) { exit = true; }
"#),

        // ── RSI ──────────────────────────────────────────────────────────────
        ("rsi_mean_rev", json!({"period": 14, "oversold": 30.0, "overbought": 70.0}), r#"
let rsi14 = ind.rsi(14, buf=1);
if rsi14[0] < 30.0 { entry = true; }
if rsi14[0] > 70.0 { exit  = true; }
"#),
        ("rsi_ma_cross", json!({"fast": 20, "slow": 50, "rsi_period": 14, "rsi_entry": 50.0, "rsi_exit": 45.0}), r#"
let e20 = ind.ema(20);
let e50 = ind.ema(50);
let rsi14 = ind.rsi(14, buf=1);
if cross_above(e20, e50) && rsi14[0] > 50.0 { entry = true; }
if cross_below(e20, e50) || rsi14[0] < 45.0 { exit = true; }
"#),

        // ── MACD ─────────────────────────────────────────────────────────────
        ("macd_crossover", json!({"fast": 12, "slow": 26, "signal": 9}), r#"
let mh = ind.macd(12);
if mh[1].histogram <= 0.0 && mh[0].histogram > 0.0 { entry = true; }
if mh[1].histogram >= 0.0 && mh[0].histogram < 0.0 { exit  = true; }
"#),
        ("macd_ma", json!({"fast": 12, "slow": 26, "signal": 9, "ma": 50}), r#"
let mh = ind.macd(12, buf=1);
let sma50 = ind.sma(50, buf=1);
let in_pos = state["in_position"] == true;
if !in_pos && mh[0].histogram > 0.0 && close[0] > sma50[0] { entry = true; state["in_position"] = true; }
if in_pos && mh[0].histogram < 0.0 { exit = true; state["in_position"] = false; }
"#),
        ("ppo_histogram", json!({"fast": 12, "slow": 26, "signal": 9}), r#"
let pp = ind.ppo(12);
if pp[1].histogram <= 0.0 && pp[0].histogram > 0.0 { entry = true; }
if pp[1].histogram >= 0.0 && pp[0].histogram < 0.0 { exit  = true; }
"#),

        // ── Stochastic / StochRSI / KDJ ──────────────────────────────────────
        ("stochastic_crossover", json!({"k_period": 14, "d_period": 3, "oversold": 20.0, "overbought": 80.0}), r#"
let st = ind.stochastic(14);
if st[1].k <= st[1].d && st[0].k > st[0].d && st[0].d < 20.0 { entry = true; }
if st[1].k >= st[1].d && st[0].k < st[0].d && st[0].d > 80.0 { exit  = true; }
"#),
        ("stoch_rsi", json!({"period": 14, "smooth_d": 3, "oversold": 0.2, "overbought": 0.8}), r#"
let sk = ind.stoch_rsi(14);
if sk[1].k >= 0.2 && sk[0].k < 0.2 { entry = true; }
if sk[1].k <= 0.8 && sk[0].k > 0.8 { exit  = true; }
"#),
        ("kdj", json!({"period": 9, "k_period": 3, "d_period": 3, "oversold": 20.0, "overbought": 80.0}), r#"
let kdj9 = ind.kdj(9);
if state["in_position"] == () {
    state["in_position"] = false;
}
let in_pos = state["in_position"];
if !in_pos {
    if kdj9[0].k < 20.0 && kdj9[0].d < 20.0 && gt(kdj9[0].k, kdj9[1].k) {
        state["in_position"] = true;
        entry = true;
    }
} else {
    if kdj9[0].k > 80.0 || kdj9[0].j > 100.0 {
        state["in_position"] = false;
        exit = true;
    }
}
"#),

        // ── ADX / DMI / Aroon / Vortex / RWI ─────────────────────────────────
        ("adx_ema_cross", json!({"fast": 20, "slow": 50, "adx_period": 14, "adx_threshold": 25.0}), r#"
let e20 = ind.ema(20);
let e50 = ind.ema(50);
let adx14 = ind.adx(14, buf=1);
if cross_above(e20, e50) && adx14[0] > 25.0 { entry = true; }
if cross_below(e20, e50) { exit = true; }
"#),
        ("aroon_trend", json!({"period": 25, "bull_threshold": 70.0, "bear_threshold": 30.0}), r#"
let ar = ind.aroon(25, buf=1);
if ar[0].up > 70.0 && ar[0].down < 30.0 { entry = true; }
if ar[0].up < ar[0].down { exit = true; }
"#),
        ("vortex_trend", json!({"period": 14}), r#"
let vx = ind.vortex(14);
if vx[1].plus_vi <= vx[1].minus_vi && vx[0].plus_vi > vx[0].minus_vi { entry = true; }
if vx[1].plus_vi >= vx[1].minus_vi && vx[0].plus_vi < vx[0].minus_vi { exit  = true; }
"#),

        // ── Momentum / oscillators ───────────────────────────────────────────
        ("cci_reversal", json!({"period": 20, "entry_level": -100.0, "exit_level": 100.0}), r#"
let cci20 = ind.cci(20);
if cci20[1] <= -100.0 && cci20[0] > -100.0 { entry = true; }
if cci20[1] <= 100.0 && cci20[0] > 100.0 { exit = true; }
"#),
        ("cmo_zero_cross", json!({"cmo_period": 14, "ema_period": 50}), r#"
let cmo14 = ind.cmo(14, buf=2);
let ema50 = ind.ema(50, buf=1);
if cmo14[1] <= 0.0 && cmo14[0] > 0.0 && close[0] > ema50[0] { entry = true; }
if (cmo14[1] >= 0.0 && cmo14[0] < 0.0) || close[0] < ema50[0] { exit = true; }
"#),
        ("fisher_crossover", json!({"period": 10}), r#"
let fi = ind.fisher(10);
if fi[1].fisher <= fi[1].signal && fi[0].fisher > fi[0].signal { entry = true; }
if fi[1].fisher >= fi[1].signal && fi[0].fisher < fi[0].signal { exit  = true; }
"#),
        ("roc", json!({"period": 10}), r#"
let roc10 = ind.roc(10);
if roc10[1] <= 0.0 && roc10[0] > 0.0 { entry = true; }
if roc10[1] >= 0.0 && roc10[0] < 0.0 { exit  = true; }
"#),
        ("kst", json!({"period": 10}), r#"
let roc10 = ind.roc(10);
if roc10[1] <= 0.0 && roc10[0] > 0.0 { entry = true; }
if roc10[1] >= 0.0 && roc10[0] < 0.0 { exit  = true; }
"#),
        ("trix", json!({"period": 18, "signal": 9}), r#"
let th = ind.trix(18);
if th[1].histogram <= 0.0 && th[0].histogram > 0.0 { entry = true; }
if th[1].histogram >= 0.0 && th[0].histogram < 0.0 { exit  = true; }
"#),
        ("tsi", json!({"first": 25, "second": 13, "entry_threshold": -25.0, "exit_threshold": 25.0}), r#"
let tsi25 = ind.tsi(25);
if tsi25[1] < -25.0 && tsi25[0] >= -25.0 { entry = true; }
if tsi25[1] >= 25.0 && tsi25[0] < 25.0   { exit  = true; }
"#),
        ("uo_reversal", json!({"fast": 7, "medium": 14, "slow": 28, "oversold": 30.0, "overbought": 70.0}), r#"
let uo = ind.uo(0);
if uo[1] <= 30.0 && uo[0] > 30.0 { entry = true; }
if uo[0] > 70.0 { exit = true; }
"#),
        ("connors_rsi", json!({"rsi_period": 3, "streak_period": 2, "rank_period": 100, "oversold": 10.0, "overbought": 70.0}), r#"
let crsi = ind.connors_rsi(3, buf=1);
if crsi[0] < 10.0 { entry = true; }
if crsi[0] > 70.0 { exit  = true; }
"#),
        ("ao", json!({"fast": 5, "slow": 34}), r#"
let ao = ind.ao(0);
if ao[1] <= 0.0 && ao[0] > 0.0 { entry = true; }
if ao[1] >= 0.0 && ao[0] < 0.0 { exit = true; }
"#),

        // ── Volume / VWAP / OBV / MFI / CMF ──────────────────────────────────
        ("mfi_trend", json!({"period": 14, "bull_threshold": 50.0, "bear_threshold": 40.0}), r#"
let mfi14 = ind.mfi(14);
if mfi14[1] <= 50.0 && mfi14[0] > 50.0 { entry = true; }
if mfi14[0] < 40.0  { exit  = true; }
"#),
        ("mfi_revert", json!({"period": 14, "oversold": 20.0, "overbought": 80.0}), r#"
let mfi14 = ind.mfi(14);
if mfi14[1] <= 20.0 && mfi14[0] > 20.0 { entry = true; }
if mfi14[1] <= 80.0 && mfi14[0] > 80.0 { exit  = true; }
"#),
        ("cmf_ema_trend", json!({"cmf_period": 20, "ema_period": 50, "bull_threshold": 0.1, "bear_threshold": 0.1}), r#"
let cmf20 = ind.cmf(20, buf=1);
let ema50 = ind.ema(50, buf=1);
if cmf20[0] > 0.1 && close[0] > ema50[0] { entry = true; }
if cmf20[0] < -0.1 { exit = true; }
"#),
        ("vwma_rsi", json!({"vwma_period": 20, "rsi_period": 14, "rsi_entry": 50.0, "rsi_exit": 45.0}), r#"
let vwma20 = ind.vwma(20, buf=1);
let rsi14 = ind.rsi(14, buf=1);
if close[0] > vwma20[0] && rsi14[0] > 50.0 { entry = true; }
if rsi14[0] < 45.0 { exit = true; }
"#),

        // ── Trend filters (CCI/Stoch + ADX, RSI + EMA) ───────────────────────
        ("chop_filter", json!({"chop_period": 14, "fast_ema": 8, "slow_ema": 21, "chop_threshold": 61.8}), r#"
let ema8  = ind.ema(8);
let ema21 = ind.ema(21);
let chop14 = ind.chop(14, buf=1);
if ema8[1] <= ema21[1] && ema8[0] > ema21[0] && chop14[0] < 61.8 { entry = true; }
if ema8[1] >= ema21[1] && ema8[0] < ema21[0] { exit = true; }
"#),

        // ── Donchian / Keltner / BB Reversal ─────────────────────────────────
        ("donchian_breakout", json!({"entry": 20, "exit": 10}), r#"
let du20 = ind.donchian(20);
let dl10 = ind.donchian(10);
if close[0] > du20[1].upper { entry = true; }
if close[0] < dl10[1].lower { exit  = true; }
"#),
        ("keltner_breakout", json!({"period": 20, "atr_period": 10, "multiplier": 2.0}), r#"
let kc20 = ind.keltner(20, buf=1);
if close[0] > kc20[0].upper { entry = true; }
if close[0] < kc20[0].lower { exit  = true; }
"#),
        ("bb_rsi_reversal", json!({"bb_period": 20, "bb_std": 2.0, "rsi_period": 14, "oversold": 35.0, "overbought": 65.0}), r#"
let bb20  = ind.bbands(20, buf=1);
let rsi14 = ind.rsi(14, buf=1);
if close[0] < bb20[0].lower && rsi14[0] < 35.0 { entry = true; }
if close[0] > bb20[0].middle || rsi14[0] > 65.0 { exit  = true; }
"#),
        ("atr_trailing", json!({"ema_period": 20, "atr_period": 14, "atr_multiplier": 2.0}), r#"
let ema20 = ind.ema(20, buf=1);
let atr14 = ind.atr(14, buf=1);
if state["in_position"] == () {
    state["in_position"] = false;
    state["highest_since_entry"] = 0.0;
    state["trailing_stop"] = 0.0;
}
if state["in_position"] {
    if close[0] > state["highest_since_entry"] {
        state["highest_since_entry"] = close[0];
        state["trailing_stop"] = close[0] - atr14[0].atr * 2.0;
    }
    if close[0] < state["trailing_stop"] {
        state["in_position"] = false;
        state["highest_since_entry"] = 0.0;
        state["trailing_stop"] = 0.0;
        exit = true;
    }
} else {
    if close[0] > ema20[0] {
        state["in_position"] = true;
        state["highest_since_entry"] = close[0];
        state["trailing_stop"] = close[0] - atr14[0].atr * 2.0;
        entry = true;
    }
}
"#),
        ("chandelier_exit", json!({"period": 22, "multiplier": 3.0}), r#"
let atr22 = ind.atr(22);
let hh = highest(high, 22);
let stop = hh - 3.0 * atr22[0].atr;
let bull = close[0] > stop;
if state["in_position"] == () {
    state["in_position"] = false;
    state["prev_bull"] = ();
}
let was_bull = state["prev_bull"];
state["prev_bull"] = bull;
if was_bull != () {
    if !was_bull && bull && !state["in_position"] {
        state["in_position"] = true;
        entry = true;
    }
    if was_bull && !bull && state["in_position"] {
        state["in_position"] = false;
        exit = true;
    }
}
"#),
        ("bb_squeeze", json!({"period": 20, "std": 2.0}), r#"
let bb20 = ind.bbands(20);
let squeezed = bb20[0].bandwidth < 0.04;
if state["was_squeezed"] == () {
    state["was_squeezed"] = false;
}
if squeezed {
    state["was_squeezed"] = true;
}
if state["was_squeezed"] && close[0] > bb20[0].upper {
    state["was_squeezed"] = false;
    entry = true;
}
if close[0] < bb20[0].middle {
    exit = true;
}
"#),
        ("mean_reversion", json!({"bb_period": 20, "bb_std": 2.0, "rsi_period": 14, "bars": 4}), r#"
let bb20 = ind.bbands(20);
let rsi14 = ind.rsi(14);
if state["below_band_count"] == () {
    state["below_band_count"] = 0;
    state["in_position"] = false;
}
let below_band_count = state["below_band_count"];
if state["in_position"] {
    if close[0] >= bb20[0].upper || rsi14[0] > 70.0 {
        state["in_position"] = false;
        state["below_band_count"] = 0;
        exit = true;
    }
} else {
    if close[0] < bb20[0].lower {
        state["below_band_count"] = below_band_count + 1;
    } else if close[0] > bb20[0].lower && below_band_count >= 4 {
        state["below_band_count"] = 0;
        state["in_position"] = true;
        strength = ((50.0 - rsi14[0]) / 50.0).clamp(0.0, 1.0);
        entry = true;
    } else {
        state["below_band_count"] = 0;
    }
}
"#),
        ("volatility_squeezer", json!({"atr_period": 14, "ma_period": 50}), r#"
let atr14 = ind.atr(14);
let sma50 = ind.sma(50);
let atr_expanding = atr14[0].atr > atr14[1].atr;
if atr_expanding && close[0] > sma50[0] {
    entry = true;
}
if close[0] < sma50[0] {
    exit = true;
}
"#),
        ("volatility_vanguard", json!({"bb_period": 20, "bb_std": 2.0, "atr_period": 14}), r#"
let bb20 = ind.bbands(20);
let atr14 = ind.atr(14);
let atr_expanding = atr14[0].atr > atr14[1].atr;
if close[0] > bb20[0].upper && atr_expanding {
    entry = true;
}
if close[0] < bb20[0].middle {
    exit = true;
}
"#),
        ("highest_breakout", json!({"period": 20}), r#"
let dummy = highest(close, 21);
let highest_val = close[1];
let lowest_val = close[1];
let i = 2;
while i <= 20 {
    if close[i] > highest_val { highest_val = close[i]; }
    if close[i] < lowest_val { lowest_val = close[i]; }
    i = i + 1;
}
if close[0] > highest_val { entry = true; }
if close[0] < lowest_val { exit = true; }
"#),
        ("bb_keltner_squeeze", json!({"bb_period": 20, "bb_std": 2.0, "kc_period": 20, "kc_atr": 10, "kc_mult": 1.5}), r#"
let bb20 = ind.bbands(20);
let kc20 = ind.keltner(20, 10, multiplier=1.5);
let squeezed = bb20[0].upper < kc20[0].upper && bb20[0].lower > kc20[0].lower;
if state["was_squeezed"] == () {
    state["was_squeezed"] = false;
    state["in_position"] = false;
}
let squeeze_released = state["was_squeezed"] && !squeezed;
state["was_squeezed"] = squeezed;
if squeeze_released && close[0] > bb20[0].middle && !state["in_position"] {
    state["in_position"] = true;
    entry = true;
}
if close[0] < bb20[0].middle && state["in_position"] {
    state["in_position"] = false;
    exit = true;
}
"#),

        // ── KAMA / SAR / SuperTrend ──────────────────────────────────────────
        ("kama", json!({"er_period": 10, "fast": 2, "slow": 30}), r#"
let kama10 = ind.kama(10);
let was_above = close[1] > kama10[1];
let is_above  = close[0] > kama10[0];
if !was_above && is_above  { entry = true; }
if  was_above && !is_above { exit  = true; }
"#),
        ("parabolic_sar", json!({"step": 0.02, "max": 0.2}), r#"
let ps = ind.parabolic_sar(0);
let was_bull = ps[1].bullish >= 0.5;
let now_bull = ps[0].bullish >= 0.5;
if !was_bull && now_bull  { entry = true; }
if  was_bull && !now_bull { exit  = true; }
"#),
        ("supertrend", json!({"period": 10, "multiplier": 3.0}), r#"
let st = ind.supertrend(10);
let was_bull = st[1].bullish >= 0.5;
let now_bull = st[0].bullish >= 0.5;
if !was_bull && now_bull  { entry = true; }
if  was_bull && !now_bull { exit  = true; }
"#),
        // ── Heiken Ashi ──────────────────────────────────────────────────────
        ("heiken_ashi_color", json!({"smooth": 1}), r#"
candle.transform("heiken_ashi");
let is_bullish = close[0] >= open[0];
let was_bullish = close[1] >= open[1];
if is_bullish && !was_bullish { entry = true; }
if !is_bullish && was_bullish { exit = true; }
"#),
        ("heiken_ashi_breakout", json!({"smooth": 1, "consecutive_bars": 2}), r#"
candle.transform("heiken_ashi");
if state["bull_count"] == () {
    state["bull_count"] = 0;
    state["bear_count"] = 0;
}
let is_bullish = close[0] >= open[0];
if is_bullish {
    state["bull_count"] = state["bull_count"] + 1;
    state["bear_count"] = 0;
} else {
    state["bear_count"] = state["bear_count"] + 1;
    state["bull_count"] = 0;
}
if state["bull_count"] >= 2 { entry = true; }
if state["bear_count"] >= 2 { exit = true; }
"#),

        // ── Momentum ROC variants ────────────────────────────────────────────
        ("momentum_roc", json!({"roc_period": 10, "ema_period": 50, "entry_threshold": 2.0, "exit_threshold": 0.0}), r#"
let roc10 = ind.roc(10, buf=1);
let ema50  = ind.ema(50, buf=1);
if roc10[0] > 2.0 && close[0] > ema50[0] { entry = true; }
if roc10[0] < 0.0 || close[0] < ema50[0] { exit  = true; }
"#),
        ("dual_momentum", json!({"fast": 10, "slow": 30}), r#"
let roc10 = ind.roc(10, buf=1);
let roc30 = ind.roc(30, buf=1);
if roc10[0] > 0.0 && roc30[0] > 0.0 { entry = true; }
if roc10[0] < 0.0 || roc30[0] < 0.0 { exit  = true; }
"#),

        // ── Williams %R + EMA ────────────────────────────────────────────────
        ("williams_r_ma", json!({"wr_period": 14, "ema_period": 50, "oversold": -80.0, "overbought": -20.0}), r#"
let wr14  = ind.williams_r(14);
let ema50 = ind.ema(50, buf=1);
if wr14[1] <= -80.0 && wr14[0] > -80.0 && close[0] > ema50[0] { entry = true; }
if wr14[1] >= -20.0 && wr14[0] < -20.0 { exit = true; }
"#),

        // ── DMI / Wolfstein / TrendTransition / TrendFollower ────────────────
        ("dmi_adx", json!({"period": 14, "adx_threshold": 25.0}), r#"
let adx14 = ind.adx(14);
let dmi14 = ind.dmi(14);
if dmi14[1].plus_di <= dmi14[1].minus_di && dmi14[0].plus_di > dmi14[0].minus_di && adx14[0] > 25.0 { entry = true; }
if dmi14[1].minus_di <= dmi14[1].plus_di && dmi14[0].minus_di > dmi14[0].plus_di { exit  = true; }
"#),
        ("wolfstein", json!({"adx_period": 14, "long_threshold": 27.5, "short_threshold": 20.5}), r#"
let adx14 = ind.adx(14, buf=1);
let dmi14 = ind.dmi(14, buf=1);
if adx14[0] > 27.5 && dmi14[0].plus_di > dmi14[0].minus_di { entry = true; }
if adx14[0] < 20.5 { exit = true; }
"#),
        ("swing_trader", json!({"cci_period": 20, "adx_period": 14, "adx_threshold": 25.0}), r#"
let cci20 = ind.cci(20);
let adx14 = ind.adx(14, buf=1);
if cci20[1] <= 100.0  && cci20[0] >  100.0 && adx14[0] > 25.0 { entry = true; }
if cci20[1] >= -100.0 && cci20[0] < -100.0 { exit = true; }
"#),

        // ── Bollinger MACD / Volatility ──────────────────────────────────────
        ("bollinger_macd", json!({"bb_period": 20, "bb_std": 2.0, "fast": 12, "slow": 26, "signal": 9}), r#"
let bb20 = ind.bbands(20, buf=1);
let mh = ind.macd(12, buf=1);
if close[0] > bb20[0].upper && mh[0].histogram > 0.0 { entry = true; }
if close[0] < bb20[0].middle || mh[0].histogram < 0.0 { exit = true; }
"#),

        // ── Equilibrium / Range / Reversal / Oscillator combos ───────────────
        ("equilibrium_explorer", json!({"ema_period": 200, "stoch_k": 14, "stoch_d": 3, "stoch_oversold": 20.0, "stoch_overbought": 80.0, "macd_fast": 12, "macd_slow": 26, "macd_signal": 9}), r#"
let ema200 = ind.ema(200, buf=1);
let st = ind.stochastic(14, buf=1);
let mh = ind.macd(12, buf=1);
if close[0] > ema200[0] && st[0].k < 20.0 && mh[0].histogram > 0.0 { entry = true; }
if st[0].k > 80.0 || mh[0].histogram < 0.0 { exit = true; }
"#),
        ("range_rover", json!({"k": 14, "d": 3, "ma": 50, "oversold": 20.0, "overbought": 80.0}), r#"
let st = ind.stochastic(14, buf=1);
let sma50 = ind.sma(50, buf=1);
if st[0].k < 20.0 && close[0] > sma50[0] { entry = true; }
if st[0].k > 80.0 { exit = true; }
"#),
        ("reversal_catcher", json!({"k": 14, "d": 3, "rsi_period": 14}), r#"
let st = ind.stochastic(14);
let rsi14 = ind.rsi(14, buf=1);
if st[1].k <= st[1].d && st[0].k > st[0].d && rsi14[0] < 50.0 { entry = true; }
if (st[1].k >= st[1].d && st[0].k < st[0].d) || rsi14[0] > 70.0 { exit = true; }
"#),

        // ── Trend Transition / Follower (slow MA cross + filters) ────────────
        ("trend_transition", json!({"fast": 50, "slow": 200, "adx_period": 14, "adx_threshold": 25.0}), r#"
let e50 = ind.ema(50);
let e200 = ind.ema(200);
let adx14 = ind.adx(14, buf=1);
if cross_above(e50, e200) && adx14[0] > 25.0 { entry = true; }
if cross_below(e50, e200) { exit = true; }
"#),

        ("trend_follower", json!({"fast_ma": 50, "slow_ma": 200, "macd_fast": 12, "macd_slow": 26, "macd_signal": 9}), r#"
let s50 = ind.sma(50, buf=1);
let s200 = ind.sma(200, buf=1);
let mh = ind.macd(12, buf=1);
let in_pos = state["in_position"] == true;
if !in_pos && s50[0] > s200[0] && mh[0].histogram > 0.0 { entry = true; state["in_position"] = true; }
if in_pos && (s50[0] < s200[0] || mh[0].histogram < 0.0) { exit = true; state["in_position"] = false; }
"#),

        // ── Multi: SuperTrend + MACD ─────────────────────────────────────────
        ("supertrend_macd", json!({"period": 10, "multiplier": 3.0, "macd_fast": 12, "macd_slow": 26, "macd_signal": 9}), r#"
let st = ind.supertrend(10, buf=1);
let mh = ind.macd(12, buf=1);
if st[0].bullish >= 0.5 && mh[0].histogram > 0.0 { entry = true; }
if st[0].bullish < 0.5 { exit = true; }
"#),

        // ── Stochastic DK / RWI / Alligator / Volatility ratio ───────────────
        ("stochastic_dk", json!({"k": 14, "d": 3}), r#"
let st = ind.stochastic(14);
if st[1].k <= st[1].d && st[0].k > st[0].d { entry = true; }
if st[1].k >= st[1].d && st[0].k < st[0].d { exit = true; }
"#),
        ("rwi", json!({"period": 14, "threshold": 1.0}), r#"
let rwi14 = ind.rwi(14, buf=1);
if rwi14[0].rwi_high > 1.0 { entry = true; }
if rwi14[0].rwi_low  > 1.0 { exit  = true; }
"#),
        ("alligator", json!({"jaw": 13, "teeth": 8, "lips": 5}), r#"
let al = ind.alligator(13);
if al[1].bullish < 0.5 && al[0].bullish >= 0.5 { entry = true; }
if al[1].bullish >= 0.5 && al[0].bullish < 0.5 { exit = true; }
"#),
        ("volatility_ratio", json!({"lookback": 10, "threshold": 0.5}), r#"
let vr = ind.volatility_ratio(10, buf=1);
if vr[0] > 0.5 && close[0] > close[1] { entry = true; }
if vr[0] <= 0.5 { exit = true; }
"#),

        // ── GMMA ─────────────────────────────────────────────────────────────
        ("gmma_crossover", json!({}), r#"
let gm = ind.gmma(0);
// .spread = (mean(short) - mean(long)) / mean(long); spread > 0 is exactly
// short_avg > long_avg (prices > 0), so this matches the named strategy's
// group-mean crossover.
let bull_now  = gm[0].spread > 0.0;
let bull_prev = gm[1].spread > 0.0;
if !bull_prev && bull_now  { entry = true; }
if bull_prev  && !bull_now { exit  = true; }
"#),

        // ── SMI Reversal ─────────────────────────────────────────────────────
        ("smi_reversal", json!({"period": 13, "smooth1": 25, "smooth2": 2, "signal_period": 9, "oversold": -40.0, "overbought": 40.0}), r#"
let smi13 = ind.smi(13);
if smi13[1].smi <= smi13[1].signal && smi13[0].smi > smi13[0].signal && smi13[1].smi < -40.0 { entry = true; }
if smi13[0].smi > 40.0 || (smi13[1].smi >= smi13[1].signal && smi13[0].smi < smi13[0].signal) { exit  = true; }
"#),

        // ── Ichimoku ─────────────────────────────────────────────────────────
        ("ichimoku_cloud", json!({"tenkan": 9, "kijun": 26, "senkou_b": 52}), r#"
let ic = ind.ichimoku(9, buf=1);
if ic[0].above_cloud >= 0.5 { entry = true; }
if ic[0].below_cloud >= 0.5 { exit  = true; }
"#),
        ("ichimoku_cross", json!({"tenkan": 9, "kijun": 26, "senkou_b": 52}), r#"
let ic = ind.ichimoku(9);
if ic[1].tenkan <= ic[1].kijun && ic[0].tenkan > ic[0].kijun && ic[0].above_cloud >= 0.5 { entry = true; }
if ic[1].tenkan >= ic[1].kijun && ic[0].tenkan < ic[0].kijun { exit = true; }
"#),
        ("elder_ray", json!({"period": 13}), r#"
let er13 = ind.elder_ray(13);
let ema_rising = er13[0].ema > er13[1].ema;
if ema_rising && er13[0].bear_power < 0.0 && er13[0].bear_power > er13[1].bear_power { entry = true; }
if er13[0].bull_power < 0.0 && er13[1].bull_power >= 0.0 { exit = true; }
"#),
        ("pixel_3", json!({}), r#"
let hh5 = highest(high, 5);
let ll5 = lowest(low, 5);
let mid5 = (hh5 + ll5) / 2.0;
let hh20 = highest(high, 20);
let ll20 = lowest(low, 20);
let mid20 = (hh20 + ll20) / 2.0;
let hh60 = highest(high, 60);
let ll60 = lowest(low, 60);
let mid60 = (hh60 + ll60) / 2.0;
let ts5 = gt(close[0], mid5);
let ts4 = gt(close[0], mid20);
let ts3 = gt(close[0], mid60);
let green_count = 0;
if ts5 { green_count = green_count + 1; }
if ts4 { green_count = green_count + 1; }
if ts3 { green_count = green_count + 1; }
if green_count >= 2 {
    strength = green_count / 3.0;
    entry = true;
}
if green_count == 0 {
    exit = true;
}
"#),
        ("ma_pullback", json!({"ma_period": 50}), r#"
let sma50 = ind.sma(50, buf=1);
if state["in_position"] == () {
    state["in_position"] = false;
    state["near_ma"] = false;
}
if state["in_position"] {
    if close[0] < sma50[0] {
        state["in_position"] = false;
        state["near_ma"] = false;
        exit = true;
    }
} else {
    if close[0] < sma50[0] {
        state["near_ma"] = false;
    } else {
        let proximity = (close[0] - sma50[0]) / sma50[0];
        if proximity <= 0.02 {
            state["near_ma"] = true;
        } else if state["near_ma"] && proximity > 0.02 {
            state["near_ma"] = false;
            state["in_position"] = true;
            entry = true;
        }
    }
}
"#),
        ("waddah_attar", json!({"fast": 12, "slow": 26, "bb_period": 20, "bb_std": 2.0}), r#"
let macd12 = ind.macd(12);
let bb20 = ind.bbands(20, buf=1);
let prev_macd = macd12[1].macd;
let explosion = (macd12[0].macd - prev_macd) * 150.0;
let dead_zone = bb20[0].upper - bb20[0].lower;
if explosion > dead_zone && macd12[0].histogram > 0.0 {
    entry = true;
}
if explosion < dead_zone || macd12[0].histogram < 0.0 {
    exit = true;
}
"#),
        ("oscillator_overlord", json!({"rsi_period": 14, "stoch_k": 14, "stoch_d": 3, "cci_period": 20}), r#"
let rsi14 = ind.rsi(14, buf=1);
let st14 = ind.stochastic(14, buf=1);
let cci20 = ind.cci(20, buf=1);
if state["in_position"] == () {
    state["in_position"] = false;
}
let os_count = 0;
if rsi14[0] < 30.0 { os_count = os_count + 1; }
if st14[0].k < 20.0 { os_count = os_count + 1; }
if cci20[0] < -100.0 { os_count = os_count + 1; }
let ob_count = 0;
if rsi14[0] > 70.0 { ob_count = ob_count + 1; }
if st14[0].k > 80.0 { ob_count = ob_count + 1; }
if cci20[0] > 100.0 { ob_count = ob_count + 1; }
if !state["in_position"] {
    if os_count >= 2 {
        state["in_position"] = true;
        entry = true;
    }
} else {
    if ob_count >= 2 {
        state["in_position"] = false;
        exit = true;
    }
}
"#),
        ("heiken_ashi_harmonizer", json!({"smooth": 1, "ema_period": 50}), r#"
if state["ema50_count"] == () {
    let ha_open_1 = (open[1] + close[1]) / 2.0;
    let ha_close_1 = (open[1] + high[1] + low[1] + close[1]) / 4.0;
    
    let ha_close_2 = (open[0] + high[0] + low[0] + close[0]) / 4.0;
    let ha_open_2 = (ha_open_1 + ha_close_1) / 2.0;
    
    state["ha_open"] = ha_open_2;
    state["ha_close"] = ha_close_2;
    
    state["ema50_count"] = 2;
    state["ema50_sum"] = close[1] + close[0];
    state["ema50_val"] = ();
} else {
    let prev_ha_open = state["ha_open"];
    let prev_ha_close = state["ha_close"];
    let ha_close = (open[0] + high[0] + low[0] + close[0]) / 4.0;
    let ha_open = (prev_ha_open + prev_ha_close) / 2.0;
    state["ha_open"] = ha_open;
    state["ha_close"] = ha_close;
    
    state["ema50_count"] = state["ema50_count"] + 1;
    let count = state["ema50_count"];
    let cur_ema50 = ();
    if count < 50 {
        state["ema50_sum"] = state["ema50_sum"] + close[0];
    } else if count == 50 {
        state["ema50_sum"] = state["ema50_sum"] + close[0];
        let val = state["ema50_sum"] / 50.0;
        state["ema50_val"] = val;
        cur_ema50 = val;
    } else {
        let prev = state["ema50_val"];
        let k = 2.0 / 51.0;
        let val = close[0] * k + prev * (1.0 - k);
        state["ema50_val"] = val;
        cur_ema50 = val;
    }
    
    if cur_ema50 != () {
        let is_bullish = state["ha_close"] >= state["ha_open"];
        if is_bullish && close[0] > cur_ema50 {
            entry = true;
        }
        if !is_bullish || close[0] < cur_ema50 {
            exit = true;
        }
    }
}
"#),
        ("scalping_ema", json!({"fast": 8, "slow": 21, "atr_period": 14, "atr_ma_period": 20}), r#"
let fast = ind.ema(8, buf=2);
let slow = ind.ema(21, buf=2);
let atr14 = ind.atr(14, buf=8);

if state["atr_ema_count"] == () {
    state["atr_ema_count"] = 0;
    state["atr_ema_sum"] = 0.0;
    state["atr_ema_val"] = ();
    
    let i = 7;
    while i > 0 {
        let val = atr14[i].atr;
        state["atr_ema_count"] = state["atr_ema_count"] + 1;
        state["atr_ema_sum"] = state["atr_ema_sum"] + val;
        i = i - 1;
    }
}

state["atr_ema_count"] = state["atr_ema_count"] + 1;
let count = state["atr_ema_count"];
let cur_atr = atr14[0].atr;
let cur_atr_ema = ();

if count < 20 {
    state["atr_ema_sum"] = state["atr_ema_sum"] + cur_atr;
} else if count == 20 {
    state["atr_ema_sum"] = state["atr_ema_sum"] + cur_atr;
    let val = state["atr_ema_sum"] / 20.0;
    state["atr_ema_val"] = val;
    cur_atr_ema = val;
} else {
    let prev = state["atr_ema_val"];
    let k = 2.0 / 21.0;
    let val = cur_atr * k + prev * (1.0 - k);
    state["atr_ema_val"] = val;
    cur_atr_ema = val;
}

if cur_atr_ema != () {
    let cross_up = fast[1] <= slow[1] && fast[0] > slow[0];
    let cross_down = fast[1] >= slow[1] && fast[0] < slow[0];
    let atr_expanding = cur_atr > cur_atr_ema;
    
    if cross_up && atr_expanding {
        let val = (fast[0] - slow[0]) / slow[0];
        strength = val.clamp(0.0, 1.0);
        entry = true;
    }
    if cross_down {
        exit = true;
    }
}
"#),
        ("obv_ema_trend", json!({"obv_ema_period": 20, "price_ema_period": 50}), r#"
let obv1 = ind.obv(0, buf=50);
let ema50 = ind.ema(50, buf=1);

if state["obv_ema_count"] == () {
    state["obv_ema_count"] = 0;
    state["obv_ema_sum"] = 0.0;
    state["obv_ema_val"] = ();
    
    let i = 49;
    while i > 0 {
        let val = obv1[i];
        state["obv_ema_count"] = state["obv_ema_count"] + 1;
        let c = state["obv_ema_count"];
        if c < 20 {
            state["obv_ema_sum"] = state["obv_ema_sum"] + val;
        } else if c == 20 {
            state["obv_ema_sum"] = state["obv_ema_sum"] + val;
            state["obv_ema_val"] = state["obv_ema_sum"] / 20.0;
        } else {
            let prev = state["obv_ema_val"];
            let k = 2.0 / 21.0;
            state["obv_ema_val"] = val * k + prev * (1.0 - k);
        }
        i = i - 1;
    }
}

state["obv_ema_count"] = state["obv_ema_count"] + 1;
let count = state["obv_ema_count"];
let cur_obv = obv1[0];
let cur_obv_ema = ();

if count < 20 {
    state["obv_ema_sum"] = state["obv_ema_sum"] + cur_obv;
} else if count == 20 {
    state["obv_ema_sum"] = state["obv_ema_sum"] + cur_obv;
    let val = state["obv_ema_sum"] / 20.0;
    state["obv_ema_val"] = val;
    cur_obv_ema = val;
} else {
    let prev = state["obv_ema_val"];
    let k = 2.0 / 21.0;
    let val = cur_obv * k + prev * (1.0 - k);
    state["obv_ema_val"] = val;
    cur_obv_ema = val;
}

if cur_obv_ema != () {
    let obv_bullish = cur_obv > cur_obv_ema;
    let price_bullish = close[0] > ema50[0];
    
    if state["in_position"] == () {
        state["in_position"] = false;
    }
    let in_pos = state["in_position"];
    
    if obv_bullish && price_bullish && !in_pos {
        state["in_position"] = true;
        entry = true;
    }
    if in_pos && (!obv_bullish || !price_bullish) {
        state["in_position"] = false;
        exit = true;
    }
}
"#),
        ("vwap_bounce", json!({"rsi_period": 14, "oversold": 40.0, "overbought": 65.0, "session_gap_mins": 60}), r#"
let vw = ind.vwap(0, session_gap_mins=60, buf=2);
let rsi14 = ind.rsi(14, buf=1);

let above_vwap = close[0] > vw[0];

if state["prev_above"] == () {
    state["prev_above"] = above_vwap;
} else {
    let was_above = state["prev_above"];
    state["prev_above"] = above_vwap;
    
    if !was_above && above_vwap && rsi14[0] < 50.0 {
        entry = true;
    }
    if was_above && !above_vwap {
        exit = true;
    }
    if rsi14[0] > 65.0 {
        exit = true;
    }
}
"#),
        ("vwap_trend", json!({"session_gap_mins": 60}), r#"
let vw = ind.vwap(0, session_gap_mins=60, buf=2);

let vwap_rising = vw[0] > vw[1];
let above_vwap = close[0] > vw[0];

if state["in_position"] == () {
    state["in_position"] = false;
}
let in_pos = state["in_position"];

if above_vwap && vwap_rising && !in_pos {
    state["in_position"] = true;
    entry = true;
}
if !above_vwap && in_pos {
    state["in_position"] = false;
    exit = true;
}
"#),
    ]
}

// ── The big parity test ──────────────────────────────────────────────────────

#[test]
fn named_vs_script_real_data_parity() {
    let Some(bars) = load_btcusdt_m1() else { return };

    let rows = translation_rows();
    let mut passed: Vec<String> = Vec::new();
    let mut failures: Vec<(String, String)> = Vec::new();

    println!("----------------------------------------------------------------------");
    println!("Parity Test Signal Statistics (20,000 bars):");
    println!("----------------------------------------------------------------------");

    for (name, params, script) in rows.iter() {
        let named = run_named(name, params, &bars);
        let script_sigs = run_script(script, &bars);
        
        let long_count = named.iter().filter(|(_, d)| *d == alm_core::signal::Direction::Long).count();
        let short_count = named.iter().filter(|(_, d)| *d == alm_core::signal::Direction::Short).count();
        let exit_count = named.iter().filter(|(_, d)| *d == alm_core::signal::Direction::Exit).count();
        
        println!(
            "{:<30} | Signals: {:>4} (Long: {:>4}, Short: {:>4}, Exit: {:>4})",
            name,
            named.len(),
            long_count,
            short_count,
            exit_count
        );

        if named == script_sigs {
            passed.push((*name).to_string());
        } else {
            // Find first divergence for compact error reporting.
            let max = named.len().max(script_sigs.len());
            let mut detail = format!(
                "named={} script={} (lens)",
                named.len(),
                script_sigs.len()
            );
            for i in 0..max {
                let a = named.get(i);
                let b = script_sigs.get(i);
                if a != b {
                    detail = format!(
                        "first diff at idx {i}: named={:?} script={:?} (named_len={}, script_len={})",
                        a, b, named.len(), script_sigs.len(),
                    );
                    break;
                }
            }
            failures.push(((*name).to_string(), detail));
        }
    }

    println!("----------------------------------------------------------------------");
    eprintln!("[parity] real-data BTCUSDT M1, {} bars", bars.len());
    eprintln!("[parity] passed {} / {} translatable strategies", passed.len(), rows.len());
    eprintln!("[parity] skipped {} untranslatable strategies:", UNTRANSLATABLE.len());
    for (name, why) in UNTRANSLATABLE {
        eprintln!("[parity]   - {name}: {why}");
    }

    if !failures.is_empty() {
        let mut msg = String::from("real-data parity failures:\n");
        for (name, detail) in &failures {
            msg.push_str(&format!("  - {name}: {detail}\n"));
        }
        panic!("{msg}");
    }
}

// ── Coverage exhaustiveness check ────────────────────────────────────────────

/// Every named strategy registered in the factory must appear either in the
/// translation rows or in `UNTRANSLATABLE`. Catching a missing entry here
/// enforces the project rule that new named strategies ship with a parity
/// test (see project memory: `feedback_strategy_parity_test.md`).
#[test]
fn coverage_is_exhaustive() {
    // Mirror of the factory's full strategy key list. Update both together.
    // (We list keys explicitly rather than enumerating the factory to avoid
    // a brittle introspection-based test.)
    let all_named: &[&str] = &[
        "adx_ema_cross", "alligator", "alma_cross", "ao", "aroon_trend",
        "atr_trailing", "bb_keltner_squeeze", "bb_rsi_reversal", "bb_squeeze",
        "bollinger_macd", "cci_reversal", "chandelier_exit", "chop_filter",
        "cmf_ema_trend", "cmo_zero_cross", "connors_rsi", "dema_crossover",
        "dmi_adx", "donchian_breakout", "dual_momentum", "elder_ray",
        "equilibrium_explorer", "fisher_crossover", "gmma_crossover",
        "heiken_ashi_breakout", "heiken_ashi_color", "heiken_ashi_harmonizer",
        "highest_breakout", "hma_crossover", "ichimoku_cloud", "ichimoku_cross",
        "kama", "kdj", "keltner_breakout", "kitchen_sink", "kst", "lsma_cross",
        "ma_crossover", "ma_pullback", "macd_crossover", "macd_ma",
        "mean_reversion", "mfi_revert", "mfi_trend", "momentum_roc",
        "obv_ema_trend", "orb_breakout", "oscillator_overlord", "parabolic_sar",
        "pattern_breakout", "pixel_3", "ppo_histogram", "price_action_swing",
        "range_rover", "reversal_catcher", "roc", "rsi_ma_cross",
        "rsi_mean_rev", "rwi", "scalping_ema", "smi_reversal", "stoch_rsi",
        "stochastic_crossover", "stochastic_dk", "supertrend", "supertrend_macd",
        "swing_trader", "tema_crossover", "trend_follower", "trend_transition",
        "triple_ema", "trix", "tsi", "uo_reversal", "volatility_ratio",
        "volatility_squeezer", "volatility_vanguard", "vortex_trend",
        "vwap_bounce", "vwap_trend", "vwma_rsi", "waddah_attar",
        "williams_r_ma", "wolfstein",
    ];

    let translated: std::collections::HashSet<&str> =
        translation_rows().iter().map(|(n, _, _)| *n).collect();
    let untranslated: std::collections::HashSet<&str> =
        UNTRANSLATABLE.iter().map(|(n, _)| *n).collect();

    let mut missing: Vec<&str> = Vec::new();
    for name in all_named {
        if !translated.contains(name) && !untranslated.contains(name) {
            missing.push(*name);
        }
    }

    if !missing.is_empty() {
        panic!(
            "coverage gap: these strategies have neither a translation row nor an \
             UNTRANSLATABLE entry: {:?}\n\
             Add the strategy to translation_rows() in named_real_data_parity_tests.rs \
             with its script equivalent, or document why it cannot be translated in UNTRANSLATABLE.",
            missing
        );
    }

    let total = all_named.len();
    let translatable = translated.len();
    let untranslatable = untranslated.len();
    eprintln!(
        "[parity] coverage: {} total = {} translated + {} untranslatable",
        total, translatable, untranslatable
    );
    assert_eq!(translatable + untranslatable, total,
        "translation rows ({}) + untranslatable ({}) must equal total named ({})",
        translatable, untranslatable, total);
}

// ── Rhai export ─────────────────────────────────────────────────────────────────
//
// Dump every named strategy's parity-tested script equivalent to
// `almanac/scripts/examples/named/<name>.rhai` (+ a README listing the
// untranslatable ones). These are the EXACT scripts asserted equal to the named
// impls on real BTCUSDT M1 data, so they're guaranteed-faithful examples.
//
//   cargo test -p alm-strategy dump_named_rhai_examples -- --ignored --nocapture
#[test]
#[ignore = "generator: writes .rhai files, run explicitly"]
fn dump_named_rhai_examples() {
    use std::fs;

    let dir = PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("../../scripts/examples/named");
    fs::create_dir_all(&dir).expect("create named/ dir");

    let rows = translation_rows();
    for (name, params, script) in &rows {
        let header = format!(
"// ─────────────────────────────────────────────────────────────────────────────
// Named strategy: {name}
// Default params: {params}
//
// Rhai equivalent — parity-tested vs `build_strategy(\"{name}\")` on real BTCUSDT M1
// (named_vs_script_real_data_parity). Auto-generated; regenerate with:
//   cargo test -p alm-strategy dump_named_rhai_examples -- --ignored
// ─────────────────────────────────────────────────────────────────────────────
",
            name = name,
            params = serde_json::to_string(params).unwrap(),
        );
        let body = script.trim_start_matches('\n').trim_end();
        fs::write(dir.join(format!("{name}.rhai")), format!("{header}\n{body}\n"))
            .unwrap_or_else(|e| panic!("write {name}.rhai: {e}"));
    }

    // README: index of translatable scripts + the untranslatable strategies & why.
    let mut readme = String::from(
"# Named strategies → Rhai

Auto-generated by `cargo test -p alm-strategy dump_named_rhai_examples -- --ignored`.
Each `<name>.rhai` is the script asserted byte-for-signal equal to the named Rust
strategy `build_strategy(\"<name>\")` on real BTCUSDT M1 data. Do not hand-edit —
edit `translation_rows()` in `named_real_data_parity_tests.rs` and regenerate.

## Translatable (parity-tested)

");
    let mut names: Vec<&str> = rows.iter().map(|(n, _, _)| *n).collect();
    names.sort_unstable();
    for n in &names {
        readme.push_str(&format!("- `{n}.rhai`\n"));
    }
    readme.push_str(
"\n## Untranslatable (no faithful script equivalent)\n\nThese named strategies use state/indicators the V1 script DSL cannot express 1:1:\n\n");
    let mut un: Vec<(&str, &str)> = UNTRANSLATABLE.to_vec();
    un.sort_unstable_by_key(|(n, _)| *n);
    for (n, why) in &un {
        readme.push_str(&format!("- **{n}** — {why}\n"));
    }
    fs::write(dir.join("README.md"), readme).expect("write README");

    eprintln!(
        "[export] wrote {} .rhai files + README ({} untranslatable) → {}",
        rows.len(), UNTRANSLATABLE.len(), dir.display()
    );
}




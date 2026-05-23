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
        .join("data/testdata/BTCUSDT/M1/BTCUSDT_M1_2026-04-13_to_2026-04-13.parquet");
    if !path.exists() {
        eprintln!("[parity] testdata missing at {}, skipping", path.display());
        return None;
    }
    let mut feed = ParquetFeed::load(&path, "BTCUSDT").ok()?;
    let bars: Vec<Bar> = std::iter::from_fn(|| feed.next()).collect();
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

// ── Untranslatable strategies ────────────────────────────────────────────────

/// Strategies whose semantics cannot be expressed cleanly in the script DSL with the
/// current DSL. Documenting them here keeps the coverage check honest.
const UNTRANSLATABLE: &[(&str, &str)] = &[
    ("heiken_ashi_color",      "Heiken-Ashi candle aggregation not exposed as a script indicator"),
    ("heiken_ashi_breakout",   "consecutive-bar HA counter is stateful in a way the script DSL cannot express"),
    ("heiken_ashi_harmonizer", "depends on Heiken-Ashi candles (not in script indicator set)"),
    ("kitchen_sink",           "factory does not accept 'kitchen_sink' (demonstrator only)"),
    ("pattern_breakout",       "uses on_window with alm-pattern detector; script has no window hook"),
    ("price_action_swing",     "uses raw OHLC pivot detection + ATR-trailed stop not expressible in script DSL"),
    ("orb_breakout",           "session-aware opening-range needs explicit timestamp-gap state"),
    ("vwap_bounce",            "VWAP session reset + multi-clause exit not 1:1 with stateless script exit"),
    ("vwap_trend",             "VWAP session reset semantics differ between named impl and script vwap"),
    ("pixel_3",                "multi-window midpoint comparison requires custom window state"),
    ("atr_trailing",           "trailing-stop state machine (highest_since_entry) not expressible"),
    ("chandelier_exit",        "manual highest-high tracking + trailing stop state"),
    ("ma_pullback",            "two-step proximity → bounce state machine outside script DSL"),
    ("bb_squeeze",             "was_squeezed latch combined with directional break not expressible"),
    ("bb_keltner_squeeze",     "squeeze-release latch state across bars"),
    ("mean_reversion",         "below_band_count consecutive-bar latch state"),
    ("volatility_squeezer",    "prev_atr expanding check + in-position gating mismatch"),
    ("volatility_vanguard",    "prev_atr expanding check is bar-1 lookback not natively expressible cleanly"),
    ("waddah_attar",           "prev_macd lookback × sensitivity formula deviates from any single script pattern"),
    ("highest_breakout",       "rolling-window lookback excluding current bar — DSL highest() includes current"),
    ("scalping_ema",           "requires ATR expansion gate inside crossover, hard to mirror exactly"),
    ("kdj",                    "in_position state machine + per-bar prev_k > k check uses gated transitions script DSL cannot express"),
    ("obv_ema_trend",          "needs streaming EMA-of-OBV which script ind.obv() does not expose"),
    ("elder_ray",              "ElderRay indicator (with .bull_power/.bear_power fields) is not exposed in script DSL — ind.bull_bear has identical math but different field names; warmup also diverges"),
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
if cross_above(e10, e20) && e20[0] > e50[0] { entry = true; }
if cross_below(e10, e20) { exit = true; }
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
let mh = ind.macd(12);
let sma50 = ind.sma(50, buf=1);
if mh[1].histogram <= 0.0 && mh[0].histogram > 0.0 && close[0] > sma50[0] { entry = true; }
if (mh[1].histogram >= 0.0 && mh[0].histogram < 0.0) || close[0] < sma50[0] { exit = true; }
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
        ("oscillator_overlord", json!({"rsi_period": 14, "stoch_k": 14, "stoch_d": 3, "cci_period": 20}), r#"
let rsi14 = ind.rsi(14, buf=1);
let st = ind.stochastic(14, buf=1);
let cci20 = ind.cci(20, buf=1);
let os = (if rsi14[0] < 30.0 { 1 } else { 0 })
       + (if st[0].k < 20.0 { 1 } else { 0 })
       + (if cci20[0] < -100.0 { 1 } else { 0 });
let ob = (if rsi14[0] > 70.0 { 1 } else { 0 })
       + (if st[0].k > 80.0 { 1 } else { 0 })
       + (if cci20[0] > 100.0 { 1 } else { 0 });
if os >= 2 { entry = true; }
if ob >= 2 { exit  = true; }
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
let s50 = ind.sma(50);
let s200 = ind.sma(200);
let mh = ind.macd(12, buf=1);
if cross_above(s50, s200) && mh[0].histogram > 0.0 { entry = true; }
if cross_below(s50, s200) || mh[0].histogram < 0.0 { exit = true; }
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
if gm[1].bullish < 0.5 && gm[0].bullish >= 0.5 { entry = true; }
if gm[1].bullish >= 0.5 && gm[0].bullish < 0.5 { exit = true; }
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
    ]
}

// ── The big parity test ──────────────────────────────────────────────────────

#[test]
fn named_vs_script_real_data_parity() {
    let Some(bars) = load_btcusdt_m1() else { return };

    let rows = translation_rows();
    let mut passed: Vec<String> = Vec::new();
    let mut failures: Vec<(String, String)> = Vec::new();

    for (name, params, script) in rows.iter() {
        let named = run_named(name, params, &bars);
        let script_sigs = run_script(script, &bars);
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
        "kama", "kdj", "keltner_breakout", "kitchen_sink", "lsma_cross",
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

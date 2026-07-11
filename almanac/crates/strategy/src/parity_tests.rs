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

use alm_core::{Bar, signal::Direction};
use serde_json::{json, Value};

use crate::factory::build_strategy;



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
        ("ma_crossover", json!({"fast": 20, "slow": 50}), crate::named::trend::ma_crossover::RHAI_SCRIPT),
        ("triple_ema", json!({"ema1": 10, "ema2": 20, "ema3": 50}), crate::named::trend::triple_ema::RHAI_SCRIPT),
        ("hma_crossover", json!({"fast": 16, "slow": 49}), crate::named::trend::hma_crossover::RHAI_SCRIPT),
        ("dema_crossover", json!({"fast": 12, "slow": 26}), crate::named::trend::dema_crossover::RHAI_SCRIPT),
        ("tema_crossover", json!({"fast": 8, "slow": 21}), crate::named::trend::tema_crossover::RHAI_SCRIPT),
        ("alma_cross", json!({"fast": 9, "slow": 21}), crate::named::trend::alma_cross::RHAI_SCRIPT),
        ("lsma_cross", json!({"fast": 20, "slow": 50}), crate::named::trend::lsma_cross::RHAI_SCRIPT),

        // ── RSI ──────────────────────────────────────────────────────────────
        ("rsi_mean_rev", json!({"period": 14, "oversold": 30.0, "overbought": 70.0}), crate::named::momentum::rsi_mean_rev::RHAI_SCRIPT),
        ("rsi_ma_cross", json!({"fast": 20, "slow": 50, "rsi_period": 14, "rsi_entry": 50.0, "rsi_exit": 45.0}), crate::named::momentum::rsi_ma_cross::RHAI_SCRIPT),

        // ── MACD ─────────────────────────────────────────────────────────────
        ("macd_crossover", json!({"fast": 12, "slow": 26, "signal": 9}), crate::named::momentum::macd_crossover::RHAI_SCRIPT),
        ("macd_ma", json!({"fast": 12, "slow": 26, "signal": 9, "ma": 50}), crate::named::momentum::macd_ma::RHAI_SCRIPT),
        ("ppo_histogram", json!({"fast": 12, "slow": 26, "signal": 9}), crate::named::momentum::ppo_histogram::RHAI),

        // ── Stochastic / StochRSI / KDJ ──────────────────────────────────────
        ("stochastic_crossover", json!({"k_period": 14, "d_period": 3, "oversold": 20.0, "overbought": 80.0}), crate::named::momentum::stochastic::RHAI_SCRIPT),
        ("stoch_rsi", json!({"period": 14, "smooth_d": 3, "oversold": 0.2, "overbought": 0.8}), crate::named::momentum::stoch_rsi_strategy::RHAI_SCRIPT),
        ("kdj", json!({"period": 9, "k_period": 3, "d_period": 3, "oversold": 20.0, "overbought": 80.0}), crate::named::momentum::kdj_strategy::RHAI_SCRIPT),

        // ── ADX / DMI / Aroon / Vortex / RWI ─────────────────────────────────
        ("adx_ema_cross", json!({"fast": 20, "slow": 50, "adx_period": 14, "adx_threshold": 25.0}), crate::named::trend::adx_ema_cross::RHAI_SCRIPT),
        ("aroon_trend", json!({"period": 25, "bull_threshold": 70.0, "bear_threshold": 30.0}), crate::named::trend::aroon_strategy::RHAI_SCRIPT),
        ("vortex_trend", json!({"period": 14}), crate::named::trend::vortex_trend::RHAI_SCRIPT),

        // ── Momentum / oscillators ───────────────────────────────────────────
        ("cci_reversal", json!({"period": 20, "entry_level": -100.0, "exit_level": 100.0}), crate::named::momentum::cci_reversal::RHAI_SCRIPT),
        ("cmo_zero_cross", json!({"cmo_period": 14, "ema_period": 50}), crate::named::momentum::cmo_zero_cross::RHAI_SCRIPT),
        ("fisher_crossover", json!({"period": 10}), crate::named::momentum::fisher_crossover::RHAI_SCRIPT),
        ("roc", json!({"period": 10}), crate::named::momentum::roc_strategy::RHAI_SCRIPT),
        ("kst", json!({"period": 10}), crate::named::momentum::roc_strategy::RHAI_SCRIPT),
        ("trix", json!({"period": 18, "signal": 9}), crate::named::momentum::trix_strategy::RHAI_SCRIPT),
        ("tsi", json!({"first": 25, "second": 13, "entry_threshold": -25.0, "exit_threshold": 25.0}), crate::named::momentum::tsi_strategy::RHAI_SCRIPT),
        ("uo_reversal", json!({"fast": 7, "medium": 14, "slow": 28, "oversold": 30.0, "overbought": 70.0}), crate::named::momentum::uo_reversal::RHAI_SCRIPT),
        ("connors_rsi", json!({"rsi_period": 3, "streak_period": 2, "rank_period": 100, "oversold": 10.0, "overbought": 70.0}), crate::named::momentum::connors_rsi_strategy::RHAI_SCRIPT),
        ("ao", json!({"fast": 5, "slow": 34}), crate::named::momentum::ao_strategy::RHAI_SCRIPT),

        // ── Volume / VWAP / OBV / MFI / CMF ──────────────────────────────────
        ("mfi_trend", json!({"period": 14, "bull_threshold": 50.0, "bear_threshold": 40.0}), crate::named::volume::mfi_trend::RHAI_SCRIPT),
        ("mfi_revert", json!({"period": 14, "oversold": 20.0, "overbought": 80.0}), crate::named::volume::mfi_revert::RHAI_SCRIPT),
        ("cmf_ema_trend", json!({"cmf_period": 20, "ema_period": 50, "bull_threshold": 0.1, "bear_threshold": 0.1}), crate::named::volume::cmf_ema_trend::RHAI),
        ("vwma_rsi", json!({"vwma_period": 20, "rsi_period": 14, "rsi_entry": 50.0, "rsi_exit": 45.0}), crate::named::volume::vwma_rsi::RHAI_SCRIPT),

        // ── Trend filters (CCI/Stoch + ADX, RSI + EMA) ───────────────────────
        ("chop_filter", json!({"chop_period": 14, "fast_ema": 8, "slow_ema": 21, "chop_threshold": 61.8}), crate::named::composite::chop_filter_strategy::RHAI_SCRIPT),

        // ── Donchian / Keltner / BB Reversal ─────────────────────────────────
        ("donchian_breakout", json!({"entry": 20, "exit": 10}), crate::named::volatility::donchian_breakout::RHAI_SCRIPT),
        ("keltner_breakout", json!({"period": 20, "atr_period": 10, "multiplier": 2.0}), crate::named::volatility::keltner_breakout::RHAI_SCRIPT),
        ("bb_rsi_reversal", json!({"bb_period": 20, "bb_std": 2.0, "rsi_period": 14, "oversold": 35.0, "overbought": 65.0}), crate::named::volatility::bb_rsi_reversal::RHAI_SCRIPT),
        ("atr_trailing", json!({"ema_period": 20, "atr_period": 14, "atr_multiplier": 2.0}), crate::named::volatility::atr_trailing::RHAI_SCRIPT),
        ("chandelier_exit", json!({"period": 22, "multiplier": 3.0}), crate::named::volatility::chandelier_exit::RHAI_SCRIPT),
        ("bb_squeeze", json!({"period": 20, "std": 2.0}), crate::named::volatility::bb_squeeze::RHAI_SCRIPT),
        ("mean_reversion", json!({"bb_period": 20, "bb_std": 2.0, "rsi_period": 14, "bars": 4}), crate::named::composite::mean_reversion::RHAI_SCRIPT),
        ("volatility_squeezer", json!({"atr_period": 14, "ma_period": 50}), crate::named::volatility::volatility_squeezer::RHAI_SCRIPT),
        ("volatility_vanguard", json!({"bb_period": 20, "bb_std": 2.0, "atr_period": 14}), crate::named::volatility::volatility_vanguard::RHAI_SCRIPT),
        ("highest_breakout", json!({"period": 20}), crate::named::volatility::highest_breakout::RHAI_SCRIPT),
        ("bb_keltner_squeeze", json!({"bb_period": 20, "bb_std": 2.0, "kc_period": 20, "kc_atr": 10, "kc_mult": 1.5}), crate::named::volatility::bb_keltner_squeeze::RHAI_SCRIPT),

        // ── KAMA / SAR / SuperTrend ──────────────────────────────────────────
        ("kama", json!({"er_period": 10, "fast": 2, "slow": 30}), crate::named::trend::kama_strategy::RHAI_SCRIPT),
        ("parabolic_sar", json!({"step": 0.02, "max": 0.2}), crate::named::pattern::sar_strategy::RHAI_SCRIPT),
        ("supertrend", json!({"period": 10, "multiplier": 3.0}), crate::named::pattern::supertrend_strategy::RHAI_SCRIPT),
        // ── Heiken Ashi ──────────────────────────────────────────────────────
        ("heiken_ashi_color", json!({"smooth": 1}), crate::named::pattern::heiken_ashi_color::RHAI_SCRIPT),
        ("heiken_ashi_breakout", json!({"smooth": 1, "consecutive_bars": 2}), crate::named::pattern::heiken_ashi_breakout::RHAI_SCRIPT),

        // ── Momentum ROC variants ────────────────────────────────────────────
        ("momentum_roc", json!({"roc_period": 10, "ema_period": 50, "entry_threshold": 2.0, "exit_threshold": 0.0}), crate::named::momentum::momentum_roc::RHAI_SCRIPT),
        ("dual_momentum", json!({"fast": 10, "slow": 30}), crate::named::momentum::dual_momentum::RHAI_SCRIPT),

        // ── Williams %R + EMA ────────────────────────────────────────────────
        ("williams_r_ma", json!({"wr_period": 14, "ema_period": 50, "oversold": -80.0, "overbought": -20.0}), crate::named::momentum::williams_r_ma::RHAI),

        // ── DMI / Wolfstein / TrendTransition / TrendFollower ────────────────
        ("dmi_adx", json!({"period": 14, "adx_threshold": 25.0}), crate::named::trend::dmi_adx::RHAI),
        ("wolfstein", json!({"adx_period": 14, "long_threshold": 27.5, "short_threshold": 20.5}), crate::named::composite::wolfstein::RHAI),
        ("swing_trader", json!({"cci_period": 20, "adx_period": 14, "adx_threshold": 25.0}), crate::named::composite::swing_trader::RHAI),

        // ── Bollinger MACD / Volatility ──────────────────────────────────────
        ("bollinger_macd", json!({"bb_period": 20, "bb_std": 2.0, "fast": 12, "slow": 26, "signal": 9}), crate::named::volatility::bollinger_macd::RHAI_SCRIPT),

        // ── Equilibrium / Range / Reversal / Oscillator combos ───────────────
        ("equilibrium_explorer", json!({"ema_period": 200, "stoch_k": 14, "stoch_d": 3, "stoch_oversold": 20.0, "stoch_overbought": 80.0, "macd_fast": 12, "macd_slow": 26, "macd_signal": 9}), crate::named::composite::equilibrium_explorer::RHAI_SCRIPT),
        ("range_rover", json!({"k": 14, "d": 3, "ma": 50, "oversold": 20.0, "overbought": 80.0}), crate::named::volatility::range_rover::RHAI_SCRIPT),
        ("reversal_catcher", json!({"k": 14, "d": 3, "rsi_period": 14}), crate::named::composite::reversal_catcher::RHAI_SCRIPT),

        // ── Trend Transition / Follower (slow MA cross + filters) ────────────
        ("trend_transition", json!({"fast": 50, "slow": 200, "adx_period": 14, "adx_threshold": 25.0}), crate::named::trend::trend_transition::RHAI),

        ("trend_follower", json!({"fast_ma": 50, "slow_ma": 200, "macd_fast": 12, "macd_slow": 26, "macd_signal": 9}), crate::named::trend::trend_follower::RHAI_SCRIPT),

        // ── Multi: SuperTrend + MACD ─────────────────────────────────────────
        ("supertrend_macd", json!({"period": 10, "multiplier": 3.0, "macd_fast": 12, "macd_slow": 26, "macd_signal": 9}), crate::named::pattern::supertrend_macd::RHAI_SCRIPT),

        // ── Stochastic DK / RWI / Alligator / Volatility ratio ───────────────
        ("stochastic_dk", json!({"k": 14, "d": 3}), crate::named::momentum::stochastic_dk::RHAI),
        ("rwi", json!({"period": 14, "threshold": 1.0}), crate::named::pattern::rwi_strategy::RHAI),
        ("alligator", json!({"jaw": 13, "teeth": 8, "lips": 5}), crate::named::pattern::alligator_strategy::RHAI),
        ("volatility_ratio", json!({"lookback": 10, "threshold": 0.5}), crate::named::volatility::volatility_ratio_breakout::RHAI_SCRIPT),

        // ── GMMA ─────────────────────────────────────────────────────────────
        ("gmma_crossover", json!({}), crate::named::trend::gmma_crossover::RHAI_SCRIPT),

        // ── SMI Reversal ─────────────────────────────────────────────────────
        ("smi_reversal", json!({"period": 13, "smooth1": 25, "smooth2": 2, "signal_period": 9, "oversold": -40.0, "overbought": 40.0}), crate::named::momentum::smi_reversal::RHAI),

        // ── Ichimoku ─────────────────────────────────────────────────────────
        ("ichimoku_cloud", json!({"tenkan": 9, "kijun": 26, "senkou_b": 52}), crate::named::trend::ichimoku_cloud::RHAI_SCRIPT),
        ("ichimoku_cross", json!({"tenkan": 9, "kijun": 26, "senkou_b": 52}), crate::named::trend::ichimoku_cross::RHAI_SCRIPT),
        ("elder_ray", json!({"period": 13}), crate::named::pattern::elder_ray_strategy::RHAI_SCRIPT),
        ("pixel_3", json!({}), crate::named::composite::pixel_3::RHAI_SCRIPT),
        ("ma_pullback", json!({"ma_period": 50}), crate::named::trend::ma_pullback::RHAI_SCRIPT),
        ("waddah_attar", json!({"fast": 20, "slow": 40, "bb_period": 20, "bb_std": 2.0}), crate::named::volume::waddah_attar::RHAI_SCRIPT),
        ("oscillator_overlord", json!({"rsi_period": 14, "stoch_k": 14, "stoch_d": 3, "cci_period": 20}), crate::named::composite::oscillator_overlord::RHAI_SCRIPT),
        ("heiken_ashi_harmonizer", json!({"smooth": 1, "ema_period": 50}), crate::named::pattern::heiken_ashi_harmonizer::RHAI_SCRIPT),
        ("scalping_ema", json!({"fast": 8, "slow": 21, "atr_period": 14, "atr_ma_period": 20}), crate::named::trend::scalping_ema::RHAI_SCRIPT),
        ("obv_ema_trend", json!({"obv_ema_period": 20, "price_ema_period": 50}), crate::named::volume::obv_ema_trend::RHAI_SCRIPT),
        ("vwap_bounce", json!({"rsi_period": 14, "oversold": 40.0, "overbought": 65.0, "session_gap_mins": 60}), crate::named::volume::vwap_bounce::RHAI_SCRIPT),
        ("vwap_trend", json!({"session_gap_mins": 60}), crate::named::volume::vwap_trend::RHAI_SCRIPT),
    ]
}

// ── The big parity test ──────────────────────────────────────────────────────

#[test]
fn named_vs_script_real_data_parity() {
    let Some(bars) = crate::test_utils::load_real_bars() else { return };

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
        "kama", "kdj", "keltner_breakout", "kst", "lsma_cross",
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
             Add the strategy to translation_rows() in parity_tests \
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
edit `translation_rows()` in `parity_tests` and regenerate.

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

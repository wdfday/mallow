//! Parity tests for multi-timeframe (MTF), Heiken Ashi candle transforms,
//! TP/SL exits
//!
//! All tests are run on real BTCUSDT M1 + H1 parquet data.

#![cfg(test)]

use std::collections::{BTreeMap, HashMap, VecDeque};
use std::path::PathBuf;

use alm_core::{Bar, Strategy};
use alm_core::{MtfSnapshot, MtfStrategy, TfBarEvent, TfView, Timeframe};
use alm_core::signal::Direction;

use crate::named::{MtfEmaRsiStrategy, MtfMaCrossStrategy};
use crate::script::v2::MtfScriptStrategy;
use crate::test_utils::{load_real_bars, run, assert_parity};

// ── Helpers ──────────────────────────────────────────────────────────────────

/// Thin simulation loop: merge M1 + H1 bars by close_ts, build MtfSnapshot per
/// tick, call `strategy.on_bars`, and collect `(timestamp, direction)` pairs.
fn run_mtf_sigs(
    strategy: &mut dyn MtfStrategy,
    base_tf: Timeframe,
    m1_bars: &[Bar],
    h1_bars: &[Bar],
) -> Vec<(i64, Direction)> {
    let mut by_ts: BTreeMap<i64, Vec<(Timeframe, Bar)>> = BTreeMap::new();
    for b in m1_bars {
        by_ts
            .entry(b.timestamp + base_tf.duration_ms())
            .or_default()
            .push((base_tf, b.clone()));
    }
    for b in h1_bars {
        by_ts
            .entry(b.timestamp + Timeframe::H1.duration_ms())
            .or_default()
            .push((Timeframe::H1, b.clone()));
    }

    let mut confirmed: HashMap<Timeframe, VecDeque<Bar>> = HashMap::new();
    let mut out: Vec<(i64, Direction)> = Vec::new();

    for (&close_ts, tick) in &by_ts {
        for (tf, b) in tick {
            confirmed.entry(*tf).or_default().push_back(b.clone());
        }

        let events: Vec<TfBarEvent<'_>> = tick
            .iter()
            .map(|(tf, b)| TfBarEvent { tf: *tf, bar: b })
            .collect();

        let views: HashMap<Timeframe, TfView<'_>> = confirmed
            .iter()
            .map(|(tf, w)| (*tf, TfView { tf: *tf, confirmed: w }))
            .collect();

        let snap = MtfSnapshot { base_tf, close_ts, events: &events, views: &views };

        for sig in strategy.on_bars(snap) {
            out.push((sig.timestamp, sig.direction));
        }
    }
    out
}

fn assert_mtf_parity(label: &str, named: &[(i64, Direction)], script: &[(i64, Direction)]) {
    assert_eq!(
        named, script,
        "{label}: signal mismatch\n  named : {named:?}\n  script: {script:?}"
    );
}

fn count_dirs(sigs: &[(i64, Direction)]) -> (usize, usize) {
    let entries = sigs.iter().filter(|(_, d)| matches!(d, Direction::Long | Direction::Short)).count();
    let exits   = sigs.iter().filter(|(_, d)| matches!(d, Direction::Exit)).count();
    (entries, exits)
}

// ── Tests ────────────────────────────────────────────────────────────────────

#[test]
fn mtf_reset_parity_script() {
    let Some((m1, h1)) = crate::test_utils::load_real_m1_h1() else { return };

    let script = r#"
let h1_rsi = ind.rsi(5, "H1");
if h1_rsi[0] < 35.0 { entry = true; }
if h1_rsi[0] > 65.0 { exit  = true; }
"#;
    let mut strat = MtfScriptStrategy::from_script(script, Timeframe::M1).unwrap();

    let r1 = run_mtf_sigs(&mut strat, Timeframe::M1, &m1, &h1);
    strat.reset();
    let r2 = run_mtf_sigs(&mut strat, Timeframe::M1, &m1, &h1);

    assert!(!r1.is_empty(), "mtf_reset_script: expected signals");
    assert_parity("mtf script reset: run1 vs run2", &r1, &r2);
}

#[test]
fn mtf_mixed_tf_reset_parity() {
    let Some((m1, h1)) = crate::test_utils::load_real_m1_h1() else { return };

    let script = r#"
let h1_rsi = ind.rsi(5, "H1");
let m1_rsi = ind.rsi(5);
if h1_rsi[0] > 50.0 && m1_rsi[0] < 35.0 { entry = true; }
if h1_rsi[0] < 50.0 || m1_rsi[0] > 65.0 { exit  = true; }
"#;
    let mut strat = MtfScriptStrategy::from_script(script, Timeframe::M1).unwrap();

    let r1 = run_mtf_sigs(&mut strat, Timeframe::M1, &m1, &h1);
    strat.reset();
    let r2 = run_mtf_sigs(&mut strat, Timeframe::M1, &m1, &h1);

    assert_parity("mtf mixed TF reset: run1 vs run2", &r1, &r2);
}

#[test]
fn ha_color_reset_parity() {
    use crate::named::heiken_ashi_color::HaColor;

    let Some(bars) = load_real_bars() else { return };

    let mut named = HaColor::new(1);
    let r1 = run(&mut named, &bars);
    named.reset();
    let r2 = run(&mut named, &bars);
    assert_parity("HaColor named reset", &r1, &r2);
}

// ── Sanity: no signals before H1 warmup ──────────────────────────────────────

#[test]
fn mtf_v2_no_signals_before_h1_warmup() {
    let Some((m1, h1)) = crate::test_utils::load_real_m1_h1() else { return };
    let m1_bars: Vec<Bar> = m1.into_iter().take(60).collect();
    let h1_bars = vec![h1[0].clone()];

    let mut s1 = MtfEmaRsiStrategy::new();
    assert!(
        run_mtf_sigs(&mut s1, Timeframe::M1, &m1_bars, &h1_bars).is_empty(),
        "EmaRsi: must not fire with 1 H1 bar"
    );

    let mut s2 = MtfMaCrossStrategy::new();
    assert!(
        run_mtf_sigs(&mut s2, Timeframe::M1, &m1_bars, &h1_bars).is_empty(),
        "MaCross: must not fire with 1 H1 bar"
    );
}

#[test]
fn debug_run_mtf_sigs_produces_signals() {
    let Some((m1, h1)) = crate::test_utils::load_real_m1_h1() else { return };
    let mut named = MtfEmaRsiStrategy::new();
    let sigs = run_mtf_sigs(&mut named, Timeframe::M1, &m1, &h1);
    assert!(
        !sigs.is_empty(),
        "debug: expected signals, got none"
    );
}

// ── Real-data MTF parity tests (BTCUSDT M1 + H1 parquet) ─────────────────────



// ── Table-driven MTF Parity ──────────────────────────────────────────────────

fn mtf_translation_rows() -> Vec<(&'static str, serde_json::Value, &'static str)> {
    use serde_json::json;
    vec![
        (
            "kitchen_sink",
            json!({"symbol": "BTCUSDT"}),
            crate::named::kitchen_sink::RHAI_SCRIPT,
        ),
        (
            "mtf_ema_rsi",
            json!({}),
            crate::named::mtf_ema_rsi::RHAI_SCRIPT,
        ),
        (
            "mtf_ma_cross",
            json!({}),
            crate::named::mtf_ma_cross::RHAI_SCRIPT,
        ),
        (
            "mtf_adx_pullback",
            json!({}),
            crate::named::mtf_adx_pullback::RHAI_SCRIPT,
        ),
        (
            "mtf_bb_macd",
            json!({}),
            crate::named::mtf_bb_macd::RHAI_SCRIPT,
        ),
    ]
}

#[test]
fn mtf_named_vs_script_real_data_parity() {
    let Some((m1, h1)) = crate::test_utils::load_real_m1_h1() else { return };
    let rows = mtf_translation_rows();

    for (name, params, script) in &rows {
        let mut named = crate::mtf_factory::build_mtf_strategy(name, params)
            .unwrap_or_else(|e| panic!("build_mtf_strategy({name}) failed: {e}"));
        let named_sigs = run_mtf_sigs(named.as_mut(), Timeframe::M1, &m1, &h1);

        let mut script_strat = MtfScriptStrategy::from_script(script, Timeframe::M1).unwrap();
        let script_sigs = run_mtf_sigs(&mut script_strat, Timeframe::M1, &m1, &h1);

        let (en, ex) = count_dirs(&named_sigs);
        eprintln!("[mtf-parity] {name}: total={} entry={} exit={}", named_sigs.len(), en, ex);
        assert!(!named_sigs.is_empty(), "mtf_v2_{name}: named must produce signals");
        assert_mtf_parity(&format!("Mtf{name} named vs script"), &named_sigs, &script_sigs);
    }
}

#[test]
fn mtf_named_reset_parity() {
    let Some((m1, h1)) = crate::test_utils::load_real_m1_h1() else { return };
    let rows = mtf_translation_rows();

    for (name, params, _) in &rows {
        let mut named = crate::mtf_factory::build_mtf_strategy(name, params)
            .unwrap_or_else(|e| panic!("build_mtf_strategy({name}) failed: {e}"));
        let r1 = run_mtf_sigs(named.as_mut(), Timeframe::M1, &m1, &h1);
        named.reset();
        let r2 = run_mtf_sigs(named.as_mut(), Timeframe::M1, &m1, &h1);
        assert!(!r1.is_empty(), "mtf_v2_{name}_reset: must produce signals");
        assert_mtf_parity(&format!("Mtf{name} named reset parity"), &r1, &r2);
    }
}

#[test]
fn mtf_script_reset_parity() {
    let Some((m1, h1)) = crate::test_utils::load_real_m1_h1() else { return };
    let rows = mtf_translation_rows();

    for (name, _, script) in &rows {
        let mut strat = MtfScriptStrategy::from_script(script, Timeframe::M1).unwrap();
        let r1 = run_mtf_sigs(&mut strat, Timeframe::M1, &m1, &h1);
        strat.reset();
        let r2 = run_mtf_sigs(&mut strat, Timeframe::M1, &m1, &h1);
        assert!(!r1.is_empty(), "mtf_v2_{name}_script_reset: must produce signals");
        assert_mtf_parity(&format!("Mtf{name} script reset"), &r1, &r2);
    }
}

#[test]
fn test_all_mtf_strategies_covered_by_parity() {
    use crate::mtf_factory::MTF_STRATEGY_KEYS;
    let covered: std::collections::HashSet<&str> =
        mtf_translation_rows().iter().map(|(n, _, _)| *n).collect();

    let mut missing = Vec::new();
    for name in MTF_STRATEGY_KEYS {
        if !covered.contains(name) {
            missing.push(*name);
        }
    }
    assert!(
        missing.is_empty(),
        "MTF coverage gap: these MTF strategies are not covered in mtf_translation_rows: {:?}",
        missing
    );
}

// ── Rhai export ─────────────────────────────────────────────────────────────────
//
// Dump every named MTF strategy's parity-tested script equivalent to
// `almanac/scripts/examples/mtf/<name>.rhai` (+ a README listing them).
//
//   cargo test -p alm-strategy dump_mtf_rhai_examples -- --ignored --nocapture
#[test]
#[ignore = "generator: writes .rhai files, run explicitly"]
fn dump_mtf_rhai_examples() {
    use std::fs;

    let dir = PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("../../scripts/examples/mtf");

    fs::create_dir_all(&dir).expect("create mtf/ dir");

    let rows = mtf_translation_rows();
    for (name, params, script) in &rows {
        let header = format!(
"// ─────────────────────────────────────────────────────────────────────────────
// Named MTF strategy: {name}
// Default params: {params}
//
// Rhai equivalent — parity-tested vs `build_mtf_strategy(\"{name}\")` on real BTCUSDT M1+H1
// (mtf_named_vs_script_real_data_parity). Auto-generated; regenerate with:
//   cargo test -p alm-strategy dump_mtf_rhai_examples -- --ignored
// ─────────────────────────────────────────────────────────────────────────────
",
            name = name,
            params = serde_json::to_string(params).unwrap(),
        );
        let body = script.trim_start_matches('\n').trim_end();
        fs::write(dir.join(format!("{name}.rhai")), format!("{header}\n{body}\n"))
            .unwrap_or_else(|e| panic!("write {name}.rhai: {e}"));
    }

    // README: index of translatable MTF scripts
    let mut readme = String::from(
"# Named MTF strategies → Rhai

Auto-generated by `cargo test -p alm-strategy dump_mtf_rhai_examples -- --ignored`.
Each `<name>.rhai` is the script asserted byte-for-signal equal to the named Rust MTF
strategy `build_mtf_strategy(\"<name>\")` on real BTCUSDT M1 + H1 data.

## Translatable (parity-tested)

");
    let mut names: Vec<&str> = rows.iter().map(|(n, _, _)| *n).collect();
    names.sort_unstable();
    for n in &names {
        readme.push_str(&format!("- `{n}.rhai`\n"));
    }
    fs::write(dir.join("README.md"), readme).expect("write README");

    eprintln!(
        "[export] wrote {} MTF .rhai files + README → {}",
        rows.len(), dir.display()
    );
}

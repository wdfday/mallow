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
use alm_data::{BarFeed, ParquetFeed};

use crate::named::{MtfAdxPullbackStrategy, MtfBbMacdStrategy, MtfEmaRsiStrategy, MtfMaCrossStrategy};
use crate::script::v2::MtfScriptStrategy;
use crate::test_utils::{load_real_bars, run, assert_parity};

// ── Helpers ──────────────────────────────────────────────────────────────────

/// Helper: aggregate M1 bars into H1 by calendar bucket (open of first, close
/// of last).
fn aggregate_m1_to_h1(m1: &[Bar]) -> Vec<Bar> {
    let h1_ms = Timeframe::H1.duration_ms();
    let mut buckets: BTreeMap<i64, Vec<&Bar>> = BTreeMap::new();
    for b in m1 {
        let key = (b.timestamp / h1_ms) * h1_ms;
        buckets.entry(key).or_default().push(b);
    }
    buckets
        .into_iter()
        .map(|(ts, bars)| {
            let o = bars.first().unwrap().open;
            let c = bars.last().unwrap().close;
            let h = bars.iter().map(|b| b.high).fold(f64::MIN, f64::max);
            let l = bars.iter().map(|b| b.low).fold(f64::MAX, f64::min);
            let v: f64 = bars.iter().map(|b| b.volume).sum();
            Bar::new(ts, "BTCUSDT", o, h, l, c, v)
        })
        .collect()
}

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
    let Some((m1, h1)) = load_btcusdt_m1_h1() else { return };

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
    let Some((m1, h1)) = load_btcusdt_m1_h1() else { return };

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
    use crate::named::heiken_ashi_strategy::HaColor;

    let Some(bars) = load_real_bars() else { return };

    let mut named = HaColor::new(1);
    let r1 = run(&mut named, &bars);
    named.reset();
    let r2 = run(&mut named, &bars);
    assert_parity("HaColor named reset", &r1, &r2);
}

// ── MtfEmaRsi: named ↔ script ─────────────────────────────────────────────────

const SCRIPT_EMA_RSI: &str = r#"
let h1_ema = ind.ema(20, "H1");
let rsi    = ind.rsi(14);
if rising(h1_ema) && rsi[0] < 40.0 { entry = true; }
if falling(h1_ema) || rsi[0] > 70.0 { exit  = true; }
"#;

#[test]
fn mtf_v2_ema_rsi_named_vs_script() {
    let Some((m1, h1)) = load_btcusdt_m1_h1() else { return };

    let mut named = MtfEmaRsiStrategy::new();
    let named_sigs = run_mtf_sigs(&mut named, Timeframe::M1, &m1, &h1);

    let mut strat = MtfScriptStrategy::from_script(SCRIPT_EMA_RSI, Timeframe::M1).unwrap();
    let script_sigs = run_mtf_sigs(&mut strat, Timeframe::M1, &m1, &h1);

    let (en, ex) = count_dirs(&named_sigs);
    eprintln!("[mtf-parity] ema_rsi: total={} entry={} exit={}", named_sigs.len(), en, ex);
    assert!(!named_sigs.is_empty(), "mtf_v2_ema_rsi: named must produce signals");
    assert_mtf_parity("MtfEmaRsi named vs script", &named_sigs, &script_sigs);
}

#[test]
fn mtf_v2_ema_rsi_named_reset_parity() {
    let Some((m1, h1)) = load_btcusdt_m1_h1() else { return };
    let mut named = MtfEmaRsiStrategy::new();
    let r1 = run_mtf_sigs(&mut named, Timeframe::M1, &m1, &h1);
    named.reset();
    let r2 = run_mtf_sigs(&mut named, Timeframe::M1, &m1, &h1);
    assert!(!r1.is_empty(), "mtf_v2_ema_rsi_reset: must produce signals");
    assert_mtf_parity("MtfEmaRsi reset: run1 vs run2", &r1, &r2);
}

#[test]
fn mtf_v2_ema_rsi_script_reset_parity() {
    let Some((m1, h1)) = load_btcusdt_m1_h1() else { return };
    let mut strat = MtfScriptStrategy::from_script(SCRIPT_EMA_RSI, Timeframe::M1).unwrap();
    let r1 = run_mtf_sigs(&mut strat, Timeframe::M1, &m1, &h1);
    strat.reset();
    let r2 = run_mtf_sigs(&mut strat, Timeframe::M1, &m1, &h1);
    assert!(!r1.is_empty(), "mtf_v2_ema_rsi_script_reset: must produce signals");
    assert_mtf_parity("MtfEmaRsi script reset", &r1, &r2);
}

// ── MtfMaCross: named ↔ script ────────────────────────────────────────────────

const SCRIPT_MA_CROSS: &str = r#"
let h1_trend = ind.ema(50, "H1");
let ema9     = ind.ema(9);
let ema21    = ind.ema(21);
if close[0] > h1_trend[0] && cross_above(ema9, ema21) { entry = true; }
if close[0] < h1_trend[0] || cross_below(ema9, ema21) { exit  = true; }
"#;

#[test]
fn mtf_v2_ma_cross_named_vs_script() {
    let Some((m1, h1)) = load_btcusdt_m1_h1() else { return };

    let mut named = MtfMaCrossStrategy::new();
    let named_sigs = run_mtf_sigs(&mut named, Timeframe::M1, &m1, &h1);

    let mut strat = MtfScriptStrategy::from_script(SCRIPT_MA_CROSS, Timeframe::M1).unwrap();
    let script_sigs = run_mtf_sigs(&mut strat, Timeframe::M1, &m1, &h1);

    let (en, ex) = count_dirs(&named_sigs);
    eprintln!("[mtf-parity] ma_cross: total={} entry={} exit={}", named_sigs.len(), en, ex);
    assert!(!named_sigs.is_empty(), "mtf_v2_ma_cross: named must produce signals");
    assert_mtf_parity("MtfMaCross named vs script", &named_sigs, &script_sigs);
}

#[test]
fn mtf_v2_ma_cross_named_reset_parity() {
    let Some((m1, h1)) = load_btcusdt_m1_h1() else { return };
    let mut named = MtfMaCrossStrategy::new();
    let r1 = run_mtf_sigs(&mut named, Timeframe::M1, &m1, &h1);
    named.reset();
    let r2 = run_mtf_sigs(&mut named, Timeframe::M1, &m1, &h1);
    assert!(!r1.is_empty(), "mtf_v2_ma_cross_reset: must produce signals");
    assert_mtf_parity("MtfMaCross reset: run1 vs run2", &r1, &r2);
}

#[test]
fn mtf_v2_ma_cross_script_reset_parity() {
    let Some((m1, h1)) = load_btcusdt_m1_h1() else { return };
    let mut strat = MtfScriptStrategy::from_script(SCRIPT_MA_CROSS, Timeframe::M1).unwrap();
    let r1 = run_mtf_sigs(&mut strat, Timeframe::M1, &m1, &h1);
    strat.reset();
    let r2 = run_mtf_sigs(&mut strat, Timeframe::M1, &m1, &h1);
    assert!(!r1.is_empty(), "mtf_v2_ma_cross_script_reset: must produce signals");
    assert_mtf_parity("MtfMaCross script reset", &r1, &r2);
}

// ── Sanity: no signals before H1 warmup ──────────────────────────────────────

#[test]
fn mtf_v2_no_signals_before_h1_warmup() {
    let Some((m1, h1)) = load_btcusdt_m1_h1() else { return };
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
    let Some((m1, h1)) = load_btcusdt_m1_h1() else { return };
    let mut named = MtfEmaRsiStrategy::new();
    let sigs = run_mtf_sigs(&mut named, Timeframe::M1, &m1, &h1);
    assert!(
        !sigs.is_empty(),
        "debug: expected signals, got none"
    );
}

// ── MtfAdxPullback: named ↔ script ───────────────────────────────────────────

const SCRIPT_ADX_PULLBACK: &str = r#"
let h1_adx   = ind.adx(14, "H1");
let h1_trend = ind.ema(50, "H1");
let rsi      = ind.rsi(14);
if h1_adx[0] > 25.0 && close[0] > h1_trend[0] && rsi[0] < 40.0 { entry = true; }
if h1_adx[0] < 20.0 || close[0] < h1_trend[0] || rsi[0] > 70.0 { exit  = true; }
"#;

#[test]
fn mtf_v2_adx_pullback_named_vs_script() {
    let Some((m1, h1)) = load_btcusdt_m1_h1() else { return };

    let mut named = MtfAdxPullbackStrategy::new();
    let named_sigs = run_mtf_sigs(&mut named, Timeframe::M1, &m1, &h1);

    let mut strat = MtfScriptStrategy::from_script(SCRIPT_ADX_PULLBACK, Timeframe::M1).unwrap();
    let script_sigs = run_mtf_sigs(&mut strat, Timeframe::M1, &m1, &h1);

    let (en, ex) = count_dirs(&named_sigs);
    eprintln!("[mtf-parity] adx_pullback: total={} entry={} exit={}", named_sigs.len(), en, ex);
    assert!(!named_sigs.is_empty(), "mtf_v2_adx_pullback: named must produce signals");
    assert_mtf_parity("MtfAdxPullback named vs script", &named_sigs, &script_sigs);
}

#[test]
fn mtf_v2_adx_pullback_named_reset_parity() {
    let Some((m1, h1)) = load_btcusdt_m1_h1() else { return };
    let mut named = MtfAdxPullbackStrategy::new();
    let r1 = run_mtf_sigs(&mut named, Timeframe::M1, &m1, &h1);
    named.reset();
    let r2 = run_mtf_sigs(&mut named, Timeframe::M1, &m1, &h1);
    assert_mtf_parity("MtfAdxPullback named reset parity", &r1, &r2);
}

#[test]
fn mtf_v2_adx_pullback_script_reset_parity() {
    let Some((m1, h1)) = load_btcusdt_m1_h1() else { return };
    let mut strat = MtfScriptStrategy::from_script(SCRIPT_ADX_PULLBACK, Timeframe::M1).unwrap();
    let r1 = run_mtf_sigs(&mut strat, Timeframe::M1, &m1, &h1);
    strat.reset();
    let r2 = run_mtf_sigs(&mut strat, Timeframe::M1, &m1, &h1);
    assert_mtf_parity("MtfAdxPullback script reset parity", &r1, &r2);
}

// ── MtfBbMacd: named ↔ script ─────────────────────────────────────────────────

const SCRIPT_BB_MACD: &str = r#"
let h1_bb = ind.bbands(20, "H1");
let macd  = ind.macd(12);
let rsi   = ind.rsi(14);
if close[0] > h1_bb[0].upper && macd[0].histogram > 0.0 && rsi[0] < 55.0 { entry = true; }
if close[0] < h1_bb[0].middle || macd[0].histogram < 0.0 { exit = true; }
"#;

#[test]
fn mtf_v2_bb_macd_named_vs_script() {
    let Some((m1, h1)) = load_btcusdt_m1_h1() else { return };

    let mut named = MtfBbMacdStrategy::new();
    let named_sigs = run_mtf_sigs(&mut named, Timeframe::M1, &m1, &h1);

    let mut strat = MtfScriptStrategy::from_script(SCRIPT_BB_MACD, Timeframe::M1).unwrap();
    let script_sigs = run_mtf_sigs(&mut strat, Timeframe::M1, &m1, &h1);

    let (en, ex) = count_dirs(&named_sigs);
    eprintln!("[mtf-parity] bb_macd: total={} entry={} exit={}", named_sigs.len(), en, ex);
    assert!(!named_sigs.is_empty(), "mtf_v2_bb_macd: named must produce signals");
    assert_mtf_parity("MtfBbMacd named vs script", &named_sigs, &script_sigs);
}

#[test]
fn mtf_v2_bb_macd_named_reset_parity() {
    let Some((m1, h1)) = load_btcusdt_m1_h1() else { return };
    let mut named = MtfBbMacdStrategy::new();
    let r1 = run_mtf_sigs(&mut named, Timeframe::M1, &m1, &h1);
    named.reset();
    let r2 = run_mtf_sigs(&mut named, Timeframe::M1, &m1, &h1);
    assert_mtf_parity("MtfBbMacd named reset parity", &r1, &r2);
}

#[test]
fn mtf_v2_bb_macd_script_reset_parity() {
    let Some((m1, h1)) = load_btcusdt_m1_h1() else { return };
    let mut strat = MtfScriptStrategy::from_script(SCRIPT_BB_MACD, Timeframe::M1).unwrap();
    let r1 = run_mtf_sigs(&mut strat, Timeframe::M1, &m1, &h1);
    strat.reset();
    let r2 = run_mtf_sigs(&mut strat, Timeframe::M1, &m1, &h1);
    assert_mtf_parity("MtfBbMacd script reset parity", &r1, &r2);
}

// ── Real-data MTF parity tests (BTCUSDT M1 + H1 parquet) ─────────────────────

fn testdata_dir() -> PathBuf {
    PathBuf::from(env!("CARGO_MANIFEST_DIR"))
        .parent()
        .unwrap()
        .join("data/testdata/BTCUSDT")
}

fn load_parquet(tf_dir: &str, file: &str) -> Option<Vec<Bar>> {
    let path = testdata_dir().join(tf_dir).join(file);
    if !path.exists() {
        eprintln!("[mtf-parity] testdata missing: {}", path.display());
        return None;
    }
    let mut feed = ParquetFeed::load(&path, "BTCUSDT").ok()?;
    let bars: Vec<Bar> = std::iter::from_fn(|| feed.next()).collect();
    Some(bars)
}

fn load_btcusdt_m1_h1() -> Option<(Vec<Bar>, Vec<Bar>)> {
    let mut m1 = load_parquet("M1", "BTCUSDT_M1_2026-01.parquet")?;
    let h1_all = load_parquet("H1", "BTCUSDT_H1_2026-01.parquet")?;
    m1.truncate(20000);
    if m1.is_empty() || h1_all.is_empty() {
        return None;
    }
    let t_start = m1.first().unwrap().timestamp;
    let t_end = m1.last().unwrap().timestamp;
    let h1: Vec<Bar> = h1_all
        .into_iter()
        .filter(|b| b.timestamp >= t_start && b.timestamp <= t_end)
        .collect();
    Some((m1, h1))
}

#[test]
fn mtf_real_data_ema_rsi_named_vs_script() {
    let Some((m1, h1)) = load_btcusdt_m1_h1() else { return };

    let mut named = MtfEmaRsiStrategy::new();
    let named_sigs = run_mtf_sigs(&mut named, Timeframe::M1, &m1, &h1);

    let mut script = MtfScriptStrategy::from_script(SCRIPT_EMA_RSI, Timeframe::M1).unwrap();
    let script_sigs = run_mtf_sigs(&mut script, Timeframe::M1, &m1, &h1);

    let (en, ex) = count_dirs(&named_sigs);
    eprintln!("[mtf-parity] ema_rsi: total={} entry={} exit={}", named_sigs.len(), en, ex);
    assert!(!named_sigs.is_empty(), "real-data ema_rsi: named must produce signals");
    assert_mtf_parity("MtfEmaRsi real-data named vs script", &named_sigs, &script_sigs);
}

#[test]
fn mtf_real_data_ma_cross_named_vs_script() {
    let Some((m1, h1)) = load_btcusdt_m1_h1() else { return };

    let mut named = MtfMaCrossStrategy::new();
    let named_sigs = run_mtf_sigs(&mut named, Timeframe::M1, &m1, &h1);

    let mut script = MtfScriptStrategy::from_script(SCRIPT_MA_CROSS, Timeframe::M1).unwrap();
    let script_sigs = run_mtf_sigs(&mut script, Timeframe::M1, &m1, &h1);

    let (en, ex) = count_dirs(&named_sigs);
    eprintln!("[mtf-parity] ma_cross: total={} entry={} exit={}", named_sigs.len(), en, ex);
    assert!(!named_sigs.is_empty(), "real-data ma_cross: named must produce signals");
    assert_mtf_parity("MtfMaCross real-data named vs script", &named_sigs, &script_sigs);
}

#[test]
fn mtf_real_data_reset_idempotent() {
    let Some((m1, h1)) = load_btcusdt_m1_h1() else { return };

    let mut named = MtfEmaRsiStrategy::new();
    let r1 = run_mtf_sigs(&mut named, Timeframe::M1, &m1, &h1);
    named.reset();
    let r2 = run_mtf_sigs(&mut named, Timeframe::M1, &m1, &h1);

    assert_mtf_parity("MtfEmaRsi real-data reset idempotent", &r1, &r2);
}

#[test]
fn mtf_real_data_adx_pullback_named_vs_script() {
    let Some((m1, h1)) = load_btcusdt_m1_h1() else { return };

    let mut named = MtfAdxPullbackStrategy::new();
    let named_sigs = run_mtf_sigs(&mut named, Timeframe::M1, &m1, &h1);

    let mut script = MtfScriptStrategy::from_script(SCRIPT_ADX_PULLBACK, Timeframe::M1).unwrap();
    let script_sigs = run_mtf_sigs(&mut script, Timeframe::M1, &m1, &h1);

    let (en, ex) = count_dirs(&named_sigs);
    eprintln!("[mtf-parity] adx_pullback: total={} entry={} exit={}", named_sigs.len(), en, ex);
    assert!(!named_sigs.is_empty(), "real-data adx_pullback: named must produce signals");
    assert_mtf_parity("MtfAdxPullback real-data named vs script", &named_sigs, &script_sigs);
}

#[test]
fn mtf_real_data_bb_macd_named_vs_script() {
    let Some((m1, h1)) = load_btcusdt_m1_h1() else { return };

    let mut named = MtfBbMacdStrategy::new();
    let named_sigs = run_mtf_sigs(&mut named, Timeframe::M1, &m1, &h1);

    let mut script = MtfScriptStrategy::from_script(SCRIPT_BB_MACD, Timeframe::M1).unwrap();
    let script_sigs = run_mtf_sigs(&mut script, Timeframe::M1, &m1, &h1);

    let (en, ex) = count_dirs(&named_sigs);
    eprintln!("[mtf-parity] bb_macd: total={} entry={} exit={}", named_sigs.len(), en, ex);
    assert!(!named_sigs.is_empty(), "real-data bb_macd: named must produce signals");
    assert_mtf_parity("MtfBbMacd real-data named vs script", &named_sigs, &script_sigs);
}

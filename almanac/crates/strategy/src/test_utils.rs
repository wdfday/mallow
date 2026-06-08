//! Shared test utilities for strategy parity tests.
#![cfg(test)]

use alm_core::{bar::Bar, signal::{Direction, Signal}, strategy::Strategy};
use std::path::PathBuf;
use alm_data::{BarFeed, ParquetFeed};

pub fn load_real_bars() -> Option<Vec<Bar>> {
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

pub fn load_real_m1_h1() -> Option<(Vec<Bar>, Vec<Bar>)> {
    let m1 = load_real_bars()?;
    let path_h1 = PathBuf::from(env!("CARGO_MANIFEST_DIR"))
        .parent()
        .unwrap()
        .join("data/testdata/BTCUSDT/H1/BTCUSDT_H1_2026-01.parquet");
    if !path_h1.exists() {
        eprintln!("[parity] testdata missing at {}, skipping", path_h1.display());
        return None;
    }
    let mut feed_h1 = ParquetFeed::load(&path_h1, "BTCUSDT").ok()?;
    let h1_all: Vec<Bar> = std::iter::from_fn(|| feed_h1.next()).collect();
    let t_start = m1.first().unwrap().timestamp;
    let t_end = m1.last().unwrap().timestamp;
    let h1: Vec<Bar> = h1_all
        .into_iter()
        .filter(|b| b.timestamp >= t_start && b.timestamp <= t_end)
        .collect();
    Some((m1, h1))
}

pub fn run(s: &mut dyn Strategy, bars: &[Bar]) -> Vec<(i64, Direction)> {
    bars.iter()
        .flat_map(|b| s.on_bar(b))
        .map(|s| (s.timestamp, s.direction))
        .collect()
}

/// Like `run`, but returns full Signal objects so tp/sl/price can be compared.
pub fn run_signals(s: &mut dyn Strategy, bars: &[Bar]) -> Vec<Signal> {
    bars.iter().flat_map(|b| s.on_bar(b)).collect()
}

pub fn assert_parity(label: &str, a: &[(i64, Direction)], b: &[(i64, Direction)]) {
    assert_eq!(
        a, b,
        "{label}: signal mismatch\n  left : {a:?}\n  right: {b:?}"
    );
}

/// Full signal parity: compares timestamp, direction, price, target_price, stop_price.
pub fn assert_signals_parity(label: &str, a: &[Signal], b: &[Signal]) {
    assert_eq!(a.len(), b.len(),
        "{label}: signal count mismatch — left {}, right {}",
        a.len(), b.len());
    for (i, (sa, sb)) in a.iter().zip(b.iter()).enumerate() {
        assert_eq!(sa.timestamp,    sb.timestamp,    "{label}[{i}]: timestamp mismatch");
        assert_eq!(sa.direction,    sb.direction,    "{label}[{i}]: direction mismatch");
        assert_eq!(sa.price,        sb.price,        "{label}[{i}]: price mismatch");
        assert_eq!(sa.target_price, sb.target_price, "{label}[{i}]: target_price mismatch");
        assert_eq!(sa.stop_price,   sb.stop_price,   "{label}[{i}]: stop_price mismatch");
    }
}

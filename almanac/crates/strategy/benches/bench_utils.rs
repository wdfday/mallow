/// Shared bar generators for benchmarks.

use alm_core::bar::Bar;
use alm_data::{BarFeed, ParquetFeed};
use std::path::Path;

/// Synthetic sinusoidal price data for benchmarking.
pub fn make_bars(n: usize) -> Vec<Bar> {
    (0..n)
        .map(|i| {
            let t = i as f64;
            let price = 100.0 + 30.0 * (t * 0.05).sin();
            Bar::new(i as i64 * 60_000, "TEST", price, price * 1.005, price * 0.995, price, 1_000.0 + t)
        })
        .collect()
}

/// Load real BTC M1 bars from the real mallow/data directory (~5.5M bars),
/// or fall back to the testdata parquet if not found.
/// Returns empty vec if no files are found.
pub fn load_btc_m1_bars() -> Vec<Bar> {
    let real_data_dir = Path::new(env!("CARGO_MANIFEST_DIR"))
        .join("../../../data/BinanceFlat/M1/BTCUSDT");

    let mut files = Vec::new();
    if let Ok(entries) = std::fs::read_dir(&real_data_dir) {
        for entry in entries.filter_map(|e| e.ok()) {
            let path = entry.path();
            if path.extension().map_or(false, |ext| ext == "parquet") {
                files.push(path);
            }
        }
    }
    files.sort();

    let mut feed = if !files.is_empty() {
        let refs: Vec<&Path> = files.iter().map(|p| p.as_path()).collect();
        match ParquetFeed::load_many(&refs, "BTCUSDT") {
            Ok(f) => f,
            Err(_) => return vec![],
        }
    } else {
        // Fallback to testdata
        let testdata_path = Path::new(env!("CARGO_MANIFEST_DIR"))
            .join("../../crates/data/testdata/BTCUSDT/M1/BTCUSDT_M1_2026-01.parquet");
        match ParquetFeed::load(&testdata_path, "BTCUSDT") {
            Ok(f) => f,
            Err(_) => return vec![],
        }
    };

    let mut bars = Vec::new();
    while let Some(b) = feed.next() {
        bars.push(b);
    }
    bars
}

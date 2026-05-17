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

/// Load real BTC M1 bars from the testdata parquet (2022-04 → 2026-04, ~2M bars).
/// Returns empty vec if file not found (bench skips gracefully).
pub fn load_btc_m1_bars() -> Vec<Bar> {
    let path = Path::new(env!("CARGO_MANIFEST_DIR"))
        .join("../../crates/data/testdata/BTCUSDT/M1/BTCUSDT_M1_2022-04-13_to_2026-04-12.parquet");
    let mut feed = match ParquetFeed::load(&path, "BTCUSDT") {
        Ok(f)  => f,
        Err(_) => return vec![],
    };
    let mut bars = Vec::new();
    while let Some(b) = feed.next() { bars.push(b); }
    bars
}

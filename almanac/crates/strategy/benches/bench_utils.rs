//! Shared bar generators for benchmarks.

use alm_core::bar::Bar;

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

/// Trending bars: down→up→down.
pub fn trending_bars(n: usize) -> Vec<Bar> {
    let third = n / 3;
    (0..n)
        .map(|i| {
            let price = if i < third {
                200.0 - i as f64 * 0.5
            } else if i < third * 2 {
                200.0 - third as f64 * 0.5 + (i - third) as f64 * 1.5
            } else {
                200.0 - third as f64 * 0.5 + third as f64 * 1.5
                    - (i - third * 2) as f64 * 2.0
            };
            Bar::new(i as i64 * 60_000, "TEST", price.max(1.0), price.max(1.0)*1.005, price.max(1.0)*0.995, price.max(1.0), 1000.0)
        })
        .collect()
}

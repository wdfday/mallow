/// Cross-framework benchmark: EMA Crossover (fast=10, slow=21)
/// Dataset: 2006-day-001 (255 daily bars)
/// Params: initial equity=10_000, commission=0.1%, slippage=0%
/// Output: JSON for comparison with backtrader / vectorbt results
use anyhow::Result;
use alm_data::ParquetFeed;
use std::path::Path;
use alm_engine::Engine;
use alm_strategy::{FixedFractional, MaCrossover};

fn main() -> Result<()> {
    let path = std::env::args()
        .nth(1)
        .unwrap_or_else(|| "benchmark/data.parquet".to_string());

    let mut feed = ParquetFeed::load(Path::new(&path), "BENCHMARK")?;

    let strategy = MaCrossover::new(10, 21);
    // max_position_pct=1.0 → invest all available equity (matches backtrader behavior)
    let risk = FixedFractional::new(1.0, 1);

    let mut engine = Engine::sync(
        10_000.0, // initial equity
        strategy,
        risk,
        0.001, // 0.1% commission
        0.0,   // no slippage (backtrader default)
    );

    let report = engine.run(&mut feed, 0.0); // risk_free=0

    let final_equity = 10_000.0 * (1.0 + report.total_return_pct / 100.0);

    println!("=== BT-ENGINE: EMA Crossover (10/21) ===");
    println!("Initial equity : {:>10.2}", 10_000.0_f64);
    println!("Final equity   : {:>10.2}", final_equity);
    println!("Total return   : {:>10.2}%", report.total_return_pct);
    println!("Sharpe ratio   : {:>10.4}", report.sharpe_ratio);
    println!("Max drawdown   : {:>10.2}%", report.max_drawdown_pct);
    println!("Total trades   : {:>10}", report.total_trades);
    println!("Win rate       : {:>10.2}%", report.win_rate_pct);

    Ok(())
}

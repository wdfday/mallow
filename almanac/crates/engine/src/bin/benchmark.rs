/// Quick backtest runner — in kết quả ra terminal.
///
/// Run với testdata M1:
///   cargo run --bin benchmark
///
/// Flags:
///   --data PATH    parquet file (default: BTCUSDT M1 testdata)
///   --symbol SYM   symbol name (default: BTCUSDT)
///   --capital N    initial capital (default: 10000)
///   --fast N       MA fast period (default: 20)
///   --slow N       MA slow period (default: 50)
///   --lot N        minimum lot size, 0 = fractional (default: 0)
///   --commission N commission pct (default: 0.001)
use anyhow::Result;
use alm_data::{BarFeed, ParquetFeed};
use alm_engine::Engine;
use alm_engine::PercentEquity;
use alm_strategy::MaCrossover;
use std::path::Path;

struct Args {
    data:       String,
    symbol:     String,
    capital:    f64,
    fast:       usize,
    slow:       usize,
    lot:        f64,
    commission: f64,
}

fn parse_args() -> Args {
    let raw: Vec<String> = std::env::args().collect();
    let get = |flag: &str, default: &str| -> String {
        raw.windows(2)
            .find(|w| w[0] == flag)
            .map(|w| w[1].clone())
            .unwrap_or_else(|| default.to_string())
    };
    Args {
        data: get("--data", ""),
        symbol:     get("--symbol",     "BTCUSDT"),
        capital:    get("--capital",    "10000").parse().unwrap_or(10_000.0),
        fast:       get("--fast",       "20").parse().unwrap_or(20),
        slow:       get("--slow",       "50").parse().unwrap_or(50),
        lot:        get("--lot",        "0").parse().unwrap_or(0.0),
        commission: get("--commission", "0.001").parse().unwrap_or(0.001),
    }
}

fn main() -> Result<()> {
    let args = parse_args();

    let mut feed = if !args.data.is_empty() {
        ParquetFeed::load(Path::new(&args.data), &args.symbol)?
    } else {
        // Try real data first: mallow/data/BinanceFlat/M1/BTCUSDT
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

        if !files.is_empty() {
            let refs: Vec<&Path> = files.iter().map(|p| p.as_path()).collect();
            ParquetFeed::load_many(&refs, &args.symbol)?
        } else {
            // Fallback to testdata
            let testdata_path = Path::new(env!("CARGO_MANIFEST_DIR"))
                .join("../data/testdata/BTCUSDT/M1/BTCUSDT_M1_2026-01.parquet");
            ParquetFeed::load(&testdata_path, &args.symbol)?
        }
    };
    let n_bars   = feed.len();
    let timeframe = feed.timeframe();

    let strategy = MaCrossover::new(args.fast, args.slow);
    let risk = PercentEquity::fractional(0.95, 1).with_lot_size(args.lot);

    let mut engine = Engine::sync(
        args.capital,
        strategy,
        risk,
        args.commission,
        0.0005,
    );

    let report = engine.run(&mut feed, 0.04);

    println!("=== MaCrossover({},{}) — {} {} ===",
        args.fast, args.slow, args.symbol, timeframe);
    println!("Bars           : {:>10}", n_bars);
    println!("Lot size       : {:>10}", if args.lot == 0.0 { "fractional".to_string() } else { args.lot.to_string() });
    println!("──────────────────────────────");
    println!("Initial equity : {:>10.2}", report.initial_capital);
    println!("Final equity   : {:>10.2}", report.final_equity);
    println!("Total return   : {:>9.2}%", report.total_return_pct);
    println!("CAGR           : {:>9.2}%", report.cagr_pct);
    println!("──────────────────────────────");
    println!("Sharpe         : {:>10.4}", report.sharpe_ratio);
    println!("Sortino        : {:>10.4}", report.sortino_ratio);
    println!("Calmar         : {:>10.4}", report.calmar_ratio);
    println!("Max drawdown   : {:>9.2}%", report.max_drawdown_pct);
    println!("──────────────────────────────");
    println!("Total trades   : {:>10}", report.total_trades);
    println!("Win rate       : {:>9.2}%", report.win_rate_pct);
    println!("Profit factor  : {:>10.4}", report.profit_factor);
    println!("Expectancy     : {:>10.4}", report.expectancy);
    println!("Avg win        : {:>9.2}%", report.avg_win_pct);
    println!("Avg loss       : {:>9.2}%", report.avg_loss_pct);
    println!("Max consec loss: {:>10}", report.max_consecutive_losses);

    Ok(())
}

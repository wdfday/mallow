/// Interactive strategy tester — chạy bất kỳ strategy nào từ CLI.
///
/// Dùng:
///   cargo run -p alm-engine --bin try-strategy -- rsi_mean_rev
///   cargo run -p alm-engine --bin try-strategy -- ma_crossover --fast 10 --slow 30
///   cargo run -p alm-engine --bin try-strategy -- macd_crossover --fast 8 --slow 17 --signal 9
///   cargo run -p alm-engine --bin try-strategy -- cel --entry "rsi(14) < 30.0" --exit "rsi(14) > 70.0"
///   cargo run -p alm-engine --bin try-strategy -- cel --entry "ema(9) > ema(21)" --exit "ema(9) < ema(21)" --data trend
///
/// Flags:
///   --data trend|osc|sine|flat   synthetic dataset (default: trend)
///   --bars N                     bar count (default: 300)
///   --entry EXPR                 CEL entry expression
///   --exit  EXPR                 CEL exit expression
///   --KEY VALUE                  strategy param (e.g. --period 20)

use alm_core::{bar::Bar, signal::Direction, strategy::Strategy};
use alm_strategy::build_strategy;
use serde_json::{json, Value};

// ── CLI parsing ────────────────────────────────────────────────────────────────

struct Args {
    strategy:  String,
    data:      String,
    n_bars:    usize,
    cel_entry: Option<String>,
    cel_exit:  Option<String>,
    params:    serde_json::Map<String, Value>,
}

fn parse_args() -> Args {
    let raw: Vec<String> = std::env::args().skip(1).collect();

    if raw.is_empty() || raw[0] == "--help" || raw[0] == "-h" {
        eprintln!("Usage: try-strategy <strategy_name> [--KEY VALUE ...]");
        eprintln!();
        eprintln!("Datasets:  --data trend|osc|sine|flat");
        eprintln!("Bar count: --bars N  (default 300)");
        eprintln!("CEL:       --entry \"expr\"  --exit \"expr\"");
        eprintln!();
        eprintln!("Examples:");
        eprintln!("  try-strategy rsi_mean_rev");
        eprintln!("  try-strategy ma_crossover --fast 10 --slow 30");
        eprintln!("  try-strategy cel --entry \"rsi(14) < 30.0\" --exit \"rsi(14) > 70.0\"");
        std::process::exit(0);
    }

    let strategy = raw[0].clone();
    let mut data = "trend".to_string();
    let mut n_bars = 300usize;
    let mut cel_entry = None;
    let mut cel_exit  = None;
    let mut params = serde_json::Map::new();

    let mut i = 1;
    while i < raw.len() {
        let key = &raw[i];
        if !key.starts_with("--") {
            i += 1;
            continue;
        }
        let key = key.trim_start_matches('-');
        let val = raw.get(i + 1).map(|s| s.as_str()).unwrap_or("");
        match key {
            "data"  => data   = val.to_string(),
            "bars"  => n_bars = val.parse().unwrap_or(300),
            "entry" => cel_entry = Some(val.to_string()),
            "exit"  => cel_exit  = Some(val.to_string()),
            k => {
                // Try numeric, else string
                if let Ok(f) = val.parse::<f64>() {
                    params.insert(k.to_string(), Value::from(f));
                } else {
                    params.insert(k.to_string(), Value::from(val));
                }
            }
        }
        i += 2;
    }

    Args { strategy, data, n_bars, cel_entry, cel_exit, params }
}

// ── Synthetic datasets ─────────────────────────────────────────────────────────

fn make_bars(dataset: &str, n: usize) -> Vec<Bar> {
    (0..n)
        .map(|i| {
            let t = i as f64;
            let p = match dataset {
                "osc"  => 100.0 + 30.0 * (t * 0.1).sin(),
                "sine" => 100.0 + 40.0 * (t * 0.05).sin() + 15.0 * (t * 0.2).sin(),
                "flat" => 100.0 + (i % 3) as f64 * 0.1,
                _      => {
                    // trend: up → consolidation → down
                    let third = n / 3;
                    if i < third {
                        100.0 + i as f64 * 0.5
                    } else if i < third * 2 {
                        100.0 + third as f64 * 0.5
                    } else {
                        100.0 + third as f64 * 0.5 - (i - third * 2) as f64 * 0.5
                    }
                }
            };
            let p = p.max(1.0);
            let vol = 1000.0 + (i % 20) as f64 * 50.0;
            Bar::new(i as i64 * 60_000, "SYN", p, p * 1.004, p * 0.996, p, vol)
        })
        .collect()
}

// ── Mini backtest ──────────────────────────────────────────────────────────────

struct Trade {
    entry_bar:   usize,
    exit_bar:    usize,
    entry_price: f64,
    exit_price:  f64,
    pnl_pct:     f64,
}

fn run_strategy(strategy: &mut dyn Strategy, bars: &[Bar]) -> (Vec<(usize, Direction)>, Vec<Trade>, f64) {
    let mut signals: Vec<(usize, Direction)> = Vec::new();
    let mut trades:  Vec<Trade>              = Vec::new();
    let mut equity    = 100_000.0f64;
    let mut in_pos    = false;
    let mut entry_bar = 0usize;
    let mut entry_px  = 0.0f64;

    for (i, bar) in bars.iter().enumerate() {
        for sig in strategy.on_bar(bar) {
            signals.push((i, sig.direction));
            match sig.direction {
                Direction::Long if !in_pos => {
                    in_pos    = true;
                    entry_bar = i;
                    entry_px  = bar.close;
                }
                Direction::Close | Direction::Short if in_pos => {
                    in_pos = false;
                    let pnl = (bar.close - entry_px) / entry_px;
                    equity *= 1.0 + pnl;
                    trades.push(Trade {
                        entry_bar,
                        exit_bar: i,
                        entry_price: entry_px,
                        exit_price: bar.close,
                        pnl_pct: pnl * 100.0,
                    });
                }
                _ => {}
            }
        }
    }

    // Force-close at end
    if in_pos {
        let last = bars.last().unwrap();
        let pnl = (last.close - entry_px) / entry_px;
        equity *= 1.0 + pnl;
        trades.push(Trade {
            entry_bar,
            exit_bar: bars.len() - 1,
            entry_price: entry_px,
            exit_price: last.close,
            pnl_pct: pnl * 100.0,
        });
    }

    (signals, trades, equity)
}

// ── Display ────────────────────────────────────────────────────────────────────

fn dir_str(d: Direction) -> &'static str {
    match d {
        Direction::Long  => "▲ Long",
        Direction::Short => "▼ Short",
        Direction::Close => "✕ Close",
    }
}

fn print_signals(signals: &[(usize, Direction)], bars: &[Bar]) {
    if signals.is_empty() {
        println!("  (no signals generated)");
        return;
    }
    println!("{:>6}  {:<10}  {:>10}  {:>10}  {:>10}",
        "bar", "direction", "open", "close", "volume");
    println!("{}", "─".repeat(54));
    for &(i, dir) in signals {
        let b = &bars[i];
        println!("{:>6}  {:<10}  {:>10.2}  {:>10.2}  {:>10.0}",
            i, dir_str(dir), b.open, b.close, b.volume);
    }
}

fn print_trades(trades: &[Trade]) {
    if trades.is_empty() {
        println!("  (no completed trades)");
        return;
    }
    println!("{:>6}  {:>6}  {:>10}  {:>10}  {:>10}",
        "entry", "exit", "buy@", "sell@", "P&L %");
    println!("{}", "─".repeat(50));
    for t in trades {
        let marker = if t.pnl_pct >= 0.0 { "+" } else { "" };
        println!("{:>6}  {:>6}  {:>10.2}  {:>10.2}  {:>9}{:.2}%",
            t.entry_bar, t.exit_bar, t.entry_price, t.exit_price, marker, t.pnl_pct);
    }
}

fn print_summary(trades: &[Trade], equity: f64, n_signals: usize) {
    let n = trades.len();
    let wins = trades.iter().filter(|t| t.pnl_pct > 0.0).count();
    let total_ret = (equity / 100_000.0 - 1.0) * 100.0;
    let avg_pnl: f64 = if n > 0 {
        trades.iter().map(|t| t.pnl_pct).sum::<f64>() / n as f64
    } else { 0.0 };
    let max_win  = trades.iter().map(|t| t.pnl_pct).fold(f64::NEG_INFINITY, f64::max);
    let max_loss = trades.iter().map(|t| t.pnl_pct).fold(f64::INFINITY,     f64::min);

    println!();
    println!("  signals      : {n_signals}");
    println!("  trades       : {n}");
    if n > 0 {
        println!("  win rate     : {:.1}%  ({wins}/{n})", wins as f64 / n as f64 * 100.0);
        println!("  avg P&L      : {:+.2}%", avg_pnl);
        println!("  best trade   : {:+.2}%", if max_win  > f64::NEG_INFINITY { max_win  } else { 0.0 });
        println!("  worst trade  : {:+.2}%", if max_loss < f64::INFINITY     { max_loss } else { 0.0 });
    }
    let ret_marker = if total_ret >= 0.0 { "▲" } else { "▼" };
    println!("  total return : {ret_marker} {:+.2}%", total_ret);
}

// ── main ───────────────────────────────────────────────────────────────────────

fn main() {
    let args = parse_args();

    // Build params JSON
    let mut p = Value::Object(args.params.clone());
    if let Some(entry) = &args.cel_entry {
        p["entry"] = json!(entry);
    }
    if let Some(exit) = &args.cel_exit {
        p["exit"] = json!(exit);
    }

    let bars = make_bars(&args.data, args.n_bars);

    println!("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━");
    println!("  strategy : {}", args.strategy);
    println!("  dataset  : {}  ({} bars)", args.data, bars.len());
    if !args.params.is_empty() || args.cel_entry.is_some() {
        println!("  params   : {}", p);
    }
    println!("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━");

    let mut strategy = match build_strategy(&args.strategy, &p) {
        Ok(s) => s,
        Err(e) => {
            eprintln!("error: {e}");
            std::process::exit(1);
        }
    };

    let (signals, trades, equity) = run_strategy(strategy.as_mut(), &bars);

    println!("\n── signals ──────────────────────────────────────────────");
    print_signals(&signals, &bars);

    println!("\n── trades ───────────────────────────────────────────────");
    print_trades(&trades);

    println!("\n── summary ──────────────────────────────────────────────");
    print_summary(&trades, equity, signals.len());
    println!();
}

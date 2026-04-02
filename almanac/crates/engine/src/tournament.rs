/// Strategy tournament — chạy tất cả named strategy trên cùng synthetic data,
/// rank theo total return.
///
/// Chạy:
///   cargo run -p alm-engine --bin tournament
///   cargo run -p alm-engine --bin tournament -- trend   # lọc theo tên

use alm_core::{bar::Bar, signal::Direction, strategy::Strategy};
use alm_strategy::build_strategy;
use serde_json::json;

// ── Synthetic datasets ────────────────────────────────────────────────────────

/// Trending up → flat → trending down (500 bars)
fn trend_bars() -> Vec<Bar> {
    let n = 500usize;
    (0..n)
        .map(|i| {
            let p = if i < 200 {
                100.0 + i as f64 * 0.5          // uptrend
            } else if i < 300 {
                200.0                            // consolidation
            } else {
                200.0 - (i - 300) as f64 * 0.5  // downtrend
            };
            Bar::new(
                i as i64 * 60_000,
                "SYN",
                p, p * 1.003, p * 0.997, p,
                1000.0 + (i % 20) as f64 * 50.0,
            )
        })
        .collect()
}

/// Oscillating price (RSI-friendly, 400 bars)
fn osc_bars() -> Vec<Bar> {
    let n = 400usize;
    (0..n)
        .map(|i| {
            let t = i as f64;
            let p = (100.0 + 30.0 * (t * 0.08).sin()).max(1.0);
            Bar::new(
                i as i64 * 60_000,
                "SYN",
                p, p * 1.004, p * 0.996, p,
                900.0 + (i % 10) as f64 * 100.0,
            )
        })
        .collect()
}

// ── Mini backtest ──────────────────────────────────────────────────────────────

struct Result {
    name:       String,
    signals:    usize,
    trades:     usize,
    return_pct: f64,
}

/// Simplified long-only backtest: enter on Long, exit on Close/Short.
/// No commission, no sizing — pure strategy signal quality.
fn mini_backtest(strategy: &mut dyn Strategy, bars: &[Bar], capital: f64) -> (usize, usize, f64) {
    let mut equity = capital;
    let mut in_position = false;
    let mut entry_price = 0.0f64;
    let mut total_signals = 0usize;
    let mut trades = 0usize;

    for bar in bars {
        let sigs = strategy.on_bar(bar);
        total_signals += sigs.len();

        for sig in sigs {
            match sig.direction {
                Direction::Long if !in_position => {
                    in_position = true;
                    entry_price = bar.close;
                }
                Direction::Close | Direction::Short if in_position => {
                    in_position = false;
                    let ret = (bar.close - entry_price) / entry_price;
                    equity *= 1.0 + ret;
                    trades += 1;
                }
                _ => {}
            }
        }
    }

    // Force-close at last bar
    if in_position {
        let last = bars.last().unwrap();
        let ret = (last.close - entry_price) / entry_price;
        equity *= 1.0 + ret;
        trades += 1;
    }

    let return_pct = (equity / capital - 1.0) * 100.0;
    (total_signals, trades, return_pct)
}

// ── All strategy entries ───────────────────────────────────────────────────────

const STRATEGIES: &[(&str, &str)] = &[
    // MA / Trend
    ("ma_crossover",         "trend"),
    ("triple_ema",           "trend"),
    ("hma_crossover",        "trend"),
    ("dema_crossover",       "trend"),
    ("trend_follower",       "trend"),
    ("trend_transition",     "trend"),
    ("gmma_crossover",       "trend"),
    ("alligator",            "trend"),
    ("ichimoku_cloud",       "trend"),
    ("ichimoku_cross",       "trend"),
    ("supertrend",           "trend"),
    ("supertrend_macd",      "trend"),
    ("dmi_adx",              "trend"),
    ("wolfstein",            "trend"),
    ("aroon_trend",          "trend"),
    // Oscillator
    ("rsi_mean_rev",         "osc"),
    ("macd_crossover",       "osc"),
    ("macd_ma",              "osc"),
    ("stochastic_crossover", "osc"),
    ("stochastic_dk",        "osc"),
    ("cci_reversal",         "osc"),
    ("trix",                 "osc"),
    ("roc",                  "osc"),
    ("tsi",                  "osc"),
    ("ao",                   "osc"),
    ("stoch_rsi",            "osc"),
    ("connors_rsi",          "osc"),
    ("kdj",                  "osc"),
    // Volatility
    ("atr_trailing",         "vol"),
    ("volatility_squeezer",  "vol"),
    ("volatility_vanguard",  "vol"),
    ("volatility_ratio",     "vol"),
    ("bb_squeeze",           "vol"),
    ("bollinger_macd",       "vol"),
    ("mean_reversion",       "vol"),
    ("bb_keltner_squeeze",   "vol"),
    ("keltner_breakout",     "vol"),
    ("chandelier_exit",      "vol"),
    ("parabolic_sar",        "vol"),
    // Breakout
    ("donchian_breakout",    "breakout"),
    ("highest_breakout",     "breakout"),
    ("price_action_swing",   "breakout"),
    ("dual_momentum",        "breakout"),
    ("momentum_roc",         "breakout"),
    ("kama",                 "breakout"),
    // Volume
    ("mfi_trend",            "volume"),
    ("mfi_revert",           "volume"),
    ("vwap_bounce",          "volume"),
    ("vwap_trend",           "volume"),
    // Multi
    ("oscillator_overlord",  "multi"),
    ("equilibrium_explorer", "multi"),
    ("range_rover",          "multi"),
    ("reversal_catcher",     "multi"),
    ("swing_trader",         "multi"),
    ("chop_filter",          "multi"),
    ("waddah_attar",         "multi"),
    ("ma_pullback",          "multi"),
    // Special
    ("scalping_ema",         "special"),
    ("heiken_ashi_color",    "special"),
    ("heiken_ashi_breakout", "special"),
    ("heiken_ashi_harmonizer","special"),
    ("elder_ray",            "special"),
    ("rwi",                  "special"),
    ("tema_crossover",       "special"),
    // CEL examples
    ("cel",                  "cel"),
    ("cel",                  "cel"),
];

// ── main ───────────────────────────────────────────────────────────────────────

fn main() {
    let filter = std::env::args().nth(1).unwrap_or_default();
    println!("almanac — strategy tournament");
    println!("filter: {}", if filter.is_empty() { "(all)" } else { &filter });

    let trend = trend_bars();
    let osc   = osc_bars();

    let cel_configs: &[(&str, (&str, &str))] = &[
        ("cel rsi(14)<30",    ("rsi(14) < 30.0",                        "rsi(14) > 70.0")),
        ("cel ema(20)x(50)",  ("prev_ema(20) <= prev_ema(50) && ema(20) > ema(50)",
                               "prev_ema(20) >= prev_ema(50) && ema(20) < ema(50)")),
    ];

    let mut results: Vec<Result> = Vec::new();

    for &(name, tag) in STRATEGIES {
        if name == "cel" {
            continue; // handled separately below
        }
        if !filter.is_empty() && !name.contains(&filter) && !tag.contains(&filter) {
            continue;
        }

        let bars = if tag == "osc" { &osc } else { &trend };

        let built = build_strategy(name, &json!({}));
        let mut s = match built {
            Ok(s) => s,
            Err(e) => {
                eprintln!("  skip {name}: {e}");
                continue;
            }
        };

        let (signals, trades, ret) = mini_backtest(s.as_mut(), bars, 100_000.0);
        results.push(Result { name: name.to_string(), signals, trades, return_pct: ret });
    }

    // CEL expressions
    for (label, (entry, exit)) in cel_configs {
        if !filter.is_empty() && !label.contains(&filter) && !"cel".contains(&filter) {
            continue;
        }
        let bars = if entry.contains("rsi") { &osc } else { &trend };
        let mut s = build_strategy("cel", &json!({ "entry": entry, "exit": exit })).unwrap();
        let (signals, trades, ret) = mini_backtest(s.as_mut(), bars, 100_000.0);
        results.push(Result { name: label.to_string(), signals, trades, return_pct: ret });
    }

    // Sort by return descending
    results.sort_by(|a, b| b.return_pct.partial_cmp(&a.return_pct).unwrap());

    println!("\n{:<30} {:>8} {:>8} {:>12}", "strategy", "signals", "trades", "return %");
    println!("{}", "─".repeat(62));

    for r in &results {
        let ret_str = format!("{:+.2}%", r.return_pct);
        let marker = if r.return_pct > 0.0 { "▲" } else if r.return_pct < 0.0 { "▼" } else { " " };
        println!("{marker} {:<29} {:>8} {:>8} {:>12}", r.name, r.signals, r.trades, ret_str);
    }

    let positive = results.iter().filter(|r| r.return_pct > 0.0).count();
    println!("{}", "─".repeat(62));
    println!("total: {}  profitable: {}  breakeven/loss: {}",
        results.len(), positive, results.len() - positive);
}

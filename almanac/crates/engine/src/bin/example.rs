use alm_engine::backtest;
use alm_engine::types::BacktestRequest;
use anyhow::Result;
use std::path::PathBuf;
use tracing::info;

const SCRIPT_RSI: &str = r#"
let rsi = ind.rsi(14);
if rsi[0] < 35.0 { long = true; }
if rsi[0] > 65.0 { exit = true; }
"#;

const SCRIPT_ST: &str = r#"
let st  = ind.supertrend(10, 3.0);
let rsi = ind.rsi(14);

let bullish_now  = close[0] > st[0];
let bullish_prev = close[1] > st[1];

if !bullish_prev && bullish_now && rsi[0] > 50.0 { long = true; }
if bullish_prev && !bullish_now                   { exit = true; }
if rsi[0] < 40.0                                  { exit = true; }
"#;

fn run(label: &str, script: &str, data_dir: &PathBuf) -> Result<()> {
    let req = BacktestRequest {
        strategy: "script".into(),
        symbol: "BTCUSDT".into(),
        params: Some(serde_json::json!({ "script": script })),
        from: Some("2024-01-01".into()),
        to: Some("2024-12-31".into()),
        initial_capital: Some(10_000.0),
        commission_pct: Some(0.001),
        slippage_pct: Some(0.0005),
        risk_free_annual: Some(0.04),
        position_size_pct: Some(0.95),
        position_size_usd: None,
        position_size_quantity: None,
        max_positions: Some(1),
        strength_sizing: None,
        size_mode: None,
        risk_per_trade_pct: None,
        atr_multiplier: None,
        max_units: None,
        max_position_pct: None,
        pyramid: None,
        data_source: Some("binanceflat".into()),
        asset_type: Some("crypto".into()),
        timeframe: Some("H1".into()),
        monte_carlo: None,
        walk_forward: None,
        intra_bar_mode: None,
    };

    let t0 = std::time::Instant::now();
    let report = backtest::run(req, data_dir)?;
    let elapsed = t0.elapsed();

    info!(
        label,
        total_return = format!("{:.2}%", report.returns.total_pct),
        cagr         = format!("{:.2}%", report.returns.cagr_pct),
        sharpe       = format!("{:.3}", report.risk_adjusted.sharpe),
        max_dd       = format!("{:.2}%", report.drawdown.max_pct),
        trades       = report.trade_stats.total,
        win_rate     = format!("{:.1}%", report.trade_stats.win_rate_pct),
        elapsed_ms   = elapsed.as_millis(),
        "result"
    );
    Ok(())
}

fn main() -> Result<()> {
    tracing_subscriber::fmt()
        .with_env_filter(tracing_subscriber::EnvFilter::from_default_env())
        .init();

    let data_dir = PathBuf::from(
        std::env::var("DATA_DIR").unwrap_or_else(|_| "../../data".into()),
    );

    run("rsi_mean_rev", SCRIPT_RSI, &data_dir)?;
    run("supertrend_rsi", SCRIPT_ST, &data_dir)?;

    Ok(())
}

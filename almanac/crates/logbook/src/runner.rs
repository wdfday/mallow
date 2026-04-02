use std::collections::HashMap;
use std::path::Path;

use anyhow::{Context, Result};
use alm_core::{exit::ExitRules, order::Side};
use alm_data::BarFeed;
use alm_engine::Engine;
use alm_strategy::{build_strategy, FixedFractional};
use alm_strategy::dynamic::indicator_box::IndicatorBox;
use chrono::{DateTime, Utc};
use serde_json::Value;

use crate::data::{find_parquet_files, load_bars, parse_date_ms};
use crate::types::{
    BacktestRequest, BacktestResponse, TradeResponse,
    IndicatorRequest, IndicatorResponse, IndicatorPoint,
};

const DEFAULT_RISK_FREE: f64   = 0.04;    // 4% — US Treasury proxy
const DEFAULT_COMMISSION: f64  = 0.001;   // 0.1% per trade
const DEFAULT_SLIPPAGE: f64    = 0.0005;  // 0.05%
const DEFAULT_CAPITAL: f64     = 10_000.0;
const DEFAULT_POSITION_PCT: f64 = 0.95;  // 95% of cash per trade

/// Run a full backtest from a request, discovering data under `data_dir`.
pub fn run(req: BacktestRequest, data_dir: &Path) -> Result<BacktestResponse> {
    let params = req.params.unwrap_or(Value::Object(Default::default()));
    let capital = req.initial_capital.unwrap_or(DEFAULT_CAPITAL);
    let symbol = if req.symbol.is_empty() {
        "BTCUSD".to_string()
    } else {
        req.symbol
    };

    // Parse date range
    let from_ms = req.from.as_deref().and_then(parse_date_ms);
    let to_ms = req.to.as_deref().and_then(|s| {
        parse_date_ms(s).map(|ms| ms + 86_400_000 - 1)
    });

    // Discover + load data
    let market_hours_only = req.market_hours_only.unwrap_or(false);
    let exchange = req.exchange.as_deref().unwrap_or("us");
    let files = find_parquet_files(data_dir, &symbol);
    let mut feed = load_bars(&files, &symbol, from_ms, to_ms, market_hours_only, exchange)
        .with_context(|| format!("loading data for '{}'", symbol))?;

    tracing::info!(
        symbol = %symbol,
        strategy = %req.strategy,
        bars = feed.len(),
        "starting backtest"
    );

    // Build strategy + engine
    let strategy = build_strategy(&req.strategy, &params)?;
    let position_pct = req.position_size_pct.unwrap_or(DEFAULT_POSITION_PCT).clamp(0.01, 1.0);
    let lot_size = asset_lot_size(req.asset_type.as_deref());
    let risk = FixedFractional::new(position_pct, 1).with_lot_size(lot_size);

    let exit_rules = req.exit.map(|cfg| ExitRules {
        stop_loss_pct:    cfg.stop_loss_pct,
        take_profit_pct:  cfg.take_profit_pct,
        trailing_stop_pct: cfg.trailing_stop_pct,
        max_bars_held:    cfg.max_bars_held,
    }).unwrap_or_default();

    let commission     = req.commission_pct.unwrap_or(DEFAULT_COMMISSION);
    let slippage       = req.slippage_pct.unwrap_or(DEFAULT_SLIPPAGE);
    let risk_free      = req.risk_free_annual.unwrap_or(DEFAULT_RISK_FREE);

    let mut engine = Engine::sync(capital, strategy, risk, commission, slippage)
        .with_exit_rules(exit_rules);

    let report = engine.run(&mut feed, risk_free);

    // Map trades
    let trades: Vec<TradeResponse> = engine
        .portfolio
        .trades
        .iter()
        .map(|t| TradeResponse {
            symbol: t.symbol.clone(),
            side: match t.side {
                Side::Buy => "long".into(),
                Side::Sell => "short".into(),
            },
            qty: t.qty,
            entry_price: t.entry_price,
            exit_price: t.exit_price,
            entry_time: ms_to_iso(t.entry_timestamp),
            exit_time: ms_to_iso(t.exit_timestamp),
            pnl: t.pnl,
            pnl_pct: t.pnl_pct,
        })
        .collect();

    let no_trades = report.total_trades == 0;

    Ok(BacktestResponse {
        strategy: req.strategy,
        symbol,
        params,
        initial_capital:          report.initial_capital,
        final_equity:             report.final_equity,
        total_return:             report.total_return_pct,
        cagr:                     report.cagr_pct,
        annualized_volatility:    if no_trades { 0.0 } else { report.annualized_volatility_pct },
        sharpe_ratio:             if no_trades { 0.0 } else { report.sharpe_ratio },
        sortino_ratio:            if no_trades { 0.0 } else { report.sortino_ratio },
        calmar_ratio:             if no_trades { 0.0 } else { report.calmar_ratio },
        max_drawdown:             report.max_drawdown_pct,
        max_dd_duration_bars:     report.max_dd_duration_bars,
        avg_drawdown:             report.avg_drawdown_pct,
        total_trades:             report.total_trades,
        win_rate:                 report.win_rate_pct,
        profit_factor:            report.profit_factor,
        expectancy:               report.expectancy,
        avg_win:                  report.avg_win_pct,
        avg_loss:                 report.avg_loss_pct,
        avg_trade_duration_hours: report.avg_trade_duration_hours,
        max_consecutive_losses:   report.max_consecutive_losses,
        trades,
    })
}

/// Map asset_type string → lot size for position sizing.
/// `"crypto"` / `None` → 0.0 (fractional)
/// `"stock"` → 1.0 (whole shares)
/// `"vn_stock"` → 100.0 (HOSE lots)
pub fn asset_lot_size(asset_type: Option<&str>) -> f64 {
    match asset_type {
        Some("stock")    => 1.0,
        Some("vn_stock") => 100.0,
        _                => 0.0, // crypto / default → fractional
    }
}

fn ms_to_iso(ms: i64) -> String {
    DateTime::<Utc>::from_timestamp_millis(ms)
        .map(|dt| dt.format("%Y-%m-%dT%H:%M:%SZ").to_string())
        .unwrap_or_default()
}

// ── Indicator computation ─────────────────────────────────────────────────────

/// Auto-generate a series label from indicator config if no explicit label given.
/// E.g. `{ "type": "ema", "period": 20 }` → `"ema_20"`.
fn auto_label(config: &serde_json::Map<String, Value>) -> String {
    let type_ = config.get("type").and_then(Value::as_str).unwrap_or("ind");
    // Collect the first numeric param value(s) in a stable order
    let mut parts: Vec<String> = vec![type_.to_string()];
    for key in &["period", "fast", "slow", "signal", "k_period", "er_period",
                 "tenkan", "kijun", "senkou_b", "lookback"] {
        if let Some(v) = config.get(*key).and_then(Value::as_f64) {
            parts.push(format!("{}", v as i64));
        }
    }
    parts.join("_")
}

/// Compute one or more indicators over historical data.
pub fn compute_indicators(req: IndicatorRequest, data_dir: &Path) -> Result<IndicatorResponse> {
    let symbol = if req.symbol.is_empty() { "BTCUSD".to_string() } else { req.symbol };

    let from_ms = req.from.as_deref().and_then(parse_date_ms);
    let to_ms   = req.to.as_deref().and_then(|s| {
        parse_date_ms(s).map(|ms| ms + 86_400_000 - 1)
    });
    let market_hours_only = req.market_hours_only.unwrap_or(false);
    let exchange          = req.exchange.as_deref().unwrap_or("us");

    let files = find_parquet_files(data_dir, &symbol);
    let mut feed = load_bars(&files, &symbol, from_ms, to_ms, market_hours_only, exchange)
        .with_context(|| format!("loading data for '{}'", symbol))?;

    let bars_total = feed.len();

    // Build (label, IndicatorBox) pairs — validate all configs before touching data
    let mut inds: Vec<(String, IndicatorBox)> = req.indicators
        .iter()
        .map(|cfg| {
            let label = cfg.label.clone()
                .unwrap_or_else(|| auto_label(&cfg.config));
            let box_ = IndicatorBox::from_config(&Value::Object(cfg.config.clone()))?;
            Ok((label, box_))
        })
        .collect::<Result<Vec<_>>>()?;

    let mut series: HashMap<String, Vec<IndicatorPoint>> =
        inds.iter().map(|(l, _)| (l.clone(), Vec::new())).collect();

    while let Some(bar) = feed.next() {
        for (label, ind) in &mut inds {
            if let Some(fields) = ind.update(&bar) {
                series.get_mut(label).unwrap().push(IndicatorPoint {
                    t: bar.timestamp,
                    fields,
                });
            }
        }
    }

    Ok(IndicatorResponse { symbol, bars: bars_total, series })
}

//! Backtest runner — executes a named / CEL / dynamic strategy over
//! historical bars. Moved from the former `logbook` crate.

use std::path::Path;

use alm_core::{exit::ExitRules, order::Side};
use alm_data::BarFeed;
use alm_report::BuyHoldBenchmark;
use alm_strategy::{build_strategy, AnySizer, FixedFractional, FixedQuantity, FixedUsd};
use anyhow::{Context, Result};
use chrono::{DateTime, Utc};
use serde_json::Value;

use crate::data::{find_parquet_files, load_bars, parse_date_ms};
use crate::types::{
    BacktestRequest, BacktestResponse, BuyHoldBenchmarkResponse, CurvePoint, ExitConfig,
    ExitLevel, RegimeSummaryResponse, TradeResponse,
};
use crate::Engine;

const DEFAULT_RISK_FREE: f64 = 0.04; // 4% — US Treasury proxy.
const DEFAULT_COMMISSION: f64 = 0.001; // 0.1% per trade.
const DEFAULT_SLIPPAGE: f64 = 0.0005; // 0.05%.
const DEFAULT_CAPITAL: f64 = 10_000.0;
const DEFAULT_POSITION_PCT: f64 = 0.95; // 95% of cash per trade.

/// Run a full backtest from a request, discovering data under `data_dir`.
pub fn run(req: BacktestRequest, data_dir: &Path) -> Result<BacktestResponse> {
    let params = req.params.unwrap_or(Value::Object(Default::default()));
    let capital = req.initial_capital.unwrap_or(DEFAULT_CAPITAL);
    let symbol = if req.symbol.is_empty() {
        "BTCUSD".to_string()
    } else {
        req.symbol
    };

    let from_ms = req.from.as_deref().and_then(parse_date_ms);
    let to_ms = req
        .to
        .as_deref()
        .and_then(|s| parse_date_ms(s).map(|ms| ms + 86_400_000 - 1));

    let market_hours_only = req.market_hours_only.unwrap_or(false);
    let exchange = req.exchange.as_deref().unwrap_or("us");
    let files = find_parquet_files(data_dir, &symbol, req.timeframe.as_deref());
    let mut feed = load_bars(&files, &symbol, from_ms, to_ms, market_hours_only, exchange)
        .with_context(|| format!("loading data for '{}'", symbol))?;

    tracing::info!(
        symbol = %symbol,
        strategy = %req.strategy,
        bars = feed.len(),
        "starting backtest"
    );

    let strategy = build_strategy(&req.strategy, &params)?;
    let lot_size = asset_lot_size(req.asset_type.as_deref());
    let max_positions = req.max_positions.unwrap_or(1).max(1);

    // Sizer priority: quantity > USD > pct.
    let risk: AnySizer = if let Some(qty) = req.position_size_quantity {
        AnySizer::FixedQuantity(FixedQuantity::new(qty, max_positions))
    } else if let Some(usd) = req.position_size_usd {
        AnySizer::FixedUsd(FixedUsd::new(usd, max_positions).with_lot_size(lot_size))
    } else {
        let pct = req
            .position_size_pct
            .unwrap_or(DEFAULT_POSITION_PCT)
            .clamp(0.01, 1.0);
        AnySizer::FixedFractional(
            FixedFractional::new(pct, max_positions).with_lot_size(lot_size),
        )
    };

    let exit_rules = req
        .exit
        .map(|cfg| exit_rules_from_config(cfg, &params))
        .unwrap_or_default();

    let commission = req.commission_pct.unwrap_or(DEFAULT_COMMISSION);
    let slippage = req.slippage_pct.unwrap_or(DEFAULT_SLIPPAGE);
    let risk_free = req.risk_free_annual.unwrap_or(DEFAULT_RISK_FREE);

    let mut engine = Engine::sync(capital, strategy, risk, commission, slippage)
        .with_exit_rules(exit_rules);

    let report = engine.run(&mut feed, risk_free);

    // Collect bars for benchmark (feed is exhausted — reset and drain again).
    feed.reset();
    let mut closes: Vec<f64> = Vec::new();
    let mut timestamps: Vec<i64> = Vec::new();
    while let Some(b) = feed.next() {
        closes.push(b.close);
        timestamps.push(b.timestamp);
    }
    let bh = BuyHoldBenchmark::compute(&closes, &timestamps, risk_free);
    let benchmark = if closes.len() >= 2 {
        Some(BuyHoldBenchmarkResponse {
            total_return_pct: bh.total_return_pct,
            cagr_pct: bh.cagr_pct,
            annualized_volatility_pct: bh.annualized_volatility_pct,
            sharpe_ratio: bh.sharpe_ratio,
            sortino_ratio: bh.sortino_ratio,
            max_drawdown_pct: bh.max_drawdown_pct,
            max_dd_duration_bars: bh.max_dd_duration_bars,
        })
    } else {
        None
    };

    let raw_series = engine.strategy.take_indicator_series();
    let indicator_series: std::collections::HashMap<String, Vec<CurvePoint>> = raw_series
        .into_iter()
        .map(|(k, pts)| {
            (
                k,
                pts.into_iter().map(|(t, v)| CurvePoint { t, v }).collect(),
            )
        })
        .collect();

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
            entry_ts: t.entry_timestamp,
            exit_ts: t.exit_timestamp,
            entry_time: ms_to_iso(t.entry_timestamp),
            exit_time: ms_to_iso(t.exit_timestamp),
            pnl: t.pnl,
            pnl_pct: t.pnl_pct,
        })
        .collect();

    let equity_curve: Vec<CurvePoint> = engine
        .portfolio
        .equity_curve
        .iter()
        .map(|p| CurvePoint {
            t: p.timestamp,
            v: p.equity,
        })
        .collect();

    let drawdown_curve: Vec<CurvePoint> = {
        let mut peak = capital;
        engine
            .portfolio
            .equity_curve
            .iter()
            .map(|p| {
                if p.equity > peak {
                    peak = p.equity;
                }
                let dd = if peak > 0.0 {
                    (p.equity - peak) / peak
                } else {
                    0.0
                };
                CurvePoint {
                    t: p.timestamp,
                    v: dd,
                }
            })
            .collect()
    };

    let no_trades = report.total_trades == 0;

    // Exposure: % of total period the strategy held a position.
    let exposure_pct = {
        let total_ms = engine
            .portfolio
            .equity_curve
            .last()
            .and_then(|last| {
                engine
                    .portfolio
                    .equity_curve
                    .first()
                    .map(|first| last.timestamp - first.timestamp)
            })
            .unwrap_or(0);
        let held_ms: i64 = engine
            .portfolio
            .trades
            .iter()
            .map(|t| t.exit_timestamp - t.entry_timestamp)
            .sum();
        if total_ms > 0 {
            held_ms as f64 / total_ms as f64 * 100.0
        } else {
            0.0
        }
    };

    let regime_summary = report.regime_summary.map(|r| RegimeSummaryResponse {
        trending_pct: r.trending_pct,
        ranging_pct: r.ranging_pct,
        neutral_pct: r.neutral_pct,
        high_vol_pct: r.high_vol_pct,
        low_vol_pct: r.low_vol_pct,
        changes: r.changes,
    });

    Ok(BacktestResponse {
        strategy: req.strategy,
        symbol,
        params,
        initial_capital: report.initial_capital,
        final_equity: report.final_equity,
        total_return: report.total_return_pct,
        cagr: report.cagr_pct,
        annualized_volatility: if no_trades {
            0.0
        } else {
            report.annualized_volatility_pct
        },
        sharpe_ratio: if no_trades { 0.0 } else { report.sharpe_ratio },
        sortino_ratio: if no_trades { 0.0 } else { report.sortino_ratio },
        calmar_ratio: if no_trades { 0.0 } else { report.calmar_ratio },
        max_drawdown: report.max_drawdown_pct,
        max_dd_duration_bars: report.max_dd_duration_bars,
        avg_drawdown: report.avg_drawdown_pct,
        total_trades: report.total_trades,
        win_rate: report.win_rate_pct,
        profit_factor: report.profit_factor,
        expectancy: report.expectancy,
        avg_win: report.avg_win_pct,
        avg_loss: report.avg_loss_pct,
        avg_trade_duration_hours: report.avg_trade_duration_hours,
        max_consecutive_losses: report.max_consecutive_losses,
        var_95: if no_trades { 0.0 } else { report.var_95 },
        cvar_95: if no_trades { 0.0 } else { report.cvar_95 },
        omega_ratio: if no_trades { 0.0 } else { report.omega_ratio },
        tail_ratio: if no_trades { 0.0 } else { report.tail_ratio },
        recovery_factor: if no_trades {
            0.0
        } else {
            report.recovery_factor
        },
        rolling_sharpe: report.rolling_sharpe,
        rolling_drawdown: report.rolling_drawdown,
        timeframe: report.timeframe.to_string(),
        exposure_pct,
        regime_summary,
        trades,
        equity_curve,
        drawdown_curve,
        indicator_series,
        benchmark,
    })
}

/// Build engine [`ExitRules`] from [`ExitConfig`].
///
/// Fixed-pct levels → directly into `ExitRules`. ATR-expression levels
/// (`"N*atr(P)"`) → injected into `cel_params` so that `CelStrategy` handles
/// them internally; engine-level `ExitRules` gets `None` for those fields
/// (the engine does not know ATR at construction time).
pub fn exit_rules_from_config(cfg: ExitConfig, cel_params: &Value) -> ExitRules {
    let _ = cel_params; // reserved for future ATR injection into non-CEL strategies.
    ExitRules {
        stop_loss_pct: cfg.sl.as_ref().and_then(ExitLevel::as_pct),
        take_profit_pct: cfg.tp.as_ref().and_then(ExitLevel::as_pct),
        max_bars_held: cfg.max_bars,
    }
}

/// Inject ATR-based `tp`/`sl` from [`ExitConfig`] into a CEL params map.
/// Called by the CEL `From` conversion so that `CelStrategy::from_params`
/// picks them up.
pub fn inject_atr_exit_into_cel_params(
    cfg: &ExitConfig,
    params: &mut serde_json::Map<String, Value>,
) {
    use serde_json::json;
    if let Some(level) = &cfg.tp {
        match level {
            ExitLevel::Pct(v) => {
                params.insert("tp".into(), json!(v));
            }
            ExitLevel::Expr(_) => {
                if let Some((mult, period)) = level.as_atr() {
                    params.insert("tp_atr".into(), json!(mult));
                    params.insert("atr_period".into(), json!(period));
                }
            }
        }
    }
    if let Some(level) = &cfg.sl {
        match level {
            ExitLevel::Pct(v) => {
                params.insert("sl".into(), json!(v));
            }
            ExitLevel::Expr(_) => {
                if let Some((mult, period)) = level.as_atr() {
                    params.insert("sl_atr".into(), json!(mult));
                    // atr_period already set above; only overwrite if not set yet.
                    params.entry("atr_period").or_insert(json!(period));
                }
            }
        }
    }
}

/// Map `asset_type` string → lot size for position sizing.
/// `"crypto"` / `None` → 0.0 (fractional), `"stock"` → 1.0, `"vn_stock"` → 100.0.
pub fn asset_lot_size(asset_type: Option<&str>) -> f64 {
    match asset_type {
        Some("stock") => 1.0,
        Some("vn_stock") => 100.0,
        _ => 0.0,
    }
}

fn ms_to_iso(ms: i64) -> String {
    DateTime::<Utc>::from_timestamp_millis(ms)
        .map(|dt| dt.format("%Y-%m-%dT%H:%M:%SZ").to_string())
        .unwrap_or_default()
}

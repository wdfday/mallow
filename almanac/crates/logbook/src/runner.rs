use std::collections::HashMap;
use std::path::Path;

use anyhow::{Context, Result};
use alm_core::{exit::ExitRules, order::Side};
use alm_data::BarFeed;
use alm_engine::Engine;
use alm_strategy::{build_strategy, FixedFractional};
use alm_strategy::bar_resampler::TimeBarResampler;
use alm_strategy::dynamic::indicator_box::IndicatorBox;
use alm_strategy::expr::cel::parse_cel_indicator;
use chrono::{DateTime, Utc};
use serde_json::Value;

use crate::data::{find_parquet_files, load_bars, parse_date_ms};
use crate::types::{
    BacktestRequest, BacktestResponse, CurvePoint, TradeResponse,
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
            entry_ts: t.entry_timestamp,
            exit_ts: t.exit_timestamp,
            entry_time: ms_to_iso(t.entry_timestamp),
            exit_time: ms_to_iso(t.exit_timestamp),
            pnl: t.pnl,
            pnl_pct: t.pnl_pct,
        })
        .collect();

    // Equity curve
    let equity_curve: Vec<CurvePoint> = engine
        .portfolio
        .equity_curve
        .iter()
        .map(|p| CurvePoint { t: p.timestamp, v: p.equity })
        .collect();

    // Drawdown curve — running drawdown from peak, expressed as fraction
    let drawdown_curve: Vec<CurvePoint> = {
        let mut peak = capital;
        engine.portfolio.equity_curve.iter().map(|p| {
            if p.equity > peak { peak = p.equity; }
            let dd = if peak > 0.0 { (p.equity - peak) / peak } else { 0.0 };
            CurvePoint { t: p.timestamp, v: dd }
        }).collect()
    };

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
        equity_curve,
        drawdown_curve,
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

/// Build a JSON config `Value` from a CEL base name + period (mirrors `make_binding` in cel.rs).
fn cel_to_config(base: &str, n: usize) -> Value {
    use serde_json::json;
    match base {
        "ema"               => json!({"type":"ema","period":n}),
        "sma"               => json!({"type":"sma","period":n}),
        "wma"               => json!({"type":"wma","period":n}),
        "hma"               => json!({"type":"hma","period":n}),
        "dema"              => json!({"type":"dema","period":n}),
        "tema"              => json!({"type":"tema","period":n}),
        "smma"              => json!({"type":"smma","period":n}),
        "alma"              => json!({"type":"alma","period":n}),
        "mcginley"          => json!({"type":"mcginley","period":n}),
        "lsma" | "lsma_slope" => json!({"type":"lsma","period":n}),
        "vwma"              => json!({"type":"vwma","period":n}),
        "kama"              => json!({"type":"kama","er_period":n}),
        "macd_hist" | "macd_line" => json!({"type":"macd","fast":n,"slow":26,"signal":9}),
        "adx" | "plus_di" | "minus_di" => json!({"type":"adx","period":n}),
        "dmi_plus" | "dmi_minus" | "dmi_dx" => json!({"type":"dmi","period":n}),
        "aroon_up" | "aroon_down" | "aroon_osc" => json!({"type":"aroon","period":n}),
        "vortex_plus" | "vortex_minus" => json!({"type":"vortex","period":n}),
        "kdj_k" | "kdj_d" | "kdj_j" => json!({"type":"kdj","period":n}),
        "supertrend" | "st_bull" => json!({"type":"supertrend","period":n,"multiplier":3.0}),
        "rsi"               => json!({"type":"rsi","period":n}),
        "cci"               => json!({"type":"cci","period":n}),
        "roc"               => json!({"type":"roc","period":n}),
        "mom"               => json!({"type":"mom","period":n}),
        "cmo"               => json!({"type":"cmo","period":n}),
        "dpo"               => json!({"type":"dpo","period":n}),
        "mfi"               => json!({"type":"mfi","period":n}),
        "williams"          => json!({"type":"williams_r","period":n}),
        "tsi"               => json!({"type":"tsi","first":n,"second":13}),
        "rci"               => json!({"type":"rci","period":n}),
        "chop"              => json!({"type":"chop","period":n}),
        "connors_rsi"       => json!({"type":"connors_rsi","rsi_period":n,"streak_period":2,"rank_period":100}),
        "stoch_k" | "stoch_d" => json!({"type":"stochastic","k_period":n,"d_period":3}),
        "srsi_k" | "srsi_d" => json!({"type":"stoch_rsi","rsi_period":n,"smooth_d":3}),
        "fisher_line" | "fisher_sig" => json!({"type":"fisher","period":n}),
        "bull_power" | "bear_power" => json!({"type":"bull_bear","period":n}),
        "ppo_line" | "ppo_sig" | "ppo_hist" => json!({"type":"ppo","fast":n,"slow":26,"signal":9}),
        "rvi_line" | "rvi_sig" => json!({"type":"rvi","period":n}),
        "atr"               => json!({"type":"atr","period":n}),
        "bb_upper" | "bb_lower" | "bb_mid" => json!({"type":"bbands","period":n,"multiplier":2.0}),
        "donchian_upper" | "donchian_lower" | "donchian_mid" => json!({"type":"donchian","period":n}),
        "chandelier_long" | "chandelier_short" => json!({"type":"chandelier_exit","period":n,"multiplier":3.0}),
        "chop_angle"        => json!({"type":"chop_zone","ema_period":n,"threshold":5.0}),
        "cmf"               => json!({"type":"cmf","period":n}),
        // zero-arg
        "obv"               => json!({"type":"obv"}),
        "ao"                => json!({"type":"ao","fast":5,"slow":34}),
        "sar"               => json!({"type":"parabolic_sar","step":0.02,"max":0.2}),
        "vwap"              => json!({"type":"vwap"}),
        "bop"               => json!({"type":"bop"}),
        "coppock"           => json!({"type":"coppock"}),
        "kst_line" | "kst_sig" => json!({"type":"kst"}),
        "pmo_line" | "pmo_sig" => json!({"type":"pmo"}),
        "uo"                => json!({"type":"uo","fast":7,"medium":14,"slow":28}),
        "alligator_jaw" | "alligator_teeth" | "alligator_lips" | "alligator_bull" =>
            json!({"type":"alligator","jaw":13,"teeth":8,"lips":5}),
        "gmma_bull"         => json!({"type":"gmma"}),
        "chande_kroll_long" | "chande_kroll_short" =>
            json!({"type":"chande_kroll","atr_period":10,"factor":1.5,"stop_period":9}),
        "fractal_bull" | "fractal_bear" => json!({"type":"fractal"}),
        // fallback: try type=base
        other               => serde_json::json!({"type": other, "period": n}),
    }
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

    // Build (label, IndicatorBox, Option<TimeBarResampler>) triples.
    // Validate all configs before touching data.
    struct BoundInd {
        label:     String,
        box_:      IndicatorBox,
        resampler: Option<TimeBarResampler>,
    }

    let mut inds: Vec<BoundInd> = req.indicators
        .iter()
        .map(|cfg| {
            if let Some(cel_expr) = &cfg.cel {
                // CEL-style: parse the expression to get (base, args, interval_ms)
                let (base, args, interval_ms) = parse_cel_indicator(cel_expr)?;
                let n = args.first().copied().unwrap_or(0.0) as usize;
                // Build JSON config from CEL parsed components
                let json_cfg = cel_to_config(&base, n);
                let box_ = IndicatorBox::from_config(&json_cfg)?;
                let resampler = interval_ms.map(TimeBarResampler::new);
                let label = cfg.label.clone().unwrap_or_else(|| {
                    // Auto-label from CEL expression, e.g. "H1.ema(200)" → "H1_ema_200"
                    let tf_prefix = interval_ms.map(|_| {
                        // Extract TF prefix from expression
                        cel_expr.split('.').next()
                            .filter(|s| !s.is_empty())
                            .map(|s| format!("{}_", s))
                            .unwrap_or_default()
                    }).unwrap_or_default();
                    if args.is_empty() {
                        format!("{tf_prefix}{base}")
                    } else {
                        let arg_str: Vec<String> = args.iter().map(|a| format!("{}", *a as i64)).collect();
                        format!("{tf_prefix}{}_{}", base, arg_str.join("_"))
                    }
                });
                Ok(BoundInd { label, box_, resampler })
            } else {
                let label = cfg.label.clone()
                    .unwrap_or_else(|| auto_label(&cfg.config));
                let box_ = IndicatorBox::from_config(&Value::Object(cfg.config.clone()))?;
                Ok(BoundInd { label, box_, resampler: None })
            }
        })
        .collect::<Result<Vec<_>>>()?;

    let mut series: HashMap<String, Vec<IndicatorPoint>> =
        inds.iter().map(|b| (b.label.clone(), Vec::new())).collect();

    while let Some(bar) = feed.next() {
        for b in &mut inds {
            // MTF: feed bar through resampler; only update indicator on HTF bar emit
            let agg = match &mut b.resampler {
                Some(rs) => rs.push(&bar),
                None     => Some(bar.clone()),
            };
            if let Some(htf_bar) = agg {
                if let Some(fields) = b.box_.update(&htf_bar) {
                    series.get_mut(&b.label).unwrap().push(IndicatorPoint {
                        t: bar.timestamp,  // use base bar timestamp for chart alignment
                        fields,
                    });
                }
            }
        }
    }

    Ok(IndicatorResponse { symbol, bars: bars_total, series })
}

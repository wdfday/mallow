use std::collections::HashMap;

use wasm_bindgen::prelude::*;
use serde::Serialize;
use serde_json::json;

use alm_core::{Bar, Strategy, Timeframe};
use alm_indicator::{HeikenAshi, IndicatorBox};

mod chart_state;
pub use chart_state::ChartState;

// ── WASM init ─────────────────────────────────────────────────────────────────

#[wasm_bindgen(start)]
pub fn init() {}

// ── Shared helpers ────────────────────────────────────────────────────────────

pub(crate) fn build_bars(symbol: &str, t: &[f64], o: &[f64], h: &[f64], l: &[f64], c: &[f64], v: &[f64]) -> Vec<Bar> {
    let n = t.len().min(o.len()).min(h.len()).min(l.len()).min(c.len()).min(v.len());
    (0..n).map(|i| {
        let ts = if t[i] < 10_000_000_000.0 {
            (t[i] * 1000.0) as i64
        } else {
            t[i] as i64
        };
        Bar::new(ts, symbol, o[i], h[i], l[i], c[i], v[i])
    }).collect()
}

pub(crate) fn to_js<T: serde::Serialize>(v: &T) -> JsValue {
    v.serialize(&serde_wasm_bindgen::Serializer::json_compatible())
        .unwrap_or(JsValue::NULL)
}

pub(crate) fn js_error(msg: &str) -> JsValue {
    to_js(&json!({ "error": msg }))
}

#[derive(Serialize)]
struct OhlcvOut { t: Vec<f64>, o: Vec<f64>, h: Vec<f64>, l: Vec<f64>, c: Vec<f64>, v: Vec<f64> }

// ── Timeframe parsing ─────────────────────────────────────────────────────────

/// Parse a timeframe string to [`Timeframe`] (case-insensitive).
/// Covers all 13 variants: M1 M3 M5 M10 M15 M30 H1 H2 H4 H6 H12 D1 W1.
fn parse_tf(s: &str) -> Option<Timeframe> {
    match s.to_ascii_uppercase().as_str() {
        "M1"  => Some(Timeframe::M1),  "M3"  => Some(Timeframe::M3),
        "M5"  => Some(Timeframe::M5),  "M10" => Some(Timeframe::M10),
        "M15" => Some(Timeframe::M15), "M30" => Some(Timeframe::M30),
        "H1"  => Some(Timeframe::H1),  "H2"  => Some(Timeframe::H2),
        "H4"  => Some(Timeframe::H4),  "H6"  => Some(Timeframe::H6),
        "H12" => Some(Timeframe::H12), "D1"  => Some(Timeframe::D1),
        "W1"  => Some(Timeframe::W1),
        _ => None,
    }
}

// ── Heikin-Ashi ───────────────────────────────────────────────────────────────

/// Standard Heikin-Ashi. Returns `{t,o,h,l,c,v}` same length as input.
#[wasm_bindgen]
pub fn heikin_ashi(
    t: &[f64], o: &[f64], h: &[f64], l: &[f64], c: &[f64], v: &[f64],
) -> JsValue {
    let n = t.len().min(o.len()).min(h.len()).min(l.len()).min(c.len()).min(v.len());
    let mut ha = HeikenAshi::new(1);
    let mut out = OhlcvOut { t: vec![], o: vec![], h: vec![], l: vec![], c: vec![], v: vec![] };
    for i in 0..n {
        if let Some(b) = ha.update(o[i], h[i], l[i], c[i]) {
            out.t.push(t[i]); out.o.push(b.open); out.h.push(b.high);
            out.l.push(b.low); out.c.push(b.close); out.v.push(v[i]);
        }
    }
    to_js(&out)
}

/// EMA-smoothed Heikin-Ashi (Smoothed HA).
///
/// Mỗi thành phần OHLC được EMA(period)-smooth độc lập trước khi tính HA.
/// Warmup = `period` bar (first `period-1` bars trả về None, bị drop khỏi output).
/// FE phải dùng mảng `t` trả về để align — output ngắn hơn input `period-1` bar.
///
/// `period` phải >= 2. Truyền `period=1` là lỗi — dùng `heikin_ashi()` cho HA chuẩn.
#[wasm_bindgen]
pub fn smooth_ha(
    t: &[f64], o: &[f64], h: &[f64], l: &[f64], c: &[f64], v: &[f64],
    period: usize,
) -> JsValue {
    if period < 2 {
        return js_error("smooth_ha: period must be >= 2; use heikin_ashi() for standard (unsmoothed) HA");
    }
    let n = t.len().min(o.len()).min(h.len()).min(l.len()).min(c.len()).min(v.len());
    let mut ha = HeikenAshi::new(period);
    let mut out = OhlcvOut { t: vec![], o: vec![], h: vec![], l: vec![], c: vec![], v: vec![] };
    for i in 0..n {
        if let Some(b) = ha.update(o[i], h[i], l[i], c[i]) {
            out.t.push(t[i]); out.o.push(b.open); out.h.push(b.high);
            out.l.push(b.low); out.c.push(b.close); out.v.push(v[i]);
        }
    }
    to_js(&out)
}

// ── Indicators ────────────────────────────────────────────────────────────────

/// Compute indicators over a bar series.
///
/// `config_json`: `{ label: { "type": "ema", "period": 20, ... } }`
/// Returns: `{ label: { field: (number|null)[] } }`
#[wasm_bindgen]
pub fn run_indicators(
    symbol: &str,
    t: &[f64], o: &[f64], h: &[f64], l: &[f64], c: &[f64], v: &[f64],
    config_json: &str,
) -> JsValue {
    let bars = build_bars(symbol, t, o, h, l, c, v);
    let config: serde_json::Value = match serde_json::from_str(config_json) {
        Ok(v) => v,
        Err(e) => return js_error(&e.to_string()),
    };
    let obj = match config.as_object() {
        Some(o) => o,
        None => return js_error("config must be a JSON object"),
    };

    let mut boxes: Vec<(String, IndicatorBox)> = Vec::new();
    for (name, cfg) in obj {
        match IndicatorBox::from_config(cfg) {
            Ok(ind) => boxes.push((name.clone(), ind)),
            Err(e) => return js_error(&format!("indicator '{name}': {e}")),
        }
    }

    let n = bars.len();
    let mut raw: Vec<Vec<Option<HashMap<String, f64>>>> =
        boxes.iter().map(|_| Vec::with_capacity(n)).collect();
    for bar in &bars {
        for (i, (_, ind)) in boxes.iter_mut().enumerate() {
            raw[i].push(ind.update(bar));
        }
    }

    let mut result = serde_json::Map::new();
    for (i, (label, _)) in boxes.iter().enumerate() {
        let rows = &raw[i];
        let fields: Vec<String> = rows.iter().find_map(|r| r.as_ref())
            .map(|m| { let mut ks: Vec<_> = m.keys().cloned().collect(); ks.sort(); ks })
            .unwrap_or_default();
        let mut ind_obj = serde_json::Map::new();
        for field in &fields {
            let vals: Vec<serde_json::Value> = rows.iter().map(|row| {
                match row.as_ref().and_then(|m| m.get(field)).copied() {
                    Some(v) if v.is_finite() =>
                        serde_json::Number::from_f64(v).map(serde_json::Value::Number).unwrap_or(serde_json::Value::Null),
                    _ => serde_json::Value::Null,
                }
            }).collect();
            ind_obj.insert(field.clone(), serde_json::Value::Array(vals));
        }
        result.insert(label.clone(), serde_json::Value::Object(ind_obj));
    }
    to_js(&serde_json::Value::Object(result))
}

/// List all indicator type strings accepted by `run_indicators`.
#[wasm_bindgen]
pub fn list_indicators() -> JsValue {
    let names = vec![
        "sma","ema","wma","hma","dema","tema","smma","alma","lsma","kama","mcginley","vwma",
        "kdj","kalman","aroon","aroon_osc","adx","dmi","macd","trix","vortex",
        "rsi","cci","roc","mfi","williams_r","stochastic","tsi","connors_rsi","cmo","ppo",
        "pmo","kst","dpo","coppock","ao","bop","bull_bear_power","uo","smi","rvi","fisher","rci",
        "obv","cmf","vwap","bbands","keltner","donchian",
        "ichimoku","elder_ray","rwi","stoch_rsi","williams_fractal",
        "alligator","gmma","heikin_ashi","chop","chop_zone","volatility_ratio",
        "atr","supertrend","parabolic_sar","chandelier_exit","chande_kroll",
    ];
    to_js(&names)
}

// ── Sizing helper (shared between single-TF and MTF engines) ─────────────────

pub(crate) fn build_sizer(cfg: &BtConfig) -> alm_engine::risk::AnySizer {
    use alm_engine::risk::{AnySizer, AtrSizing, FixedFractional, FixedQuantity, FixedUsd, RiskFractional};
    let max_slots = cfg.max_units.max(1);
    let pct = cfg.size_pct.clamp(0.01, 1.0);
    let risk = cfg.risk_per_trade.unwrap_or(0.01);
    let str_ = cfg.strength_sizing;
    match cfg.size_mode.as_deref() {
        Some("fixed_qty")        => AnySizer::FixedQuantity(FixedQuantity::new(cfg.size_qty.unwrap_or(1.0), max_slots)),
        Some("quote_qty")        => AnySizer::FixedUsd(FixedUsd::new(cfg.size_usd.unwrap_or(0.0), max_slots)),
        Some("volatility")       => AnySizer::Atr(AtrSizing::new(risk, cfg.atr_mult, max_slots).with_strength_sizing(false)),
        Some("fixed_fractional") => AnySizer::RiskFractional(RiskFractional::new(risk, max_slots).with_strength_sizing(false)),
        Some("percent_equity")   => AnySizer::FixedFractional(FixedFractional::fractional(pct, max_slots).with_strength_sizing(str_)),
        _ => {
            if let Some(qty) = cfg.size_qty {
                AnySizer::FixedQuantity(FixedQuantity::new(qty, max_slots))
            } else if let Some(usd) = cfg.size_usd {
                AnySizer::FixedUsd(FixedUsd::new(usd, max_slots))
            } else if let Some(r) = cfg.risk_per_trade {
                AnySizer::Atr(AtrSizing::new(r, cfg.atr_mult, max_slots).with_strength_sizing(false))
            } else {
                AnySizer::FixedFractional(FixedFractional::fractional(pct, max_slots).with_strength_sizing(str_))
            }
        }
    }
}

// ── Portfolio output helper ───────────────────────────────────────────────────

fn collect_trades(portfolio: &alm_core::Portfolio) -> Vec<serde_json::Value> {
    portfolio.trades.iter()
        .map(|t| json!({
            "entry_ts": t.entry_timestamp, "exit_ts": t.exit_timestamp,
            "entry_price": t.entry_price,  "exit_price": t.exit_price,
            "qty": t.qty, "pnl": t.pnl,
            "pnl_pct": t.pnl_pct,  // fraction — FE ×100
            "commission": t.commission,
            "mae_pct": t.mae_pct, "mfe_pct": t.mfe_pct,
            "bars_held": t.bars_held,
            "exit_reason": t.exit_reason.to_string(),
            "side": format!("{:?}", t.side).to_lowercase(),
        }))
        .collect()
}

fn collect_fills(portfolio: &alm_core::Portfolio) -> Vec<serde_json::Value> {
    portfolio.fills.iter()
        .map(|f| {
            let leg = f.symbol.split('#').nth(1).and_then(|s| s.parse::<usize>().ok())
                .map(|n| n.saturating_sub(1)).unwrap_or(0);
            json!({
                "t": f.timestamp, "price": f.price, "qty": f.qty,
                "side": format!("{:?}", f.side).to_lowercase(),
                "sym": f.symbol, "leg": leg,
            })
        })
        .collect()
}

/// Build the full nested BacktestResponse JSON that the FE `BacktestReport` interface expects.
///
/// The flat `alm_report::BacktestReport` fields are mapped to the same nested sections
/// produced by the server-side `engine/src/backtest/response.rs::build()`.
/// Legacy flat aliases (`total_return_pct`, `sharpe_ratio`, `max_drawdown_pct`,
/// `win_rate_pct`, `total_trades`) are kept at the top level for backward compat.
fn assemble_nested_output(
    report: alm_report::BacktestReport,
    portfolio: &alm_core::Portfolio,
    trades: Vec<serde_json::Value>,
    fills: Vec<serde_json::Value>,
    ind_map: serde_json::Map<String, serde_json::Value>,
) -> serde_json::Value {
    // curves.equity — {t, v} format matching CurvePoint interface
    let equity_curve: Vec<serde_json::Value> = portfolio.equity_curve.iter()
        .map(|p| json!({ "t": p.timestamp, "v": p.equity }))
        .collect();

    // curves.drawdown — peak-tracked drawdown as fraction at each bar
    let mut peak = portfolio.initial_capital;
    let drawdown_curve: Vec<serde_json::Value> = portfolio.equity_curve.iter()
        .map(|p| {
            if p.equity > peak { peak = p.equity; }
            let dd = if peak > 0.0 { (p.equity - peak) / peak } else { 0.0 };
            json!({ "t": p.timestamp, "v": dd })
        })
        .collect();

    // exposure_pct: fraction of backtest duration spent in a position
    let exposure_pct = {
        let total_ms = portfolio.equity_curve
            .last()
            .and_then(|last| portfolio.equity_curve.first().map(|f| last.timestamp - f.timestamp))
            .unwrap_or(0);
        let held_ms: i64 = portfolio.trades.iter()
            .map(|t| t.exit_timestamp - t.entry_timestamp)
            .sum();
        if total_ms > 0 { held_ms as f64 / total_ms as f64 * 100.0 } else { 0.0 }
    };

    // monthly_returns: (year, month, pct) → [year, month, pct]
    let monthly: Vec<[f64; 3]> = report.monthly_returns.iter()
        .map(|&(y, m, r)| [y as f64, m as f64, r])
        .collect();
    // yearly_returns: (year, pct) → [year, pct]
    let yearly: Vec<[f64; 2]> = report.yearly_returns.iter()
        .map(|&(y, r)| [y as f64, r])
        .collect();

    json!({
        "strategy": report.strategy,
        "symbol": report.symbol,
        "timeframe": report.timeframe.to_string(),
        "bar_count": portfolio.equity_curve.len(),

        // Legacy flat aliases — kept so any FE path that reads these directly still works
        "total_return_pct": report.total_return_pct,
        "sharpe_ratio": report.sharpe_ratio,
        "max_drawdown_pct": report.max_drawdown_pct,
        "win_rate_pct": report.win_rate_pct,
        "total_trades": report.total_trades,

        // ── Nested sections matching BacktestResponse / FE BacktestReport interface ──

        "capital": {
            "initial": report.initial_capital,
            "final_equity": report.final_equity,
        },
        "returns": {
            "total_pct": report.total_return_pct,
            "cagr_pct": report.cagr_pct,
            "annualized_volatility_pct": report.annualized_volatility_pct,
        },
        "risk_adjusted": {
            "sharpe":          report.sharpe_ratio,
            "sortino":         report.sortino_ratio,
            "calmar":          report.calmar_ratio,
            "serenity":        report.serenity_ratio,
            "omega":           report.omega_ratio,
            "tail_ratio":      report.tail_ratio,
            "recovery_factor": report.recovery_factor,
            "var_95":          report.var_95,
            "cvar_95":         report.cvar_95,
        },
        "drawdown": {
            "max_pct":           report.max_drawdown_pct,
            "max_duration_bars": report.max_dd_duration_bars,
            "avg_pct":           report.avg_drawdown_pct,
            "ulcer_index":       report.ulcer_index,
        },
        "trade_stats": {
            "total":                   report.total_trades,
            "win_rate_pct":            report.win_rate_pct,
            "profit_factor":           report.profit_factor,
            "payoff_ratio":            report.payoff_ratio,
            "expectancy":              report.expectancy,
            "breakeven_win_rate_pct":  report.breakeven_win_rate_pct,
            "gross_profit_usd":        report.gross_profit_usd,
            "gross_loss_usd":          report.gross_loss_usd,
            "avg_win_pct":             report.avg_win_pct,
            "avg_loss_pct":            report.avg_loss_pct,
            "avg_duration_hours":      report.avg_trade_duration_hours,
            "avg_bars_held_winners":   report.avg_bars_held_winners,
            "avg_bars_held_losers":    report.avg_bars_held_losers,
            "max_consecutive_wins":    report.max_consecutive_wins,
            "max_consecutive_losses":  report.max_consecutive_losses,
            "largest_win_pct":         report.largest_win_pct,
            "largest_loss_pct":        report.largest_loss_pct,
            "mfe_capture_ratio":       report.mfe_capture_ratio,
            "exit_reasons": {
                "signal":        report.exit_reasons.signal,
                "stop_loss":     report.exit_reasons.stop_loss,
                "take_profit":   report.exit_reasons.take_profit,
                "trailing_stop": report.exit_reasons.trailing_stop,
                "max_bars":      report.exit_reasons.max_bars,
                "end_of_data":   report.exit_reasons.end_of_data,
            },
        },
        "distribution": {
            "skewness":        report.skewness,
            "excess_kurtosis": report.excess_kurtosis,
            "sqn":             report.sqn,
            "psr":             report.psr,
        },
        "long_short": {
            "long_trades":        report.long_stats.count,
            "long_win_rate_pct":  report.long_stats.win_rate * 100.0,
            "long_profit_factor": report.long_stats.profit_factor,
            "short_trades":       report.short_stats.count,
            "short_win_rate_pct": report.short_stats.win_rate * 100.0,
            "short_profit_factor": report.short_stats.profit_factor,
        },
        "excursion": {
            "avg_mae_pct":  report.avg_mae_pct,
            "avg_mfe_pct":  report.avg_mfe_pct,
            "mae_mfe_ratio": report.mae_mfe_ratio,
        },
        "activity": {
            "trades_per_year":     report.trades_per_year,
            "exposure_pct":        exposure_pct,
            "total_commission_usd": report.total_commission_paid,
            "kelly_pct":           report.kelly_pct,
        },
        "curves": {
            "equity":            equity_curve,
            "drawdown":          drawdown_curve,
            "rolling_sharpe":    report.rolling_sharpe,
            "rolling_sharpe_std": report.rolling_sharpe_std,
            "rolling_drawdown":  report.rolling_drawdown,
        },
        "calendar": {
            "monthly_returns": monthly,
            "yearly_returns":  yearly,
        },
        "regime_summary": report.regime_summary,

        "trades":           trades,
        "fills":            fills,
        "indicator_series": serde_json::Value::Object(ind_map),
    })
}

// ── Single-TF backtest engine ─────────────────────────────────────────────────

pub(crate) fn run_strategy(
    symbol: &str,
    bars: &[Bar],
    strategy: Box<dyn Strategy>,
    cfg: &BtConfig,
) -> serde_json::Value {
    use alm_engine::Engine;
    use alm_data::BarVecFeed;

    fn intra_bar_mode_from_str(s: Option<&str>) -> alm_core::exit::IntraBarMode {
        match s {
            Some("pessimistic") => alm_core::exit::IntraBarMode::Pessimistic,
            Some("ohlc_heuristic") => alm_core::exit::IntraBarMode::OhlcHeuristic,
            _ => alm_core::exit::IntraBarMode::Pessimistic,
        }
    }

    let sizer = build_sizer(cfg);
    let mut engine = Engine::sync(cfg.capital, strategy, sizer, cfg.commission, cfg.slippage);

    // Apply intra_bar_mode
    let intra_bar_mode = intra_bar_mode_from_str(cfg.intra_bar_mode.as_deref());
    engine = engine.with_intra_bar_mode(intra_bar_mode);

    // Apply reverse_policy
    if let Some(ref policy_str) = cfg.reverse_policy {
        let policy = match policy_str.as_str() {
            "exit" => Some(alm_engine::ReversePolicy::Exit),
            "flip" => Some(alm_engine::ReversePolicy::Flip),
            _ => None,
        };
        if let Some(p) = policy {
            engine = engine.with_reverse_policy(p);
        }
    }

    if cfg.max_units > 1 {
        engine = engine.with_pyramiding(cfg.max_units, cfg.max_position_pct);
        if !cfg.pyramid {
            engine = engine.with_independent_legs();
        }
    }
    let mut feed = BarVecFeed::new(bars.to_vec(), symbol.to_string());
    let report = engine.run(&mut feed, 0.0);

    let trades = collect_trades(&engine.core.portfolio);
    let fills  = collect_fills(&engine.core.portfolio);

    let mut ind_map = serde_json::Map::new();
    for (name, pts) in engine.strategy.take_indicator_series() {
        ind_map.insert(name, json!({
            "t": pts.iter().map(|(t, _)| *t).collect::<Vec<_>>(),
            "v": pts.iter().map(|(_, v)| *v).collect::<Vec<_>>(),
        }));
    }

    assemble_nested_output(report, &engine.core.portfolio, trades, fills, ind_map)
}

// ── Config / strategy catalog ─────────────────────────────────────────────────

pub(crate) struct BtConfig {
    pub capital: f64,
    pub size_pct: f64,
    pub size_usd: Option<f64>,
    pub size_qty: Option<f64>,
    pub risk_per_trade: Option<f64>,
    pub atr_mult: f64,
    pub strength_sizing: bool,
    pub size_mode: Option<String>,
    pub commission: f64,
    pub slippage: f64,
    pub max_units: usize,
    pub max_position_pct: f64,
    pub pyramid: bool,
    pub reverse_policy: Option<String>,
    pub intra_bar_mode: Option<String>,
}

pub(crate) fn parse_config(config_json: &str) -> BtConfig {
    let v: serde_json::Value = serde_json::from_str(config_json).unwrap_or(json!({}));
    let get = |key: &str, default: f64| v.get(key).and_then(|v| v.as_f64()).unwrap_or(default);
    let opt = |key: &str| v.get(key).and_then(|x| x.as_f64()).filter(|n| *n > 0.0);
    BtConfig {
        capital:    get("initial_capital",   10_000.0),
        size_pct:   get("position_size_pct", 1.0),
        size_usd:   opt("position_size_usd"),
        size_qty:   opt("position_size_quantity"),
        risk_per_trade: opt("risk_per_trade_pct"),
        atr_mult:   get("atr_multiplier", 2.0),
        strength_sizing: v.get("strength_sizing").and_then(|x| x.as_bool()).unwrap_or(true),
        size_mode: v.get("size_mode").and_then(|x| x.as_str()).map(String::from),
        commission: get("commission_pct",    0.001),
        slippage:   get("slippage_pct",      0.0005),
        max_units:  get("max_units", 1.0).max(1.0) as usize,
        max_position_pct: get("max_position_pct", 0.0),
        pyramid: v.get("pyramid").map(|x| x.as_bool().unwrap_or_else(|| x.as_f64().unwrap_or(1.0) != 0.0)).unwrap_or(true),
        reverse_policy: v.get("reverse_policy").and_then(|x| x.as_str()).map(String::from),
        intra_bar_mode: v.get("intra_bar_mode").and_then(|x| x.as_str()).map(String::from),
    }
}

/// List all single-TF named strategy keys usable with `ChartState::backtest_named`.
#[wasm_bindgen]
pub fn list_strategies() -> JsValue {
    use alm_strategy::catalog::STRATEGY_KEYS;
    to_js(&STRATEGY_KEYS)
}

/// List all multi-TF named strategy keys usable with `run_mtf_backtest`.
#[wasm_bindgen]
pub fn list_mtf_strategies() -> JsValue {
    use alm_strategy::MTF_STRATEGY_KEYS;
    to_js(&MTF_STRATEGY_KEYS)
}

/// Full indicator catalog for editor hints: `[{name, label, category, description,
/// params:[{name,type,default}], outputs:[{name,type}]}, ...]`.
#[wasm_bindgen]
pub fn indicator_catalog() -> JsValue {
    to_js(&alm_strategy::catalog::all())
}

/// Lint a strategy script client-side (no server round-trip).
///
/// Returns `{ errors: [{line, col, message, severity}], scope: {...} }`.
/// `base_tf`: `"M1"` / `"H4"` / etc., or empty string to skip TF checks.
/// Supports all 13 timeframes: M1 M3 M5 M10 M15 M30 H1 H2 H4 H6 H12 D1 W1.
#[wasm_bindgen]
pub fn validate_script(script: &str, base_tf: &str) -> JsValue {
    use alm_strategy::script_lint;
    let tf = if base_tf.is_empty() { None } else { parse_tf(base_tf) };
    let (errors, scope) = script_lint(script, tf);
    to_js(&json!({ "errors": errors, "scope": scope }))
}

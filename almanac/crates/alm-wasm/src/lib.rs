use std::collections::HashMap;

use wasm_bindgen::prelude::*;
use serde::Serialize;
use serde_json::json;

use alm_core::{Bar, Strategy, signal::Direction};
use alm_indicator::{HeikenAshi, IndicatorBox};
use alm_strategy::build_strategy;

// ── WASM init ─────────────────────────────────────────────────────────────────

#[wasm_bindgen(start)]
pub fn init() {
    // Redirect Rust panics to the browser console on debug builds.
    #[cfg(feature = "console_error_panic_hook")]
    console_error_panic_hook::set_once();
}

// ── Shared helpers ────────────────────────────────────────────────────────────

fn build_bars(symbol: &str, t: &[f64], o: &[f64], h: &[f64], l: &[f64], c: &[f64], v: &[f64]) -> Vec<Bar> {
    let n = t.len().min(o.len()).min(h.len()).min(l.len()).min(c.len()).min(v.len());
    (0..n).map(|i| Bar::new(t[i] as i64, symbol, o[i], h[i], l[i], c[i], v[i])).collect()
}

fn js_error(msg: &str) -> JsValue {
    serde_wasm_bindgen::to_value(&json!({ "error": msg })).unwrap_or(JsValue::NULL)
}

#[derive(Serialize)]
struct OhlcvOut { t: Vec<f64>, o: Vec<f64>, h: Vec<f64>, l: Vec<f64>, c: Vec<f64>, v: Vec<f64> }

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
    serde_wasm_bindgen::to_value(&out).unwrap_or(JsValue::NULL)
}

/// EMA-smoothed Heikin-Ashi. Warmup bars trimmed from output.
#[wasm_bindgen]
pub fn smooth_ha(
    t: &[f64], o: &[f64], h: &[f64], l: &[f64], c: &[f64], v: &[f64],
    period: usize,
) -> JsValue {
    let n = t.len().min(o.len()).min(h.len()).min(l.len()).min(c.len()).min(v.len());
    let mut ha = HeikenAshi::new(period.max(2));
    let mut out = OhlcvOut { t: vec![], o: vec![], h: vec![], l: vec![], c: vec![], v: vec![] };
    for i in 0..n {
        if let Some(b) = ha.update(o[i], h[i], l[i], c[i]) {
            out.t.push(t[i]); out.o.push(b.open); out.h.push(b.high);
            out.l.push(b.low); out.c.push(b.close); out.v.push(v[i]);
        }
    }
    serde_wasm_bindgen::to_value(&out).unwrap_or(JsValue::NULL)
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
    serde_wasm_bindgen::to_value(&serde_json::Value::Object(result)).unwrap_or(JsValue::NULL)
}

/// List all indicator type strings accepted by `run_indicators`.
#[wasm_bindgen]
pub fn list_indicators() -> JsValue {
    let names = vec![
        "sma","ema","wma","hma","dema","tema","smma","alma","lsma","kama","mcginley","vwma",
        "kdj","kalman","aroon","adx","dmi","macd","trix","vortex",
        "rsi","cci","roc","mfi","williams_r","stochastic","tsi","connors_rsi","cmo","ppo",
        "pmo","kst","dpo","coppock","ao","bop","bull_bear_power","uo","smi","rvi","fisher","rci",
        "obv","cmf","vwap","bbands","keltner","donchian",
        "ichimoku","elder_ray","rwi","stoch_rsi","williams_fractal",
        "alligator","gmma","heikin_ashi","chop","chop_zone","volatility_ratio",
        "atr","supertrend","parabolic_sar","chandelier_exit","chande_kroll",
    ];
    serde_wasm_bindgen::to_value(&names).unwrap_or(JsValue::NULL)
}

// ── Mini backtest engine ──────────────────────────────────────────────────────

#[derive(Serialize)]
struct TradeOut {
    entry_ts:    i64,
    exit_ts:     i64,
    entry_price: f64,
    exit_price:  f64,
    pnl_pct:     f64,
    side:        &'static str,
}

#[derive(Serialize)]
struct EquityPoint { t: i64, equity: f64 }

#[derive(Serialize)]
struct BacktestOut {
    total_return_pct:  f64,
    max_drawdown_pct:  f64,
    win_rate_pct:      f64,
    profit_factor:     f64,
    sharpe_ratio:      f64,
    total_trades:      usize,
    equity_curve:      Vec<EquityPoint>,
    trades:            Vec<TradeOut>,
    indicator_series:  serde_json::Value,
}

/// Run a bar-by-bar backtest using a named strategy or Rhai script.
///
/// Config (JSON string):
/// ```json
/// {
///   "initial_capital": 10000,
///   "position_size_pct": 1.0,
///   "commission_pct": 0.001,
///   "slippage_pct": 0.0005
/// }
/// ```
fn run_strategy(
    _symbol: &str,
    bars: &[Bar],
    strategy: &mut dyn Strategy,
    initial_capital: f64,
    position_size_pct: f64,
    commission_pct: f64,
    slippage_pct: f64,
) -> BacktestOut {
    let mut equity = initial_capital;
    let mut position: f64 = 0.0;   // qty held
    let mut entry_price: f64 = 0.0;
    let mut entry_ts: i64 = 0;

    let mut peak = equity;
    let mut max_dd: f64 = 0.0;
    let mut equity_curve: Vec<EquityPoint> = Vec::with_capacity(bars.len());
    let mut trades: Vec<TradeOut> = Vec::new();

    for bar in bars {
        let signals = strategy.on_bar(bar);

        for sig in &signals {
            match sig.direction {
                Direction::Long if position == 0.0 => {
                    let fill = bar.close * (1.0 + slippage_pct);
                    let alloc = equity * position_size_pct;
                    let cost = alloc * commission_pct;
                    position = (alloc - cost) / fill;
                    entry_price = fill;
                    entry_ts = bar.timestamp;
                    equity -= cost;
                }
                Direction::Close | Direction::Short if position > 0.0 => {
                    let fill = bar.close * (1.0 - slippage_pct);
                    let proceeds = position * fill;
                    let cost = proceeds * commission_pct;
                    let pnl_pct = (fill - entry_price) / entry_price * 100.0;
                    equity = equity - position * entry_price + proceeds - cost;
                    trades.push(TradeOut {
                        entry_ts, exit_ts: bar.timestamp,
                        entry_price, exit_price: fill,
                        pnl_pct, side: "long",
                    });
                    position = 0.0;
                }
                _ => {}
            }
        }

        // Mark-to-market equity
        let mark = if position > 0.0 {
            equity - position * entry_price + position * bar.close
        } else {
            equity
        };
        peak = peak.max(mark);
        if peak > 0.0 { max_dd = max_dd.max((peak - mark) / peak * 100.0); }
        equity_curve.push(EquityPoint { t: bar.timestamp, equity: mark });
    }

    // Close open position at last bar
    if position > 0.0 {
        if let Some(last) = bars.last() {
            let fill = last.close * (1.0 - slippage_pct);
            let proceeds = position * fill;
            let cost = proceeds * commission_pct;
            let pnl_pct = (fill - entry_price) / entry_price * 100.0;
            equity = equity - position * entry_price + proceeds - cost;
            trades.push(TradeOut {
                entry_ts, exit_ts: last.timestamp,
                entry_price, exit_price: fill,
                pnl_pct, side: "long",
            });
        }
    }

    let total_return_pct = (equity / initial_capital - 1.0) * 100.0;
    let total_trades = trades.len();
    let wins = trades.iter().filter(|t| t.pnl_pct > 0.0).count();
    let win_rate_pct = if total_trades > 0 { wins as f64 / total_trades as f64 * 100.0 } else { 0.0 };
    let gross_profit: f64 = trades.iter().filter(|t| t.pnl_pct > 0.0).map(|t| t.pnl_pct).sum();
    let gross_loss: f64 = trades.iter().filter(|t| t.pnl_pct <= 0.0).map(|t| t.pnl_pct.abs()).sum();
    let profit_factor = if gross_loss > 0.0 { gross_profit / gross_loss } else { f64::INFINITY };

    // Sharpe: annualise bar returns from equity curve
    let sharpe_ratio = if equity_curve.len() > 2 {
        let rets: Vec<f64> = equity_curve.windows(2)
            .map(|w| (w[1].equity - w[0].equity) / w[0].equity)
            .collect();
        let mean = rets.iter().sum::<f64>() / rets.len() as f64;
        let var: f64 = rets.iter().map(|r| (r - mean).powi(2)).sum::<f64>() / rets.len() as f64;
        let std = var.sqrt();
        if std > 0.0 { mean / std * (252_f64).sqrt() } else { 0.0 }
    } else { 0.0 };

    // indicator_series from strategy.take_indicator_series()
    let ind_series = strategy.take_indicator_series();
    let mut ind_map = serde_json::Map::new();
    for (name, pts) in ind_series {
        let ts: Vec<i64> = pts.iter().map(|(t, _)| *t).collect();
        let vs: Vec<f64> = pts.iter().map(|(_, v)| *v).collect();
        ind_map.insert(name, json!({ "t": ts, "v": vs }));
    }

    BacktestOut {
        total_return_pct, max_drawdown_pct: max_dd,
        win_rate_pct, profit_factor,
        sharpe_ratio: if sharpe_ratio.is_finite() { sharpe_ratio } else { 0.0 },
        total_trades, equity_curve, trades,
        indicator_series: serde_json::Value::Object(ind_map),
    }
}

/// Run a named strategy backtest client-side.
///
/// `strategy_name`: any name from `list_strategies()` (e.g. `"ema_cross"`, `"rsi_mean_rev"`)
/// `params_json`:   `{"period": 14, ...}`
/// `config_json`:   `{"initial_capital": 10000, "position_size_pct": 1.0, ...}`
#[wasm_bindgen]
pub fn run_backtest(
    symbol: &str,
    strategy_name: &str,
    params_json: &str,
    t: &[f64], o: &[f64], h: &[f64], l: &[f64], c: &[f64], v: &[f64],
    config_json: &str,
) -> JsValue {
    let params: serde_json::Value = match serde_json::from_str(params_json) {
        Ok(v) => v,
        Err(e) => return js_error(&format!("params: {e}")),
    };
    let mut strategy = match build_strategy(strategy_name, &params) {
        Ok(s) => s,
        Err(e) => return js_error(&format!("strategy '{strategy_name}': {e}")),
    };
    let bars = build_bars(symbol, t, o, h, l, c, v);
    let (cap, size, comm, slip) = parse_config(config_json);
    let out = run_strategy(symbol, &bars, strategy.as_mut(), cap, size, comm, slip);
    serde_wasm_bindgen::to_value(&out).unwrap_or(JsValue::NULL)
}

/// Run a Rhai script backtest client-side.
///
/// `script`: Rhai script (same syntax as herald `/api/v1/backtest/rhai`)
/// `config_json`: `{"initial_capital": 10000, "position_size_pct": 1.0, ...}`
#[wasm_bindgen]
pub fn run_rhai_backtest(
    symbol: &str,
    script: &str,
    t: &[f64], o: &[f64], h: &[f64], l: &[f64], c: &[f64], v: &[f64],
    config_json: &str,
) -> JsValue {
    let mut strategy = match build_strategy("rhai", &json!({ "script": script })) {
        Ok(s) => s,
        Err(e) => return js_error(&format!("rhai strategy: {e}")),
    };
    let bars = build_bars(symbol, t, o, h, l, c, v);
    let (cap, size, comm, slip) = parse_config(config_json);
    let out = run_strategy(symbol, &bars, strategy.as_mut(), cap, size, comm, slip);
    serde_wasm_bindgen::to_value(&out).unwrap_or(JsValue::NULL)
}

fn parse_config(config_json: &str) -> (f64, f64, f64, f64) {
    let v: serde_json::Value = serde_json::from_str(config_json).unwrap_or(json!({}));
    let get = |key: &str, default: f64| v.get(key).and_then(|v| v.as_f64()).unwrap_or(default);
    (
        get("initial_capital",   10_000.0),
        get("position_size_pct", 1.0),
        get("commission_pct",    0.001),
        get("slippage_pct",      0.0005),
    )
}

/// List all named strategy keys usable with `run_backtest`.
#[wasm_bindgen]
pub fn list_strategies() -> JsValue {
    use alm_strategy::catalog::STRATEGY_KEYS;
    serde_wasm_bindgen::to_value(&STRATEGY_KEYS).unwrap_or(JsValue::NULL)
}

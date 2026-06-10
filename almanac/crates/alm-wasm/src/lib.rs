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
    (0..n).map(|i| Bar::new(t[i] as i64, symbol, o[i], h[i], l[i], c[i], v[i])).collect()
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

pub(crate) fn build_sizer(cfg: &BtConfig) -> alm_strategy::risk::AnySizer {
    use alm_strategy::risk::{AnySizer, AtrSizing, FixedFractional, FixedQuantity, FixedUsd, RiskFractional};
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

// ── Portfolio output helper (shared between Engine and MtfEngine) ─────────────

/// Collect equity_curve / trades / fills from a completed engine portfolio into
/// serialisable Vecs. Both `Engine` and `MtfEngine` expose `pub portfolio: Portfolio`.
fn collect_portfolio(portfolio: &alm_core::Portfolio) -> (
    Vec<serde_json::Value>,
    Vec<serde_json::Value>,
    Vec<serde_json::Value>,
) {
    let equity_curve = portfolio.equity_curve.iter()
        .map(|p| json!({ "t": p.timestamp, "equity": p.equity }))
        .collect();
    let trades = portfolio.trades.iter()
        .map(|t| json!({
            "entry_ts": t.entry_timestamp, "exit_ts": t.exit_timestamp,
            "entry_price": t.entry_price,   "exit_price": t.exit_price,
            "qty": t.qty, "pnl": t.pnl,
            "pnl_pct":  t.pnl_pct,  // fraction — FE ×100
            "commission": t.commission,
            "mae_pct": t.mae_pct, "mfe_pct": t.mfe_pct,
            "bars_held": t.bars_held,
            "exit_reason": t.exit_reason.to_string(),
            "side": format!("{:?}", t.side).to_lowercase(),
        }))
        .collect();
    let fills = portfolio.fills.iter()
        .map(|f| {
            let leg = f.symbol.split('#').nth(1).and_then(|s| s.parse::<usize>().ok())
                .map(|n| n.saturating_sub(1)).unwrap_or(0);
            json!({
                "t": f.timestamp, "price": f.price, "qty": f.qty,
                "side": format!("{:?}", f.side).to_lowercase(),
                "sym": f.symbol, "leg": leg,
            })
        })
        .collect();
    (equity_curve, trades, fills)
}

/// Assemble the final backtest JSON from a report + portfolio output + indicator series.
fn assemble_output(
    report: serde_json::Value,
    equity_curve: Vec<serde_json::Value>,
    trades: Vec<serde_json::Value>,
    fills: Vec<serde_json::Value>,
    ind_map: serde_json::Map<String, serde_json::Value>,
) -> serde_json::Value {
    let mut out = report;
    if let serde_json::Value::Object(ref mut m) = out {
        m.insert("equity_curve".into(), json!(equity_curve));
        m.insert("trades".into(), json!(trades));
        m.insert("fills".into(), json!(fills));
        m.insert("indicator_series".into(), serde_json::Value::Object(ind_map));
    }
    out
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

    let sizer = build_sizer(cfg);
    let mut engine = Engine::sync(cfg.capital, strategy, sizer, cfg.commission, cfg.slippage);
    if cfg.max_units > 1 {
        engine = engine.with_pyramiding(cfg.max_units, cfg.max_position_pct);
        if !cfg.pyramid {
            engine = engine.with_independent_legs();
        }
    }
    let mut feed = BarVecFeed::new(bars.to_vec(), symbol.to_string());
    let report = engine.run(&mut feed, 0.0);

    let (equity_curve, trades, fills) = collect_portfolio(&engine.portfolio);

    let mut ind_map = serde_json::Map::new();
    for (name, pts) in engine.strategy.take_indicator_series() {
        ind_map.insert(name, json!({
            "t": pts.iter().map(|(t, _)| *t).collect::<Vec<_>>(),
            "v": pts.iter().map(|(_, v)| *v).collect::<Vec<_>>(),
        }));
    }

    assemble_output(
        serde_json::to_value(&report).unwrap_or_else(|_| json!({})),
        equity_curve, trades, fills, ind_map,
    )
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

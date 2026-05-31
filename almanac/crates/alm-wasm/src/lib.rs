use std::collections::HashMap;

use wasm_bindgen::prelude::*;
use serde::Serialize;
use serde_json::json;

use alm_core::{Bar, Strategy};
use alm_indicator::{HeikenAshi, IndicatorBox};
use alm_strategy::build_strategy;

mod chart_state;
pub use chart_state::ChartState;

// ── WASM init ─────────────────────────────────────────────────────────────────

#[wasm_bindgen(start)]
pub fn init() {
    // Redirect Rust panics to the browser console on debug builds.
    #[cfg(feature = "console_error_panic_hook")]
    console_error_panic_hook::set_once();
}

// ── Shared helpers ────────────────────────────────────────────────────────────

pub(crate) fn build_bars(symbol: &str, t: &[f64], o: &[f64], h: &[f64], l: &[f64], c: &[f64], v: &[f64]) -> Vec<Bar> {
    let n = t.len().min(o.len()).min(h.len()).min(l.len()).min(c.len()).min(v.len());
    (0..n).map(|i| Bar::new(t[i] as i64, symbol, o[i], h[i], l[i], c[i], v[i])).collect()
}

/// Serialize any `Serialize` value to `JsValue` using json-compatible mode
/// (maps → plain JS objects, not JS `Map`). Required for serde-wasm-bindgen 0.6+
/// where the default changed to emitting JS `Map` for HashMap/BTreeMap.
pub(crate) fn to_js<T: serde::Serialize>(v: &T) -> JsValue {
    v.serialize(&serde_wasm_bindgen::Serializer::json_compatible())
        .unwrap_or(JsValue::NULL)
}

pub(crate) fn js_error(msg: &str) -> JsValue {
    to_js(&json!({ "error": msg }))
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
    to_js(&out)
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

// ── Mini backtest engine ──────────────────────────────────────────────────────

/// Run an in-memory backtest through the real [`alm_engine::Engine`] — the same
/// engine + [`alm_report::BacktestReport`] the backend uses, so on-chart results
/// match the server (minus Monte-Carlo / walk-forward, which stay server-side).
///
/// Config (JSON): `{ "initial_capital", "position_size_pct", "commission_pct", "slippage_pct" }`.
/// Returns the full report (all metrics, equity curve, trades, regime summary) plus
/// `indicator_series` for chart overlay.
pub(crate) fn run_strategy(
    symbol: &str,
    bars: &[Bar],
    strategy: Box<dyn Strategy>,
    cfg: &BtConfig,
) -> serde_json::Value {
    use alm_engine::Engine;
    use alm_strategy::risk::{AnySizer, AtrSizing, FixedFractional, FixedQuantity, FixedUsd, RiskFractional};
    use alm_data::BarVecFeed;

    // Mirror alm-engine `build_sizer`: explicit size_mode (helm SizeMode) first,
    // else legacy field inference (qty > usd > risk(ATR) > pct).
    let max_slots = cfg.max_units.max(1);
    let pct = cfg.size_pct.clamp(0.01, 1.0);
    let risk = cfg.risk_per_trade.unwrap_or(0.01);
    let str_ = cfg.strength_sizing;
    let sizer = match cfg.size_mode.as_deref() {
        Some("fixed_qty")        => AnySizer::FixedQuantity(FixedQuantity::new(cfg.size_qty.unwrap_or(1.0), max_slots)),
        Some("quote_qty")        => AnySizer::FixedUsd(FixedUsd::new(cfg.size_usd.unwrap_or(0.0), max_slots)),
        // Risk-based modes ignore strength (it would break the fixed-risk invariant).
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
    };
    let mut engine = Engine::sync(cfg.capital, strategy, sizer, cfg.commission, cfg.slippage);
    if cfg.max_units > 1 {
        engine = engine.with_pyramiding(cfg.max_units, cfg.max_position_pct);
        if !cfg.pyramid {
            engine = engine.with_independent_legs();
        }
    }
    let mut feed = BarVecFeed::new(bars.to_vec(), symbol.to_string());
    let report = engine.run(&mut feed, 0.0);

    // Equity curve + trades live on the portfolio (not the report) — the chart
    // uses them for the equity overlay and entry/exit markers.
    let equity_curve: Vec<_> = engine.portfolio.equity_curve.iter()
        .map(|p| json!({ "t": p.timestamp, "equity": p.equity }))
        .collect();
    let trades: Vec<_> = engine.portfolio.trades.iter()
        .map(|t| json!({
            "entry_ts": t.entry_timestamp, "exit_ts": t.exit_timestamp,
            "entry_price": t.entry_price,   "exit_price": t.exit_price,
            "qty": t.qty,
            "pnl": t.pnl,
            // FRACTIONS (0.1 = 10%) — matches deep TradeResponse; FE multiplies by 100.
            "pnl_pct": t.pnl_pct,
            "commission": t.commission,
            "mae_pct": t.mae_pct,
            "mfe_pct": t.mfe_pct,
            "bars_held": t.bars_held,
            "exit_reason": t.exit_reason.to_string(),
            "side": format!("{:?}", t.side).to_lowercase(),
        }))
        .collect();
    // Raw fills (every order execution, in order) — lets the chart mark each
    // pyramiding *add* that MERGE mode hides inside one averaged trade. `sym`
    // carries the leg key (`BTCUSDT` or `BTCUSDT#2`); `leg` is its 0-based index.
    let fills: Vec<_> = engine.portfolio.fills.iter()
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

    // Indicator series for chart overlay — read from the strategy after the run
    // (the engine owns it; `engine.strategy` is public).
    let mut ind_map = serde_json::Map::new();
    for (name, pts) in engine.strategy.take_indicator_series() {
        let ts: Vec<i64> = pts.iter().map(|(t, _)| *t).collect();
        let vs: Vec<f64> = pts.iter().map(|(_, v)| *v).collect();
        ind_map.insert(name, json!({ "t": ts, "v": vs }));
    }

    let mut out = serde_json::to_value(&report).unwrap_or_else(|_| json!({}));
    if let serde_json::Value::Object(ref mut m) = out {
        m.insert("equity_curve".into(), json!(equity_curve));
        m.insert("trades".into(), json!(trades));
        m.insert("fills".into(), json!(fills));
        m.insert("indicator_series".into(), serde_json::Value::Object(ind_map));
    }
    out
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
    let strategy = match build_strategy(strategy_name, &params) {
        Ok(s) => s,
        Err(e) => return js_error(&format!("strategy '{strategy_name}': {e}")),
    };
    let bars = build_bars(symbol, t, o, h, l, c, v);
    let cfg = parse_config(config_json);
    let out = run_strategy(symbol, &bars, strategy, &cfg);
    to_js(&out)
}

/// Run a script backtest client-side.
///
/// `script`: Script (same syntax as herald `/api/v1/backtest/script`)
/// `config_json`: `{"initial_capital": 10000, "position_size_pct": 1.0, ...}`
#[wasm_bindgen]
pub fn run_script_backtest(
    symbol: &str,
    script: &str,
    t: &[f64], o: &[f64], h: &[f64], l: &[f64], c: &[f64], v: &[f64],
    config_json: &str,
) -> JsValue {
    // On-chart runs on a single timeframe's bars and cannot load/warm HTF feeds,
    // so a multi-timeframe (HTF) script is skipped here with a clear message — run
    // it via Deep backtest instead (which loads + warms each TF from parquet).
    let htfs = alm_strategy::probe_script_htfs(script);
    if !htfs.is_empty() {
        let tfs = htfs.iter().map(|t| format!("{t:?}")).collect::<Vec<_>>().join(", ");
        return js_error(&format!(
            "Multi-timeframe script (uses {tfs}) — on-chart is single-timeframe only. Run it with Deep backtest."
        ));
    }
    let strategy = match build_strategy("script", &json!({ "script": script })) {
        Ok(s) => s,
        Err(e) => return js_error(&format!("script strategy: {e}")),
    };
    let bars = build_bars(symbol, t, o, h, l, c, v);
    let cfg = parse_config(config_json);
    let out = run_strategy(symbol, &bars, strategy, &cfg);
    to_js(&out)
}

pub(crate) struct BtConfig {
    pub capital: f64,
    pub size_pct: f64,
    /// Fixed USD notional per trade (quote_qty mode). None = unused.
    pub size_usd: Option<f64>,
    /// Fixed absolute quantity per trade (fixed_qty mode). None = unused.
    pub size_qty: Option<f64>,
    /// Risk fraction of equity per trade (fixed_fractional / "Risk %" mode). None = unused.
    pub risk_per_trade: Option<f64>,
    /// ATR stop-distance multiplier for the Risk % mode (default 2.0).
    pub atr_mult: f64,
    /// Whether signal strength scales the size (orthogonal tag).
    pub strength_sizing: bool,
    /// Explicit sizing mode synced with helm SizeMode (fixed_fractional/volatility/percent_equity/quote_qty/fixed_qty).
    pub size_mode: Option<String>,
    pub commission: f64,
    pub slippage: f64,
    /// Pyramiding: max accumulated legs (1 = off). Mirrors helm `MaxUnits`.
    pub max_units: usize,
    /// Pyramiding: exposure cap as fraction of equity (0 = none). Mirrors helm `MaxPositionPct`.
    pub max_position_pct: f64,
    /// Pyramiding mode: true (default) = MERGE (averaged), false = INDEPENDENT legs. Mirrors helm `Pyramid`.
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
        // Default merge (true). Accept bool or 0/1 numeric.
        pyramid: v.get("pyramid").map(|x| x.as_bool().unwrap_or_else(|| x.as_f64().unwrap_or(1.0) != 0.0)).unwrap_or(true),
    }
}

/// List all named strategy keys usable with `run_backtest`.
#[wasm_bindgen]
pub fn list_strategies() -> JsValue {
    use alm_strategy::catalog::STRATEGY_KEYS;
    to_js(&STRATEGY_KEYS)
}

/// Full indicator catalog for editor hints: `[{name, label, category, description,
/// params:[{name,type,default}], outputs:[{name,type}]}, ...]`. Drives autocomplete,
/// field completion, and hover docs client-side (no server round-trip).
#[wasm_bindgen]
pub fn indicator_catalog() -> JsValue {
    to_js(&alm_strategy::catalog::all())
}

/// Lint a strategy script client-side (no server round-trip).
///
/// Returns `{ errors: [{line, col, message, severity}], scope: {...} }`.
/// Pass `base_tf` as e.g. `"H1"` or `null` / empty string to skip TF checks.
#[wasm_bindgen]
pub fn validate_script(script: &str, base_tf: &str) -> JsValue {
    use alm_core::Timeframe;
    use alm_strategy::script_lint;

    let tf = match base_tf {
        "" => None,
        s  => match s.to_ascii_uppercase().as_str() {
            "M1"  => Some(Timeframe::M1),  "M3"  => Some(Timeframe::M3),
            "M5"  => Some(Timeframe::M5),  "M15" => Some(Timeframe::M15),
            "M30" => Some(Timeframe::M30), "H1"  => Some(Timeframe::H1),
            "H2"  => Some(Timeframe::H2),  "H4"  => Some(Timeframe::H4),
            "H6"  => Some(Timeframe::H6),  "H12" => Some(Timeframe::H12),
            "D1"  => Some(Timeframe::D1),  "W1"  => Some(Timeframe::W1),
            _     => None,
        },
    };

    let (errors, scope) = script_lint(script, tf);
    to_js(&json!({ "errors": errors, "scope": scope }))
}

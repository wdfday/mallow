//! Static indicator catalog — returned by `GET /api/indicators`.

use serde::Serialize;
use serde_json::Value;

/// A single parameter descriptor.
#[derive(Debug, Serialize)]
#[cfg_attr(feature = "openapi", derive(utoipa::ToSchema))]
pub struct ParamDef {
    pub name: &'static str,
    /// `"int"` | `"float"` | `"int[]"`
    #[serde(rename = "type")]
    pub type_: &'static str,
    pub default: serde_json::Value,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub description: Option<&'static str>,
}

/// Metadata for one indicator type.
#[derive(Debug, Serialize)]
#[cfg_attr(feature = "openapi", derive(utoipa::ToSchema))]
pub struct IndicatorMeta {
    /// Identifier used in `"type"` field.
    pub name: &'static str,
    /// Human-readable label.
    pub label: &'static str,
    /// Grouping: `"trend"` | `"momentum"` | `"volatility"` | `"volume"` | `"pattern"`
    pub category: &'static str,
    /// Constructor parameters accepted by `IndicatorBox::from_config`.
    pub params: Vec<ParamDef>,
    /// Output field names returned in `IndicatorPoint.fields`.
    pub outputs: Vec<&'static str>,
}

fn p_int(name: &'static str, default: i64) -> ParamDef {
    ParamDef { name, type_: "int", default: serde_json::json!(default), description: None }
}
fn p_float(name: &'static str, default: f64) -> ParamDef {
    ParamDef { name, type_: "float", default: serde_json::json!(default), description: None }
}
fn p_int_desc(name: &'static str, default: i64, desc: &'static str) -> ParamDef {
    ParamDef { name, type_: "int", default: serde_json::json!(default), description: Some(desc) }
}

/// Returns the full indicator catalog (one entry per `"type"` key).
pub fn all() -> Vec<IndicatorMeta> {
    vec![
        // ── Trend / MA ────────────────────────────────────────────────────────
        IndicatorMeta {
            name: "sma", label: "Simple Moving Average", category: "trend",
            params: vec![p_int("period", 20)],
            outputs: vec!["value"],
        },
        IndicatorMeta {
            name: "ema", label: "Exponential Moving Average", category: "trend",
            params: vec![p_int("period", 20)],
            outputs: vec!["value"],
        },
        IndicatorMeta {
            name: "wma", label: "Weighted Moving Average", category: "trend",
            params: vec![p_int("period", 20)],
            outputs: vec!["value"],
        },
        IndicatorMeta {
            name: "hma", label: "Hull Moving Average", category: "trend",
            params: vec![p_int("period", 20)],
            outputs: vec!["value"],
        },
        IndicatorMeta {
            name: "dema", label: "Double EMA", category: "trend",
            params: vec![p_int("period", 20)],
            outputs: vec!["value"],
        },
        IndicatorMeta {
            name: "tema", label: "Triple EMA", category: "trend",
            params: vec![p_int("period", 20)],
            outputs: vec!["value"],
        },
        IndicatorMeta {
            name: "smma", label: "Smoothed MA (RMA)", category: "trend",
            params: vec![p_int("period", 20)],
            outputs: vec!["value"],
        },
        IndicatorMeta {
            name: "alma", label: "Arnaud Legoux MA", category: "trend",
            params: vec![p_int("period", 9), p_float("offset", 0.85), p_float("sigma", 6.0)],
            outputs: vec!["value"],
        },
        IndicatorMeta {
            name: "mcginley", label: "McGinley Dynamic", category: "trend",
            params: vec![p_int("period", 14)],
            outputs: vec!["value"],
        },
        IndicatorMeta {
            name: "lsma", label: "Least Squares MA", category: "trend",
            params: vec![p_int("period", 25)],
            outputs: vec!["value", "slope"],
        },
        IndicatorMeta {
            name: "vwma", label: "Volume-Weighted MA", category: "trend",
            params: vec![p_int("period", 20)],
            outputs: vec!["value"],
        },
        IndicatorMeta {
            name: "kama", label: "Kaufman Adaptive MA", category: "trend",
            params: vec![
                p_int_desc("er_period", 10, "efficiency ratio period"),
                p_int_desc("fast", 2, "fast EMA period"),
                p_int_desc("slow", 30, "slow EMA period"),
            ],
            outputs: vec!["value"],
        },
        IndicatorMeta {
            name: "macd", label: "MACD", category: "trend",
            params: vec![p_int("fast", 12), p_int("slow", 26), p_int("signal", 9)],
            outputs: vec!["macd", "signal", "histogram"],
        },
        IndicatorMeta {
            name: "trix", label: "TRIX", category: "trend",
            params: vec![p_int("period", 18), p_int("signal", 9)],
            outputs: vec!["trix", "signal", "histogram"],
        },
        IndicatorMeta {
            name: "adx", label: "Average Directional Index", category: "trend",
            params: vec![p_int("period", 14)],
            outputs: vec!["adx", "plus_di", "minus_di"],
        },
        IndicatorMeta {
            name: "dmi", label: "DMI (Directional Movement)", category: "trend",
            params: vec![p_int("period", 14)],
            outputs: vec!["plus_di", "minus_di", "dx"],
        },
        IndicatorMeta {
            name: "aroon", label: "Aroon", category: "trend",
            params: vec![p_int("period", 25)],
            outputs: vec!["up", "down", "oscillator"],
        },
        IndicatorMeta {
            name: "vortex", label: "Vortex Indicator", category: "trend",
            params: vec![p_int("period", 14)],
            outputs: vec!["plus_vi", "minus_vi"],
        },
        IndicatorMeta {
            name: "alligator", label: "Williams Alligator", category: "trend",
            params: vec![p_int("jaw", 13), p_int("teeth", 8), p_int("lips", 5)],
            outputs: vec!["jaw", "teeth", "lips", "bullish"],
        },
        IndicatorMeta {
            name: "gmma", label: "Guppy MMMA", category: "trend",
            params: vec![],
            outputs: vec!["bullish"],
        },
        IndicatorMeta {
            name: "kdj", label: "KDJ", category: "trend",
            params: vec![p_int("period", 9), p_int("k_period", 3), p_int("d_period", 3)],
            outputs: vec!["k", "d", "j"],
        },
        // ── Momentum / Oscillator ─────────────────────────────────────────────
        IndicatorMeta {
            name: "rsi", label: "Relative Strength Index", category: "momentum",
            params: vec![p_int("period", 14)],
            outputs: vec!["value"],
        },
        IndicatorMeta {
            name: "cci", label: "Commodity Channel Index", category: "momentum",
            params: vec![p_int("period", 20)],
            outputs: vec!["value"],
        },
        IndicatorMeta {
            name: "roc", label: "Rate of Change", category: "momentum",
            params: vec![p_int("period", 10)],
            outputs: vec!["value"],
        },
        IndicatorMeta {
            name: "mom", label: "Momentum", category: "momentum",
            params: vec![p_int("period", 10)],
            outputs: vec!["value"],
        },
        IndicatorMeta {
            name: "cmo", label: "Chande Momentum Oscillator", category: "momentum",
            params: vec![p_int("period", 14)],
            outputs: vec!["value"],
        },
        IndicatorMeta {
            name: "dpo", label: "Detrended Price Oscillator", category: "momentum",
            params: vec![p_int("period", 20)],
            outputs: vec!["value"],
        },
        IndicatorMeta {
            name: "mfi", label: "Money Flow Index", category: "momentum",
            params: vec![p_int("period", 14)],
            outputs: vec!["value"],
        },
        IndicatorMeta {
            name: "bop", label: "Balance of Power", category: "momentum",
            params: vec![],
            outputs: vec!["value"],
        },
        IndicatorMeta {
            name: "williams_r", label: "Williams %R", category: "momentum",
            params: vec![p_int("period", 14)],
            outputs: vec!["value"],
        },
        IndicatorMeta {
            name: "stochastic", label: "Stochastic Oscillator", category: "momentum",
            params: vec![p_int("k_period", 14), p_int("d_period", 3)],
            outputs: vec!["k", "d"],
        },
        IndicatorMeta {
            name: "stoch_rsi", label: "Stochastic RSI", category: "momentum",
            params: vec![p_int("rsi_period", 14), p_int("smooth_d", 3)],
            outputs: vec!["k", "d"],
        },
        IndicatorMeta {
            name: "tsi", label: "True Strength Index", category: "momentum",
            params: vec![p_int("first", 25), p_int("second", 13)],
            outputs: vec!["value"],
        },
        IndicatorMeta {
            name: "rci", label: "Rank Correlation Index", category: "momentum",
            params: vec![p_int("period", 9)],
            outputs: vec!["value"],
        },
        IndicatorMeta {
            name: "bull_bear", label: "Bull/Bear Power (Elder Ray)", category: "momentum",
            params: vec![p_int("period", 13)],
            outputs: vec!["bull", "bear"],
        },
        IndicatorMeta {
            name: "fisher", label: "Fisher Transform", category: "momentum",
            params: vec![p_int("period", 9)],
            outputs: vec!["fisher", "signal"],
        },
        IndicatorMeta {
            name: "kst", label: "Know Sure Thing", category: "momentum",
            params: vec![],
            outputs: vec!["kst", "signal"],
        },
        IndicatorMeta {
            name: "pmo", label: "Price Momentum Oscillator", category: "momentum",
            params: vec![],
            outputs: vec!["pmo", "signal"],
        },
        IndicatorMeta {
            name: "ppo", label: "Percentage Price Oscillator", category: "momentum",
            params: vec![p_int("fast", 12), p_int("slow", 26), p_int("signal", 9)],
            outputs: vec!["ppo", "signal", "histogram"],
        },
        IndicatorMeta {
            name: "rvi", label: "Relative Vigor Index", category: "momentum",
            params: vec![p_int("period", 10)],
            outputs: vec!["value", "signal"],
        },
        IndicatorMeta {
            name: "smi", label: "Stochastic Momentum Index", category: "momentum",
            params: vec![],
            outputs: vec!["smi", "signal"],
        },
        IndicatorMeta {
            name: "uo", label: "Ultimate Oscillator", category: "momentum",
            params: vec![p_int("fast", 7), p_int("medium", 14), p_int("slow", 28)],
            outputs: vec!["value"],
        },
        IndicatorMeta {
            name: "connors_rsi", label: "ConnorsRSI", category: "momentum",
            params: vec![
                p_int("rsi_period", 3),
                p_int("streak_period", 2),
                p_int("rank_period", 100),
            ],
            outputs: vec!["value"],
        },
        IndicatorMeta {
            name: "ao", label: "Awesome Oscillator", category: "momentum",
            params: vec![p_int("fast", 5), p_int("slow", 34)],
            outputs: vec!["value"],
        },
        IndicatorMeta {
            name: "coppock", label: "Coppock Curve", category: "momentum",
            params: vec![],
            outputs: vec!["value"],
        },
        // ── Volatility ────────────────────────────────────────────────────────
        IndicatorMeta {
            name: "atr", label: "Average True Range", category: "volatility",
            params: vec![p_int("period", 14)],
            outputs: vec!["atr"],
        },
        IndicatorMeta {
            name: "bbands", label: "Bollinger Bands", category: "volatility",
            params: vec![p_int("period", 20), p_float("multiplier", 2.0)],
            outputs: vec!["upper", "middle", "lower"],
        },
        IndicatorMeta {
            name: "keltner", label: "Keltner Channel", category: "volatility",
            params: vec![p_int("period", 20), p_int("atr_period", 10), p_float("multiplier", 2.0)],
            outputs: vec!["upper", "middle", "lower"],
        },
        IndicatorMeta {
            name: "supertrend", label: "SuperTrend", category: "volatility",
            params: vec![p_int("period", 10), p_float("multiplier", 3.0)],
            outputs: vec!["value", "bullish"],
        },
        IndicatorMeta {
            name: "donchian", label: "Donchian Channel", category: "volatility",
            params: vec![p_int("period", 20)],
            outputs: vec!["upper", "middle", "lower"],
        },
        IndicatorMeta {
            name: "chop", label: "Choppiness Index", category: "volatility",
            params: vec![p_int("period", 14)],
            outputs: vec!["value"],
        },
        IndicatorMeta {
            name: "chop_zone", label: "ChopZone", category: "volatility",
            params: vec![p_int("ema_period", 34), p_float("threshold", 5.0)],
            outputs: vec!["angle"],
        },
        IndicatorMeta {
            name: "chandelier_exit", label: "Chandelier Exit", category: "volatility",
            params: vec![p_int("period", 22), p_float("multiplier", 3.0)],
            outputs: vec!["long", "short"],
        },
        IndicatorMeta {
            name: "chande_kroll", label: "Chande Kroll Stop", category: "volatility",
            params: vec![
                p_int("atr_period", 10),
                p_float("factor", 1.5),
                p_int("stop_period", 9),
            ],
            outputs: vec!["long", "short"],
        },
        IndicatorMeta {
            name: "volatility_ratio", label: "Volatility Ratio", category: "volatility",
            params: vec![p_int("lookback", 10)],
            outputs: vec!["value"],
        },
        // ── Volume ────────────────────────────────────────────────────────────
        IndicatorMeta {
            name: "obv", label: "On-Balance Volume", category: "volume",
            params: vec![],
            outputs: vec!["value"],
        },
        IndicatorMeta {
            name: "cmf", label: "Chaikin Money Flow", category: "volume",
            params: vec![p_int("period", 20)],
            outputs: vec!["value"],
        },
        IndicatorMeta {
            name: "vwap", label: "VWAP (session-aware)", category: "volume",
            params: vec![p_int_desc("session_gap_mins", 390, "session gap threshold in minutes")],
            outputs: vec!["value"],
        },
        // ── Pattern ───────────────────────────────────────────────────────────
        IndicatorMeta {
            name: "ichimoku", label: "Ichimoku Cloud", category: "pattern",
            params: vec![p_int("tenkan", 9), p_int("kijun", 26), p_int("senkou_b", 52)],
            outputs: vec!["tenkan", "kijun", "senkou_a", "senkou_b", "chikou"],
        },
        IndicatorMeta {
            name: "parabolic_sar", label: "Parabolic SAR", category: "pattern",
            params: vec![p_float("step", 0.02), p_float("max", 0.2)],
            outputs: vec!["sar"],
        },
        IndicatorMeta {
            name: "rwi", label: "Random Walk Index", category: "pattern",
            params: vec![p_int("period", 14)],
            outputs: vec!["high", "low"],
        },
        IndicatorMeta {
            name: "fractal", label: "Williams Fractal", category: "pattern",
            params: vec![],
            outputs: vec!["bull", "bear"],
        },
    ]
}

// ── Strategy catalogue ────────────────────────────────────────────────────────

pub const STRATEGY_KEYS: &[&str] = &[
    "ma_crossover",
    "triple_ema",
    "hma_crossover",
    "rsi_mean_rev",
    "macd_crossover",
    "macd_ma",
    "waddah_attar",
    "stochastic_crossover",
    "stochastic_dk",
    "range_rover",
    "reversal_catcher",
    "atr_trailing",
    "volatility_squeezer",
    "volatility_vanguard",
    "volatility_ratio",
    "bollinger_macd",
    "bb_squeeze",
    "mean_reversion",
    "bb_keltner_squeeze",
    "dmi_adx",
    "wolfstein",
    "trend_transition",
    "swing_trader",
    "oscillator_overlord",
    "equilibrium_explorer",
    "trend_follower",
    "ma_pullback",
    "cci_reversal",
    "supertrend",
    "supertrend_macd",
    "parabolic_sar",
    "heiken_ashi_color",
    "heiken_ashi_breakout",
    "heiken_ashi_harmonizer",
    "gmma_crossover",
    "ichimoku_cloud",
    "ichimoku_cross",
    "scalping_ema",
    "dema_crossover",
    "donchian_breakout",
    "elder_ray",
    "aroon_trend",
    "chandelier_exit",
    "vwap_bounce",
    "vwap_trend",
    "momentum_roc",
    "dual_momentum",
    "price_action_swing",
    "orb_breakout",
    "highest_breakout",
    "keltner_breakout",
    "mfi_trend",
    "mfi_revert",
    "roc",
    "kst",
    "trix",
    "alligator",
    "rwi",
    "pattern_breakout",
    "kama",
    "tsi",
    "stoch_rsi",
    "chop_filter",
    "connors_rsi",
    "kdj",
    "ao",
    "tema_crossover",
    "pixel_3",
];

// ── Indicator helpers (used by indicator runner) ──────────────────────────────

/// Auto-generate a series label from indicator config if no explicit label given.
/// E.g. `{ "type": "ema", "period": 20 }` → `"ema_20"`.
pub fn auto_label(config: &serde_json::Map<String, Value>) -> String {
    let type_ = config.get("type").and_then(Value::as_str).unwrap_or("ind");
    let mut parts: Vec<String> = vec![type_.to_string()];
    for key in &["period", "fast", "slow", "signal", "k_period", "er_period",
                 "tenkan", "kijun", "senkou_b", "lookback"] {
        if let Some(v) = config.get(*key).and_then(Value::as_f64) {
            parts.push(format!("{}", v as i64));
        }
    }
    parts.join("_")
}


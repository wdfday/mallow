//! Static indicator catalog — returned by `GET /api/indicators`.

use serde::Serialize;
use serde_json::Value;
use utoipa::ToSchema;

/// A single parameter descriptor.
#[derive(Debug, Serialize, ToSchema)]
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
#[derive(Debug, Serialize, ToSchema)]
pub struct IndicatorMeta {
    /// Identifier used in `"type"` field and CEL expressions.
    pub name: &'static str,
    /// Human-readable label.
    pub label: &'static str,
    /// Grouping: `"trend"` | `"momentum"` | `"volatility"` | `"volume"` | `"pattern"`
    pub category: &'static str,
    /// Constructor parameters accepted by `IndicatorBox::from_config`.
    pub params: Vec<ParamDef>,
    /// Output field names returned in `IndicatorPoint.fields`.
    pub outputs: Vec<&'static str>,
    /// CEL function name(s) — how this indicator is called in CEL expressions.
    /// Multiple entries when the same indicator exposes several CEL aliases.
    pub cel_funcs: Vec<&'static str>,
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
            cel_funcs: vec!["sma"],
        },
        IndicatorMeta {
            name: "ema", label: "Exponential Moving Average", category: "trend",
            params: vec![p_int("period", 20)],
            outputs: vec!["value"],
            cel_funcs: vec!["ema"],
        },
        IndicatorMeta {
            name: "wma", label: "Weighted Moving Average", category: "trend",
            params: vec![p_int("period", 20)],
            outputs: vec!["value"],
            cel_funcs: vec!["wma"],
        },
        IndicatorMeta {
            name: "hma", label: "Hull Moving Average", category: "trend",
            params: vec![p_int("period", 20)],
            outputs: vec!["value"],
            cel_funcs: vec!["hma"],
        },
        IndicatorMeta {
            name: "dema", label: "Double EMA", category: "trend",
            params: vec![p_int("period", 20)],
            outputs: vec!["value"],
            cel_funcs: vec!["dema"],
        },
        IndicatorMeta {
            name: "tema", label: "Triple EMA", category: "trend",
            params: vec![p_int("period", 20)],
            outputs: vec!["value"],
            cel_funcs: vec!["tema"],
        },
        IndicatorMeta {
            name: "smma", label: "Smoothed MA (RMA)", category: "trend",
            params: vec![p_int("period", 20)],
            outputs: vec!["value"],
            cel_funcs: vec!["smma"],
        },
        IndicatorMeta {
            name: "alma", label: "Arnaud Legoux MA", category: "trend",
            params: vec![p_int("period", 9), p_float("offset", 0.85), p_float("sigma", 6.0)],
            outputs: vec!["value"],
            cel_funcs: vec!["alma"],
        },
        IndicatorMeta {
            name: "mcginley", label: "McGinley Dynamic", category: "trend",
            params: vec![p_int("period", 14)],
            outputs: vec!["value"],
            cel_funcs: vec!["mcginley"],
        },
        IndicatorMeta {
            name: "lsma", label: "Least Squares MA", category: "trend",
            params: vec![p_int("period", 25)],
            outputs: vec!["value", "slope"],
            cel_funcs: vec!["lsma", "lsma_slope"],
        },
        IndicatorMeta {
            name: "vwma", label: "Volume-Weighted MA", category: "trend",
            params: vec![p_int("period", 20)],
            outputs: vec!["value"],
            cel_funcs: vec!["vwma"],
        },
        IndicatorMeta {
            name: "kama", label: "Kaufman Adaptive MA", category: "trend",
            params: vec![
                p_int_desc("er_period", 10, "efficiency ratio period"),
                p_int_desc("fast", 2, "fast EMA period"),
                p_int_desc("slow", 30, "slow EMA period"),
            ],
            outputs: vec!["value"],
            cel_funcs: vec!["kama"],
        },
        IndicatorMeta {
            name: "macd", label: "MACD", category: "trend",
            params: vec![p_int("fast", 12), p_int("slow", 26), p_int("signal", 9)],
            outputs: vec!["macd", "signal", "histogram"],
            cel_funcs: vec!["macd_hist", "macd_line"],
        },
        IndicatorMeta {
            name: "trix", label: "TRIX", category: "trend",
            params: vec![p_int("period", 18), p_int("signal", 9)],
            outputs: vec!["trix", "signal", "histogram"],
            cel_funcs: vec![],
        },
        IndicatorMeta {
            name: "adx", label: "Average Directional Index", category: "trend",
            params: vec![p_int("period", 14)],
            outputs: vec!["adx", "plus_di", "minus_di"],
            cel_funcs: vec!["adx", "plus_di", "minus_di"],
        },
        IndicatorMeta {
            name: "dmi", label: "DMI (Directional Movement)", category: "trend",
            params: vec![p_int("period", 14)],
            outputs: vec!["plus_di", "minus_di", "dx"],
            cel_funcs: vec!["dmi_plus", "dmi_minus", "dmi_dx"],
        },
        IndicatorMeta {
            name: "aroon", label: "Aroon", category: "trend",
            params: vec![p_int("period", 25)],
            outputs: vec!["up", "down", "oscillator"],
            cel_funcs: vec!["aroon_up", "aroon_down", "aroon_osc"],
        },
        IndicatorMeta {
            name: "vortex", label: "Vortex Indicator", category: "trend",
            params: vec![p_int("period", 14)],
            outputs: vec!["plus_vi", "minus_vi"],
            cel_funcs: vec!["vortex_plus", "vortex_minus"],
        },
        IndicatorMeta {
            name: "alligator", label: "Williams Alligator", category: "trend",
            params: vec![p_int("jaw", 13), p_int("teeth", 8), p_int("lips", 5)],
            outputs: vec!["jaw", "teeth", "lips", "bullish"],
            cel_funcs: vec!["alligator_jaw", "alligator_teeth", "alligator_lips", "alligator_bull"],
        },
        IndicatorMeta {
            name: "gmma", label: "Guppy MMMA", category: "trend",
            params: vec![],
            outputs: vec!["bullish"],
            cel_funcs: vec!["gmma_bull"],
        },
        IndicatorMeta {
            name: "kdj", label: "KDJ", category: "trend",
            params: vec![p_int("period", 9), p_int("k_period", 3), p_int("d_period", 3)],
            outputs: vec!["k", "d", "j"],
            cel_funcs: vec!["kdj_k", "kdj_d", "kdj_j"],
        },
        // ── Momentum / Oscillator ─────────────────────────────────────────────
        IndicatorMeta {
            name: "rsi", label: "Relative Strength Index", category: "momentum",
            params: vec![p_int("period", 14)],
            outputs: vec!["value"],
            cel_funcs: vec!["rsi"],
        },
        IndicatorMeta {
            name: "cci", label: "Commodity Channel Index", category: "momentum",
            params: vec![p_int("period", 20)],
            outputs: vec!["value"],
            cel_funcs: vec!["cci"],
        },
        IndicatorMeta {
            name: "roc", label: "Rate of Change", category: "momentum",
            params: vec![p_int("period", 10)],
            outputs: vec!["value"],
            cel_funcs: vec!["roc"],
        },
        IndicatorMeta {
            name: "mom", label: "Momentum", category: "momentum",
            params: vec![p_int("period", 10)],
            outputs: vec!["value"],
            cel_funcs: vec!["mom"],
        },
        IndicatorMeta {
            name: "cmo", label: "Chande Momentum Oscillator", category: "momentum",
            params: vec![p_int("period", 14)],
            outputs: vec!["value"],
            cel_funcs: vec!["cmo"],
        },
        IndicatorMeta {
            name: "dpo", label: "Detrended Price Oscillator", category: "momentum",
            params: vec![p_int("period", 20)],
            outputs: vec!["value"],
            cel_funcs: vec!["dpo"],
        },
        IndicatorMeta {
            name: "mfi", label: "Money Flow Index", category: "momentum",
            params: vec![p_int("period", 14)],
            outputs: vec!["value"],
            cel_funcs: vec!["mfi"],
        },
        IndicatorMeta {
            name: "bop", label: "Balance of Power", category: "momentum",
            params: vec![],
            outputs: vec!["value"],
            cel_funcs: vec!["bop"],
        },
        IndicatorMeta {
            name: "williams_r", label: "Williams %R", category: "momentum",
            params: vec![p_int("period", 14)],
            outputs: vec!["value"],
            cel_funcs: vec!["williams"],
        },
        IndicatorMeta {
            name: "stochastic", label: "Stochastic Oscillator", category: "momentum",
            params: vec![p_int("k_period", 14), p_int("d_period", 3)],
            outputs: vec!["k", "d"],
            cel_funcs: vec!["stoch_k", "stoch_d"],
        },
        IndicatorMeta {
            name: "stoch_rsi", label: "Stochastic RSI", category: "momentum",
            params: vec![p_int("rsi_period", 14), p_int("smooth_d", 3)],
            outputs: vec!["k", "d"],
            cel_funcs: vec!["srsi_k", "srsi_d"],
        },
        IndicatorMeta {
            name: "tsi", label: "True Strength Index", category: "momentum",
            params: vec![p_int("first", 25), p_int("second", 13)],
            outputs: vec!["value"],
            cel_funcs: vec!["tsi"],
        },
        IndicatorMeta {
            name: "rci", label: "Rank Correlation Index", category: "momentum",
            params: vec![p_int("period", 9)],
            outputs: vec!["value"],
            cel_funcs: vec!["rci"],
        },
        IndicatorMeta {
            name: "bull_bear", label: "Bull/Bear Power (Elder Ray)", category: "momentum",
            params: vec![p_int("period", 13)],
            outputs: vec!["bull", "bear"],
            cel_funcs: vec!["bull_power", "bear_power"],
        },
        IndicatorMeta {
            name: "fisher", label: "Fisher Transform", category: "momentum",
            params: vec![p_int("period", 9)],
            outputs: vec!["fisher", "signal"],
            cel_funcs: vec!["fisher_line", "fisher_sig"],
        },
        IndicatorMeta {
            name: "kst", label: "Know Sure Thing", category: "momentum",
            params: vec![],
            outputs: vec!["kst", "signal"],
            cel_funcs: vec!["kst_line", "kst_sig"],
        },
        IndicatorMeta {
            name: "pmo", label: "Price Momentum Oscillator", category: "momentum",
            params: vec![],
            outputs: vec!["pmo", "signal"],
            cel_funcs: vec!["pmo_line", "pmo_sig"],
        },
        IndicatorMeta {
            name: "ppo", label: "Percentage Price Oscillator", category: "momentum",
            params: vec![p_int("fast", 12), p_int("slow", 26), p_int("signal", 9)],
            outputs: vec!["ppo", "signal", "histogram"],
            cel_funcs: vec!["ppo_line", "ppo_sig", "ppo_hist"],
        },
        IndicatorMeta {
            name: "rvi", label: "Relative Vigor Index", category: "momentum",
            params: vec![p_int("period", 10)],
            outputs: vec!["value", "signal"],
            cel_funcs: vec!["rvi_line", "rvi_sig"],
        },
        IndicatorMeta {
            name: "smi", label: "Stochastic Momentum Index", category: "momentum",
            params: vec![],
            outputs: vec!["smi", "signal"],
            cel_funcs: vec!["smi_line", "smi_sig"],
        },
        IndicatorMeta {
            name: "uo", label: "Ultimate Oscillator", category: "momentum",
            params: vec![p_int("fast", 7), p_int("medium", 14), p_int("slow", 28)],
            outputs: vec!["value"],
            cel_funcs: vec!["uo"],
        },
        IndicatorMeta {
            name: "connors_rsi", label: "ConnorsRSI", category: "momentum",
            params: vec![
                p_int("rsi_period", 3),
                p_int("streak_period", 2),
                p_int("rank_period", 100),
            ],
            outputs: vec!["value"],
            cel_funcs: vec!["connors_rsi"],
        },
        IndicatorMeta {
            name: "ao", label: "Awesome Oscillator", category: "momentum",
            params: vec![p_int("fast", 5), p_int("slow", 34)],
            outputs: vec!["value"],
            cel_funcs: vec!["ao"],
        },
        IndicatorMeta {
            name: "coppock", label: "Coppock Curve", category: "momentum",
            params: vec![],
            outputs: vec!["value"],
            cel_funcs: vec!["coppock"],
        },
        // ── Volatility ────────────────────────────────────────────────────────
        IndicatorMeta {
            name: "atr", label: "Average True Range", category: "volatility",
            params: vec![p_int("period", 14)],
            outputs: vec!["atr"],
            cel_funcs: vec!["atr"],
        },
        IndicatorMeta {
            name: "bbands", label: "Bollinger Bands", category: "volatility",
            params: vec![p_int("period", 20), p_float("multiplier", 2.0)],
            outputs: vec!["upper", "middle", "lower"],
            cel_funcs: vec!["bb_upper", "bb_mid", "bb_lower"],
        },
        IndicatorMeta {
            name: "keltner", label: "Keltner Channel", category: "volatility",
            params: vec![p_int("period", 20), p_int("atr_period", 10), p_float("multiplier", 2.0)],
            outputs: vec!["upper", "middle", "lower"],
            cel_funcs: vec![],
        },
        IndicatorMeta {
            name: "supertrend", label: "SuperTrend", category: "volatility",
            params: vec![p_int("period", 10), p_float("multiplier", 3.0)],
            outputs: vec!["value", "bullish"],
            cel_funcs: vec!["supertrend", "st_bull"],
        },
        IndicatorMeta {
            name: "donchian", label: "Donchian Channel", category: "volatility",
            params: vec![p_int("period", 20)],
            outputs: vec!["upper", "middle", "lower"],
            cel_funcs: vec!["donchian_upper", "donchian_mid", "donchian_lower"],
        },
        IndicatorMeta {
            name: "chop", label: "Choppiness Index", category: "volatility",
            params: vec![p_int("period", 14)],
            outputs: vec!["value"],
            cel_funcs: vec!["chop"],
        },
        IndicatorMeta {
            name: "chop_zone", label: "ChopZone", category: "volatility",
            params: vec![p_int("ema_period", 34), p_float("threshold", 5.0)],
            outputs: vec!["angle"],
            cel_funcs: vec!["chop_angle"],
        },
        IndicatorMeta {
            name: "chandelier_exit", label: "Chandelier Exit", category: "volatility",
            params: vec![p_int("period", 22), p_float("multiplier", 3.0)],
            outputs: vec!["long", "short"],
            cel_funcs: vec!["chandelier_long", "chandelier_short"],
        },
        IndicatorMeta {
            name: "chande_kroll", label: "Chande Kroll Stop", category: "volatility",
            params: vec![
                p_int("atr_period", 10),
                p_float("factor", 1.5),
                p_int("stop_period", 9),
            ],
            outputs: vec!["long", "short"],
            cel_funcs: vec!["chande_kroll_long", "chande_kroll_short"],
        },
        IndicatorMeta {
            name: "volatility_ratio", label: "Volatility Ratio", category: "volatility",
            params: vec![p_int("lookback", 10)],
            outputs: vec!["value"],
            cel_funcs: vec![],
        },
        // ── Volume ────────────────────────────────────────────────────────────
        IndicatorMeta {
            name: "obv", label: "On-Balance Volume", category: "volume",
            params: vec![],
            outputs: vec!["value"],
            cel_funcs: vec!["obv"],
        },
        IndicatorMeta {
            name: "cmf", label: "Chaikin Money Flow", category: "volume",
            params: vec![p_int("period", 20)],
            outputs: vec!["value"],
            cel_funcs: vec!["cmf"],
        },
        IndicatorMeta {
            name: "vwap", label: "VWAP (session-aware)", category: "volume",
            params: vec![p_int_desc("session_gap_mins", 390, "session gap threshold in minutes")],
            outputs: vec!["value"],
            cel_funcs: vec!["vwap"],
        },
        // ── Pattern ───────────────────────────────────────────────────────────
        IndicatorMeta {
            name: "ichimoku", label: "Ichimoku Cloud", category: "pattern",
            params: vec![p_int("tenkan", 9), p_int("kijun", 26), p_int("senkou_b", 52)],
            outputs: vec!["tenkan", "kijun", "senkou_a", "senkou_b", "chikou"],
            cel_funcs: vec![],
        },
        IndicatorMeta {
            name: "parabolic_sar", label: "Parabolic SAR", category: "pattern",
            params: vec![p_float("step", 0.02), p_float("max", 0.2)],
            outputs: vec!["sar"],
            cel_funcs: vec!["sar"],
        },
        IndicatorMeta {
            name: "rwi", label: "Random Walk Index", category: "pattern",
            params: vec![p_int("period", 14)],
            outputs: vec!["high", "low"],
            cel_funcs: vec![],
        },
        IndicatorMeta {
            name: "fractal", label: "Williams Fractal", category: "pattern",
            params: vec![],
            outputs: vec!["bull", "bear"],
            cel_funcs: vec!["fractal_bull", "fractal_bear"],
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
    "cel",
    "dynamic",
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

/// Build a JSON config `Value` from a CEL base name + period.
pub fn cel_to_config(base: &str, n: usize) -> Value {
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
        other               => serde_json::json!({"type": other, "period": n}),
    }
}

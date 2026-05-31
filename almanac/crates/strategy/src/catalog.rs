//! Static indicator catalog — returned by `GET /api/indicators`.

use serde::Serialize;
use serde_json::Value;

/// One indicator output field with its semantic type. Most fields are `"f64"`
/// scalars; a handful (`bullish`, `bearish`, `above_cloud`, …) are `"bool"`
/// flags encoded as `0.0`/`1.0` — they must be compared (`> 0.5`), never
/// negated with `!` (Rhai has no `!` for `f64`).
#[derive(Debug, Serialize)]
#[cfg_attr(feature = "openapi", derive(utoipa::ToSchema))]
pub struct OutputField {
    pub name: &'static str,
    /// `"f64"` (scalar) | `"bool"` (0/1 flag).
    #[serde(rename = "type")]
    pub type_: &'static str,
}

/// Serialize a list of output field names as `[{name, type}]`, deriving each
/// field's semantic type from `alm_indicator::field_kind`.
fn serialize_outputs<S>(outputs: &[&'static str], s: S) -> Result<S::Ok, S::Error>
where
    S: serde::Serializer,
{
    use serde::ser::SerializeSeq;
    let mut seq = s.serialize_seq(Some(outputs.len()))?;
    for &name in outputs {
        seq.serialize_element(&OutputField {
            name,
            type_: alm_indicator::field_kind(name).as_str(),
        })?;
    }
    seq.end()
}

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
    /// One-sentence explanation of what the indicator measures.
    pub description: &'static str,
    /// Constructor parameters accepted by `IndicatorBox::from_config`.
    pub params: Vec<ParamDef>,
    /// Output fields returned in `IndicatorPoint.fields`, each tagged with its
    /// semantic type (`f64` scalar vs `bool` 0/1 flag). Built statically from
    /// the field-name list; serialized as `[{name, type}]`.
    #[serde(serialize_with = "serialize_outputs")]
    #[cfg_attr(feature = "openapi", schema(value_type = Vec<OutputField>))]
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
            description: "Arithmetic mean of closing prices over a rolling window — the simplest trend baseline.",
            params: vec![p_int("period", 20)],
            outputs: vec!["value"],
        },
        IndicatorMeta {
            name: "ema", label: "Exponential Moving Average", category: "trend",
            description: "Exponentially-weighted average that gives more weight to recent bars — faster than SMA.",
            params: vec![p_int("period", 20)],
            outputs: vec!["value"],
        },
        IndicatorMeta {
            name: "wma", label: "Weighted Moving Average", category: "trend",
            description: "Linearly weighted average where the most recent bar has the highest weight.",
            params: vec![p_int("period", 20)],
            outputs: vec!["value"],
        },
        IndicatorMeta {
            name: "hma", label: "Hull Moving Average", category: "trend",
            description: "Hull's MA using WMA of WMA to dramatically reduce lag while staying smooth.",
            params: vec![p_int("period", 20)],
            outputs: vec!["value"],
        },
        IndicatorMeta {
            name: "dema", label: "Double EMA", category: "trend",
            description: "Double EMA that subtracts the error of EMA to eliminate most lag from a single EMA.",
            params: vec![p_int("period", 20)],
            outputs: vec!["value"],
        },
        IndicatorMeta {
            name: "tema", label: "Triple EMA", category: "trend",
            description: "Triple EMA — even faster response than DEMA with further lag reduction.",
            params: vec![p_int("period", 20)],
            outputs: vec!["value"],
        },
        IndicatorMeta {
            name: "smma", label: "Smoothed MA (RMA)", category: "trend",
            description: "Smoothed MA (alias RMA) — slow-reacting average that filters out short-term noise.",
            params: vec![p_int("period", 20)],
            outputs: vec!["value"],
        },
        IndicatorMeta {
            name: "alma", label: "Arnaud Legoux MA", category: "trend",
            description: "Gaussian-weighted MA centred near the most recent bar for low lag and low noise.",
            params: vec![p_int("period", 9), p_float("offset", 0.85), p_float("sigma", 6.0)],
            outputs: vec!["value"],
        },
        IndicatorMeta {
            name: "mcginley", label: "McGinley Dynamic", category: "trend",
            description: "Self-adjusting MA that automatically corrects for speed differences in market movement.",
            params: vec![p_int("period", 14)],
            outputs: vec!["value"],
        },
        IndicatorMeta {
            name: "lsma", label: "Least Squares MA", category: "trend",
            description: "Linear regression line fit over the window — slope indicates trend direction and strength.",
            params: vec![p_int("period", 25)],
            outputs: vec!["value", "slope"],
        },
        IndicatorMeta {
            name: "vwma", label: "Volume-Weighted MA", category: "trend",
            description: "Volume-weighted MA where high-volume bars exert proportionally more influence.",
            params: vec![p_int("period", 20)],
            outputs: vec!["value"],
        },
        IndicatorMeta {
            name: "kama", label: "Kaufman Adaptive MA", category: "trend",
            description: "Kaufman adaptive MA — speeds up in trending markets and slows down in ranging conditions.",
            params: vec![
                p_int_desc("er_period", 10, "efficiency ratio period"),
                p_int_desc("fast", 2, "fast EMA period"),
                p_int_desc("slow", 30, "slow EMA period"),
            ],
            outputs: vec!["value"],
        },
        IndicatorMeta {
            name: "macd", label: "MACD", category: "trend",
            description: "Moving Average Convergence Divergence — difference between fast and slow EMAs with a signal line.",
            params: vec![p_int("fast", 12), p_int("slow", 26), p_int("signal", 9)],
            outputs: vec!["macd", "signal", "histogram"],
        },
        IndicatorMeta {
            name: "trix", label: "TRIX", category: "trend",
            description: "1-day rate-of-change of a triple-smoothed EMA — filters noise and shows momentum.",
            params: vec![p_int("period", 18), p_int("signal", 9)],
            outputs: vec!["trix", "signal", "histogram"],
        },
        IndicatorMeta {
            name: "adx", label: "Average Directional Index", category: "trend",
            description: "Trend-strength on a 0–100 scale (primary `adx`; above 25 = strong trend), plus the +DI/-DI directional lines.",
            params: vec![p_int("period", 14)],
            outputs: vec!["adx", "plus_di", "minus_di"],
        },
        IndicatorMeta {
            name: "dmi", label: "DMI (Directional Movement)", category: "trend",
            description: "Directional Movement Index — +DI vs −DI crossover signals trend direction changes.",
            params: vec![p_int("period", 14)],
            outputs: vec!["plus_di", "minus_di"],
        },
        IndicatorMeta {
            name: "aroon", label: "Aroon", category: "trend",
            description: "Measures how many bars ago the highest high / lowest low occurred to gauge trend age.",
            params: vec![p_int("period", 25)],
            outputs: vec!["up", "down"],
        },
        IndicatorMeta {
            name: "aroon_osc", label: "Aroon Oscillator", category: "trend",
            description: "Aroon Up − Aroon Down on a −100…+100 scale — positive = uptrend dominance, negative = downtrend.",
            params: vec![p_int("period", 25)],
            outputs: vec!["value"],
        },
        IndicatorMeta {
            name: "vortex", label: "Vortex Indicator", category: "trend",
            description: "+VI and −VI crossover signals new trend direction based on high-low range movement.",
            params: vec![p_int("period", 14)],
            outputs: vec!["plus_vi", "minus_vi"],
        },
        IndicatorMeta {
            name: "alligator", label: "Williams Alligator", category: "trend",
            description: "Three smoothed MAs (jaw/teeth/lips) that identify sleeping, awakening, and eating trend phases.",
            params: vec![p_int("jaw", 13), p_int("teeth", 8), p_int("lips", 5)],
            outputs: vec!["jaw", "teeth", "lips", "bullish", "bearish"],
        },
        IndicatorMeta {
            name: "gmma", label: "Guppy MMMA", category: "trend",
            description: "Guppy Multiple MA — short-term vs long-term group averages reveal underlying trend structure.",
            params: vec![],
            outputs: vec!["spread", "bullish", "short_0", "short_1", "short_2", "short_3", "short_4", "short_5", "long_0", "long_1", "long_2", "long_3", "long_4", "long_5"],
        },
        IndicatorMeta {
            name: "kdj", label: "KDJ", category: "trend",
            description: "Stochastic-based oscillator popular in Asian markets; J line amplifies divergence signals.",
            params: vec![p_int("period", 9), p_int("k_period", 3), p_int("d_period", 3)],
            outputs: vec!["k", "d", "j"],
        },
        IndicatorMeta {
            name: "kalman", label: "Kalman Filter", category: "trend",
            description: "Optimal state estimator tracking price position and velocity — minimal lag, low noise.",
            params: vec![
                p_float("q_pos", 0.001),
                p_float("q_vel", 0.001),
                p_float("r", 1.0),
            ],
            outputs: vec!["value", "velocity"],
        },
        // ── Momentum / Oscillator ─────────────────────────────────────────────
        IndicatorMeta {
            name: "rsi", label: "Relative Strength Index", category: "momentum",
            description: "Momentum oscillator bounded 0–100; above 70 is overbought, below 30 is oversold.",
            params: vec![p_int("period", 14)],
            outputs: vec!["value"],
        },
        IndicatorMeta {
            name: "cci", label: "Commodity Channel Index", category: "momentum",
            description: "Measures deviation of price from its statistical mean — cycles above/below zero indicate momentum.",
            params: vec![p_int("period", 20)],
            outputs: vec!["value"],
        },
        IndicatorMeta {
            name: "roc", label: "Rate of Change", category: "momentum",
            description: "Percentage price change over n bars — positive values indicate upward momentum.",
            params: vec![p_int("period", 10)],
            outputs: vec!["value"],
        },
        IndicatorMeta {
            name: "mom", label: "Momentum", category: "momentum",
            description: "Raw price difference over n bars without normalization — simple speed-of-change measure.",
            params: vec![p_int("period", 10)],
            outputs: vec!["value"],
        },
        IndicatorMeta {
            name: "cmo", label: "Chande Momentum Oscillator", category: "momentum",
            description: "Like RSI but uses the raw sum of up/down moves — bounded −100 to +100.",
            params: vec![p_int("period", 14)],
            outputs: vec!["value"],
        },
        IndicatorMeta {
            name: "dpo", label: "Detrended Price Oscillator", category: "momentum",
            description: "Removes trend from price to isolate underlying price cycles.",
            params: vec![p_int("period", 20)],
            outputs: vec!["value"],
        },
        IndicatorMeta {
            name: "mfi", label: "Money Flow Index", category: "momentum",
            description: "Volume-weighted RSI that detects accumulation and distribution pressure.",
            params: vec![p_int("period", 14)],
            outputs: vec!["value"],
        },
        IndicatorMeta {
            name: "bop", label: "Balance of Power", category: "momentum",
            description: "Measures buyer vs seller strength within the bar range: (close−open)/(high−low).",
            params: vec![],
            outputs: vec!["value"],
        },
        IndicatorMeta {
            name: "williams_r", label: "Williams %R", category: "momentum",
            description: "Inverted stochastic in the range −100 to 0; above −20 is overbought, below −80 is oversold.",
            params: vec![p_int("period", 14)],
            outputs: vec!["value"],
        },
        IndicatorMeta {
            name: "stochastic", label: "Stochastic Oscillator", category: "momentum",
            description: "Compares close to the high-low range over k periods; %D is the signal line smoothing.",
            params: vec![p_int("k_period", 14), p_int("d_period", 3)],
            outputs: vec!["k", "d"],
        },
        IndicatorMeta {
            name: "stoch_rsi", label: "Stochastic RSI", category: "momentum",
            description: "Applies the stochastic formula to RSI values for extra sensitivity to momentum shifts.",
            params: vec![p_int("rsi_period", 14), p_int("smooth_d", 3)],
            outputs: vec!["k", "d"],
        },
        IndicatorMeta {
            name: "tsi", label: "True Strength Index", category: "momentum",
            description: "Double-smoothed price change momentum oscillator — shows trend direction and exhaustion.",
            params: vec![p_int("first", 25), p_int("second", 13)],
            outputs: vec!["value"],
        },
        IndicatorMeta {
            name: "rci", label: "Rank Correlation Index", category: "momentum",
            description: "Spearman rank correlation between price and time — positive = uptrend, negative = downtrend.",
            params: vec![p_int("period", 9)],
            outputs: vec!["value"],
        },
        IndicatorMeta {
            name: "bull_bear", label: "Bull/Bear Power (Elder Ray)", category: "momentum",
            description: "Bull Power (high − EMA) and Bear Power (low − EMA) measure buying/selling energy.",
            params: vec![p_int("period", 13)],
            outputs: vec!["bull", "bear", "ema"],
        },
        IndicatorMeta {
            name: "fisher", label: "Fisher Transform", category: "momentum",
            description: "Converts price into a Gaussian normal distribution — sharp peaks signal turning points.",
            params: vec![p_int("period", 9)],
            outputs: vec!["fisher", "signal"],
        },
        IndicatorMeta {
            name: "kst", label: "Know Sure Thing", category: "momentum",
            description: "Weighted sum of multiple ROC signals — designed to identify major market cycle turns.",
            params: vec![p_int("signal", 9)],
            outputs: vec!["kst", "signal", "histogram"],
        },
        IndicatorMeta {
            name: "pmo", label: "Price Momentum Oscillator", category: "momentum",
            description: "Double-smoothed rate-of-change oscillator — more responsive than MACD for cycle timing.",
            params: vec![p_int("smooth1", 35), p_int("smooth2", 20), p_int("signal", 10)],
            outputs: vec!["pmo", "signal", "histogram"],
        },
        IndicatorMeta {
            name: "ppo", label: "Percentage Price Oscillator", category: "momentum",
            description: "MACD expressed as a percentage of the slow EMA — useful for comparing different price levels.",
            params: vec![p_int("fast", 12), p_int("slow", 26), p_int("signal", 9)],
            outputs: vec!["ppo", "signal", "histogram"],
        },
        IndicatorMeta {
            name: "rvi", label: "Relative Vigor Index", category: "momentum",
            description: "Measures closing strength relative to the bar range — confirms trend vigor.",
            params: vec![p_int("period", 10)],
            outputs: vec!["rvi", "signal"],
        },
        IndicatorMeta {
            name: "smi", label: "Stochastic Momentum Index", category: "momentum",
            description: "Refinement of the stochastic centred around zero — shows where close is relative to bar midpoint.",
            params: vec![p_int("period", 13), p_int("smooth1", 25), p_int("smooth2", 2), p_int("signal", 9)],
            outputs: vec!["smi", "signal"],
        },
        IndicatorMeta {
            name: "uo", label: "Ultimate Oscillator", category: "momentum",
            description: "Weighted combination of short, medium, and long-term buying pressure to reduce false divergence.",
            params: vec![p_int("fast", 7), p_int("medium", 14), p_int("slow", 28)],
            outputs: vec!["value"],
        },
        IndicatorMeta {
            name: "connors_rsi", label: "ConnorsRSI", category: "momentum",
            description: "Composite of price RSI, consecutive-bar streak RSI, and percentile rank — mean-reversion focused.",
            params: vec![
                p_int("rsi_period", 3),
                p_int("streak_period", 2),
                p_int("rank_period", 100),
            ],
            outputs: vec!["value"],
        },
        IndicatorMeta {
            name: "ao", label: "Awesome Oscillator", category: "momentum",
            description: "Difference of 5-bar and 34-bar SMA of midpoints — measures market momentum around a baseline.",
            params: vec![p_int("fast", 5), p_int("slow", 34)],
            outputs: vec!["value"],
        },
        IndicatorMeta {
            name: "coppock", label: "Coppock Curve", category: "momentum",
            description: "Long-term momentum indicator originally designed to identify major bear market recoveries.",
            params: vec![p_int("short", 11), p_int("long", 14), p_int("wma", 10)],
            outputs: vec!["value"],
        },
        // ── Volatility ────────────────────────────────────────────────────────
        IndicatorMeta {
            name: "atr", label: "Average True Range", category: "volatility",
            description: "Average of true range (max of high−low, |high−prev_close|, |low−prev_close|) — raw volatility.",
            params: vec![p_int("period", 14)],
            outputs: vec!["atr", "tr"],
        },
        IndicatorMeta {
            name: "bbands", label: "Bollinger Bands", category: "volatility",
            description: "Price envelope at ±n standard deviations around an SMA — expands in volatile markets.",
            params: vec![p_int("period", 20), p_float("multiplier", 2.0)],
            outputs: vec!["upper", "middle", "lower", "bandwidth", "percent_b"],
        },
        IndicatorMeta {
            name: "keltner", label: "Keltner Channel", category: "volatility",
            description: "ATR-based channel around an EMA — less sensitive to spikes than Bollinger Bands.",
            params: vec![p_int("period", 20), p_int("atr_period", 10), p_float("multiplier", 2.0)],
            outputs: vec!["upper", "middle", "lower"],
        },
        IndicatorMeta {
            name: "supertrend", label: "SuperTrend", category: "volatility",
            description: "ATR-based trailing stop that flips direction when price crosses the band — trend-following.",
            params: vec![p_int("period", 10), p_float("multiplier", 3.0)],
            outputs: vec!["value", "bullish"],
        },
        IndicatorMeta {
            name: "donchian", label: "Donchian Channel", category: "volatility",
            description: "Highest high and lowest low over the period — breakout above upper or below lower signals trend.",
            params: vec![p_int("period", 20)],
            outputs: vec!["upper", "middle", "lower"],
        },
        IndicatorMeta {
            name: "chop", label: "Choppiness Index", category: "volatility",
            description: "Ranges from 0–100: high values indicate sideways chop, low values indicate a strong trend.",
            params: vec![p_int("period", 14)],
            outputs: vec!["value"],
        },
        IndicatorMeta {
            name: "chop_zone", label: "ChopZone", category: "volatility",
            description: "EMA angle quantised into colour zones — positive angle = bullish trend, negative = bearish.",
            params: vec![p_int("ema_period", 34), p_float("threshold", 5.0)],
            outputs: vec!["angle", "zone"],
        },
        IndicatorMeta {
            name: "chandelier_exit", label: "Chandelier Exit", category: "volatility",
            description: "ATR-based stop placed below the highest high (long) or above the lowest low (short).",
            params: vec![p_int("period", 22), p_float("multiplier", 3.0)],
            outputs: vec!["long_stop", "short_stop", "atr"],
        },
        IndicatorMeta {
            name: "chande_kroll", label: "Chande Kroll Stop", category: "volatility",
            description: "Two-pass ATR stop that filters out noise — cleaner exit signals than a single-pass stop.",
            params: vec![
                p_int("atr_period", 10),
                p_float("factor", 1.5),
                p_int("stop_period", 9),
            ],
            outputs: vec!["stop_long", "stop_short"],
        },
        IndicatorMeta {
            name: "volatility_ratio", label: "Volatility Ratio", category: "volatility",
            description: "Ratio of current true range to historical ATR — values above 1 indicate a volatility expansion.",
            params: vec![p_int("lookback", 10)],
            outputs: vec!["value"],
        },
        // ── Volume ────────────────────────────────────────────────────────────
        IndicatorMeta {
            name: "obv", label: "On-Balance Volume", category: "volume",
            description: "Cumulates volume with a positive sign on up bars and negative on down bars — tracks money flow.",
            params: vec![],
            outputs: vec!["value"],
        },
        IndicatorMeta {
            name: "cmf", label: "Chaikin Money Flow", category: "volume",
            description: "Volume-weighted sum of close-location values over the period — above zero indicates buying pressure.",
            params: vec![p_int("period", 20)],
            outputs: vec!["value"],
        },
        IndicatorMeta {
            name: "vwap", label: "VWAP (session-aware)", category: "volume",
            description: "Volume-weighted average price that resets each session — used as a fair-value reference intraday.",
            params: vec![p_int_desc("session_gap_mins", 390, "minutes of inactivity that trigger a session reset")],
            outputs: vec!["value"],
        },
        // ── Pattern ───────────────────────────────────────────────────────────
        IndicatorMeta {
            name: "ichimoku", label: "Ichimoku Cloud", category: "pattern",
            description: "Comprehensive Japanese trend system: tenkan/kijun cross, cloud (kumo), and chikou span.",
            params: vec![p_int("tenkan", 9), p_int("kijun", 26), p_int("senkou_b", 52)],
            outputs: vec!["tenkan", "kijun", "senkou_a", "senkou_b", "chikou", "above_cloud", "below_cloud", "chikou_above", "chikou_below"],
        },
        IndicatorMeta {
            name: "parabolic_sar", label: "Parabolic SAR", category: "pattern",
            description: "Accelerating trailing stop that flips sides when price crosses it — bullish=1 when long.",
            params: vec![p_float("step", 0.02), p_float("max", 0.2)],
            outputs: vec!["sar", "bullish"],
        },
        IndicatorMeta {
            name: "rwi", label: "Random Walk Index", category: "pattern",
            description: "Tests whether high/low price movement exceeds what a random walk would produce over the period.",
            params: vec![p_int("period", 14)],
            outputs: vec!["rwi_high", "rwi_low"],
        },
        IndicatorMeta {
            name: "fractal", label: "Williams Fractal", category: "pattern",
            description: "Five-bar pattern marking local swing highs (bearish fractal) and swing lows (bullish fractal).",
            params: vec![],
            outputs: vec!["bullish", "bearish", "fractal_high", "fractal_low"],
        },
        IndicatorMeta {
            name: "elder_ray", label: "Elder Ray Index", category: "pattern",
            description: "Separates price into bull power (high − EMA) and bear power (low − EMA) — used with a trend filter in Elder's Triple Screen.",
            params: vec![p_int("period", 13)],
            outputs: vec!["bull_power", "bear_power", "ema"],
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
    "williams_r_ma",
    "fisher_crossover",
    "uo_reversal",
    "cmo_zero_cross",
    "vortex_trend",
    "cmf_ema_trend",
    "obv_ema_trend",
    "rsi_ma_cross",
    "bb_rsi_reversal",
    "adx_ema_cross",
    "lsma_cross",
    "alma_cross",
    "vwma_rsi",
    "smi_reversal",
    "ppo_histogram",
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


#[cfg(test)]
mod tests {
    use super::*;

    /// Every catalog entry must serialize `outputs` as `[{name, type}]` and tag
    /// bool-semantic fields as `"bool"`. Guards against the field-type info
    /// silently regressing to plain name strings.
    #[test]
    fn outputs_serialize_with_field_types() {
        let cat = all();
        let st = cat.iter().find(|m| m.name == "supertrend").expect("supertrend in catalog");
        let json = serde_json::to_value(st).unwrap();
        let outputs = json["outputs"].as_array().expect("outputs is array");

        let value = outputs.iter().find(|o| o["name"] == "value").expect("value field");
        assert_eq!(value["type"], "f64", "value must be f64 scalar");

        let bullish = outputs.iter().find(|o| o["name"] == "bullish").expect("bullish field");
        assert_eq!(bullish["type"], "bool", "bullish must be a bool flag");
    }

    /// Field-type classification is consistent across the whole catalog: every
    /// output named in `BOOL_FIELDS` serializes as `"bool"`, the rest as `"f64"`.
    #[test]
    fn every_output_type_matches_field_kind() {
        for meta in all() {
            let json = serde_json::to_value(&meta).unwrap();
            for out in json["outputs"].as_array().unwrap() {
                let name = out["name"].as_str().unwrap();
                let want = alm_indicator::field_kind(name).as_str();
                assert_eq!(out["type"], want, "{}.{} type mismatch", meta.name, name);
            }
        }
    }

    /// Catalog ↔ engine parity: what the FE sees (`GET /api/v1/indicators`) must
    /// match what indicators actually produce. Three checks:
    ///   1. Every catalog `name` is a real, buildable indicator type.
    ///   2. Each entry's `outputs` exactly equals the indicator's `field_names()`
    ///      (same names, same order) — otherwise the FE plots/labels the wrong fields.
    ///   3. Coverage both ways: catalog names == `KNOWN_INDICATOR_TYPES` (no
    ///      indicator missing from the FE, no phantom entry).
    #[test]
    fn catalog_matches_engine_indicators() {
        use std::collections::{HashMap, HashSet};
        use alm_indicator::IndicatorBox;
        use crate::script::v1::indicator_json_config;
        use crate::script::KNOWN_INDICATOR_TYPES;

        // Some indicators reject certain periods via panic (e.g. coppock needs
        // short < long). Try a few under a silenced hook; use the first that builds.
        let candidate_periods = [11usize, 14, 20, 26, 34, 52];
        let empty = HashMap::new();
        let prev_hook = std::panic::take_hook();
        std::panic::set_hook(Box::new(|_| {}));
        let build = |ty: &str| -> Option<IndicatorBox> {
            candidate_periods.iter().find_map(|&p| {
                std::panic::catch_unwind(std::panic::AssertUnwindSafe(|| {
                    IndicatorBox::from_config(&indicator_json_config(ty, p, &empty)).ok()
                })).ok().flatten()
            })
        };

        let mut unbuildable = Vec::new();
        let mut output_mismatch = Vec::new();

        for meta in all() {
            match build(meta.name) {
                None => unbuildable.push(meta.name),
                Some(bx) => {
                    let actual: Vec<&str> = bx.field_names().to_vec();
                    if meta.outputs != actual {
                        output_mismatch.push(format!(
                            "{}: catalog {:?} != engine {:?}", meta.name, meta.outputs, actual));
                    }
                }
            }
        }
        std::panic::set_hook(prev_hook);

        let cat_names: HashSet<&str> = all().iter().map(|m| m.name).collect();
        let known: HashSet<&str> = KNOWN_INDICATOR_TYPES.iter().copied().collect();
        let missing_from_catalog: Vec<&str> = known.difference(&cat_names).copied().collect();
        let phantom_in_catalog:   Vec<&str> = cat_names.difference(&known).copied().collect();

        assert!(unbuildable.is_empty(),
            "catalog names that don't build as indicators: {unbuildable:?}");
        assert!(output_mismatch.is_empty(),
            "catalog outputs ≠ field_names():\n  {}", output_mismatch.join("\n  "));
        assert!(missing_from_catalog.is_empty(),
            "indicators in KNOWN_INDICATOR_TYPES but missing from catalog (FE can't see them): {missing_from_catalog:?}");
        assert!(phantom_in_catalog.is_empty(),
            "catalog entries not in KNOWN_INDICATOR_TYPES (phantom): {phantom_in_catalog:?}");
    }
}

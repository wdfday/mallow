//! Static indicator catalog — returned by `GET /api/indicators`.

use serde::Serialize;
use serde_json::Value;

/// One indicator output field with its semantic type. Most fields are `"f64"`
/// scalars; a handful (`bullish`, `bearish`, `above_cloud`, …) are `"bool"`
/// flags encoded as `0.0`/`1.0` — they must be compared (`> 0.5`), never
/// negated with `!` (Rhai has no `!` for `f64`; use `flag(x)` helper).
#[derive(Debug, Serialize)]
#[cfg_attr(feature = "openapi", derive(utoipa::ToSchema))]
pub struct OutputField {
    pub name: &'static str,
    /// `"f64"` (scalar) | `"bool"` (0/1 flag, use `flag(x)` in Rhai).
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
    /// `"int"` | `"float"`
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
    /// Identifier used in `"type"` field and `ind.TYPE(...)` Rhai call.
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
    /// `true`  → `name[0]` is an `MEntry` (use `.field` accessor or compare via primary).
    /// `false` → `name[0]` is a plain `f64`.
    pub multi: bool,
    /// For multi-output indicators: the field used when comparing directly
    /// (`adx14[0] > 25` reads `.adx`). For single-output indicators this is
    /// always `"value"` and the note is informational only.
    pub primary: &'static str,
    /// Typical Rhai declaration snippet (copy-paste ready).
    pub declaration: &'static str,
    /// Common signal condition showing how to use the indicator's output.
    pub example: &'static str,
    /// `true` = overlaid on the price chart (main pane); `false` = separate sub-pane.
    pub overlay: bool,
}

fn p_int(name: &'static str, default: i64) -> ParamDef {
    ParamDef { name, type_: "int", default: serde_json::json!(default), description: None }
}
fn p_int_desc(name: &'static str, default: i64, desc: &'static str) -> ParamDef {
    ParamDef { name, type_: "int", default: serde_json::json!(default), description: Some(desc) }
}
fn p_float_desc(name: &'static str, default: f64, desc: &'static str) -> ParamDef {
    ParamDef { name, type_: "float", default: serde_json::json!(default), description: Some(desc) }
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
            multi: false, primary: "value",
            declaration: "let sma20 = ind.sma(20);",
            example: "if close > sma20[0] { long = 1; }",
            overlay: true,
        },
        IndicatorMeta {
            name: "ema", label: "Exponential Moving Average", category: "trend",
            description: "Exponentially-weighted average that gives more weight to recent bars — faster than SMA.",
            params: vec![p_int("period", 20)],
            outputs: vec!["value"],
            multi: false, primary: "value",
            declaration: "let ema9 = ind.ema(9); let ema21 = ind.ema(21);",
            example: "if ema9[0] > ema21[0] && ema9[1] <= ema21[1] { long = 1; }",
            overlay: true,
        },
        IndicatorMeta {
            name: "wma", label: "Weighted Moving Average", category: "trend",
            description: "Linearly weighted average where the most recent bar has the highest weight.",
            params: vec![p_int("period", 20)],
            outputs: vec!["value"],
            multi: false, primary: "value",
            declaration: "let wma14 = ind.wma(14);",
            example: "if close > wma14[0] { long = 1; }",
            overlay: true,
        },
        IndicatorMeta {
            name: "hma", label: "Hull Moving Average", category: "trend",
            description: "Hull's MA using WMA of WMA to dramatically reduce lag while staying smooth.",
            params: vec![p_int("period", 20)],
            outputs: vec!["value"],
            multi: false, primary: "value",
            declaration: "let hma20 = ind.hma(20);",
            example: "if hma20[0] > hma20[1] { long = 1; }",
            overlay: true,
        },
        IndicatorMeta {
            name: "dema", label: "Double EMA", category: "trend",
            description: "Double EMA that subtracts the error of EMA to eliminate most lag from a single EMA.",
            params: vec![p_int("period", 20)],
            outputs: vec!["value"],
            multi: false, primary: "value",
            declaration: "let dema9 = ind.dema(9); let dema21 = ind.dema(21);",
            example: "if dema9[0] > dema21[0] && dema9[1] <= dema21[1] { long = 1; }",
            overlay: true,
        },
        IndicatorMeta {
            name: "tema", label: "Triple EMA", category: "trend",
            description: "Triple EMA — even faster response than DEMA with further lag reduction.",
            params: vec![p_int("period", 20)],
            outputs: vec!["value"],
            multi: false, primary: "value",
            declaration: "let tema9 = ind.tema(9); let tema21 = ind.tema(21);",
            example: "if tema9[0] > tema21[0] && tema9[1] <= tema21[1] { long = 1; }",
            overlay: true,
        },
        IndicatorMeta {
            name: "smma", label: "Smoothed MA (RMA)", category: "trend",
            description: "Smoothed MA (alias RMA) — slow-reacting average that filters out short-term noise.",
            params: vec![p_int("period", 20)],
            outputs: vec!["value"],
            multi: false, primary: "value",
            declaration: "let smma14 = ind.smma(14);",
            example: "if close > smma14[0] { long = 1; }",
            overlay: true,
        },
        IndicatorMeta {
            name: "alma", label: "Arnaud Legoux MA", category: "trend",
            description: "Gaussian-weighted MA centred near the most recent bar for low lag and low noise.",
            params: vec![
                p_int("period", 9),
                p_float_desc("offset", 0.85, "centre of Gaussian window (0–1; higher = more recent)"),
                p_float_desc("sigma", 6.0, "width of Gaussian bell curve"),
            ],
            outputs: vec!["value"],
            multi: false, primary: "value",
            declaration: "let alma9 = ind.alma(9, offset=0.85, sigma=6.0);",
            example: "if close > alma9[0] { long = 1; }",
            overlay: true,
        },
        IndicatorMeta {
            name: "mcginley", label: "McGinley Dynamic", category: "trend",
            description: "Self-adjusting MA that automatically corrects for speed differences in market movement.",
            params: vec![p_int("period", 14)],
            outputs: vec!["value"],
            multi: false, primary: "value",
            declaration: "let mc14 = ind.mcginley(14);",
            example: "if close > mc14[0] { long = 1; }",
            overlay: true,
        },
        IndicatorMeta {
            name: "lsma", label: "Least Squares MA", category: "trend",
            description: "Linear regression line fit over the window — slope indicates trend direction and strength.",
            params: vec![p_int("period", 25)],
            outputs: vec!["value", "slope"],
            multi: true, primary: "value",
            declaration: "let ls25 = ind.lsma(25);",
            example: "if ls25[0].slope > 0 && close > ls25[0].value { long = 1; }",
            overlay: true,
        },
        IndicatorMeta {
            name: "vwma", label: "Volume-Weighted MA", category: "trend",
            description: "Volume-weighted MA where high-volume bars exert proportionally more influence.",
            params: vec![p_int("period", 20)],
            outputs: vec!["value"],
            multi: false, primary: "value",
            declaration: "let vwma20 = ind.vwma(20);",
            example: "if close > vwma20[0] { long = 1; }",
            overlay: true,
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
            multi: false, primary: "value",
            declaration: "let kama10 = ind.kama(10, fast=2, slow=30);",
            example: "if kama10[0] > kama10[1] { long = 1; }",
            overlay: true,
        },
        IndicatorMeta {
            name: "macd", label: "MACD", category: "trend",
            description: "Moving Average Convergence Divergence — difference between fast and slow EMAs with a signal line.",
            params: vec![p_int("fast", 12), p_int("slow", 26), p_int("signal", 9)],
            outputs: vec!["macd", "signal", "histogram"],
            multi: true, primary: "macd",
            declaration: "let macd1 = ind.macd(12, 26, 9);",
            example: "if macd1[0].histogram > 0 && macd1[1].histogram <= 0 { long = 1; }",
            overlay: false,
        },
        IndicatorMeta {
            name: "trix", label: "TRIX", category: "trend",
            description: "1-day rate-of-change of a triple-smoothed EMA — filters noise and shows momentum.",
            params: vec![p_int("period", 18), p_int("signal", 9)],
            outputs: vec!["trix", "signal", "histogram"],
            multi: true, primary: "trix",
            declaration: "let trix18 = ind.trix(18, 9);",
            example: "if trix18[0].trix > trix18[0].signal && trix18[1].trix <= trix18[1].signal { long = 1; }",
            overlay: false,
        },
        IndicatorMeta {
            name: "adx", label: "Average Directional Index", category: "trend",
            description: "Trend-strength on a 0–100 scale (above 25 = strong trend), plus +DI/-DI directional lines.",
            params: vec![p_int("period", 14)],
            outputs: vec!["adx", "plus_di", "minus_di"],
            multi: true, primary: "adx",
            declaration: "let adx14 = ind.adx(14);",
            example: "if adx14[0] > 25 && adx14[0].plus_di > adx14[0].minus_di { long = 1; }",
            overlay: false,
        },
        IndicatorMeta {
            name: "dmi", label: "DMI (Directional Movement)", category: "trend",
            description: "Directional Movement Index — +DI vs −DI crossover signals trend direction changes.",
            params: vec![p_int("period", 14)],
            outputs: vec!["plus_di", "minus_di"],
            multi: true, primary: "plus_di",
            declaration: "let dmi14 = ind.dmi(14);",
            example: "if dmi14[0].plus_di > dmi14[0].minus_di && dmi14[1].plus_di <= dmi14[1].minus_di { long = 1; }",
            overlay: false,
        },
        IndicatorMeta {
            name: "aroon", label: "Aroon", category: "trend",
            description: "Measures how many bars ago the highest high / lowest low occurred to gauge trend age.",
            params: vec![p_int("period", 25)],
            outputs: vec!["up", "down"],
            multi: true, primary: "up",
            declaration: "let aroon25 = ind.aroon(25);",
            example: "if aroon25[0].up > 70 && aroon25[0].down < 30 { long = 1; }",
            overlay: false,
        },
        IndicatorMeta {
            name: "aroon_osc", label: "Aroon Oscillator", category: "trend",
            description: "Aroon Up − Aroon Down on a −100…+100 scale — positive = uptrend dominance.",
            params: vec![p_int("period", 25)],
            outputs: vec!["value"],
            multi: false, primary: "value",
            declaration: "let arosc25 = ind.aroon_osc(25);",
            example: "if arosc25[0] > 0 && arosc25[1] <= 0 { long = 1; }",
            overlay: false,
        },
        IndicatorMeta {
            name: "vortex", label: "Vortex Indicator", category: "trend",
            description: "+VI and −VI crossover signals new trend direction based on high-low range movement.",
            params: vec![p_int("period", 14)],
            outputs: vec!["plus_vi", "minus_vi"],
            multi: true, primary: "plus_vi",
            declaration: "let vx14 = ind.vortex(14);",
            example: "if vx14[0].plus_vi > vx14[0].minus_vi && vx14[1].plus_vi <= vx14[1].minus_vi { long = 1; }",
            overlay: false,
        },
        IndicatorMeta {
            name: "alligator", label: "Williams Alligator", category: "trend",
            description: "Three smoothed MAs (jaw/teeth/lips) that identify sleeping, awakening, and eating trend phases.",
            params: vec![
                p_int_desc("jaw", 13, "jaw period (slowest, blue)"),
                p_int_desc("teeth", 8, "teeth period (medium, red)"),
                p_int_desc("lips", 5, "lips period (fastest, green)"),
            ],
            outputs: vec!["jaw", "teeth", "lips", "bullish", "bearish"],
            multi: true, primary: "teeth",
            declaration: "let ali = ind.alligator(13, 8, 5);",
            example: "if flag(ali[0].bullish) && !flag(ali[1].bullish) { long = 1; }",
            overlay: true,
        },
        IndicatorMeta {
            name: "gmma", label: "Guppy MMMA", category: "trend",
            description: "Guppy Multiple MA — two groups of EMAs (short/long) reveal underlying trend structure.",
            params: vec![],
            outputs: vec!["spread", "bullish",
                "short_0","short_1","short_2","short_3","short_4","short_5",
                "long_0","long_1","long_2","long_3","long_4","long_5"],
            multi: true, primary: "spread",
            declaration: "let gmma = ind.gmma(0);",
            example: "if gmma[0] > 0 && flag(gmma[0].bullish) { long = 1; }",
            overlay: true,
        },
        IndicatorMeta {
            name: "kdj", label: "KDJ", category: "trend",
            description: "Stochastic-based oscillator popular in Asian markets; J line amplifies divergence signals.",
            params: vec![p_int("period", 9), p_int("k_period", 3), p_int("d_period", 3)],
            outputs: vec!["k", "d", "j"],
            multi: true, primary: "k",
            declaration: "let kdj9 = ind.kdj(9, 3, 3);",
            example: "if kdj9[0].k > kdj9[0].d && kdj9[0].k < 20 { long = 1; }",
            overlay: false,
        },
        IndicatorMeta {
            name: "kalman", label: "Kalman Filter", category: "trend",
            description: "Optimal state estimator tracking price (value) and velocity — minimal lag, low noise.",
            params: vec![
                p_float_desc("q_pos", 0.001, "process noise for position"),
                p_float_desc("q_vel", 0.001, "process noise for velocity"),
                p_float_desc("r", 1.0, "measurement noise"),
            ],
            outputs: vec!["value", "velocity"],
            multi: true, primary: "value",
            declaration: "let kf = ind.kalman(0, q_pos=0.001, q_vel=0.001, r=1.0);",
            example: "if kf[0].velocity > 0 && close > kf[0].value { long = 1; }",
            overlay: true,
        },
        // ── Momentum / Oscillator ─────────────────────────────────────────────
        IndicatorMeta {
            name: "rsi", label: "Relative Strength Index", category: "momentum",
            description: "Momentum oscillator bounded 0–100; above 70 is overbought, below 30 is oversold.",
            params: vec![p_int("period", 14)],
            outputs: vec!["value"],
            multi: false, primary: "value",
            declaration: "let rsi14 = ind.rsi(14);",
            example: "if rsi14[0] < 30 { long = 1; } else if rsi14[0] > 70 { short = 1; }",
            overlay: false,
        },
        IndicatorMeta {
            name: "cci", label: "Commodity Channel Index", category: "momentum",
            description: "Measures deviation of price from its statistical mean — cycles above/below zero indicate momentum.",
            params: vec![p_int("period", 20)],
            outputs: vec!["value"],
            multi: false, primary: "value",
            declaration: "let cci20 = ind.cci(20);",
            example: "if cci20[0] > 100 { long = 1; } else if cci20[0] < -100 { short = 1; }",
            overlay: false,
        },
        IndicatorMeta {
            name: "roc", label: "Rate of Change", category: "momentum",
            description: "Percentage price change over n bars — positive values indicate upward momentum.",
            params: vec![p_int("period", 10)],
            outputs: vec!["value"],
            multi: false, primary: "value",
            declaration: "let roc10 = ind.roc(10);",
            example: "if roc10[0] > 0 && roc10[1] <= 0 { long = 1; }",
            overlay: false,
        },
        IndicatorMeta {
            name: "mom", label: "Momentum", category: "momentum",
            description: "Raw price difference over n bars without normalization — simple speed-of-change measure.",
            params: vec![p_int("period", 10)],
            outputs: vec!["value"],
            multi: false, primary: "value",
            declaration: "let mom10 = ind.mom(10);",
            example: "if mom10[0] > 0 && mom10[1] <= 0 { long = 1; }",
            overlay: false,
        },
        IndicatorMeta {
            name: "cmo", label: "Chande Momentum Oscillator", category: "momentum",
            description: "Like RSI but uses the raw sum of up/down moves — bounded −100 to +100.",
            params: vec![p_int("period", 14)],
            outputs: vec!["value"],
            multi: false, primary: "value",
            declaration: "let cmo14 = ind.cmo(14);",
            example: "if cmo14[0] > 50 { long = 1; } else if cmo14[0] < -50 { short = 1; }",
            overlay: false,
        },
        IndicatorMeta {
            name: "dpo", label: "Detrended Price Oscillator", category: "momentum",
            description: "Removes trend from price to isolate underlying price cycles.",
            params: vec![p_int("period", 20)],
            outputs: vec!["value"],
            multi: false, primary: "value",
            declaration: "let dpo20 = ind.dpo(20);",
            example: "if dpo20[0] > 0 && dpo20[1] <= 0 { long = 1; }",
            overlay: false,
        },
        IndicatorMeta {
            name: "mfi", label: "Money Flow Index", category: "momentum",
            description: "Volume-weighted RSI that detects accumulation and distribution pressure.",
            params: vec![p_int("period", 14)],
            outputs: vec!["value"],
            multi: false, primary: "value",
            declaration: "let mfi14 = ind.mfi(14);",
            example: "if mfi14[0] < 20 { long = 1; } else if mfi14[0] > 80 { short = 1; }",
            overlay: false,
        },
        IndicatorMeta {
            name: "bop", label: "Balance of Power", category: "momentum",
            description: "Measures buyer vs seller strength within the bar range: (close−open)/(high−low).",
            params: vec![],
            outputs: vec!["value"],
            multi: false, primary: "value",
            declaration: "let bop = ind.bop(0);",
            example: "if bop[0] > 0 && bop[1] <= 0 { long = 1; }",
            overlay: false,
        },
        IndicatorMeta {
            name: "williams_r", label: "Williams %R", category: "momentum",
            description: "Inverted stochastic in the range −100 to 0; above −20 is overbought, below −80 is oversold.",
            params: vec![p_int("period", 14)],
            outputs: vec!["value"],
            multi: false, primary: "value",
            declaration: "let wr14 = ind.williams_r(14);",
            example: "if wr14[0] < -80 { long = 1; } else if wr14[0] > -20 { short = 1; }",
            overlay: false,
        },
        IndicatorMeta {
            name: "stochastic", label: "Stochastic Oscillator", category: "momentum",
            description: "Compares close to the high-low range over k periods; %D is the signal line smoothing.",
            params: vec![p_int("k_period", 14), p_int("d_period", 3)],
            outputs: vec!["k", "d"],
            multi: true, primary: "k",
            declaration: "let stoch14 = ind.stochastic(14, 3);",
            example: "if stoch14[0].k > stoch14[0].d && stoch14[0].k < 20 { long = 1; }",
            overlay: false,
        },
        IndicatorMeta {
            name: "stoch_rsi", label: "Stochastic RSI", category: "momentum",
            description: "Applies the stochastic formula to RSI values for extra sensitivity to momentum shifts.",
            params: vec![p_int("rsi_period", 14), p_int("smooth_d", 3)],
            outputs: vec!["k", "d"],
            multi: true, primary: "k",
            declaration: "let srsi14 = ind.stoch_rsi(14, 3);",
            example: "if srsi14[0].k > srsi14[0].d && srsi14[0].k < 0.2 { long = 1; }",
            overlay: false,
        },
        IndicatorMeta {
            name: "tsi", label: "True Strength Index", category: "momentum",
            description: "Double-smoothed price change momentum oscillator bounded −100…+100 — shows trend direction and exhaustion.",
            params: vec![
                p_int_desc("first", 25, "outer smoothing period"),
                p_int_desc("second", 13, "inner smoothing period"),
            ],
            outputs: vec!["value"],
            multi: false, primary: "value",
            declaration: "let tsi = ind.tsi(25, 13);",
            example: "if tsi[0] > 0 && tsi[1] <= 0 { long = 1; }",
            overlay: false,
        },
        IndicatorMeta {
            name: "rci", label: "Rank Correlation Index", category: "momentum",
            description: "Spearman rank correlation between price and time — +100 = perfect uptrend, −100 = downtrend.",
            params: vec![p_int("period", 9)],
            outputs: vec!["value"],
            multi: false, primary: "value",
            declaration: "let rci9 = ind.rci(9);",
            example: "if rci9[0] < -80 { long = 1; } else if rci9[0] > 80 { short = 1; }",
            overlay: false,
        },
        IndicatorMeta {
            name: "bull_bear", label: "Bull/Bear Power", category: "momentum",
            description: "Bull Power (high − EMA) and Bear Power (low − EMA) measure buying/selling energy independently.",
            params: vec![p_int("period", 13)],
            outputs: vec!["bull", "bear", "ema"],
            multi: true, primary: "bull",
            declaration: "let bb13 = ind.bull_bear(13);",
            example: "if bb13[0].bull > 0 && bb13[0].bear < 0 { long = 1; }",
            overlay: false,
        },
        IndicatorMeta {
            name: "fisher", label: "Fisher Transform", category: "momentum",
            description: "Converts price into a Gaussian normal distribution — sharp peaks signal turning points.",
            params: vec![p_int("period", 9)],
            outputs: vec!["fisher", "signal"],
            multi: true, primary: "fisher",
            declaration: "let fsh9 = ind.fisher(9);",
            example: "if fsh9[0].fisher > fsh9[0].signal && fsh9[1].fisher <= fsh9[1].signal { long = 1; }",
            overlay: false,
        },
        IndicatorMeta {
            name: "kst", label: "Know Sure Thing", category: "momentum",
            description: "Weighted sum of multiple ROC signals — designed to identify major market cycle turns.",
            params: vec![p_int("signal", 9)],
            outputs: vec!["kst", "signal", "histogram"],
            multi: true, primary: "kst",
            declaration: "let kst = ind.kst(9);",
            example: "if kst[0].kst > kst[0].signal && kst[1].kst <= kst[1].signal { long = 1; }",
            overlay: false,
        },
        IndicatorMeta {
            name: "pmo", label: "Price Momentum Oscillator", category: "momentum",
            description: "Double-smoothed rate-of-change oscillator — more responsive than MACD for cycle timing.",
            params: vec![p_int("smooth1", 35), p_int("smooth2", 20), p_int("signal", 10)],
            outputs: vec!["pmo", "signal", "histogram"],
            multi: true, primary: "pmo",
            declaration: "let pmo = ind.pmo(35, 20, 10);",
            example: "if pmo[0].pmo > pmo[0].signal && pmo[1].pmo <= pmo[1].signal { long = 1; }",
            overlay: false,
        },
        IndicatorMeta {
            name: "ppo", label: "Percentage Price Oscillator", category: "momentum",
            description: "MACD expressed as a percentage of the slow EMA — useful for comparing instruments at different price levels.",
            params: vec![p_int("fast", 12), p_int("slow", 26), p_int("signal", 9)],
            outputs: vec!["ppo", "signal", "histogram"],
            multi: true, primary: "ppo",
            declaration: "let ppo = ind.ppo(12, 26, 9);",
            example: "if ppo[0].histogram > 0 && ppo[1].histogram <= 0 { long = 1; }",
            overlay: false,
        },
        IndicatorMeta {
            name: "rvi", label: "Relative Vigor Index", category: "momentum",
            description: "Measures closing strength relative to the bar range — confirms trend vigor.",
            params: vec![p_int("period", 10)],
            outputs: vec!["rvi", "signal"],
            multi: true, primary: "rvi",
            declaration: "let rvi10 = ind.rvi(10);",
            example: "if rvi10[0].rvi > rvi10[0].signal { long = 1; }",
            overlay: false,
        },
        IndicatorMeta {
            name: "smi", label: "Stochastic Momentum Index", category: "momentum",
            description: "Refinement of the stochastic centred around zero — shows where close is relative to bar midpoint.",
            params: vec![
                p_int("period", 13),
                p_int_desc("smooth1", 25, "first smoothing period"),
                p_int_desc("smooth2", 2, "second smoothing period"),
                p_int("signal", 9),
            ],
            outputs: vec!["smi", "signal"],
            multi: true, primary: "smi",
            declaration: "let smi13 = ind.smi(13, 25, 2, 9);",
            example: "if smi13[0].smi > smi13[0].signal { long = 1; }",
            overlay: false,
        },
        IndicatorMeta {
            name: "uo", label: "Ultimate Oscillator", category: "momentum",
            description: "Weighted combination of short, medium, and long-term buying pressure to reduce false divergence.",
            params: vec![p_int("fast", 7), p_int("medium", 14), p_int("slow", 28)],
            outputs: vec!["value"],
            multi: false, primary: "value",
            declaration: "let uo = ind.uo(7, 14, 28);",
            example: "if uo[0] < 30 { long = 1; } else if uo[0] > 70 { short = 1; }",
            overlay: false,
        },
        IndicatorMeta {
            name: "connors_rsi", label: "ConnorsRSI", category: "momentum",
            description: "Composite of price RSI, consecutive-bar streak RSI, and percentile rank — mean-reversion focused.",
            params: vec![
                p_int_desc("rsi_period", 3, "price RSI period"),
                p_int_desc("streak_period", 2, "consecutive-bar streak RSI period"),
                p_int_desc("rank_period", 100, "percentile rank lookback"),
            ],
            outputs: vec!["value"],
            multi: false, primary: "value",
            declaration: "let crsi = ind.connors_rsi(3, 2, 100);",
            example: "if crsi[0] < 10 { long = 1; } else if crsi[0] > 90 { short = 1; }",
            overlay: false,
        },
        IndicatorMeta {
            name: "ao", label: "Awesome Oscillator", category: "momentum",
            description: "Difference of 5-bar and 34-bar SMA of midpoints — measures market momentum around a baseline.",
            params: vec![
                p_int_desc("fast", 5, "fast SMA period"),
                p_int_desc("slow", 34, "slow SMA period"),
            ],
            outputs: vec!["value"],
            multi: false, primary: "value",
            declaration: "let ao = ind.ao(0, fast=5, slow=34);",
            example: "if ao[0] > 0 && ao[1] <= 0 { long = 1; }",
            overlay: false,
        },
        IndicatorMeta {
            name: "coppock", label: "Coppock Curve", category: "momentum",
            description: "Long-term momentum indicator originally designed to identify major bear market recoveries.",
            params: vec![
                p_int_desc("short", 11, "short ROC period"),
                p_int_desc("long", 14, "long ROC period"),
                p_int_desc("wma", 10, "WMA smoothing period"),
            ],
            outputs: vec!["value"],
            multi: false, primary: "value",
            declaration: "let cop = ind.coppock(0, short=11, long=14, wma=10);",
            example: "if cop[0] > 0 && cop[1] <= 0 { long = 1; }",
            overlay: false,
        },
        // ── Volatility ────────────────────────────────────────────────────────
        IndicatorMeta {
            name: "atr", label: "Average True Range", category: "volatility",
            description: "Average of true range (max of high−low, |high−prev_close|, |low−prev_close|) — raw volatility measure.",
            params: vec![p_int("period", 14)],
            outputs: vec!["atr", "tr"],
            multi: true, primary: "atr",
            declaration: "let atr14 = ind.atr(14);",
            example: "stop_price = close - atr14[0].atr * 2.0;",
            overlay: false,
        },
        IndicatorMeta {
            name: "bbands", label: "Bollinger Bands", category: "volatility",
            description: "Price envelope at ±n standard deviations around an SMA — expands in volatile markets.",
            params: vec![
                p_int("period", 20),
                p_float_desc("multiplier", 2.0, "standard deviation multiplier"),
            ],
            outputs: vec!["upper", "middle", "lower", "bandwidth", "percent_b"],
            multi: true, primary: "middle",
            declaration: "let bb20 = ind.bbands(20, multiplier=2.0);",
            example: "if close < bb20[0].lower { long = 1; } else if close > bb20[0].upper { short = 1; }",
            overlay: true,
        },
        IndicatorMeta {
            name: "keltner", label: "Keltner Channel", category: "volatility",
            description: "ATR-based channel around an EMA — less sensitive to price spikes than Bollinger Bands.",
            params: vec![
                p_int("period", 20),
                p_int_desc("atr_period", 10, "ATR smoothing period"),
                p_float_desc("multiplier", 2.0, "ATR multiplier for channel width"),
            ],
            outputs: vec!["upper", "middle", "lower"],
            multi: true, primary: "middle",
            declaration: "let kc20 = ind.keltner(20, 10, multiplier=2.0);",
            example: "if close > kc20[0].upper { long = 1; } else if close < kc20[0].lower { short = 1; }",
            overlay: true,
        },
        IndicatorMeta {
            name: "supertrend", label: "SuperTrend", category: "volatility",
            description: "ATR-based trailing stop that flips direction when price crosses the band — trend-following.",
            params: vec![
                p_int("period", 10),
                p_float_desc("multiplier", 3.0, "ATR multiplier for band width"),
            ],
            outputs: vec!["value", "bullish", "bearish"],
            multi: true, primary: "value",
            declaration: "let st10 = ind.supertrend(10, multiplier=3.0);",
            example: "if flag(st10[0].bullish) && !flag(st10[1].bullish) { long = 1; }",
            overlay: true,
        },
        IndicatorMeta {
            name: "donchian", label: "Donchian Channel", category: "volatility",
            description: "Highest high and lowest low over the period — breakout above upper or below lower signals trend.",
            params: vec![p_int("period", 20)],
            outputs: vec!["upper", "middle", "lower"],
            multi: true, primary: "middle",
            declaration: "let dc20 = ind.donchian(20);",
            example: "if close > dc20[0].upper { long = 1; } else if close < dc20[0].lower { short = 1; }",
            overlay: true,
        },
        IndicatorMeta {
            name: "chop", label: "Choppiness Index", category: "volatility",
            description: "Ranges 0–100: high (>61.8) = choppy/ranging; low (<38.2) = strong directional trend.",
            params: vec![p_int("period", 14)],
            outputs: vec!["value"],
            multi: false, primary: "value",
            declaration: "let chop14 = ind.chop(14);",
            example: "if chop14[0] < 38.2 { long = 1; }",
            overlay: false,
        },
        IndicatorMeta {
            name: "chop_zone", label: "ChopZone", category: "volatility",
            description: "EMA angle quantised into colour zones — positive angle = bullish, negative = bearish, near-zero = choppy.",
            params: vec![
                p_int_desc("ema_period", 34, "EMA period for angle calculation"),
                p_float_desc("threshold", 5.0, "minimum angle (degrees) to classify as trending"),
            ],
            outputs: vec!["angle", "zone"],
            multi: true, primary: "zone",
            declaration: "let cz34 = ind.chop_zone(34, threshold=5.0);",
            example: "if cz34[0].angle > 5.0 { long = 1; } else if cz34[0].angle < -5.0 { short = 1; }",
            overlay: false,
        },
        IndicatorMeta {
            name: "chandelier_exit", label: "Chandelier Exit", category: "volatility",
            description: "ATR-based stop placed below the highest high (long stop) or above the lowest low (short stop).",
            params: vec![
                p_int("period", 22),
                p_float_desc("multiplier", 3.0, "ATR multiplier"),
            ],
            outputs: vec!["long_stop", "short_stop", "atr"],
            multi: true, primary: "long_stop",
            declaration: "let ce22 = ind.chandelier_exit(22, multiplier=3.0);",
            example: "if close > ce22[1].long_stop && close[1] <= ce22[1].long_stop { long = 1; }",
            overlay: true,
        },
        IndicatorMeta {
            name: "chande_kroll", label: "Chande Kroll Stop", category: "volatility",
            description: "Two-pass ATR stop that filters out noise — cleaner exit signals than a single-pass stop.",
            params: vec![
                p_int_desc("atr_period", 10, "ATR period for first stop"),
                p_float_desc("factor", 1.5, "ATR multiplier for first stop"),
                p_int_desc("stop_period", 9, "highest-high/lowest-low period for final stop"),
            ],
            outputs: vec!["stop_long", "stop_short"],
            multi: true, primary: "stop_long",
            declaration: "let ck = ind.chande_kroll(10, factor=1.5, stop_period=9);",
            example: "if close > ck[0].stop_long { long = 1; } else if close < ck[0].stop_short { short = 1; }",
            overlay: true,
        },
        IndicatorMeta {
            name: "volatility_ratio", label: "Volatility Ratio", category: "volatility",
            description: "Ratio of current true range to the highest true range over the lookback — values >1 indicate volatility expansion.",
            params: vec![p_int_desc("lookback", 10, "highest-TR lookback")],
            outputs: vec!["value"],
            multi: false, primary: "value",
            declaration: "let vr10 = ind.volatility_ratio(10);",
            example: "if vr10[0] > 1.0 { long = 1; }",
            overlay: false,
        },
        // ── Volume ────────────────────────────────────────────────────────────
        IndicatorMeta {
            name: "obv", label: "On-Balance Volume", category: "volume",
            description: "Cumulates volume with a positive sign on up bars and negative on down bars — tracks money flow direction.",
            params: vec![],
            outputs: vec!["value"],
            multi: false, primary: "value",
            declaration: "let obv = ind.obv(0); let obv_ema = ind.ema(20);",
            example: "if obv[0] > obv_ema[0] && obv[1] <= obv_ema[1] { long = 1; }",
            overlay: false,
        },
        IndicatorMeta {
            name: "cmf", label: "Chaikin Money Flow", category: "volume",
            description: "Volume-weighted sum of close-location values over the period — above zero indicates buying pressure.",
            params: vec![p_int("period", 20)],
            outputs: vec!["value"],
            multi: false, primary: "value",
            declaration: "let cmf20 = ind.cmf(20);",
            example: "if cmf20[0] > 0 && cmf20[1] <= 0 { long = 1; }",
            overlay: false,
        },
        IndicatorMeta {
            name: "vwap", label: "VWAP (session-aware)", category: "volume",
            description: "Volume-weighted average price that resets each session — intraday fair-value reference.",
            params: vec![p_int_desc("session_gap_mins", 390, "minutes of inactivity that trigger a session reset")],
            outputs: vec!["value"],
            multi: false, primary: "value",
            declaration: "let vwap = ind.vwap(0);",
            example: "if close > vwap[0] { long = 1; }",
            overlay: true,
        },
        // ── Pattern ───────────────────────────────────────────────────────────
        IndicatorMeta {
            name: "ichimoku", label: "Ichimoku Cloud", category: "pattern",
            description: "Comprehensive Japanese trend system: tenkan/kijun cross, cloud (senkou_a/b), and chikou span.",
            params: vec![
                p_int_desc("tenkan", 9, "tenkan-sen (conversion line) period"),
                p_int_desc("kijun", 26, "kijun-sen (base line) period"),
                p_int_desc("senkou_b", 52, "senkou span B period"),
            ],
            outputs: vec!["tenkan","kijun","senkou_a","senkou_b","chikou",
                          "above_cloud","below_cloud","chikou_above","chikou_below"],
            multi: true, primary: "tenkan",
            declaration: "let ichi = ind.ichimoku(9, 26, 52);",
            example: "if ichi[0].tenkan > ichi[0].kijun && flag(ichi[0].above_cloud) { long = 1; }",
            overlay: true,
        },
        IndicatorMeta {
            name: "parabolic_sar", label: "Parabolic SAR", category: "pattern",
            description: "Accelerating trailing stop that flips sides when price crosses it — `bullish` = 1.0 when long side.",
            params: vec![
                p_float_desc("step", 0.02, "acceleration factor increment"),
                p_float_desc("max", 0.2, "maximum acceleration factor"),
            ],
            outputs: vec!["sar", "bullish"],
            multi: true, primary: "sar",
            declaration: "let psar = ind.parabolic_sar(0, step=0.02, max=0.2);",
            example: "if flag(psar[0].bullish) && !flag(psar[1].bullish) { long = 1; }",
            overlay: true,
        },
        IndicatorMeta {
            name: "rwi", label: "Random Walk Index", category: "pattern",
            description: "Tests whether high/low movement exceeds what a random walk would produce — >1 confirms directional move.",
            params: vec![p_int("period", 14)],
            outputs: vec!["rwi_high", "rwi_low"],
            multi: true, primary: "rwi_high",
            declaration: "let rwi14 = ind.rwi(14);",
            example: "if rwi14[0].rwi_high > 1.0 { long = 1; } else if rwi14[0].rwi_low > 1.0 { short = 1; }",
            overlay: false,
        },
        IndicatorMeta {
            name: "fractal", label: "Williams Fractal", category: "pattern",
            description: "Five-bar pattern marking local swing highs (bearish fractal) and swing lows (bullish fractal) — 2-bar lag.",
            params: vec![],
            outputs: vec!["bullish", "bearish", "fractal_high", "fractal_low"],
            multi: true, primary: "bullish",
            declaration: "let frac = ind.fractal(0);",
            example: "if flag(frac[0].bullish) { long = 1; } else if flag(frac[0].bearish) { short = 1; }",
            overlay: true,
        },
        IndicatorMeta {
            name: "elder_ray", label: "Elder Ray Index", category: "pattern",
            description: "Separates price power into bull_power (high − EMA) and bear_power (low − EMA) — used with a trend filter.",
            params: vec![p_int("period", 13)],
            outputs: vec!["bull_power", "bear_power", "ema"],
            multi: true, primary: "bull_power",
            declaration: "let er13 = ind.elder_ray(13);",
            example: "if er13[0].bull_power > 0 && er13[0].bear_power < 0 { long = 1; }",
            overlay: false,
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
    use crate::test_utils::*;
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

    /// `multi` and `primary` fields must be consistent with what the script
    /// engine actually does (derived from `map_indicator_type`).
    #[test]
    fn multi_and_primary_match_engine() {
        use crate::script::v1::{map_indicator_type, IndicatorKind};
        for meta in all() {
            let (_, kind) = map_indicator_type(meta.name);
            let engine_multi = matches!(kind, IndicatorKind::Multi(_));
            assert_eq!(meta.multi, engine_multi,
                "{}: catalog multi={} but engine says {}", meta.name, meta.multi, engine_multi);
            let engine_primary = match &kind {
                IndicatorKind::Single(f) | IndicatorKind::Multi(f) => f.as_str(),
            };
            assert_eq!(meta.primary, engine_primary,
                "{}: catalog primary='{}' but engine primary='{}'",
                meta.name, meta.primary, engine_primary);
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

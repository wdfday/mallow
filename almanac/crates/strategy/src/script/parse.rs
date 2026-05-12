use anyhow::Result;
use alm_core::Timeframe;
use alm_indicator::IndicatorBox;
use serde_json::json;

use super::engine::DEFAULT_BUF_DEPTH;

// ── IndicatorKind ─────────────────────────────────────────────────────────────

/// Whether an indicator returns a single scalar or a multi-field map per bar.
#[derive(Clone, Debug)]
pub(super) enum IndicatorKind {
    /// Extract one named field from the IndicatorBox output → `Array<f64>`.
    Single(String),
    /// Expose the full field map → `Array<Map>`, e.g. `macd[0].histogram`.
    Multi,
}

// ── IndicatorDecl ─────────────────────────────────────────────────────────────

pub(super) struct IndicatorDecl {
    pub(super) var_name:  String,
    pub(super) ind_type:  String,
    pub(super) period:    usize,
    pub(super) buf_depth: usize,
    pub(super) kind:      IndicatorKind,
    pub(super) timeframe: Option<Timeframe>,
    pub(super) live:      bool,
}

// ── Timeframe parser ──────────────────────────────────────────────────────────

fn parse_timeframe(s: &str) -> Option<Timeframe> {
    match s.to_uppercase().as_str() {
        "M1"  => Some(Timeframe::M1),
        "M3"  => Some(Timeframe::M3),
        "M5"  => Some(Timeframe::M5),
        "M10" => Some(Timeframe::M10),
        "M15" => Some(Timeframe::M15),
        "M30" => Some(Timeframe::M30),
        "H1"  => Some(Timeframe::H1),
        "H2"  => Some(Timeframe::H2),
        "H4"  => Some(Timeframe::H4),
        "H6"  => Some(Timeframe::H6),
        "H8"  => Some(Timeframe::H8),
        "H12" => Some(Timeframe::H12),
        "D1"  => Some(Timeframe::D1),
        "W1"  => Some(Timeframe::W1),
        _     => None,
    }
}

// ── Declaration parser ────────────────────────────────────────────────────────

/// Parse an indicator declaration line:
///   `let NAME = ind.TYPE(period [, tf_or_buf [, buf]]);`
pub(crate) fn try_parse_indicator_line(line: &str) -> Option<IndicatorDecl> {
    let line = line.trim().split("//").next()?.trim();
    if line.is_empty() { return None; }

    let rest     = line.strip_prefix("let ")?.trim();
    let eq_pos   = rest.find('=')?;
    let var_name = rest[..eq_pos].trim().to_string();
    if var_name.is_empty() { return None; }

    let rhs = rest[eq_pos + 1..].trim().trim_end_matches(';').trim();

    let after_dot  = rhs.strip_prefix("ind.")?;
    let paren      = after_dot.find('(')?;
    let type_str   = after_dot[..paren].trim().to_string();
    if type_str.is_empty() { return None; }
    let args_inner = after_dot[paren + 1..].trim_end_matches(')');

    // Parse args: period [, tf_or_buf [, buf]]
    let mut args = args_inner.splitn(3, ',');
    let period: usize = args.next()?.trim().parse().ok()?;

    let mut timeframe = None;
    let mut buf_depth = DEFAULT_BUF_DEPTH;
    let mut live      = false;

    if let Some(second) = args.next() {
        let s = second.trim().trim_matches('"').trim_matches('\'');
        if let Ok(n) = s.parse::<usize>() {
            buf_depth = n;
        } else {
            let (tf_str, is_live) = s.strip_prefix("live_")
                .map(|r| (r, true))
                .unwrap_or((s, false));
            live      = is_live;
            timeframe = parse_timeframe(tf_str);
            if let Some(third) = args.next() {
                buf_depth = third.trim().parse().unwrap_or(DEFAULT_BUF_DEPTH);
            }
        }
    }

    let (ind_type, kind) = map_indicator_type(&type_str);
    Some(IndicatorDecl { var_name, ind_type, period, buf_depth, kind, timeframe, live })
}

// ── Type → (canonical_type, IndicatorKind) ───────────────────────────────────

pub(super) fn map_indicator_type(type_str: &str) -> (String, IndicatorKind) {
    use IndicatorKind::{Multi, Single};
    match type_str {
        // ── Single-output: Array<f64> ────────────────────────────────────────
        "ema" | "sma" | "wma" | "hma" | "dema" | "tema" | "smma" | "kama" | "alma" |
        "mcginley" | "lsma" | "vwma" | "rsi" | "cci" | "roc" | "mfi" | "mom" | "cmo" |
        "dpo" | "rci" | "chop" | "williams" | "cmf" | "obv" | "vwap" | "ao" | "bop" |
        "coppock" | "uo" | "tsi" =>
            (type_str.to_string(), Single("value".to_string())),

        "atr" => ("atr".to_string(), Single("atr".to_string())),

        // ── Multi-output: Array<Map> ─────────────────────────────────────────
        // fields: .macd  .signal  .histogram
        "macd" => ("macd".to_string(), Multi),

        // fields: .adx  .plus_di  .minus_di
        "adx"  => ("adx".to_string(), Multi),
        // fields: .plus_di  .minus_di  .dx
        "dmi"  => ("dmi".to_string(), Multi),

        // fields: .upper  .middle  .lower  .bandwidth  .percent_b
        "bbands"   => ("bbands".to_string(),   Multi),
        // fields: .upper  .middle  .lower
        "keltner"  => ("keltner".to_string(),  Multi),
        "donchian" => ("donchian".to_string(), Multi),

        // fields: .k  .d
        "stochastic" => ("stochastic".to_string(), Multi),
        "stoch_rsi"  => ("stoch_rsi".to_string(),  Multi),

        // fields: .k  .d  .j
        "kdj" => ("kdj".to_string(), Multi),

        // fields: .value  .bullish
        "supertrend"   => ("supertrend".to_string(),   Multi),
        // fields: .sar  .bullish
        "parabolic_sar" => ("parabolic_sar".to_string(), Multi),

        // fields: .up  .down  .oscillator
        "aroon" => ("aroon".to_string(), Multi),
        // fields: .plus_vi  .minus_vi
        "vortex" => ("vortex".to_string(), Multi),

        // fields: .trix  .signal  .histogram
        "trix" => ("trix".to_string(), Multi),
        // fields: .ppo  .signal  .histogram
        "ppo"  => ("ppo".to_string(),  Multi),
        // fields: .kst  .signal  .histogram
        "kst"  => ("kst".to_string(),  Multi),
        // fields: .pmo  .signal  .histogram
        "pmo"  => ("pmo".to_string(),  Multi),
        // fields: .rvi  .signal
        "rvi"  => ("rvi".to_string(),  Multi),
        // fields: .smi  .signal
        "smi"  => ("smi".to_string(),  Multi),
        // fields: .fisher  .signal
        "fisher" => ("fisher".to_string(), Multi),

        // fields: .rwi_high  .rwi_low
        "rwi" => ("rwi".to_string(), Multi),

        // fields: .tenkan  .kijun  .senkou_a  .senkou_b  .chikou  .above_cloud
        "ichimoku" => ("ichimoku".to_string(), Multi),
        // fields: .jaw  .teeth  .lips  .bullish
        "alligator" => ("alligator".to_string(), Multi),
        // fields: .short_avg  .long_avg  .bullish
        "gmma" => ("gmma".to_string(), Multi),
        // fields: .value  .slope
        "kalman" => ("kalman".to_string(), Multi),

        // fields: .bull  .bear  .ema
        "bull_bear_power"  => ("bull_bear_power".to_string(),  Multi),
        // fields: .long_stop  .short_stop  .atr
        "chandelier_exit"  => ("chandelier_exit".to_string(),  Multi),
        // fields: .stop_long  .stop_short
        "chande_kroll_stop" => ("chande_kroll_stop".to_string(), Multi),
        // fields: .bullish  .bearish  .fractal_high  .fractal_low
        "william_fractal" => ("william_fractal".to_string(), Multi),
        // fields: .angle  .zone
        "chop_zone" => ("chop_zone".to_string(), Multi),

        other => (other.to_string(), Single("value".to_string())),
    }
}

// ── JSON config builder ───────────────────────────────────────────────────────

/// Build the JSON config for `IndicatorBox::from_config`.
pub(crate) fn indicator_json_config(ind_type: &str, period: usize) -> serde_json::Value {
    match ind_type {
        "macd"          => json!({"type": "macd",          "fast": period, "slow": 26, "signal": 9}),
        "bbands"        => json!({"type": "bbands",        "period": period, "multiplier": 2.0}),
        "stochastic"    => json!({"type": "stochastic",    "k_period": period, "d_period": 3}),
        "stoch_rsi"     => json!({"type": "stoch_rsi",     "rsi_period": period, "smooth_d": 3}),
        "supertrend"    => json!({"type": "supertrend",    "period": period, "multiplier": 3.0}),
        "parabolic_sar" => json!({"type": "parabolic_sar", "step": 0.02, "max": 0.2}),
        "kama"          => json!({"type": "kama",          "er_period": period}),
        "obv"           => json!({"type": "obv"}),
        "vwap"          => json!({"type": "vwap"}),
        "ao"            => json!({"type": "ao",            "fast": 5, "slow": 34}),
        "bop"           => json!({"type": "bop"}),
        "coppock"       => json!({"type": "coppock"}),
        "uo"            => json!({"type": "uo",            "fast": 7, "medium": 14, "slow": 28}),
        "kdj"           => json!({"type": "kdj", "period": period, "k_period": 3, "d_period": 3}),
        t               => json!({"type": t, "period": period}),
    }
}

pub(super) fn make_indicator_box(decl: &IndicatorDecl) -> Result<IndicatorBox> {
    IndicatorBox::from_config(&indicator_json_config(&decl.ind_type, decl.period))
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use alm_core::Timeframe;

    #[test]
    fn parse_single_output() {
        let d = try_parse_indicator_line(r#"let ema9 = ind.ema(9);"#).unwrap();
        assert_eq!(d.var_name, "ema9");
        assert_eq!(d.ind_type, "ema");
        assert_eq!(d.period, 9);
        assert_eq!(d.buf_depth, DEFAULT_BUF_DEPTH);
        assert!(matches!(d.kind, IndicatorKind::Single(_)));
    }

    #[test]
    fn parse_multi_output() {
        let d = try_parse_indicator_line(r#"let macd = ind.macd(12);"#).unwrap();
        assert_eq!(d.ind_type, "macd");
        assert!(matches!(d.kind, IndicatorKind::Multi));

        let d = try_parse_indicator_line(r#"let bb = ind.bbands(20);"#).unwrap();
        assert_eq!(d.ind_type, "bbands");
        assert!(matches!(d.kind, IndicatorKind::Multi));
    }

    #[test]
    fn parse_custom_buf() {
        let d = try_parse_indicator_line(r#"let atr = ind.atr(14, 5);"#).unwrap();
        assert_eq!(d.period, 14);
        assert_eq!(d.buf_depth, 5);
        assert!(matches!(d.kind, IndicatorKind::Single(_)));
    }

    #[test]
    fn parse_htf() {
        let d = try_parse_indicator_line(r#"let h1_ema = ind.ema(20, "H1");"#).unwrap();
        assert_eq!(d.timeframe, Some(Timeframe::H1));
        assert_eq!(d.buf_depth, DEFAULT_BUF_DEPTH);
    }

    #[test]
    fn parse_htf_custom_buf() {
        let d = try_parse_indicator_line(r#"let x = ind.rsi(5, "M5", 3);"#).unwrap();
        assert_eq!(d.timeframe, Some(Timeframe::M5));
        assert_eq!(d.buf_depth, 3);
    }

    #[test]
    fn non_indicator_line_returns_none() {
        assert!(try_parse_indicator_line("let x = 42;").is_none());
        assert!(try_parse_indicator_line("// comment").is_none());
    }
}

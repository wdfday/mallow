//! Indicator declaration parser for v2 scripts.
//!
//! Same surface as v1's `parse` module, but produces [`FieldExtract`] (v2's
//! unified binding type) instead of v1's `IndicatorKind`. Shared parsers for
//! candle directives and the `regime { }` block are re-exported directly from
//! v1 since they're TF-agnostic.

use anyhow::Result;
use alm_core::Timeframe;
use alm_indicator::IndicatorBox;
use serde_json::json;
use std::collections::HashMap;

use super::feed_binding::FieldExtract;

// Re-export directive/block parsers from v1 (they don't touch IndicatorKind).
pub(super) use crate::script::v1::{
    extract_candle_directives,
    extract_regime_block,
    CandleDirective,
    DEFAULT_BUF_DEPTH,
    extract_max_lookback,
};

// ── IndicatorDecl ─────────────────────────────────────────────────────────────

pub(super) struct IndicatorDecl {
    pub(super) var_name:     String,
    pub(super) ind_type:     String,
    pub(super) period:       usize,
    pub(super) extra_params: HashMap<String, f64>,
    pub(super) buf_depth:    usize,
    pub(super) extract:      FieldExtract,
    pub(super) timeframe:    Option<Timeframe>,
    /// `true` when the user wrote `"live_H1"` — forces live even if we would
    /// otherwise infer it. (In v2, all HTF bindings get live by default; this
    /// flag is kept for parity with the v1 syntax.)
    pub(super) live:         bool,
}

// ── Timeframe parser ──────────────────────────────────────────────────────────

pub(super) fn parse_timeframe_str(s: &str) -> Option<Timeframe> {
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
        "H12" => Some(Timeframe::H12),
        "D1"  => Some(Timeframe::D1),
        "W1"  => Some(Timeframe::W1),
        _     => None,
    }
}

// ── Declaration parser ────────────────────────────────────────────────────────

/// Parse `let NAME = ind.TYPE(period [, tf_or_buf [, buf]]);`.
pub(super) fn try_parse_indicator_line(line: &str) -> Option<IndicatorDecl> {
    let line = line.trim().split("//").next()?.trim();
    if line.is_empty() { return None; }

    let rest     = line.strip_prefix("let ")?.trim();
    let eq_pos   = rest.find('=')?;
    let var_name = rest[..eq_pos].trim().to_string();
    if var_name.is_empty() { return None; }

    let rhs        = rest[eq_pos + 1..].trim().trim_end_matches(';').trim();
    let after_dot  = rhs.strip_prefix("ind.")?;
    let paren      = after_dot.find('(')?;
    let type_str   = after_dot[..paren].trim().to_string();
    if type_str.is_empty() { return None; }
    let args_inner = after_dot[paren + 1..].trim_end_matches(')');

    let mut args = args_inner.split(',');
    let period: usize = args.next()?.trim().parse().ok()?;

    let mut extra_params = HashMap::new();
    let mut timeframe    = None;
    let mut buf_depth    = DEFAULT_BUF_DEPTH;
    let mut live         = false;

    for token in args {
        let token = token.trim();
        if let Some(eq) = token.find('=') {
            let name    = token[..eq].trim().to_string();
            let val_str = token[eq + 1..].trim();
            if let Ok(v) = val_str.parse::<f64>() {
                extra_params.insert(name, v);
            }
        } else {
            let s = token.trim_matches('"').trim_matches('\'');
            if let Ok(n) = s.parse::<usize>() {
                buf_depth = n;
            } else {
                let (tf_str, is_live) = s.strip_prefix("live_")
                    .map(|r| (r, true))
                    .unwrap_or((s, false));
                live      = is_live;
                timeframe = parse_timeframe_str(tf_str);
            }
        }
    }

    let (ind_type, extract) = map_indicator_type(&type_str);
    Some(IndicatorDecl { var_name, ind_type, period, extra_params, buf_depth, extract, timeframe, live })
}

// ── Type → (canonical_type, FieldExtract) ─────────────────────────────────────

fn map_indicator_type(type_str: &str) -> (String, FieldExtract) {
    use FieldExtract::{Multi, Single};
    match type_str {
        // ── Single-output: Array<f64> ────────────────────────────────────────
        "ema" | "sma" | "wma" | "hma" | "dema" | "tema" | "smma" | "kama" | "alma" |
        "mcginley" | "lsma" | "vwma" | "rsi" | "cci" | "roc" | "mfi" | "mom" | "cmo" |
        "dpo" | "rci" | "chop" | "williams" | "cmf" | "obv" | "vwap" | "ao" | "bop" |
        "coppock" | "uo" | "tsi" =>
            (type_str.to_string(), Single("value".to_string())),

        "atr" => ("atr".to_string(), Single("atr".to_string())),

        // ── Multi-output: Array<MEntry> ──────────────────────────────────────
        "macd" => ("macd".to_string(), Multi { primary: "macd".to_string() }),
        "adx"  => ("adx".to_string(),  Multi { primary: "adx".to_string() }),
        "dmi"  => ("dmi".to_string(),  Multi { primary: "plus_di".to_string() }),

        "bbands"   => ("bbands".to_string(),   Multi { primary: "middle".to_string() }),
        "keltner"  => ("keltner".to_string(),  Multi { primary: "middle".to_string() }),
        "donchian" => ("donchian".to_string(), Multi { primary: "middle".to_string() }),

        "stochastic" => ("stochastic".to_string(), Multi { primary: "k".to_string() }),
        "stoch_rsi"  => ("stoch_rsi".to_string(),  Multi { primary: "k".to_string() }),
        "kdj"        => ("kdj".to_string(),         Multi { primary: "k".to_string() }),

        "supertrend"    => ("supertrend".to_string(),    Multi { primary: "value".to_string() }),
        "parabolic_sar" => ("parabolic_sar".to_string(), Multi { primary: "sar".to_string() }),

        "aroon"  => ("aroon".to_string(),  Multi { primary: "oscillator".to_string() }),
        "vortex" => ("vortex".to_string(), Multi { primary: "plus_vi".to_string() }),

        "trix" => ("trix".to_string(), Multi { primary: "trix".to_string() }),
        "ppo"  => ("ppo".to_string(),  Multi { primary: "ppo".to_string() }),
        "kst"  => ("kst".to_string(),  Multi { primary: "kst".to_string() }),
        "pmo"  => ("pmo".to_string(),  Multi { primary: "pmo".to_string() }),
        "rvi"  => ("rvi".to_string(),  Multi { primary: "rvi".to_string() }),
        "smi"  => ("smi".to_string(),  Multi { primary: "smi".to_string() }),
        "fisher" => ("fisher".to_string(), Multi { primary: "fisher".to_string() }),
        "rwi"    => ("rwi".to_string(),    Multi { primary: "rwi_high".to_string() }),

        "ichimoku"  => ("ichimoku".to_string(),  Multi { primary: "tenkan".to_string() }),
        "alligator" => ("alligator".to_string(), Multi { primary: "teeth".to_string() }),
        "gmma"   => ("gmma".to_string(),   Multi { primary: "long_avg".to_string() }),
        "kalman" => ("kalman".to_string(), Multi { primary: "value".to_string() }),

        "bull_bear_power"   => ("bull_bear_power".to_string(),   Multi { primary: "bull".to_string() }),
        "chandelier_exit"   => ("chandelier_exit".to_string(),   Multi { primary: "long_stop".to_string() }),
        "chande_kroll_stop" => ("chande_kroll_stop".to_string(), Multi { primary: "stop_long".to_string() }),
        "william_fractal"   => ("william_fractal".to_string(),   Multi { primary: "bullish".to_string() }),
        "chop_zone" => ("chop_zone".to_string(), Multi { primary: "zone".to_string() }),

        other => (other.to_string(), Single("value".to_string())),
    }
}

// ── JSON config / factory ─────────────────────────────────────────────────────

pub(super) fn indicator_json_config(
    ind_type: &str,
    period: usize,
    extra: &HashMap<String, f64>,
) -> serde_json::Value {
    macro_rules! p {
        ($key:literal, $default:expr) => {
            extra.get($key).copied().unwrap_or($default)
        };
    }
    match ind_type {
        "macd" => json!({
            "type": "macd",
            "fast": period,
            "slow": p!("slow", 26.0) as u64,
            "signal": p!("signal", 9.0) as u64,
        }),
        "bbands" => json!({
            "type": "bbands",
            "period": period,
            "multiplier": p!("multiplier", 2.0),
        }),
        "stochastic" => json!({
            "type": "stochastic",
            "k_period": period,
            "d_period": p!("d_period", 3.0) as u64,
        }),
        "stoch_rsi" => json!({
            "type": "stoch_rsi",
            "rsi_period": period,
            "smooth_d": p!("smooth_d", 3.0) as u64,
        }),
        "supertrend" => json!({
            "type": "supertrend",
            "period": period,
            "multiplier": p!("multiplier", 3.0),
        }),
        "parabolic_sar" => json!({
            "type": "parabolic_sar",
            "step": p!("step", 0.02),
            "max":  p!("max", 0.2),
        }),
        "kdj" => json!({
            "type": "kdj",
            "period": period,
            "k_period": p!("k_period", 3.0) as u64,
            "d_period": p!("d_period", 3.0) as u64,
        }),
        "kama"    => json!({"type": "kama",    "er_period": period}),
        "obv"     => json!({"type": "obv"}),
        "vwap"    => json!({"type": "vwap"}),
        "ao"      => json!({"type": "ao",      "fast": 5, "slow": 34}),
        "bop"     => json!({"type": "bop"}),
        "coppock" => json!({"type": "coppock"}),
        "uo"      => json!({"type": "uo",      "fast": 7, "medium": 14, "slow": 28}),
        t         => json!({"type": t, "period": period}),
    }
}

pub(super) fn make_indicator_box(decl: &IndicatorDecl) -> Result<IndicatorBox> {
    IndicatorBox::from_config(&indicator_json_config(&decl.ind_type, decl.period, &decl.extra_params))
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use alm_core::Timeframe;

    #[test]
    fn parse_single_output() {
        let d = try_parse_indicator_line("let ema9 = ind.ema(9);").unwrap();
        assert_eq!(d.var_name, "ema9");
        assert_eq!(d.ind_type, "ema");
        assert_eq!(d.period, 9);
        assert_eq!(d.buf_depth, DEFAULT_BUF_DEPTH);
        assert!(matches!(d.extract, FieldExtract::Single(_)));
    }

    #[test]
    fn parse_htf() {
        let d = try_parse_indicator_line(r#"let h1_ema = ind.ema(20, "H1");"#).unwrap();
        assert_eq!(d.timeframe, Some(Timeframe::H1));
        assert_eq!(d.buf_depth, DEFAULT_BUF_DEPTH);
        assert!(!d.live);
    }

    #[test]
    fn parse_htf_live_prefix() {
        let d = try_parse_indicator_line(r#"let rsi = ind.rsi(5, "live_H1");"#).unwrap();
        assert_eq!(d.timeframe, Some(Timeframe::H1));
        assert!(d.live);
    }

    #[test]
    fn parse_htf_custom_buf() {
        let d = try_parse_indicator_line(r#"let x = ind.rsi(5, "M5", 3);"#).unwrap();
        assert_eq!(d.timeframe, Some(Timeframe::M5));
        assert_eq!(d.buf_depth, 3);
    }

    #[test]
    fn parse_multi_output() {
        let d = try_parse_indicator_line("let macd = ind.macd(12);").unwrap();
        assert!(matches!(d.extract, FieldExtract::Multi { .. }));
        let d = try_parse_indicator_line("let bb = ind.bbands(20);").unwrap();
        assert!(matches!(d.extract, FieldExtract::Multi { .. }));
    }

    #[test]
    fn non_indicator_line_returns_none() {
        assert!(try_parse_indicator_line("let x = 42;").is_none());
        assert!(try_parse_indicator_line("// comment").is_none());
    }
}

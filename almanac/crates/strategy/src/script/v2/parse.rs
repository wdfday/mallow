//! Indicator declaration parser for v2 scripts.
//!
//! Same surface as v1's `parse` module, but produces [`FieldExtract`] (v2's
//! unified binding type) instead of v1's `IndicatorKind`. Shared parsers for
//! candle directives and the `regime { }` block are re-exported directly from
//! v1 since they're TF-agnostic.

use anyhow::Result;
use alm_core::Timeframe;
use alm_indicator::IndicatorBox;
use std::collections::HashMap;

use super::feed_binding::FieldExtract;

// Re-export directive/block parsers from v1 (they don't touch IndicatorKind).
pub(super) use crate::script::v1::{
    extract_candle_directives,
    extract_regime_block,
    CandleDirective,
    DEFAULT_BUF_DEPTH,
    extract_max_lookback,
    PERIOD_EXEMPT,
};

// Shared parser internals reused from v1 (single source of truth). v2 only
// differs in producing `FieldExtract` instead of `IndicatorKind`; everything
// else — canonical type names, positional param ordering, JSON config — is
// identical and must not drift.
use crate::script::v1::{
    IndicatorKind,
    map_indicator_type as v1_map_indicator_type,
    positional_param_names,
    indicator_json_config,
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

/// Parse `let NAME = ind.TYPE(period [, name=value]* [, "TF"] [, buf=N]);`.
///
/// Positional integers after the period are mapped to per-type secondary
/// parameter names. Buffer depth must be set explicitly as `buf=N`.
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

    let mut extra_params   = HashMap::new();
    let mut timeframe      = None;
    let mut buf_depth      = DEFAULT_BUF_DEPTH;
    let mut live           = false;
    let mut positional_idx = 0usize;

    for token in args {
        let token = token.trim();
        if let Some(eq) = token.find('=') {
            let name    = token[..eq].trim();
            let val_str = token[eq + 1..].trim();
            if name == "buf" {
                if let Ok(n) = val_str.parse::<usize>() {
                    buf_depth = n;
                }
            } else if let Ok(v) = val_str.parse::<f64>() {
                extra_params.insert(name.to_string(), v);
            }
        } else {
            let s = token.trim_matches('"').trim_matches('\'');
            if let Ok(n) = s.parse::<f64>() {
                let param_names = positional_param_names(&type_str);
                if let Some(param_name) = param_names.get(positional_idx) {
                    extra_params.insert(param_name.to_string(), n);
                }
                positional_idx += 1;
            } else if !s.is_empty() {
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

/// Wraps v1's canonical `map_indicator_type`, converting its `IndicatorKind`
/// into v2's structurally-isomorphic `FieldExtract`. This guarantees the two
/// script layers can never drift on canonical names or primary fields.
fn map_indicator_type(type_str: &str) -> (String, FieldExtract) {
    let (canonical, kind) = v1_map_indicator_type(type_str);
    let extract = match kind {
        IndicatorKind::Single(field) => FieldExtract::Single(field),
        IndicatorKind::Multi(primary) => FieldExtract::Multi { primary },
    };
    (canonical, extract)
}

// ── JSON config / factory ─────────────────────────────────────────────────────

pub(super) fn make_indicator_box(decl: &IndicatorDecl) -> Result<IndicatorBox> {
    // Mirror v1's validation — same rules apply regardless of which engine parses the script.
    if decl.period == 0 && !PERIOD_EXEMPT.contains(&decl.ind_type.as_str()) {
        anyhow::bail!(
            "indicator '{}' (type '{}'): period must be ≥ 1, got 0",
            decl.var_name, decl.ind_type
        );
    }
    for (key, &val) in &decl.extra_params {
        if val < 0.0 {
            anyhow::bail!(
                "indicator '{}': parameter '{}' must be ≥ 0, got {}",
                decl.var_name, key, val
            );
        }
    }
    IndicatorBox::from_config(&indicator_json_config(&decl.ind_type, decl.period, &decl.extra_params))
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use crate::test_utils::*;
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
        let d = try_parse_indicator_line(r#"let x = ind.rsi(5, "M5", buf=3);"#).unwrap();
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

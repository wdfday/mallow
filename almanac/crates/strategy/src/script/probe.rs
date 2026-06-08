//! Probe utilities for script strategies.
//!
//! Exposes parse-only helpers so callers (HTTP handlers, herald registry,
//! engine backtest router) can decide between V1 and V2 paths **without**
//! constructing a full `MtfScriptStrategy`. Saves an `IndicatorBox` allocation
//! per indicator and avoids the Rhai AST compile.

use std::collections::HashSet;

use alm_core::Timeframe;

use crate::script::v1::try_parse_indicator_line;

/// Return the unique set of higher-timeframe indicators declared by `script`.
///
/// `vec![]` means the script is base-TF only — caller can use V1 / `Engine`.
/// Non-empty means the script needs V2 / `MtfEngine` and the caller must
/// register a feed for every returned timeframe.
///
/// The probe ignores syntax errors and regime/candle directives — it walks
/// every line, runs `try_parse_indicator_line`, and collects the declared
/// timeframes. Lines that don't look like indicator declarations are skipped
/// silently; full validation happens later when the strategy is actually
/// built.
pub fn probe_script_htfs(script: &str) -> Vec<Timeframe> {
    let mut seen = HashSet::new();
    let mut out = Vec::new();
    for line in script.lines() {
        if let Some(decl) = try_parse_indicator_line(line) {
            if let Some(tf) = decl.timeframe {
                if seen.insert(tf) {
                    out.push(tf);
                }
            }
        }
    }
    out
}

#[cfg(test)]
mod tests {
    use crate::test_utils::*;
    use super::*;

    #[test]
    fn empty_for_base_only_script() {
        let s = r#"
let ema9  = ind.ema(9);
let rsi14 = ind.rsi(14);
if cross_above(ema9, ema9) { entry = true; }
"#;
        assert!(probe_script_htfs(s).is_empty());
    }

    #[test]
    fn returns_single_htf() {
        let s = r#"let h1_ema = ind.ema(20, "H1");"#;
        assert_eq!(probe_script_htfs(s), vec![Timeframe::H1]);
    }

    #[test]
    fn dedups_repeated_htf() {
        let s = r#"
let h1_ema = ind.ema(20, "H1");
let h1_rsi = ind.rsi(14, "H1");
"#;
        assert_eq!(probe_script_htfs(s), vec![Timeframe::H1]);
    }

    #[test]
    fn returns_multiple_htfs() {
        let s = r#"
let h1_ema = ind.ema(20, "H1");
let h4_atr = ind.atr(14, "H4");
let m1_rsi = ind.rsi(5);
"#;
        let htfs = probe_script_htfs(s);
        assert!(htfs.contains(&Timeframe::H1));
        assert!(htfs.contains(&Timeframe::H4));
        assert_eq!(htfs.len(), 2);
    }

    #[test]
    fn treats_live_prefix_as_htf() {
        let s = r#"let h1_rsi = ind.rsi(5, "live_H1");"#;
        assert_eq!(probe_script_htfs(s), vec![Timeframe::H1]);
    }
}

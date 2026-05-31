//! Shared linter for strategy scripts — covers both single-TF (v1) and
//! multi-timeframe (v2) syntax.
//!
//! The script surface is identical in both versions: indicators are declared
//! with `ind.TYPE(period)` or `ind.TYPE(period, "TF")`. This module checks
//! prefix/type validity, HTF-vs-base-TF ordering, and Rhai syntax — once,
//! for both engines.

use alm_core::Timeframe;

use crate::script::v1::{
    build_engine, BAR_FIELDS,
    extract_candle_directives, extract_regime_block,
    try_parse_indicator_line, IndicatorKind,
};

// ── Known types ───────────────────────────────────────────────────────────────

/// All user-facing indicator types accepted by `ind.TYPE(period)`.
///
/// Single-output types produce `Array<f64>`; multi-output types produce `Array<Map>`
/// where each element has named fields (e.g. `macd[0].histogram`).
pub const KNOWN_INDICATOR_TYPES: &[&str] = &[
    // ── Single-output: Array<f64> ────────────────────────────────────────────
    "ema", "sma", "wma", "hma", "dema", "tema", "smma", "kama", "alma",
    "mcginley", "vwma", "rsi", "cci", "roc", "mfi", "mom", "cmo",
    "dpo", "rci", "chop", "williams_r", "cmf", "obv", "vwap", "ao", "bop",
    "coppock", "uo", "tsi", "connors_rsi", "volatility_ratio",
    "aroon_osc",   // scalar aroon oscillator (−100…+100)
    // ── Multi-output: Array<Map> ─────────────────────────────────────────────
    "atr",         // .atr (default)  .tr
    "lsma",        // .value (default)  .slope
    "macd",        // .macd  .signal  .histogram
    "adx",         // .adx (default, strength 0–100)  .plus_di  .minus_di
    "dmi",         // .plus_di  .minus_di
    "bbands",      // .upper  .middle  .lower  .bandwidth  .percent_b
    "keltner",     // .upper  .middle  .lower
    "donchian",    // .upper  .middle  .lower
    "stochastic",  // .k  .d
    "stoch_rsi",   // .k  .d
    "kdj",         // .k  .d  .j
    "supertrend",  // .value  .bullish
    "parabolic_sar", // .sar  .bullish
    "aroon",       // .up  .down
    "vortex",      // .plus_vi  .minus_vi
    "trix",        // .trix  .signal  .histogram
    "ppo",         // .ppo  .signal  .histogram
    "kst",         // .kst  .signal  .histogram
    "pmo",         // .pmo  .signal  .histogram
    "rvi",         // .rvi  .signal
    "smi",         // .smi  .signal
    "fisher",      // .fisher  .signal
    "rwi",         // .rwi_high  .rwi_low
    "elder_ray",   // .bull_power  .bear_power  .ema
    "ichimoku",    // .tenkan  .kijun  .senkou_a  .senkou_b  .chikou  .above_cloud  .below_cloud  .chikou_above  .chikou_below
    "alligator",   // .jaw  .teeth  .lips  .bullish  .bearish
    "gmma",        // .spread (default)  .bullish  .short_0..short_5  .long_0..long_5
    "kalman",      // .value  .velocity
    "bull_bear",         // .bull  .bear  .ema
    "chandelier_exit",   // .long_stop  .short_stop  .atr
    "chande_kroll",      // .stop_long  .stop_short
    "fractal",           // .bullish  .bearish  .fractal_high  .fractal_low
    "chop_zone",         // .angle  .zone
];

// ── Public lint types ─────────────────────────────────────────────────────────

/// A lint diagnostic with Monaco-compatible line/col coordinates (1-based).
#[derive(Debug, Clone, serde::Serialize)]
#[cfg_attr(feature = "openapi", derive(utoipa::ToSchema))]
pub struct LintDiagnostic {
    /// 1-based line number (0 = unknown / whole-script).
    pub line: usize,
    /// 1-based column number (0 = unknown).
    pub col:  usize,
    pub message:  String,
    /// `"error"` or `"warning"`.
    pub severity: &'static str,
}

/// Per-line declared indicator summary returned in the lint scope.
#[derive(Debug, Clone, serde::Serialize)]
#[cfg_attr(feature = "openapi", derive(utoipa::ToSchema))]
pub struct DeclaredIndicator {
    pub name:     String,
    pub ind_type: String,
    pub period:   usize,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub timeframe: Option<String>,
    #[serde(skip_serializing_if = "std::ops::Not::not")]
    pub live:  bool,
    /// `true` → this indicator returns `Array<Map>` (multi-field access).
    #[serde(skip_serializing_if = "std::ops::Not::not")]
    pub multi: bool,
}

/// Scope information for Monaco autocomplete / hover.
#[derive(Debug, Clone, Default, serde::Serialize)]
#[cfg_attr(feature = "openapi", derive(utoipa::ToSchema))]
pub struct ScriptLintScope {
    pub indicators:  Vec<DeclaredIndicator>,
    pub bar_fields:  Vec<&'static str>,
    pub output_vars: Vec<&'static str>,
    pub functions:   Vec<&'static str>,
}

// ── Lint implementation ───────────────────────────────────────────────────────

/// Lint a strategy script. Returns `(diagnostics, scope)`.
///
/// Checks:
/// 1. Wrong indicator prefix (e.g. `indi.ema` instead of `ind.ema`).
/// 2. Unknown indicator type against [`KNOWN_INDICATOR_TYPES`].
/// 3. Script syntax errors in the logic section (indicator decls stripped first).
/// 4. HTF declared smaller than `base_tf` — error (e.g. `ind.ema(9, "M1")` in an H1 strategy).
/// 5. HTF equal to `base_tf` — warning (no benefit, same as base-TF indicator).
///
/// Pass `base_tf = None` to skip checks 4–5 (e.g. when base TF is unknown at lint time).
pub fn script_lint(script: &str, base_tf: Option<Timeframe>) -> (Vec<LintDiagnostic>, ScriptLintScope) {
    let mut diags: Vec<LintDiagnostic>          = Vec::new();
    let mut cleaned_lines: Vec<&str>            = Vec::new();
    let mut line_map: Vec<usize>                = Vec::new();
    let mut scope_inds: Vec<DeclaredIndicator>  = Vec::new();

    // Strip setup directives + regime block first so the Rhai parser sees
    // clean logic (candle.transform / regime {} are pre-parse constructs).
    let after_candle = match extract_candle_directives(script) {
        Ok((_dirs, cleaned)) => cleaned,
        Err(e) => {
            diags.push(LintDiagnostic {
                line: 1, col: 1, message: e.to_string(), severity: "error",
            });
            return (diags, ScriptLintScope::default());
        }
    };
    let (regime_body_opt, main_source) = match extract_regime_block(&after_candle) {
        Ok(parts) => parts,
        Err(e) => {
            diags.push(LintDiagnostic {
                line: 1, col: 1, message: e.to_string(), severity: "error",
            });
            return (diags, ScriptLintScope::default());
        }
    };

    // Collect indicator decls from inside the regime block.
    if let Some(body) = regime_body_opt.as_deref() {
        for (idx, line) in body.lines().enumerate() {
            let lineno = idx + 1;
            if let Some(decl) = try_parse_indicator_line(line.trim()) {
                if let Some(d) = check_htf_vs_base(&decl.var_name, decl.timeframe, base_tf, lineno) {
                    diags.push(d);
                }
                let multi = matches!(decl.kind, IndicatorKind::Multi(_));
                scope_inds.push(DeclaredIndicator {
                    name:      decl.var_name,
                    ind_type:  decl.ind_type,
                    period:    decl.period,
                    timeframe: decl.timeframe.map(|tf| tf.to_string()),
                    live:      decl.live,
                    multi,
                });
            }
        }
    }

    for (idx, line) in main_source.lines().enumerate() {
        let lineno  = idx + 1;
        let trimmed = line.trim();

        if let Some((prefix, raw_type)) = extract_raw_indicator_type(trimmed) {
            if prefix != "ind" {
                diags.push(LintDiagnostic {
                    line: lineno, col: 1,
                    message: format!(
                        "Wrong indicator prefix '{prefix}.'; use 'ind.{raw_type}(...)' instead"
                    ),
                    severity: "error",
                });
                cleaned_lines.push(line);
                line_map.push(lineno);
            } else {
                if !KNOWN_INDICATOR_TYPES.contains(&raw_type.as_str()) {
                    let suggestion = suggest_similar(&raw_type);
                    diags.push(LintDiagnostic {
                        line: lineno, col: 1,
                        message: if suggestion.is_empty() {
                            format!("Unknown indicator type '{raw_type}'; see KNOWN_INDICATOR_TYPES for valid types")
                        } else {
                            format!("Unknown indicator type '{raw_type}'; did you mean '{suggestion}'?")
                        },
                        severity: "error",
                    });
                }
                if let Some(decl) = try_parse_indicator_line(trimmed) {
                    if let Some(d) = check_htf_vs_base(&decl.var_name, decl.timeframe, base_tf, lineno) {
                        diags.push(d);
                    }
                    let multi = matches!(decl.kind, IndicatorKind::Multi(_));
                    scope_inds.push(DeclaredIndicator {
                        name:      decl.var_name,
                        ind_type:  raw_type,
                        period:    decl.period,
                        timeframe: decl.timeframe.map(|tf| tf.to_string()),
                        live:      decl.live,
                        multi,
                    });
                } else {
                    cleaned_lines.push(line);
                    line_map.push(lineno);
                }
            }
        } else {
            cleaned_lines.push(line);
            line_map.push(lineno);
        }
    }

    // ── Semantic check: field access on scalar indicators ────────────────────
    //
    // Rhai is dynamically typed — `adx14[0].adx` is syntactically valid AST but
    // fails at runtime because `adx14[0]` is `f64` (no `.adx` getter). Detect
    // this early by scanning every non-declaration line for `var[...].field`
    // where `var` is a known single-output indicator.
    {
        let scalar_names: std::collections::HashSet<&str> = scope_inds.iter()
            .filter(|d| !d.multi)
            .map(|d| d.name.as_str())
            .collect();

        // Check main-block non-declaration lines.
        for (i, &line) in cleaned_lines.iter().enumerate() {
            let orig = line_map[i];
            for diag in field_access_on_scalar(line, orig, &scalar_names) {
                diags.push(diag);
            }
        }

        // Check regime body lines (line numbers are body-relative; add an offset
        // to approximate the original script position — close enough for UX).
        if let Some(body) = regime_body_opt.as_deref() {
            // Count how many lines come before the regime body in main_source.
            let regime_start_line = main_source
                .lines()
                .take_while(|l| l.trim().is_empty() || !body.starts_with(l.trim()))
                .count();
            for (idx, line) in body.lines().enumerate() {
                let trimmed = line.trim();
                if trimmed.is_empty() || try_parse_indicator_line(trimmed).is_some() {
                    continue;
                }
                let approx_line = regime_start_line + idx + 1;
                for diag in field_access_on_scalar(line, approx_line, &scalar_names) {
                    diags.push(diag);
                }
            }
        }
    }

    let cleaned = cleaned_lines.join("\n");
    let engine = build_engine();
    if let Err(e) = engine.compile(&cleaned) {
        let pos = e.1;
        let orig_line = pos
            .line()
            .and_then(|l| line_map.get(l.saturating_sub(1)).copied())
            .unwrap_or_else(|| pos.line().unwrap_or(0));
        let col = pos.position().unwrap_or(1);
        diags.push(LintDiagnostic {
            line: orig_line,
            col,
            message: e.0.to_string(),
            severity: "error",
        });
    }

    let scope = ScriptLintScope {
        indicators:  scope_inds,
        bar_fields:  BAR_FIELDS.to_vec(),
        output_vars: vec![
            // signal outputs
            "long", "entry", "short", "exit",
            "tp", "sl", "is_offset", "strength", "reason", "atr",
            // regime outputs (writable in regime block, readable everywhere)
            "trend", "trend_value",
            "vol",   "vol_value",
        ],
        functions:   vec![
            // crossover
            "cross_above", "crossover",
            "cross_below", "crossunder", "crossed",
            // direction
            "rising", "falling", "rising_n", "falling_n",
            "above", "below", "in_range", "within",
            // bool-flag coercion (f64 0/1 → bool)
            "flag",
            // movement
            "slope", "momentum",
            // lookback
            "highest", "lowest",
            // aggregation / statistics
            "avg", "sum", "stdev", "pct_change", "zscore",
            // scalar math
            "abs", "sqrt", "pow", "sign",
            "round", "floor", "ceil",
            "min", "max", "clamp",
            // debug
            "log",
        ],
    };

    (diags, scope)
}

// ── Internal helpers ──────────────────────────────────────────────────────────

/// Scan a single source line for `name[...].field` patterns where `name` is a
/// known scalar (single-output) indicator. Returns one diagnostic per hit.
fn field_access_on_scalar(
    line: &str,
    lineno: usize,
    scalar_names: &std::collections::HashSet<&str>,
) -> Vec<LintDiagnostic> {
    let mut diags = Vec::new();
    for name in scalar_names {
        let needle = format!("{name}[");
        let mut search = line;
        let mut col_offset = 0usize;
        while let Some(start) = search.find(&needle) {
            let after_open = &search[start + needle.len()..];
            // Find the matching ']' (ignore nested brackets — scripts don't nest them).
            if let Some(close) = after_open.find(']') {
                let after_close = &after_open[close + 1..];
                let trimmed_after = after_close.trim_start();
                if trimmed_after.starts_with('.') {
                    // Extract the accessed field name for the message.
                    let field: String = trimmed_after[1..]
                        .chars()
                        .take_while(|c| c.is_alphanumeric() || *c == '_')
                        .collect();
                    diags.push(LintDiagnostic {
                        line: lineno,
                        col: col_offset + start + 1,
                        message: format!(
                            "'{name}' is a scalar indicator (Array<f64>); \
                             field access '.{field}' is invalid — use '{name}[0]' directly. \
                             For multi-field access use a multi-output indicator."
                        ),
                        severity: "error",
                    });
                }
            }
            // Advance past this occurrence to find further hits on the same line.
            col_offset += start + needle.len();
            search = &search[start + needle.len()..];
        }
    }
    diags
}

fn check_htf_vs_base(
    var_name: &str,
    htf: Option<Timeframe>,
    base_tf: Option<Timeframe>,
    lineno: usize,
) -> Option<LintDiagnostic> {
    let (htf, base) = (htf?, base_tf?);
    let htf_ms  = htf.duration_ms();
    let base_ms = base.duration_ms();
    if htf_ms < base_ms {
        Some(LintDiagnostic {
            line: lineno, col: 1,
            message: format!(
                "'{var_name}': HTF timeframe '{htf}' ({htf_ms} ms) is smaller than the \
                 strategy base TF '{base}' ({base_ms} ms). \
                 Declare an indicator on a timeframe ≥ the base TF."
            ),
            severity: "error",
        })
    } else if htf_ms == base_ms {
        Some(LintDiagnostic {
            line: lineno, col: 1,
            message: format!(
                "'{var_name}': timeframe '{htf}' matches the base TF — \
                 omit the TF argument to use a plain base-TF indicator instead."
            ),
            severity: "warning",
        })
    } else {
        None
    }
}

fn extract_raw_indicator_type(trimmed: &str) -> Option<(String, String)> {
    let rest = trimmed.strip_prefix("let ")?.trim();
    let eq_pos = rest.find('=')?;
    let rhs = rest[eq_pos + 1..].trim();

    let dot_pos   = rhs.find('.')?;
    let prefix    = rhs[..dot_pos].trim().to_string();
    let after_dot = rhs[dot_pos + 1..].trim();
    let paren_pos = after_dot.find('(')?;
    let type_str  = after_dot[..paren_pos].trim().to_string();

    if prefix.is_empty() || type_str.is_empty() { return None; }
    // Only an indicator declaration if both sides are bare identifiers, e.g.
    // `let x = ind.ema(9)`. Reject expressions like `let f = !flag(st[1].bullish)`
    // or `let v = ema9[0].value` whose `prefix`/`type` carry `!`, `(`, `[`, etc.
    let is_ident = |s: &str| !s.is_empty()
        && s.chars().all(|c| c.is_ascii_alphanumeric() || c == '_');
    if !is_ident(&prefix) || !is_ident(&type_str) { return None; }
    Some((prefix, type_str))
}

fn suggest_similar(input: &str) -> String {
    let mut best: Option<(&str, usize)> = None;
    for &known in KNOWN_INDICATOR_TYPES {
        let dist = edit_distance(input, known);
        if dist <= 2 && best.map_or(true, |(_, d)| dist < d) {
            best = Some((known, dist));
        }
    }
    best.map(|(s, _)| s.to_string()).unwrap_or_default()
}

fn edit_distance(a: &str, b: &str) -> usize {
    let a: Vec<char> = a.chars().collect();
    let b: Vec<char> = b.chars().collect();
    let m = a.len(); let n = b.len();
    if m.abs_diff(n) > 3 { return 4; }
    let mut dp = vec![vec![0usize; n + 1]; m + 1];
    for i in 0..=m { dp[i][0] = i; }
    for j in 0..=n { dp[0][j] = j; }
    for i in 1..=m {
        for j in 1..=n {
            dp[i][j] = if a[i-1] == b[j-1] { dp[i-1][j-1] }
                       else { 1 + dp[i-1][j-1].min(dp[i-1][j]).min(dp[i][j-1]) };
        }
    }
    dp[m][n]
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;

    /// `let x = !flag(st[1].bullish) && flag(st[0].bullish)` is a valid expression,
    /// NOT an indicator declaration — must not be flagged as "wrong indicator prefix".
    #[test]
    fn lint_flag_expr_not_indicator_decl() {
        let script = r#"
let st = ind.supertrend(10, multiplier=3.0, buf=3);
let flip_up   = !flag(st[1].bullish) && flag(st[0].bullish);
let flip_down =  flag(st[1].bullish) && !flag(st[0].bullish);
if flip_up { entry = true; }
"#;
        let (errors, _scope) = script_lint(script, None);
        assert!(errors.is_empty(), "flag() expr falsely flagged: {errors:?}");
    }

    #[test]
    fn lint_single_output_clean() {
        let script = r#"
let ema9  = ind.ema(9);
let rsi14 = ind.rsi(14);
if cross_above(ema9, ema9) && rsi14[0] < 70.0 { entry = true; }
if cross_below(ema9, ema9) { exit = true; }
"#;
        let (errors, scope) = script_lint(script, None);
        assert!(errors.is_empty(), "{errors:?}");
        assert_eq!(scope.indicators.len(), 2);
        assert!(!scope.indicators[0].multi);
        assert!(!scope.indicators[1].multi);
    }

    #[test]
    fn lint_multi_output_flagged_correctly() {
        let script = r#"
let macd  = ind.macd(12);
let bb    = ind.bbands(20);
if macd[0].histogram > 0.0 && bb[0].upper > 0.0 { entry = true; }
"#;
        let (errors, scope) = script_lint(script, None);
        assert!(errors.is_empty(), "{errors:?}");
        assert_eq!(scope.indicators.len(), 2);
        assert!(scope.indicators[0].multi);
        assert!(scope.indicators[1].multi);
    }

    #[test]
    fn lint_wrong_prefix() {
        let (errors, _) = script_lint("let ema9 = indi.ema(9);\nif ema9[0] > 0.0 { entry = true; }", None);
        assert!(!errors.is_empty());
        assert!(errors[0].message.contains("indi"));
        assert_eq!(errors[0].line, 1);
    }

    #[test]
    fn lint_unknown_type_suggestion() {
        let (errors, _) = script_lint("let x = ind.mma(9);", None);
        assert!(!errors.is_empty());
        assert!(errors[0].message.contains("mma") && errors[0].message.contains("ema"));
    }

    #[test]
    fn lint_syntax_error_mapped() {
        let (errors, _) = script_lint("let ema9 = ind.ema(9);\nif ema9[0] > { entry = true; }", None);
        assert!(errors.iter().any(|e| e.severity == "error"));
    }

    #[test]
    fn lint_scope_bar_fields() {
        let (_, scope) = script_lint("if close[0] > 0.0 { entry = true; }", None);
        assert!(scope.bar_fields.contains(&"close") && scope.bar_fields.contains(&"open"));
    }

    #[test]
    fn lint_regime_block_does_not_trip_parser() {
        // adx[0] (bare) uses the primary field (ADX strength) — no field access needed.
        let script = r#"
regime {
    let adx14 = ind.adx(14);
    if adx14[0] > 25.0 { trend = "trending"; }
    else               { trend = "ranging";  }
}

let ema9 = ind.ema(9);
if cross_above(ema9, ema9) && trend == "trending" { entry = true; }
"#;
        let (errors, scope) = script_lint(script, None);
        assert!(errors.is_empty(), "unexpected errors: {errors:?}");
        let names: Vec<&str> = scope.indicators.iter().map(|i| i.name.as_str()).collect();
        assert!(names.contains(&"adx14"), "adx14 from regime block missing: {names:?}");
        assert!(names.contains(&"ema9"),  "ema9 from main missing: {names:?}");
    }

    #[test]
    fn lint_candle_directive_does_not_trip_parser() {
        let script = r#"candle.transform("heiken_ashi");
let ema9 = ind.ema(9);
if cross_above(ema9, ema9) { entry = true; }
"#;
        let (errors, _) = script_lint(script, None);
        assert!(errors.is_empty(), "false-positive lint on candle directive: {errors:?}");
    }

    #[test]
    fn lint_misplaced_candle_directive_surfaces_error() {
        let script = r#"let ema9 = ind.ema(9);
candle.transform("heiken_ashi");
"#;
        let (errors, _) = script_lint(script, None);
        assert!(!errors.is_empty(), "misplaced directive should produce a diagnostic");
        assert!(errors[0].message.contains("must appear at the top"));
    }

    #[test]
    fn lint_scalar_field_access_is_error() {
        let script = r#"
let cci20 = ind.cci(20);
let rsi14 = ind.rsi(14);
if cci20[0].value > 25.0 { entry = true; }
if rsi14[0] > 60.0       { exit  = true; }
"#;
        let (errors, _) = script_lint(script, None);
        assert!(
            errors.iter().any(|e| e.severity == "error" && e.message.contains("cci20")),
            "expected error for cci20[0].value on scalar indicator, got: {errors:?}"
        );
        assert!(
            !errors.iter().any(|e| e.message.contains("rsi14")),
            "rsi14[0] (no field access) should not error, got: {errors:?}"
        );
    }

    #[test]
    fn lint_scalar_field_access_in_regime_block_is_error() {
        let script = r#"
regime {
    let cci20 = ind.cci(20);
    if cci20[0].value > 25.0 { trend = "trending"; }
}
let ema9 = ind.ema(9);
if ema9[0] > 0.0 { entry = true; }
"#;
        let (errors, _) = script_lint(script, None);
        assert!(
            errors.iter().any(|e| e.severity == "error" && e.message.contains("cci20")),
            "expected error for cci20[0].value in regime block, got: {errors:?}"
        );
    }

    #[test]
    fn lint_multi_field_access_is_clean() {
        let script = r#"
let macd  = ind.macd(12);
let dmi14 = ind.dmi(14);
if macd[0].histogram > 0.0 && dmi14[0].plus_di > dmi14[0].minus_di { entry = true; }
"#;
        let (errors, _) = script_lint(script, None);
        let field_errs: Vec<_> = errors.iter().filter(|e| e.message.contains("field access")).collect();
        assert!(field_errs.is_empty(), "multi-output field access should not error: {field_errs:?}");
    }

    #[test]
    fn lint_htf_smaller_than_base_tf_is_error() {
        let script = r#"let ema9 = ind.ema(9, "M1");
if ema9[0] > 0.0 { entry = true; }
"#;
        let (errors, _) = script_lint(script, Some(Timeframe::H1));
        assert!(
            errors.iter().any(|e| e.severity == "error" && e.message.contains("M1")),
            "expected error for HTF M1 < base H1, got: {errors:?}"
        );
    }

    #[test]
    fn lint_htf_equal_to_base_tf_is_warning() {
        let script = r#"let ema9 = ind.ema(9, "H1");
if ema9[0] > 0.0 { entry = true; }
"#;
        let (errors, _) = script_lint(script, Some(Timeframe::H1));
        assert!(
            errors.iter().any(|e| e.severity == "warning"),
            "expected warning for HTF H1 == base H1, got: {errors:?}"
        );
    }

    #[test]
    fn lint_htf_larger_than_base_tf_is_clean() {
        let script = r#"let ema9 = ind.ema(9, "H4");
if ema9[0] > 0.0 { entry = true; }
"#;
        let (errors, _) = script_lint(script, Some(Timeframe::M1));
        let htf_errors: Vec<_> = errors.iter()
            .filter(|e| e.message.contains("H4") || e.message.contains("timeframe"))
            .collect();
        assert!(htf_errors.is_empty(), "unexpected htf errors: {htf_errors:?}");
    }

    #[test]
    fn lint_htf_check_in_regime_block() {
        let script = r#"
regime {
    let adx = ind.adx(14, "M1");
    if adx[0] > 25.0 { trend = "trending"; }
}
let ema9 = ind.ema(9);
if ema9[0] > 0.0 { entry = true; }
"#;
        let (errors, _) = script_lint(script, Some(Timeframe::H1));
        assert!(
            errors.iter().any(|e| e.severity == "error" && e.message.contains("M1")),
            "expected error for HTF M1 < base H1 in regime block, got: {errors:?}"
        );
    }
}

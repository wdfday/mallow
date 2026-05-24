use super::engine::build_engine;
use super::parse::{
    extract_candle_directives, extract_regime_block, try_parse_indicator_line, IndicatorKind,
};

// ── Known types ───────────────────────────────────────────────────────────────

/// All user-facing indicator types accepted by `ind.TYPE(period)`.
///
/// Single-output types produce `Array<f64>`; multi-output types produce `Array<Map>`
/// where each element has named fields (e.g. `macd[0].histogram`).
pub const KNOWN_INDICATOR_TYPES: &[&str] = &[
    // ── Single-output: Array<f64> ────────────────────────────────────────────
    "ema", "sma", "wma", "hma", "dema", "tema", "smma", "kama", "alma",
    "mcginley", "lsma", "vwma", "rsi", "cci", "roc", "mfi", "mom", "cmo",
    "dpo", "rci", "chop", "williams", "cmf", "obv", "vwap", "ao", "bop",
    "coppock", "uo", "tsi",
    "atr",
    // ── Multi-output: Array<Map> ─────────────────────────────────────────────
    "macd",        // .macd  .signal  .histogram
    "adx",         // .adx  .plus_di  .minus_di
    "dmi",         // .plus_di  .minus_di
    "bbands",      // .upper  .middle  .lower  .bandwidth  .percent_b
    "keltner",     // .upper  .middle  .lower
    "donchian",    // .upper  .middle  .lower
    "stochastic",  // .k  .d
    "stoch_rsi",   // .k  .d
    "kdj",         // .k  .d  .j
    "supertrend",  // .value  .bullish
    "parabolic_sar", // .sar  .bullish
    "aroon",       // .up  .down  .oscillator
    "vortex",      // .plus_vi  .minus_vi
    "trix",        // .trix  .signal  .histogram
    "ppo",         // .ppo  .signal  .histogram
    "kst",         // .kst  .signal  .histogram
    "pmo",         // .pmo  .signal  .histogram
    "rvi",         // .rvi  .signal
    "smi",         // .smi  .signal
    "fisher",      // .fisher  .signal
    "rwi",         // .rwi_high  .rwi_low
    "ichimoku",    // .tenkan  .kijun  .senkou_a  .senkou_b  .chikou  .above_cloud
    "alligator",   // .jaw  .teeth  .lips  .bullish
    "gmma",        // .short_avg  .long_avg  .bullish
    "kalman",      // .value  .slope
    "bull_bear_power",   // .bull  .bear  .ema
    "chandelier_exit",   // .long_stop  .short_stop  .atr
    "chande_kroll_stop", // .stop_long  .stop_short
    "william_fractal",   // .bullish  .bearish  .fractal_high  .fractal_low
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
pub fn script_lint(script: &str) -> (Vec<LintDiagnostic>, ScriptLintScope) {
    let mut diags: Vec<LintDiagnostic>          = Vec::new();
    let mut cleaned_lines: Vec<&str>            = Vec::new();
    let mut line_map: Vec<usize>                = Vec::new();
    let mut scope_inds: Vec<DeclaredIndicator>  = Vec::new();

    // ── Strip setup directives + regime block first ──────────────────────────
    //
    // The Rhai parser doesn't know `candle.transform(...)` or `regime { … }`.
    // They are handled by `RhaiStrategy::build()` pre-parse extraction, so the
    // linter must do the same — otherwise a valid script would surface a
    // bogus "Expecting ';' to terminate this statement" at `regime {`.
    //
    // Both extractors blank out their span with whitespace so line numbers
    // stay aligned with the original script.
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

    // Collect indicator decls from inside the regime block so the autocomplete
    // scope is complete even if the user declared them only there.
    if let Some(body) = regime_body_opt.as_deref() {
        for line in body.lines() {
            if let Some(decl) = try_parse_indicator_line(line.trim()) {
                let multi = matches!(decl.kind, IndicatorKind::Multi(_));
                let raw_type = decl.ind_type.clone();
                scope_inds.push(DeclaredIndicator {
                    name:      decl.var_name,
                    ind_type:  raw_type,
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
        bar_fields:  super::engine::BAR_FIELDS.to_vec(),
        output_vars: vec!["long", "short", "exit", "tp", "sl", "strength", "is_offset", "reason"],
        functions:   vec![
            "cross_above", "cross_below",
            "rising", "falling", "rising_n", "falling_n",
            "above", "below", "in_range",
            "highest", "lowest",
        ],
    };

    (diags, scope)
}

// ── Internal helpers ──────────────────────────────────────────────────────────

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

    #[test]
    fn lint_single_output_clean() {
        let script = r#"
let ema9  = ind.ema(9);
let rsi14 = ind.rsi(14);
if cross_above(ema9, ema9) && rsi14[0] < 70.0 { entry = true; }
if cross_below(ema9, ema9) { exit = true; }
"#;
        let (errors, scope) = script_lint(script);
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
        let (errors, scope) = script_lint(script);
        assert!(errors.is_empty(), "{errors:?}");
        assert_eq!(scope.indicators.len(), 2);
        assert!(scope.indicators[0].multi);
        assert!(scope.indicators[1].multi);
    }

    #[test]
    fn lint_wrong_prefix() {
        let (errors, _) = script_lint("let ema9 = indi.ema(9);\nif ema9[0] > 0.0 { entry = true; }");
        assert!(!errors.is_empty());
        assert!(errors[0].message.contains("indi"));
        assert_eq!(errors[0].line, 1);
    }

    #[test]
    fn lint_unknown_type_suggestion() {
        let (errors, _) = script_lint("let x = ind.mma(9);");
        assert!(!errors.is_empty());
        assert!(errors[0].message.contains("mma") && errors[0].message.contains("ema"));
    }

    #[test]
    fn lint_syntax_error_mapped() {
        let (errors, _) = script_lint("let ema9 = ind.ema(9);\nif ema9[0] > { entry = true; }");
        assert!(errors.iter().any(|e| e.severity == "error"));
    }

    #[test]
    fn lint_scope_bar_fields() {
        let (_, scope) = script_lint("if close[0] > 0.0 { entry = true; }");
        assert!(scope.bar_fields.contains(&"close") && scope.bar_fields.contains(&"open"));
    }

    #[test]
    fn lint_regime_block_does_not_trip_parser() {
        // Without pre-extraction, the Rhai parser would emit
        // "Expecting ';' to terminate this statement" at `regime {`.
        let script = r#"
regime {
    let adx14 = ind.adx(14);
    if adx14[0].adx > 25.0 { trend = "trending"; }
    else                   { trend = "ranging";  }
}

let ema9 = ind.ema(9);
if cross_above(ema9, ema9) && trend == "trending" { entry = true; }
"#;
        let (errors, scope) = script_lint(script);
        assert!(errors.is_empty(), "false-positive lint on regime block: {errors:?}");
        // Indicators from both regime + main blocks should appear in scope.
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
        let (errors, _) = script_lint(script);
        assert!(errors.is_empty(), "false-positive lint on candle directive: {errors:?}");
    }

    #[test]
    fn lint_misplaced_candle_directive_surfaces_error() {
        let script = r#"let ema9 = ind.ema(9);
candle.transform("heiken_ashi");
"#;
        let (errors, _) = script_lint(script);
        // Directive after a let → extract_candle_directives bails; we expect the
        // error to surface as a lint diagnostic (not a panic / silent skip).
        assert!(!errors.is_empty(), "misplaced directive should produce a diagnostic");
        assert!(errors[0].message.contains("must appear at the top"));
    }
}

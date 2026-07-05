//! Shared linter for strategy scripts — covers both single-TF (v1) and
//! multi-timeframe (v2) syntax.
//!
//! The script surface is identical in both versions: indicators are declared
//! with `ind.TYPE(period)` or `ind.TYPE(period, "TF")`. This module checks
//! prefix/type validity, HTF-vs-base-TF ordering, and Rhai syntax — once,
//! for both engines.

use alm_core::Timeframe;

use crate::script::v1::{
    build_engine, BAR_FIELDS, DEFAULT_BUF_DEPTH,
    extract_candle_directives, extract_regime_block,
    try_parse_indicator_line, IndicatorKind,
    second_arg_is_static_literal,
    PERIOD_EXEMPT,
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
/// 6. period = 0 — error (panics during indicator construction).
/// 7. Cross-parameter constraint violations — error (e.g. macd fast ≥ slow).
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

    const OUTPUT_VARS: &[&str] = &[
        "long", "entry", "short", "exit",
        "tp", "sl", "is_offset", "strength", "reason", "atr",
        "trail", "max_bars",
        "trend", "trend_value", "vol", "vol_value",
    ];

    // Collect indicator decls from inside the regime block.
    if let Some(body) = regime_body_opt.as_deref() {
        for (idx, line) in body.lines().enumerate() {
            let lineno = idx + 1;
            if let Some(decl) = try_parse_indicator_line(line.trim()) {
                if let Some(d) = check_htf_vs_base(&decl.var_name, decl.timeframe, base_tf, lineno) {
                    diags.push(d);
                }
                diags.extend(check_indicator_params(
                    &decl.var_name, &decl.ind_type, decl.period, &decl.extra_params, lineno,
                ));
                if OUTPUT_VARS.contains(&decl.var_name.as_str()) {
                    diags.push(LintDiagnostic {
                        line:     lineno,
                        col:      1,
                        message:  format!(
                            "indicator name '{}' shadows the output variable '{}'; \
                             rename the indicator to avoid writing to the wrong variable \
                             (e.g. use '{}_ind' or '{}_14' instead).",
                            decl.var_name, decl.var_name, decl.var_name, decl.var_name
                        ),
                        severity: "warning",
                    });
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
                    diags.extend(check_indicator_params(
                        &decl.var_name, &decl.ind_type, decl.period, &decl.extra_params, lineno,
                    ));
                    if OUTPUT_VARS.contains(&decl.var_name.as_str()) {
                        diags.push(LintDiagnostic {
                            line:     lineno,
                            col:      1,
                            message:  format!(
                                "indicator name '{}' shadows the output variable '{}'; \
                                 rename the indicator to avoid writing to the wrong variable \
                                 (e.g. use '{}_ind' or '{}_14' instead).",
                                decl.var_name, decl.var_name, decl.var_name, decl.var_name
                            ),
                            severity: "warning",
                        });
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
            for diag in check_negative_n_builtins(line, orig) {
                diags.push(diag);
            }
            for diag in check_non_literal_n_builtins(line, orig) {
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
                for diag in check_negative_n_builtins(line, approx_line) {
                    diags.push(diag);
                }
                for diag in check_non_literal_n_builtins(line, approx_line) {
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
            "trail", "max_bars",
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

/// Scan a single source line for calls to builtin functions where the `n`
/// argument (second positional parameter) is a negative integer literal.
///
/// Affected functions: `rising_n`, `falling_n`, `momentum`, `highest`, `lowest`.
/// A negative `n` at runtime is silently clamped / returns a sentinel (after the
/// CPU DoS guard), but passing a literal `-N` is almost certainly a mistake.
fn check_negative_n_builtins(line: &str, lineno: usize) -> Vec<LintDiagnostic> {
    const FUNCS: &[&str] = &[
        "rising_n", "falling_n", "momentum", "highest", "lowest",
        "stdev", "zscore", "pct_change", "slope", "avg", "sum",
    ];
    let mut diags = Vec::new();

    for &fname in FUNCS {
        let needle = format!("{fname}(");
        let mut search = line;
        let mut col_base = 0usize;

        while let Some(start) = search.find(&needle) {
            let after_open = &search[start + needle.len()..];

            // Walk forward tracking paren depth to find the first top-level comma.
            let mut depth = 1i32;
            let mut comma_pos: Option<usize> = None;
            for (i, ch) in after_open.char_indices() {
                match ch {
                    '(' => depth += 1,
                    ')' => {
                        depth -= 1;
                        if depth == 0 { break; }
                    }
                    ',' if depth == 1 => {
                        comma_pos = Some(i);
                        break;
                    }
                    _ => {}
                }
            }

            if let Some(cp) = comma_pos {
                // Text of the second argument (before the closing paren / next comma).
                let after_comma = after_open[cp + 1..].trim_start();
                // A negative literal looks like `-<digits>` (optionally with spaces
                // between `-` and the digit, though that's uncommon).
                if after_comma.starts_with('-') {
                    let after_minus = after_comma[1..].trim_start();
                    if after_minus.starts_with(|c: char| c.is_ascii_digit()) {
                        diags.push(LintDiagnostic {
                            line:    lineno,
                            col:     col_base + start + 1,
                            message: format!(
                                "'{fname}': second argument (n) must be a positive integer; \
                                 a negative literal is always treated as n ≤ 0 \
                                 and returns a no-op/sentinel value."
                            ),
                            severity: "error",
                        });
                    }
                }
            }

            // Advance past this call to detect further occurrences on the same line.
            col_base += start + needle.len();
            search = &search[start + needle.len()..];
        }
    }

    diags
}

/// Scan a single source line for calls to lookback functions where the `n`
/// argument (second positional parameter) is **not a static integer literal**.
///
/// The bar-ring buffer depth is determined at compile time by
/// `extract_max_lookback`, which can only parse integer literals (and simple
/// arithmetic of literals like `20 + 5`).  If `n` is a variable or a complex
/// runtime expression the buffer will not be sized correctly, causing the
/// affected functions to silently operate on a too-short array (or, after the
/// runtime guard was added, to throw an explicit runtime error).
///
/// Emit `"error"` so the user sees it in Monaco before running the backtest.
fn check_non_literal_n_builtins(line: &str, lineno: usize) -> Vec<LintDiagnostic> {
    const FUNCS: &[&str] = &[
        "highest", "lowest", "momentum", "stdev", "zscore", "pct_change",
        "rising_n", "falling_n", "slope", "avg", "sum",
    ];
    let mut diags = Vec::new();

    for &fname in FUNCS {
        let needle = format!("{fname}(");
        let mut search = line;
        let mut col_base = 0usize;

        while let Some(start) = search.find(&needle) {
            let after_open = &search[start + needle.len()..];

            // Only flag when a second arg is actually present and it is NOT a
            // static literal.  `second_arg_is_static_literal` returns false
            // both when there is no second arg (single-arg form like
            // `stdev(close)`) AND when the arg is a variable/expression.
            // Distinguish the two cases so we don't warn on the valid
            // single-arg forms.
            let has_comma = {
                let mut depth = 0i32;
                let mut found = false;
                for ch in after_open.chars() {
                    match ch {
                        '(' | '[' => depth += 1,
                        ')' | ']' => { if depth == 0 { break; } depth -= 1; }
                        ',' if depth == 0 => { found = true; break; }
                        _ => {}
                    }
                }
                found
            };

            if has_comma && !second_arg_is_static_literal(after_open) {
                diags.push(LintDiagnostic {
                    line:    lineno,
                    col:     col_base + start + 1,
                    message: format!(
                        "'{fname}': second argument (n) must be a static integer literal \
                         (e.g. `{fname}(arr, 20)`) so the bar buffer can be sized at \
                         compile time. Variables and runtime expressions are not analysed \
                         — the buffer will default to {DEFAULT_BUF_DEPTH} bars and the \
                         result will be wrong."
                    ),
                    severity: "error",
                });
            }

            col_base += start + needle.len();
            search   = &search[start + needle.len()..];
        }
    }

    diags
}

/// Indicators where the `period` argument in `ind.TYPE(N)` is NOT forwarded
/// as the `"period"` config key — it maps to other parameters or is ignored
/// entirely. `period = 0` will NOT panic for these types.
///
/// - `kalman`  → params: q_pos, q_vel, r (no period key)
/// - `gmma`    → params: short[], long[] arrays (no period key)
/// - `ao`      → params: fast, slow (no period key)
/// - `coppock` → params: short, long, wma (no period key)
// `PERIOD_EXEMPT` is defined once in `v1::parse` and re-exported via `v1::mod`
// as the single source of truth shared by the linter, v1 runtime, and v2 runtime.

/// Validate period ≥ 1 and cross-parameter constraints for indicators that
/// would panic or produce wrong results with invalid params.
///
/// Only checks params the user actually provided — defaults are assumed sane.
fn check_indicator_params(
    var_name: &str,
    ind_type: &str,
    period: usize,
    extra: &std::collections::HashMap<String, f64>,
    lineno: usize,
) -> Vec<LintDiagnostic> {
    let mut diags: Vec<LintDiagnostic> = Vec::new();

    // All indicators except those that don't use "period" in their config:
    // period = 0 would be forwarded as the period key and panic in new().
    if period == 0 && !PERIOD_EXEMPT.contains(&ind_type) {
        diags.push(LintDiagnostic {
            line: lineno, col: 1,
            message: format!("'{var_name}': period must be ≥ 1, got 0"),
            severity: "error",
        });
    }

    // Helper: get an extra param as usize (rounded).
    let get_u = |key: &str| extra.get(key).map(|&v| v as usize);
    let get_f = |key: &str| extra.get(key).copied();

    match ind_type {
        // macd / ppo: fast (period) < slow.
        "macd" | "ppo" => {
            if let Some(slow) = get_u("slow") {
                if period >= slow {
                    diags.push(LintDiagnostic {
                        line: lineno, col: 1,
                        message: format!(
                            "'{var_name}': {ind_type} fast period ({period}) must be < slow ({slow})"
                        ),
                        severity: "error",
                    });
                }
            }
        }
        // alligator: jaw (period) > teeth > lips.
        "alligator" => {
            if let (Some(teeth), Some(lips)) = (get_u("teeth"), get_u("lips")) {
                if period <= teeth {
                    diags.push(LintDiagnostic {
                        line: lineno, col: 1,
                        message: format!(
                            "'{var_name}': alligator jaw ({period}) must be > teeth ({teeth})"
                        ),
                        severity: "error",
                    });
                }
                if teeth <= lips {
                    diags.push(LintDiagnostic {
                        line: lineno, col: 1,
                        message: format!(
                            "'{var_name}': alligator teeth ({teeth}) must be > lips ({lips})"
                        ),
                        severity: "error",
                    });
                }
            } else if let Some(teeth) = get_u("teeth") {
                if period <= teeth {
                    diags.push(LintDiagnostic {
                        line: lineno, col: 1,
                        message: format!(
                            "'{var_name}': alligator jaw ({period}) must be > teeth ({teeth})"
                        ),
                        severity: "error",
                    });
                }
            }
        }
        // coppock: short ROC (period) < long ROC.
        "coppock" => {
            if let Some(long) = get_u("long") {
                if period >= long {
                    diags.push(LintDiagnostic {
                        line: lineno, col: 1,
                        message: format!(
                            "'{var_name}': coppock short ROC ({period}) must be < long ROC ({long})"
                        ),
                        severity: "error",
                    });
                }
            }
        }
        // kama: fast EMA period < slow EMA period.
        "kama" => {
            if let (Some(fast), Some(slow)) = (get_u("fast"), get_u("slow")) {
                if fast >= slow {
                    diags.push(LintDiagnostic {
                        line: lineno, col: 1,
                        message: format!(
                            "'{var_name}': kama fast EMA ({fast}) must be < slow EMA ({slow})"
                        ),
                        severity: "error",
                    });
                }
            }
        }
        // parabolic_sar: step > 0; max > step.
        "parabolic_sar" => {
            if let Some(step) = get_f("step") {
                if step <= 0.0 {
                    diags.push(LintDiagnostic {
                        line: lineno, col: 1,
                        message: format!(
                            "'{var_name}': parabolic_sar step must be > 0, got {step}"
                        ),
                        severity: "error",
                    });
                }
                if let Some(max) = get_f("max") {
                    if max <= step {
                        diags.push(LintDiagnostic {
                            line: lineno, col: 1,
                            message: format!(
                                "'{var_name}': parabolic_sar max ({max}) must be > step ({step})"
                            ),
                            severity: "error",
                        });
                    }
                }
            }
        }
        // alma: offset ∈ [0, 1]; sigma > 0.
        "alma" => {
            if let Some(offset) = get_f("offset") {
                if !(0.0..=1.0).contains(&offset) {
                    diags.push(LintDiagnostic {
                        line: lineno, col: 1,
                        message: format!(
                            "'{var_name}': alma offset must be in [0.0, 1.0], got {offset}"
                        ),
                        severity: "error",
                    });
                }
            }
            if let Some(sigma) = get_f("sigma") {
                if sigma <= 0.0 {
                    diags.push(LintDiagnostic {
                        line: lineno, col: 1,
                        message: format!(
                            "'{var_name}': alma sigma must be > 0, got {sigma}"
                        ),
                        severity: "error",
                    });
                }
            }
        }
        // tsi: second smoothing < first (period).
        "tsi" => {
            if let Some(second) = get_u("second") {
                if second >= period {
                    diags.push(LintDiagnostic {
                        line: lineno, col: 1,
                        message: format!(
                            "'{var_name}': tsi second smoothing ({second}) must be < first ({period})"
                        ),
                        severity: "error",
                    });
                }
            }
        }
        // uo: fast (period) < medium < slow.
        "uo" => {
            let medium = get_u("medium");
            let slow   = get_u("slow");
            if let Some(med) = medium {
                if period >= med {
                    diags.push(LintDiagnostic {
                        line: lineno, col: 1,
                        message: format!(
                            "'{var_name}': uo fast ({period}) must be < medium ({med})"
                        ),
                        severity: "error",
                    });
                }
                if let Some(sl) = slow {
                    if med >= sl {
                        diags.push(LintDiagnostic {
                            line: lineno, col: 1,
                            message: format!(
                                "'{var_name}': uo medium ({med}) must be < slow ({sl})"
                            ),
                            severity: "error",
                        });
                    }
                }
            }
        }
        _ => {}
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
    } else if htf_ms % base_ms != 0 {
        // V2 gives every HTF binding "live" bucket projection
        // (`LiveBucketAggregator`), which divides `htf_ms / base_ms` to know
        // how many base bars fill one HTF bucket — if that's not exact (e.g.
        // base M3 + HTF M5), the count is silently wrong and `fill_ratio`
        // reports the bucket as fully formed too early. Mirrored as a hard
        // error here (not just at `MtfScriptStrategy::build` time) so the
        // editor catches it before the script is ever run.
        Some(LintDiagnostic {
            line: lineno, col: 1,
            message: format!(
                "'{var_name}': HTF timeframe '{htf}' ({htf_ms} ms) is not an exact multiple of \
                 the base TF '{base}' ({base_ms} ms) — e.g. M5 doesn't evenly divide into M3. \
                 Pick a base TF that evenly divides every declared HTF."
            ),
            severity: "error",
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
    use crate::test_utils::*;
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
    fn lint_htf_not_multiple_of_base_tf_is_error() {
        // M5 (300_000 ms) doesn't evenly divide into M3 (180_000 ms) —
        // `LiveBucketAggregator::fill_ratio`'s integer division would
        // silently under-count the expected bars per bucket.
        let script = r#"let ema9 = ind.ema(9, "M5");
if ema9[0] > 0.0 { entry = true; }
"#;
        let (errors, _) = script_lint(script, Some(Timeframe::M3));
        assert!(
            errors.iter().any(|e| e.severity == "error" && e.message.contains("exact multiple")),
            "expected error for HTF M5 not a multiple of base M3, got: {errors:?}"
        );
    }

    #[test]
    fn lint_htf_exact_multiple_of_base_tf_is_clean() {
        // M15 (900_000 ms) is exactly 5x M3 (180_000 ms).
        let script = r#"let ema9 = ind.ema(9, "M15");
if ema9[0] > 0.0 { entry = true; }
"#;
        let (errors, _) = script_lint(script, Some(Timeframe::M3));
        let htf_errors: Vec<_> = errors.iter()
            .filter(|e| e.message.contains("M15") || e.message.contains("multiple"))
            .collect();
        assert!(htf_errors.is_empty(), "unexpected htf errors: {htf_errors:?}");
    }

    // ── Period / cross-param checks ──────────────────────────────────────────

    #[test]
    fn lint_period_zero_is_error() {
        let (errors, _) = script_lint("let ema0 = ind.ema(0);\nif ema0[0] > 0.0 { entry = true; }", None);
        assert!(
            errors.iter().any(|e| e.severity == "error" && e.message.contains("period") && e.message.contains("ema0")),
            "expected period=0 error, got: {errors:?}"
        );
    }

    #[test]
    fn lint_period_zero_exempt_indicators_clean() {
        // kalman, gmma, ao, bop, obv, vwap, fractal, coppock don't use period key.
        // pmo, smi, kst, parabolic_sar, uo handle period=0 with built-in defaults.
        for ind in &["kalman", "gmma", "ao", "coppock", "bop", "obv", "vwap", "fractal",
                     "pmo", "smi", "kst", "parabolic_sar", "uo"] {
            let script = format!("let x = ind.{ind}(0);\nif x[0] > 0.0 {{ entry = true; }}");
            let (errors, _) = script_lint(&script, None);
            let period_errors: Vec<_> = errors.iter()
                .filter(|e| e.severity == "error" && e.message.contains("period"))
                .collect();
            assert!(
                period_errors.is_empty(),
                "ind.{ind}(0) should NOT error on period=0 (exempt), got: {period_errors:?}"
            );
        }
    }

    #[test]
    fn lint_macd_fast_ge_slow_is_error() {
        let (errors, _) = script_lint("let m = ind.macd(26, 12);\nif m[0] > 0.0 { entry = true; }", None);
        assert!(
            errors.iter().any(|e| e.severity == "error" && e.message.contains("fast") && e.message.contains("slow")),
            "expected macd fast >= slow error, got: {errors:?}"
        );
    }

    #[test]
    fn lint_macd_valid_params_clean() {
        let (errors, _) = script_lint("let m = ind.macd(12, 26, 9);\nif m[0] > 0.0 { entry = true; }", None);
        let param_errors: Vec<_> = errors.iter().filter(|e| e.message.contains("fast") || e.message.contains("slow")).collect();
        assert!(param_errors.is_empty(), "valid macd should not error: {param_errors:?}");
    }

    #[test]
    fn lint_alligator_jaw_le_teeth_is_error() {
        let (errors, _) = script_lint("let a = ind.alligator(8, 13, 5);\nif a[0] > 0.0 { entry = true; }", None);
        assert!(
            errors.iter().any(|e| e.severity == "error" && e.message.contains("jaw") && e.message.contains("teeth")),
            "expected jaw <= teeth error, got: {errors:?}"
        );
    }

    #[test]
    fn lint_alligator_valid_params_clean() {
        let (errors, _) = script_lint("let a = ind.alligator(13, 8, 5);\nif a[0] > 0.0 { entry = true; }", None);
        let param_errors: Vec<_> = errors.iter().filter(|e| e.message.contains("jaw") || e.message.contains("teeth") || e.message.contains("lips")).collect();
        assert!(param_errors.is_empty(), "valid alligator should not error: {param_errors:?}");
    }

    #[test]
    fn lint_parabolic_sar_step_zero_is_error() {
        let (errors, _) = script_lint("let sar = ind.parabolic_sar(5, step=0.0);\nif sar[0] > 0.0 { entry = true; }", None);
        assert!(
            errors.iter().any(|e| e.severity == "error" && e.message.contains("step")),
            "expected step=0 error, got: {errors:?}"
        );
    }

    #[test]
    fn lint_period_zero_in_regime_block_is_error() {
        let script = r#"
regime {
    let adx0 = ind.adx(0);
    if adx0[0] > 25.0 { trend = "trending"; }
}
let ema9 = ind.ema(9);
if ema9[0] > 0.0 { entry = true; }
"#;
        let (errors, _) = script_lint(script, None);
        assert!(
            errors.iter().any(|e| e.severity == "error" && e.message.contains("period") && e.message.contains("adx0")),
            "expected period=0 error in regime block, got: {errors:?}"
        );
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

    // ── Negative n checks ────────────────────────────────────────────────────

    #[test]
    fn lint_negative_n_rising_falling_is_error() {
        let script = r#"
let ema9 = ind.ema(9);
if rising_n(ema9, -3)  { entry = true; }
if falling_n(ema9, -2) { exit  = true; }
"#;
        let (errors, _) = script_lint(script, None);
        assert!(
            errors.iter().any(|e| e.severity == "error" && e.message.contains("rising_n")),
            "expected error for rising_n with negative n, got: {errors:?}"
        );
        assert!(
            errors.iter().any(|e| e.severity == "error" && e.message.contains("falling_n")),
            "expected error for falling_n with negative n, got: {errors:?}"
        );
    }

    #[test]
    fn lint_negative_n_momentum_is_error() {
        let script = r#"
let rsi14 = ind.rsi(14);
let mom = momentum(rsi14, -5);
if mom > 0.0 { entry = true; }
"#;
        let (errors, _) = script_lint(script, None);
        assert!(
            errors.iter().any(|e| e.severity == "error" && e.message.contains("momentum")),
            "expected error for momentum with negative n, got: {errors:?}"
        );
    }

    #[test]
    fn lint_negative_n_highest_lowest_is_error() {
        let script = r#"
let ema9 = ind.ema(9);
let hi = highest(ema9, -10);
let lo = lowest(ema9,  -10);
if hi > lo { entry = true; }
"#;
        let (errors, _) = script_lint(script, None);
        assert!(
            errors.iter().any(|e| e.severity == "error" && e.message.contains("highest")),
            "expected error for highest with negative n, got: {errors:?}"
        );
        assert!(
            errors.iter().any(|e| e.severity == "error" && e.message.contains("lowest")),
            "expected error for lowest with negative n, got: {errors:?}"
        );
    }

    #[test]
    fn lint_negative_n_positive_literal_is_clean() {
        let script = r#"
let ema9 = ind.ema(9);
let rsi14 = ind.rsi(14);
if rising_n(ema9, 3)      { entry = true; }
if falling_n(ema9, 2)     { exit  = true; }
let mom = momentum(rsi14, 5);
let hi  = highest(ema9, 10);
let lo  = lowest(ema9,  10);
"#;
        let (errors, _) = script_lint(script, None);
        let neg_errors: Vec<_> = errors.iter()
            .filter(|e| e.message.contains("must be a positive integer"))
            .collect();
        assert!(
            neg_errors.is_empty(),
            "positive n args should not trigger negative-n error, got: {neg_errors:?}"
        );
    }

    // ── Non-literal n checks ─────────────────────────────────────────────────

    #[test]
    fn lint_variable_n_in_lookback_is_error() {
        // `length` is a variable, not a literal → linter must flag it.
        let script = r#"
let ema9 = ind.ema(9);
let length = 20;
let hi = highest(ema9, length);
let lo = lowest(ema9,  length);
if hi > lo { entry = true; }
"#;
        let (errors, _) = script_lint(script, None);
        assert!(
            errors.iter().any(|e| e.severity == "error" && e.message.contains("highest")),
            "expected error for highest(arr, variable), got: {errors:?}"
        );
        assert!(
            errors.iter().any(|e| e.severity == "error" && e.message.contains("lowest")),
            "expected error for lowest(arr, variable), got: {errors:?}"
        );
    }

    #[test]
    fn lint_variable_n_momentum_stdev_zscore_is_error() {
        let script = r#"
let rsi14 = ind.rsi(14);
let n = 5;
let mom = momentum(rsi14, n);
let sd  = stdev(rsi14, n);
let z   = zscore(rsi14, n);
if mom > 0.0 { entry = true; }
"#;
        let (errors, _) = script_lint(script, None);
        assert!(errors.iter().any(|e| e.message.contains("momentum")), "momentum var n: {errors:?}");
        assert!(errors.iter().any(|e| e.message.contains("stdev")),    "stdev var n: {errors:?}");
        assert!(errors.iter().any(|e| e.message.contains("zscore")),   "zscore var n: {errors:?}");
    }

    #[test]
    fn lint_literal_n_in_lookback_is_clean() {
        // Static literal and constant arithmetic expression → no error.
        let script = r#"
let ema9 = ind.ema(9);
let hi = highest(ema9, 20);
let lo = lowest(ema9,  20 + 5);
let sd = stdev(ema9, 10);
let z  = zscore(ema9, 10);
if hi > lo { entry = true; }
"#;
        let (errors, _) = script_lint(script, None);
        let lit_errors: Vec<_> = errors.iter()
            .filter(|e| e.message.contains("static integer literal"))
            .collect();
        assert!(
            lit_errors.is_empty(),
            "literal/constant-expr n should not trigger non-literal error: {lit_errors:?}"
        );
    }

    #[test]
    fn lint_single_arg_stdev_zscore_is_clean() {
        // Single-arg forms (no n) must not be flagged.
        let script = r#"
let ema9 = ind.ema(9);
let sd = stdev(ema9);
let z  = zscore(ema9);
if sd > 0.0 { entry = true; }
"#;
        let (errors, _) = script_lint(script, None);
        let lit_errors: Vec<_> = errors.iter()
            .filter(|e| e.message.contains("static integer literal"))
            .collect();
        assert!(
            lit_errors.is_empty(),
            "single-arg stdev/zscore should not error: {lit_errors:?}"
        );
    }

    #[test]
    fn lint_negative_n_in_regime_block_is_error() {
        let script = r#"
regime {
    let adx14 = ind.adx(14);
    let hi = highest(adx14, -5);
    if hi > 25.0 { trend = "trending"; }
}
let ema9 = ind.ema(9);
if ema9[0] > 0.0 { entry = true; }
"#;
        let (errors, _) = script_lint(script, None);
        assert!(
            errors.iter().any(|e| e.severity == "error" && e.message.contains("highest")),
            "expected error for highest(-5) in regime block, got: {errors:?}"
        );
    }

    #[test]
    fn lint_htf_indicator_name_shadows_output_var() {
        let script = r#"
let atr = ind.atr(14);
if atr[0] > 1.5 { entry = true; }
"#;
        let (errors, _) = script_lint(script, None);
        assert!(
            errors.iter().any(|e| e.severity == "warning" && e.message.contains("atr")),
            "expected warning for indicator name 'atr' shadowing output var, got: {errors:?}"
        );
    }

    #[test]
    fn lint_period_zero_pmo_smi_kst_clean() {
        for ind in &["pmo", "smi", "kst", "parabolic_sar", "uo"] {
            let script = format!("let x = ind.{ind}(0);\nif x[0] > 0.0 {{ entry = true; }}");
            let (errors, _) = script_lint(&script, None);
            let period_errors: Vec<_> = errors.iter()
                .filter(|e| e.severity == "error" && e.message.contains("period"))
                .collect();
            assert!(
                period_errors.is_empty(),
                "ind.{ind}(0) should NOT error on period=0 (exempt), got: {period_errors:?}"
            );
        }
    }

    #[test]
    fn lint_avg_sum_errors_and_clean() {
        let script = r#"
let ema9 = ind.ema(9);
let bad_avg = avg(ema9, -5);
let bad_sum = sum(ema9, -10);
let n = 5;
let var_avg = avg(ema9, n);
let var_sum = sum(ema9, n);
let good_avg = avg(ema9, 5);
let good_sum = sum(ema9, 10);
let single_avg = avg(ema9);
let single_sum = sum(ema9);
if good_avg > 0.0 { entry = true; }
"#;
        let (errors, _) = script_lint(script, None);
        // Assert negative n errors
        assert!(errors.iter().any(|e| e.severity == "error" && e.message.contains("'avg'") && e.message.contains("positive integer")), "avg negative n check failed: {errors:?}");
        assert!(errors.iter().any(|e| e.severity == "error" && e.message.contains("'sum'") && e.message.contains("positive integer")), "sum negative n check failed: {errors:?}");

        // Assert variable n errors
        assert!(errors.iter().any(|e| e.severity == "error" && e.message.contains("'avg'") && e.message.contains("static integer literal")), "avg var n check failed: {errors:?}");
        assert!(errors.iter().any(|e| e.severity == "error" && e.message.contains("'sum'") && e.message.contains("static integer literal")), "sum var n check failed: {errors:?}");

        // Filter errors related to good_avg, good_sum, single_avg, single_sum
        let unexpected: Vec<_> = errors.iter()
            .filter(|e| e.message.contains("good_avg") || e.message.contains("good_sum") || e.message.contains("single_avg") || e.message.contains("single_sum"))
            .collect();
        assert!(unexpected.is_empty(), "unexpected errors for valid/single-arg forms: {unexpected:?}");
    }

    #[test]
    fn lint_trail_max_bars_shadowing() {
        let script = r#"
let trail = ind.ema(9);
let max_bars = ind.rsi(14);
if trail[0] > max_bars[0] { entry = true; }
"#;
        let (errors, _) = script_lint(script, None);
        assert!(
            errors.iter().any(|e| e.severity == "warning" && e.message.contains("trail")),
            "expected warning for indicator name 'trail' shadowing, got: {errors:?}"
        );
        assert!(
            errors.iter().any(|e| e.severity == "warning" && e.message.contains("max_bars")),
            "expected warning for indicator name 'max_bars' shadowing, got: {errors:?}"
        );
    }
}

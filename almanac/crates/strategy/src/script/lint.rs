//! Shared linter for strategy scripts — covers both single-TF (v1) and
//! multi-timeframe (v2) syntax.
//!
//! The script surface is identical in both versions: indicators are declared
//! with `ind.TYPE(period)` or `ind.TYPE(period, "TF")`. This module checks
//! prefix/type validity, HTF-vs-base-TF ordering, and Rhai syntax — once,
//! for both engines.

use alm_core::Timeframe;
use rhai::{ASTNode, Expr};

use crate::script::v1::{
    shared_engine, BAR_FIELDS, DEFAULT_BUF_DEPTH,
    extract_candle_directives, extract_regime_block,
    try_parse_indicator_line, IndicatorKind,
    second_arg_is_static_literal,
    PERIOD_EXEMPT, positional_param_names,
    rewrite_ta_line, validate_ta_declarations, brace_depth_delta, validate_ta_top_level,
};

/// Bare method names callable on the `ta` state map — `ta.ema(...)`,
/// `ta.smma(...)`, etc. Mirrors the registrations in `crate::script::ta::register_ta`.
pub const KNOWN_TA_FUNCS: &[&str] = &[
    "ema", "smma", "decay", "sma", "rsum", "stdev",
    "highest", "lowest", "wma", "hma", "vwma", "reset",
];

/// Script output/context vars — pre-declared in the Rhai `Scope` before the
/// script runs (`scope.push("entry", false)` etc. in `v1::strategy.rs`), so a
/// bare `entry = true;` (no `let`) works without erroring: Rhai treats
/// top-level assignment to a name that already exists in scope as a normal
/// write. Assigning to a name NOT in this list (and not a declared
/// `ind.*`/`ta.*` var) still doesn't error — Rhai creates it as a fresh local
/// — which is exactly the silent-typo trap `check_unrecognized_assignment`
/// exists to catch (`stop_loss = ...` instead of `sl = ...`).
const OUTPUT_VARS: &[&str] = &[
    "long", "entry", "short", "exit",
    "tp", "sl", "is_offset", "strength", "reason", "atr",
    "trail", "max_bars",
    "trend", "trend_value", "vol", "vol_value",
];

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

/// A literal threshold the script itself compares this indicator's value
/// against (e.g. `rsi14[0] < 25` → `{field: None, op: "<", value: 25.0}`),
/// extracted by walking the compiled Rhai AST — see [`extract_ast_thresholds`].
#[derive(Debug, Clone, PartialEq, serde::Serialize)]
#[cfg_attr(feature = "openapi", derive(utoipa::ToSchema))]
pub struct ScriptThreshold {
    /// `Some("k")` for an explicit multi-output field access like
    /// `stoch1[0].k < 20`. A BARE multi-output access (`adx14[0] > 25.0`,
    /// no `.field`) also resolves to `Some(primary_field)` — e.g. `Some("adx")`
    /// — since that's what the value actually is at runtime (see
    /// `IndicatorKind::Multi`'s doc comment in `v1::parse`: the primary
    /// field is what a bare `[i]` access reads). `None` only for a truly
    /// single-output indicator like `rsi14[0] < 25`, which has no fields at all.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub field: Option<String>,
    /// `"<"` | `"<="` | `">"` | `">="`.
    pub op:    String,
    pub value: f64,
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
    /// Literal comparison thresholds the script uses against this
    /// indicator, e.g. for personalizing chart reference lines instead of a
    /// generic default. Empty when the script never compares this indicator
    /// against a literal number, or when the script failed to compile.
    #[serde(skip_serializing_if = "Vec::is_empty")]
    pub thresholds: Vec<ScriptThreshold>,
}

/// Scope information for Monaco autocomplete / hover.
#[derive(Debug, Clone, Default, serde::Serialize)]
#[cfg_attr(feature = "openapi", derive(utoipa::ToSchema))]
pub struct ScriptLintScope {
    pub indicators:  Vec<DeclaredIndicator>,
    pub bar_fields:  Vec<&'static str>,
    pub output_vars: Vec<&'static str>,
    pub functions:   Vec<&'static str>,
    /// Names bound via `let NAME = ta.FUNC(...)` — separate from `indicators`
    /// since `ta.*` state isn't a `DeclaredIndicator` (no fixed type/period).
    pub ta_vars:      Vec<String>,
    /// Method names callable on `ta.*` — see `KNOWN_TA_FUNCS`.
    pub ta_functions: Vec<&'static str>,
}

// ── AST-based threshold extraction ────────────────────────────────────────────
//
// Unlike every other check in this file (which scans raw lines so it still
// works on syntactically-broken, mid-keystroke script for live Monaco
// linting), this walks the real compiled Rhai `AST` — extracting a threshold
// is only meaningful for a script that already compiles, so there's no
// tolerate-broken-code constraint to give up robustness for. Requires the
// `rhai` crate's `internals` feature (enabled in Cargo.toml) to access
// `AST::walk`/`Expr`/`ASTNode`.

/// Walks the compiled `ast` for literal comparisons against a declared
/// indicator's value — `varName[i] <op> LITERAL`, `LITERAL <op> varName[i]`,
/// or the multi-output form `varName[i].field <op> LITERAL` — and returns
/// every distinct threshold found, keyed by indicator variable name. Used to
/// personalize chart reference lines (e.g. RSI's oversold/overbought line)
/// from the user's own script instead of always drawing a generic default.
///
/// `varName[i] <op> otherVar[j]` (comparing two indicators to each other) is
/// deliberately NOT recorded as a threshold for either side — only a
/// literal-vs-indicator comparison counts.
fn extract_ast_thresholds(
    ast: &rhai::AST,
    primary_fields: &std::collections::HashMap<String, Option<String>>,
) -> std::collections::HashMap<String, Vec<ScriptThreshold>> {
    let mut found: std::collections::HashMap<String, Vec<ScriptThreshold>> = std::collections::HashMap::new();

    ast.walk(&mut |path: &[ASTNode]| {
        if let Some(ASTNode::Expr(Expr::FnCall(call, ..))) = path.last() {
            if call.is_operator_call() && call.args.len() == 2 {
                if let Some(op) = comparison_op(call.name.as_str()) {
                    let lhs_ind = as_indexed_indicator(&call.args[0], primary_fields);
                    let rhs_ind = as_indexed_indicator(&call.args[1], primary_fields);

                    let entry = if let (Some((name, field)), None) = (&lhs_ind, &rhs_ind) {
                        // varName[i] <op> ??? — rhs must be a literal
                        call.args[1]
                            .get_literal_value(None)
                            .and_then(|d| d.as_float().ok())
                            .map(|value| (name.clone(), field.clone(), op.to_string(), value))
                    } else if let (None, Some((name, field))) = (&lhs_ind, &rhs_ind) {
                        // ??? <op> varName[i] — lhs must be a literal; flip the operator
                        // so the recorded op always reads "indicator <op> literal".
                        call.args[0]
                            .get_literal_value(None)
                            .and_then(|d| d.as_float().ok())
                            .map(|value| (name.clone(), field.clone(), flip_comparison_op(op), value))
                    } else {
                        // Both sides indexed indicators (cross-indicator
                        // comparison) or neither — not a threshold.
                        None
                    };

                    if let Some((name, field, op, value)) = entry {
                        let list = found.entry(name).or_default();
                        let t = ScriptThreshold { field, op, value };
                        if !list.contains(&t) {
                            list.push(t);
                        }
                    }
                }
            }
        }
        true
    });

    found
}

/// `<`/`<=`/`>`/`>=` only — `==`/`!=` aren't threshold-shaped semantically.
fn comparison_op(name: &str) -> Option<&'static str> {
    match name {
        "<" => Some("<"),
        "<=" => Some("<="),
        ">" => Some(">"),
        ">=" => Some(">="),
        _ => None,
    }
}

fn flip_comparison_op(op: &str) -> String {
    match op {
        "<" => ">", "<=" => ">=", ">" => "<", ">=" => "<=",
        other => other,
    }
    .to_string()
}

/// If `expr` is `varName[i]` (single-output) or `varName[i].field`
/// (multi-output) for a `varName` declared in `primary_fields`, returns
/// `(varName, field)`. Anything else — a scratch variable, a function call,
/// a nested expression — returns `None`.
///
/// Confirmed via a probe of rhai 1.24's actual parse output: `a[i].field`
/// is NOT `Dot(Index(a, i), Property(field))` as its surface syntax might
/// suggest — it's `Index{lhs: a, rhs: Dot{lhs: i, rhs: Property(field)}}`.
/// The property access is nested inside the index's `rhs`, not wrapped
/// around the whole index expression.
///
/// A BARE multi-output access (no `.field` at all, e.g. `adx14[0] > 25.0`)
/// resolves to that type's primary field (`Some("adx")`) rather than `None`
/// — see `IndicatorKind::Multi`'s doc comment in `v1::parse`: a bare `[i]`
/// access on a multi-output indicator reads its primary field, so the
/// threshold really does belong to that specific field, not to "no field."
fn as_indexed_indicator(
    expr: &Expr,
    primary_fields: &std::collections::HashMap<String, Option<String>>,
) -> Option<(String, Option<String>)> {
    match expr {
        Expr::Index(x, ..) => {
            let (name, primary) = match &x.lhs {
                Expr::Variable(v, ..) => {
                    let name = v.1.as_str();
                    match primary_fields.get(name) {
                        Some(primary) => (name.to_string(), primary.clone()),
                        None => return None,
                    }
                }
                _ => return None,
            };
            let field = match &x.rhs {
                Expr::Dot(y, ..) => match &y.rhs {
                    Expr::Property(p, ..) => Some(p.2.to_string()),
                    _ => None,
                },
                _ => None,
            };
            Some((name, field.or(primary)))
        }
        _ => None,
    }
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
    let mut cleaned_lines: Vec<String>          = Vec::new();
    let mut line_map: Vec<usize>                = Vec::new();
    let mut scope_inds: Vec<DeclaredIndicator>  = Vec::new();
    // Shared across regime + main blocks — mirrors the one namespace
    // `ind.*`/`ta.*` declarations share at real build time (see
    // `v1::strategy::process_script_block`).
    let mut decl_names: std::collections::HashSet<String> = std::collections::HashSet::new();
    let mut ta_vars: Vec<String> = Vec::new();
    // ind.* name → primary field for multi-output types (`None` for
    // single-output) — used by `extract_ast_thresholds` below to resolve a
    // BARE `varName[i]` access on a multi-output indicator to the field it
    // actually reads at runtime (`IndicatorKind::Multi`'s doc comment in
    // `v1::parse`: a bare access reads the primary field, e.g. `adx14[0]`
    // reads `.adx`), instead of leaving it ambiguous.
    let mut primary_fields: std::collections::HashMap<String, Option<String>> = std::collections::HashMap::new();

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
        let mut regime_brace_depth: i32 = 0;
        for (idx, line) in body.lines().enumerate() {
            let lineno = idx + 1;
            let trimmed = line.trim();
            if let Some(decl) = try_parse_indicator_line(trimmed) {
                if let Some(d) = check_htf_vs_base(&decl.var_name, decl.timeframe, base_tf, lineno) {
                    diags.push(d);
                }
                diags.extend(check_indicator_params(
                    &decl.var_name, &decl.ind_type, decl.period, &decl.extra_params, lineno,
                ));
                diags.extend(check_unknown_param_keys(
                    &decl.var_name, &decl.ind_type, &decl.extra_params, lineno,
                ));
                if let Some(d) = check_kalman_positional_misuse(&decl.var_name, &decl.ind_type, trimmed, lineno) {
                    diags.push(d);
                }
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
                if !decl_names.insert(decl.var_name.clone()) {
                    diags.push(LintDiagnostic {
                        line: lineno, col: 1,
                        message: format!(
                            "`{}` is declared more than once (`ind.*`/`ta.*` declarations share one namespace)",
                            decl.var_name
                        ),
                        severity: "error",
                    });
                }
                let multi = matches!(decl.kind, IndicatorKind::Multi(_));
                let primary = match &decl.kind {
                    IndicatorKind::Multi(field) => Some(field.clone()),
                    IndicatorKind::Single(_) => None,
                };
                primary_fields.insert(decl.var_name.clone(), primary);
                scope_inds.push(DeclaredIndicator {
                    name:      decl.var_name,
                    ind_type:  decl.ind_type,
                    period:    decl.period,
                    timeframe: decl.timeframe.map(|tf| tf.to_string()),
                    live:      decl.live,
                    multi,
                    thresholds: Vec::new(),
                });
            } else if let Err(e) = validate_ta_declarations(trimmed) {
                diags.push(LintDiagnostic { line: lineno, col: 1, message: e.to_string(), severity: "error" });
            } else {
                match rewrite_ta_line(trimmed) {
                    Err(e) => diags.push(LintDiagnostic { line: lineno, col: 1, message: e.to_string(), severity: "error" }),
                    Ok(Some((var_name, _))) => {
                        if let Err(e) = validate_ta_top_level(&var_name, trimmed, regime_brace_depth) {
                            diags.push(LintDiagnostic { line: lineno, col: 1, message: e.to_string(), severity: "error" });
                        }
                        if !decl_names.insert(var_name.clone()) {
                            diags.push(LintDiagnostic {
                                line: lineno, col: 1,
                                message: format!(
                                    "`{var_name}` is declared more than once (`ind.*`/`ta.*` declarations share one namespace)"
                                ),
                                severity: "error",
                            });
                        } else {
                            ta_vars.push(var_name);
                        }
                    }
                    Ok(None) => {}
                }
            }
            regime_brace_depth += brace_depth_delta(line);
        }
    }

    let mut main_brace_depth: i32 = 0;
    for (idx, line) in main_source.lines().enumerate() {
        let lineno  = idx + 1;
        let trimmed = line.trim();

        // `ta.*` isn't an `ind.*`-prefix typo — route it to the dedicated
        // handling below instead of the "wrong indicator prefix" check.
        let ind_prefix = extract_raw_indicator_type(trimmed).filter(|(p, _)| p != "ta");

        if let Some((prefix, raw_type)) = ind_prefix {
            if prefix != "ind" {
                diags.push(LintDiagnostic {
                    line: lineno, col: 1,
                    message: format!(
                        "Wrong indicator prefix '{prefix}.'; use 'ind.{raw_type}(...)' instead"
                    ),
                    severity: "error",
                });
                cleaned_lines.push(line.to_string());
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
                    diags.extend(check_unknown_param_keys(
                        &decl.var_name, &decl.ind_type, &decl.extra_params, lineno,
                    ));
                    if let Some(d) = check_kalman_positional_misuse(&decl.var_name, &decl.ind_type, trimmed, lineno) {
                        diags.push(d);
                    }
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
                    if !decl_names.insert(decl.var_name.clone()) {
                        diags.push(LintDiagnostic {
                            line: lineno, col: 1,
                            message: format!(
                                "`{}` is declared more than once (`ind.*`/`ta.*` declarations share one namespace)",
                                decl.var_name
                            ),
                            severity: "error",
                        });
                    }
                    let multi = matches!(decl.kind, IndicatorKind::Multi(_));
                    let primary = match &decl.kind {
                        IndicatorKind::Multi(field) => Some(field.clone()),
                        IndicatorKind::Single(_) => None,
                    };
                    primary_fields.insert(decl.var_name.clone(), primary);
                    scope_inds.push(DeclaredIndicator {
                        name:      decl.var_name,
                        ind_type:  raw_type,
                        period:    decl.period,
                        timeframe: decl.timeframe.map(|tf| tf.to_string()),
                        live:      decl.live,
                        multi,
                        thresholds: Vec::new(),
                    });
                } else {
                    cleaned_lines.push(line.to_string());
                    line_map.push(lineno);
                }
            }
        } else {
            // `ta.*` declarations (rewritten with injected key + buf so the
            // later `engine.compile(&cleaned)` pass sees valid call arity)
            // plus every other non-indicator line.
            match validate_ta_declarations(trimmed) {
                Err(e) => {
                    diags.push(LintDiagnostic { line: lineno, col: 1, message: e.to_string(), severity: "error" });
                    cleaned_lines.push(line.to_string());
                    line_map.push(lineno);
                }
                Ok(()) => match rewrite_ta_line(trimmed) {
                    Err(e) => {
                        diags.push(LintDiagnostic { line: lineno, col: 1, message: e.to_string(), severity: "error" });
                        cleaned_lines.push(line.to_string());
                        line_map.push(lineno);
                    }
                    Ok(Some((var_name, rewritten))) => {
                        if let Err(e) = validate_ta_top_level(&var_name, trimmed, main_brace_depth) {
                            diags.push(LintDiagnostic { line: lineno, col: 1, message: e.to_string(), severity: "error" });
                        }
                        if !decl_names.insert(var_name.clone()) {
                            diags.push(LintDiagnostic {
                                line: lineno, col: 1,
                                message: format!(
                                    "`{var_name}` is declared more than once (`ind.*`/`ta.*` declarations share one namespace)"
                                ),
                                severity: "error",
                            });
                        } else {
                            ta_vars.push(var_name);
                        }
                        cleaned_lines.push(rewritten);
                        line_map.push(lineno);
                    }
                    Ok(None) => {
                        cleaned_lines.push(line.to_string());
                        line_map.push(lineno);
                    }
                },
            }
        }

        main_brace_depth += brace_depth_delta(line);
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

        // Real output-field names per declared multi-output var, sourced from
        // `catalog::all()` — the same static list the `catalog_matches_engine_indicators`
        // guard test keeps in sync with `alm_indicator::field_names()`.
        let catalog_all = crate::catalog::all();
        let outputs_by_type: std::collections::HashMap<&str, &[&str]> = catalog_all.iter()
            .map(|m| (m.name, m.outputs.as_slice()))
            .collect();
        let multi_fields: std::collections::HashMap<&str, &[&str]> = scope_inds.iter()
            .filter(|d| d.multi)
            .filter_map(|d| outputs_by_type.get(d.ind_type.as_str()).map(|&f| (d.name.as_str(), f)))
            .collect();

        // Every declared ind.*/ta.* var is an Array — used by
        // `field_access_missing_index` (checks scalar AND multi alike, unlike
        // `field_access_on_scalar` which only cares about scalars).
        let array_names: std::collections::HashSet<&str> = scalar_names.iter().copied()
            .chain(multi_fields.keys().copied())
            .chain(ta_vars.iter().map(|s| s.as_str()))
            .collect();

        // Check main-block non-declaration lines. Mask `//` comments and
        // string-literal contents FIRST (same helper `scan_undeclared_reads`
        // already uses) — none of these six checks did this before, so a
        // comment merely describing pseudocode (e.g. `// bb < 30 is
        // oversold`) was scanned as if it were real code and could trip a
        // false "missing [0]" / "unrecognized assignment" / etc. diagnostic.
        // `mask_noise` preserves byte length/positions, so column offsets
        // computed below stay accurate.
        for (i, line) in cleaned_lines.iter().enumerate() {
            let orig = line_map[i];
            let masked = mask_noise(line);
            for diag in field_access_on_scalar(&masked, orig, &scalar_names) {
                diags.push(diag);
            }
            for diag in field_access_wrong_name(&masked, orig, &multi_fields) {
                diags.push(diag);
            }
            for diag in field_access_missing_index(&masked, orig, &array_names) {
                diags.push(diag);
            }
            for diag in check_unrecognized_assignment(&masked, orig, &decl_names) {
                diags.push(diag);
            }
            for diag in check_negative_n_builtins(&masked, orig) {
                diags.push(diag);
            }
            for diag in check_non_literal_n_builtins(&masked, orig) {
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
                let masked = mask_noise(line);
                for diag in field_access_on_scalar(&masked, approx_line, &scalar_names) {
                    diags.push(diag);
                }
                for diag in field_access_wrong_name(&masked, approx_line, &multi_fields) {
                    diags.push(diag);
                }
                for diag in field_access_missing_index(&masked, approx_line, &array_names) {
                    diags.push(diag);
                }
                for diag in check_unrecognized_assignment(&masked, approx_line, &decl_names) {
                    diags.push(diag);
                }
                for diag in check_negative_n_builtins(&masked, approx_line) {
                    diags.push(diag);
                }
                for diag in check_non_literal_n_builtins(&masked, approx_line) {
                    diags.push(diag);
                }
            }
        }
    }

    // ── Semantic check: reading a name before it's ever `let`-declared ────────
    //
    // Rhai resolves a bare identifier against its `Scope` at the point each
    // statement runs — it does NOT hoist `let` declarations, so `if
    // some_var[0] > 0 { ... }` above the line that declares `some_var` (or a
    // script that never declares it at all) compiles just fine and only
    // fails — or silently reads as nothing — at runtime, once per bar,
    // forever. `ScriptStrategy::on_bar` swallows that runtime error, so this
    // is otherwise a silent-dead-strategy bug with zero feedback. Separate,
    // self-contained pass (re-parses decl lines via the same pure
    // `try_parse_indicator_line`/`rewrite_ta_line` helpers already used
    // above) so it can't disturb the existing declaration-collection loop.
    let ind_names: std::collections::HashSet<String> = scope_inds.iter().map(|d| d.name.clone()).collect();
    diags.extend(check_undeclared_reads(regime_body_opt.as_deref(), &main_source, &decl_names, &ind_names));

    let cleaned = cleaned_lines.join("\n");
    let engine = shared_engine();
    match engine.compile(&cleaned) {
        Ok(ast) => {
            let mut thresholds = extract_ast_thresholds(&ast, &primary_fields);
            for ind in scope_inds.iter_mut() {
                if let Some(t) = thresholds.remove(&ind.name) {
                    ind.thresholds = t;
                }
            }
        }
        Err(e) => {
            let pos = e.1;
            let orig_line = pos
                .line()
                .and_then(|l| line_map.get(l.saturating_sub(1)).copied())
                .unwrap_or_else(|| pos.line().unwrap_or(0));
            let col = pos.position().unwrap_or(1);

            // Rhai's own error for `and`/`or`/`not` (Python/Pine-Script habit —
            // Rhai uses `&&`/`||`/`!`) is the raw parser message at whatever
            // token comes *after* the bad keyword (e.g. "Expecting '{' to start a
            // statement block"), which says nothing about the real cause. Add a
            // friendlier hint alongside it — keep the raw message too, a power
            // user debugging something else still wants the real parser text.
            if let Some(word) = first_python_boolean_keyword(&cleaned) {
                diags.push(LintDiagnostic {
                    line: orig_line,
                    col,
                    message: format!(
                        "Rhai uses '&&' / '||' / '!' for boolean logic, not '{word}' — \
                         this script uses '{word}', which is very likely the real cause \
                         of the syntax error below."
                    ),
                    severity: "error",
                });
            }

            diags.push(LintDiagnostic {
                line: orig_line,
                col,
                message: e.0.to_string(),
                severity: "error",
            });
        }
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
        ta_vars,
        ta_functions: KNOWN_TA_FUNCS.to_vec(),
    };

    (diags, scope)
}

// ── Internal helpers ──────────────────────────────────────────────────────────

/// Comparison operators that only make sense between two scalars — never
/// between two `Array`s. `cross_above(a, b)`/`rising(a)`/`highest(arr, n)`/etc.
/// all take the BARE array on purpose (they index internally), so only flag a
/// name when it's directly adjacent to one of these, not merely "not `[`".
const COMPARISON_OPS: &[&str] = &[">=", "<=", "==", "!=", ">", "<"];

/// Scan a single source line for a declared `ind.*`/`ta.*` variable compared
/// bare (no `[index]`) — e.g. `if fast > slow` instead of `if fast[0] > slow[0]`.
///
/// Every `ind.*`/`ta.*` declaration is an `Array` (single- or multi-output
/// alike — `ta.*` buffers into a small array the same as `ind.*`, see
/// `rewrite_ta_line`'s injected trailing `buf` arg). Rhai is dynamically
/// typed, so `fast > slow` compiles fine and is syntactically indistinguishable
/// from a real bug until runtime, where it silently never matches — a
/// strategy that compiles clean and never trades. Deliberately narrow: only
/// flags a name directly touching a comparison operator, NOT every bare
/// occurrence — being passed whole to `cross_above`/`highest`/`avg`/etc. is
/// the normal, correct way to use these builtins and must not be flagged.
fn field_access_missing_index(
    line: &str,
    lineno: usize,
    array_names: &std::collections::HashSet<&str>,
) -> Vec<LintDiagnostic> {
    let mut diags = Vec::new();
    let bytes = line.as_bytes();
    for &name in array_names {
        let mut search_from = 0usize;
        while let Some(rel) = line[search_from..].find(name) {
            let start = search_from + rel;
            let end = start + name.len();

            // Whole-word boundary check: neither neighbor may be an
            // identifier char, or this is a substring hit inside a longer
            // name (e.g. `fast_ma` contains `fast`).
            let before_ok = start == 0 || !is_ident_char(bytes[start - 1]);
            let after_ok = end >= bytes.len() || !is_ident_char(bytes[end]);

            if before_ok && after_ok {
                let already_indexed = line[end..].trim_start().starts_with('[');
                let touches_comparison = !already_indexed && (
                    COMPARISON_OPS.iter().any(|op| line[end..].trim_start().starts_with(op))
                    || COMPARISON_OPS.iter().any(|op| line[..start].trim_end().ends_with(op))
                );
                if touches_comparison {
                    diags.push(LintDiagnostic {
                        line: lineno,
                        col: start + 1,
                        message: format!(
                            "'{name}' is an Array (ind.*/ta.* declarations always are) — \
                             use '{name}[0]' for the current value, not '{name}' bare, \
                             in a comparison."
                        ),
                        severity: "error",
                    });
                }
            }
            search_from = end;
        }
    }
    diags
}

fn is_ident_char(b: u8) -> bool {
    b.is_ascii_alphanumeric() || b == b'_'
}

/// First standalone `and`/`or`/`not` word (whole-word, not `android`/`orb`/
/// `nothing`) found anywhere in the script — used only as a hint trigger
/// alongside a real Rhai compile error, not a standalone diagnostic on its
/// own (a script can legally contain these as substrings of identifiers, or
/// even use `and`/`or`/`not` as plain variable names — only relevant here
/// because compilation already failed for some other reason).
fn first_python_boolean_keyword(text: &str) -> Option<&'static str> {
    let bytes = text.as_bytes();
    for word in ["and", "or", "not"] {
        let mut search_from = 0usize;
        while let Some(rel) = text[search_from..].find(word) {
            let start = search_from + rel;
            let end = start + word.len();
            let before_ok = start == 0 || !is_ident_char(bytes[start - 1]);
            let after_ok = end >= bytes.len() || !is_ident_char(bytes[end]);
            if before_ok && after_ok {
                return Some(word);
            }
            search_from = end;
        }
    }
    None
}

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

/// `kalman` is the one indicator with NO positional-arg mapping at all (see
/// `positional_param_names` — every other type maps `ind.TYPE(period, N, ...)`
/// positionals to real param names by index; kalman's list is empty). A user
/// reasonably tries the same all-positional style that works everywhere else
/// (`ind.kalman(0.001, 0.001, 1)`) — those extra positional tokens are
/// silently dropped with zero feedback, and kalman quietly falls back to its
/// defaults (`q_pos=0.001, q_vel=0.001, r=1.0`) instead of whatever the user
/// actually typed. Only `q_pos=`/`q_vel=`/`r=` named params are ever read.
fn check_kalman_positional_misuse(
    var_name: &str,
    ind_type: &str,
    line: &str,
    lineno: usize,
) -> Option<LintDiagnostic> {
    if ind_type != "kalman" { return None; }
    let start = line.find("ind.kalman(")? + "ind.kalman(".len();
    let rest  = &line[start..];
    let end   = rest.find(')')?;
    let args_inner = &rest[..end];

    // Skip the first (period) slot — kalman ignores it, no diagnostic needed
    // for that one specifically. Any LATER arg that isn't `name=value` is a
    // silently-dropped positional value.
    let has_dropped_positional = args_inner.split(',').skip(1).any(|tok| !tok.contains('='));
    if !has_dropped_positional { return None; }

    Some(LintDiagnostic {
        line: lineno, col: 1,
        message: format!(
            "'{var_name}': kalman only reads q_pos/q_vel/r as NAMED params \
             (e.g. ind.kalman(1, q_pos=0.001, q_vel=0.001, r=1.0)) — bare \
             positional numbers after the first placeholder are silently \
             ignored, so kalman is using its defaults instead of these values."
        ),
        severity: "warning",
    })
}

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

/// Flag extra-param keys that don't match this indicator's known param names.
///
/// `extra` is keyed by name for both `name=value` tokens (typed verbatim by the
/// user) and positional numeric args (mapped via `positional_param_names` during
/// parsing — always a valid name, or silently dropped if it overflows the
/// expected arity). So any key surviving into `extra` that ISN'T in
/// `positional_param_names(ind_type)` can only have come from a typo'd
/// `name=value` token — e.g. `ind.macd(12, slwo=26)` stores `"slwo": 26.0`,
/// which the relational check in [`check_indicator_params`] never sees (it
/// looks up `"slow"`), so the typo silently falls back to the default value
/// with no diagnostic at all. This check closes that hole.
fn check_unknown_param_keys(
    var_name: &str,
    ind_type: &str,
    extra: &std::collections::HashMap<String, f64>,
    lineno: usize,
) -> Vec<LintDiagnostic> {
    // Two DIFFERENT sources of "known param name", both legitimate:
    // - `positional_param_names`: names positional (non-`name=value`) args
    //   map to by index — meaningless for a type with no positional mapping.
    // - the real catalog `params` list: every param the constructor actually
    //   reads, regardless of how it's supplied.
    // `kalman` is the case where these diverge completely: it has ZERO
    // positional mapping (its period slot is a dummy, ignored placeholder),
    // but DOES have 3 real named-only params (`q_pos`/`q_vel`/`r`) — using
    // `positional_param_names` alone flagged every correct, legitimate
    // `q_pos=`/`q_vel=`/`r=` usage as "unknown param".
    let positional_known = positional_param_names(ind_type);
    let catalog_known: Vec<&str> = crate::catalog::all().into_iter()
        .find(|m| m.name == ind_type)
        .map(|m| m.params.iter().map(|p| p.name).collect())
        .unwrap_or_default();
    let mut diags = Vec::new();
    for key in extra.keys() {
        let known_here = positional_known.contains(&key.as_str()) || catalog_known.contains(&key.as_str());
        if !known_here {
            let all_known: Vec<&str> = positional_known.iter().copied().chain(catalog_known.iter().copied()).collect();
            let suggestion = if all_known.is_empty() {
                format!("'{ind_type}' takes no extra named params besides 'period'.")
            } else {
                let closest = all_known.iter()
                    .min_by_key(|k| edit_distance(key, k))
                    .copied()
                    .unwrap_or("");
                format!("did you mean '{closest}'? Known params for '{ind_type}': {}.", all_known.join(", "))
            };
            diags.push(LintDiagnostic {
                line: lineno, col: 1,
                message: format!("'{var_name}': unknown param '{key}' for indicator '{ind_type}' — {suggestion}"),
                severity: "error",
            });
        }
    }
    diags
}

/// Flag a top-level assignment to a name that looks like a typo'd output var
/// (`stop_loss = ...` instead of `sl = ...`) — Rhai allows assigning to any
/// undeclared name (creates it as a fresh local, no error), so a typo here
/// silently drops the user's intent with zero diagnostic. Deliberately only
/// warns on a *close* match to a real `OUTPUT_VARS` entry — a name with no
/// resemblance to any of them is far more likely a genuine scratch variable
/// than a typo, and flagging those would be pure noise.
/// Semantic-name collisions with other frameworks (Backtrader/Freqtrade/
/// TradingView) — the real name is not a *typo* of these (very different
/// length/spelling), so `edit_distance` can't find them; this is the same
/// class of gap as an indicator-name alias table (`"bollinger"` vs
/// `"bbands"`). Checked before falling back to edit-distance so a genuine
/// typo of the ALIAS itself (`stop_los =`) still resolves through here too.
const OUTPUT_VAR_ALIASES: &[(&str, &str)] = &[
    ("stop_loss", "sl"), ("stoploss", "sl"),
    ("take_profit", "tp"), ("takeprofit", "tp"),
    ("trailing_stop", "trail"), ("trailing", "trail"),
    ("signal_strength", "strength"),
];

fn check_unrecognized_assignment(
    line: &str,
    lineno: usize,
    decl_names: &std::collections::HashSet<String>,
) -> Vec<LintDiagnostic> {
    let mut diags = Vec::new();
    let trimmed = line.trim();
    let Some(eq_pos) = find_bare_assignment_eq(trimmed) else { return diags; };
    let name = trimmed[..eq_pos].trim();
    if name.is_empty() || !name.chars().all(|c| c.is_alphanumeric() || c == '_') {
        return diags; // not a bare single identifier (`state["x"] =`, `foo.bar =`, ...)
    }
    if name.chars().next().is_some_and(|c| c.is_ascii_digit()) {
        return diags;
    }
    if OUTPUT_VARS.contains(&name) || decl_names.contains(name) {
        return diags;
    }

    let alias_hit = OUTPUT_VAR_ALIASES.iter()
        .find(|(alias, _)| edit_distance(name, alias) <= 2)
        .map(|(_, real)| *real);
    let closest = alias_hit.or_else(|| {
        let c = OUTPUT_VARS.iter().min_by_key(|v| edit_distance(name, v)).copied().unwrap_or("");
        (!c.is_empty() && edit_distance(name, c) <= 2).then_some(c)
    });

    if let Some(closest) = closest {
        diags.push(LintDiagnostic {
            line: lineno, col: 1,
            message: format!(
                "'{name}' is not a real output var — did you mean '{closest}'? \
                 Assigning to an unrecognized name silently creates a local variable \
                 Rhai never reads back."
            ),
            severity: "warning",
        });
    }
    diags
}

/// Find the byte position of a bare `=` (assignment) at the start of a
/// trimmed line — not `==`/`!=`/`<=`/`>=`, and only when everything before it
/// is a single identifier token, so `if x == y {` or a mid-expression `==`
/// never match. Deliberately single-line-only: `if x { sl = 100; }` on one
/// line won't be caught (bails at `{`) — real scripts in this DSL are
/// overwhelmingly formatted one statement per line, so this covers the
/// common case without the complexity of a real brace-aware scanner.
fn find_bare_assignment_eq(trimmed: &str) -> Option<usize> {
    let bytes = trimmed.as_bytes();
    for (i, &b) in bytes.iter().enumerate() {
        if b == b'=' {
            let prev = if i > 0 { Some(bytes[i - 1]) } else { None };
            let next = bytes.get(i + 1).copied();
            if next == Some(b'=') { return None; } // `==`
            if matches!(prev, Some(b'!') | Some(b'<') | Some(b'>') | Some(b'=')) { return None; }
            return Some(i);
        }
        if !(b.is_ascii_alphanumeric() || b == b'_' || b == b' ' || b == b'\t') {
            return None;
        }
    }
    None
}

/// Flag `.field` access on a multi-output indicator where `field` isn't one of
/// its real output fields (e.g. `macd12[0].histogram12321` — typo of `.histogram`).
///
/// Mirrors [`field_access_on_scalar`]'s bracket-scan, but instead of treating
/// *any* field access as invalid, cross-checks the accessed name against the
/// indicator's real `outputs` (from [`crate::catalog::all`] — the same static
/// list kept in sync with `alm_indicator::field_names()` by the
/// `catalog_matches_engine_indicators` guard test). Rhai's dynamic typing means
/// `macd12[0].histogram12321` is valid AST but returns unit/nothing at runtime —
/// silently, since `ScriptStrategy::on_bar` swallows the resulting error.
fn field_access_wrong_name(
    line: &str,
    lineno: usize,
    multi_fields: &std::collections::HashMap<&str, &[&str]>,
) -> Vec<LintDiagnostic> {
    let mut diags = Vec::new();
    for (&name, fields) in multi_fields {
        let needle = format!("{name}[");
        let mut search = line;
        let mut col_offset = 0usize;
        while let Some(start) = search.find(&needle) {
            let after_open = &search[start + needle.len()..];
            if let Some(close) = after_open.find(']') {
                let after_close = &after_open[close + 1..];
                let trimmed_after = after_close.trim_start();
                if trimmed_after.starts_with('.') {
                    let field: String = trimmed_after[1..]
                        .chars()
                        .take_while(|c| c.is_alphanumeric() || *c == '_')
                        .collect();
                    if !fields.contains(&field.as_str()) {
                        // Prefer a prefix match first — typos here are usually garbage
                        // appended/truncated off a real field name (e.g. `histogram12321`
                        // or `histo`), which a length-capped edit_distance scores poorly on
                        // since it short-circuits once the length delta exceeds 3.
                        let closest = fields.iter()
                            .find(|f| field.starts_with(*f) || f.starts_with(&field))
                            .or_else(|| fields.iter().min_by_key(|f| edit_distance(&field, f)))
                            .copied()
                            .unwrap_or("");
                        diags.push(LintDiagnostic {
                            line: lineno,
                            col: col_offset + start + 1,
                            message: format!(
                                "'{name}' has no field '.{field}' — did you mean '.{closest}'? \
                                 Valid fields for '{name}': {}.",
                                fields.join(", ")
                            ),
                            severity: "error",
                        });
                    }
                }
            }
            col_offset += start + needle.len();
            search = &search[start + needle.len()..];
        }
    }
    diags
}

/// Rhai keywords/literals — never a variable name, must be excluded from the
/// undeclared-read scan below or `if`/`true`/etc. would look exactly like a
/// bare identifier read of something that was never declared.
const RHAI_KEYWORDS: &[&str] = &[
    "let", "const", "if", "else", "while", "loop", "for", "in",
    "continue", "break", "return", "fn", "true", "false",
    "throw", "try", "catch", "switch", "do", "until",
];

/// Names always present in the Rhai `Scope` before a script ever runs — see
/// the `scope.push(...)` calls in `v1::strategy.rs` / `v2::strategy.rs`
/// (`BAR_FIELDS`, every signal/regime output var, plus the `state`/`ta` state
/// maps). A bare read of any of these is always valid regardless of `let`.
const ALWAYS_AVAILABLE: &[&str] = &[
    "open", "high", "low", "close", "volume",
    "long", "entry", "short", "exit",
    "tp", "sl", "is_offset", "strength", "reason", "atr", "trail", "max_bars",
    "trend", "trend_value", "vol", "vol_value",
    "state", "ta",
];

/// Blank out string-literal contents and `//` line comments (replacing with
/// spaces, same byte length) so the identifier scan in
/// [`scan_undeclared_reads`] never trips on text that isn't real code —
/// `reason = "entry_price crossed"` or `// check fast > slow` must not be
/// scanned as variable reads. Falls back to the untouched line if masking
/// ever produces invalid UTF-8 (only possible via multi-byte chars split by
/// the byte-level masking inside an unterminated string) — a rare
/// degrade-to-unmasked edge case, not a panic.
fn mask_noise(line: &str) -> String {
    let bytes = line.as_bytes();
    let mut out = vec![0u8; bytes.len()];
    let mut in_string = false;
    let mut i = 0usize;
    while i < bytes.len() {
        let b = bytes[i];
        if in_string {
            if b == b'\\' && i + 1 < bytes.len() {
                out[i] = b' ';
                out[i + 1] = b' ';
                i += 2;
                continue;
            }
            if b == b'"' {
                out[i] = b'"';
                in_string = false;
            } else {
                out[i] = b' ';
            }
        } else if b == b'"' {
            out[i] = b'"';
            in_string = true;
        } else if b == b'/' && i + 1 < bytes.len() && bytes[i + 1] == b'/' {
            for slot in out.iter_mut().skip(i) { *slot = b' '; }
            break;
        } else {
            out[i] = b;
        }
        i += 1;
    }
    String::from_utf8(out).unwrap_or_else(|_| line.to_string())
}

/// Scan one non-declaration line for a bare identifier read that isn't in
/// `available` (everything `let`-declared so far, plus [`ALWAYS_AVAILABLE`]).
/// Skips: Rhai keywords, `.field` access targets (preceded by `.`), and
/// function-call names (followed by `(`) — a name immediately before `(` is
/// always a call (builtin or user `fn`), never a variable read.
fn scan_undeclared_reads(
    line: &str,
    lineno: usize,
    available: &std::collections::HashSet<String>,
    all_declared: &std::collections::HashSet<String>,
) -> Vec<LintDiagnostic> {
    let mut diags = Vec::new();
    let masked = mask_noise(line);
    let bytes = masked.as_bytes();
    let mut i = 0usize;
    while i < bytes.len() {
        if bytes[i].is_ascii_alphabetic() || bytes[i] == b'_' {
            let start = i;
            while i < bytes.len() && is_ident_char(bytes[i]) { i += 1; }
            let name = &masked[start..i];

            if RHAI_KEYWORDS.contains(&name) { continue; }
            if start > 0 && bytes[start - 1] == b'.' { continue; } // `.field`
            if masked[i..].trim_start().starts_with('(') { continue; } // call
            if available.contains(name) { continue; }

            let message = if all_declared.contains(name) {
                format!(
                    "'{name}' is read here but its `let {name} = ...;` declaration is \
                     further down in the script — Rhai resolves names at the point each \
                     line runs (no hoisting), so this line reads an undeclared variable \
                     every bar until execution reaches that declaration further down. \
                     Move the declaration above this line."
                )
            } else {
                format!(
                    "'{name}' is never declared anywhere in this script (no \
                     `let {name} = ...;`) — this will error or silently read as nothing \
                     at runtime. Typo, or a missing declaration?"
                )
            };
            diags.push(LintDiagnostic { line: lineno, col: start + 1, message, severity: "error" });
            continue;
        }
        i += 1;
    }
    diags
}

/// Parse a plain scratch `let NAME = ...;` line's bare identifier `NAME` —
/// only for ordinary local variables, NOT `ind.*`/`ta.*` declarations (those
/// are parsed and tracked separately via `try_parse_indicator_line`/
/// `rewrite_ta_line`, which callers must check first). Returns `None` for
/// anything that isn't a simple `let <ident> = ` prefix (destructuring,
/// missing `=`, etc. — Rhai supports very little of that anyway in this DSL).
fn bare_let_scratch_name(trimmed: &str) -> Option<&str> {
    let rest = trimmed.strip_prefix("let ")?.trim_start();
    let eq_pos = rest.find('=')?;
    let name = rest[..eq_pos].trim();
    if name.is_empty() { return None; }
    if !name.chars().next().is_some_and(|c| c.is_ascii_alphabetic() || c == '_') { return None; }
    if !name.chars().all(|c| c.is_ascii_alphanumeric() || c == '_') { return None; }
    Some(name)
}

/// Flag any bare identifier read that was never `let`-declared before this
/// point in the script (or never declared at all). Self-contained: re-parses
/// declaration lines with the same pure helpers the main loop already uses,
/// so it never has to touch (or risk breaking) that loop's own bookkeeping.
///
/// Order sensitivity differs by declaration kind — confirmed against real
/// execution in `v1::strategy::process_script_block`/`ScriptStrategy::on_bar`,
/// not assumed:
/// - `ind.*` declarations are NEVER order-sensitive: `try_parse_indicator_line`
///   pulls the whole line out of what Rhai actually executes — the indicator
///   is bound into `Scope` for every declared name, from ANYWHERE in the
///   script, before the first line of real script code ever runs. A read of
///   `rsi14[0]` works identically whether `let rsi14 = ind.rsi(14);` sits
///   above it, below it, or in a completely different block (regime vs
///   main). So `ind_names` is seeded into `available` up front, unconditionally.
/// - `ta.*` declarations ARE genuinely order-sensitive: `rewrite_ta_line`
///   keeps the (rewritten) line as real sequential Rhai code, so a `ta.*`
///   binding only exists in scope once execution actually reaches that line.
/// - Plain scratch `let` vars are likewise genuinely order-sensitive, and are
///   NOT shared across regime/main blocks — only the regime OUTPUT vars
///   (`trend`/`vol`/etc., already in [`ALWAYS_AVAILABLE`]) cross into main.
fn check_undeclared_reads(
    regime_body: Option<&str>,
    main_source: &str,
    decl_names: &std::collections::HashSet<String>,
    ind_names: &std::collections::HashSet<String>,
) -> Vec<LintDiagnostic> {
    use std::collections::HashSet;

    // Pass 1: collect every plain scratch `let` name declared anywhere (both
    // blocks) — used only to word the diagnostic ("declared later" vs "never
    // declared"), not for the order-sensitive availability check below.
    let mut all_scratch: HashSet<String> = HashSet::new();
    for body in regime_body.into_iter().chain(std::iter::once(main_source)) {
        for line in body.lines() {
            let trimmed = line.trim();
            if trimmed.is_empty() { continue; }
            if try_parse_indicator_line(trimmed).is_some() { continue; }
            if matches!(rewrite_ta_line(trimmed), Ok(Some(_))) { continue; }
            if let Some(name) = bare_let_scratch_name(trimmed) {
                all_scratch.insert(name.to_string());
            }
        }
    }
    let all_declared: HashSet<String> = decl_names.iter().cloned().chain(all_scratch).collect();

    // `ind.*` names are always available, regardless of where in the script
    // (or which block) they were declared — see the doc comment above.
    let mut available: HashSet<String> = ALWAYS_AVAILABLE.iter().map(|s| s.to_string()).collect();
    available.extend(ind_names.iter().cloned());
    let mut diags = Vec::new();

    // Pass 2: regime block first — its ind./ta. declarations feed into
    // `available` for the main-body pass below too (shared namespace); its
    // own scratch vars stay local to a `regime_available` copy.
    if let Some(body) = regime_body {
        let mut regime_available = available.clone();
        for (idx, line) in body.lines().enumerate() {
            let lineno = idx + 1;
            let trimmed = line.trim();
            if trimmed.is_empty() { continue; }
            if let Some(decl) = try_parse_indicator_line(trimmed) {
                regime_available.insert(decl.var_name.clone());
                available.insert(decl.var_name);
                continue;
            }
            match rewrite_ta_line(trimmed) {
                Ok(Some((var_name, _))) => {
                    regime_available.insert(var_name.clone());
                    available.insert(var_name);
                    continue;
                }
                Ok(None) => {}
                Err(_) => continue, // already reported by the main loop above
            }
            if let Some(name) = bare_let_scratch_name(trimmed) {
                // Insert BEFORE scanning: the LHS `name` itself appears as a
                // bare token in `line` too, and must not flag itself as an
                // undeclared read of its own not-yet-registered declaration.
                regime_available.insert(name.to_string());
                diags.extend(scan_undeclared_reads(line, lineno, &regime_available, &all_declared));
                continue;
            }
            diags.extend(scan_undeclared_reads(line, lineno, &regime_available, &all_declared));
        }
    }

    // Pass 3: main body, seeded with everything the regime block declared.
    for (idx, line) in main_source.lines().enumerate() {
        let lineno = idx + 1;
        let trimmed = line.trim();
        if trimmed.is_empty() { continue; }
        if let Some(decl) = try_parse_indicator_line(trimmed) {
            available.insert(decl.var_name);
            continue;
        }
        match rewrite_ta_line(trimmed) {
            Ok(Some((var_name, _))) => { available.insert(var_name); continue; }
            Ok(None) => {}
            Err(_) => continue,
        }
        if let Some(name) = bare_let_scratch_name(trimmed) {
            // Insert BEFORE scanning — see the matching comment in the
            // regime-block pass above.
            available.insert(name.to_string());
            diags.extend(scan_undeclared_reads(line, lineno, &available, &all_declared));
            continue;
        }
        diags.extend(scan_undeclared_reads(line, lineno, &available, &all_declared));
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

    #[test]
    fn lint_ta_ema_not_flagged_as_wrong_prefix() {
        let script = "let ema20 = ta.ema(20, close[0]);\nif ema20[0] > 0.0 { entry = true; }";
        let (diags, scope) = script_lint(script, None);
        assert!(diags.is_empty(), "unexpected diagnostics: {diags:#?}");
        assert_eq!(scope.ta_vars, vec!["ema20".to_string()]);
        assert!(scope.ta_functions.contains(&"ema"));
    }

    #[test]
    fn lint_ta_invalid_buf_is_error() {
        let script = "let ema20 = ta.ema(20, close[0], buf=0);\nif ema20[0] > 0.0 { entry = true; }";
        let (diags, _) = script_lint(script, None);
        assert!(diags.iter().any(|d| d.severity == "error" && d.message.contains("too small")), "{diags:#?}");
    }

    #[test]
    fn lint_ta_two_refs_one_line_is_error() {
        let script = "let x = ta.ema(9, close[0]) + ta.ema(20, close[0]);\nif x[0] > 0.0 { entry = true; }";
        let (diags, _) = script_lint(script, None);
        assert!(diags.iter().any(|d| d.severity == "error" && d.message.contains("only one")), "{diags:#?}");
    }

    #[test]
    fn lint_ta_nested_in_if_is_error() {
        let script = "if close[0] > 0.0 {\n    let ema20 = ta.ema(20, close[0]);\n}\n";
        let (diags, _) = script_lint(script, None);
        assert!(diags.iter().any(|d| d.severity == "error" && d.message.contains("top level")), "{diags:#?}");
    }

    #[test]
    fn lint_ta_duplicate_name_across_ind_and_ta_is_error() {
        let script = "let ema20 = ind.ema(20);\nlet ema20 = ta.ema(20, close[0]);\nif ema20[0] > 0.0 { entry = true; }";
        let (diags, _) = script_lint(script, None);
        assert!(diags.iter().any(|d| d.severity == "error" && d.message.contains("declared more than once")), "{diags:#?}");
    }

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

    #[test]
    fn lint_multi_field_typo_is_error() {
        let script = r#"
let macd12 = ind.macd(12);
if macd12[0].histogram12321 > 0.0 { entry = true; }
"#;
        let (errors, _) = script_lint(script, None);
        assert!(
            errors.iter().any(|e| e.severity == "error"
                && e.message.contains("macd12")
                && e.message.contains("histogram12321")
                && e.message.contains("did you mean '.histogram'")),
            "expected typo'd field error for macd12[0].histogram12321, got: {errors:?}"
        );
    }

    #[test]
    fn lint_multi_field_real_name_clean() {
        let script = r#"
let macd12 = ind.macd(12);
let dmi14  = ind.dmi(14);
if macd12[0].histogram > 0.0 && dmi14[0].plus_di > dmi14[0].minus_di { entry = true; }
"#;
        let (errors, _) = script_lint(script, None);
        let field_errs: Vec<_> = errors.iter().filter(|e| e.message.contains("has no field")).collect();
        assert!(field_errs.is_empty(), "real multi-output fields should not error: {field_errs:?}");
    }

    #[test]
    fn lint_unknown_param_key_is_error() {
        let script = r#"
let macd12 = ind.macd(12, slwo=26, signal=9);
if macd12[0].histogram > 0.0 { entry = true; }
"#;
        let (errors, _) = script_lint(script, None);
        assert!(
            errors.iter().any(|e| e.severity == "error"
                && e.message.contains("macd12")
                && e.message.contains("'slwo'")
                && e.message.contains("did you mean 'slow'")),
            "expected unknown param key error for slwo=26, got: {errors:?}"
        );
        assert!(
            !errors.iter().any(|e| e.message.contains("'signal'") && e.message.contains("unknown param")),
            "signal=9 is a real param and should not error, got: {errors:?}"
        );
    }

    #[test]
    fn lint_known_param_keys_clean() {
        let script = r#"
let macd12 = ind.macd(12, slow=26, signal=9);
let alli   = ind.alligator(13, teeth=8, lips=5);
if macd12[0].histogram > 0.0 && alli[0].bullish > 0.5 { entry = true; }
"#;
        let (errors, _) = script_lint(script, None);
        let param_errs: Vec<_> = errors.iter().filter(|e| e.message.contains("unknown param")).collect();
        assert!(param_errs.is_empty(), "real param keys should not error: {param_errs:?}");
    }

    // ── UX / robustness: script_lint feeds Monaco-style as-you-type validation
    // (POST /api/v1/script/validate) — called on every keystroke, including on
    // incomplete/malformed script text mid-edit. A panic here is a production
    // reliability bug (crashes the request), not just a bad diagnostic. These
    // tests assert `script_lint` never panics on partial/garbage input — they
    // deliberately do NOT assert on the diagnostic content, only that it returns.
    #[test]
    fn lint_never_panics_on_partial_or_malformed_input() {
        let inputs: &[&str] = &[
            "",
            " ",
            "\n\n\n",
            "let x = ind.",
            "let x = ind.ema(",
            "let x = ind.ema(20",
            "let x = ind.ema(20,",
            "let x = ind.ema(20, \"H1",
            "if cross_above(",
            "if cross_above(ema9,",
            "regime {",
            "regime { let x = ind.ema(9",
            "let x = ind.ema(9);\nif x[0] > 0.0 { entry = ",
            "candle.transform(",
            "// just a comment, no code",
            "}}}}}}}}}}",
            "((((((((((",
            "let x = ind.unknown_type_xyz(9);",
            "let x = ind.ema(-5);",
            "let x = ind.ema(9999999999999999999999);",
            "let x = ind.ema(9, buf=",
            "let 中文变量 = ind.ema(9);",
            "let x = ind.ema(9);\0null byte here",
            &"let x = ind.ema(9);\n".repeat(500),
            "let macd = ind.macd(12, slow=",
            "let macd = ind.macd(12, slow=abc);",
        ];
        for input in inputs {
            let result = std::panic::catch_unwind(|| script_lint(input, None));
            assert!(result.is_ok(), "script_lint panicked on input: {input:?}");
        }
    }

    /// Corpus test: every embedded RHAI_SCRIPT constant used elsewhere in the
    /// codebase (parity tests, named-strategy `.script()` impls) is a real,
    /// hand-written script that MUST lint clean. If one of these ever produces
    /// an unexpected error, either the linter has a false positive (bad UX —
    /// blocks a script that actually works) or the script itself regressed.
    #[test]
    fn lint_clean_on_known_good_named_strategy_scripts() {
        use alm_core::strategy::Strategy;
        let ma_cross = crate::named::MaCrossover::new(20, 50);
        let scripts: Vec<(&str, &str)> = vec![
            ("MaCrossover", ma_cross.script().expect("MaCrossover exposes a RHAI_SCRIPT")),
        ];
        for (label, script) in scripts {
            let (errors, _) = script_lint(script, None);
            let hard_errors: Vec<_> = errors.iter().filter(|e| e.severity == "error").collect();
            assert!(
                hard_errors.is_empty(),
                "'{label}' embedded script should lint clean, got errors: {hard_errors:?}\nscript:\n{script}"
            );
        }
    }

    #[test]
    fn lint_bare_array_comparison_is_error() {
        let script = r#"
let fast = ind.ema(9);
let slow = ind.ema(21);
if fast > slow { entry = true; }
"#;
        let (errors, _) = script_lint(script, None);
        assert!(
            errors.iter().any(|e| e.severity == "error" && e.message.contains("'fast'") && e.message.contains("comparison")),
            "expected bare-array-comparison error for 'fast > slow', got: {errors:?}"
        );
        assert!(
            errors.iter().any(|e| e.severity == "error" && e.message.contains("'slow'")),
            "expected bare-array-comparison error for 'slow' too, got: {errors:?}"
        );
    }

    #[test]
    fn lint_indexed_comparison_is_clean() {
        let script = r#"
let fast = ind.ema(9);
let slow = ind.ema(21);
if fast[0] > slow[0] { entry = true; }
"#;
        let (errors, _) = script_lint(script, None);
        let array_errs: Vec<_> = errors.iter().filter(|e| e.message.contains("is an Array")).collect();
        assert!(array_errs.is_empty(), "indexed comparison should not error: {array_errs:?}");
    }

    #[test]
    fn lint_bare_array_as_builtin_arg_is_clean() {
        let script = r#"
let fast = ind.ema(9);
let slow = ind.ema(21);
if cross_above(fast, slow) { entry = true; }
if rising(fast) { exit = true; }
let h = highest(fast, 5);
"#;
        let (errors, _) = script_lint(script, None);
        let array_errs: Vec<_> = errors.iter().filter(|e| e.message.contains("is an Array")).collect();
        assert!(array_errs.is_empty(), "bare array passed to a builtin should not error: {array_errs:?}");
    }

    #[test]
    fn lint_declaration_line_itself_not_flagged() {
        // The `let fast = ind.ema(9);` line's own LHS occurrence of `fast`
        // must never be flagged — it's a binding target, not a usage.
        let script = r#"
let fast = ind.ema(9);
if fast[0] > 0.0 { entry = true; }
"#;
        let (errors, _) = script_lint(script, None);
        let array_errs: Vec<_> = errors.iter().filter(|e| e.message.contains("is an Array")).collect();
        assert!(array_errs.is_empty(), "declaration line should never self-flag: {array_errs:?}");
    }

    #[test]
    fn lint_bare_multi_output_comparison_is_error() {
        let script = r#"
let macd12 = ind.macd(12);
if macd12 > 0.0 { entry = true; }
"#;
        let (errors, _) = script_lint(script, None);
        assert!(
            errors.iter().any(|e| e.severity == "error" && e.message.contains("'macd12'") && e.message.contains("Array")),
            "multi-output var used bare in a comparison should also be flagged, got: {errors:?}"
        );
    }

    #[test]
    fn lint_typo_output_var_assignment_suggests_real_name() {
        let script = r#"
let atr14 = ind.atr(14);
if atr14[0] > 0.0 {
entry = true;
stop_loss = atr14[0];
}
"#;
        let (errors, _) = script_lint(script, None);
        assert!(
            errors.iter().any(|e| e.severity == "warning"
                && e.message.contains("'stop_loss'")
                && e.message.contains("did you mean 'sl'")),
            "expected typo suggestion for stop_loss -> sl, got: {errors:?}"
        );
    }

    #[test]
    fn lint_assignment_with_no_close_output_var_stays_clean() {
        // "position_size" has no close match among OUTPUT_VARS (sizing isn't
        // authored in-script at all) — must not false-positive a suggestion.
        let script = r#"
let ema9 = ind.ema(9);
if ema9[0] > 0.0 {
entry = true;
position_size = 1.0;
}
"#;
        let (errors, _) = script_lint(script, None);
        let warns: Vec<_> = errors.iter().filter(|e| e.message.contains("not a real output var")).collect();
        assert!(warns.is_empty(), "no close OUTPUT_VARS match should not warn: {warns:?}");
    }

    #[test]
    fn lint_real_output_var_assignment_stays_clean() {
        let script = r#"
let ema9 = ind.ema(9);
if ema9[0] > 0.0 {
entry = true;
sl = ema9[0];
tp = ema9[0];
}
"#;
        let (errors, _) = script_lint(script, None);
        let warns: Vec<_> = errors.iter().filter(|e| e.message.contains("not a real output var")).collect();
        assert!(warns.is_empty(), "real OUTPUT_VARS names should never warn: {warns:?}");
    }

    #[test]
    fn lint_reassigning_declared_indicator_var_not_flagged_as_typo() {
        // Reassigning a declared ind.*/ta.* var is a different (unrelated)
        // concern — this rule must not also fire on it.
        let script = r#"
let fast = ind.ema(9);
fast = fast;
if fast[0] > 0.0 { entry = true; }
"#;
        let (errors, _) = script_lint(script, None);
        let warns: Vec<_> = errors.iter().filter(|e| e.message.contains("not a real output var")).collect();
        assert!(warns.is_empty(), "reassigning a declared var should not trigger the typo check: {warns:?}");
    }

    #[test]
    fn lint_python_and_gets_a_friendly_hint_alongside_the_raw_rhai_error() {
        let script = r#"
let ema9 = ind.ema(9);
let rsi14 = ind.rsi(14);
if ema9[0] > 0.0 and rsi14[0] < 70.0 { entry = true; }
"#;
        let (errors, _) = script_lint(script, None);
        assert!(
            errors.iter().any(|e| e.message.contains("uses '&&'") && e.message.contains("'and'")),
            "expected a friendly and/or/not hint, got: {errors:?}"
        );
        // The raw Rhai parser error must still be present too — not replaced.
        assert!(
            errors.len() >= 2,
            "expected both the hint AND the original raw Rhai error, got: {errors:?}"
        );
    }

    #[test]
    fn lint_real_and_and_or_operators_no_false_positive_hint() {
        let script = r#"
let ema9 = ind.ema(9);
let rsi14 = ind.rsi(14);
if ema9[0] > 0.0 && rsi14[0] < 70.0 { entry = true; }
"#;
        let (errors, _) = script_lint(script, None);
        let hints: Vec<_> = errors.iter().filter(|e| e.message.contains("uses '&&'")).collect();
        assert!(hints.is_empty(), "real && should never trigger the and/or/not hint: {hints:?}");
    }

    #[test]
    fn lint_unrelated_compile_error_without_and_or_not_gets_no_hint() {
        let script = "let x = ind.ema(9\n"; // unclosed paren, no and/or/not anywhere
        let (errors, _) = script_lint(script, None);
        let hints: Vec<_> = errors.iter().filter(|e| e.message.contains("uses '&&'")).collect();
        assert!(hints.is_empty(), "compile error with no and/or/not present should not add the hint: {hints:?}");
    }

    // ── kalman positional-args regression (real user-reported bug) ──────────

    #[test]
    fn lint_kalman_all_positional_no_longer_falsely_flags_ind_as_undeclared() {
        // Real reported case: `ind.kalman(0.001, 0.001, 1)` — the non-integer
        // first arg used to make `try_parse_indicator_line` bail entirely via
        // `?`, so the declaration was never recognized and `check_undeclared_reads`
        // (correctly, given that failure) flagged bare `ind` as never declared.
        let script = "let k = ind.kalman(0.001, 0.001, 1);\n";
        let (errors, scope) = script_lint(script, None);
        let undeclared_ind = errors.iter().find(|e| e.message.contains("'ind'"));
        assert!(undeclared_ind.is_none(), "kalman declaration must be recognized, not fall through: {errors:?}");
        assert_eq!(scope.indicators.len(), 1, "kalman should register as a real declared indicator");
        assert_eq!(scope.indicators[0].name, "k");
    }

    #[test]
    fn lint_kalman_positional_args_warns_they_are_silently_dropped() {
        let script = "let k = ind.kalman(0.001, 0.001, 1);\n";
        let (errors, _) = script_lint(script, None);
        assert!(
            errors.iter().any(|e| e.severity == "warning"
                && e.message.contains("'k'")
                && e.message.contains("NAMED params")),
            "expected a warning steering the user to kalman's named-param syntax, got: {errors:?}"
        );
    }

    #[test]
    fn lint_kalman_named_params_is_clean() {
        let script = "let k = ind.kalman(1, q_pos=0.001, q_vel=0.001, r=1.0);\n";
        let (errors, _) = script_lint(script, None);
        let kalman_warnings: Vec<_> = errors.iter().filter(|e| e.message.contains("kalman")).collect();
        assert!(kalman_warnings.is_empty(), "correct named-param kalman usage should not warn: {kalman_warnings:?}");
    }

    #[test]
    fn lint_kalman_bare_period_only_is_clean() {
        // `ind.kalman(1)` — just the (ignored) period placeholder, no extra
        // args at all — must not warn; using every default is legitimate.
        let script = "let k = ind.kalman(1);\n";
        let (errors, _) = script_lint(script, None);
        let kalman_warnings: Vec<_> = errors.iter().filter(|e| e.message.contains("kalman")).collect();
        assert!(kalman_warnings.is_empty(), "bare period-only kalman usage should not warn: {kalman_warnings:?}");
    }

    // ── AST threshold extraction ─────────────────────────────────────────────

    #[test]
    fn lint_ast_threshold_single() {
        let script = "let rsi14 = ind.rsi(14);\nif rsi14[0] < 25.0 { entry = true; }\n";
        let (errors, scope) = script_lint(script, None);
        assert!(errors.is_empty(), "{errors:?}");
        let rsi = scope.indicators.iter().find(|i| i.name == "rsi14").unwrap();
        assert_eq!(rsi.thresholds, vec![ScriptThreshold { field: None, op: "<".to_string(), value: 25.0 }]);
    }

    #[test]
    fn lint_ast_threshold_reversed_literal_flips_operator() {
        // `25.0 > rsi14[0]` means the same thing as `rsi14[0] < 25.0` —
        // the recorded operator must be normalized to read "indicator <op> literal".
        let script = "let rsi14 = ind.rsi(14);\nif 25.0 > rsi14[0] { entry = true; }\n";
        let (errors, scope) = script_lint(script, None);
        assert!(errors.is_empty(), "{errors:?}");
        let rsi = scope.indicators.iter().find(|i| i.name == "rsi14").unwrap();
        assert_eq!(rsi.thresholds, vec![ScriptThreshold { field: None, op: "<".to_string(), value: 25.0 }]);
    }

    #[test]
    fn lint_ast_threshold_multiple_same_var() {
        let script = "let rsi14 = ind.rsi(14);\nif rsi14[0] < 25.0 { entry = true; }\nif rsi14[0] > 75.0 { exit = true; }\n";
        let (errors, scope) = script_lint(script, None);
        assert!(errors.is_empty(), "{errors:?}");
        let rsi = scope.indicators.iter().find(|i| i.name == "rsi14").unwrap();
        assert_eq!(rsi.thresholds.len(), 2);
        assert!(rsi.thresholds.contains(&ScriptThreshold { field: None, op: "<".to_string(), value: 25.0 }));
        assert!(rsi.thresholds.contains(&ScriptThreshold { field: None, op: ">".to_string(), value: 75.0 }));
    }

    #[test]
    fn lint_ast_threshold_cross_indicator_not_captured() {
        // Comparing two indicators to each other is not a threshold for
        // either side — must not be recorded on `rsi14` or `rsi7`.
        let script = "let rsi14 = ind.rsi(14);\nlet rsi7 = ind.rsi(7);\nif rsi14[0] < rsi7[0] { entry = true; }\n";
        let (errors, scope) = script_lint(script, None);
        assert!(errors.is_empty(), "{errors:?}");
        let rsi14 = scope.indicators.iter().find(|i| i.name == "rsi14").unwrap();
        let rsi7 = scope.indicators.iter().find(|i| i.name == "rsi7").unwrap();
        assert!(rsi14.thresholds.is_empty());
        assert!(rsi7.thresholds.is_empty());
    }

    #[test]
    fn lint_ast_threshold_multi_output_field_access() {
        let script = "let stoch1 = ind.stochastic(14);\nif stoch1[0].k < 20.0 { entry = true; }\n";
        let (errors, scope) = script_lint(script, None);
        assert!(errors.is_empty(), "{errors:?}");
        let stoch = scope.indicators.iter().find(|i| i.name == "stoch1").unwrap();
        assert_eq!(
            stoch.thresholds,
            vec![ScriptThreshold { field: Some("k".to_string()), op: "<".to_string(), value: 20.0 }]
        );
    }

    #[test]
    fn lint_ast_threshold_bare_multi_output_resolves_to_primary_field() {
        // `adx14[0] > 25.0` (no `.field` at all) on a multi-output type reads
        // its PRIMARY field at runtime (`IndicatorKind::Multi`'s doc comment
        // in `v1::parse`: `adx` → `.adx`) — the threshold must be attributed
        // to that field explicitly, not left as `field: None` (which would
        // read as "no field," ambiguous with a genuinely single-output type
        // like `rsi`).
        let script = "let adx14 = ind.adx(14);\nif adx14[0] > 25.0 { entry = true; }\n";
        let (errors, scope) = script_lint(script, None);
        assert!(errors.is_empty(), "{errors:?}");
        let adx = scope.indicators.iter().find(|i| i.name == "adx14").unwrap();
        assert_eq!(
            adx.thresholds,
            vec![ScriptThreshold { field: Some("adx".to_string()), op: ">".to_string(), value: 25.0 }]
        );
    }

    #[test]
    fn lint_ast_threshold_no_comparisons_is_empty() {
        let script = "let rsi14 = ind.rsi(14);\nentry = true;\n";
        let (errors, scope) = script_lint(script, None);
        assert!(errors.is_empty(), "{errors:?}");
        let rsi = scope.indicators.iter().find(|i| i.name == "rsi14").unwrap();
        assert!(rsi.thresholds.is_empty());
    }

    #[test]
    fn lint_ast_threshold_skipped_on_compile_error() {
        // Syntactically broken script — extraction must never be attempted
        // (the `Ok(ast)` branch never runs), and must not panic.
        let script = "let rsi14 = ind.rsi(14);\nif rsi14[0] < 25.0 { entry = true\n";
        let (errors, scope) = script_lint(script, None);
        assert!(!errors.is_empty());
        let rsi = scope.indicators.iter().find(|i| i.name == "rsi14").unwrap();
        assert!(rsi.thresholds.is_empty());
    }

    // ── Undeclared-read checks ──────────────────────────────────────────────

    #[test]
    fn lint_ta_read_before_declared_is_error() {
        // `ta.*` declarations ARE genuinely order-sensitive — `rewrite_ta_line`
        // keeps the rewritten line as real sequential Rhai code, unlike `ind.*`
        // (see the two tests below), so this one really is a runtime bug.
        let script = r#"
if fast[0] > 0.0 { entry = true; }
let fast = ta.ema(9, close[0]);
"#;
        let (errors, _) = script_lint(script, None);
        assert!(
            errors.iter().any(|e| e.severity == "error"
                && e.message.contains("'fast'")
                && e.message.contains("further down")),
            "expected a used-before-declared error for 'fast', got: {errors:?}"
        );
    }

    #[test]
    fn lint_ind_read_before_declared_in_same_body_is_clean() {
        // Unlike `ta.*`, an `ind.*` declaration is fully extracted from what
        // Rhai actually executes (`try_parse_indicator_line` — never pushed
        // to `cleaned_lines`) — every declared indicator is bound into
        // `Scope` before the FIRST line of real script code runs (see
        // `ScriptStrategy::on_bar`), regardless of where in the script the
        // `let x = ind.foo(...)` line sits. A read before it is real,
        // working code — must not be flagged.
        let script = r#"
if base_price[0] > 0.0 { entry = true; }
let base_price = ind.ema(9);
"#;
        let (errors, _) = script_lint(script, None);
        let undeclared: Vec<_> = errors.iter()
            .filter(|e| e.message.contains("'base_price'")
                && (e.message.contains("never declared") || e.message.contains("further down")))
            .collect();
        assert!(undeclared.is_empty(), "an ind.* read before its own declaration line is real, working code: {undeclared:?}");
    }

    #[test]
    fn lint_ind_declared_before_regime_block_visible_inside_regime_is_clean() {
        // Regression for a real false positive: declaring every indicator up
        // front (before `regime { }`), then reading them INSIDE the regime
        // block, is idiomatic and correct — `ind.*` names are never
        // order/block-sensitive (see the test above). The bug was that
        // `check_undeclared_reads` only seeded `available` incrementally
        // as it scanned, so regime-block reads never saw indicators that
        // were declared earlier in `main_source`, textually before the
        // regime block.
        let script = r#"
let bb = ind.bbands(20);
let atr14 = ind.atr(14);
regime {
    let bandwidth = bb[0].upper - bb[0].lower;
    if bandwidth < atr14[0] * 1.5 {
        vol = "squeeze";
    } else {
        vol = "expanded";
    }
}
if vol == "squeeze" { entry = true; }
"#;
        let (errors, _) = script_lint(script, None);
        let undeclared: Vec<_> = errors.iter()
            .filter(|e| (e.message.contains("'bb'") || e.message.contains("'atr14'"))
                && (e.message.contains("never declared") || e.message.contains("further down")))
            .collect();
        assert!(undeclared.is_empty(), "ind.* declared before the regime block must be visible inside it: {undeclared:?}");
    }

    #[test]
    fn lint_never_declared_read_is_error() {
        let script = r#"
let fast = ind.ema(9);
if fast[0] > daily_pnl { entry = true; }
"#;
        let (errors, _) = script_lint(script, None);
        assert!(
            errors.iter().any(|e| e.severity == "error"
                && e.message.contains("'daily_pnl'")
                && e.message.contains("never declared")),
            "expected a never-declared error for 'daily_pnl', got: {errors:?}"
        );
    }

    #[test]
    fn lint_declared_then_used_is_clean() {
        let script = r#"
let fast = ind.ema(9);
let slow = ind.ema(21);
let spread = fast[0] - slow[0];
if spread > 0.0 { entry = true; }
"#;
        let (errors, _) = script_lint(script, None);
        let undeclared: Vec<_> = errors.iter()
            .filter(|e| e.message.contains("never declared") || e.message.contains("further down"))
            .collect();
        assert!(undeclared.is_empty(), "properly-ordered declarations should not error: {undeclared:?}");
    }

    #[test]
    fn lint_bar_fields_and_context_vars_never_flagged() {
        let script = r#"
let range = high[0] - low[0];
if close[0] > open[0] && volume[0] > 0.0 { entry = true; }
state["seen"] = true;
let d = ta.decay(0.1, close[0]);
if trend == "up" && vol_value < 1.0 { long = true; }
"#;
        let (errors, _) = script_lint(script, None);
        let undeclared: Vec<_> = errors.iter()
            .filter(|e| e.message.contains("never declared") || e.message.contains("further down"))
            .collect();
        assert!(undeclared.is_empty(), "bar fields / context vars / state / ta must never be flagged: {undeclared:?}");
    }

    #[test]
    fn lint_builtin_function_calls_never_flagged_as_reads() {
        let script = r#"
let fast = ind.ema(9);
let slow = ind.ema(21);
if cross_above(fast, slow) && rising(fast) { entry = true; }
let h = highest(fast, 5);
"#;
        let (errors, _) = script_lint(script, None);
        let undeclared: Vec<_> = errors.iter()
            .filter(|e| e.message.contains("never declared") || e.message.contains("further down"))
            .collect();
        assert!(undeclared.is_empty(), "builtin function names must never be treated as variable reads: {undeclared:?}");
    }

    #[test]
    fn lint_regime_indicator_visible_in_main_but_regime_scratch_var_is_not() {
        let script = r#"
regime {
    let adx14 = ind.adx(14);
    let doubled = adx14[0].adx * 2.0;
    trend = "trending";
}
if adx14[0].adx > 20.0 { entry = true; }
if doubled > 0.0 { long = true; }
"#;
        let (errors, _) = script_lint(script, None);
        assert!(
            errors.iter().all(|e| !(e.message.contains("'adx14'") && (e.message.contains("never declared") || e.message.contains("further down")))),
            "regime-declared ind.* var must be visible in main body (shared namespace): {errors:?}"
        );
        assert!(
            errors.iter().any(|e| e.severity == "error" && e.message.contains("'doubled'")),
            "regime block's own scratch let must NOT leak into main body: {errors:?}"
        );
    }

    #[test]
    fn lint_identifier_inside_string_or_comment_never_flagged() {
        let script = r#"
let fast = ind.ema(9);
reason = "check daily_pnl before entry";
// daily_pnl should stay under budget
if fast[0] > 0.0 { entry = true; }
"#;
        let (errors, _) = script_lint(script, None);
        let undeclared: Vec<_> = errors.iter()
            .filter(|e| e.message.contains("'daily_pnl'"))
            .collect();
        assert!(undeclared.is_empty(), "identifiers inside a string literal or comment must never be scanned: {undeclared:?}");
    }

    #[test]
    fn lint_field_access_checks_never_scan_comments() {
        // Regression: `field_access_missing_index` (and its siblings
        // `field_access_on_scalar`/`field_access_wrong_name`) used to scan
        // the RAW line, unlike `scan_undeclared_reads` above which already
        // masks comments/strings first — a comment merely describing
        // pseudocode (`// bb < 30 ...`) was scanned as if it were real code,
        // tripping a false "use bb[0]" diagnostic.
        let script = r#"
let bb = ind.bbands(20);
// bb < 30 la vung qua ban, chi la ghi chu, khong phai code
if bb[0].lower > 0.0 { entry = true; }
"#;
        let (errors, _) = script_lint(script, None);
        let false_positives: Vec<_> = errors.iter()
            .filter(|e| e.message.contains("'bb'"))
            .collect();
        assert!(false_positives.is_empty(), "a comment describing pseudocode must not be scanned as real code: {false_positives:?}");
    }
}

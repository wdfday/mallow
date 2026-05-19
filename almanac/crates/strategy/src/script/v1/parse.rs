use anyhow::Result;
use alm_core::Timeframe;
use alm_indicator::IndicatorBox;
use serde_json::json;

use super::engine::DEFAULT_BUF_DEPTH;

// ── IndicatorKind ─────────────────────────────────────────────────────────────

/// Whether an indicator returns a single scalar or a multi-field map per bar.
#[derive(Clone, Debug)]
pub(crate) enum IndicatorKind {
    /// Extract one named field from the IndicatorBox output → `Array<f64>`.
    Single(String),
    /// Expose the full field map → `Array<MEntry>`.
    /// The `String` is the **primary field** returned when the entry is used
    /// directly as a number (e.g. `macd[0] > 0`  →  reads `.macd` field).
    Multi(String),
}

// ── IndicatorDecl ─────────────────────────────────────────────────────────────

pub(crate) struct IndicatorDecl {
    pub(crate) var_name:  String,
    pub(crate) ind_type:  String,
    pub(crate) period:    usize,
    pub(crate) buf_depth: usize,
    pub(crate) kind:      IndicatorKind,
    pub(crate) timeframe: Option<Timeframe>,
    pub(crate) live:      bool,
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
        "H12" => Some(Timeframe::H12),
        "D1"  => Some(Timeframe::D1),
        "W1"  => Some(Timeframe::W1),
        _     => None,
    }
}

// ── Regime block extractor ────────────────────────────────────────────────────

/// Locate a top-level `regime { ... }` block and return `(block_body, cleaned_script)`.
/// `block_body` is the code between the matching braces (excluding the braces themselves).
/// `cleaned_script` is the original script with the entire block (including `regime { … }`)
/// replaced by an empty line so line numbers remain stable for error reporting.
///
/// Only the first top-level `regime` block is extracted. Returns `(None, script.into())`
/// if no block is found. Returns an error if a `regime` keyword is found but braces
/// don't match.
pub(crate) fn extract_regime_block(script: &str) -> anyhow::Result<(Option<String>, String)> {
    // Strip line comments before scanning so commented-out `regime { ... }` is ignored.
    // We also need to preserve byte offsets for slicing, so scan the raw script byte-by-byte
    // and track an "in line comment" flag.
    let bytes = script.as_bytes();
    let mut i = 0;
    while i < bytes.len() {
        // Skip line comments.
        if i + 1 < bytes.len() && bytes[i] == b'/' && bytes[i + 1] == b'/' {
            while i < bytes.len() && bytes[i] != b'\n' { i += 1; }
            continue;
        }
        // Skip string literals (simplistic — does not handle escapes inside, but enough for our use).
        if bytes[i] == b'"' {
            i += 1;
            while i < bytes.len() && bytes[i] != b'"' { i += 1; }
            i = (i + 1).min(bytes.len());
            continue;
        }

        // Look for the keyword `regime` at a word boundary.
        if bytes[i] == b'r' && script[i..].starts_with("regime") {
            let prev_is_ident = i > 0 && is_ident_byte(bytes[i - 1]);
            let after = i + "regime".len();
            let next_non_ws = (after..bytes.len()).find(|&k| !bytes[k].is_ascii_whitespace());
            if !prev_is_ident && next_non_ws.map(|k| bytes[k] == b'{').unwrap_or(false) {
                let open = next_non_ws.unwrap();
                // Balanced-brace match.
                let mut depth = 0i32;
                let mut close: Option<usize> = None;
                let mut j = open;
                while j < bytes.len() {
                    // Skip line comments inside the block.
                    if j + 1 < bytes.len() && bytes[j] == b'/' && bytes[j + 1] == b'/' {
                        while j < bytes.len() && bytes[j] != b'\n' { j += 1; }
                        continue;
                    }
                    if bytes[j] == b'"' {
                        j += 1;
                        while j < bytes.len() && bytes[j] != b'"' { j += 1; }
                        j = (j + 1).min(bytes.len());
                        continue;
                    }
                    match bytes[j] {
                        b'{' => depth += 1,
                        b'}' => {
                            depth -= 1;
                            if depth == 0 { close = Some(j); break; }
                        }
                        _ => {}
                    }
                    j += 1;
                }
                let close = close.ok_or_else(|| anyhow::anyhow!(
                    "regime {{ … }} block has unbalanced braces"
                ))?;
                let body = script[open + 1..close].to_string();
                let mut cleaned = String::with_capacity(script.len());
                cleaned.push_str(&script[..i]);
                // Replace the entire `regime { ... }` span with line-preserving whitespace.
                for b in &bytes[i..=close] {
                    if *b == b'\n' { cleaned.push('\n'); }
                }
                cleaned.push_str(&script[close + 1..]);
                return Ok((Some(body), cleaned));
            }
        }
        i += 1;
    }
    Ok((None, script.to_string()))
}

fn is_ident_byte(b: u8) -> bool {
    b.is_ascii_alphanumeric() || b == b'_'
}

// ── Setup-directive extractor ─────────────────────────────────────────────────

/// A `candle.*` setup directive parsed from the top of a script.
#[derive(Debug, Clone, PartialEq)]
pub(crate) enum CandleDirective {
    /// `candle.transform("heiken_ashi")` or `candle.transform("smooth_ha", N)`.
    /// `kind` is the raw string passed by the user; `smooth` is the optional
    /// second argument (only meaningful for smooth_ha).
    Transform { kind: String, smooth: Option<usize> },
}

/// Scan the **top** of `script` for `candle.*` setup directives and strip
/// them from the body. Directives MUST appear before any other statement
/// (only blank lines and `//` comments may precede). The first non-directive,
/// non-blank, non-comment line closes the header; any later `candle.*`
/// directive is rejected as a parse error.
///
/// Returns `(directives, cleaned_script)`. `cleaned_script` is line-count
/// preserving so downstream error positions remain accurate.
pub(crate) fn extract_candle_directives(
    script: &str,
) -> anyhow::Result<(Vec<CandleDirective>, String)> {
    let mut directives = Vec::new();
    let mut out_lines: Vec<String> = Vec::with_capacity(script.lines().count());
    let mut in_header = true;

    for (idx, raw_line) in script.lines().enumerate() {
        let line_no = idx + 1;
        let stripped = strip_line_comment(raw_line).trim();

        // Blank / comment-only lines stay as-is in both header and body.
        if stripped.is_empty() {
            out_lines.push(raw_line.to_string());
            continue;
        }

        let is_directive = stripped.starts_with("candle.");
        if is_directive {
            if !in_header {
                anyhow::bail!(
                    "`candle.*` directive must appear at the top of the script, \
                     before any other statement (found at line {line_no}). \
                     Move it above all `let`, `regime`, and logic lines."
                );
            }
            let d = parse_candle_directive(stripped, line_no)?;
            directives.push(d);
            // Replace with blank line to keep line numbers stable.
            out_lines.push(String::new());
        } else {
            // Any non-directive, non-blank line closes the header.
            in_header = false;
            out_lines.push(raw_line.to_string());
        }
    }
    let mut cleaned = out_lines.join("\n");
    // `str::lines()` drops a trailing newline; preserve it so line-count math
    // stays exact for downstream error messages.
    if script.ends_with('\n') { cleaned.push('\n'); }
    Ok((directives, cleaned))
}

fn strip_line_comment(line: &str) -> &str {
    line.split("//").next().unwrap_or(line)
}

/// Parse one directive statement, e.g. `candle.transform("heiken_ashi");`.
fn parse_candle_directive(stmt: &str, line_no: usize) -> anyhow::Result<CandleDirective> {
    let s = stmt.trim().trim_end_matches(';').trim();
    let after_ns = s.strip_prefix("candle.").ok_or_else(|| {
        anyhow::anyhow!("line {line_no}: not a candle directive: `{stmt}`")
    })?;
    let lparen = after_ns.find('(').ok_or_else(|| {
        anyhow::anyhow!("line {line_no}: missing `(` in directive: `{stmt}`")
    })?;
    let rparen = after_ns.rfind(')').ok_or_else(|| {
        anyhow::anyhow!("line {line_no}: missing `)` in directive: `{stmt}`")
    })?;
    let method = after_ns[..lparen].trim();
    let args_src = &after_ns[lparen + 1..rparen];

    match method {
        "transform" => parse_transform_args(args_src, line_no),
        other => anyhow::bail!(
            "line {line_no}: unknown candle directive `candle.{other}(…)`. \
             Supported: candle.transform(\"heiken_ashi\" [, smooth_period])."
        ),
    }
}

fn parse_transform_args(args: &str, line_no: usize) -> anyhow::Result<CandleDirective> {
    let parts: Vec<&str> = args.split(',').map(str::trim).collect();
    if parts.is_empty() || parts[0].is_empty() {
        anyhow::bail!(
            "line {line_no}: candle.transform() needs a kind argument, \
             e.g. candle.transform(\"heiken_ashi\")."
        );
    }
    let kind = parts[0].trim_matches('"').trim_matches('\'').to_string();
    let smooth = match parts.get(1) {
        None    => None,
        Some(s) => Some(s.parse::<usize>().map_err(|_| {
            anyhow::anyhow!("line {line_no}: smooth period must be a positive integer, got `{s}`")
        })?),
    };
    Ok(CandleDirective::Transform { kind, smooth })
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

        // ── Multi-output: Array<MEntry> ──────────────────────────────────────
        // Primary = the one field that makes sense when used as a plain number.

        // fields: .macd  .signal  .histogram  — MACD line is the primary output
        "macd" => ("macd".to_string(), Multi("macd".to_string())),

        // fields: .adx  .plus_di  .minus_di   — ADX is the trend-strength value
        "adx"  => ("adx".to_string(),  Multi("adx".to_string())),
        // fields: .plus_di  .minus_di  .dx     — positive DI is the bullish leg
        "dmi"  => ("dmi".to_string(),  Multi("plus_di".to_string())),

        // fields: .upper  .middle  .lower  .bandwidth  .percent_b
        "bbands"   => ("bbands".to_string(),   Multi("middle".to_string())),
        // fields: .upper  .middle  .lower
        "keltner"  => ("keltner".to_string(),  Multi("middle".to_string())),
        "donchian" => ("donchian".to_string(), Multi("middle".to_string())),

        // fields: .k  .d  — %K is the fast (primary) stochastic line
        "stochastic" => ("stochastic".to_string(), Multi("k".to_string())),
        "stoch_rsi"  => ("stoch_rsi".to_string(),  Multi("k".to_string())),

        // fields: .k  .d  .j
        "kdj" => ("kdj".to_string(), Multi("k".to_string())),

        // fields: .value  .bullish  — price-level stop/trend line
        "supertrend"    => ("supertrend".to_string(),    Multi("value".to_string())),
        // fields: .sar  .bullish    — price-level stop-and-reverse
        "parabolic_sar" => ("parabolic_sar".to_string(), Multi("sar".to_string())),

        // fields: .up  .down  .oscillator  — net momentum = up − down
        "aroon" => ("aroon".to_string(), Multi("oscillator".to_string())),
        // fields: .plus_vi  .minus_vi      — bullish vortex indicator
        "vortex" => ("vortex".to_string(), Multi("plus_vi".to_string())),

        // fields: .trix  .signal  .histogram
        "trix" => ("trix".to_string(), Multi("trix".to_string())),
        // fields: .ppo  .signal  .histogram
        "ppo"  => ("ppo".to_string(),  Multi("ppo".to_string())),
        // fields: .kst  .signal  .histogram
        "kst"  => ("kst".to_string(),  Multi("kst".to_string())),
        // fields: .pmo  .signal  .histogram
        "pmo"  => ("pmo".to_string(),  Multi("pmo".to_string())),
        // fields: .rvi  .signal
        "rvi"  => ("rvi".to_string(),  Multi("rvi".to_string())),
        // fields: .smi  .signal
        "smi"  => ("smi".to_string(),  Multi("smi".to_string())),
        // fields: .fisher  .signal
        "fisher" => ("fisher".to_string(), Multi("fisher".to_string())),

        // fields: .rwi_high  .rwi_low  — high RWI confirms uptrend
        "rwi" => ("rwi".to_string(), Multi("rwi_high".to_string())),

        // fields: .tenkan  .kijun  .senkou_a  .senkou_b  .chikou  .above_cloud
        "ichimoku" => ("ichimoku".to_string(), Multi("tenkan".to_string())),
        // fields: .jaw  .teeth  .lips  .bullish  — teeth = 8-bar balance line
        "alligator" => ("alligator".to_string(), Multi("teeth".to_string())),
        // fields: .short_avg  .long_avg  .bullish  — long group defines the trend
        "gmma" => ("gmma".to_string(), Multi("long_avg".to_string())),
        // fields: .value  .slope  — filtered price estimate
        "kalman" => ("kalman".to_string(), Multi("value".to_string())),

        // fields: .bull  .bear  .ema  — bull power = high − EMA
        "bull_bear_power"  => ("bull_bear_power".to_string(),  Multi("bull".to_string())),
        // fields: .long_stop  .short_stop  .atr  — long-side chandelier stop
        "chandelier_exit"  => ("chandelier_exit".to_string(),  Multi("long_stop".to_string())),
        // fields: .stop_long  .stop_short  — long-side Chande-Kroll stop
        "chande_kroll_stop" => ("chande_kroll_stop".to_string(), Multi("stop_long".to_string())),
        // fields: .bullish  .bearish  .fractal_high  .fractal_low
        "william_fractal" => ("william_fractal".to_string(), Multi("bullish".to_string())),
        // fields: .angle  .zone  — zone is the color-coded trend zone
        "chop_zone" => ("chop_zone".to_string(), Multi("zone".to_string())),

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
        assert!(matches!(d.kind, IndicatorKind::Multi(_)));

        let d = try_parse_indicator_line(r#"let bb = ind.bbands(20);"#).unwrap();
        assert_eq!(d.ind_type, "bbands");
        assert!(matches!(d.kind, IndicatorKind::Multi(_)));
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

    #[test]
    fn extract_regime_block_basic() {
        let script = r#"
let ema9 = ind.ema(9);

regime {
    let adx14 = ind.adx(14);
    if adx14[0].adx > 25.0 { trend = "trending"; trend_value = adx14[0].adx; }
}

if cross_above(ema9, ema21) { entry = true; }
"#;
        let (body, cleaned) = extract_regime_block(script).unwrap();
        let body = body.expect("regime block must be extracted");
        assert!(body.contains("let adx14 = ind.adx(14)"));
        assert!(body.contains("trending"));
        assert!(!cleaned.contains("regime {"));
        assert!(!cleaned.contains("adx14"));
        // Main code outside block is preserved.
        assert!(cleaned.contains("ema9"));
        assert!(cleaned.contains("cross_above"));
        // Line count is preserved (whitespace replaces the block).
        assert_eq!(script.matches('\n').count(), cleaned.matches('\n').count());
    }

    #[test]
    fn extract_regime_block_none() {
        let script = "let ema9 = ind.ema(9);\nif true { entry = true; }";
        let (body, cleaned) = extract_regime_block(script).unwrap();
        assert!(body.is_none());
        assert_eq!(cleaned, script);
    }

    #[test]
    fn extract_regime_block_nested_braces() {
        let script = r#"
regime {
    let adx14 = ind.adx(14);
    if adx14[0].adx > 25.0 {
        trend = "trending";
    } else {
        trend = "neutral";
    }
}
let exit = false;
"#;
        let (body, _) = extract_regime_block(script).unwrap();
        let body = body.unwrap();
        assert!(body.contains("else"));
        assert!(body.contains("neutral"));
    }

    #[test]
    fn extract_regime_block_ignores_commented() {
        let script = "// regime { ignore_me }\nlet x = 1;";
        let (body, _) = extract_regime_block(script).unwrap();
        assert!(body.is_none());
    }

    #[test]
    fn extract_regime_block_word_boundary() {
        // `regimes` is not the keyword `regime`.
        let script = "let regimes = 1;";
        let (body, _) = extract_regime_block(script).unwrap();
        assert!(body.is_none());
    }

    #[test]
    fn extract_regime_block_unbalanced_errors() {
        let script = "regime { let x = 1; ";
        let r = extract_regime_block(script);
        assert!(r.is_err());
    }

    // ── candle directive tests ────────────────────────────────────────────────

    #[test]
    fn candle_directive_basic_transform() {
        let s = r#"candle.transform("heiken_ashi");

let ema9 = ind.ema(9);
"#;
        let (ds, cleaned) = extract_candle_directives(s).unwrap();
        assert_eq!(ds.len(), 1);
        match &ds[0] {
            CandleDirective::Transform { kind, smooth } => {
                assert_eq!(kind, "heiken_ashi");
                assert_eq!(*smooth, None);
            }
        }
        // Directive line is blanked, body preserved.
        assert!(!cleaned.contains("candle.transform"));
        assert!(cleaned.contains("let ema9"));
        assert_eq!(s.matches('\n').count(), cleaned.matches('\n').count());
    }

    #[test]
    fn candle_directive_with_smooth() {
        let s = r#"candle.transform("smooth_ha", 3);
let x = ind.ema(9);"#;
        let (ds, _) = extract_candle_directives(s).unwrap();
        match &ds[0] {
            CandleDirective::Transform { kind, smooth } => {
                assert_eq!(kind, "smooth_ha");
                assert_eq!(*smooth, Some(3));
            }
        }
    }

    #[test]
    fn candle_directive_after_let_errors() {
        let s = r#"let ema9 = ind.ema(9);
candle.transform("heiken_ashi");"#;
        let err = extract_candle_directives(s).err().expect("should error");
        let msg = err.to_string();
        assert!(msg.contains("must appear at the top"), "{msg}");
        assert!(msg.contains("line 2"), "{msg}");
    }

    #[test]
    fn candle_directive_after_regime_errors() {
        let s = r#"regime { trend = "x"; }
candle.transform("ha");"#;
        let err = extract_candle_directives(s).err().expect("should error");
        assert!(err.to_string().contains("must appear at the top"));
    }

    #[test]
    fn candle_directive_after_comment_allowed() {
        let s = r#"// strategy header
// uses heiken ashi
candle.transform("heiken_ashi");

let x = ind.ema(9);"#;
        let (ds, _) = extract_candle_directives(s).unwrap();
        assert_eq!(ds.len(), 1);
    }

    #[test]
    fn candle_directive_multiple_in_header_ok() {
        // Future-proofing: header can hold multiple directives.
        let s = r#"candle.transform("heiken_ashi");
candle.transform("heiken_ashi");
let x = ind.ema(9);"#;
        let (ds, _) = extract_candle_directives(s).unwrap();
        assert_eq!(ds.len(), 2);
    }

    #[test]
    fn candle_directive_no_directive_ok() {
        let s = "let x = ind.ema(9);";
        let (ds, cleaned) = extract_candle_directives(s).unwrap();
        assert!(ds.is_empty());
        assert_eq!(cleaned, s);
    }

    #[test]
    fn candle_directive_unknown_method_errors() {
        let s = r#"candle.zonk(3);"#;
        let err = extract_candle_directives(s).err().expect("should error");
        assert!(err.to_string().contains("unknown candle directive"));
    }

    #[test]
    fn candle_directive_missing_arg_errors() {
        let s = r#"candle.transform();"#;
        let err = extract_candle_directives(s).err().expect("should error");
        assert!(err.to_string().contains("needs a kind argument"));
    }
}

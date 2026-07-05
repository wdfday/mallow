use anyhow::Result;
use alm_core::Timeframe;
use alm_indicator::IndicatorBox;
use serde_json::json;
use std::collections::HashMap;

use crate::script::engine::DEFAULT_BUF_DEPTH;

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
    pub(crate) var_name:     String,
    pub(crate) ind_type:     String,
    pub(crate) period:       usize,
    pub(crate) extra_params: HashMap<String, f64>,
    pub(crate) buf_depth:    usize,
    pub(crate) kind:         IndicatorKind,
    pub(crate) timeframe:    Option<Timeframe>,
    pub(crate) live:         bool,
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
        // Skip string literals (handles escape sequences).
        if bytes[i] == b'"' {
            i += 1;
            while i < bytes.len() {
                if bytes[i] == b'\\' { i += 2; continue; }
                if bytes[i] == b'"' { break; }
                i += 1;
            }
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
                        while j < bytes.len() {
                            if bytes[j] == b'\\' { j += 2; continue; }
                            if bytes[j] == b'"' { break; }
                            j += 1;
                        }
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
    let bytes = line.as_bytes();
    let mut in_string = false;
    let mut i = 0;
    while i < bytes.len() {
        if in_string {
            if bytes[i] == b'\\' { i += 2; continue; } // skip escaped char
            if bytes[i] == b'"'  { in_string = false; }
        } else {
            if bytes[i] == b'"' {
                in_string = true;
            } else if i + 1 < bytes.len() && bytes[i] == b'/' && bytes[i + 1] == b'/' {
                return &line[..i];
            }
        }
        i += 1;
    }
    line
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

// ── Positional parameter names per indicator type ─────────────────────────────

/// Returns the ordered list of secondary parameter names for positional args
/// after the period. These map `ind.macd(12, 26, 9)` → `{slow:26, signal:9}`.
///
/// Indicators not listed here accept no positional secondary params — any
/// integer after the period is silently ignored (use `buf=N` for buf depth).
pub(crate) fn positional_param_names(ind_type: &str) -> &'static [&'static str] {
    match ind_type {
        "macd"            => &["slow", "signal"],
        "bbands"          => &["multiplier"],
        "supertrend"      => &["multiplier"],
        "stochastic"      => &["d_period"],
        "stoch_rsi"       => &["smooth_d"],
        "parabolic_sar"   => &["step", "max"],
        "kdj"             => &["k_period", "d_period"],
        "alma"            => &["offset", "sigma"],
        "trix"            => &["signal"],
        "kama"            => &["fast", "slow"],
        "keltner"         => &["atr_period", "multiplier"],
        "chop_zone"       => &["threshold"],
        "chandelier_exit" => &["multiplier"],
        "chande_kroll"    => &["factor", "stop_period"],
        "alligator"       => &["teeth", "lips"],
        "tsi"             => &["second"],
        "ppo"             => &["slow", "signal"],
        "pmo"             => &["smooth2", "signal"],
        "smi"             => &["smooth1", "smooth2", "signal"],
        "coppock"         => &["long", "wma"],
        "uo"              => &["medium", "slow"],
        "ao"              => &["slow"],
        "connors_rsi"     => &["streak_period", "rank_period"],
        "ichimoku"        => &["kijun", "senkou_b"],
        // gmma: period = short[0]; remaining positionals fill short[1..5] then long[0..5]
        "gmma"            => &["s1", "s2", "s3", "s4", "s5", "l0", "l1", "l2", "l3", "l4", "l5"],
        _                 => &[],
    }
}

// ── ta.* key injection ────────────────────────────────────────────────────────

/// Default `ta.*` output history depth when no `buf=N` is given — matches
/// `ind.*`'s `DEFAULT_BUF_DEPTH`, and is exactly enough for `cross_above` /
/// `cross_below` / `crossed` / `rising` / `falling`.
const DEFAULT_TA_BUF: usize = DEFAULT_BUF_DEPTH;

/// Find the index (within `s`) of the `)` that closes the call whose `(`
/// was already consumed — i.e. `s` starts right after that opening paren.
/// Tracks `(`/`[` nesting depth together (they're always balanced in valid
/// Rhai) and skips the contents of `"..."` string literals.
fn find_matching_close(s: &str) -> Option<usize> {
    let mut depth = 1i32;
    let mut in_str = false;
    let mut chars = s.char_indices();
    while let Some((i, c)) = chars.next() {
        if in_str {
            if c == '\\' { chars.next(); } else if c == '"' { in_str = false; }
            continue;
        }
        match c {
            '"' => in_str = true,
            '(' | '[' => depth += 1,
            ')' | ']' => {
                depth -= 1;
                if depth == 0 { return Some(i); }
            }
            _ => {}
        }
    }
    None
}

/// Split `s` on top-level commas only — nested `(...)`/`[...]` and the
/// contents of `"..."` strings are protected from the split. Returns an
/// empty `Vec` for a blank/whitespace-only `s` (i.e. an empty arg list).
fn split_top_level_commas(s: &str) -> Vec<&str> {
    if s.trim().is_empty() { return Vec::new(); }
    let mut segments = Vec::new();
    let mut depth = 0i32;
    let mut in_str = false;
    let mut start = 0usize;
    let mut chars = s.char_indices();
    while let Some((i, c)) = chars.next() {
        if in_str {
            if c == '\\' { chars.next(); } else if c == '"' { in_str = false; }
            continue;
        }
        match c {
            '"' => in_str = true,
            '(' | '[' => depth += 1,
            ')' | ']' => depth -= 1,
            ',' if depth == 0 => {
                segments.push(&s[start..i]);
                start = i + 1;
            }
            _ => {}
        }
    }
    segments.push(&s[start..]);
    segments
}

/// Minimum sane `buf=N` — `0` would make the output history ring hold
/// nothing, so `wide[0]` reads out of bounds; reject it loudly rather than
/// silently clamping (see `ring_push`, which still defensively clamps to 1
/// as a last resort against anything that slips past this check).
const MIN_TA_BUF: usize = 1;

/// Scan `code` (already comment-stripped) for occurrences of `ta.IDENT(` at
/// a real word boundary, skipping the contents of `"..."` string literals so
/// a `reason = "...ta.foo(...)..."` message can't be mistaken for a call.
/// Returns `(byte_offset_of_"ta.", func_name)` pairs in order.
fn find_ta_occurrences(code: &str) -> Vec<(usize, String)> {
    let bytes = code.as_bytes();
    let mut out = Vec::new();
    let mut in_str = false;
    let mut i = 0usize;
    while i < bytes.len() {
        if in_str {
            if bytes[i] == b'\\' { i += 2; continue; }
            if bytes[i] == b'"' { in_str = false; }
            i += 1;
            continue;
        }
        if bytes[i] == b'"' { in_str = true; i += 1; continue; }
        if code[i..].starts_with("ta.") {
            let word_boundary = i == 0 || {
                let prev = bytes[i - 1];
                !(prev.is_ascii_alphanumeric() || prev == b'_')
            };
            if word_boundary {
                let after = &code[i + 3..];
                let func_end = after.find('(').unwrap_or(after.len());
                out.push((i, after[..func_end].trim().to_string()));
            }
        }
        i += 1;
    }
    out
}

/// Net `{`/`}` balance contributed by one line (comment-stripped,
/// string-literal-aware — a stray brace inside a `"..."` value like
/// `reason = "config: {ok}"` must not be counted). Used to track brace
/// depth across a script block so `ta.*` declarations can be rejected when
/// they're nested inside `if`/`while`/`for` instead of at the top level —
/// see `validate_ta_top_level` for why that nesting is dangerous.
pub(crate) fn brace_depth_delta(line: &str) -> i32 {
    let Some(code) = line.split("//").next() else { return 0 };
    let mut delta = 0i32;
    let mut in_str = false;
    let bytes = code.as_bytes();
    let mut i = 0usize;
    while i < bytes.len() {
        if in_str {
            if bytes[i] == b'\\' { i += 2; continue; }
            if bytes[i] == b'"' { in_str = false; }
            i += 1;
            continue;
        }
        match bytes[i] {
            b'"' => in_str = true,
            b'{' => delta += 1,
            b'}' => delta -= 1,
            _ => {}
        }
        i += 1;
    }
    delta
}

/// Reject a `ta.*` declaration found at nonzero brace depth — i.e. nested
/// inside `if`/`while`/`for` rather than at the top level of the regime/main
/// block. Unlike `ind.*` (fully extracted from the script and updated by
/// Rust code every bar regardless of where it was textually written, so
/// nesting it is merely confusing, never wrong), `ta.*` executes as real
/// Rhai code respecting control flow — if the branch it's declared in
/// doesn't run on some bar, its incremental state simply doesn't advance
/// that bar, silently corrupting the smoothing math (which assumes
/// continuous per-bar feeding) the next time the branch *does* run.
pub(crate) fn validate_ta_top_level(var_name: &str, line: &str, brace_depth: i32) -> anyhow::Result<()> {
    if brace_depth != 0 {
        anyhow::bail!(
            "`ta.*` declaration `{var_name}` must be at the top level of the script (found nested \
             inside `if`/`while`/`for`, brace depth {brace_depth}) — it must run every bar to keep \
             its incremental state correct; a conditionally-skipped update would silently corrupt \
             the smoothing math (line: `{}`)",
            line.trim()
        );
    }
    Ok(())
}

/// Rewrite `let NAME = ta.FUNC(args [, buf=N]);` to
/// `let NAME = ta.FUNC("NAME", args, N);` — the variable name becomes the
/// incremental-state key automatically, and a trailing `buf=N` (parsed
/// statically here, same convention as `ind.*`'s `buf=N` — see
/// `try_parse_indicator_line`) becomes an explicit output-history-depth
/// argument every `ta.*` function takes; `DEFAULT_TA_BUF` is injected when
/// `buf=N` is absent. Unlike `ind.*`, `ta.*` calls stay in the script and
/// execute as real Rhai function calls (see `crate::script::ta`) — this only
/// rewrites the call text, it does not extract/remove the line.
///
/// `ta.reset(...)` is never treated as a declaration here (returns `Ok(None)`
/// as soon as `func == "reset"` is seen) — it takes an explicit key by
/// design and must be called as a bare statement; `validate_ta_declarations`
/// enforces that.
///
/// Returns three distinct outcomes:
/// - `Ok(None)` — `line` isn't a top-level `let NAME = ta.FUNC(...)` binding
///   at all (unrelated code, `ta.reset("key")`, or `ta.*` used inline/bare —
///   `validate_ta_declarations` is what rejects the latter, this function
///   just leaves such lines untouched).
/// - `Ok(Some((var_name, rewritten)))` — a valid declaration, successfully
///   rewritten; the caller uses `var_name` for the cross-namespace
///   uniqueness check (`decl_names` in `ScriptStrategy::build`).
/// - `Err(_)` — `line` clearly IS a `let NAME = ta.FUNC(...)` attempt but is
///   malformed (unclosed call, or an invalid `buf=`) — a real diagnostic,
///   not a "line doesn't apply" case.
pub(crate) fn rewrite_ta_line(line: &str) -> anyhow::Result<Option<(String, String)>> {
    let indent_len = line.len() - line.trim_start().len();
    let (indent, code) = line.split_at(indent_len);
    let Some(code) = code.split("//").next() else { return Ok(None) };
    if code.trim().is_empty() { return Ok(None); }

    let Some(rest) = code.trim_start().strip_prefix("let ") else { return Ok(None) };
    let rest = rest.trim_start();
    let Some(eq_pos) = rest.find('=') else { return Ok(None) };
    let var_name = rest[..eq_pos].trim();
    if var_name.is_empty() || !var_name.chars().all(|c| c.is_alphanumeric() || c == '_') {
        return Ok(None);
    }

    let rhs = rest[eq_pos + 1..].trim_start();
    let Some(after_ta) = rhs.strip_prefix("ta.") else { return Ok(None) };
    let Some(paren) = after_ta.find('(') else { return Ok(None) };
    let func = after_ta[..paren].trim();
    if func.is_empty() || !func.chars().all(|c| c.is_alphanumeric() || c == '_') {
        return Ok(None);
    }
    // `reset` is never `let`-bindable — it's a bare-statement-only escape
    // hatch with its own (unrelated) 1-arg signature.
    if func == "reset" { return Ok(None); }

    // Past this point `line` is unambiguously a `let NAME = ta.FUNC(...)`
    // attempt — anything wrong from here on is a real error, not "N/A".
    let after_paren = &after_ta[paren + 1..];
    let close = find_matching_close(after_paren).ok_or_else(|| anyhow::anyhow!(
        "`ta.{func}(...)` is missing a closing ')' (line: `{}`)", code.trim()
    ))?;
    let (args, tail) = (&after_paren[..close], &after_paren[close..]); // tail starts with ')'

    let mut segments = split_top_level_commas(args);
    let buf = match segments.last().map(|s| s.trim()) {
        Some(last) if last.starts_with("buf") => {
            let after_buf = last.strip_prefix("buf").unwrap().trim_start();
            let value_str = after_buf.strip_prefix('=').ok_or_else(|| anyhow::anyhow!(
                "`ta.{func}(...)`: expected `buf=N`, found `{last}` (line: `{}`)", code.trim()
            ))?.trim();
            let n: usize = value_str.parse().map_err(|_| anyhow::anyhow!(
                "`ta.{func}(...)`: `buf={value_str}` is not a valid non-negative integer \
                 (line: `{}`)", code.trim()
            ))?;
            if n < MIN_TA_BUF {
                anyhow::bail!(
                    "`ta.{func}(...)`: `buf={n}` is too small — must be >= {MIN_TA_BUF} \
                     (line: `{}`)", code.trim()
                );
            }
            segments.pop();
            n
        }
        _ => DEFAULT_TA_BUF,
    };

    let mut parts: Vec<String> = segments.iter().map(|s| s.trim().to_string()).collect();
    parts.push(buf.to_string());
    let rebuilt_args = parts.join(", ");

    Ok(Some((
        var_name.to_string(),
        format!("{indent}let {var_name} = ta.{func}(\"{var_name}\", {rebuilt_args}{tail}"),
    )))
}

/// Enforce that every `ta.*` call is a top-level `let NAME = ta.FUNC(...)`
/// declaration — the only exception is `ta.reset(key)`, which must be a
/// *bare statement* (an explicit key by design, never `let`-bound — see
/// `rewrite_ta_line`'s doc). Also enforces **at most one `ta.*` reference per
/// line**: nesting one `ta.*` call inside another, or writing two
/// declarations separated by `;` on the same physical line, both silently
/// escape `rewrite_ta_line` (which only recognizes the single outermost
/// `let NAME = ta.FUNC(...)` shape) — so instead of rejecting those shapes
/// outright this used to let them through as "already valid" and only failed
/// at runtime, on whichever bar first executed the un-rewritten call, with a
/// cryptic Rhai arity error. Propagates `rewrite_ta_line`'s error directly
/// when the line clearly attempted a declaration but is malformed (bad
/// `buf=`, unclosed call — a specific diagnostic already exists for those);
/// only synthesizes the generic "must be declared as `let NAME = ...`"
/// message when `rewrite_ta_line` says the line isn't a declaration attempt
/// at all (inline/expression use, bare statement, reassignment, etc.).
pub(crate) fn validate_ta_declarations(line: &str) -> anyhow::Result<()> {
    let Some(code) = line.split("//").next() else { return Ok(()) };
    let occurrences = find_ta_occurrences(code);

    if occurrences.len() > 1 {
        anyhow::bail!(
            "only one `ta.*` reference is allowed per line (found {}) — nested `ta.*` calls \
             and multiple declarations on one line aren't supported; put each on its own line \
             (line: `{}`)",
            occurrences.len(), code.trim()
        );
    }

    let Some((_, func)) = occurrences.into_iter().next() else { return Ok(()) };

    if func == "reset" {
        if code.trim_start().starts_with("let ") {
            anyhow::bail!(
                "`ta.reset(...)` must be called as a bare statement, e.g. `ta.reset(\"key\");` \
                 — not assigned via `let` (line: `{}`)",
                code.trim()
            );
        }
        return Ok(());
    }

    if rewrite_ta_line(line)?.is_none() {
        anyhow::bail!(
            "`ta.{func}(...)` must be declared as `let NAME = ta.{func}(...);` \
             (found: `{}`) — inline/expression use of `ta.*` isn't supported, only \
             `ta.reset(key)` may be called bare",
            code.trim()
        );
    }
    Ok(())
}

// ── Declaration parser ────────────────────────────────────────────────────────

/// Parse an indicator declaration line:
/// ```text
/// let NAME = ind.TYPE(period [, name=value]* [, "TF"] [, buf=N]);
/// ```
///
/// Positional integers after the period are mapped to per-type secondary
/// parameter names via [`positional_param_names`].  Buffer depth **must** be
/// set with the explicit `buf=N` named form; a bare integer is no longer
/// treated as buf.
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

    // Parse args: period [, name=value | positional_num | "TF"]*
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
            // named param: `name=value`  or  `buf=N`
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
            // positional: quoted "TF" string or numeric secondary param
            let s = token.trim_matches('"').trim_matches('\'');
            if let Ok(n) = s.parse::<f64>() {
                // Map numeric positional to indicator param name by index.
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
                timeframe = crate::script::utils::parse_timeframe(tf_str);
            }
        }
    }

    let (ind_type, kind) = map_indicator_type(&type_str);
    Some(IndicatorDecl { var_name, ind_type, period, extra_params, buf_depth, kind, timeframe, live })
}

// ── Type → (canonical_type, IndicatorKind) ───────────────────────────────────

pub(crate) fn map_indicator_type(type_str: &str) -> (String, IndicatorKind) {
    use IndicatorKind::{Multi, Single};
    match type_str {
        // ── Single-output: Array<f64> ────────────────────────────────────────
        "ema" | "sma" | "wma" | "hma" | "dema" | "tema" | "smma" | "kama" | "alma" |
        "mcginley" | "vwma" | "rsi" | "cci" | "roc" | "mfi" | "mom" | "cmo" |
        "dpo" | "rci" | "chop" | "williams_r" | "cmf" | "obv" | "vwap" | "ao" | "bop" |
        "coppock" | "uo" | "tsi" | "connors_rsi" | "volatility_ratio" =>
            (type_str.to_string(), Single("value".to_string())),

        // fields: .atr  .tr  — average true range is the primary; .tr = raw true range
        "atr" => ("atr".to_string(), Multi("atr".to_string())),
        // fields: .value  .slope  — least-squares MA value is primary; .slope = regression slope
        "lsma" => ("lsma".to_string(), Multi("value".to_string())),

        // ── Multi-output: Array<MEntry> ──────────────────────────────────────
        // Primary = the one field that makes sense when used as a plain number.

        // fields: .macd  .signal  .histogram  — MACD line is the primary output
        "macd" => ("macd".to_string(), Multi("macd".to_string())),

        // fields: .adx (default, trend strength) .plus_di .minus_di
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

        // fields: .up  .down  — bullish leg is primary; oscillator: use aroon_osc
        "aroon"     => ("aroon".to_string(),     Multi("up".to_string())),
        // scalar aroon oscillator (−100…+100) — up − down
        "aroon_osc" => ("aroon_osc".to_string(), Single("value".to_string())),
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

        // fields: .bull_power (default)  .bear_power  .ema  — Elder Ray
        "elder_ray" => ("elder_ray".to_string(), Multi("bull_power".to_string())),

        // fields: .tenkan  .kijun  .senkou_a  .senkou_b  .chikou  .above_cloud
        "ichimoku" => ("ichimoku".to_string(), Multi("tenkan".to_string())),
        // fields: .jaw  .teeth  .lips  .bullish  — teeth = 8-bar balance line
        "alligator" => ("alligator".to_string(), Multi("teeth".to_string())),
        // fields: .spread (default, >0 bull/<0 bear)  .bullish  .short_0..short_5  .long_0..long_5
        "gmma" => ("gmma".to_string(), Multi("spread".to_string())),
        // fields: .value  .velocity  — filtered price + rate-of-change estimate
        "kalman" => ("kalman".to_string(), Multi("value".to_string())),

        // fields: .bull  .bear  .ema  — bull power = high − EMA
        "bull_bear"  => ("bull_bear".to_string(),  Multi("bull".to_string())),
        // fields: .long_stop  .short_stop  .atr  — long-side chandelier stop
        "chandelier_exit"  => ("chandelier_exit".to_string(),  Multi("long_stop".to_string())),
        // fields: .stop_long  .stop_short  — long-side Chande-Kroll stop
        "chande_kroll" => ("chande_kroll".to_string(), Multi("stop_long".to_string())),
        // fields: .bullish  .bearish  .fractal_high  .fractal_low
        "fractal" => ("fractal".to_string(), Multi("bullish".to_string())),
        // fields: .angle  .zone  — zone is the color-coded trend zone
        "chop_zone" => ("chop_zone".to_string(), Multi("zone".to_string())),

        other => (other.to_string(), Single("value".to_string())),
    }
}

// ── JSON config builder ───────────────────────────────────────────────────────

/// Build the JSON config for `IndicatorBox::from_config`.
/// `extra` overrides per-indicator secondary defaults (e.g. `multiplier`, `slow`, `signal`).
pub(crate) fn indicator_json_config(
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
            "slow": p!("slow", 26.0).max(0.0).round() as u64,
            "signal": p!("signal", 9.0).max(0.0).round() as u64,
        }),
        "bbands" => json!({
            "type": "bbands",
            "period": period,
            "multiplier": p!("multiplier", 2.0),
        }),
        // smooth_k: 1 = Fast Stochastic (default), >1 = Slow Stochastic.
        // Named-only param to keep the legacy positional `ind.stochastic(14, 3)`
        // mapping (positional[0] → d_period) non-breaking.
        "stochastic" => json!({
            "type": "stochastic",
            "k_period": period,
            "smooth_k": p!("smooth_k", 1.0).max(0.0).round() as u64,
            "d_period": p!("d_period", 3.0).max(0.0).round() as u64,
        }),
        "stoch_rsi" => json!({
            "type": "stoch_rsi",
            "rsi_period": period,
            "smooth_d": p!("smooth_d", 3.0).max(0.0).round() as u64,
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
            "k_period": p!("k_period", 3.0).max(0.0).round() as u64,
            "d_period": p!("d_period", 3.0).max(0.0).round() as u64,
        }),
        "gmma" => {
            if period == 0 {
                // default GUPPY periods
                json!({"type": "gmma"})
            } else {
                json!({
                    "type": "gmma",
                    "short": [
                        period as u64,
                        p!("s1", 5.0).max(0.0).round() as u64, p!("s2", 8.0).max(0.0).round() as u64,
                        p!("s3", 10.0).max(0.0).round() as u64, p!("s4", 12.0).max(0.0).round() as u64, p!("s5", 15.0).max(0.0).round() as u64,
                    ],
                    "long": [
                        p!("l0", 30.0).max(0.0).round() as u64, p!("l1", 35.0).max(0.0).round() as u64,
                        p!("l2", 40.0).max(0.0).round() as u64, p!("l3", 45.0).max(0.0).round() as u64,
                        p!("l4", 50.0).max(0.0).round() as u64, p!("l5", 60.0).max(0.0).round() as u64,
                    ],
                })
            }
        }
        "alma" => json!({
            "type": "alma",
            "period": period,
            "offset": p!("offset", 0.85),
            "sigma": p!("sigma", 6.0),
        }),
        "trix" => json!({
            "type": "trix",
            "period": period,
            "signal": p!("signal", 9.0).max(0.0).round() as u64,
        }),
        "kama" => json!({
            "type": "kama",
            "er_period": period,
            "fast": p!("fast", 2.0).max(0.0).round() as u64,
            "slow": p!("slow", 30.0).max(0.0).round() as u64,
        }),
        "keltner" => json!({
            "type": "keltner",
            "period": period,
            "atr_period": p!("atr_period", 10.0).max(0.0).round() as u64,
            "multiplier": p!("multiplier", 2.0),
        }),
        "chop_zone" => json!({
            "type": "chop_zone",
            "ema_period": period,
            "threshold": p!("threshold", 5.0),
        }),
        "chandelier_exit" => json!({
            "type": "chandelier_exit",
            "period": period,
            "multiplier": p!("multiplier", 3.0),
        }),
        "chande_kroll" => json!({
            "type": "chande_kroll",
            "atr_period": period,
            "factor": p!("factor", 1.5),
            "stop_period": p!("stop_period", 9.0).max(0.0).round() as u64,
        }),
        // kalman: no period concept — use named params only (period arg ignored)
        "kalman" => json!({
            "type": "kalman",
            "q_pos": p!("q_pos", 0.001),
            "q_vel": p!("q_vel", 0.001),
            "r": p!("r", 1.0),
        }),
        "alligator" => json!({
            "type": "alligator",
            "jaw":   period,
            "teeth": p!("teeth", 8.0).max(0.0).round() as u64,
            "lips":  p!("lips",  5.0).max(0.0).round() as u64,
        }),
        "tsi" => json!({
            "type": "tsi",
            "first":  period,
            "second": p!("second", 13.0).max(0.0).round() as u64,
        }),
        "ppo" => json!({
            "type": "ppo",
            "fast":   period,
            "slow":   p!("slow",   26.0).max(0.0).round() as u64,
            "signal": p!("signal",  9.0).max(0.0).round() as u64,
        }),
        "uo" => json!({
            "type":   "uo",
            "fast":   if period == 0 { 7 } else { period },
            "medium": p!("medium", 14.0).max(0.0).round() as u64,
            "slow":   p!("slow",   28.0).max(0.0).round() as u64,
        }),
        "ao" => json!({
            "type": "ao",
            "fast": if period == 0 { 5 } else { period },
            "slow": p!("slow", 34.0).max(0.0).round() as u64,
        }),
        "connors_rsi" => json!({
            "type":          "connors_rsi",
            "rsi_period":    period,
            "streak_period": p!("streak_period",   2.0).max(0.0).round() as u64,
            "rank_period":   p!("rank_period",   100.0).max(0.0).round() as u64,
        }),
        "ichimoku" => json!({
            "type":     "ichimoku",
            "tenkan":   period,
            "kijun":    p!("kijun",    26.0).max(0.0).round() as u64,
            "senkou_b": p!("senkou_b", 52.0).max(0.0).round() as u64,
        }),
        "volatility_ratio" => json!({"type": "volatility_ratio", "lookback": period}),
        "obv"     => json!({"type": "obv"}),
        "vwap"    => json!({
            "type": "vwap",
            "session_gap_mins": p!("session_gap_mins", 390.0).max(0.0).round() as u64,
        }),
        "bop"     => json!({"type": "bop"}),
        "coppock" => json!({
            "type":  "coppock",
            "short": if period == 0 { 11 } else { period },
            "long":  p!("long", 14.0).max(0.0).round() as u64,
            "wma":   p!("wma",  10.0).max(0.0).round() as u64,
        }),
        "pmo" => json!({
            "type":    "pmo",
            "smooth1": if period == 0 { 35 } else { period },
            "smooth2": p!("smooth2", 20.0).max(0.0).round() as u64,
            "signal":  p!("signal",  10.0).max(0.0).round() as u64,
        }),
        "smi" => json!({
            "type":    "smi",
            "period":  if period == 0 { 13 } else { period },
            "smooth1": p!("smooth1", 25.0).max(0.0).round() as u64,
            "smooth2": p!("smooth2",  2.0).max(0.0).round() as u64,
            "signal":  p!("signal",   9.0).max(0.0).round() as u64,
        }),
        // kst: 4-ROC + 4-SMA arrays can't be expressed positionally; the period
        // arg sets the signal line. Arrays stay at the Pring defaults (override
        // via the JSON config path if needed).
        "kst" => json!({
            "type":   "kst",
            "signal": if period == 0 { 9 } else { period },
        }),
        t         => json!({"type": t, "period": period}),
    }
}

/// Indicator types where `period = 0` is valid — the positional period argument
/// maps to other parameters or the indicator uses built-in defaults.
///
/// Shared by `make_indicator_box` (runtime validation), `lint.rs` (linter), and
/// `v2/parse.rs` (v2 runtime validation) — single source of truth.
pub(crate) use crate::script::utils::PERIOD_EXEMPT;

pub(super) fn make_indicator_box(decl: &IndicatorDecl) -> Result<IndicatorBox> {
    crate::script::utils::build_indicator_box(&decl.var_name, &decl.ind_type, decl.period, &decl.extra_params)
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use crate::test_utils::*;
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
        // buf must be explicit via `buf=N`
        let d = try_parse_indicator_line(r#"let atr = ind.atr(14, buf=5);"#).unwrap();
        assert_eq!(d.period, 14);
        assert_eq!(d.buf_depth, 5);
        // atr is now multi-output (.atr default, .tr raw true range).
        assert!(matches!(d.kind, IndicatorKind::Multi(_)));
    }

    #[test]
    fn parse_htf() {
        let d = try_parse_indicator_line(r#"let h1_ema = ind.ema(20, "H1");"#).unwrap();
        assert_eq!(d.timeframe, Some(Timeframe::H1));
        assert_eq!(d.buf_depth, DEFAULT_BUF_DEPTH);
    }

    #[test]
    fn parse_htf_custom_buf() {
        let d = try_parse_indicator_line(r#"let x = ind.rsi(5, "M5", buf=3);"#).unwrap();
        assert_eq!(d.timeframe, Some(Timeframe::M5));
        assert_eq!(d.buf_depth, 3);
    }

    #[test]
    fn non_indicator_line_returns_none() {
        assert!(try_parse_indicator_line("let x = 42;").is_none());
        assert!(try_parse_indicator_line("// comment").is_none());
    }

    #[test]
    fn parse_named_param_single() {
        let d = try_parse_indicator_line("let st = ind.supertrend(10, multiplier=5.0);").unwrap();
        assert_eq!(d.period, 10);
        assert_eq!(d.extra_params.get("multiplier").copied(), Some(5.0));
        assert_eq!(d.buf_depth, DEFAULT_BUF_DEPTH);
        assert!(d.timeframe.is_none());
    }

    #[test]
    fn parse_named_param_with_buf() {
        let d = try_parse_indicator_line("let st = ind.supertrend(10, multiplier=5.0, buf=4);").unwrap();
        assert_eq!(d.period, 10);
        assert_eq!(d.extra_params.get("multiplier").copied(), Some(5.0));
        assert_eq!(d.buf_depth, 4);
    }

    #[test]
    fn parse_named_param_with_tf_and_buf() {
        let d = try_parse_indicator_line(r#"let st = ind.supertrend(10, multiplier=5.0, "H1", buf=3);"#).unwrap();
        assert_eq!(d.period, 10);
        assert_eq!(d.extra_params.get("multiplier").copied(), Some(5.0));
        assert_eq!(d.timeframe, Some(Timeframe::H1));
        assert_eq!(d.buf_depth, 3);
    }

    #[test]
    fn parse_multiple_named_params() {
        let d = try_parse_indicator_line("let m = ind.macd(12, slow=26, signal=9);").unwrap();
        assert_eq!(d.period, 12);
        assert_eq!(d.extra_params.get("slow").copied(), Some(26.0));
        assert_eq!(d.extra_params.get("signal").copied(), Some(9.0));
        assert!(d.extra_params.get("multiplier").is_none());
    }

    #[test]
    fn indicator_json_config_supertrend_default_multiplier() {
        let extra = HashMap::new();
        let cfg = indicator_json_config("supertrend", 10, &extra);
        assert_eq!(cfg["multiplier"].as_f64(), Some(3.0));
        assert_eq!(cfg["period"].as_u64(), Some(10));
    }

    #[test]
    fn indicator_json_config_supertrend_custom_multiplier() {
        let mut extra = HashMap::new();
        extra.insert("multiplier".to_string(), 5.0f64);
        let cfg = indicator_json_config("supertrend", 10, &extra);
        assert_eq!(cfg["multiplier"].as_f64(), Some(5.0));
    }

    #[test]
    fn indicator_json_config_bbands_custom_multiplier() {
        let mut extra = HashMap::new();
        extra.insert("multiplier".to_string(), 2.5f64);
        let cfg = indicator_json_config("bbands", 20, &extra);
        assert_eq!(cfg["multiplier"].as_f64(), Some(2.5));
        assert_eq!(cfg["period"].as_u64(), Some(20));
    }

    #[test]
    fn indicator_json_config_macd_custom_secondary() {
        let mut extra = HashMap::new();
        extra.insert("slow".to_string(), 30.0f64);
        extra.insert("signal".to_string(), 7.0f64);
        let cfg = indicator_json_config("macd", 12, &extra);
        assert_eq!(cfg["fast"].as_u64(), Some(12));
        assert_eq!(cfg["slow"].as_u64(), Some(30));
        assert_eq!(cfg["signal"].as_u64(), Some(7));
    }

    #[test]
    fn buf_must_be_named() {
        // Bare integer after period for single-param indicators is silently
        // ignored (not treated as buf) — buf requires explicit `buf=N`.
        let d1 = try_parse_indicator_line("let x = ind.atr(14, 5);").unwrap();
        assert_eq!(d1.period, 14);
        assert_eq!(d1.buf_depth, DEFAULT_BUF_DEPTH); // 5 is ignored, not buf
        assert!(d1.extra_params.is_empty());

        // The correct way to set buf:
        let d2 = try_parse_indicator_line("let x = ind.atr(14, buf=5);").unwrap();
        assert_eq!(d2.buf_depth, 5);

        // HTF + buf still works via buf=N:
        let d3 = try_parse_indicator_line(r#"let h = ind.ema(20, "H1", buf=3);"#).unwrap();
        assert_eq!(d3.period, 20);
        assert_eq!(d3.timeframe, Some(Timeframe::H1));
        assert_eq!(d3.buf_depth, 3);
        assert!(d3.extra_params.is_empty());
    }

    #[test]
    fn positional_params_map_to_indicator_secondary() {
        // MACD: period=fast, positional 0→slow, positional 1→signal.
        let d = try_parse_indicator_line("let m = ind.macd(12, 26, 9);").unwrap();
        assert_eq!(d.period, 12);
        assert_eq!(d.extra_params.get("slow").copied(), Some(26.0));
        assert_eq!(d.extra_params.get("signal").copied(), Some(9.0));
        assert_eq!(d.buf_depth, DEFAULT_BUF_DEPTH);

        // bbands: multiplier is first positional.
        let d2 = try_parse_indicator_line("let bb = ind.bbands(20, 2.5);").unwrap();
        assert_eq!(d2.extra_params.get("multiplier").copied(), Some(2.5));
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

    #[test]
    fn rewrite_ta_line_injects_key_and_default_buf() {
        let (var_name, out) = rewrite_ta_line("let fast = ta.ema(9, close[0]);").unwrap().unwrap();
        assert_eq!(var_name, "fast");
        assert_eq!(out, r#"let fast = ta.ema("fast", 9, close[0], 2);"#);
    }

    #[test]
    fn rewrite_ta_line_parses_explicit_buf() {
        let (_, out) = rewrite_ta_line("let fast = ta.ema(9, close[0], buf=5);").unwrap().unwrap();
        assert_eq!(out, r#"let fast = ta.ema("fast", 9, close[0], 5);"#);
    }

    #[test]
    fn rewrite_ta_line_handles_buf_with_spaces() {
        let (_, out) = rewrite_ta_line("let fast = ta.ema(9, close[0], buf = 7);").unwrap().unwrap();
        assert_eq!(out, r#"let fast = ta.ema("fast", 9, close[0], 7);"#);
    }

    #[test]
    fn rewrite_ta_line_two_arg_form() {
        // ta.decay(alpha, value) — no period, still gets the trailing default buf.
        let (_, out) = rewrite_ta_line("let d = ta.decay(0.1, close[0]);").unwrap().unwrap();
        assert_eq!(out, r#"let d = ta.decay("d", 0.1, close[0], 2);"#);
    }

    #[test]
    fn rewrite_ta_line_multi_arg_form_with_buf() {
        // ta.vwma(period, value, weight, buf=N) — nested `[0]` indices must not
        // be mistaken for top-level comma boundaries.
        let (_, out) = rewrite_ta_line("let v = ta.vwma(9, close[0], volume[0], buf=4);").unwrap().unwrap();
        assert_eq!(out, r#"let v = ta.vwma("v", 9, close[0], volume[0], 4);"#);
    }

    #[test]
    fn rewrite_ta_line_preserves_indent() {
        let (_, out) = rewrite_ta_line("    let fast = ta.ema(9, close[0]);").unwrap().unwrap();
        assert_eq!(out, "    let fast = ta.ema(\"fast\", 9, close[0], 2);");
    }

    #[test]
    fn rewrite_ta_line_ignores_non_ta_lines() {
        assert!(rewrite_ta_line("let ema9 = ind.ema(9);").unwrap().is_none());
        assert!(rewrite_ta_line("if cross_above(fast, slow) { entry = true; }").unwrap().is_none());
        assert!(rewrite_ta_line("ta.reset(\"fast\");").unwrap().is_none());
    }

    #[test]
    fn rewrite_ta_line_reset_is_never_let_bindable() {
        // `reset` must never be treated as a declaration, even in let-form —
        // it has its own 1-arg (key) signature, not key+buf.
        assert!(rewrite_ta_line("let x = ta.reset(\"fast\");").unwrap().is_none());
    }

    #[test]
    fn rewrite_ta_line_rejects_non_numeric_buf() {
        let err = rewrite_ta_line("let fast = ta.ema(9, close[0], buf=ngu);")
            .err().expect("non-numeric buf must be a hard error, not a silent fallback");
        assert!(err.to_string().contains("not a valid non-negative integer"), "unexpected error: {err}");
    }

    #[test]
    fn rewrite_ta_line_rejects_zero_buf() {
        let err = rewrite_ta_line("let fast = ta.ema(9, close[0], buf=0);")
            .err().expect("buf=0 must be a hard error, not silently clamped to 1");
        assert!(err.to_string().contains("too small"), "unexpected error: {err}");
    }

    #[test]
    fn rewrite_ta_line_rejects_unclosed_call() {
        let err = rewrite_ta_line("let fast = ta.ema(9, close[0];")
            .err().expect("unclosed ta.* call must be a hard error");
        assert!(err.to_string().contains("missing a closing"), "unexpected error: {err}");
    }

    #[test]
    fn validate_ta_declarations_accepts_let_form() {
        assert!(validate_ta_declarations("let fast = ta.ema(9, close[0]);").is_ok());
        assert!(validate_ta_declarations("    let fast = ta.ema(9, close[0], buf=5);").is_ok());
    }

    #[test]
    fn validate_ta_declarations_accepts_bare_reset() {
        assert!(validate_ta_declarations(r#"ta.reset("fast");"#).is_ok());
    }

    #[test]
    fn validate_ta_declarations_ignores_unrelated_lines() {
        assert!(validate_ta_declarations("let ema9 = ind.ema(9);").is_ok());
        assert!(validate_ta_declarations("if cross_above(fast, slow) { entry = true; }").is_ok());
        assert!(validate_ta_declarations("let delta = high[0] - low[0];").is_ok()); // "ta." substring, no word boundary
    }

    #[test]
    fn validate_ta_declarations_rejects_inline_expression_use() {
        let err = validate_ta_declarations("if close[0] > ta.ema(9, close[0])[0] { entry = true; }")
            .err().expect("inline ta.* use must be rejected");
        assert!(err.to_string().contains("must be declared as `let NAME"), "unexpected error: {err}");
    }

    #[test]
    fn validate_ta_declarations_rejects_bare_statement() {
        let err = validate_ta_declarations("ta.ema(9, close[0]);")
            .err().expect("bare ta.* statement (no let) must be rejected");
        assert!(err.to_string().contains("must be declared as `let NAME"), "unexpected error: {err}");
    }

    #[test]
    fn validate_ta_declarations_rejects_reassignment_form() {
        // `x = ta.ema(...)` isn't `let x = ta.ema(...)` — must still be rejected.
        let err = validate_ta_declarations("fast = ta.ema(9, close[0]);")
            .err().expect("non-`let` assignment must be rejected");
        assert!(err.to_string().contains("must be declared as `let NAME"), "unexpected error: {err}");
    }

    #[test]
    fn validate_ta_declarations_ignores_ta_inside_string_literal() {
        // `reason` is a documented output var — a message mentioning "ta."
        // must not be mistaken for an actual ta.* call.
        assert!(validate_ta_declarations(r#"reason = "cross via ta.ema fast/slow";"#).is_ok());
    }

    #[test]
    fn validate_ta_declarations_rejects_two_declarations_on_one_line() {
        let err = validate_ta_declarations(
            "let fast = ta.ema(9, close[0]); let slow = ta.ema(21, close[0]);"
        ).err().expect("two ta.* declarations on one line must be rejected");
        assert!(err.to_string().contains("only one `ta.*` reference"), "unexpected error: {err}");
    }

    #[test]
    fn validate_ta_declarations_rejects_nested_ta_call() {
        let err = validate_ta_declarations("let x = ta.ema(9, ta.sma(3, close[0])[0]);")
            .err().expect("nested ta.* call must be rejected");
        assert!(err.to_string().contains("only one `ta.*` reference"), "unexpected error: {err}");
    }

    #[test]
    fn validate_ta_declarations_rejects_let_bound_reset() {
        let err = validate_ta_declarations(r#"let x = ta.reset("fast");"#)
            .err().expect("let-bound ta.reset(...) must be rejected");
        assert!(err.to_string().contains("must be called as a bare statement"), "unexpected error: {err}");
    }

    #[test]
    fn brace_depth_delta_counts_ignoring_comments_and_strings() {
        assert_eq!(brace_depth_delta("if x { "), 1);
        assert_eq!(brace_depth_delta("}"), -1);
        assert_eq!(brace_depth_delta("if a { } else { "), 1);
        assert_eq!(brace_depth_delta("// if x { "), 0);
        assert_eq!(brace_depth_delta(r#"reason = "config: {ok}";"#), 0);
        assert_eq!(brace_depth_delta("let x = 5;"), 0);
    }

    #[test]
    fn validate_ta_top_level_accepts_zero_depth() {
        assert!(validate_ta_top_level("fast", "let fast = ta.ema(9, close[0]);", 0).is_ok());
    }

    #[test]
    fn validate_ta_top_level_rejects_nonzero_depth() {
        let err = validate_ta_top_level("fast", "    let fast = ta.ema(9, close[0]);", 1)
            .err().expect("nested ta.* declaration must be rejected");
        assert!(err.to_string().contains("must be at the top level"), "unexpected error: {err}");
    }
}

//! True CEL Strategy — uses [`cel-interpreter`](https://crates.io/crates/cel-interpreter)
//! (Google Common Expression Language) instead of the evalexpr approximation.
//!
//! **Key difference from [`crate::cel_strategy`]:**
//! indicators are addressed as *function calls* — `ema(9)`, `rsi(14)` — not as
//! `_`-suffixed identifiers like `ema_9`.  This matches real CEL ergonomics and
//! is more natural to read/write.
//!
//! # Indicator reference
//!
//! | Function             | Indicator                     | Output            |
//! |----------------------|-------------------------------|-------------------|
//! | **MA / Trend**       |                               |                   |
//! | `ema(N)`             | EMA(N)                        | price level       |
//! | `sma(N)`             | SMA(N)                        | price level       |
//! | `wma(N)`             | WMA(N)                        | price level       |
//! | `hma(N)`             | HMA(N)                        | price level       |
//! | `dema(N)`            | DEMA(N)                       | price level       |
//! | `tema(N)`            | TEMA(N)                       | price level       |
//! | `smma(N)`            | SMMA / RMA(N)                 | price level       |
//! | `alma(N)`            | ALMA(N, offset=0.85, σ=6)     | price level       |
//! | `mcginley(N)`        | McGinley Dynamic(N)           | price level       |
//! | `lsma(N)`            | LSMA value(N)                 | price level       |
//! | `lsma_slope(N)`      | LSMA slope(N)                 | signed rate       |
//! | `vwma(N)`            | VWMA(N)                       | price level       |
//! | `kama(N)`            | KAMA(N)                       | price level       |
//! | `macd_hist(N)`       | MACD histogram (N, 26, 9)     | signed            |
//! | `macd_line(N)`       | MACD line (N, 26, 9)          | signed            |
//! ## Multi-Timeframe (MTF)
//!
//! Prefix any indicator with `TF.` (TradingView-style) to compute it on a higher timeframe.
//! Uses wall-clock time alignment — safe for stock data with session gaps.
//!
//! ```text
//! H1.ema(200)          → EMA(200) on confirmed H1 bars (updates on bar close)
//! M15.rsi(14)          → RSI(14) on confirmed M15 bars
//! D1.adx(14)           → ADX(14) on confirmed D1 bars
//! prev_H1.ema(200)     → previous confirmed H1 EMA value (crossover detection)
//! live_H1.ema(200)     → EMA(200) on the forming H1 bar (updates every base bar)
//! ```
//!
//! **Confirmed vs live**: by default (`H1.ema`) the value updates only when the HTF
//! bar closes — no look-ahead bias. `live_H1.ema` recomputes on every incoming base
//! bar using the partially-formed HTF bar, so it reflects current price action within
//! the period. Both modes share the same indicator state; the live value is derived by
//! cloning the confirmed state and applying the forming bar (O(1), cheap).
//!
//! Supported timeframes: `M1` `M5` `M15` `M30` `H1` `H2` `H4` `H6` `H8` `H12` `D1` `W1`.
//!
//! The confirmed HTF bar is emitted when the first base bar of the **next** period
//! arrives — i.e. the previous period's incomplete bar is never used for `H1.ema` etc.
//!
//! | Function             | Indicator                     | Output            |
//! |----------------------|-------------------------------|-------------------|
//! | **MA / Trend**       |                               |                   |
//! | `adx(N)`             | ADX(N)                        | 0–100             |
//! | `plus_di(N)`         | +DI from ADX(N)               | 0–100             |
//! | `minus_di(N)`        | −DI from ADX(N)               | 0–100             |
//! | `dmi_plus(N)`        | DMI +DI(N)                    | 0–100             |
//! | `dmi_minus(N)`       | DMI −DI(N)                    | 0–100             |
//! | `dmi_dx(N)`          | DMI DX(N)                     | 0–100             |
//! | `aroon_up(N)`        | Aroon Up(N)                   | 0–100             |
//! | `aroon_down(N)`      | Aroon Down(N)                 | 0–100             |
//! | `aroon_osc(N)`       | Aroon Oscillator(N)           | -100–100          |
//! | `vortex_plus(N)`     | Vortex +VI(N)                 | ratio             |
//! | `vortex_minus(N)`    | Vortex −VI(N)                 | ratio             |
//! | `alligator_jaw()`    | Alligator jaw (13,8,5)        | price level       |
//! | `alligator_teeth()`  | Alligator teeth               | price level       |
//! | `alligator_lips()`   | Alligator lips                | price level       |
//! | `alligator_bull()`   | Alligator bullish flag        | 0.0 / 1.0         |
//! | `gmma_bull()`        | GMMA bullish flag             | 0.0 / 1.0         |
//! | `kdj_k(N)`           | KDJ %K(N)                     |                   |
//! | `kdj_d(N)`           | KDJ %D(N)                     |                   |
//! | `kdj_j(N)`           | KDJ %J(N)                     |                   |
//! | **Momentum**         |                               |                   |
//! | `rsi(N)`             | RSI(N)                        | 0–100             |
//! | `cci(N)`             | CCI(N)                        | oscillator        |
//! | `roc(N)`             | ROC(N)                        | % change          |
//! | `mom(N)`             | Momentum(N)                   | price diff        |
//! | `cmo(N)`             | CMO(N)                        | -100–100          |
//! | `dpo(N)`             | DPO(N)                        | oscillator        |
//! | `mfi(N)`             | MFI(N)                        | 0–100             |
//! | `williams(N)`        | Williams %R(N)                | -100–0            |
//! | `tsi(N)`             | TSI(N, 13)                    | -100–100          |
//! | `rci(N)`             | RCI(N)                        | -100–100          |
//! | `chop(N)`            | Choppiness(N)                 | 0–100             |
//! | `connors_rsi(N)`     | ConnorsRSI(N, 2, 100)         | 0–100             |
//! | `fisher_line(N)`     | Fisher Transform value(N)     | signed            |
//! | `fisher_sig(N)`      | Fisher Transform signal(N)    | signed            |
//! | `bull_power(N)`      | Bull Power(N)                 | signed            |
//! | `bear_power(N)`      | Bear Power(N)                 | signed            |
//! | `ppo_line(N)`        | PPO line (N, 26, 9)           | %                 |
//! | `ppo_sig(N)`         | PPO signal                    | %                 |
//! | `ppo_hist(N)`        | PPO histogram                 | %                 |
//! | `rvi_line(N)`        | RVI value(N)                  | -1–1              |
//! | `rvi_sig(N)`         | RVI signal(N)                 | -1–1              |
//! | `smi_line(N)`        | SMI value(N)                  | -100–100          |
//! | `smi_sig(N)`         | SMI signal(N)                 | -100–100          |
//! | `bop()`              | Balance of Power              | -1–1              |
//! | `coppock()`          | Coppock Curve (11,14,10)      | signed            |
//! | `kst_line()`         | KST line (standard)           | signed            |
//! | `kst_sig()`          | KST signal (standard)         | signed            |
//! | `pmo_line()`         | PMO line (standard)           | signed            |
//! | `pmo_sig()`          | PMO signal (standard)         | signed            |
//! | `uo()`               | Ultimate Oscillator (7,14,28) | 0–100             |
//! | **Volatility**       |                               |                   |
//! | `atr(N)`             | ATR(N)                        | price range       |
//! | `bb_upper(N)`        | Bollinger upper (N, 2σ)       | price level       |
//! | `bb_lower(N)`        | Bollinger lower (N, 2σ)       | price level       |
//! | `bb_mid(N)`          | Bollinger middle (N)          | price level       |
//! | `stoch_k(N)`         | Stochastic %K(N)              | 0–100             |
//! | `stoch_d(N)`         | Stochastic %D(N)              | 0–100             |
//! | `srsi_k(N)`          | StochRSI %K(N)                | 0–100             |
//! | `srsi_d(N)`          | StochRSI %D(N)                | 0–100             |
//! | `supertrend(N)`      | SuperTrend value (N, 3×ATR)   |                   |
//! | `st_bull(N)`         | SuperTrend bullish flag       | 0.0 / 1.0         |
//! | `donchian_upper(N)`  | Donchian upper(N)             | price level       |
//! | `donchian_lower(N)`  | Donchian lower(N)             | price level       |
//! | `donchian_mid(N)`    | Donchian middle(N)            | price level       |
//! | `chandelier_long(N)` | Chandelier long stop(N, 3×)   | price level       |
//! | `chandelier_short(N)`| Chandelier short stop(N, 3×)  | price level       |
//! | `chande_kroll_long()`| Chande Kroll long stop        | price level       |
//! | `chande_kroll_short()`| Chande Kroll short stop      | price level       |
//! | `chop_angle(N)`      | ChopZone angle(N)             | -90–90 degrees    |
//! | **Volume / Pattern** |                               |                   |
//! | `cmf(N)`             | Chaikin Money Flow(N)         | -1–1              |
//! | `obv()`              | On-Balance Volume             | cumulative vol    |
//! | `vwap()`             | VWAP (session-aware)          | price level       |
//! | `ao()`               | Awesome Oscillator            | signed            |
//! | `sar()`              | Parabolic SAR                 | price level       |
//! | `fractal_bull()`     | Williams Fractal bullish      | 0.0 / 1.0         |
//! | `fractal_bear()`     | Williams Fractal bearish      | 0.0 / 1.0         |
//!
//! Bar fields always in scope: `open`, `high`, `low`, `close`, `volume`.
//!
//! ## History indexing
//!
//! Use `[N]` to access values N bars ago. Works on both bar fields and indicator calls.
//! `[0]` = current bar, `[1]` = one bar ago, `[2]` = two bars ago, and so on.
//!
//! ```text
//! close[1] > close[0]          // price dropped from previous bar
//! ema(9)[1] < ema(9)[0]        // EMA rising
//! close[2] > close[1] && close[1] > close[0]  // two consecutive drops
//! ```
//!
//! The maximum index used in any expression determines the lookback buffer depth.
//!
//! ## Previous-bar shorthand
//!
//! `prev_ema(9)` is equivalent to `ema(9)[1]` — kept for backward compatibility.
//! Prefer `[N]` syntax for consistency when mixing multiple lookback depths.
//!
//! ```text
//! // EMA cross-above (classic form)
//! prev_ema(9) <= prev_ema(21) && ema(9) > ema(21)
//!
//! // MACD histogram zero-cross
//! prev_macd_hist(12) <= 0.0 && macd_hist(12) > 0.0
//!
//! // Stochastic cross in oversold territory
//! prev_stoch_k(14) <= prev_stoch_d(14) && stoch_k(14) > stoch_d(14) && stoch_d(14) < 20.0
//! ```
//!
//! # Examples
//! ```text
//! entry: "rsi(14) < 30.0 && close > ema(50)"
//! exit:  "rsi(14) > 70.0 || close < ema(50)"
//! ```
//! ```text
//! entry: "prev_ema(9) <= prev_ema(21) && ema(9) > ema(21)"
//! exit:  "prev_ema(9) >= prev_ema(21) && ema(9) < ema(21)"
//! ```
//! ```text
//! entry: "close > bb_upper(20) && adx(14) > 25.0 && macd_hist(12) > 0.0"
//! exit:  "close < bb_mid(20) || rsi(14) > 75.0"
//! ```

use std::collections::{HashMap, VecDeque};

use anyhow::Result;
use cel_interpreter::{Context, Program, Value};
use alm_core::{bar::Bar, signal::Signal, strategy::Strategy};
use serde_json::json;

use crate::bar_resampler::{TimeBarResampler, parse_timeframe_ms};
use crate::candle_type::{CandleTransform, CandleType};
use alm_indicator::IndicatorBox;

// ── VarBinding ────────────────────────────────────────────────────────────────

struct VarBinding {
    ind:       IndicatorBox,
    field:     String,
    cached:    Option<f64>,   // last confirmed value (HTF bar fully closed)
    live:      Option<f64>,   // forming value (HTF bar in progress); = cached for base-TF
    prev:      Option<f64>,
    /// Ring buffer of confirmed values, newest at back. Used for `ema(9)[N]` indexing.
    history:   VecDeque<f64>,
    /// Fixed capacity of `history` (lookback + 1). VecDeque auto-grows so we track this manually.
    hist_cap:  usize,
    /// Time-based resampler for MTF indicators (`H1.ema`, `M15.rsi`, etc.).
    /// `None` = same timeframe as the base bars.
    resampler: Option<TimeBarResampler>,
}

impl VarBinding {
    fn update(&mut self, bar: &Bar) -> Option<f64> {
        self.prev = self.cached;

        // MTF: feed indicator only when the time-based resampler completes a HTF bar.
        // Between HTF bars: hold last cached value so the ready-check stays true.
        let agg = match &mut self.resampler {
            Some(rs) => rs.push(bar),
            None     => Some(bar.clone()),
        };

        if let Some(b) = &agg {
            let fields = self.ind.update(b)?;
            self.cached = fields.get(&self.field).copied();
            if let Some(v) = self.cached {
                self.history.push_back(v);
                if self.history.len() > self.hist_cap {
                    self.history.pop_front();
                }
            }
        }

        // Live/forming value: clone confirmed state, feed the forming HTF bar.
        // For base-TF (no resampler) the forming bar IS the current bar → live = cached.
        self.live = match &self.resampler {
            None => self.cached,
            Some(rs) => {
                if let Some(forming) = rs.peek() {
                    let mut tmp = self.ind.clone();
                    tmp.update(&forming).and_then(|f: std::collections::HashMap<String, f64>| {
                        f.get(self.field.as_str()).copied()
                    })
                } else {
                    self.cached
                }
            }
        };

        self.cached
    }

    fn reset(&mut self) {
        self.ind.reset();
        if let Some(rs) = &mut self.resampler { rs.reset(); }
        self.cached = None;
        self.live   = None;
        self.prev   = None;
        self.history.clear();
    }
}

// ── Expression normalizer ─────────────────────────────────────────────────────

/// Convert bare integer literals to float literals so CEL type-checks pass.
/// `rsi(14) < 30`  →  `rsi(14.0) < 30.0`
/// Already-float literals like `30.0` are left unchanged.
fn normalize_cel_expr(expr: &str) -> String {
    let mut out = String::with_capacity(expr.len() + 8);
    let b = expr.as_bytes();
    let n = b.len();
    let mut i = 0;
    while i < n {
        if b[i].is_ascii_digit() {
            // If the char before this digit is part of an identifier (alnum or '_'),
            // these digits are part of an identifier (e.g. `tf4_ema`) — emit as-is.
            let in_ident = i > 0 && (b[i - 1].is_ascii_alphanumeric() || b[i - 1] == b'_');
            if in_ident {
                out.push(b[i] as char);
                i += 1;
                continue;
            }
            // If the char before this digit sequence is '.', this is already the
            // fractional part of a float — don't append another ".0".
            let is_fractional = i > 0 && b[i - 1] == b'.';
            let start = i;
            while i < n && b[i].is_ascii_digit() { i += 1; }
            out.push_str(&expr[start..i]);
            if !is_fractional && (i >= n || b[i] != b'.') {
                out.push_str(".0"); // promote bare integer to float
            }
        } else {
            out.push(b[i] as char);
            i += 1;
        }
    }
    out
}

const BAR_FIELDS: &[&str] = &["open", "high", "low", "close", "volume"];

/// Second-pass expander: rewrites `varname[N]` → `varname_hN`.
///
/// Runs after `expand_indicators` so indicator calls have already been
/// flattened to underscore form (`ema_9`, `rsi_14`, …).
///
/// Returns `(new_expr, lookback)` where `lookback` is the maximum index seen.
/// `lookback = 0` means no history indexing was used.
fn expand_history_indices(expr: &str) -> (String, usize) {
    let b = expr.as_bytes();
    let n = b.len();
    let mut out = String::with_capacity(expr.len());
    let mut max_idx: usize = 0;
    let mut i = 0;

    while i < n {
        if b[i].is_ascii_alphabetic() || b[i] == b'_' {
            // Collect identifier
            let id_start = i;
            while i < n && (b[i].is_ascii_alphanumeric() || b[i] == b'_') { i += 1; }
            let id = &expr[id_start..i];

            // Check for `[N]` immediately after
            if i < n && b[i] == b'[' {
                let bracket_start = i;
                i += 1;
                let num_start = i;
                while i < n && b[i].is_ascii_digit() { i += 1; }
                if i < n && b[i] == b']' && i > num_start {
                    let idx: usize = expr[num_start..i].parse().unwrap_or(0);
                    i += 1; // consume ']'
                    if idx > max_idx { max_idx = idx; }
                    out.push_str(id);
                    out.push_str("_h");
                    out.push_str(&idx.to_string());
                    continue;
                } else {
                    // Not a clean `[N]` — emit as-is
                    out.push_str(id);
                    out.push_str(&expr[bracket_start..i]);
                    continue;
                }
            }

            out.push_str(id);
        } else {
            out.push(b[i] as char);
            i += 1;
        }
    }
    (out, max_idx)
}

// ── TF prefix helpers ─────────────────────────────────────────────────────────

/// If `ident` starts with a timeframe prefix (`H1_`, `M15_`, `D1_`, …),
/// returns `(indicator_base, Some(interval_ms))`.
/// Otherwise returns `(ident, None)`.
fn strip_tf_prefix(ident: &str) -> (&str, Option<i64>) {
    let bytes = ident.as_bytes();
    if bytes.is_empty() { return (ident, None); }
    if !matches!(bytes[0], b'M' | b'H' | b'D' | b'W') { return (ident, None); }

    let mut digit_end = 1;
    while digit_end < bytes.len() && bytes[digit_end].is_ascii_digit() {
        digit_end += 1;
    }
    if digit_end == 1 { return (ident, None); }  // no digits after unit letter
    if digit_end >= bytes.len() || bytes[digit_end] != b'_' { return (ident, None); }

    let base = &ident[digit_end + 1..];
    if base.is_empty() { return (ident, None); }

    match parse_timeframe_ms(&ident[..digit_end]) {
        Some(ms) => (base, Some(ms)),
        None     => (ident, None),
    }
}

// ── Expression scanner ────────────────────────────────────────────────────────

struct Call {
    /// Underscore form (no dot), may carry `prev_` or `live_` prefix.
    /// e.g. `"prev_ema"`, `"H1_ema"`, `"live_H1_ema"`, `"macd_hist"`, `"obv"`.
    raw:     String,
    args:    Vec<f64>, // e.g. [9.0], []
    #[allow(dead_code)] // read in tests to assert live_ prefix was parsed
    is_live: bool,     // true when `live_` prefix was present
}

impl Call {
    /// Returns `(indicator_base, is_prev, interval_ms)`.
    fn parts(&self) -> (&str, bool, Option<i64>) {
        let (no_prev, is_prev) = match self.raw.strip_prefix("prev_") {
            Some(b) => (b, true),
            None    => (self.raw.as_str(), false),
        };
        let (base, interval_ms) = strip_tf_prefix(no_prev);
        (base, is_prev, interval_ms)
    }

    /// Canonical binding key — strips `prev_`/`live_`, keeps TF prefix, appends args.
    /// `"H1_ema_200"`, `"M15_rsi_14"`, `"ema_9"`, `"obv"`.
    fn key(&self) -> String {
        let no_prev = self.raw.strip_prefix("prev_").unwrap_or(&self.raw);
        if self.args.is_empty() {
            no_prev.to_string()
        } else {
            let parts: Vec<String> = self.args.iter().map(|a| format!("{}", *a as i64)).collect();
            format!("{}_{}", no_prev, parts.join("_"))
        }
    }
}

/// Single-pass scanner + expander.
///
/// Walks `expr`, recognises indicator call patterns in their natural dot form
/// (`H1.ema(200)`, `prev_M15.rsi(14)`, `ema(9)`, `obv()`) and replaces each
/// with its pre-allocated CEL variable name (`H1_ema_200`, `prev_M15_rsi_14`,
/// `ema_9`, `obv`).  All other tokens are emitted verbatim.
///
/// Returns `(expanded_expression, calls)` where `calls` contains one entry per
/// **unique logical indicator** found (deduplication happens in
/// `build_bindings_from_calls`).
fn expand_indicators(expr: &str) -> (String, Vec<Call>) {
    let mut out   = String::with_capacity(expr.len());
    let mut calls = Vec::new();
    let b = expr.as_bytes();
    let n = b.len();
    let mut i = 0;

    while i < n {
        if b[i].is_ascii_alphabetic() || b[i] == b'_' {
            if let Some((end, call, var_name)) = try_match_indicator(expr, i) {
                out.push_str(&var_name);
                calls.push(call);
                i = end;
            } else {
                // Ordinary identifier — emit as-is
                let id_start = i;
                while i < n && (b[i].is_ascii_alphanumeric() || b[i] == b'_') { i += 1; }
                out.push_str(&expr[id_start..i]);
            }
        } else {
            out.push(b[i] as char);
            i += 1;
        }
    }
    (out, calls)
}

/// Try to match one complete indicator call starting at `start`.
///
/// Handles all six forms:
/// - `ema(9)`           → var `ema_9`
/// - `prev_ema(9)`      → var `prev_ema_9`
/// - `H1.ema(200)`      → var `H1_ema_200`   (dot form — preferred)
/// - `prev_H1.ema(200)` → var `prev_H1_ema_200`
/// - `H1_ema(200)`      → var `H1_ema_200`   (underscore form — also accepted)
/// - `prev_H1_ema(200)` → var `prev_H1_ema_200`
///
/// Returns `None` if the text at `start` is not a known indicator call.
fn try_match_indicator(expr: &str, start: usize) -> Option<(usize, Call, String)> {
    let b = expr.as_bytes();
    let n = expr.len();
    let mut i = start;

    // Optional `prev_` or `live_` prefix (mutually exclusive)
    let is_prev = expr[i..].starts_with("prev_");
    let is_live = !is_prev && expr[i..].starts_with("live_");
    if is_prev || is_live {
        i += 5;
        if i >= n { return None; }
    }

    // Optional TF prefix: [MHDW]\d+\.  (e.g. `H1.`, `M15.`, `D1.`)
    let tf_start = i;
    let mut tf_token = "";
    let _interval_ms: Option<i64> = if i < n && matches!(b[i], b'M' | b'H' | b'D' | b'W') {
        let ts = i;
        i += 1;
        while i < n && b[i].is_ascii_digit() { i += 1; }
        let candidate = &expr[ts..i];
        if i < n && b[i] == b'.' && parse_timeframe_ms(candidate).is_some() {
            tf_token = candidate;
            let ms = parse_timeframe_ms(candidate);
            i += 1; // consume '.'
            ms
        } else {
            i = tf_start; // rewind — not a valid TF prefix
            None
        }
    } else {
        None
    };

    // Indicator base name
    if i >= n || !(b[i].is_ascii_alphabetic() || b[i] == b'_') { return None; }
    let name_start = i;
    while i < n && (b[i].is_ascii_alphanumeric() || b[i] == b'_') { i += 1; }
    let base_name = &expr[name_start..i];

    // Must be followed by '('
    if i >= n || b[i] != b'(' { return None; }

    // If no dot TF was detected, try underscore TF prefix embedded in base_name.
    // e.g. `H1_ema(200)` → resolved_base = `ema`, resolved_tf = `H1`
    let (resolved_base, resolved_tf): (&str, &str) = if tf_token.is_empty() {
        let (stripped, ms) = strip_tf_prefix(base_name);
        if ms.is_some() {
            // tf_part is everything before the trailing `_<stripped>` suffix
            let tf_part = &base_name[..base_name.len() - stripped.len() - 1];
            (stripped, tf_part)
        } else {
            (base_name, "")
        }
    } else {
        (base_name, tf_token)
    };

    // Must be a known indicator
    if !ONE_ARG_FUNCS.contains(&resolved_base) && !ZERO_ARG_FUNCS.contains(&resolved_base) {
        return None;
    }

    // Consume argument list
    i += 1; // skip '('
    let arg_start = i;
    let mut depth = 1usize;
    while i < n && depth > 0 {
        match b[i] { b'(' => depth += 1, b')' => depth -= 1, _ => {} }
        i += 1;
    }
    let args_str = &expr[arg_start..i - 1];
    let args: Vec<f64> = args_str.split(',')
        .filter_map(|s| s.trim().parse::<f64>().ok())
        .collect();

    // Build `Call.raw` in underscore form (strips `prev_`/`live_`, keeps TF prefix)
    let raw = match resolved_tf.is_empty() {
        true  => resolved_base.to_string(),
        false => format!("{resolved_tf}_{resolved_base}"),
    };

    let call = Call { raw, args, is_live };
    let var_name = if is_prev {
        format!("prev_{}", call.key())
    } else if is_live {
        format!("live_{}", call.key())
    } else {
        call.key()
    };

    Some((i, call, var_name))
}

// ── Indicator factory ─────────────────────────────────────────────────────────

fn make_binding(base: &str, args: &[f64], interval_ms: Option<i64>, lookback: usize) -> Result<Option<VarBinding>> {
    let n = args.first().copied().unwrap_or(0.0) as usize;

    macro_rules! bind {
        ($cfg:expr, $field:expr) => {{
            let resampler = interval_ms.map(TimeBarResampler::new);
            let cap = lookback + 1;
            Ok(Some(VarBinding {
                ind:       IndicatorBox::from_config(&$cfg)?,
                field:     $field.into(),
                cached:    None,
                live:      None,
                prev:      None,
                history:   VecDeque::with_capacity(cap),
                hist_cap:  cap,
                resampler,
            }))
        }};
    }

    match base {
        "ema"         => bind!(json!({"type":"ema","period":n}),                                                    "value"),
        "sma"         => bind!(json!({"type":"sma","period":n}),                                                    "value"),
        "wma"         => bind!(json!({"type":"wma","period":n}),                                                    "value"),
        "hma"         => bind!(json!({"type":"hma","period":n}),                                                    "value"),
        "tema"        => bind!(json!({"type":"tema","period":n}),                                                   "value"),
        "kama"        => bind!(json!({"type":"kama","er_period":n}),                                                "value"),
        "rsi"         => bind!(json!({"type":"rsi","period":n}),                                                    "value"),
        "cci"         => bind!(json!({"type":"cci","period":n}),                                                    "value"),
        "roc"         => bind!(json!({"type":"roc","period":n}),                                                    "value"),
        "mfi"         => bind!(json!({"type":"mfi","period":n}),                                                    "value"),
        "williams"    => bind!(json!({"type":"williams_r","period":n}),                                             "value"),
        "tsi"         => bind!(json!({"type":"tsi","first":n,"second":13}),                                         "value"),
        "chop"        => bind!(json!({"type":"chop","period":n}),                                                   "value"),
        "connors_rsi" => bind!(json!({"type":"connors_rsi","rsi_period":n,"streak_period":2,"rank_period":100}),    "value"),
        "atr"         => bind!(json!({"type":"atr","period":n}),                                                    "atr"),
        "adx"         => bind!(json!({"type":"adx","period":n}),                                                    "adx"),
        "plus_di"     => bind!(json!({"type":"adx","period":n}),                                                    "plus_di"),
        "minus_di"    => bind!(json!({"type":"adx","period":n}),                                                    "minus_di"),
        "macd_hist"   => bind!(json!({"type":"macd","fast":n,"slow":26,"signal":9}),                                "histogram"),
        "macd_line"   => bind!(json!({"type":"macd","fast":n,"slow":26,"signal":9}),                                "macd"),
        "bb_upper"    => bind!(json!({"type":"bbands","period":n,"multiplier":2.0}),                                "upper"),
        "bb_lower"    => bind!(json!({"type":"bbands","period":n,"multiplier":2.0}),                                "lower"),
        "bb_mid"      => bind!(json!({"type":"bbands","period":n,"multiplier":2.0}),                                "middle"),
        "bb_pct"      => bind!(json!({"type":"bbands","period":n,"multiplier":2.0}),                                "percent_b"),
        "bb_bw"       => bind!(json!({"type":"bbands","period":n,"multiplier":2.0}),                                "bandwidth"),
        "stoch_k"     => bind!(json!({"type":"stochastic","k_period":n,"d_period":3}),                             "k"),
        "stoch_d"     => bind!(json!({"type":"stochastic","k_period":n,"d_period":3}),                             "d"),
        "srsi_k"      => bind!(json!({"type":"stoch_rsi","rsi_period":n,"smooth_d":3}),                             "k"),
        "srsi_d"      => bind!(json!({"type":"stoch_rsi","rsi_period":n,"smooth_d":3}),                             "d"),
        "kdj_k"       => bind!(json!({"type":"kdj","period":n}),                                                    "k"),
        "kdj_d"       => bind!(json!({"type":"kdj","period":n}),                                                    "d"),
        "kdj_j"       => bind!(json!({"type":"kdj","period":n}),                                                    "j"),
        "supertrend"  => bind!(json!({"type":"supertrend","period":n,"multiplier":3.0}),                            "value"),
        "st_bull"     => bind!(json!({"type":"supertrend","period":n,"multiplier":3.0}),                            "bullish"),
        "cmf"         => bind!(json!({"type":"cmf","period":n}),                                                    "value"),
        "obv"         => bind!(json!({"type":"obv"}),                                                               "value"),
        "ao"          => bind!(json!({"type":"ao","fast":5,"slow":34}),                                             "value"),
        "sar"         => bind!(json!({"type":"parabolic_sar","step":0.02,"max":0.2}),                               "sar"),

        // ── New trend / MA ───────────────────────────────────────────────────
        "dema"              => bind!(json!({"type":"dema","period":n}),                                             "value"),
        "smma"              => bind!(json!({"type":"smma","period":n}),                                             "value"),
        "alma"              => bind!(json!({"type":"alma","period":n}),                                             "value"),
        "mcginley"          => bind!(json!({"type":"mcginley","period":n}),                                         "value"),
        "lsma"              => bind!(json!({"type":"lsma","period":n}),                                             "value"),
        "lsma_slope"        => bind!(json!({"type":"lsma","period":n}),                                             "slope"),
        "vwma"              => bind!(json!({"type":"vwma","period":n}),                                             "value"),
        "dmi_plus"          => bind!(json!({"type":"dmi","period":n}),                                              "plus_di"),
        "dmi_minus"         => bind!(json!({"type":"dmi","period":n}),                                              "minus_di"),
        "dmi_dx"            => bind!(json!({"type":"dmi","period":n}),                                              "dx"),
        "aroon_up"          => bind!(json!({"type":"aroon","period":n}),                                            "up"),
        "aroon_down"        => bind!(json!({"type":"aroon","period":n}),                                            "down"),
        "aroon_osc"         => bind!(json!({"type":"aroon","period":n}),                                            "oscillator"),
        "vortex_plus"       => bind!(json!({"type":"vortex","period":n}),                                           "plus_vi"),
        "vortex_minus"      => bind!(json!({"type":"vortex","period":n}),                                           "minus_vi"),

        // ── New momentum ─────────────────────────────────────────────────────
        "mom"               => bind!(json!({"type":"mom","period":n}),                                              "value"),
        "cmo"               => bind!(json!({"type":"cmo","period":n}),                                              "value"),
        "dpo"               => bind!(json!({"type":"dpo","period":n}),                                              "value"),
        "rci"               => bind!(json!({"type":"rci","period":n}),                                              "value"),
        "fisher_line"       => bind!(json!({"type":"fisher","period":n}),                                           "fisher"),
        "fisher_sig"        => bind!(json!({"type":"fisher","period":n}),                                           "signal"),
        "bull_power"        => bind!(json!({"type":"bull_bear","period":n}),                                        "bull"),
        "bear_power"        => bind!(json!({"type":"bull_bear","period":n}),                                        "bear"),
        "ppo_line"          => bind!(json!({"type":"ppo","fast":n,"slow":26,"signal":9}),                           "ppo"),
        "ppo_sig"           => bind!(json!({"type":"ppo","fast":n,"slow":26,"signal":9}),                           "signal"),
        "ppo_hist"          => bind!(json!({"type":"ppo","fast":n,"slow":26,"signal":9}),                           "histogram"),
        "rvi_line"          => bind!(json!({"type":"rvi","period":n}),                                              "rvi"),
        "rvi_sig"           => bind!(json!({"type":"rvi","period":n}),                                              "signal"),
        "smi_line"          => bind!(json!({"type":"smi"}),                                                         "smi"),
        "smi_sig"           => bind!(json!({"type":"smi"}),                                                         "signal"),

        // ── New volatility ───────────────────────────────────────────────────
        "donchian_upper"    => bind!(json!({"type":"donchian","period":n}),                                         "upper"),
        "donchian_lower"    => bind!(json!({"type":"donchian","period":n}),                                         "lower"),
        "donchian_mid"      => bind!(json!({"type":"donchian","period":n}),                                         "middle"),
        "keltner_upper"     => bind!(json!({"type":"keltner","period":n,"atr_period":10,"multiplier":2.0}),          "upper"),
        "keltner_lower"     => bind!(json!({"type":"keltner","period":n,"atr_period":10,"multiplier":2.0}),          "lower"),
        "keltner_mid"       => bind!(json!({"type":"keltner","period":n,"atr_period":10,"multiplier":2.0}),          "middle"),
        "rwi_high"          => bind!(json!({"type":"rwi","period":n}),                                               "rwi_high"),
        "rwi_low"           => bind!(json!({"type":"rwi","period":n}),                                               "rwi_low"),
        "chandelier_long"   => bind!(json!({"type":"chandelier_exit","period":n,"multiplier":3.0}),                 "long_stop"),
        "chandelier_short"  => bind!(json!({"type":"chandelier_exit","period":n,"multiplier":3.0}),                 "short_stop"),
        "chop_angle"        => bind!(json!({"type":"chop_zone","ema_period":n,"threshold":5.0}),                    "angle"),

        // ── Zero-arg additions ────────────────────────────────────────────────
        "bop"               => bind!(json!({"type":"bop"}),                                                         "value"),
        "coppock"           => bind!(json!({"type":"coppock"}),                                                     "value"),
        "kst_line"          => bind!(json!({"type":"kst"}),                                                         "kst"),
        "kst_sig"           => bind!(json!({"type":"kst"}),                                                         "signal"),
        "pmo_line"          => bind!(json!({"type":"pmo"}),                                                         "pmo"),
        "pmo_sig"           => bind!(json!({"type":"pmo"}),                                                         "signal"),
        "uo"                => bind!(json!({"type":"uo","fast":7,"medium":14,"slow":28}),                            "value"),
        "vwap"              => bind!(json!({"type":"vwap"}),                                                         "value"),
        "alligator_jaw"     => bind!(json!({"type":"alligator","jaw":13,"teeth":8,"lips":5}),                       "jaw"),
        "alligator_teeth"   => bind!(json!({"type":"alligator","jaw":13,"teeth":8,"lips":5}),                       "teeth"),
        "alligator_lips"    => bind!(json!({"type":"alligator","jaw":13,"teeth":8,"lips":5}),                       "lips"),
        "alligator_bull"    => bind!(json!({"type":"alligator","jaw":13,"teeth":8,"lips":5}),                       "bullish"),
        "gmma_bull"         => bind!(json!({"type":"gmma"}),                                                         "bullish"),
        "chande_kroll_long" => bind!(json!({"type":"chande_kroll","atr_period":10,"factor":1.5,"stop_period":9}),   "stop_long"),
        "chande_kroll_short"=> bind!(json!({"type":"chande_kroll","atr_period":10,"factor":1.5,"stop_period":9}),   "stop_short"),
        "fractal_bull"      => bind!(json!({"type":"fractal"}),                                                      "bullish"),
        "fractal_bear"      => bind!(json!({"type":"fractal"}),                                                      "bearish"),

        _             => Ok(None),
    }
}

/// Returns the `IndicatorBox`-compatible JSON config for a CEL indicator call.
///
/// Mirrors the dispatch in `make_binding`, but returns only the config (no
/// `IndicatorBox` allocated). Used by `cel_indicator_deps` to declare
/// dependencies without instantiating indicators.
///
/// Returns `None` for unrecognised base names — callers should treat this as
/// "no dependency" rather than an error (CEL accepts arbitrary identifiers as
/// CEL variables).
pub fn cel_indicator_config(base: &str, args: &[f64]) -> Option<serde_json::Value> {
    let n = args.first().copied().unwrap_or(0.0) as usize;
    let cfg = match base {
        // ── MA / trend ───────────────────────────────────────────────────────
        "ema"          => json!({"type":"ema","period":n}),
        "sma"          => json!({"type":"sma","period":n}),
        "wma"          => json!({"type":"wma","period":n}),
        "hma"          => json!({"type":"hma","period":n}),
        "tema"         => json!({"type":"tema","period":n}),
        "dema"         => json!({"type":"dema","period":n}),
        "smma"         => json!({"type":"smma","period":n}),
        "alma"         => json!({"type":"alma","period":n}),
        "mcginley"     => json!({"type":"mcginley","period":n}),
        "lsma" | "lsma_slope" => json!({"type":"lsma","period":n}),
        "vwma"         => json!({"type":"vwma","period":n}),
        "kama"         => json!({"type":"kama","er_period":n}),
        "macd_hist" | "macd_line" => json!({"type":"macd","fast":n,"slow":26,"signal":9}),
        "adx" | "plus_di" | "minus_di" => json!({"type":"adx","period":n}),
        "dmi_plus" | "dmi_minus" | "dmi_dx" => json!({"type":"dmi","period":n}),
        "aroon_up" | "aroon_down" | "aroon_osc" => json!({"type":"aroon","period":n}),
        "vortex_plus" | "vortex_minus" => json!({"type":"vortex","period":n}),
        "kdj_k" | "kdj_d" | "kdj_j" => json!({"type":"kdj","period":n}),
        "supertrend" | "st_bull" => json!({"type":"supertrend","period":n,"multiplier":3.0}),

        // ── Momentum ─────────────────────────────────────────────────────────
        "rsi"          => json!({"type":"rsi","period":n}),
        "cci"          => json!({"type":"cci","period":n}),
        "roc"          => json!({"type":"roc","period":n}),
        "mfi"          => json!({"type":"mfi","period":n}),
        "williams"     => json!({"type":"williams_r","period":n}),
        "tsi"          => json!({"type":"tsi","first":n,"second":13}),
        "chop"         => json!({"type":"chop","period":n}),
        "connors_rsi"  => json!({"type":"connors_rsi","rsi_period":n,"streak_period":2,"rank_period":100}),
        "mom"          => json!({"type":"mom","period":n}),
        "cmo"          => json!({"type":"cmo","period":n}),
        "dpo"          => json!({"type":"dpo","period":n}),
        "rci"          => json!({"type":"rci","period":n}),
        "fisher_line" | "fisher_sig" => json!({"type":"fisher","period":n}),
        "bull_power" | "bear_power"  => json!({"type":"bull_bear","period":n}),
        "ppo_line" | "ppo_sig" | "ppo_hist" => json!({"type":"ppo","fast":n,"slow":26,"signal":9}),
        "rvi_line" | "rvi_sig" => json!({"type":"rvi","period":n}),
        "smi_line" | "smi_sig" => json!({"type":"smi"}),
        "stoch_k" | "stoch_d"  => json!({"type":"stochastic","k_period":n,"d_period":3}),
        "srsi_k" | "srsi_d"    => json!({"type":"stoch_rsi","rsi_period":n,"smooth_d":3}),

        // ── Volatility ───────────────────────────────────────────────────────
        "atr"          => json!({"type":"atr","period":n}),
        "bb_upper" | "bb_lower" | "bb_mid" | "bb_pct" | "bb_bw" =>
            json!({"type":"bbands","period":n,"multiplier":2.0}),
        "donchian_upper" | "donchian_lower" | "donchian_mid" =>
            json!({"type":"donchian","period":n}),
        "keltner_upper" | "keltner_lower" | "keltner_mid" =>
            json!({"type":"keltner","period":n,"atr_period":10,"multiplier":2.0}),
        "rwi_high" | "rwi_low" => json!({"type":"rwi","period":n}),
        "chandelier_long" | "chandelier_short" =>
            json!({"type":"chandelier_exit","period":n,"multiplier":3.0}),
        "chop_angle"   => json!({"type":"chop_zone","ema_period":n,"threshold":5.0}),

        // ── Volume / pattern ─────────────────────────────────────────────────
        "cmf"          => json!({"type":"cmf","period":n}),
        "obv"          => json!({"type":"obv"}),
        "ao"           => json!({"type":"ao","fast":5,"slow":34}),
        "sar"          => json!({"type":"parabolic_sar","step":0.02,"max":0.2}),
        "vwap"         => json!({"type":"vwap"}),

        // ── Zero-arg ─────────────────────────────────────────────────────────
        "bop"              => json!({"type":"bop"}),
        "coppock"          => json!({"type":"coppock"}),
        "kst_line" | "kst_sig" => json!({"type":"kst"}),
        "pmo_line" | "pmo_sig" => json!({"type":"pmo"}),
        "uo"               => json!({"type":"uo","fast":7,"medium":14,"slow":28}),
        "alligator_jaw" | "alligator_teeth" | "alligator_lips" | "alligator_bull" =>
            json!({"type":"alligator","jaw":13,"teeth":8,"lips":5}),
        "gmma_bull"        => json!({"type":"gmma"}),
        "chande_kroll_long" | "chande_kroll_short" =>
            json!({"type":"chande_kroll","atr_period":10,"factor":1.5,"stop_period":9}),
        "fractal_bull" | "fractal_bear" => json!({"type":"fractal"}),

        _ => return None,
    };
    Some(cfg)
}

/// Extract the `entry` and `exit` expression strings from a CEL params object.
///
/// Mirrors `CelStrategy::from_params`: accepts either the `"script"` textarea
/// form or the legacy `"entry"` / `"exit"` split form. Returns empty strings if
/// parsing fails — callers treat that as "no deps" rather than propagating.
fn extract_cel_entry_exit(p: &serde_json::Value) -> (String, String) {
    if let Some(script) = p.get("script").and_then(|v| v.as_str()) {
        if let Ok(s) = CelScript::parse(script) {
            let entry = p.get("entry").and_then(|v| v.as_str()).map(str::to_string)
                .unwrap_or(s.entry);
            let exit = p.get("exit").and_then(|v| v.as_str()).map(str::to_string)
                .unwrap_or(s.exit);
            return (entry, exit);
        }
    }
    let entry = p.get("entry").and_then(|v| v.as_str()).unwrap_or("").to_string();
    let exit  = p.get("exit").and_then(|v| v.as_str()).unwrap_or("").to_string();
    (entry, exit)
}

/// Extract the indicator dependencies declared by a CEL strategy's params.
///
/// Parses both `entry` and `exit` expressions, collects every recognised
/// indicator call, filters out MTF ones (those with a TF prefix — `H1.ema(200)`),
/// and deduplicates by canonical config shape.
///
/// Called by `alm_strategy::factory::build_strategy_with_deps`.
pub fn cel_indicator_deps(params: &serde_json::Value) -> Vec<crate::factory::IndicatorDep> {
    use std::collections::BTreeSet;

    let (entry, exit) = extract_cel_entry_exit(params);
    let (_, entry_calls) = expand_indicators(&entry);
    let (_, exit_calls)  = expand_indicators(&exit);

    let mut seen: BTreeSet<String> = BTreeSet::new();
    let mut deps = Vec::new();

    for call in entry_calls.into_iter().chain(exit_calls.into_iter()) {
        let (base, _is_prev, interval_ms) = call.parts();
        if interval_ms.is_some() {
            continue;
        }
        let Some(cfg) = cel_indicator_config(base, &call.args) else {
            continue;
        };
        let key = cfg.to_string();
        if seen.insert(key) {
            deps.push(crate::factory::IndicatorDep { config: cfg, source_tf: None });
        }
    }
    deps
}

/// Parse a single CEL-style indicator expression into its components.
///
/// Supports MTF dot-notation: `"H1.ema(200)"` → `("ema", vec![200.0], Some(3_600_000))`.
/// Plain expressions: `"rsi(14)"` → `("rsi", vec![14.0], None)`.
/// Zero-arg: `"obv()"` → `("obv", vec![], None)`.
///
/// Returns an error if no recognisable indicator call is found.
pub fn parse_cel_indicator(expr: &str) -> Result<(String, Vec<f64>, Option<i64>)> {
    let (_, calls) = expand_indicators(expr.trim());
    let call = calls.into_iter()
        .find(|c| {
            let (base, _, _) = c.parts();
            ONE_ARG_FUNCS.contains(&base) || ZERO_ARG_FUNCS.contains(&base)
        })
        .ok_or_else(|| anyhow::anyhow!("no recognised indicator call in CEL expression: '{expr}'"))?;

    let (base, _, interval_ms) = call.parts();
    Ok((base.to_string(), call.args.clone(), interval_ms))
}

fn build_bindings_from_calls(calls: impl IntoIterator<Item = Call>, lookback: usize) -> Result<HashMap<String, VarBinding>> {
    let mut map: HashMap<String, VarBinding> = HashMap::new();
    for call in calls {
        let (base, _, interval_ms) = call.parts();
        let key = call.key();
        if !map.contains_key(&key) {
            if let Some(b) = make_binding(base, &call.args, interval_ms, lookback)? {
                map.insert(key, b);
            }
        }
    }
    Ok(map)
}

// ── Function lists ────────────────────────────────────────────────────────────

const ONE_ARG_FUNCS: &[&str] = &[
    // MA / trend
    "ema", "sma", "wma", "hma", "dema", "tema", "smma", "alma", "mcginley",
    "lsma", "lsma_slope", "vwma", "kama",
    "macd_hist", "macd_line",
    "adx", "plus_di", "minus_di",
    "dmi_plus", "dmi_minus", "dmi_dx",
    "aroon_up", "aroon_down", "aroon_osc",
    "vortex_plus", "vortex_minus",
    "kdj_k", "kdj_d", "kdj_j",
    "supertrend", "st_bull",
    // momentum
    "rsi", "cci", "roc", "mom", "cmo", "dpo", "mfi", "williams",
    "tsi", "rci", "chop", "connors_rsi",
    "stoch_k", "stoch_d", "srsi_k", "srsi_d",
    "fisher_line", "fisher_sig",
    "bull_power", "bear_power",
    "ppo_line", "ppo_sig", "ppo_hist",
    "rvi_line", "rvi_sig",
    "smi_line", "smi_sig",
    // volatility
    "atr",
    "bb_upper", "bb_lower", "bb_mid", "bb_pct", "bb_bw",
    "donchian_upper", "donchian_lower", "donchian_mid",
    "keltner_upper", "keltner_lower", "keltner_mid",
    "rwi_high", "rwi_low",
    "chandelier_long", "chandelier_short",
    "chop_angle",
    // volume
    "cmf",
];

const ZERO_ARG_FUNCS: &[&str] = &[
    "obv", "ao", "sar", "vwap",
    "bop", "coppock", "kst_line", "kst_sig", "pmo_line", "pmo_sig", "uo",
    "alligator_jaw", "alligator_teeth", "alligator_lips", "alligator_bull",
    "gmma_bull",
    "chande_kroll_long", "chande_kroll_short",
    "fractal_bull", "fractal_bear",
];

// ── CEL script parser ─────────────────────────────────────────────────────────

/// Parsed representation of a multi-line CEL script.
///
/// The script format lets a UI send a single textarea value instead of
/// separate JSON fields:
///
/// ```text
/// entry: rsi(14) < 30.0 && close > ema(50)
/// exit:  rsi(14) > 70.0 || close < ema(50)
/// tp:    0.05
/// sl:    0.02
/// # comment — ignored
/// candle_type: heiken_ashi
/// ```
///
/// History indexing `[N]` works in both entry and exit expressions:
/// ```text
/// entry: close[1] > close[0] && rsi(14)[1] < 30.0
/// exit:  ema(9)[0] < ema(9)[1]
/// ```
///
/// Supported keys: `entry`, `exit`, `tp`, `sl`, `tp_atr`, `sl_atr`,
/// `atr_period`, `candle_type`, `ha_smooth`.
///
/// Rules:
/// - Lines starting with `#` or blank are ignored.
/// - Continuation lines (no recognised `key:` prefix) are joined with a
///   space onto the previous `entry` or `exit` section — useful for
///   multi-line expressions with leading `&&` / `||`.
/// - JSON params in `from_params` override the same keys from the script.
pub struct CelScript {
    pub entry:       String,
    pub exit:        String,
    pub tp:          Option<f64>,
    pub sl:          Option<f64>,
    pub tp_atr:      Option<f64>,
    pub sl_atr:      Option<f64>,
    pub atr_period:  Option<usize>,
    pub candle_type: String,
    pub ha_smooth:   usize,
}

impl CelScript {
    pub fn parse(script: &str) -> anyhow::Result<Self> {
        let mut entry       = String::new();
        let mut exit        = String::new();
        let mut tp:         Option<f64>   = None;
        let mut sl:         Option<f64>   = None;
        let mut tp_atr:     Option<f64>   = None;
        let mut sl_atr:     Option<f64>   = None;
        let mut atr_period: Option<usize> = None;
        let mut candle_type = String::from("raw");
        let mut ha_smooth: usize = 2;
        // 0 = entry section, 1 = exit section, None = numeric/string key
        let mut last_section: Option<u8> = None;

        for raw in script.lines() {
            let line = raw.trim();
            if line.is_empty() || line.starts_with('#') {
                continue;
            }

            macro_rules! pf {
                ($rest:expr, $dst:ident) => {
                    $dst = $rest.trim().parse::<f64>().ok();
                    last_section = None;
                };
            }

            if let Some(rest) = line.strip_prefix("entry:") {
                if !entry.is_empty() { entry.push(' '); }
                entry.push_str(rest.trim());
                last_section = Some(0);
            } else if let Some(rest) = line.strip_prefix("exit:") {
                if !exit.is_empty() { exit.push(' '); }
                exit.push_str(rest.trim());
                last_section = Some(1);
            } else if let Some(rest) = line.strip_prefix("tp:")         { pf!(rest, tp); }
            else if let Some(rest) = line.strip_prefix("sl:")           { pf!(rest, sl); }
            else if let Some(rest) = line.strip_prefix("tp_atr:")       { pf!(rest, tp_atr); }
            else if let Some(rest) = line.strip_prefix("sl_atr:")       { pf!(rest, sl_atr); }
            else if let Some(rest) = line.strip_prefix("atr_period:") {
                atr_period = rest.trim().parse::<usize>().ok();
                last_section = None;
            } else if let Some(rest) = line.strip_prefix("candle_type:") {
                candle_type = rest.trim().to_string();
                last_section = None;
            } else if let Some(rest) = line.strip_prefix("ha_smooth:") {
                ha_smooth = rest.trim().parse::<usize>().unwrap_or(2);
                last_section = None;
            } else {
                // Continuation: join onto last expression section
                match last_section {
                    Some(0) => { entry.push(' '); entry.push_str(line); }
                    Some(1) => { exit.push(' ');  exit.push_str(line); }
                    _ => anyhow::bail!(
                        "CEL script: unexpected line with no active section: {line:?}"
                    ),
                }
            }
        }

        anyhow::ensure!(!entry.is_empty(), "CEL script must contain an 'entry:' line");

        Ok(Self { entry, exit, tp, sl, tp_atr, sl_atr,
                  atr_period, candle_type, ha_smooth })
    }
}

// ── Strategy ──────────────────────────────────────────────────────────────────

pub struct CelStrategy {
    entry_prog:  Program,
    exit_prog:   Program,
    /// Indicator state, keyed by canonical name (e.g. `"rsi_14"`).
    /// Kept as HashMap so external tests can assert on `contains_key`.
    bindings:    HashMap<String, VarBinding>,
    /// Pre-allocated `(current_var_name, prev_var_name, live_var_name)` strings for the hot path.
    /// Order matches `bindings.keys()` at construction time.
    var_keys:    Vec<(String, String, String)>,
    /// Ring buffer of effective (post-transform) bars for `close[N]` etc.
    /// Capacity = `lookback + 1`; index 0 = oldest, back = newest (current).
    bar_buf:     VecDeque<Bar>,
    /// Maximum history index used in expressions (`close[N]` → N). 0 = no indexing used.
    lookback:    usize,
    /// Accumulated indicator series — drained by `take_indicator_series()`.
    /// Key = binding key (e.g. `"ema_9"`), value = `(timestamp_ms, value)` per bar.
    indicator_series: HashMap<String, Vec<(i64, f64)>>,
    in_position: bool,
    entry_price: f64,
    /// Fixed % TP/SL from entry price.
    tp_pct:      Option<f64>,
    sl_pct:      Option<f64>,
    /// ATR-based fixed levels (computed at entry, stored as absolute price).
    tp_atr_mult: Option<f64>,
    sl_atr_mult: Option<f64>,
    tp_atr_level: f64,
    sl_atr_level: f64,
    /// Internal ATR — only allocated when any ATR mode is active.
    atr:      Option<alm_indicator::Atr>,
    last_atr: f64,
    /// CEL context; indicator values written as variables each bar — no closures,
    /// no mutex, no `format!` in the hot path.
    ctx:         Context<'static>,
    /// Candle transform — Raw (pass-through) hoặc HeikenAshi.
    transform:   CandleTransform,
}

impl CelStrategy {
    pub fn new(entry: &str, exit: &str) -> Result<Self> {
        Self::build(entry, exit, None, None, None, None, None, CandleType::Raw)
    }

    pub fn with_risk(
        entry:  &str,
        exit:   &str,
        tp_pct: Option<f64>,
        sl_pct: Option<f64>,
    ) -> Result<Self> {
        Self::build(entry, exit, tp_pct, sl_pct, None, None, None, CandleType::Raw)
    }

    pub fn from_params(p: &serde_json::Value) -> Result<Self> {
        // ── Script format (single textarea) ──────────────────────────────────
        // If "script" is present, parse it first; JSON fields act as overrides.
        if let Some(script) = p.get("script").and_then(|v| v.as_str()) {
            let s = CelScript::parse(script)?;
            let ha_smooth = p.get("ha_smooth").and_then(serde_json::Value::as_f64)
                .map(|v| v as usize).unwrap_or(s.ha_smooth);
            let candle_type = CandleType::from_str(
                p.get("candle_type").and_then(|v| v.as_str()).unwrap_or(&s.candle_type),
                ha_smooth,
            );
            return Self::build(
                &s.entry,
                if s.exit.is_empty() { "false" } else { &s.exit },
                p.get("tp").and_then(serde_json::Value::as_f64).or(s.tp),
                p.get("sl").and_then(serde_json::Value::as_f64).or(s.sl),
                p.get("tp_atr").and_then(serde_json::Value::as_f64).or(s.tp_atr),
                p.get("sl_atr").and_then(serde_json::Value::as_f64).or(s.sl_atr),
                p.get("atr_period").and_then(serde_json::Value::as_f64)
                    .map(|v| v as usize).or(s.atr_period),
                candle_type,
            );
        }

        // ── Legacy: separate entry / exit fields ──────────────────────────────
        let entry = p.get("entry").and_then(|v| v.as_str()).unwrap_or("false");
        let exit  = p.get("exit").and_then(|v| v.as_str()).unwrap_or("false");
        let atr_period = p.get("atr_period").and_then(serde_json::Value::as_f64).map(|v| v as usize);
        let ha_smooth = p.get("ha_smooth").and_then(serde_json::Value::as_f64).map(|v| v as usize).unwrap_or(2);
        let candle_type = CandleType::from_str(
            p.get("candle_type").and_then(|v| v.as_str()).unwrap_or("raw"),
            ha_smooth,
        );
        Self::build(
            entry, exit,
            p.get("tp").and_then(serde_json::Value::as_f64),
            p.get("sl").and_then(serde_json::Value::as_f64),
            p.get("tp_atr").and_then(serde_json::Value::as_f64),
            p.get("sl_atr").and_then(serde_json::Value::as_f64),
            atr_period,
            candle_type,
        )
    }

    /// Builder: thêm candle type transform (HA, smooth HA, v.v.).
    pub fn with_candle_type(mut self, candle_type: CandleType) -> Self {
        self.transform = CandleTransform::new(candle_type);
        self
    }

    fn build(
        entry:          &str,
        exit:           &str,
        tp_pct:         Option<f64>,
        sl_pct:         Option<f64>,
        tp_atr_mult:    Option<f64>,
        sl_atr_mult:    Option<f64>,
        atr_period:     Option<usize>,
        candle_type:    CandleType,
    ) -> Result<Self> {
        // Pipeline:
        //   H1.ema(200) > close[1] && rsi(14) < 30
        //   → expand_indicators  → H1_ema_200 > close[1] && rsi_14 < 30
        //   → expand_history_indices → H1_ema_200_h0 ... wait, no —
        //     close[1] → close_h1   (bar field history)
        //     H1_ema_200[1] → H1_ema_200_h1  (indicator history)
        //   → normalize_cel_expr → final float-literal form
        let (entry_ind, entry_calls) = expand_indicators(entry);
        let (exit_ind,  exit_calls)  = expand_indicators(exit);
        let (entry_hist, entry_lb) = expand_history_indices(&entry_ind);
        let (exit_hist,  exit_lb)  = expand_history_indices(&exit_ind);
        let lookback = entry_lb.max(exit_lb);
        let entry_n = normalize_cel_expr(&entry_hist);
        let exit_n  = normalize_cel_expr(&exit_hist);

        let bindings = build_bindings_from_calls(
            entry_calls.into_iter().chain(exit_calls), lookback,
        )?;

        // Pre-allocate the (current, prev, live) variable name strings once.
        let var_keys: Vec<(String, String, String)> = bindings.keys()
            .map(|k| (k.clone(), format!("prev_{k}"), format!("live_{k}")))
            .collect();

        // Build a minimal context: bar fields + indicator placeholders (all as Float(0.0)).
        // No function closures — all values written as plain variables each bar.
        let mut ctx = Context::default();
        ctx.add_variable_from_value("open",        Value::Float(0.0));
        ctx.add_variable_from_value("high",        Value::Float(0.0));
        ctx.add_variable_from_value("low",         Value::Float(0.0));
        ctx.add_variable_from_value("close",       Value::Float(0.0));
        ctx.add_variable_from_value("volume",      Value::Float(0.0));
        ctx.add_variable_from_value("entry_price", Value::Float(0.0));
        // Register `field_hN` vars for bar-field history indexing.
        if lookback > 0 {
            for field in BAR_FIELDS {
                for n in 0..=lookback {
                    let key = format!("{field}_h{n}");
                    ctx.add_variable_from_value(key.as_str(), Value::Float(0.0));
                }
            }
        }
        for (cur, prev, live) in &var_keys {
            ctx.add_variable_from_value(cur.as_str(),  Value::Float(0.0));
            ctx.add_variable_from_value(prev.as_str(), Value::Float(0.0));
            ctx.add_variable_from_value(live.as_str(), Value::Float(0.0));
            // Register `indicator_hN` vars for indicator history indexing.
            if lookback > 0 {
                for n in 0..=lookback {
                    let key = format!("{cur}_h{n}");
                    ctx.add_variable_from_value(key.as_str(), Value::Float(0.0));
                }
            }
        }

        let needs_atr = tp_atr_mult.is_some() || sl_atr_mult.is_some();
        let atr = if needs_atr {
            Some(alm_indicator::Atr::new(atr_period.unwrap_or(14)))
        } else {
            None
        };

        let bar_cap = lookback + 1;
        let indicator_series: HashMap<String, Vec<(i64, f64)>> = HashMap::new();
        Ok(Self {
            entry_prog:  Program::compile(&entry_n)?,
            exit_prog:   Program::compile(&exit_n)?,
            bindings,
            var_keys,
            bar_buf:     VecDeque::with_capacity(bar_cap),
            lookback,
            indicator_series,
            in_position: false,
            entry_price: 0.0,
            tp_pct,
            sl_pct,
            tp_atr_mult,
            sl_atr_mult,
            tp_atr_level: 0.0,
            sl_atr_level: 0.0,
            atr,
            last_atr: 0.0,
            ctx,
            transform: CandleTransform::new(candle_type),
        })
    }
}

impl Strategy for CelStrategy {
    fn on_bar(&mut self, bar: &Bar) -> Vec<Signal> {
        // ── 0. Transform candle if needed (HA, smooth HA, v.v.) ──────────────
        let effective = match self.transform.apply(bar) {
            Some(b) => b,
            None => return vec![], // HA warmup
        };

        // ── 1. Update all indicators ──────────────────────────────────────────
        let mut all_ready = true;
        for b in self.bindings.values_mut() {
            if b.update(&effective).is_none() {
                all_ready = false;
            }
        }

        // ── 2. Push bar into history buffer (always, so close[N] is warm on first ready bar) ──
        if self.lookback > 0 {
            if self.bar_buf.len() == self.bar_buf.capacity() {
                self.bar_buf.pop_front();
            }
            self.bar_buf.push_back(effective.clone());
        }

        if !all_ready {
            return vec![];
        }

        // ── 3. Write bar fields into context ──────────────────────────────────
        // Dùng effective bar (HA nếu có) để expression nhất quán với indicators.
        self.ctx.add_variable_from_value("open",        Value::Float(effective.open));
        self.ctx.add_variable_from_value("high",        Value::Float(effective.high));
        self.ctx.add_variable_from_value("low",         Value::Float(effective.low));
        self.ctx.add_variable_from_value("close",       Value::Float(effective.close));
        self.ctx.add_variable_from_value("volume",      Value::Float(effective.volume));
        // `entry_price` = fill price of the current open position (0.0 when flat).
        self.ctx.add_variable_from_value("entry_price", Value::Float(self.entry_price));

        // Write `field_hN`: index 0 = current bar, 1 = one bar ago, …
        if self.lookback > 0 {
            let buf_len = self.bar_buf.len();
            for n in 0..=self.lookback {
                let bar_opt = if n < buf_len {
                    self.bar_buf.get(buf_len - 1 - n)
                } else {
                    None
                };
                let (o, h, l, c, v) = bar_opt
                    .map(|b| (b.open, b.high, b.low, b.close, b.volume))
                    .unwrap_or((0.0, 0.0, 0.0, 0.0, 0.0));
                self.ctx.add_variable_from_value(format!("open_h{n}").as_str(),   Value::Float(o));
                self.ctx.add_variable_from_value(format!("high_h{n}").as_str(),   Value::Float(h));
                self.ctx.add_variable_from_value(format!("low_h{n}").as_str(),    Value::Float(l));
                self.ctx.add_variable_from_value(format!("close_h{n}").as_str(),  Value::Float(c));
                self.ctx.add_variable_from_value(format!("volume_h{n}").as_str(), Value::Float(v));
            }
        }

        // ── 4. Write indicator values as variables (no Mutex, no format!) ─────
        for (cur_key, prev_key, live_key) in &self.var_keys {
            if let Some(b) = self.bindings.get(cur_key) {
                if let Some(v) = b.cached {
                    self.ctx.add_variable_from_value(cur_key.as_str(),  Value::Float(v));
                }
                if let Some(p) = b.prev {
                    self.ctx.add_variable_from_value(prev_key.as_str(), Value::Float(p));
                }
                if let Some(l) = b.live {
                    self.ctx.add_variable_from_value(live_key.as_str(), Value::Float(l));
                }
                // Write `indicator_hN` for history indexing.
                if self.lookback > 0 {
                    let hist_len = b.history.len();
                    for n in 0..=self.lookback {
                        let val = if n < hist_len {
                            b.history[hist_len - 1 - n]
                        } else {
                            0.0
                        };
                        self.ctx.add_variable_from_value(
                            format!("{cur_key}_h{n}").as_str(),
                            Value::Float(val),
                        );
                    }
                }
            }
        }

        // ── 4b. Append to indicator series ───────────────────────────────────
        for (cur_key, _, _) in &self.var_keys {
            if let Some(b) = self.bindings.get(cur_key) {
                if let Some(v) = b.cached {
                    self.indicator_series
                        .entry(cur_key.clone())
                        .or_default()
                        .push((effective.timestamp, v));
                }
            }
        }

        // ── 5. Update internal ATR ────────────────────────────────────────────
        if let Some(atr) = &mut self.atr {
            if let Some(v) = atr.update(bar.high, bar.low, bar.close) {
                self.last_atr = v.atr;
            }
        }

        // ── 6. Evaluate the appropriate program ───────────────────────────────
        if !self.in_position {
            let fire = matches!(self.entry_prog.execute(&self.ctx), Ok(Value::Bool(true)));
            if fire {
                self.in_position = true;
                self.entry_price = bar.close;
                self.tp_atr_level = self.tp_atr_mult
                    .filter(|_| self.last_atr > 0.0)
                    .map(|m| bar.close + m * self.last_atr)
                    .unwrap_or(0.0);
                self.sl_atr_level = self.sl_atr_mult
                    .filter(|_| self.last_atr > 0.0)
                    .map(|m| bar.close - m * self.last_atr)
                    .unwrap_or(0.0);
                return vec![Signal::long(bar.timestamp, &bar.symbol, 1.0)];
            }
        } else {
            let tp_hit = self.tp_pct
                .map(|pct| bar.close >= self.entry_price * (1.0 + pct))
                .unwrap_or(false)
                || (self.tp_atr_level > 0.0 && bar.close >= self.tp_atr_level);

            let sl_hit = self.sl_pct
                .map(|pct| bar.close <= self.entry_price * (1.0 - pct))
                .unwrap_or(false)
                || (self.sl_atr_level > 0.0 && bar.close <= self.sl_atr_level);

            let exit_hit = matches!(self.exit_prog.execute(&self.ctx), Ok(Value::Bool(true)));

            if tp_hit || sl_hit || exit_hit {
                self.in_position = false;
                self.entry_price = 0.0;
                self.tp_atr_level = 0.0;
                self.sl_atr_level = 0.0;
                return vec![Signal::close(bar.timestamp, &bar.symbol)];
            }
        }

        vec![]
    }

    fn name(&self) -> &str { "cel" }

    fn reset(&mut self) {
        for b in self.bindings.values_mut() { b.reset(); }
        if let Some(atr) = &mut self.atr { atr.reset(); }
        self.transform.reset();
        self.bar_buf.clear();
        self.in_position = false;
        self.entry_price = 0.0;
        self.tp_atr_level = 0.0;
        self.sl_atr_level = 0.0;
        self.last_atr = 0.0;
    }

    fn take_indicator_series(&mut self) -> std::collections::HashMap<String, Vec<(i64, f64)>> {
        std::mem::take(&mut self.indicator_series)
    }
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use alm_core::bar::Bar;

    fn bar(ts: i64, c: f64) -> Bar {
        Bar::new(ts, "T", c, c * 1.01, c * 0.99, c, 1_000.0)
    }

    // ── CelScript::parse ─────────────────────────────────────────────────────

    #[test]
    fn script_basic() {
        let s = CelScript::parse(
            "entry: rsi(14) < 30.0 && close > ema(50)\n\
             exit:  rsi(14) > 70.0\n\
             tp:    0.05\n\
             sl:    0.02",
        ).unwrap();
        assert_eq!(s.entry, "rsi(14) < 30.0 && close > ema(50)");
        assert_eq!(s.exit,  "rsi(14) > 70.0");
        assert_eq!(s.tp,    Some(0.05));
        assert_eq!(s.sl,    Some(0.02));
    }

    #[test]
    fn script_comments_and_blanks() {
        let s = CelScript::parse(
            "# This is a comment\n\
             \n\
             entry: rsi(14) < 30.0\n\
             # another comment\n\
             exit:  rsi(14) > 70.0",
        ).unwrap();
        assert_eq!(s.entry, "rsi(14) < 30.0");
        assert_eq!(s.exit,  "rsi(14) > 70.0");
    }

    #[test]
    fn script_multiline_entry() {
        let s = CelScript::parse(
            "entry: rsi(14) < 30.0\n\
             \t&& close > ema(50)\n\
             \t&& adx(14) > 25.0\n\
             exit:  rsi(14) > 70.0",
        ).unwrap();
        assert_eq!(s.entry, "rsi(14) < 30.0 && close > ema(50) && adx(14) > 25.0");
    }

    #[test]
    fn script_no_exit_is_ok() {
        let s = CelScript::parse("entry: rsi(14) < 30.0\ntp: 0.10").unwrap();
        assert!(s.exit.is_empty());
        assert_eq!(s.tp, Some(0.10));
    }

    #[test]
    fn script_candle_type() {
        let s = CelScript::parse(
            "entry: close > ema(50)\n\
             candle_type: heiken_ashi\n\
             ha_smooth: 3",
        ).unwrap();
        assert_eq!(s.candle_type, "heiken_ashi");
        assert_eq!(s.ha_smooth, 3);
    }

    #[test]
    fn script_all_risk_params() {
        let s = CelScript::parse(
            "entry: close > ema(50)\n\
             tp_atr: 2.0\n\
             sl_atr: 1.0\n\
             atr_period: 10",
        ).unwrap();
        assert_eq!(s.tp_atr,     Some(2.0));
        assert_eq!(s.sl_atr,     Some(1.0));
        assert_eq!(s.atr_period, Some(10));
    }

    #[test]
    fn script_missing_entry_errors() {
        assert!(CelScript::parse("exit: rsi(14) > 70.0\ntp: 0.05").is_err());
    }

    #[test]
    fn script_unknown_line_without_section_errors() {
        assert!(CelScript::parse("tp: 0.05\njunk line here").is_err());
    }

    #[test]
    fn from_params_script_field() {
        let p = serde_json::json!({
            "script": "entry: rsi(14) < 30.0 && close > ema(50)\nexit: rsi(14) > 70.0\ntp: 0.05"
        });
        assert!(CelStrategy::from_params(&p).is_ok());
    }

    #[test]
    fn from_params_json_overrides_script() {
        // JSON fields override script — just ensure it builds without error
        let p = serde_json::json!({
            "script": "entry: rsi(14) < 30.0\ntp: 0.05",
            "tp": 0.10
        });
        assert!(CelStrategy::from_params(&p).is_ok());
    }

    // ── expand_indicators ────────────────────────────────────────────────────

    #[test]
    fn expand_one_arg_indicator() {
        let (expr, _) = expand_indicators("rsi(14) < 30");
        assert_eq!(normalize_cel_expr(&expr), "rsi_14 < 30.0");
    }

    #[test]
    fn expand_prev_prefix() {
        let (expr, _) = expand_indicators("prev_ema(9) <= prev_ema(21) && ema(9) > ema(21)");
        assert_eq!(expr, "prev_ema_9 <= prev_ema_21 && ema_9 > ema_21");
    }

    #[test]
    fn expand_zero_arg() {
        let (expr, _) = expand_indicators("obv() > 0 && ao() > 0");
        assert_eq!(normalize_cel_expr(&expr), "obv > 0.0 && ao > 0.0");
    }

    #[test]
    fn expand_bar_fields_unchanged() {
        // close/high are not indicator calls — must pass through verbatim
        let (expr, calls) = expand_indicators("close > 100 && high < 200");
        assert_eq!(normalize_cel_expr(&expr), "close > 100.0 && high < 200.0");
        assert!(calls.is_empty());
    }

    #[test]
    fn expand_collects_calls() {
        let (_, calls) = expand_indicators("ema(9) > close && rsi(14) < 30.0");
        let keys: Vec<_> = calls.iter().map(|c| c.key()).collect();
        assert!(keys.iter().any(|k| k == "ema_9"),  "ema_9");
        assert!(keys.iter().any(|k| k == "rsi_14"), "rsi_14");
    }

    #[test]
    fn expand_prev_calls_share_key() {
        let (_, calls) = expand_indicators("prev_ema(9) <= prev_ema(21) && ema(9) > ema(21)");
        // 4 tokens, but keys deduplicate to ema_9 and ema_21
        let keys: std::collections::HashSet<_> = calls.iter().map(|c| c.key()).collect();
        assert!(keys.contains("ema_9"),  "ema_9");
        assert!(keys.contains("ema_21"), "ema_21");
        assert!(!keys.contains("prev_ema_9"), "prev_ must not appear in key()");
    }

    #[test]
    fn expand_zero_arg_calls() {
        let (_, calls) = expand_indicators("obv() > 0.0 && ao() > 0.0 && sar() < close");
        let keys: Vec<_> = calls.iter().map(|c| c.key()).collect();
        assert!(keys.iter().any(|k| k == "obv"));
        assert!(keys.iter().any(|k| k == "ao"));
        assert!(keys.iter().any(|k| k == "sar"));
        let obv = calls.iter().find(|c| c.key() == "obv").unwrap();
        assert!(obv.args.is_empty());
    }

    #[test]
    fn expand_multi_field_indicators() {
        let (_, calls) = expand_indicators("macd_hist(12) > 0.0 && bb_upper(20) > close && stoch_k(14) > 80.0");
        let keys: Vec<_> = calls.iter().map(|c| c.key()).collect();
        assert!(keys.iter().any(|k| k == "macd_hist_12"));
        assert!(keys.iter().any(|k| k == "bb_upper_20"));
        assert!(keys.iter().any(|k| k == "stoch_k_14"));
    }

    // ── Binding map ───────────────────────────────────────────────────────────

    #[test]
    fn bindings_deduped() {
        // prev_ema(9) and ema(9) must share one binding
        let s = CelStrategy::new(
            "prev_ema(9) <= prev_ema(21) && ema(9) > ema(21)",
            "prev_ema(9) >= prev_ema(21) && ema(9) < ema(21)",
        ).unwrap();
        assert!(s.bindings.contains_key("ema_9"));
        assert!(s.bindings.contains_key("ema_21"));
        assert!(!s.bindings.contains_key("prev_ema_9"));
        assert_eq!(s.bindings.len(), 2);
    }

    #[test]
    fn complex_binding_set() {
        let s = CelStrategy::new(
            "rsi(14) < 35.0 && close > ema(50) && adx(14) > 20.0",
            "rsi(14) > 65.0 || close < ema(50)",
        ).unwrap();
        assert!(s.bindings.contains_key("rsi_14"));
        assert!(s.bindings.contains_key("ema_50"));
        assert!(s.bindings.contains_key("adx_14"));
        assert_eq!(s.bindings.len(), 3);
    }

    #[test]
    fn macd_hist_single_binding() {
        let s = CelStrategy::new(
            "prev_macd_hist(12) <= 0.0 && macd_hist(12) > 0.0",
            "prev_macd_hist(12) >= 0.0 && macd_hist(12) < 0.0",
        ).unwrap();
        assert!(s.bindings.contains_key("macd_hist_12"));
        assert_eq!(s.bindings.len(), 1);
    }

    // ── Warmup & signal generation ────────────────────────────────────────────

    #[test]
    fn no_signal_before_warmup() {
        let mut s = CelStrategy::new("rsi(14) < 30.0", "rsi(14) > 70.0").unwrap();
        for i in 0..14 {
            assert!(s.on_bar(&bar(i, 100.0)).is_empty(), "bar {i}: should be silent");
        }
    }

    #[test]
    fn long_on_oversold_rsi() {
        let mut s = CelStrategy::new("rsi(14) < 30.0", "rsi(14) > 70.0").unwrap();
        let mut sigs = vec![];
        for i in 0..40 {
            sigs.extend(s.on_bar(&bar(i, 150.0 - i as f64 * 4.0)));
        }
        use alm_core::signal::Direction;
        assert!(sigs.iter().any(|s| s.direction == Direction::Long), "no Long signal found");
    }

    #[test]
    fn ema_cross_above_generates_long() {
        let mut s = CelStrategy::new(
            "prev_ema(5) <= prev_ema(20) && ema(5) > ema(20)",
            "prev_ema(5) >= prev_ema(20) && ema(5) < ema(20)",
        ).unwrap();
        use alm_core::signal::Direction;

        let mut sigs = vec![];
        for i in 0..30 { sigs.extend(s.on_bar(&bar(i, 100.0 - i as f64))); }
        for i in 30..70 { sigs.extend(s.on_bar(&bar(i, 70.0 + (i - 30) as f64 * 3.0))); }
        assert!(sigs.iter().any(|s| s.direction == Direction::Long), "no EMA cross-above signal");
    }

    #[test]
    fn reset_clears_state() {
        let mut s = CelStrategy::new("rsi(14) < 30.0", "rsi(14) > 70.0").unwrap();
        for i in 0..25 { s.on_bar(&bar(i, 100.0)); }
        s.reset();
        assert!(s.on_bar(&bar(0, 100.0)).is_empty(), "first bar after reset must be silent");
    }

    #[test]
    fn entry_then_exit_cycle() {
        let mut s = CelStrategy::new("close < 80.0", "close > 120.0").unwrap();
        let mut sigs = vec![];
        for i in 0..5  { sigs.extend(s.on_bar(&bar(i, 75.0))); }  // trigger entry
        for i in 5..10 { sigs.extend(s.on_bar(&bar(i, 130.0))); } // trigger exit

        use alm_core::signal::Direction;
        assert!(sigs.iter().any(|s| s.direction == Direction::Long),  "no Long");
        assert!(sigs.iter().any(|s| s.direction == Direction::Close), "no Close");
        assert!(!s.in_position, "should not be in position after close");
    }

    // ── Compile-time parsing ──────────────────────────────────────────────────

    #[test]
    fn invalid_cel_expr_rejected_at_new() {
        // "&&" alone is not valid CEL
        assert!(CelStrategy::new("&&", "rsi(14) > 70.0").is_err());
    }

    // ── MTF (Multi-Timeframe) ─────────────────────────────────────────────────

    #[test]
    fn expand_indicators_dot_tf() {
        let (expr, _) = expand_indicators("H1.ema(200) > close");
        assert_eq!(expr, "H1_ema_200 > close");

        let (expr, _) = expand_indicators("M15.rsi(14) < 30");
        assert_eq!(expr, "M15_rsi_14 < 30");

        let (expr, _) = expand_indicators("prev_H4.macd_hist(12) < 0.0 && H4.macd_hist(12) > 0.0");
        assert_eq!(expr, "prev_H4_macd_hist_12 < 0.0 && H4_macd_hist_12 > 0.0");

        // Non-indicator dots must pass through unchanged
        let (expr, _) = expand_indicators("x.field > 1.5");
        assert_eq!(expr, "x.field > 1.5");
    }

    #[test]
    fn expand_indicators_underscore_tf() {
        // H1_ema(200) is the underscore form — should produce the same CEL var as dot form
        let (expr, calls) = expand_indicators("H1_ema(200) > close");
        assert_eq!(expr, "H1_ema_200 > close");
        assert_eq!(calls[0].key(), "H1_ema_200");

        let (expr, _) = expand_indicators("M15_rsi(14) < 30");
        assert_eq!(expr, "M15_rsi_14 < 30");

        // prev_ prefix with underscore TF
        let (expr, _) = expand_indicators("prev_H4_macd_hist(12) < 0.0");
        assert_eq!(expr, "prev_H4_macd_hist_12 < 0.0");

        // Dot and underscore forms are equivalent
        let (dot_expr,  _) = expand_indicators("H1.ema(200) > close");
        let (us_expr,   _) = expand_indicators("H1_ema(200) > close");
        assert_eq!(dot_expr, us_expr);
    }

    #[test]
    fn expand_indicators_live_prefix() {
        // live_ prefix → var name prefixed with live_
        let (expr, calls) = expand_indicators("live_H1.ema(200) > close");
        assert_eq!(expr, "live_H1_ema_200 > close");
        assert!(calls[0].is_live);
        assert_eq!(calls[0].key(), "H1_ema_200"); // key strips live_

        // live_ with base TF (no HTF)
        let (expr, calls) = expand_indicators("live_ema(9) > close");
        assert_eq!(expr, "live_ema_9 > close");
        assert!(calls[0].is_live);

        // live_ and non-live share the same binding key
        let (_, live_calls)  = expand_indicators("live_H1.ema(200) > 0.0");
        let (_, plain_calls) = expand_indicators("H1.ema(200) > 0.0");
        assert_eq!(live_calls[0].key(), plain_calls[0].key());
    }

    #[test]
    fn live_and_confirmed_share_one_binding() {
        // live_H1.ema(200) and H1.ema(200) in the same strategy → one binding, not two
        let s = CelStrategy::new(
            "live_H1.ema(200) > close",
            "close < H1.ema(200)",
        ).unwrap();
        assert_eq!(s.bindings.len(), 1, "live_ and confirmed must share binding");
        assert!(s.bindings.contains_key("H1_ema_200"));
    }

    #[test]
    fn mtf_binding_key_h1_syntax() {
        let s = CelStrategy::new("H1.ema(200) > close", "close < H1.ema(200)").unwrap();
        assert!(
            s.bindings.contains_key("H1_ema_200"),
            "expected H1_ema_200, got: {:?}",
            s.bindings.keys().collect::<Vec<_>>()
        );
        assert_eq!(s.bindings.len(), 1);
    }

    #[test]
    fn mtf_resampler_uses_time_based() {
        let s = CelStrategy::new("H4.ema(50) > close", "close < H4.ema(50)").unwrap();
        let b = s.bindings.get("H4_ema_50").expect("binding missing");
        assert!(b.resampler.is_some(), "MTF binding must have a TimeBarResampler");
        // H4 = 4 * 3_600_000 ms
        assert_eq!(b.resampler.as_ref().unwrap().interval_ms(), 4 * 3_600_000);
    }

    #[test]
    fn same_tf_and_base_are_separate_bindings() {
        // ema(50) and H4.ema(50) must be distinct bindings
        let s = CelStrategy::new(
            "ema(50) > close && H4.ema(50) > close",
            "close < ema(50)",
        ).unwrap();
        assert!(s.bindings.contains_key("ema_50"),    "missing ema_50");
        assert!(s.bindings.contains_key("H4_ema_50"), "missing H4_ema_50");
        assert_eq!(s.bindings.len(), 2);
    }

    #[test]
    fn mtf_prev_shares_binding() {
        // prev_H1.ema(200) and H1.ema(200) must share one binding
        let s = CelStrategy::new(
            "prev_H1.ema(200) < H1.ema(200)",
            "H1.ema(200) < prev_H1.ema(200)",
        ).unwrap();
        assert!(s.bindings.contains_key("H1_ema_200"));
        assert_eq!(s.bindings.len(), 1);
    }

    #[test]
    fn mtf_stays_silent_until_htf_bar() {
        // H1 = 3_600_000 ms. Feed bars spaced 1 minute apart.
        // EMA(5) needs 5 H1 bars = 300 M1 bars before producing a value.
        let mut s = CelStrategy::new("H1.ema(5) > 0.0", "H1.ema(5) < 0.0").unwrap();
        // Feed 4 complete H1 bars (240 M1 bars) — EMA(5) not ready yet
        for i in 0..240i64 {
            let ts = i * 60_000;
            let sigs = s.on_bar(&Bar::new(ts, "T", 100.0, 101.0, 99.0, 100.0, 1000.0));
            // First 4 H1 bars emit at bar 60, 120, 180, 240 — all silent (EMA needs 5)
            let _ = sigs;
        }
        // After 240 M1 bars (4 H1 bars) EMA(5) still has only 4 HTF data points → silent
        let last = s.on_bar(&Bar::new(240 * 60_000, "T", 100.0, 101.0, 99.0, 100.0, 1000.0));
        assert!(last.is_empty(), "EMA(5) needs 5 H1 bars, silent after 4");
    }

    #[test]
    fn strip_tf_prefix_parses_correctly() {
        assert_eq!(strip_tf_prefix("H1_ema"),   ("ema",  Some(3_600_000)));
        assert_eq!(strip_tf_prefix("M15_rsi"),  ("rsi",  Some(900_000)));
        assert_eq!(strip_tf_prefix("D1_adx"),   ("adx",  Some(86_400_000)));
        assert_eq!(strip_tf_prefix("H4_macd_hist"), ("macd_hist", Some(14_400_000)));
        assert_eq!(strip_tf_prefix("ema"),       ("ema",  None));
        assert_eq!(strip_tf_prefix("H_ema"),     ("H_ema", None)); // no digits
        assert_eq!(strip_tf_prefix("X1_ema"),    ("X1_ema", None)); // unknown unit
    }

    // ── History indexing ─────────────────────────────────────────────────────

    #[test]
    fn expand_history_indices_bar_field() {
        let (out, lb) = expand_history_indices("close[1] > close[2]");
        assert_eq!(out, "close_h1 > close_h2");
        assert_eq!(lb, 2);
    }

    #[test]
    fn expand_history_indices_indicator() {
        // After expand_indicators, ema(9) becomes ema_9, then [1] → ema_9_h1
        let (ind, _) = expand_indicators("ema(9)[1] < ema(9)[0]");
        let (out, lb) = expand_history_indices(&ind);
        assert_eq!(out, "ema_9_h1 < ema_9_h0");
        assert_eq!(lb, 1);
    }

    #[test]
    fn expand_history_indices_zero_lookback() {
        let (out, lb) = expand_history_indices("close > ema_9 && rsi_14 < 30");
        assert_eq!(out, "close > ema_9 && rsi_14 < 30");
        assert_eq!(lb, 0);
    }

    #[test]
    fn strategy_builds_with_history_indexing() {
        // Verify that [N] syntax compiles into a valid CEL strategy.
        let s = CelStrategy::new(
            "close[1] > ema(50) && close[0] < ema(50)",
            "close[0] > ema(50)",
        ).unwrap();
        assert_eq!(s.lookback, 1);
    }

    #[test]
    fn history_index_produces_correct_value() {
        // entry when current close dropped from previous close
        let mut s = CelStrategy::new("close[1] > close[0]", "false").unwrap();
        let mut sigs = vec![];
        sigs.extend(s.on_bar(&bar(1, 100.0))); // no prev yet
        sigs.extend(s.on_bar(&bar(2, 95.0)));  // close[1]=100 > close[0]=95 → Long
        use alm_core::signal::Direction;
        assert!(sigs.iter().any(|s| s.direction == Direction::Long), "expected Long on drop");
    }

    #[test]
    fn history_index_no_signal_on_rise() {
        // entry when current close dropped from previous — should NOT fire on rise
        let mut s = CelStrategy::new("close[1] > close[0]", "false").unwrap();
        let mut sigs = vec![];
        sigs.extend(s.on_bar(&bar(1, 100.0)));
        sigs.extend(s.on_bar(&bar(2, 110.0))); // close[1]=100 < close[0]=110 → no signal
        use alm_core::signal::Direction;
        assert!(!sigs.iter().any(|s| s.direction == Direction::Long), "should not fire on rise");
    }
}

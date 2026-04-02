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
//! | Function          | Indicator                  | Output          |
//! |-------------------|----------------------------|-----------------|
//! | `ema(N)`          | EMA(N)                     | price level     |
//! | `sma(N)`          | SMA(N)                     | price level     |
//! | `wma(N)`          | WMA(N)                     | price level     |
//! | `hma(N)`          | HMA(N)                     | price level     |
//! | `tema(N)`         | TEMA(N)                    | price level     |
//! | `kama(N)`         | KAMA(N)                    | price level     |
//! | `rsi(N)`          | RSI(N)                     | 0–100           |
//! | `cci(N)`          | CCI(N)                     | oscillator      |
//! | `roc(N)`          | ROC(N)                     | % change        |
//! | `mfi(N)`          | MFI(N)                     | 0–100           |
//! | `williams(N)`     | Williams %R(N)             | -100–0          |
//! | `tsi(N)`          | TSI(N, 13)                 | -100–100        |
//! | `chop(N)`         | Choppiness(N)              | 0–100           |
//! | `connors_rsi(N)`  | ConnorsRSI(N, 2, 100)      | 0–100           |
//! | `atr(N)`          | ATR(N)                     | price range     |
//! | `adx(N)`          | ADX(N)                     | 0–100           |
//! | `plus_di(N)`      | +DI from ADX(N)            | 0–100           |
//! | `minus_di(N)`     | −DI from ADX(N)            | 0–100           |
//! | `macd_hist(N)`    | MACD histogram (N, 26, 9)  | signed          |
//! | `macd_line(N)`    | MACD line (N, 26, 9)       | signed          |
//! | `bb_upper(N)`     | Bollinger upper (N, 2σ)    | price level     |
//! | `bb_lower(N)`     | Bollinger lower (N, 2σ)    | price level     |
//! | `bb_mid(N)`       | Bollinger middle (N)       | price level     |
//! | `stoch_k(N)`      | Stochastic %K(N)           | 0–100           |
//! | `stoch_d(N)`      | Stochastic %D(N)           | 0–100           |
//! | `srsi_k(N)`       | StochRSI %K(N)             | 0–100           |
//! | `srsi_d(N)`       | StochRSI %D(N)             | 0–100           |
//! | `kdj_k(N)`        | KDJ %K(N)                  |                 |
//! | `kdj_d(N)`        | KDJ %D(N)                  |                 |
//! | `kdj_j(N)`        | KDJ %J(N)                  |                 |
//! | `supertrend(N)`   | SuperTrend value (N, 3×ATR)|                 |
//! | `st_bull(N)`      | SuperTrend bullish flag    | 0.0 / 1.0       |
//! | `cmf(N)`          | Chaikin Money Flow(N)      | -1–1            |
//! | `obv()`           | On-Balance Volume          | cumulative vol  |
//! | `ao()`            | Awesome Oscillator         | signed          |
//! | `sar()`           | Parabolic SAR              | price level     |
//!
//! Bar fields always in scope: `open`, `high`, `low`, `close`, `volume`.
//!
//! ## Previous-bar values
//!
//! Prefix any indicator function with `prev_` to read the previous bar's value.
//! This is the standard way to detect crossovers — no special operator needed.
//!
//! ```text
//! // EMA cross-above
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

use std::collections::HashMap;

use anyhow::Result;
use cel_interpreter::{Context, Program, Value};
use alm_core::{bar::Bar, signal::Signal, strategy::Strategy};
use serde_json::json;

use crate::dynamic::indicator_box::IndicatorBox;

// ── VarBinding ────────────────────────────────────────────────────────────────

struct VarBinding {
    ind:    IndicatorBox,
    field:  String,
    cached: Option<f64>,
    prev:   Option<f64>,
}

impl VarBinding {
    fn update(&mut self, bar: &Bar) -> Option<f64> {
        self.prev = self.cached;
        let fields = self.ind.update(bar)?;
        let v = *fields.get(&self.field)?;
        self.cached = Some(v);
        Some(v)
    }

    fn reset(&mut self) {
        self.ind.reset();
        self.cached = None;
        self.prev   = None;
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

// ── Call → variable expander ──────────────────────────────────────────────────

/// Replace known indicator function calls with plain variable references.
///
/// Applied **after** `normalize_cel_expr` so arguments are already float literals.
///
/// - `rsi(14.0)`      → `rsi_14`
/// - `prev_ema(9.0)`  → `prev_ema_9`
/// - `obv()`          → `obv`
/// - `prev_obv()`     → `prev_obv`
/// - `close`, `high`  → unchanged (not in indicator lists)
///
/// The resulting identifiers are registered as CEL variables per bar, eliminating
/// function-call overhead (mutex lock + `format!` + HashMap lookup) on the hot path.
fn expand_calls_to_vars(expr: &str) -> String {
    let mut out = String::with_capacity(expr.len());
    let b = expr.as_bytes();
    let n = b.len();
    let mut i = 0;

    while i < n {
        if b[i].is_ascii_alphabetic() || b[i] == b'_' {
            let start = i;
            while i < n && (b[i].is_ascii_alphanumeric() || b[i] == b'_') {
                i += 1;
            }
            let ident = &expr[start..i];

            if i < n && b[i] == b'(' {
                let base = ident.strip_prefix("prev_").unwrap_or(ident);

                if ONE_ARG_FUNCS.contains(&base) {
                    i += 1; // skip '('
                    let arg_start = i;
                    let mut depth = 1usize;
                    while i < n && depth > 0 {
                        match b[i] {
                            b'(' => depth += 1,
                            b')' => depth -= 1,
                            _ => {}
                        }
                        i += 1;
                    }
                    let arg_str = expr[arg_start..i - 1].trim();
                    if let Ok(period) = arg_str.parse::<f64>() {
                        // e.g. "rsi_14" or "prev_ema_9"
                        out.push_str(ident);
                        out.push('_');
                        out.push_str(&(period as i64).to_string());
                    } else {
                        // Unrecognised arg — keep original syntax
                        out.push_str(ident);
                        out.push('(');
                        out.push_str(&expr[arg_start..i]);
                    }
                } else if ZERO_ARG_FUNCS.contains(&base) {
                    i += 1; // skip '('
                    let mut depth = 1usize;
                    while i < n && depth > 0 {
                        match b[i] {
                            b'(' => depth += 1,
                            b')' => depth -= 1,
                            _ => {}
                        }
                        i += 1;
                    }
                    // e.g. "obv" or "prev_obv" — no argument suffix
                    out.push_str(ident);
                } else {
                    // Not a known indicator — emit identifier, leave '(' to next iteration
                    out.push_str(ident);
                }
            } else {
                out.push_str(ident);
            }
        } else {
            out.push(b[i] as char);
            i += 1;
        }
    }

    out
}

// ── Expression scanner ────────────────────────────────────────────────────────

struct Call {
    raw:  String,    // e.g. "prev_ema", "macd_hist", "obv"
    args: Vec<f64>,  // e.g. [9.0], []
}

impl Call {
    fn base(&self) -> (&str, bool) {
        match self.raw.strip_prefix("prev_") {
            Some(b) => (b, true),
            None    => (self.raw.as_str(), false),
        }
    }

    /// Canonical binding key: `"ema_9"`, `"macd_hist_12"`, `"obv"`.
    fn key(&self) -> String {
        let (base, _) = self.base();
        if self.args.is_empty() {
            base.to_string()
        } else {
            let parts: Vec<String> = self.args.iter().map(|a| format!("{}", *a as i64)).collect();
            format!("{}_{}", base, parts.join("_"))
        }
    }
}

/// Walk `expr` byte-by-byte collecting every `ident(...)` call.
fn scan_calls(expr: &str) -> Vec<Call> {
    let mut calls = Vec::new();
    let b = expr.as_bytes();
    let n = b.len();
    let mut i = 0;
    while i < n {
        if b[i].is_ascii_alphabetic() || b[i] == b'_' {
            let start = i;
            while i < n && (b[i].is_ascii_alphanumeric() || b[i] == b'_') { i += 1; }
            if i < n && b[i] == b'(' {
                let raw = expr[start..i].to_string();
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
                calls.push(Call { raw, args });
            }
        } else {
            i += 1;
        }
    }
    calls
}

// ── Indicator factory ─────────────────────────────────────────────────────────

fn make_binding(base: &str, args: &[f64]) -> Result<Option<VarBinding>> {
    let n = args.first().copied().unwrap_or(0.0) as usize;

    macro_rules! bind {
        ($cfg:expr, $field:expr) => {
            Ok(Some(VarBinding {
                ind:    IndicatorBox::from_config(&$cfg)?,
                field:  $field.into(),
                cached: None,
                prev:   None,
            }))
        };
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
        _             => Ok(None),
    }
}

fn build_bindings(entry: &str, exit: &str) -> Result<HashMap<String, VarBinding>> {
    let mut map: HashMap<String, VarBinding> = HashMap::new();
    for call in scan_calls(&format!("{entry} {exit}")) {
        let (base, _) = call.base();
        let key = call.key();
        if !map.contains_key(&key) {
            if let Some(b) = make_binding(base, &call.args)? {
                map.insert(key, b);
            }
        }
    }
    Ok(map)
}

// ── Function lists ────────────────────────────────────────────────────────────

const ONE_ARG_FUNCS: &[&str] = &[
    "ema", "sma", "wma", "hma", "tema", "kama",
    "rsi", "cci", "roc", "mfi", "williams", "tsi", "chop", "connors_rsi",
    "atr", "adx", "plus_di", "minus_di",
    "macd_hist", "macd_line",
    "bb_upper", "bb_lower", "bb_mid",
    "stoch_k", "stoch_d", "srsi_k", "srsi_d",
    "kdj_k", "kdj_d", "kdj_j",
    "supertrend", "st_bull",
    "cmf",
];

const ZERO_ARG_FUNCS: &[&str] = &["obv", "ao", "sar"];

// ── Strategy ──────────────────────────────────────────────────────────────────

pub struct CelStrategy {
    entry_prog:  Program,
    exit_prog:   Program,
    /// Indicator state, keyed by canonical name (e.g. `"rsi_14"`).
    /// Kept as HashMap so external tests can assert on `contains_key`.
    bindings:    HashMap<String, VarBinding>,
    /// Pre-allocated `(current_var_name, prev_var_name)` strings for the hot path.
    /// Order matches `bindings.keys()` at construction time.
    var_keys:    Vec<(String, String)>,
    in_position: bool,
    entry_price: f64,
    /// Take-profit as fraction of entry price, e.g. 0.05 = 5%. None = disabled.
    tp_pct:      Option<f64>,
    /// Stop-loss as fraction of entry price, e.g. 0.02 = 2%. None = disabled.
    sl_pct:      Option<f64>,
    /// CEL context; indicator values written as variables each bar — no closures,
    /// no mutex, no `format!` in the hot path.
    ctx:         Context<'static>,
}

impl CelStrategy {
    pub fn new(entry: &str, exit: &str) -> Result<Self> {
        Self::with_risk(entry, exit, None, None)
    }

    pub fn with_risk(
        entry: &str,
        exit:  &str,
        tp_pct: Option<f64>,
        sl_pct: Option<f64>,
    ) -> Result<Self> {
        // Normalise integer literals, then expand function calls to variable refs.
        // rsi(14) < 30  →  normalize  →  rsi(14.0) < 30.0
        //               →  expand     →  rsi_14 < 30.0
        let entry_n = expand_calls_to_vars(&normalize_cel_expr(entry));
        let exit_n  = expand_calls_to_vars(&normalize_cel_expr(exit));

        let bindings = build_bindings(entry, exit)?;

        // Pre-allocate the (current, prev) variable name strings once.
        let var_keys: Vec<(String, String)> = bindings.keys()
            .map(|k| (k.clone(), format!("prev_{k}")))
            .collect();

        // Build a minimal context: only the 5 bar fields + indicator placeholders.
        // No function closures — indicators are injected as plain variables each bar.
        let mut ctx = Context::default();
        ctx.add_variable_from_value("open",   Value::Float(0.0));
        ctx.add_variable_from_value("high",   Value::Float(0.0));
        ctx.add_variable_from_value("low",    Value::Float(0.0));
        ctx.add_variable_from_value("close",  Value::Float(0.0));
        ctx.add_variable_from_value("volume", Value::Float(0.0));
        for (cur, prev) in &var_keys {
            ctx.add_variable_from_value(cur.as_str(),  Value::Float(0.0));
            ctx.add_variable_from_value(prev.as_str(), Value::Float(0.0));
        }

        Ok(Self {
            entry_prog:  Program::compile(&entry_n)?,
            exit_prog:   Program::compile(&exit_n)?,
            bindings,
            var_keys,
            in_position: false,
            entry_price: 0.0,
            tp_pct,
            sl_pct,
            ctx,
        })
    }
}

impl Strategy for CelStrategy {
    fn on_bar(&mut self, bar: &Bar) -> Vec<Signal> {
        // ── 1. Update all indicators ──────────────────────────────────────────
        let mut all_ready = true;
        for b in self.bindings.values_mut() {
            if b.update(bar).is_none() {
                all_ready = false;
            }
        }
        if !all_ready {
            return vec![];
        }

        // ── 2. Write bar fields into context ──────────────────────────────────
        self.ctx.add_variable_from_value("open",   Value::Float(bar.open));
        self.ctx.add_variable_from_value("high",   Value::Float(bar.high));
        self.ctx.add_variable_from_value("low",    Value::Float(bar.low));
        self.ctx.add_variable_from_value("close",  Value::Float(bar.close));
        self.ctx.add_variable_from_value("volume", Value::Float(bar.volume));

        // ── 3. Write indicator values as variables (no Mutex, no format!) ─────
        for (cur_key, prev_key) in &self.var_keys {
            if let Some(b) = self.bindings.get(cur_key) {
                if let Some(v) = b.cached {
                    self.ctx.add_variable_from_value(cur_key.as_str(), Value::Float(v));
                }
                if let Some(p) = b.prev {
                    self.ctx.add_variable_from_value(prev_key.as_str(), Value::Float(p));
                }
            }
        }

        // ── 4. Evaluate the appropriate program ───────────────────────────────
        if !self.in_position {
            let fire = matches!(self.entry_prog.execute(&self.ctx), Ok(Value::Bool(true)));
            if fire {
                self.in_position = true;
                self.entry_price = bar.close;
                return vec![Signal::long(bar.timestamp, &bar.symbol, 1.0)];
            }
        } else {
            // SL: hit if close drops below entry * (1 - sl_pct)
            let sl_hit = self.sl_pct
                .map(|pct| bar.close <= self.entry_price * (1.0 - pct))
                .unwrap_or(false);
            // TP: hit if close rises above entry * (1 + tp_pct)
            let tp_hit = self.tp_pct
                .map(|pct| bar.close >= self.entry_price * (1.0 + pct))
                .unwrap_or(false);
            let exit_hit = matches!(self.exit_prog.execute(&self.ctx), Ok(Value::Bool(true)));

            if sl_hit || tp_hit || exit_hit {
                self.in_position = false;
                self.entry_price = 0.0;
                return vec![Signal::close(bar.timestamp, &bar.symbol)];
            }
        }

        vec![]
    }

    fn name(&self) -> &str { "cel" }

    fn reset(&mut self) {
        for b in self.bindings.values_mut() { b.reset(); }
        self.in_position = false;
        self.entry_price = 0.0;
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

    // ── expand_calls_to_vars ──────────────────────────────────────────────────

    #[test]
    fn expand_one_arg_indicator() {
        let norm = normalize_cel_expr("rsi(14) < 30");
        let expanded = expand_calls_to_vars(&norm);
        assert_eq!(expanded, "rsi_14 < 30.0");
    }

    #[test]
    fn expand_prev_prefix() {
        let norm = normalize_cel_expr("prev_ema(9) <= prev_ema(21) && ema(9) > ema(21)");
        let expanded = expand_calls_to_vars(&norm);
        assert_eq!(expanded, "prev_ema_9 <= prev_ema_21 && ema_9 > ema_21");
    }

    #[test]
    fn expand_zero_arg() {
        let norm = normalize_cel_expr("obv() > 0 && ao() > 0");
        let expanded = expand_calls_to_vars(&norm);
        assert_eq!(expanded, "obv > 0.0 && ao > 0.0");
    }

    #[test]
    fn expand_bar_fields_unchanged() {
        let norm = normalize_cel_expr("close > 100 && high < 200");
        let expanded = expand_calls_to_vars(&norm);
        assert_eq!(expanded, "close > 100.0 && high < 200.0");
    }

    // ── Scanner ───────────────────────────────────────────────────────────────

    #[test]
    fn scan_simple_calls() {
        let calls = scan_calls("ema(9) > close && rsi(14) < 30.0");
        let raws: Vec<_> = calls.iter().map(|c| c.raw.as_str()).collect();
        assert!(raws.contains(&"ema"), "ema not found");
        assert!(raws.contains(&"rsi"), "rsi not found");
    }

    #[test]
    fn scan_prev_prefix() {
        let calls = scan_calls("prev_ema(9) <= prev_ema(21) && ema(9) > ema(21)");
        // 4 tokens: prev_ema (×2), ema (×2)
        let keys: std::collections::HashSet<_> = calls.iter().map(|c| c.key()).collect();
        assert!(keys.contains("ema_9"),  "ema_9");
        assert!(keys.contains("ema_21"), "ema_21");
        // prev_ calls fold into the same base key
        assert!(!keys.contains("prev_ema_9"), "prev_ key must not appear in key()");
    }

    #[test]
    fn scan_zero_arg_funcs() {
        let calls = scan_calls("obv() > 0.0 && ao() > 0.0 && sar() < close");
        let raws: Vec<_> = calls.iter().map(|c| c.raw.as_str()).collect();
        assert!(raws.contains(&"obv"));
        assert!(raws.contains(&"ao"));
        assert!(raws.contains(&"sar"));
        // zero-arg calls have empty args and key == name
        for c in &calls {
            if c.raw == "obv" { assert!(c.args.is_empty()); assert_eq!(c.key(), "obv"); }
        }
    }

    #[test]
    fn scan_multi_field_indicators() {
        let calls = scan_calls("macd_hist(12) > 0.0 && bb_upper(20) > close && stoch_k(14) > 80.0");
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
}

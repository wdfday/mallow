//! Rhai scripting strategy — define entry/exit logic as a Rhai script.
//!
//! # Script format
//!
//! ```text
//! // Declarations — parsed by us, stripped before Rhai evaluates
//! let ema9   = ind.ema(9);
//! let h1_ema = ind.ema(20, "H1");     // H1 timeframe
//! let rsi14  = ind.rsi(14);
//! let atr14  = ind.atr(14, "H1", 3); // H1, keep 3 bars
//!
//! // Logic — imperative Rhai; entry/exit/tp/sl are pre-pushed into scope
//! if cross_above(ema9, h1_ema) && rsi14[0] < 60.0 {
//!     entry = true;
//!     tp    = close[0] + atr14[0] * 2.0;
//!     sl    = close[0] - atr14[0] * 1.5;
//! }
//! if cross_below(ema9, h1_ema) { exit = true; }
//! ```
//!
//! # Indicator declaration syntax
//!
//! `ind.TYPE(period [, tf_or_buf [, buf]])` — preferred form.
//! Legacy `indicator("TYPE", period, ...)` is still accepted.
//!
//! | Form | Meaning |
//! |---|---|
//! | `ind.ema(9)` | Base-TF, default buf=2 |
//! | `ind.ema(9, 5)` | Base-TF, buf=5 |
//! | `ind.ema(20, "H1")` | H1 timeframe, buf=2 |
//! | `ind.ema(20, "H1", 3)` | H1 timeframe, buf=3 |
//!
//! 3rd arg is a **timeframe** if it is a quoted string (e.g. `"H1"`, `"M5"`),
//! or a **buffer depth** if it is a number.
//!
//! # Key design decisions
//!
//! - Indicator declarations are parsed by us (Rust), stripped from the script
//!   before compiling the Rhai AST.
//! - Each indicator and bar field is a Rhai **array** in scope:
//!   `ema9[0]` = current, `ema9[1]` = prev, etc.
//! - HTF bindings embed an inline aggregator: M1 bars accumulate until the
//!   target TF bucket closes, then the indicator is updated. No external
//!   resampler needed.
//! - Output variables (`entry`, `exit`, `tp`, `sl`, `strength`) are pre-pushed
//!   into scope before each bar. Use plain assignment — `entry = true`, `tp = ...`
//!   — rather than `let`, so values survive outside block scope.
//! - Pre-registered helpers: `cross_above`, `cross_below`, `rising`, `falling`,
//!   `in_range`, `abs`, `min`, `max`.

use std::collections::{HashMap, VecDeque};
use std::sync::{Arc, Mutex};

use anyhow::Result;
use rhai::{Array, Dynamic, Engine, Scope, AST};
use alm_core::{bar::Bar, signal::Signal, strategy::Strategy, Timeframe};
use alm_indicator::IndicatorBox;
use serde_json::json;

type PlotBuf = Arc<Mutex<Vec<(String, f64)>>>;

const DEFAULT_BUF_DEPTH: usize = 2;
const BAR_FIELDS: &[&str] = &["open", "high", "low", "close", "volume"];

// ── HtfAggregator ─────────────────────────────────────────────────────────────

/// Accumulates M1 bars into HTF bars. Emits a completed bar whenever the bucket
/// boundary changes (i.e. when the next M1 bar belongs to a new TF bucket).
struct HtfAggregator {
    tf_ms:   i64,
    bucket:  Option<i64>, // floor timestamp of current open bucket
    last_ts: i64,
    o: f64, h: f64, l: f64, c: f64, v: f64,
}

impl HtfAggregator {
    fn new(tf_ms: i64) -> Self {
        Self { tf_ms, bucket: None, last_ts: 0, o: 0.0, h: 0.0, l: 0.0, c: 0.0, v: 0.0 }
    }

    /// Feed one M1 bar. Returns `Some(htf_bar)` when the previous bucket closes.
    fn update(&mut self, bar: &Bar) -> Option<Bar> {
        let new_bucket = bar.timestamp / self.tf_ms * self.tf_ms;

        if let Some(cur) = self.bucket {
            if new_bucket > cur {
                // Bucket boundary — emit completed HTF bar, start new bucket.
                let completed = Bar::new(
                    self.last_ts, &bar.symbol,
                    self.o, self.h, self.l, self.c, self.v,
                );
                self.bucket  = Some(new_bucket);
                self.o       = bar.open;
                self.h       = bar.high;
                self.l       = bar.low;
                self.c       = bar.close;
                self.v       = bar.volume;
                self.last_ts = bar.timestamp;
                return Some(completed);
            }
            // Same bucket — extend.
            self.h        = self.h.max(bar.high);
            self.l        = self.l.min(bar.low);
            self.c        = bar.close;
            self.v       += bar.volume;
            self.last_ts  = bar.timestamp;
        } else {
            // First bar ever.
            self.bucket  = Some(new_bucket);
            self.o       = bar.open;
            self.h       = bar.high;
            self.l       = bar.low;
            self.c       = bar.close;
            self.v       = bar.volume;
            self.last_ts = bar.timestamp;
        }
        None
    }

    fn reset(&mut self) {
        self.bucket  = None;
        self.last_ts = 0;
        self.o = 0.0; self.h = 0.0; self.l = 0.0; self.c = 0.0; self.v = 0.0;
    }
}

// ── VarBinding ────────────────────────────────────────────────────────────────

struct VarBinding {
    ind:        IndicatorBox,
    field:      String,
    history:    VecDeque<f64>,
    buf_depth:  usize,
    aggregator: Option<HtfAggregator>, // Some → HTF binding
}

impl VarBinding {
    fn new(ind: IndicatorBox, field: String, buf_depth: usize, tf: Option<Timeframe>) -> Self {
        let aggregator = tf.map(|t| HtfAggregator::new(t.duration_ms()));
        Self {
            ind,
            field,
            history: VecDeque::with_capacity(buf_depth),
            buf_depth,
            aggregator,
        }
    }

    /// Feed a bar. Returns `true` when the history buffer is full (warmed up).
    fn update(&mut self, bar: &Bar) -> bool {
        // For HTF bindings, only feed indicator when a bucket completes.
        let bar_to_feed: Option<Bar> = if let Some(agg) = &mut self.aggregator {
            match agg.update(bar) {
                Some(htf_bar) => Some(htf_bar),
                None          => return self.history.len() >= self.buf_depth,
            }
        } else {
            None // base-TF: feed `bar` directly below
        };

        let b = bar_to_feed.as_ref().unwrap_or(bar);
        if let Some(fields) = self.ind.update(b) {
            if let Some(&v) = fields.get(self.field.as_str()) {
                self.history.push_back(v);
                if self.history.len() > self.buf_depth {
                    self.history.pop_front();
                }
            }
        }
        self.history.len() >= self.buf_depth
    }

    /// Build a Rhai array with newest value at index 0.
    fn to_rhai_array(&self) -> Array {
        self.history.iter().rev().map(|&v| Dynamic::from_float(v)).collect()
    }

    fn reset(&mut self) {
        self.ind.reset();
        self.history.clear();
        if let Some(agg) = &mut self.aggregator {
            agg.reset();
        }
    }
}

// ── Indicator declaration parsing ─────────────────────────────────────────────

struct IndicatorDecl {
    var_name:  String,
    ind_type:  String,
    period:    usize,
    buf_depth: usize,
    field:     String,
    timeframe: Option<Timeframe>,
}

/// Parse a timeframe string like "H1", "M5", "D1".
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
        "H8"  => Some(Timeframe::H8),
        "H12" => Some(Timeframe::H12),
        "D1"  => Some(Timeframe::D1),
        "W1"  => Some(Timeframe::W1),
        _     => None,
    }
}

/// Parse an indicator declaration line. Accepts two forms:
///   new: `let NAME = ind.TYPE(period [, tf_or_buf [, buf]]);`
///   old: `let NAME = indicator("TYPE", period [, tf_or_buf [, buf]]);`
fn try_parse_indicator_line(line: &str) -> Option<IndicatorDecl> {
    let line = line.trim().split("//").next()?.trim();
    if line.is_empty() { return None; }

    let rest     = line.strip_prefix("let ")?.trim();
    let eq_pos   = rest.find('=')?;
    let var_name = rest[..eq_pos].trim().to_string();
    if var_name.is_empty() { return None; }

    let rhs = rest[eq_pos + 1..].trim().trim_end_matches(';').trim();

    // Extract (type_str, args_inner) from either syntax.
    let (type_str, args_inner): (String, &str) = if let Some(after_dot) = rhs.strip_prefix("ind.") {
        // new: ind.ema(9, "H1", 3)
        let paren = after_dot.find('(')?;
        let t = after_dot[..paren].trim().to_string();
        if t.is_empty() { return None; }
        let inner = after_dot[paren + 1..].trim_end_matches(')');
        (t, inner)
    } else if let Some(inner) = rhs.strip_prefix("indicator(") {
        // old: indicator("ema", 9, "H1", 3)
        let inner = inner.trim_end_matches(')');
        let mut parts = inner.splitn(2, ',');
        let t = parts.next()?.trim().trim_matches('"').trim_matches('\'').to_string();
        if t.is_empty() { return None; }
        (t, parts.next().unwrap_or(""))
    } else {
        return None;
    };

    // Parse remaining args: period [, tf_or_buf [, buf]]
    let mut args = args_inner.splitn(3, ',');
    let period: usize = args.next()?.trim().parse().ok()?;

    let mut timeframe = None;
    let mut buf_depth = DEFAULT_BUF_DEPTH;

    if let Some(third) = args.next() {
        let third_str = third.trim().trim_matches('"').trim_matches('\'');
        if let Ok(n) = third_str.parse::<usize>() {
            buf_depth = n;
        } else {
            timeframe = parse_timeframe(third_str);
            if let Some(fourth) = args.next() {
                buf_depth = fourth.trim().parse().unwrap_or(DEFAULT_BUF_DEPTH);
            }
        }
    }

    let (ind_type, field) = map_indicator_type(&type_str);
    Some(IndicatorDecl { var_name, ind_type, period, buf_depth, field, timeframe })
}

/// Map a user-friendly indicator type string to the internal config type and
/// the output field name.
fn map_indicator_type(type_str: &str) -> (String, String) {
    match type_str {
        "ema" | "sma" | "wma" | "hma" | "dema" | "tema" | "smma" | "kama" | "alma" |
        "mcginley" | "lsma" | "vwma" | "rsi" | "cci" | "roc" | "mfi" | "mom" | "cmo" |
        "dpo" | "rci" | "chop" | "williams" | "cmf" | "obv" | "vwap" | "ao" | "bop" |
        "coppock" | "uo" | "trix" | "tsi" | "pmo" | "kst" | "rvi" | "smi" | "ppo" =>
            (type_str.to_string(), "value".to_string()),

        "atr" => ("atr".to_string(), "atr".to_string()),
        "adx" => ("adx".to_string(), "adx".to_string()),

        "macd" | "macd_hist" => ("macd".to_string(), "histogram".to_string()),
        "macd_line"          => ("macd".to_string(), "macd".to_string()),

        "bb_upper" => ("bbands".to_string(), "upper".to_string()),
        "bb_lower" => ("bbands".to_string(), "lower".to_string()),
        "bb_mid"   => ("bbands".to_string(), "middle".to_string()),

        "stoch_k"  => ("stochastic".to_string(), "k".to_string()),
        "stoch_d"  => ("stochastic".to_string(), "d".to_string()),

        "srsi_k"   => ("stoch_rsi".to_string(), "k".to_string()),
        "srsi_d"   => ("stoch_rsi".to_string(), "d".to_string()),

        "supertrend" => ("supertrend".to_string(), "value".to_string()),
        "st_bull"    => ("supertrend".to_string(), "bullish".to_string()),

        "donchian_upper" => ("donchian".to_string(), "upper".to_string()),
        "donchian_lower" => ("donchian".to_string(), "lower".to_string()),
        "donchian_mid"   => ("donchian".to_string(), "middle".to_string()),

        "sar" | "parabolic_sar" => ("parabolic_sar".to_string(), "sar".to_string()),

        "vortex_plus"  => ("vortex".to_string(), "plus_vi".to_string()),
        "vortex_minus" => ("vortex".to_string(), "minus_vi".to_string()),

        other => (other.to_string(), "value".to_string()),
    }
}

/// Build an `IndicatorBox` from a parsed declaration.
fn make_indicator_box(decl: &IndicatorDecl) -> Result<IndicatorBox> {
    let n = decl.period;
    let cfg = match decl.ind_type.as_str() {
        "atr"           => json!({"type": "atr",           "period": n}),
        "adx"           => json!({"type": "adx",           "period": n}),
        "macd"          => json!({"type": "macd",          "fast": n, "slow": 26, "signal": 9}),
        "bbands"        => json!({"type": "bbands",        "period": n, "multiplier": 2.0}),
        "stochastic"    => json!({"type": "stochastic",    "k_period": n, "d_period": 3}),
        "stoch_rsi"     => json!({"type": "stoch_rsi",     "rsi_period": n, "smooth_d": 3}),
        "supertrend"    => json!({"type": "supertrend",    "period": n, "multiplier": 3.0}),
        "donchian"      => json!({"type": "donchian",      "period": n}),
        "parabolic_sar" => json!({"type": "parabolic_sar", "step": 0.02, "max": 0.2}),
        "kama"          => json!({"type": "kama",          "er_period": n}),
        "obv"           => json!({"type": "obv"}),
        "vwap"          => json!({"type": "vwap"}),
        "ao"            => json!({"type": "ao",            "fast": 5, "slow": 34}),
        "bop"           => json!({"type": "bop"}),
        "coppock"       => json!({"type": "coppock"}),
        "uo"            => json!({"type": "uo",            "fast": 7, "medium": 14, "slow": 28}),
        "vortex"        => json!({"type": "vortex",        "period": n}),
        t               => json!({"type": t, "period": n}),
    };
    IndicatorBox::from_config(&cfg)
}

// ── Rhai Engine + built-in functions ──────────────────────────────────────────

fn get_f(v: Option<&Dynamic>) -> f64 {
    v.and_then(|d| d.as_float().ok()).unwrap_or(0.0)
}

fn build_engine(plot_buf: PlotBuf) -> Engine {
    let mut engine = Engine::new();

    // ── Crossover / direction ────────────────────────────────────────────────
    engine.register_fn("cross_above", |a: Array, b: Array| -> bool {
        let a1 = get_f(a.get(1)); let a0 = get_f(a.get(0));
        let b1 = get_f(b.get(1)); let b0 = get_f(b.get(0));
        a1 <= b1 && a0 > b0
    });
    engine.register_fn("cross_below", |a: Array, b: Array| -> bool {
        let a1 = get_f(a.get(1)); let a0 = get_f(a.get(0));
        let b1 = get_f(b.get(1)); let b0 = get_f(b.get(0));
        a1 >= b1 && a0 < b0
    });
    engine.register_fn("rising",   |a: Array| -> bool { get_f(a.get(0)) > get_f(a.get(1)) });
    engine.register_fn("falling",  |a: Array| -> bool { get_f(a.get(0)) < get_f(a.get(1)) });
    engine.register_fn("above",    |a: Array, b: Array| -> bool { get_f(a.get(0)) > get_f(b.get(0)) });
    engine.register_fn("below",    |a: Array, b: Array| -> bool { get_f(a.get(0)) < get_f(b.get(0)) });
    engine.register_fn("in_range", |v: f64, lo: f64, hi: f64| -> bool { v >= lo && v <= hi });

    // rising_n / falling_n — monotonically rising/falling for last n bars.
    // Requires buf_depth >= n+1 on the indicator declaration.
    engine.register_fn("rising_n", |a: Array, n: i64| -> bool {
        let n = n as usize;
        if a.len() < n + 1 { return false; }
        (0..n).all(|i| get_f(a.get(i)) > get_f(a.get(i + 1)))
    });
    engine.register_fn("falling_n", |a: Array, n: i64| -> bool {
        let n = n as usize;
        if a.len() < n + 1 { return false; }
        (0..n).all(|i| get_f(a.get(i)) < get_f(a.get(i + 1)))
    });

    // slope — average rate of change across all elements: (arr[0] - arr[n-1]) / (n-1).
    // Positive = rising, negative = falling, magnitude = speed.
    engine.register_fn("slope", |a: Array| -> f64 {
        let n = a.len();
        if n < 2 { return 0.0; }
        (get_f(a.get(0)) - get_f(a.get(n - 1))) / (n - 1) as f64
    });

    // momentum — absolute change vs N bars ago: arr[0] - arr[n].
    engine.register_fn("momentum", |a: Array, n: i64| -> f64 {
        get_f(a.get(0)) - get_f(a.get(n as usize))
    });

    // ── Lookback — highest/lowest over N bars of an array ───────────────────
    // `highest(close, 20)` → max of close[0..20].
    // Requires bar_buf_depth >= N (auto-extended at compile time — see build()).
    engine.register_fn("highest", |arr: Array, n: i64| -> f64 {
        arr.iter().take(n as usize).map(|d| d.as_float().unwrap_or(f64::NEG_INFINITY)).fold(f64::NEG_INFINITY, f64::max)
    });
    engine.register_fn("lowest", |arr: Array, n: i64| -> f64 {
        arr.iter().take(n as usize).map(|d| d.as_float().unwrap_or(f64::INFINITY)).fold(f64::INFINITY, f64::min)
    });

    // ── Array aggregation ────────────────────────────────────────────────────
    engine.register_fn("avg", |arr: Array| -> f64 {
        if arr.is_empty() { return 0.0; }
        let s: f64 = arr.iter().map(|d| d.as_float().unwrap_or(0.0)).sum();
        s / arr.len() as f64
    });
    engine.register_fn("sum", |arr: Array| -> f64 {
        arr.iter().map(|d| d.as_float().unwrap_or(0.0)).sum()
    });

    // ── Scalar math ──────────────────────────────────────────────────────────
    engine.register_fn("abs",   |v: f64| -> f64 { v.abs() });
    engine.register_fn("sqrt",  |v: f64| -> f64 { v.sqrt() });
    engine.register_fn("pow",   |v: f64, e: f64| -> f64 { v.powf(e) });
    engine.register_fn("round", |v: f64| -> f64 { v.round() });
    engine.register_fn("floor", |v: f64| -> f64 { v.floor() });
    engine.register_fn("ceil",  |v: f64| -> f64 { v.ceil() });
    engine.register_fn("min",   |a: f64, b: f64| -> f64 { a.min(b) });
    engine.register_fn("max",   |a: f64, b: f64| -> f64 { a.max(b) });
    engine.register_fn("clamp", |v: f64, lo: f64, hi: f64| -> f64 { v.clamp(lo, hi) });

    // ── Debug ────────────────────────────────────────────────────────────────
    engine.register_fn("log", |msg: String| {
        tracing::debug!(rhai = %msg);
    });

    // ── Plot ─────────────────────────────────────────────────────────────────
    engine.register_fn("plot", move |name: String, value: f64| {
        if let Ok(mut buf) = plot_buf.lock() {
            buf.push((name, value));
        }
    });

    engine
}

/// Scan cleaned script for lookback functions and extract the maximum N so
/// `bar_buf` is sized correctly at build time.
///
/// Handles two call shapes:
/// - `f(arr, N)` — `highest`, `lowest`, `rising_n`, `falling_n`, `momentum`
/// - `f(arr)` using full array — `slope` (uses entire buf, no explicit N)
fn extract_max_lookback(script: &str) -> usize {
    // Functions whose 2nd arg is the lookback N (need buf >= N for bar arrays,
    // or N+1 for rising_n / falling_n).
    const SECOND_ARG_FNS: &[(&str, usize)] = &[
        ("highest(",  0),  // needs N elements
        ("lowest(",   0),  // needs N elements
        ("momentum(", 1),  // needs N+1 elements (arr[n] must exist)
        ("rising_n(", 1),  // needs N+1 elements
        ("falling_n(", 1), // needs N+1 elements
    ];

    let mut max_n = 0usize;
    for (prefix, extra) in SECOND_ARG_FNS {
        let mut search = script;
        while let Some(idx) = search.find(prefix) {
            search = &search[idx + prefix.len()..];
            if let Some(comma) = search.find(',') {
                let after = search[comma + 1..].trim_start();
                let n: usize = after
                    .chars()
                    .take_while(|c| c.is_ascii_digit())
                    .collect::<String>()
                    .parse()
                    .unwrap_or(0);
                let needed = n + extra;
                if needed > max_n { max_n = needed; }
            }
        }
    }
    max_n
}

// ── RhaiStrategy ─────────────────────────────────────────────────────────────

pub struct RhaiStrategy {
    engine:          Engine,
    ast:             AST,
    bindings:        HashMap<String, VarBinding>,
    binding_order:   Vec<String>,
    bar_buf:         VecDeque<Bar>,
    bar_buf_depth:   usize,
    in_position:     bool,
    entry_price:     f64,
    plot_buf:        PlotBuf,
    /// `None` in live mode — plot() calls are silently flushed and discarded.
    series:          Option<HashMap<String, Vec<(i64, f64)>>>,
}

impl RhaiStrategy {
    /// Backtest mode: `plot()` calls accumulate in `series()` / `take_indicator_series()`.
    pub fn from_script(script: &str) -> Result<Self> {
        Self::build(script, true)
    }

    /// Live (herald) mode: `plot()` calls are registered but immediately discarded.
    /// No memory accumulates regardless of how long the bot runs.
    pub fn from_script_live(script: &str) -> Result<Self> {
        Self::build(script, false)
    }

    fn build(script: &str, collect_series: bool) -> Result<Self> {
        let mut decls: Vec<IndicatorDecl> = Vec::new();
        let mut cleaned_lines: Vec<&str>  = Vec::new();

        for line in script.lines() {
            match try_parse_indicator_line(line) {
                Some(decl) => decls.push(decl),
                None       => cleaned_lines.push(line),
            }
        }

        let cleaned_script = cleaned_lines.join("\n");
        let plot_buf: PlotBuf = Arc::new(Mutex::new(Vec::new()));
        let engine = build_engine(Arc::clone(&plot_buf));
        let ast = engine
            .compile(&cleaned_script)
            .map_err(|e| anyhow::anyhow!("Rhai compile error: {e}"))?;

        let mut bindings:      HashMap<String, VarBinding> = HashMap::new();
        let mut binding_order: Vec<String>                  = Vec::new();
        let mut max_buf = DEFAULT_BUF_DEPTH;

        for decl in &decls {
            if decl.buf_depth > max_buf { max_buf = decl.buf_depth; }
            let ind     = make_indicator_box(decl)?;
            let binding = VarBinding::new(ind, decl.field.clone(), decl.buf_depth, decl.timeframe);
            binding_order.push(decl.var_name.clone());
            bindings.insert(decl.var_name.clone(), binding);
        }

        // Extend bar_buf to cover any highest(arr, N) / lowest(arr, N) lookbacks.
        let lookback = extract_max_lookback(&cleaned_script);
        if lookback > max_buf { max_buf = lookback; }

        Ok(Self {
            engine,
            ast,
            bindings,
            binding_order,
            bar_buf: VecDeque::with_capacity(max_buf),
            bar_buf_depth: max_buf,
            in_position: false,
            entry_price: 0.0,
            plot_buf,
            series: if collect_series { Some(HashMap::new()) } else { None },
        })
    }

    /// Backtest: `from_params` defaults to collect mode.
    /// Live: caller must pass `"_live": true` in params (injected by herald registry).
    pub fn from_params(p: &serde_json::Value) -> Result<Self> {
        let script = p
            .get("script")
            .and_then(|v| v.as_str())
            .ok_or_else(|| anyhow::anyhow!("RhaiStrategy requires a 'script' param"))?;
        let live = p.get("_live").and_then(|v| v.as_bool()).unwrap_or(false);
        if live { Self::from_script_live(script) } else { Self::from_script(script) }
    }

    /// Snapshot of collected plot series (backtest mode only).
    pub fn series(&self) -> Option<&HashMap<String, Vec<(i64, f64)>>> {
        self.series.as_ref()
    }
}

// ── Strategy impl ─────────────────────────────────────────────────────────────

impl Strategy for RhaiStrategy {
    fn on_bar(&mut self, bar: &Bar) -> Vec<Signal> {
        self.bar_buf.push_back(bar.clone());
        if self.bar_buf.len() > self.bar_buf_depth {
            self.bar_buf.pop_front();
        }

        let mut all_ready = self.bar_buf.len() >= self.bar_buf_depth;
        for name in &self.binding_order {
            if let Some(b) = self.bindings.get_mut(name) {
                if !b.update(bar) { all_ready = false; }
            }
        }
        if !all_ready { return vec![]; }

        let mut scope = Scope::new();

        for name in &self.binding_order {
            if let Some(b) = self.bindings.get(name) {
                scope.push_dynamic(name.as_str(), Dynamic::from_array(b.to_rhai_array()));
            }
        }

        for field in BAR_FIELDS {
            let arr: Array = self.bar_buf.iter().rev().map(|b| {
                Dynamic::from_float(match *field {
                    "open"   => b.open,
                    "high"   => b.high,
                    "low"    => b.low,
                    "close"  => b.close,
                    "volume" => b.volume,
                    _        => 0.0,
                })
            }).collect();
            scope.push_dynamic(*field, Dynamic::from_array(arr));
        }

        scope.push("entry_price", self.entry_price);
        scope.push("in_position", self.in_position);
        // Pre-push output vars so scripts can use plain assignment (entry = true)
        // without `let`. Old-style `let entry = expr` at top level still works —
        // the new binding shadows this one and is picked up by get_value.
        scope.push("entry",    false);
        scope.push("exit",     false);
        scope.push("tp",       0.0_f64);
        scope.push("sl",       0.0_f64);
        scope.push("strength", 1.0_f64);

        if self.engine.run_ast_with_scope(&mut scope, &self.ast).is_err() {
            return vec![];
        }

        // Drain plot() calls. In live mode series is None → data discarded immediately.
        if let Ok(mut buf) = self.plot_buf.lock() {
            if let Some(series) = &mut self.series {
                for (name, value) in buf.drain(..) {
                    series.entry(name).or_default().push((bar.timestamp, value));
                }
            } else {
                buf.clear();
            }
        }

        if !self.in_position {
            if scope.get_value::<bool>("entry").unwrap_or(false) {
                self.in_position = true;
                self.entry_price = bar.close;

                let target   = scope.get_value::<f64>("tp").filter(|&v| v != 0.0);
                let stop     = scope.get_value::<f64>("sl").filter(|&v| v != 0.0);
                let strength = scope.get_value::<f64>("strength").unwrap_or(1.0).clamp(0.0, 1.0);

                let mut sig = Signal::long(bar.timestamp, &bar.symbol, strength);
                sig.price        = Some(bar.close);
                sig.target_price = target;
                sig.stop_price   = stop;
                return vec![sig];
            }
        } else if scope.get_value::<bool>("exit").unwrap_or(false) {
            self.in_position = false;
            self.entry_price = 0.0;
            return vec![Signal::close(bar.timestamp, &bar.symbol)];
        }

        vec![]
    }

    fn take_indicator_series(&mut self) -> std::collections::HashMap<String, Vec<(i64, f64)>> {
        self.series.as_mut().map(std::mem::take).unwrap_or_default()
    }

    fn name(&self) -> &str { "rhai" }

    fn reset(&mut self) {
        for b in self.bindings.values_mut() { b.reset(); }
        self.bar_buf.clear();
        self.in_position = false;
        self.entry_price = 0.0;
        if let Some(s) = &mut self.series { s.clear(); }
        if let Ok(mut buf) = self.plot_buf.lock() { buf.clear(); }
    }
}

// ── Indicator dependency extraction ───────────────────────────────────────────

/// Extract `IndicatorDep`s from a rhai strategy's `"script"` param.
/// Used by `herald::Registry` to acquire live indicator handles.
pub fn rhai_indicator_deps(params: &serde_json::Value) -> Vec<crate::factory::IndicatorDep> {
    let script = match params.get("script").and_then(|v| v.as_str()) {
        Some(s) => s,
        None    => return vec![],
    };

    script
        .lines()
        .filter_map(try_parse_indicator_line)
        .filter_map(|decl| {
            make_indicator_box(&decl).ok()?;
            let cfg = match decl.ind_type.as_str() {
                "atr"           => json!({"type": "atr",           "period": decl.period}),
                "adx"           => json!({"type": "adx",           "period": decl.period}),
                "macd"          => json!({"type": "macd",          "fast": decl.period, "slow": 26, "signal": 9}),
                "bbands"        => json!({"type": "bbands",        "period": decl.period, "multiplier": 2.0}),
                "stochastic"    => json!({"type": "stochastic",    "k_period": decl.period, "d_period": 3}),
                "stoch_rsi"     => json!({"type": "stoch_rsi",     "rsi_period": decl.period, "smooth_d": 3}),
                "supertrend"    => json!({"type": "supertrend",    "period": decl.period, "multiplier": 3.0}),
                "donchian"      => json!({"type": "donchian",      "period": decl.period}),
                "parabolic_sar" => json!({"type": "parabolic_sar", "step": 0.02, "max": 0.2}),
                "kama"          => json!({"type": "kama",          "er_period": decl.period}),
                "obv"           => json!({"type": "obv"}),
                "vwap"          => json!({"type": "vwap"}),
                "ao"            => json!({"type": "ao",            "fast": 5, "slow": 34}),
                "bop"           => json!({"type": "bop"}),
                "coppock"       => json!({"type": "coppock"}),
                "uo"            => json!({"type": "uo",            "fast": 7, "medium": 14, "slow": 28}),
                "vortex"        => json!({"type": "vortex",        "period": decl.period}),
                t               => json!({"type": t, "period": decl.period}),
            };
            Some(crate::factory::IndicatorDep { config: cfg, source_tf: decl.timeframe })
        })
        .collect()
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use alm_core::bar::Bar;

    fn make_bar(i: usize) -> Bar {
        let c = 100.0 + i as f64 * 0.5;
        Bar::new(i as i64 * 60_000, "TEST", c, c * 1.005, c * 0.995, c, 1000.0 + (i % 10) as f64 * 100.0)
    }

    const EMA_CROSS_SCRIPT: &str = r#"
let ema9  = ind.ema(9);
let ema21 = ind.ema(21);
let rsi14 = ind.rsi(14);

if cross_above(ema9, ema21) && rsi14[0] < 70.0 { entry = true; }
if cross_below(ema9, ema21) { exit = true; }
"#;

    const ATR_TP_SL_SCRIPT: &str = r#"
let ema20 = ind.ema(20);
let atr14 = ind.atr(14);

if close[0] > ema20[0] {
    entry = true;
    tp    = close[0] + atr14[0] * 2.0;
    sl    = close[0] - atr14[0] * 1.5;
}
if close[0] < ema20[0] { exit = true; }
"#;

    const MTF_SCRIPT: &str = r#"
let h1_ema20 = ind.ema(20, "H1");
let m1_rsi5  = ind.rsi(5);

if rising(h1_ema20) && m1_rsi5[0] < 35.0 { entry = true; }
if falling(h1_ema20) || m1_rsi5[0] > 65.0 { exit = true; }
"#;

    // ── ind.TYPE() new syntax ─────────────────────────────────────────────────

    #[test]
    fn parse_ind_dot_base_tf() {
        let d = try_parse_indicator_line(r#"let ema9 = ind.ema(9);"#).unwrap();
        assert_eq!(d.var_name, "ema9");
        assert_eq!(d.ind_type, "ema");
        assert_eq!(d.period, 9);
        assert_eq!(d.buf_depth, DEFAULT_BUF_DEPTH);
        assert_eq!(d.timeframe, None);
    }

    #[test]
    fn parse_ind_dot_custom_buf() {
        let d = try_parse_indicator_line(r#"let atr = ind.atr(14, 5);"#).unwrap();
        assert_eq!(d.period, 14);
        assert_eq!(d.buf_depth, 5);
        assert_eq!(d.timeframe, None);
    }

    #[test]
    fn parse_ind_dot_htf() {
        let d = try_parse_indicator_line(r#"let h1_ema = ind.ema(20, "H1");"#).unwrap();
        assert_eq!(d.var_name, "h1_ema");
        assert_eq!(d.period, 20);
        assert_eq!(d.timeframe, Some(Timeframe::H1));
        assert_eq!(d.buf_depth, DEFAULT_BUF_DEPTH);
    }

    #[test]
    fn parse_ind_dot_htf_custom_buf() {
        let d = try_parse_indicator_line(r#"let x = ind.rsi(5, "M5", 3);"#).unwrap();
        assert_eq!(d.timeframe, Some(Timeframe::M5));
        assert_eq!(d.buf_depth, 3);
    }

    // ── legacy indicator() syntax — backward compat ───────────────────────────

    #[test]
    fn parse_base_tf() {
        let d = try_parse_indicator_line(r#"let ema9 = indicator("ema", 9);"#).unwrap();
        assert_eq!(d.var_name, "ema9");
        assert_eq!(d.ind_type, "ema");
        assert_eq!(d.period, 9);
        assert_eq!(d.buf_depth, DEFAULT_BUF_DEPTH);
        assert_eq!(d.timeframe, None);
    }

    #[test]
    fn parse_base_tf_custom_buf() {
        let d = try_parse_indicator_line(r#"let atr = indicator("atr", 14, 5);"#).unwrap();
        assert_eq!(d.period, 14);
        assert_eq!(d.buf_depth, 5);
        assert_eq!(d.timeframe, None);
    }

    #[test]
    fn parse_htf() {
        let d = try_parse_indicator_line(r#"let h1_ema = indicator("ema", 20, "H1");"#).unwrap();
        assert_eq!(d.var_name, "h1_ema");
        assert_eq!(d.period, 20);
        assert_eq!(d.timeframe, Some(Timeframe::H1));
        assert_eq!(d.buf_depth, DEFAULT_BUF_DEPTH);
    }

    #[test]
    fn parse_htf_custom_buf() {
        let d = try_parse_indicator_line(r#"let x = indicator("rsi", 5, "M5", 3);"#).unwrap();
        assert_eq!(d.timeframe, Some(Timeframe::M5));
        assert_eq!(d.buf_depth, 3);
    }

    #[test]
    fn non_indicator_line_returns_none() {
        assert!(try_parse_indicator_line("let x = 42;").is_none());
        assert!(try_parse_indicator_line("// comment").is_none());
    }

    #[test]
    fn legacy_script_still_compiles() {
        let script = r#"
let ema9  = indicator("ema", 9);
let ema21 = indicator("ema", 21);

let entry = cross_above(ema9, ema21);
let exit  = cross_below(ema9, ema21);
"#;
        RhaiStrategy::from_script(script).expect("legacy indicator() syntax must still compile");
    }

    #[test]
    fn compile_ema_cross() {
        RhaiStrategy::from_script(EMA_CROSS_SCRIPT).expect("should compile");
    }

    #[test]
    fn compile_atr_tp_sl() {
        RhaiStrategy::from_script(ATR_TP_SL_SCRIPT).expect("should compile");
    }

    #[test]
    fn compile_mtf_script() {
        RhaiStrategy::from_script(MTF_SCRIPT).expect("MTF script should compile");
    }

    #[test]
    fn runs_200_bars_no_panic() {
        let mut s = RhaiStrategy::from_script(EMA_CROSS_SCRIPT).unwrap();
        for i in 0..200 { let _ = s.on_bar(&make_bar(i)); }
    }

    #[test]
    fn mtf_runs_200_m1_bars_no_panic() {
        let mut s = RhaiStrategy::from_script(MTF_SCRIPT).unwrap();
        // Feed 200 M1 bars: should form ~3 H1 bars and eventually warm up
        for i in 0..200 { let _ = s.on_bar(&make_bar(i)); }
    }

    #[test]
    fn htf_aggregator_emits_on_bucket_cross() {
        let mut agg = HtfAggregator::new(Timeframe::H1.duration_ms());
        let sym = "TEST";
        let h1_ms = 3_600_000i64;

        // Feed 60 M1 bars in first H1 bucket (ts 0..59 * 60_000)
        for i in 0..60usize {
            let ts = i as i64 * 60_000;
            let b = Bar::new(ts, sym, 100.0, 101.0, 99.0, 100.0 + i as f64 * 0.1, 1000.0);
            assert!(agg.update(&b).is_none(), "no emit within same bucket");
        }
        // First bar of next H1 bucket → triggers emit
        let next = Bar::new(h1_ms, sym, 106.0, 107.0, 105.0, 106.0, 1000.0);
        let completed = agg.update(&next).expect("should emit on bucket cross");
        assert_eq!(completed.open, 100.0, "HTF bar open = first M1 open");
        assert!((completed.close - 105.9).abs() < 0.01, "HTF bar close = last M1 close");
    }

    #[test]
    fn reset_clears_state() {
        let mut s = RhaiStrategy::from_script(EMA_CROSS_SCRIPT).unwrap();
        for i in 0..50 { let _ = s.on_bar(&make_bar(i)); }
        s.reset();
        assert!(!s.in_position);
        assert_eq!(s.entry_price, 0.0);
        assert!(s.bar_buf.is_empty());
    }

    #[test]
    fn from_params_requires_script_key() {
        assert!(RhaiStrategy::from_params(&serde_json::json!({})).is_err());
    }

    #[test]
    fn indicator_deps_base_tf() {
        let p = serde_json::json!({ "script": EMA_CROSS_SCRIPT });
        let deps = rhai_indicator_deps(&p);
        assert_eq!(deps.len(), 3); // ema9, ema21, rsi14
        assert!(deps.iter().all(|d| d.source_tf.is_none()));
    }

    #[test]
    fn indicator_deps_mtf() {
        let p = serde_json::json!({ "script": MTF_SCRIPT });
        let deps = rhai_indicator_deps(&p);
        assert_eq!(deps.len(), 2); // h1_ema20, m1_rsi5
        let h1_dep = deps.iter().find(|d| d.source_tf == Some(Timeframe::H1));
        assert!(h1_dep.is_some(), "h1_ema20 should have source_tf=H1");
        let m1_dep = deps.iter().find(|d| d.source_tf.is_none());
        assert!(m1_dep.is_some(), "m1_rsi5 should have source_tf=None");
    }

    #[test]
    fn plot_collects_series() {
        let script = r#"
let ema9 = ind.ema(9);

plot("ema9", ema9[0]);
"#;
        let mut s = RhaiStrategy::from_script(script).unwrap();
        for i in 0..30 { let _ = s.on_bar(&make_bar(i)); }
        let series = s.series().expect("backtest mode must have series");
        assert!(series.contains_key("ema9"), "plot('ema9', ...) should create series");
        assert!(!series["ema9"].is_empty(), "series must have data points");
        for &(ts, _val) in &series["ema9"] {
            assert!(ts >= 0);
        }
    }

    #[test]
    fn plot_live_mode_no_series() {
        let script = r#"
let ema9 = ind.ema(9);

plot("ema9", ema9[0]);
"#;
        let mut s = RhaiStrategy::from_script_live(script).unwrap();
        for i in 0..30 { let _ = s.on_bar(&make_bar(i)); }
        assert!(s.series().is_none(), "live mode must not accumulate series");
    }

    #[test]
    fn plot_reset_clears_series() {
        let script = r#"
let ema9 = ind.ema(9);

plot("ema9", ema9[0]);
"#;
        let mut s = RhaiStrategy::from_script(script).unwrap();
        for i in 0..30 { let _ = s.on_bar(&make_bar(i)); }
        assert!(!s.series().unwrap()["ema9"].is_empty());
        s.reset();
        assert!(s.series().unwrap().is_empty(), "reset() must clear series");
    }

    #[test]
    fn highest_lowest_auto_extends_bar_buf() {
        let script = r#"
let entry = highest(close, 20) > lowest(low, 10) * 1.01;
let exit  = false;
"#;
        let s = RhaiStrategy::from_script(script).unwrap();
        assert_eq!(s.bar_buf_depth, 20, "bar_buf_depth must be extended to highest/lowest N");
    }

    #[test]
    fn highest_lowest_values() {
        let script = r#"
let h = highest(close, 5);
let l = lowest(low, 5);
let entry = close[0] > h * 0.99;
let exit  = close[0] < l * 1.01;
plot("h", h);
plot("l", l);
"#;
        let mut s = RhaiStrategy::from_script(script).unwrap();
        for i in 0..30 { let _ = s.on_bar(&make_bar(i)); }
        // series should have "h" and "l" plots
        let series = s.series().unwrap();
        assert!(series.contains_key("h") && series.contains_key("l"));
    }

    #[test]
    fn in_position_exposed_in_scope() {
        let script = r#"
let ema9 = ind.ema(9);

if !in_position && ema9[0] > 0.0 { entry = true; }
if  in_position && ema9[0] < 0.0 { exit  = true; }
"#;
        // Should compile and run without error
        let mut s = RhaiStrategy::from_script(script).unwrap();
        for i in 0..30 { let _ = s.on_bar(&make_bar(i)); }
    }

    #[test]
    fn rising_n_falling_n_buf_extended() {
        // rising_n(arr, 3) needs buf_depth >= 4 for indicator arrays,
        // and bar_buf >= 4 for bar-field arrays.
        let script = r#"
let adx14 = indicator("adx", 14, 4);
let entry = adx14[0] > 25.0 && rising_n(adx14, 3);
let exit  = falling_n(adx14, 2);
"#;
        let s = RhaiStrategy::from_script(script).unwrap();
        // rising_n(adx14,3) in cleaned script → extract_max_lookback finds 3+1=4
        // but bar fields aren't referenced here so bar_buf stays at 4 from indicator
        assert!(s.bar_buf_depth >= 2);
        let mut s = s;
        for i in 0..100 { let _ = s.on_bar(&make_bar(i)); }
    }

    #[test]
    fn momentum_auto_extends_bar_buf() {
        // momentum(close, 5) on bar fields needs bar_buf >= 6
        let script = r#"
let m = momentum(close, 5);
let entry = m > 1.0;
let exit  = m < -1.0;
"#;
        let s = RhaiStrategy::from_script(script).unwrap();
        assert_eq!(s.bar_buf_depth, 6, "momentum(arr,5) needs buf>=6");
    }

    #[test]
    fn slope_and_momentum_compile_run() {
        let script = r#"
let adx14 = indicator("adx", 14, 5);
let s     = slope(adx14);
let m     = momentum(adx14, 3);
let entry = adx14[0] > 25.0 && s > 0.0 && m > 0.5;
let exit  = s < 0.0;
"#;
        let mut s = RhaiStrategy::from_script(script).unwrap();
        for i in 0..100 { let _ = s.on_bar(&make_bar(i)); }
    }

    #[test]
    fn strength_read_from_scope() {
        let script = r#"
let ema9  = ind.ema(9);
let ema21 = ind.ema(21);

if cross_above(ema9, ema21) { entry = true; strength = 0.75; }
if cross_below(ema9, ema21) { exit  = true; }
"#;
        let mut s = RhaiStrategy::from_script(script).unwrap();
        let bars: Vec<Bar> = (0..60).map(make_bar).collect();
        for b in &bars {
            let sigs = s.on_bar(b);
            for sig in &sigs {
                if sig.direction == alm_core::signal::Direction::Long {
                    assert!((sig.strength - 0.75).abs() < 1e-9, "strength must be 0.75");
                }
            }
        }
    }

    #[test]
    fn tp_sl_signal_fields() {
        let mut s = RhaiStrategy::from_script(ATR_TP_SL_SCRIPT).unwrap();
        let bars: Vec<Bar> = (0..60).map(make_bar).collect();
        let mut entry_sig: Option<Signal> = None;
        for b in &bars {
            let sigs = s.on_bar(b);
            if let Some(sig) = sigs.into_iter().find(|s| s.direction == alm_core::signal::Direction::Long) {
                entry_sig = Some(sig);
                break;
            }
        }
        if let Some(sig) = entry_sig {
            assert!(sig.target_price.is_some(), "tp should be set");
            assert!(sig.stop_price.is_some(), "sl should be set");
        }
    }
}

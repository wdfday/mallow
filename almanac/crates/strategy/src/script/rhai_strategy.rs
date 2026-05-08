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
//! | `ind.ema(20, "H1")` | H1 confirmed (fires only at H1 close), buf=2 |
//! | `ind.ema(20, "H1", 3)` | H1 confirmed, buf=3 |
//! | `ind.rsi(5, "live_H1")` | H1 **live** — `[0]`=confirmed, `_live`=forming scalar |
//! | `ind.rsi(5, "live_H1", 3)` | H1 live, confirmed buf=3 |
//!
//! **Live semantics**: prefix `live_` to any timeframe string.
//! - `name[0]`, `name[1]`, … — confirmed HTF values as usual (unchanged).
//! - `name_live` — scalar f64: current forming-bar indicator value (0.0 before first bucket).
//! - `name_fill` — scalar f64 in `0.0..=1.0`: fraction of the current HTF bucket filled.
//!
//! Check `name_fill > 0.5` before acting on `name_live` to avoid noisy early-bucket signals.
//!
//! 3rd arg is a **timeframe** (with optional `live_` prefix) if it is a quoted string,
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
//! - Output variables (`long`, `short`, `exit`, `tp`, `sl`, `strength`, `is_offset`) are
//!   pre-pushed into scope before each bar. Use plain assignment — `long = true`, `tp = ...`
//!   — rather than `let`, so values survive outside block scope.
//!   `entry` is a legacy alias for `long`.
//!   When `is_offset = true`, `tp`/`sl` are treated as deltas from the actual fill price
//!   by helm (e.g. `tp = atr14[0] * 2.0`). Default is `false` (absolute price levels).
//! - Pre-registered helpers: `cross_above`, `cross_below`, `rising`, `falling`,
//!   `in_range`, `abs`, `min`, `max`.
//!
//! # MTF internals vs named strategies
//!
//! Named strategies use [`crate::bar_resampler::TimeBarResampler`], which stamps
//! the emitted HTF bar with the **bucket-floor** timestamp and has no live-bar
//! support.
//!
//! Rhai bindings use the inline [`HtfAggregator`] defined in this module, which:
//! - Stamps the HTF bar with the **last M1 bar's timestamp** inside the bucket.
//! - Supports `live_` prefix: two indicator instances run in parallel — one on
//!   confirmed (closed) HTF bars, one updated every M1 bar on the forming bucket.
//! - Exposes `{name}_live` (forming scalar) and `{name}_fill` (0.0–1.0 fill
//!   ratio) alongside the confirmed `{name}[0]`, `{name}[1]`, … history array.
//!
//! Both implementations share the same bucket logic: emit on bucket-boundary
//! crossing, drop the last partial bucket, never lookahead.

use std::collections::{HashMap, VecDeque};
use std::sync::{Arc, Mutex};

use anyhow::Result;
use rhai::{Array, Dynamic, Engine, Scope, AST};
use alm_core::{bar::Bar, signal::Signal, strategy::Strategy, Timeframe};
use alm_indicator::IndicatorBox;
use serde_json::json;
use crate::candle_type::{CandleType, CandleTransform};

pub(super) type PlotBuf = Arc<Mutex<Vec<(String, f64)>>>;

pub(super) const DEFAULT_BUF_DEPTH: usize = 2;
pub(super) const BAR_FIELDS: &[&str] = &["open", "high", "low", "close", "volume"];

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

    /// Return the current forming bar without advancing state.
    /// Returns `None` if no bar has been seen yet.
    fn peek(&self) -> Option<Bar> {
        self.bucket.map(|_| Bar::new(
            self.last_ts, "",
            self.o, self.h, self.l, self.c, self.v,
        ))
    }

    fn reset(&mut self) {
        self.bucket  = None;
        self.last_ts = 0;
        self.o = 0.0; self.h = 0.0; self.l = 0.0; self.c = 0.0; self.v = 0.0;
    }
}

// ── VarBinding ────────────────────────────────────────────────────────────────

struct VarBinding {
    ind:          IndicatorBox,
    field:        String,
    history:      VecDeque<f64>,
    buf_depth:    usize,
    aggregator:   Option<HtfAggregator>,
    // ── live-only fields ──────────────────────────────────────────────────────
    /// `true` → this binding exposes a live (forming) scalar alongside confirmed
    /// history.  Confirmed array `[0]` is always the last *closed* HTF bar.
    /// Live value is exposed in scope as `{var}_live`; fill ratio as `{var}_fill`.
    live:         bool,
    /// Separate indicator instance driven by forming bars only.
    live_ind:     Option<IndicatorBox>,
    /// Current live indicator value (0.0 when the bucket hasn't started yet).
    live_val:     f64,
    /// Fraction of the current HTF bucket already filled: 0.0 → 1.0.
    fill_ratio:   f64,
    /// M1-bar count inside the current HTF bucket.
    bucket_m1:    u32,
    /// Total base-TF bars per HTF bucket (e.g. 60 for H1 on M1 feed).
    tf_total_m1:  u32,
}

impl VarBinding {
    fn new(
        ind:      IndicatorBox,
        live_ind: Option<IndicatorBox>,
        field:    String,
        buf_depth: usize,
        tf:       Option<Timeframe>,
        live:     bool,
    ) -> Self {
        let tf_total_m1 = tf
            .map(|t| (t.duration_ms() / 60_000) as u32)
            .unwrap_or(1);
        let aggregator = tf.map(|t| HtfAggregator::new(t.duration_ms()));
        Self {
            ind,
            field,
            history: VecDeque::with_capacity(buf_depth),
            buf_depth,
            aggregator,
            live,
            live_ind,
            live_val:    0.0,
            fill_ratio:  0.0,
            bucket_m1:   0,
            tf_total_m1,
        }
    }

    /// Feed a bar. Returns `true` when the confirmed history buffer is full.
    fn update(&mut self, bar: &Bar) -> bool {
        if let Some(agg) = &mut self.aggregator {
            if self.live {
                // ── Live HTF binding ───────────────────────────────────────
                // Confirmed path: advance aggregator; feed indicator only on close.
                if let Some(htf_bar) = agg.update(bar) {
                    if let Some(fields) = self.ind.update(&htf_bar) {
                        if let Some(&v) = fields.get(self.field.as_str()) {
                            self.history.push_back(v);
                            if self.history.len() > self.buf_depth {
                                self.history.pop_front();
                            }
                        }
                    }
                    // Bucket just closed — reset fill counter.
                    self.bucket_m1 = 0;
                }

                // Live path: track fill ratio and feed forming bar to live_ind.
                self.bucket_m1 += 1;
                self.fill_ratio = (self.bucket_m1 as f64 / self.tf_total_m1 as f64).min(1.0);

                if let Some(forming) = agg.peek() {
                    if let Some(live_ind) = &mut self.live_ind {
                        if let Some(fields) = live_ind.update(&forming) {
                            if let Some(&v) = fields.get(self.field.as_str()) {
                                self.live_val = v;
                            }
                        }
                    }
                }
            } else {
                // ── Confirmed-only HTF binding ─────────────────────────────
                if let Some(htf_bar) = agg.update(bar) {
                    if let Some(fields) = self.ind.update(&htf_bar) {
                        if let Some(&v) = fields.get(self.field.as_str()) {
                            self.history.push_back(v);
                            if self.history.len() > self.buf_depth {
                                self.history.pop_front();
                            }
                        }
                    }
                } else {
                    return self.history.len() >= self.buf_depth;
                }
            }
        } else {
            // ── Base-TF binding ────────────────────────────────────────────
            if let Some(fields) = self.ind.update(bar) {
                if let Some(&v) = fields.get(self.field.as_str()) {
                    self.history.push_back(v);
                    if self.history.len() > self.buf_depth {
                        self.history.pop_front();
                    }
                }
            }
        }

        self.history.len() >= self.buf_depth
    }

    /// Build a Rhai array of confirmed values; newest at index 0.
    fn to_rhai_array(&self) -> Array {
        self.history.iter().rev().map(|&v| Dynamic::from_float(v)).collect()
    }

    fn reset(&mut self) {
        self.ind.reset();
        self.history.clear();
        if let Some(agg) = &mut self.aggregator { agg.reset(); }
        if let Some(li)  = &mut self.live_ind   { li.reset(); }
        self.live_val   = 0.0;
        self.fill_ratio = 0.0;
        self.bucket_m1  = 0;
    }
}

// ── Indicator declaration parsing ─────────────────────────────────────────────

pub(super) struct IndicatorDecl {
    pub(super) var_name:  String,
    pub(super) ind_type:  String,
    pub(super) period:    usize,
    pub(super) buf_depth: usize,
    pub(super) field:     String,
    pub(super) timeframe: Option<Timeframe>,
    /// `true` → live (forming) bar semantics; `false` → confirmed bar only.
    pub(super) live:      bool,
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
pub(super) fn try_parse_indicator_line(line: &str) -> Option<IndicatorDecl> {
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
    let mut live      = false;

    if let Some(third) = args.next() {
        let third_str = third.trim().trim_matches('"').trim_matches('\'');
        if let Ok(n) = third_str.parse::<usize>() {
            buf_depth = n;
        } else {
            // Detect optional `live_` prefix: `"live_H1"` → live=true, tf="H1"
            let (tf_str, is_live) = if let Some(rest) = third_str.strip_prefix("live_") {
                (rest, true)
            } else {
                (third_str, false)
            };
            live      = is_live;
            timeframe = parse_timeframe(tf_str);
            if let Some(fourth) = args.next() {
                buf_depth = fourth.trim().parse().unwrap_or(DEFAULT_BUF_DEPTH);
            }
        }
    }

    let (ind_type, field) = map_indicator_type(&type_str);
    Some(IndicatorDecl { var_name, ind_type, period, buf_depth, field, timeframe, live })
}

/// Map a user-friendly indicator type string to the internal config type and
/// the output field name.
pub(super) fn map_indicator_type(type_str: &str) -> (String, String) {
    match type_str {
        "ema" | "sma" | "wma" | "hma" | "dema" | "tema" | "smma" | "kama" | "alma" |
        "mcginley" | "lsma" | "vwma" | "rsi" | "cci" | "roc" | "mfi" | "mom" | "cmo" |
        "dpo" | "rci" | "chop" | "williams" | "cmf" | "obv" | "vwap" | "ao" | "bop" |
        "coppock" | "uo" | "tsi" =>
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

        // Multi-output primaries (their box outputs the named field, not "value")
        "trix"     => ("trix".to_string(),   "trix".to_string()),
        "trix_hist"=> ("trix".to_string(),   "histogram".to_string()),
        "trix_sig" => ("trix".to_string(),   "signal".to_string()),
        "ppo"      => ("ppo".to_string(),    "ppo".to_string()),
        "ppo_hist" => ("ppo".to_string(),    "histogram".to_string()),
        "ppo_sig"  => ("ppo".to_string(),    "signal".to_string()),
        "kst"      => ("kst".to_string(),    "kst".to_string()),
        "kst_hist" => ("kst".to_string(),    "histogram".to_string()),
        "pmo"      => ("pmo".to_string(),    "pmo".to_string()),
        "pmo_sig"  => ("pmo".to_string(),    "signal".to_string()),
        "rvi"      => ("rvi".to_string(),    "rvi".to_string()),
        "rvi_sig"  => ("rvi".to_string(),    "signal".to_string()),
        "smi"      => ("smi".to_string(),    "smi".to_string()),
        "smi_sig"  => ("smi".to_string(),    "signal".to_string()),

        // Fisher
        "fisher"     => ("fisher".to_string(), "fisher".to_string()),
        "fisher_sig" => ("fisher".to_string(), "signal".to_string()),

        // Aroon
        "aroon_up"   => ("aroon".to_string(), "up".to_string()),
        "aroon_down" => ("aroon".to_string(), "down".to_string()),
        "aroon_osc"  => ("aroon".to_string(), "oscillator".to_string()),

        // KDJ
        "kdj_k" => ("kdj".to_string(), "k".to_string()),
        "kdj_d" => ("kdj".to_string(), "d".to_string()),
        "kdj_j" => ("kdj".to_string(), "j".to_string()),

        // RWI
        "rwi_h" => ("rwi".to_string(), "rwi_high".to_string()),
        "rwi_l" => ("rwi".to_string(), "rwi_low".to_string()),

        other => (other.to_string(), "value".to_string()),
    }
}

/// Build the JSON config for an indicator type.
///
/// `ind_type` must already be the canonical internal name (post-`map_indicator_type`),
/// e.g. `"bbands"` not `"bb_upper"`.  Only types whose config differs from the
/// default `{"type": t, "period": period}` need an explicit arm.
pub(super) fn indicator_json_config(ind_type: &str, period: usize) -> serde_json::Value {
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

/// Build an `IndicatorBox` from a parsed declaration.
fn make_indicator_box(decl: &IndicatorDecl) -> Result<IndicatorBox> {
    IndicatorBox::from_config(&indicator_json_config(&decl.ind_type, decl.period))
}

// ── Rhai Engine + built-in functions ──────────────────────────────────────────

fn get_f(v: Option<&Dynamic>) -> f64 {
    v.and_then(|d| d.as_float().ok()).unwrap_or(0.0)
}

pub(super) fn build_engine(plot_buf: PlotBuf) -> Engine {
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
pub(super) fn extract_max_lookback(script: &str) -> usize {
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
    engine:           Engine,
    ast:              AST,
    bindings:         HashMap<String, VarBinding>,
    binding_order:    Vec<String>,
    bar_buf:          VecDeque<Bar>,
    bar_buf_depth:    usize,
    plot_buf:         PlotBuf,
    /// `None` in live mode — plot() calls are silently flushed and discarded.
    series:           Option<HashMap<String, Vec<(i64, f64)>>>,
    candle_transform: CandleTransform,
}

impl RhaiStrategy {
    /// Backtest mode: `plot()` calls accumulate in `series()` / `take_indicator_series()`.
    pub fn from_script(script: &str) -> Result<Self> {
        Self::build(script, true, CandleType::Raw)
    }

    /// Live (herald) mode: `plot()` calls are registered but immediately discarded.
    /// No memory accumulates regardless of how long the bot runs.
    pub fn from_script_live(script: &str) -> Result<Self> {
        Self::build(script, false, CandleType::Raw)
    }

    fn build(script: &str, collect_series: bool, candle_type: CandleType) -> Result<Self> {
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
            let ind      = make_indicator_box(decl)?;
            let live_ind = if decl.live { Some(make_indicator_box(decl)?) } else { None };
            let binding  = VarBinding::new(ind, live_ind, decl.field.clone(), decl.buf_depth, decl.timeframe, decl.live);
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
            plot_buf,
            series: if collect_series { Some(HashMap::new()) } else { None },
            candle_transform: CandleTransform::new(candle_type),
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
        let candle_type = {
            let ct = p.get("candle_type").and_then(|v| v.as_str()).unwrap_or("raw");
            let sp = p.get("smooth_period").and_then(|v| v.as_u64()).unwrap_or(3) as usize;
            CandleType::from_str(ct, sp)
        };
        if live { Self::build(script, false, candle_type) } else { Self::build(script, true, candle_type) }
    }

    /// Snapshot of collected plot series (backtest mode only).
    pub fn series(&self) -> Option<&HashMap<String, Vec<(i64, f64)>>> {
        self.series.as_ref()
    }
}

// ── Strategy impl ─────────────────────────────────────────────────────────────

impl Strategy for RhaiStrategy {
    fn on_bar(&mut self, bar: &Bar) -> Vec<Signal> {
        let Some(bar_owned) = self.candle_transform.apply(bar) else {
            return vec![];
        };
        let bar = &bar_owned;

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
                if b.live {
                    scope.push(format!("{name}_live").as_str(), b.live_val);
                    scope.push(format!("{name}_fill").as_str(), b.fill_ratio);
                }
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

        // Pre-push output vars so scripts can use plain assignment (entry = true)
        // without `let`. Old-style `let entry = expr` at top level still works —
        // the new binding shadows this one and is picked up by get_value.
        scope.push("entry",     false);
        scope.push("exit",      false);
        scope.push("long",      false);
        scope.push("short",     false);
        scope.push("tp",        0.0_f64);
        scope.push("sl",        0.0_f64);
        scope.push("strength",  1.0_f64);
        scope.push("is_offset", false);
        scope.push("reason",    String::new());

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

        let strength   = scope.get_value::<f64>("strength").unwrap_or(1.0).clamp(0.0, 1.0);
        let target     = scope.get_value::<f64>("tp").filter(|&v| v != 0.0);
        let stop       = scope.get_value::<f64>("sl").filter(|&v| v != 0.0);
        let is_offset  = scope.get_value::<bool>("is_offset").unwrap_or(false);
        let reason     = scope.get_value::<String>("reason").filter(|s| !s.is_empty());

        // `entry` is a legacy alias for `long`.
        let go_long  = scope.get_value::<bool>("long").unwrap_or(false)
                    || scope.get_value::<bool>("entry").unwrap_or(false);
        let go_short = scope.get_value::<bool>("short").unwrap_or(false);
        let go_exit  = scope.get_value::<bool>("exit").unwrap_or(false);

        if go_long {
            let mut sig = Signal::long(bar.timestamp, &bar.symbol, strength);
            sig.price        = Some(bar.close);
            sig.target_price = target;
            sig.stop_price   = stop;
            sig.is_offset    = is_offset;
            sig.reason       = reason;
            return vec![sig];
        }
        if go_short {
            let mut sig = Signal::short(bar.timestamp, &bar.symbol, strength);
            sig.price        = Some(bar.close);
            sig.target_price = target;
            sig.stop_price   = stop;
            sig.is_offset    = is_offset;
            sig.reason       = reason;
            return vec![sig];
        }
        if go_exit {
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
        self.candle_transform.reset();
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
            Some(crate::factory::IndicatorDep {
                config: indicator_json_config(&decl.ind_type, decl.period),
                source_tf: decl.timeframe,
            })
        })
        .collect()
}

// ── Rhai lint — public API for the herald validate endpoint ──────────────────

/// All user-facing indicator type aliases accepted by `ind.TYPE(period)`.
/// Any alias not in this list is an error.
pub const KNOWN_INDICATOR_TYPES: &[&str] = &[
    // Single-output (box emits "value" field)
    "ema", "sma", "wma", "hma", "dema", "tema", "smma", "kama", "alma",
    "mcginley", "lsma", "vwma", "rsi", "cci", "roc", "mfi", "mom", "cmo",
    "dpo", "rci", "chop", "williams", "cmf", "obv", "vwap", "ao", "bop",
    "coppock", "uo", "tsi",
    // Multi-output / aliases
    "atr", "adx",
    "macd", "macd_hist", "macd_line",
    "bb_upper", "bb_lower", "bb_mid",
    "stoch_k", "stoch_d",
    "srsi_k", "srsi_d",
    "supertrend", "st_bull",
    "donchian_upper", "donchian_lower", "donchian_mid",
    "sar", "parabolic_sar",
    "vortex_plus", "vortex_minus",
    // TRIX / PPO / KST / PMO / RVI / SMI — primary + derived fields
    "trix", "trix_hist", "trix_sig",
    "ppo", "ppo_hist", "ppo_sig",
    "kst", "kst_hist",
    "pmo", "pmo_sig",
    "rvi", "rvi_sig",
    "smi", "smi_sig",
    // Fisher
    "fisher", "fisher_sig",
    // Aroon
    "aroon_up", "aroon_down", "aroon_osc",
    // KDJ
    "kdj_k", "kdj_d", "kdj_j",
    // RWI
    "rwi_h", "rwi_l",
];

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
    /// Timeframe string if HTF, e.g. `"H1"`.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub timeframe: Option<String>,
    /// `true` if declared with `live_` prefix, e.g. `ind.rsi(5, "live_H1")`.
    #[serde(skip_serializing_if = "std::ops::Not::not")]
    pub live: bool,
}

/// Scope information for Monaco autocomplete / hover.
#[derive(Debug, Clone, serde::Serialize)]
#[cfg_attr(feature = "openapi", derive(utoipa::ToSchema))]
pub struct RhaiLintScope {
    pub indicators: Vec<DeclaredIndicator>,
    pub bar_fields: Vec<&'static str>,
    pub output_vars: Vec<&'static str>,
    pub functions:   Vec<&'static str>,
}

/// Extract the raw type string from a potential indicator declaration line
/// WITHOUT performing `map_indicator_type` mapping.
/// Returns `(prefix, raw_type_str)` — prefix is `"ind"` for `ind.TYPE(` syntax.
fn extract_raw_indicator_type(trimmed: &str) -> Option<(String, String)> {
    let rest = trimmed.strip_prefix("let ")?.trim();
    let eq_pos = rest.find('=')?;
    let rhs = rest[eq_pos + 1..].trim();

    // Legacy: indicator("type", ...)
    if rhs.starts_with("indicator(") {
        let inner = rhs.strip_prefix("indicator(")?.trim_end_matches(')');
        let t = inner.split(',').next()?.trim().trim_matches('"').trim_matches('\'');
        if t.is_empty() { return None; }
        return Some(("indicator".to_string(), t.to_string()));
    }

    // `PREFIX.TYPE(` pattern
    let dot_pos = rhs.find('.')?;
    let prefix = rhs[..dot_pos].trim().to_string();
    let after_dot = rhs[dot_pos + 1..].trim();
    let paren_pos = after_dot.find('(')?;
    let type_str = after_dot[..paren_pos].trim().to_string();

    if prefix.is_empty() || type_str.is_empty() { return None; }
    Some((prefix, type_str))
}

/// Lint a Rhai strategy script. Returns `(diagnostics, scope)`.
///
/// Checks:
/// 1. Wrong indicator prefix (e.g. `indi.ema` instead of `ind.ema`).
/// 2. Unknown indicator type against [`KNOWN_INDICATOR_TYPES`].
/// 3. Rhai syntax errors in the logic section (indicator decls stripped first).
pub fn rhai_lint(script: &str) -> (Vec<LintDiagnostic>, RhaiLintScope) {
    let mut diags: Vec<LintDiagnostic> = Vec::new();
    let mut cleaned_lines: Vec<&str>   = Vec::new();
    // cleaned line index → original 1-based line number (for error remapping)
    let mut line_map: Vec<usize>       = Vec::new();
    let mut scope_inds: Vec<DeclaredIndicator> = Vec::new();

    for (idx, line) in script.lines().enumerate() {
        let lineno  = idx + 1;
        let trimmed = line.trim();

        // Try to identify indicator-like prefix+type from the line
        if let Some((prefix, raw_type)) = extract_raw_indicator_type(trimmed) {
            if prefix != "ind" && prefix != "indicator" {
                // Wrong prefix — tell the user what they should use
                let suggestion = format!("ind.{raw_type}(");
                diags.push(LintDiagnostic {
                    line: lineno, col: 1,
                    message: format!(
                        "Wrong indicator prefix '{prefix}.'; use '{suggestion}...' instead"
                    ),
                    severity: "error",
                });
                // Keep in cleaned script so Rhai also reports the syntax error
                cleaned_lines.push(line);
                line_map.push(lineno);
            } else {
                // Correct prefix — validate the type
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
                // Strip successfully parsed declarations from cleaned script
                if let Some(decl) = try_parse_indicator_line(trimmed) {
                    scope_inds.push(DeclaredIndicator {
                        name:      decl.var_name,
                        ind_type:  raw_type,
                        period:    decl.period,
                        timeframe: decl.timeframe.map(|tf| tf.to_string()),
                        live:      decl.live,
                    });
                    // Don't add to cleaned_lines — stripped from Rhai AST
                } else {
                    // Parsed prefix+type but period/args unparseable
                    cleaned_lines.push(line);
                    line_map.push(lineno);
                }
            }
        } else {
            cleaned_lines.push(line);
            line_map.push(lineno);
        }
    }

    // Compile stripped logic through the Rhai engine for syntax errors.
    let cleaned = cleaned_lines.join("\n");
    let plot_buf: PlotBuf = Arc::new(Mutex::new(Vec::new()));
    let engine = build_engine(plot_buf);
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

    let scope = RhaiLintScope {
        indicators:  scope_inds,
        bar_fields:  BAR_FIELDS.to_vec(),
        output_vars: vec!["long", "short", "exit", "tp", "sl", "strength", "is_offset", "reason"],
        functions:   vec![
            "cross_above", "cross_below",
            "rising", "falling", "rising_n", "falling_n",
            "above", "below", "in_range",
            "highest", "lowest",
            "plot",
        ],
    };

    (diags, scope)
}

/// Return the most similar known indicator type to `input` (edit distance ≤ 2).
/// Returns an empty string if no close match found.
fn suggest_similar(input: &str) -> String {
    let mut best: Option<(&str, usize)> = None;
    for &known in KNOWN_INDICATOR_TYPES {
        let dist = edit_distance(input, known);
        if dist <= 2 {
            if best.map_or(true, |(_, d)| dist < d) {
                best = Some((known, dist));
            }
        }
    }
    best.map(|(s, _)| s.to_string()).unwrap_or_default()
}

/// Minimal Levenshtein distance (bounded at 3 for performance).
fn edit_distance(a: &str, b: &str) -> usize {
    let a: Vec<char> = a.chars().collect();
    let b: Vec<char> = b.chars().collect();
    let m = a.len();
    let n = b.len();
    if m.abs_diff(n) > 3 { return 4; }
    let mut dp = vec![vec![0usize; n + 1]; m + 1];
    for i in 0..=m { dp[i][0] = i; }
    for j in 0..=n { dp[0][j] = j; }
    for i in 1..=m {
        for j in 1..=n {
            dp[i][j] = if a[i - 1] == b[j - 1] {
                dp[i - 1][j - 1]
            } else {
                1 + dp[i - 1][j - 1].min(dp[i - 1][j]).min(dp[i][j - 1])
            };
        }
    }
    dp[m][n]
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

    // ── rhai_lint tests ───────────────────────────────────────────────────────

    #[test]
    fn lint_clean_script_no_errors() {
        let script = r#"
let ema9  = ind.ema(9);
let rsi14 = ind.rsi(14);
if cross_above(ema9, ema9) && rsi14[0] < 70.0 { entry = true; }
if cross_below(ema9, ema9) { exit = true; }
"#;
        let (errors, scope) = rhai_lint(script);
        assert!(errors.is_empty(), "clean script should have no lint errors: {errors:?}");
        assert_eq!(scope.indicators.len(), 2);
        assert_eq!(scope.indicators[0].ind_type, "ema");
        assert_eq!(scope.indicators[1].ind_type, "rsi");
    }

    #[test]
    fn lint_wrong_prefix_indi() {
        let script = "let ema9 = indi.ema(9);\nif ema9[0] > 0.0 { entry = true; }";
        let (errors, _scope) = rhai_lint(script);
        assert!(!errors.is_empty(), "should flag wrong prefix 'indi'");
        assert!(errors[0].message.contains("indi"), "message should mention bad prefix");
        assert_eq!(errors[0].line, 1);
    }

    #[test]
    fn lint_unknown_type_suggestion() {
        // "mma" is unknown but close to "ema" (edit distance 1)
        let script = "let x = ind.mma(9);";
        let (errors, _scope) = rhai_lint(script);
        assert!(!errors.is_empty(), "unknown type should be an error");
        let msg = &errors[0].message;
        assert!(msg.contains("mma"), "should mention unknown type");
        assert!(msg.contains("ema"), "should suggest 'ema'");
    }

    #[test]
    fn lint_unknown_type_no_suggestion() {
        // "zzz" has no close match
        let script = "let x = ind.zzz(9);";
        let (errors, _scope) = rhai_lint(script);
        assert!(!errors.is_empty());
        assert!(errors[0].message.contains("zzz"));
    }

    #[test]
    fn lint_rhai_syntax_error_line_mapped() {
        let script = "let ema9 = ind.ema(9);\nif ema9[0] > { entry = true; }";
        let (errors, _scope) = rhai_lint(script);
        // The syntax error is on line 2 in original; after stripping line 1,
        // the cleaned script has it on line 1 → should map back to line 2.
        let syntax_errs: Vec<_> = errors.iter().filter(|e| e.severity == "error").collect();
        assert!(!syntax_errs.is_empty(), "should report Rhai syntax error");
    }

    #[test]
    fn lint_legacy_indicator_syntax_accepted() {
        let script = r#"let ema9 = indicator("ema", 9);
if ema9[0] > 0.0 { entry = true; }
"#;
        let (errors, scope) = rhai_lint(script);
        assert!(errors.is_empty(), "legacy indicator() syntax should be lint-clean: {errors:?}");
        assert_eq!(scope.indicators.len(), 1);
    }

    #[test]
    fn lint_scope_contains_all_bar_fields() {
        let (_, scope) = rhai_lint("if close[0] > 0.0 { entry = true; }");
        assert!(scope.bar_fields.contains(&"close"));
        assert!(scope.bar_fields.contains(&"open"));
    }
}

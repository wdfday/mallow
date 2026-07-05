//! `ta.*` — incremental (stateful) primitives for custom indicator composition
//! inside Rhai scripts.
//!
//! Distinct from the `ind.*` catalog (`crate::script::v1::parse`): `ind.*`
//! declarations are text-extracted before Rhai ever sees them and computed by
//! the fixed Rust catalog (`alm_indicator`). `ta.*` calls are genuine Rhai
//! function dispatch — they run wherever the script writes them (inside
//! branches, expressions, anywhere), so users can build formulas the catalog
//! doesn't have.
//!
//! # Syntax
//!
//! ```text
//! let fast = ta.ema(9, close[0]);
//! let slow = ta.smma(14, close[0] - close[1]);
//! let wide = ta.ema(9, close[0], buf=5);   // 5-deep output history instead of the default 2
//! ```
//!
//! `let NAME = ta.FUNC(args [, buf=N]);` is rewritten at build time (see
//! [`super::v1::parse::rewrite_ta_line`]) to `let NAME = ta.FUNC("NAME", args, N);`
//! — the variable name becomes the state key automatically (never typed by
//! the user), and a trailing `buf=N` (same static-parsing convention as
//! `ind.*`'s `buf=N`) becomes an explicit output-history-depth argument,
//! defaulting to 2 when omitted. Declaration is mandatory: any `ta.*` call
//! that isn't a top-level `let NAME = ta.FUNC(...)` (inline in an
//! expression, a bare statement, a reassignment) fails to *build*, not at
//! runtime — see `validate_ta_declarations`. A malformed `buf=` (non-numeric,
//! or `0`) is also a build-time error, never a silent fallback — see
//! `MIN_TA_BUF` in `rewrite_ta_line`. State lives in a dedicated `rhai::Map`
//! (`ta_state` field on `ScriptStrategy` / `MtfScriptStrategy`, pushed into
//! scope as `ta` each bar and read back after eval — mirrors the existing
//! `persistent_state`/`state` mechanism exactly).
//!
//! # Scope
//!
//! Every function here takes its input as an arbitrary `value` (and, for
//! `vwma`, a second `weight`) rather than being fixed to `close` — this is
//! the actual reason `ta.*` exists alongside `ind.*`. `ind.ema(9)` is wired
//! internally to `bar.close` (`v1/binding.rs`, `IndicatorBox::update` decides
//! the field per type; the user can't change it), so it can't smooth
//! `volume`, `high - low`, or any derived value. `ta.ema(9, volume[0])` can.
//! Only the MA family (whose formula is the same regardless of what series
//! you feed it) belongs here — oscillators/bands built from a *fixed* OHLCV
//! combination (RSI, ADX, Bollinger) stay in `ind.*` and are composed from
//! these primitives when a custom variant is needed (see `USER_GUIDE.md`
//! composition examples once written).
//!
//! # Output shape
//!
//! Every function returns an `Array` (newest at `[0]`, depth from `buf=N` or
//! 2 by default — same convention as `ind.*` bindings), **not** a bare
//! scalar, so the built-in direction helpers *and* lookback helpers work
//! directly on the result:
//!
//! ```text
//! let fast = ta.ema(9, close[0]);
//! let slow = ta.ema(21, close[0]);
//! if cross_above(fast, slow) { entry = true; }   // fast/slow are Array<f64>
//! if rising(fast) { ... }
//!
//! let wide = ta.ema(9, close[0], buf=10);
//! if wide[0] > highest(wide, 5) - 0.001 { ... }  // needs buf >= 5 to be meaningful
//! ```
//!
//! # Functions
//!
//! | Function | Formula |
//! |---|---|
//! | `ta.ema(period, value)`    | standard EMA, alpha = 2/(period+1) |
//! | `ta.smma(period, value)`   | smoothed MA / Wilder smoothing, alpha = 1/period — same name as `ind.smma`; RSI/ATR/ADX building block |
//! | `ta.decay(alpha, value)`   | exponential decay with user-supplied alpha (escape hatch for adaptive MAs like KAMA) |
//! | `ta.sma(period, value)`    | rolling mean; `()` (unit) until warmed |
//! | `ta.rsum(period, value)`   | rolling sum (no division) — building block for custom oscillators |
//! | `ta.stdev(period, value)` | rolling population stdev; `()` until warmed |
//! | `ta.highest(period, value)` / `ta.lowest(period, value)` | rolling max/min over an arbitrary computed value (not just a declared series) |
//! | `ta.wma(period, value)`    | weighted MA (linear weights, newest heaviest); `()` until warmed |
//! | `ta.hma(period, value)`    | Hull MA — WMA(2·WMA(n/2) − WMA(n), √n); `()` until warmed |
//! | `ta.vwma(period, value, weight)` | MA of `value` weighted by an arbitrary `weight` series (volume is the common case, but any series works); `()` until warmed |
//! | `ta.reset(key)`            | manually clear one slot (explicit key — not variable-name-derived) |
//!
//! All rolling-sum-based primitives (`sma`, `rsum`, `stdev`, `wma`, `hma`,
//! `vwma`) use [`KahanSum`] internally to avoid the float drift a naive
//! `sum += x; sum -= old` accumulates over long series (see
//! `alm-indicator`'s `Sma` for the bug this avoids).

use rhai::{Dynamic, Engine, Map};
use std::collections::VecDeque;

// ── Kahan-compensated running sum ───────────────────────────────────────────

/// Compensated summation — keeps float error bounded across many incremental
/// add/sub operations. A naive `sum += x; sum -= old` rolling sum drifts
/// because each subtraction can lose low-order bits permanently; Kahan's
/// running compensation term recovers them.
#[derive(Clone, Debug, Default)]
struct KahanSum {
    sum: f64,
    c: f64,
}

impl KahanSum {
    fn add(&mut self, x: f64) {
        let y = x - self.c;
        let t = self.sum + y;
        self.c = (t - self.sum) - y;
        self.sum = t;
    }

    fn sub(&mut self, x: f64) {
        self.add(-x);
    }

    fn value(&self) -> f64 {
        self.sum
    }
}

// ── Output history ring (newest at [0]) ─────────────────────────────────────

/// Push the latest computed value (a real float or `Dynamic::UNIT` while
/// still warming up) into `hist` and return it as a Rhai `Array`, newest at
/// `[0]` — matches the `ind.*` binding convention so direction helpers work
/// directly on `ta.*` outputs. `depth` comes from the script's `buf=N`
/// (parsed statically at build time by `rewrite_ta_line`, default 2) — it's
/// the same literal every call for a given key, so the ring naturally
/// settles at that size without needing to be stored on the state struct.
fn ring_push(hist: &mut VecDeque<Dynamic>, v: Dynamic, depth: usize) -> rhai::Array {
    hist.push_front(v);
    hist.truncate(depth.max(1));
    hist.iter().cloned().collect()
}

/// Degraded fallback for the case where a key is reused across two
/// different `ta.*` functions and `write_lock` finds a type mismatch —
/// still returns an indexable array (sized to `depth`) instead of panicking
/// or silently returning garbage. This is now actually unreachable in
/// practice: `ScriptStrategy`/`MtfScriptStrategy::build` share one
/// `decl_names` set across `ind.*` *and* `ta.*` declarations (see
/// `process_script_block` in each), so reusing a name for two different
/// `ta.*` functions is a build-time error, not a runtime key collision.
/// Kept as defense in depth (e.g. against a future bug in that uniqueness
/// check) rather than relied upon.
fn fallback_array(value: f64, depth: usize) -> Dynamic {
    Dynamic::from_array(vec![Dynamic::from_float(value); depth.max(1)])
}

// ── ta.ema ───────────────────────────────────────────────────────────────────

#[derive(Clone, Debug)]
struct TaEma {
    val: f64,
    alpha: f64,
    seeded: bool,
    hist: VecDeque<Dynamic>,
}

impl TaEma {
    fn new(period: i64) -> Self {
        Self { val: 0.0, alpha: 2.0 / (period.max(1) as f64 + 1.0), seeded: false, hist: VecDeque::new() }
    }

    fn update(&mut self, value: f64) -> f64 {
        self.val = if self.seeded { value * self.alpha + self.val * (1.0 - self.alpha) } else { value };
        self.seeded = true;
        self.val
    }
}

// ── ta.smma — smoothed MA / Wilder's smoothing (alpha = 1/period); RSI/ATR/ADX use this ─

#[derive(Clone, Debug)]
struct TaSmma {
    val: f64,
    alpha: f64,
    seeded: bool,
    hist: VecDeque<Dynamic>,
}

impl TaSmma {
    fn new(period: i64) -> Self {
        Self { val: 0.0, alpha: 1.0 / period.max(1) as f64, seeded: false, hist: VecDeque::new() }
    }

    fn update(&mut self, value: f64) -> f64 {
        self.val = if self.seeded { value * self.alpha + self.val * (1.0 - self.alpha) } else { value };
        self.seeded = true;
        self.val
    }
}

// ── ta.decay — user-supplied smoothing coefficient ──────────────────────────

#[derive(Clone, Debug, Default)]
struct TaDecay {
    val: f64,
    seeded: bool,
    hist: VecDeque<Dynamic>,
}

impl TaDecay {
    fn update(&mut self, alpha: f64, value: f64) -> f64 {
        self.val = if self.seeded { value * alpha + self.val * (1.0 - alpha) } else { value };
        self.seeded = true;
        self.val
    }
}

// ── ta.sma / ta.rsum — rolling window sum ───────────────────────────────────

#[derive(Clone, Debug)]
struct TaWindowSum {
    period: usize,
    buffer: VecDeque<f64>,
    sum: KahanSum,
    hist: VecDeque<Dynamic>,
}

impl TaWindowSum {
    fn new(period: i64) -> Self {
        let p = period.max(1) as usize;
        Self { period: p, buffer: VecDeque::with_capacity(p), sum: KahanSum::default(), hist: VecDeque::new() }
    }

    fn push(&mut self, value: f64) -> f64 {
        self.buffer.push_back(value);
        self.sum.add(value);
        if self.buffer.len() > self.period {
            self.sum.sub(self.buffer.pop_front().unwrap());
        }
        self.sum.value()
    }

    fn is_ready(&self) -> bool {
        self.buffer.len() == self.period
    }
}

// ── ta.stdev — rolling population stdev ─────────────────────────────────────

#[derive(Clone, Debug)]
struct TaStdev {
    period: usize,
    buffer: VecDeque<f64>,
    sum: KahanSum,
    sum_sq: KahanSum,
    hist: VecDeque<Dynamic>,
}

impl TaStdev {
    fn new(period: i64) -> Self {
        let p = period.max(1) as usize;
        Self {
            period: p, buffer: VecDeque::with_capacity(p),
            sum: KahanSum::default(), sum_sq: KahanSum::default(),
            hist: VecDeque::new(),
        }
    }

    fn push(&mut self, value: f64) -> Option<f64> {
        self.buffer.push_back(value);
        self.sum.add(value);
        self.sum_sq.add(value * value);
        if self.buffer.len() > self.period {
            let old = self.buffer.pop_front().unwrap();
            self.sum.sub(old);
            self.sum_sq.sub(old * old);
        }
        if self.buffer.len() == self.period {
            let n = self.period as f64;
            let mean = self.sum.value() / n;
            let var = (self.sum_sq.value() / n - mean * mean).max(0.0);
            Some(var.sqrt())
        } else {
            None
        }
    }
}

// ── ta.highest / ta.lowest — thin wrappers over the tested,
// already-monotonic-deque `alm_indicator::{RollingMax, RollingMin}` ─────────
//
// `Donchian` already uses these for the identical highest-high/lowest-low
// job — no reason to re-derive the eviction logic here. Both gate on the
// window being full (`None` until `period` samples), same as `sma`/`stdev`/
// `wma`/`hma`/`vwma` below, for consistency.

#[derive(Clone, Debug)]
struct TaHighest {
    inner: alm_indicator::RollingMax,
    hist: VecDeque<Dynamic>,
}

impl TaHighest {
    fn new(period: i64) -> Self {
        Self { inner: alm_indicator::RollingMax::new(period.max(1) as usize), hist: VecDeque::new() }
    }

    fn push(&mut self, value: f64) -> Option<f64> {
        self.inner.push(value)
    }
}

#[derive(Clone, Debug)]
struct TaLowest {
    inner: alm_indicator::RollingMin,
    hist: VecDeque<Dynamic>,
}

impl TaLowest {
    fn new(period: i64) -> Self {
        Self { inner: alm_indicator::RollingMin::new(period.max(1) as usize), hist: VecDeque::new() }
    }

    fn push(&mut self, value: f64) -> Option<f64> {
        self.inner.push(value)
    }
}

// ── ta.wma — thin wrapper over the tested `alm_indicator::Wma` ─────────────

// `alm_indicator::Wma` only derives `Clone`, not `Debug`.
#[derive(Clone)]
struct TaWma {
    inner: alm_indicator::Wma,
    hist: VecDeque<Dynamic>,
}

impl TaWma {
    fn new(period: i64) -> Self {
        Self { inner: alm_indicator::Wma::new(period.max(1) as usize), hist: VecDeque::new() }
    }

    fn push(&mut self, value: f64) -> Option<f64> {
        self.inner.update(value)
    }
}

// ── ta.hma — thin wrapper over the tested `alm_indicator::Hma` ─────────────
// (which already composes WMA(2·WMA(n/2) − WMA(n), √n) internally).

// `alm_indicator::Hma` only derives `Clone`, not `Debug`.
#[derive(Clone)]
struct TaHma {
    inner: alm_indicator::Hma,
    hist: VecDeque<Dynamic>,
}

impl TaHma {
    fn new(period: i64) -> Self {
        // `Hma::new` asserts `period >= 4` — script-authored periods aren't
        // statically checked (unlike `buf=N`, `period` may be an arbitrary
        // Rhai expression), so clamp defensively rather than let a bad
        // script value panic the whole process.
        Self { inner: alm_indicator::Hma::new(period.max(4) as usize), hist: VecDeque::new() }
    }

    fn push(&mut self, value: f64) -> Option<f64> {
        self.inner.update(value)
    }
}

// ── ta.vwma — thin wrapper over the tested `alm_indicator::Vwma` ───────────
// (falls back to a plain mean of `value` when the weight sum is ~0, rather
// than this module's earlier ad hoc `0.0` fallback).

#[derive(Clone, Debug)]
struct TaVwma {
    inner: alm_indicator::Vwma,
    hist: VecDeque<Dynamic>,
}

impl TaVwma {
    fn new(period: i64) -> Self {
        Self { inner: alm_indicator::Vwma::new(period.max(1) as usize), hist: VecDeque::new() }
    }

    fn push(&mut self, value: f64, weight: f64) -> Option<f64> {
        self.inner.update(value, weight)
    }
}

// ── Rhai registration — every fn returns Array<f64|()>, newest at [0] ───────
//
// The trailing `buf: i64` on every function is injected by `rewrite_ta_line`
// (static analysis, same convention as `ind.*`'s `buf=N` — defaults to
// `DEFAULT_TA_BUF` when the script doesn't specify one) and sizes the output
// history ring for that key.

fn ta_ema(state: &mut Map, key: &str, period: i64, value: f64, buf: i64) -> Dynamic {
    let slot = state.entry(key.into()).or_insert_with(|| Dynamic::from(TaEma::new(period)));
    let Some(mut e) = slot.write_lock::<TaEma>() else { return fallback_array(value, buf as usize) };
    let v = e.update(value);
    Dynamic::from_array(ring_push(&mut e.hist, Dynamic::from_float(v), buf as usize))
}

fn ta_smma(state: &mut Map, key: &str, period: i64, value: f64, buf: i64) -> Dynamic {
    let slot = state.entry(key.into()).or_insert_with(|| Dynamic::from(TaSmma::new(period)));
    let Some(mut e) = slot.write_lock::<TaSmma>() else { return fallback_array(value, buf as usize) };
    let v = e.update(value);
    Dynamic::from_array(ring_push(&mut e.hist, Dynamic::from_float(v), buf as usize))
}

fn ta_decay(state: &mut Map, key: &str, alpha: f64, value: f64, buf: i64) -> Dynamic {
    let slot = state.entry(key.into()).or_insert_with(|| Dynamic::from(TaDecay::default()));
    let Some(mut d) = slot.write_lock::<TaDecay>() else { return fallback_array(value, buf as usize) };
    let v = d.update(alpha, value);
    Dynamic::from_array(ring_push(&mut d.hist, Dynamic::from_float(v), buf as usize))
}

fn ta_sma(state: &mut Map, key: &str, period: i64, value: f64, buf: i64) -> Dynamic {
    let slot = state.entry(key.into()).or_insert_with(|| Dynamic::from(TaWindowSum::new(period)));
    let Some(mut w) = slot.write_lock::<TaWindowSum>() else { return fallback_array(value, buf as usize) };
    let sum = w.push(value);
    let out = if w.is_ready() { Dynamic::from_float(sum / w.period as f64) } else { Dynamic::UNIT };
    Dynamic::from_array(ring_push(&mut w.hist, out, buf as usize))
}

fn ta_rsum(state: &mut Map, key: &str, period: i64, value: f64, buf: i64) -> Dynamic {
    let slot = state.entry(key.into()).or_insert_with(|| Dynamic::from(TaWindowSum::new(period)));
    let Some(mut w) = slot.write_lock::<TaWindowSum>() else { return fallback_array(value, buf as usize) };
    let sum = w.push(value);
    Dynamic::from_array(ring_push(&mut w.hist, Dynamic::from_float(sum), buf as usize))
}

fn ta_stdev(state: &mut Map, key: &str, period: i64, value: f64, buf: i64) -> Dynamic {
    let slot = state.entry(key.into()).or_insert_with(|| Dynamic::from(TaStdev::new(period)));
    let Some(mut w) = slot.write_lock::<TaStdev>() else { return fallback_array(value, buf as usize) };
    let out = match w.push(value) {
        Some(v) => Dynamic::from_float(v),
        None => Dynamic::UNIT,
    };
    Dynamic::from_array(ring_push(&mut w.hist, out, buf as usize))
}

fn ta_highest(state: &mut Map, key: &str, period: i64, value: f64, buf: i64) -> Dynamic {
    let slot = state.entry(key.into()).or_insert_with(|| Dynamic::from(TaHighest::new(period)));
    let Some(mut w) = slot.write_lock::<TaHighest>() else { return fallback_array(value, buf as usize) };
    let out = match w.push(value) {
        Some(v) => Dynamic::from_float(v),
        None => Dynamic::UNIT,
    };
    Dynamic::from_array(ring_push(&mut w.hist, out, buf as usize))
}

fn ta_lowest(state: &mut Map, key: &str, period: i64, value: f64, buf: i64) -> Dynamic {
    let slot = state.entry(key.into()).or_insert_with(|| Dynamic::from(TaLowest::new(period)));
    let Some(mut w) = slot.write_lock::<TaLowest>() else { return fallback_array(value, buf as usize) };
    let out = match w.push(value) {
        Some(v) => Dynamic::from_float(v),
        None => Dynamic::UNIT,
    };
    Dynamic::from_array(ring_push(&mut w.hist, out, buf as usize))
}

fn ta_wma(state: &mut Map, key: &str, period: i64, value: f64, buf: i64) -> Dynamic {
    let slot = state.entry(key.into()).or_insert_with(|| Dynamic::from(TaWma::new(period)));
    let Some(mut w) = slot.write_lock::<TaWma>() else { return fallback_array(value, buf as usize) };
    let out = match w.push(value) {
        Some(v) => Dynamic::from_float(v),
        None => Dynamic::UNIT,
    };
    Dynamic::from_array(ring_push(&mut w.hist, out, buf as usize))
}

fn ta_hma(state: &mut Map, key: &str, period: i64, value: f64, buf: i64) -> Dynamic {
    let slot = state.entry(key.into()).or_insert_with(|| Dynamic::from(TaHma::new(period)));
    let Some(mut w) = slot.write_lock::<TaHma>() else { return fallback_array(value, buf as usize) };
    let out = match w.push(value) {
        Some(v) => Dynamic::from_float(v),
        None => Dynamic::UNIT,
    };
    Dynamic::from_array(ring_push(&mut w.hist, out, buf as usize))
}

fn ta_vwma(state: &mut Map, key: &str, period: i64, value: f64, weight: f64, buf: i64) -> Dynamic {
    let slot = state.entry(key.into()).or_insert_with(|| Dynamic::from(TaVwma::new(period)));
    let Some(mut w) = slot.write_lock::<TaVwma>() else { return fallback_array(value, buf as usize) };
    let out = match w.push(value, weight) {
        Some(v) => Dynamic::from_float(v),
        None => Dynamic::UNIT,
    };
    Dynamic::from_array(ring_push(&mut w.hist, out, buf as usize))
}

fn ta_reset(state: &mut Map, key: &str) {
    state.remove(key);
}

pub(crate) fn register_ta(engine: &mut Engine) {
    engine.register_type::<TaEma>();
    engine.register_type::<TaSmma>();
    engine.register_type::<TaDecay>();
    engine.register_type::<TaWindowSum>();
    engine.register_type::<TaStdev>();
    engine.register_type::<TaHighest>();
    engine.register_type::<TaLowest>();
    engine.register_type::<TaWma>();
    engine.register_type::<TaHma>();
    engine.register_type::<TaVwma>();

    engine.register_fn("ema", ta_ema);
    engine.register_fn("smma", ta_smma);
    engine.register_fn("decay", ta_decay);
    engine.register_fn("sma", ta_sma);
    engine.register_fn("rsum", ta_rsum);
    engine.register_fn("stdev", ta_stdev);
    engine.register_fn("highest", ta_highest);
    engine.register_fn("lowest", ta_lowest);
    engine.register_fn("wma", ta_wma);
    engine.register_fn("hma", ta_hma);
    engine.register_fn("vwma", ta_vwma);
    engine.register_fn("reset", ta_reset);
}

#[cfg(test)]
mod tests {
    use super::*;
    use rhai::Scope;

    fn engine_with_ta() -> Engine {
        let mut engine = Engine::new();
        register_ta(&mut engine);
        engine
    }

    #[test]
    fn ta_ema_matches_reference() {
        let prices = [100.0, 105.0, 110.0, 115.0, 112.0, 108.0, 114.0];
        let period = 10;

        let mut rust_ema = TaEma::new(period);
        let mut rust_results = Vec::new();
        for &p in &prices {
            rust_results.push(rust_ema.update(p));
        }

        let engine = engine_with_ta();
        let mut scope = Scope::new();
        scope.push("ta", Map::new());

        let mut rhai_results = Vec::new();
        for &p in &prices {
            let expr = format!(r#"ta.ema("fast", {period}, {p:.4}, 2)[0]"#);
            rhai_results.push(engine.eval_with_scope::<f64>(&mut scope, &expr).unwrap());
        }

        for (r, rh) in rust_results.iter().zip(rhai_results.iter()) {
            assert!((r - rh).abs() < 1e-9);
        }
    }

    #[test]
    fn ta_sma_matches_reference_and_gates_until_warm() {
        let prices = [1.0, 2.0, 3.0, 4.0, 5.0];
        let period = 3;

        let mut rust_sma = TaWindowSum::new(period);
        let engine = engine_with_ta();
        let mut scope = Scope::new();
        scope.push("ta", Map::new());

        for &p in &prices {
            let sum = rust_sma.push(p);
            let expr = format!(r#"ta.sma("s", {period}, {p:.4}, 2)"#);
            let res: rhai::Array = engine.eval_with_scope(&mut scope, &expr).unwrap();
            if rust_sma.is_ready() {
                assert!((res[0].as_float().unwrap() - sum / period as f64).abs() < 1e-9);
            } else {
                assert!(res[0].is_unit());
            }
        }
    }

    #[test]
    fn ta_variable_name_becomes_key() {
        // Simulates the rewrite that ScriptStrategy::build performs:
        // `let fast = ta.ema(9, close);` -> `let fast = ta.ema("fast", 9, close, 2);`
        let engine = engine_with_ta();
        let mut scope = Scope::new();
        scope.push("ta", Map::new());
        scope.push("close", 100.0_f64);

        let script = r#"let fast = ta.ema("fast", 9, close, 2); fast[0]"#;
        let res: f64 = engine.eval_with_scope(&mut scope, script).unwrap();
        assert_eq!(res, 100.0);
    }

    /// `buf` (5th positional arg, injected from `buf=N` by `rewrite_ta_line`)
    /// sizes the output history ring — grows to `buf` entries and then holds
    /// there, newest at `[0]`, oldest evicted at the back.
    #[test]
    fn ta_buf_controls_output_history_depth() {
        let engine = engine_with_ta();
        let mut scope = Scope::new();
        scope.push("ta", Map::new());

        for i in 0..8 {
            let expr = format!(r#"ta.ema("fast", 9, {:.4}, 5)"#, 100.0 + i as f64);
            let res: rhai::Array = engine.eval_with_scope(&mut scope, &expr).unwrap();
            assert_eq!(res.len(), (i + 1).min(5), "call {i}: expected len {}, got {}", (i + 1).min(5), res.len());
        }
    }

    #[test]
    fn ta_state_persists_across_scope_rebuilds() {
        // ScriptStrategy rebuilds Scope fresh every bar, cloning `ta_state` in
        // and reading it back out — reproduce that cycle here.
        let engine = engine_with_ta();
        let mut ta_state = Map::new();

        let mut last = 0.0;
        for p in [100.0, 110.0, 120.0] {
            let mut scope = Scope::new();
            scope.push("ta", ta_state.clone());
            scope.push("close", p);
            last = engine
                .eval_with_scope::<f64>(&mut scope, r#"ta.ema("fast", 10, close, 2)[0]"#)
                .unwrap();
            ta_state = scope.get_value::<Map>("ta").unwrap();
        }

        let mut reference = TaEma::new(10);
        let mut r = 0.0;
        for p in [100.0, 110.0, 120.0] {
            r = reference.update(p);
        }
        assert!((last - r).abs() < 1e-9);
    }

    /// Brute-force weighted mean over the trailing `period` window (`window`
    /// is chronological, oldest first — newest, at the end, gets weight
    /// `period`), recomputed from scratch every call — the reference
    /// `TaWma`'s O(1) recurrence is checked against.
    fn bruteforce_wma(window: &[f64]) -> f64 {
        let n = window.len() as f64;
        let num: f64 = window.iter().enumerate().map(|(i, &v)| (i + 1) as f64 * v).sum();
        num / (n * (n + 1.0) / 2.0)
    }

    #[test]
    fn ta_wma_matches_bruteforce() {
        let prices: Vec<f64> = (0..40).map(|i| 100.0 + (i as f64 * 0.37).sin() * 5.0 + i as f64 * 0.2).collect();
        let period: usize = 6;
        let mut wma = TaWma::new(period as i64);

        for i in 0..prices.len() {
            let got = wma.push(prices[i]);
            if i + 1 >= period {
                let window = &prices[i + 1 - period..=i];
                let expected = bruteforce_wma(window);
                assert!((got.unwrap() - expected).abs() < 1e-9, "index {i}: expected {expected}, got {got:?}");
            } else {
                assert!(got.is_none());
            }
        }
    }

    #[test]
    fn ta_hma_matches_bruteforce() {
        let prices: Vec<f64> = (0..60).map(|i| 100.0 + (i as f64 * 0.29).cos() * 4.0 + i as f64 * 0.15).collect();
        let period: usize = 9;
        // Matches `alm_indicator::Hma::new`'s own half-period convention
        // (integer division, not round-half-away-from-zero) — `TaHma` now
        // delegates to that struct directly.
        let half_period = (period / 2).max(1);
        let sqrt_period = (period as f64).sqrt().round() as usize;

        let mut hma = TaHma::new(period as i64);
        let mut raw_series: Vec<f64> = Vec::new();

        for i in 0..prices.len() {
            let got = hma.push(prices[i]);
            let have_half = i + 1 >= half_period;
            let have_full = i + 1 >= period;
            if have_half && have_full {
                let half = bruteforce_wma(&prices[i + 1 - half_period..=i]);
                let full = bruteforce_wma(&prices[i + 1 - period..=i]);
                raw_series.push(2.0 * half - full);
            }
            if raw_series.len() >= sqrt_period {
                let window = &raw_series[raw_series.len() - sqrt_period..];
                let expected = bruteforce_wma(window);
                assert!((got.unwrap() - expected).abs() < 1e-9, "index {i}: expected {expected}, got {got:?}");
            } else {
                assert!(got.is_none(), "index {i}: expected None, got {got:?}");
            }
        }
    }

    #[test]
    fn ta_vwma_matches_bruteforce() {
        let values: Vec<f64> = (0..30).map(|i| 100.0 + i as f64 * 0.3).collect();
        let weights: Vec<f64> = (0..30).map(|i| 1000.0 + (i % 7) as f64 * 50.0).collect();
        let period: usize = 5;
        let mut vwma = TaVwma::new(period as i64);

        for i in 0..values.len() {
            let got = vwma.push(values[i], weights[i]);
            if i + 1 >= period {
                let vs = &values[i + 1 - period..=i];
                let ws = &weights[i + 1 - period..=i];
                let num: f64 = vs.iter().zip(ws).map(|(v, w)| v * w).sum();
                let den: f64 = ws.iter().sum();
                let expected = num / den;
                assert!((got.unwrap() - expected).abs() < 1e-9, "index {i}: expected {expected}, got {got:?}");
            } else {
                assert!(got.is_none());
            }
        }
    }

    #[test]
    fn ta_hma_and_vwma_callable_from_rhai() {
        let engine = engine_with_ta();
        let mut scope = Scope::new();
        scope.push("ta", Map::new());

        for i in 0..20 {
            let price = 100.0 + i as f64;
            let volume = 1000.0 + i as f64 * 10.0;
            let expr = format!(r#"[ta.hma("h", 9, {price:.4}, 2)[0], ta.vwma("v", 9, {price:.4}, {volume:.4}, 2)[0]]"#);
            let res: rhai::Array = engine.eval_with_scope(&mut scope, &expr).unwrap();
            // vwma(9) warms after 9 bars; hma(9) needs period(9) + sqrt(9) - 1 = 11.
            if i >= 8 {
                assert!(res[1].as_float().is_ok());
            }
            if i >= 10 {
                assert!(res[0].as_float().is_ok());
            }
        }
    }

    #[test]
    fn ta_real_data_indicators_comparison() {
        let bars = match crate::test_utils::load_real_bars() {
            Some(b) => b,
            None => {
                println!("testdata not found, skipping real data comparison");
                return;
            }
        };
        let prices: Vec<f64> = bars.iter().map(|b| b.close).take(2000).collect();
        let period = 20;

        let mut rust_ema = TaEma::new(period);
        let mut rust_sma = TaWindowSum::new(period);
        let mut rust_ema_res = Vec::new();
        let mut rust_sma_res = Vec::new();
        for &p in &prices {
            rust_ema_res.push(rust_ema.update(p));
            let sum = rust_sma.push(p);
            rust_sma_res.push(if rust_sma.is_ready() { Some(sum / period as f64) } else { None });
        }

        let engine = engine_with_ta();
        let mut scope = Scope::new();
        scope.push("ta", Map::new());

        for (i, &p) in prices.iter().enumerate() {
            let expr = format!(r#"[ta.ema("e", {period}, {p:.4}, 2)[0], ta.sma("s", {period}, {p:.4}, 2)[0]]"#);
            let res: rhai::Array = engine.eval_with_scope(&mut scope, &expr).unwrap();
            let e: f64 = res[0].as_float().unwrap();
            let s: Option<f64> = res[1].as_float().ok();

            assert!((rust_ema_res[i] - e).abs() < 1e-9);
            match (rust_sma_res[i], s) {
                (Some(r), Some(rh)) => assert!((r - rh).abs() < 1e-9),
                (None, None) => {}
                _ => panic!("SMA result mismatch at index {i}"),
            }
        }
    }
}

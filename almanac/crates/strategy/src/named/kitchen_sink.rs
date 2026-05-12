use std::collections::VecDeque;

use alm_core::{bar::Bar, signal::Signal, strategy::Strategy};
use alm_indicator::{Adx, Atr, BBands, Ema, Macd, Rsi};

use crate::bar_resampler::TimeBarResampler;

/// Kitchen Sink — demonstrator strategy that exercises every Rhai helper added
/// in v2: `cross_above/below`, `above/below`, `rising_n/falling_n`, `momentum`,
/// `highest`, H1 MTF indicator, and ATR-based `tp/sl`.
///
/// Logic mirrors the canonical Rhai script verbatim so the two can be parity-tested:
///
/// ```rhai
/// let ema9    = ind.ema(9, 4);
/// let ema21   = ind.ema(21, 4);
/// let ema50   = ind.ema(50, 4);
/// let rsi14   = ind.rsi(14, 4);
/// let adx14   = ind.adx(14, 5);
/// let atr14   = ind.atr(14, 3);
/// let macd    = ind.macd(12, 3);  // not used in signals
/// let bb      = ind.bbands(20, 3);
/// let h1_ema  = ind.ema(20, "H1", 3);
///
/// let trend   = adx14[0].adx > 25.0 && rising_n(adx14.adx, 3);
/// let mom     = momentum(rsi14, 3) > 0.0;
/// let squeeze = (bb[0].upper - bb[0].lower) < atr14[0].atr * 1.5;
/// let h_break = highest(close, 20) == close[0];
///
/// let entry = cross_above(ema9, ema21)
///          && above(ema21, ema50)
///          && rsi14[0] > 50.0 && rsi14[0] < 70.0
///          && trend && mom && squeeze && h_break
///          && above(h1_ema, ema50);
///
/// let exit  = cross_below(ema9, ema21) || rsi14[0] > 80.0 || falling_n(adx14.adx, 2);
/// let tp    = close[0] + atr14[0].atr * 2.5;
/// let sl    = close[0] - atr14[0].atr * 1.5;
/// ```
pub struct KitchenSinkStrategy {
    // ── M1 indicators ──────────────────────────────────────────────────────────
    ema9:  Ema,
    ema21: Ema,
    ema50: Ema,
    rsi14: Rsi,
    adx14: Adx,
    atr14: Atr,
    macd:  Macd, // fed but not wired to signals — bench completeness
    bb20:  BBands,

    // ── Depth-limited history buffers (newest at front/index-0) ────────────────
    /// depth 4: [0]=curr, [1]=prev (cross), [2],[3] unused by entry but kept for depth
    ema9_hist:  VecDeque<f64>,
    ema21_hist: VecDeque<f64>,
    ema50_hist: VecDeque<f64>,
    /// depth 4: [3] for momentum(rsi14, 3)
    rsi14_hist: VecDeque<f64>,
    /// depth 5: [0..=3] for rising_n(adx14, 3) / falling_n(adx14, 2)
    adx14_hist: VecDeque<f64>,
    /// depth 3: [0] only
    atr14_hist: VecDeque<f64>,
    bb_upper_hist: VecDeque<f64>,
    bb_lower_hist: VecDeque<f64>,
    /// depth 20: highest(close, 20)
    close_buf: VecDeque<f64>,

    // ── H1 MTF ─────────────────────────────────────────────────────────────────
    h1_rs:      TimeBarResampler,
    h1_ema:     Ema,
    h1_ema_hist: VecDeque<f64>, // depth 3

    // ── State ───────────────────────────────────────────────────────────────────
    in_position: bool,
}

const H1_MS: i64 = 3_600_000;

impl KitchenSinkStrategy {
    pub fn new() -> Self {
        Self {
            ema9:  Ema::new(9),
            ema21: Ema::new(21),
            ema50: Ema::new(50),
            rsi14: Rsi::new(14),
            adx14: Adx::new(14),
            atr14: Atr::new(14),
            macd:  Macd::new(12, 26, 9),
            bb20:  BBands::new(20, 2.0),

            ema9_hist:     VecDeque::with_capacity(4),
            ema21_hist:    VecDeque::with_capacity(4),
            ema50_hist:    VecDeque::with_capacity(4),
            rsi14_hist:    VecDeque::with_capacity(4),
            adx14_hist:    VecDeque::with_capacity(5),
            atr14_hist:    VecDeque::with_capacity(3),
            bb_upper_hist: VecDeque::with_capacity(3),
            bb_lower_hist: VecDeque::with_capacity(3),
            close_buf:     VecDeque::with_capacity(20),

            h1_rs:       TimeBarResampler::new(H1_MS),
            h1_ema:      Ema::new(20),
            h1_ema_hist: VecDeque::with_capacity(3),

            in_position: false,
        }
    }
}

impl Default for KitchenSinkStrategy {
    fn default() -> Self { Self::new() }
}

// ── helpers ────────────────────────────────────────────────────────────────────

#[inline]
fn push(hist: &mut VecDeque<f64>, val: f64, cap: usize) {
    hist.push_front(val);
    if hist.len() > cap { hist.pop_back(); }
}

/// `cross_above(a, b)` — a[1] <= b[1] && a[0] > b[0].
#[inline]
fn cross_above(a: &VecDeque<f64>, b: &VecDeque<f64>) -> bool {
    a.len() >= 2 && b.len() >= 2 && a[1] <= b[1] && a[0] > b[0]
}

/// `cross_below(a, b)` — a[1] >= b[1] && a[0] < b[0].
#[inline]
fn cross_below(a: &VecDeque<f64>, b: &VecDeque<f64>) -> bool {
    a.len() >= 2 && b.len() >= 2 && a[1] >= b[1] && a[0] < b[0]
}

/// `above(a, b)` — a[0] > b[0].
#[inline]
fn above(a: &VecDeque<f64>, b: &VecDeque<f64>) -> bool {
    !a.is_empty() && !b.is_empty() && a[0] > b[0]
}

/// `rising_n(arr, n)` — a[0] > a[1] > … > a[n].
#[inline]
fn rising_n(arr: &VecDeque<f64>, n: usize) -> bool {
    arr.len() >= n + 1 && (0..n).all(|i| arr[i] > arr[i + 1])
}

/// `falling_n(arr, n)` — a[0] < a[1] < … < a[n].
#[inline]
fn falling_n(arr: &VecDeque<f64>, n: usize) -> bool {
    arr.len() >= n + 1 && (0..n).all(|i| arr[i] < arr[i + 1])
}

/// `momentum(arr, n)` — arr[0] - arr[n].
#[inline]
fn momentum(arr: &VecDeque<f64>, n: usize) -> f64 {
    if arr.len() > n { arr[0] - arr[n] } else { 0.0 }
}

/// `highest(arr, n)` — max of arr[0..n].
#[inline]
fn highest(arr: &VecDeque<f64>, n: usize) -> f64 {
    arr.iter().take(n).copied().fold(f64::NEG_INFINITY, f64::max)
}

// ── Strategy impl ──────────────────────────────────────────────────────────────

impl Strategy for KitchenSinkStrategy {
    fn on_bar(&mut self, bar: &Bar) -> Vec<Signal> {
        // ── Update M1 indicators ───────────────────────────────────────────────
        let e9  = self.ema9.update(bar.close);
        let e21 = self.ema21.update(bar.close);
        let e50 = self.ema50.update(bar.close);
        let rsi = self.rsi14.update(bar.close);
        let adx = self.adx14.update(bar.high, bar.low, bar.close);
        let atr = self.atr14.update(bar.high, bar.low, bar.close);
        let _m  = self.macd.update(bar.close);
        let bb  = self.bb20.update(bar.close);

        // ── Update H1 resampler ────────────────────────────────────────────────
        if let Some(h1_bar) = self.h1_rs.push(bar) {
            if let Some(h1v) = self.h1_ema.update(h1_bar.close) {
                push(&mut self.h1_ema_hist, h1v, 3);
            }
        }

        // ── Push to history buffers ────────────────────────────────────────────
        push(&mut self.close_buf, bar.close, 20);

        let (Some(e9), Some(e21), Some(e50), Some(rsi), Some(adx), Some(atr), Some(bb)) =
            (e9, e21, e50, rsi, adx, atr, bb)
        else {
            return vec![];
        };

        push(&mut self.ema9_hist,     e9,        4);
        push(&mut self.ema21_hist,    e21,       4);
        push(&mut self.ema50_hist,    e50,       4);
        push(&mut self.rsi14_hist,    rsi,       4);
        push(&mut self.adx14_hist,    adx.adx,   5);
        push(&mut self.atr14_hist,    atr.atr,   3);
        push(&mut self.bb_upper_hist, bb.upper,  3);
        push(&mut self.bb_lower_hist, bb.lower,  3);

        // ── Entry ──────────────────────────────────────────────────────────────
        if !self.in_position {
            let trend   = !self.adx14_hist.is_empty() && self.adx14_hist[0] > 25.0
                          && rising_n(&self.adx14_hist, 3);
            let mom     = momentum(&self.rsi14_hist, 3) > 0.0;
            let squeeze = !self.bb_upper_hist.is_empty() && !self.bb_lower_hist.is_empty()
                          && !self.atr14_hist.is_empty()
                          && (self.bb_upper_hist[0] - self.bb_lower_hist[0])
                             < self.atr14_hist[0] * 1.5;
            let h_break = !self.close_buf.is_empty()
                          && highest(&self.close_buf, 20) == self.close_buf[0];
            let entry = cross_above(&self.ema9_hist, &self.ema21_hist)
                        && above(&self.ema21_hist, &self.ema50_hist)
                        && !self.rsi14_hist.is_empty()
                        && self.rsi14_hist[0] > 50.0 && self.rsi14_hist[0] < 70.0
                        && trend && mom && squeeze && h_break
                        && above(&self.h1_ema_hist, &self.ema50_hist);

            if entry {
                self.in_position = true;
                let tp = bar.close + atr.atr * 2.5;
                let sl = bar.close - atr.atr * 1.5;
                let mut sig = Signal::long(bar.timestamp, &bar.symbol, 1.0);
                sig.price        = Some(bar.close);
                sig.target_price = Some(tp);
                sig.stop_price   = Some(sl);
                return vec![sig];
            }
        } else {
            // ── Exit ──────────────────────────────────────────────────────────
            let exit = cross_below(&self.ema9_hist, &self.ema21_hist)
                       || (!self.rsi14_hist.is_empty() && self.rsi14_hist[0] > 80.0)
                       || falling_n(&self.adx14_hist, 2);

            if exit {
                self.in_position = false;
                return vec![Signal::close(bar.timestamp, &bar.symbol)];
            }
        }

        vec![]
    }

    fn name(&self) -> &str { "kitchen_sink" }

    fn reset(&mut self) {
        self.ema9  = Ema::new(9);
        self.ema21 = Ema::new(21);
        self.ema50 = Ema::new(50);
        self.rsi14 = Rsi::new(14);
        self.adx14 = Adx::new(14);
        self.atr14 = Atr::new(14);
        self.macd  = Macd::new(12, 26, 9);
        self.bb20  = BBands::new(20, 2.0);

        self.ema9_hist.clear();
        self.ema21_hist.clear();
        self.ema50_hist.clear();
        self.rsi14_hist.clear();
        self.adx14_hist.clear();
        self.atr14_hist.clear();
        self.bb_upper_hist.clear();
        self.bb_lower_hist.clear();
        self.close_buf.clear();

        self.h1_rs.reset();
        self.h1_ema = Ema::new(20);
        self.h1_ema_hist.clear();

        self.in_position = false;
    }
}

// ── Tests ──────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use crate::factory::build_strategy;
    use crate::test_utils::{bar, run, assert_parity};
    use serde_json::json;

    /// Four-phase bar sequence used for parity and reset tests.
    ///
    /// **Signals do NOT fire on this synthetic data** — the 8 entry conditions
    /// (`cross_above`, `above(ema21,ema50)`, RSI 50-70, ADX > 25 & rising_n(3),
    /// RSI momentum, BB squeeze, new-20-bar-high, `above(h1_ema,ema50)`) are
    /// structurally hard to satisfy simultaneously with step-function price series:
    ///
    /// - `above(h1_ema, ema50)` is only TRUE after a significant decline (H1 EMA20
    ///   lags behind M1 EMA50), but during that same recovery, `above(ema21, ema50)`
    ///   is typically FALSE until several dozen bars after `cross_above` already fired.
    /// - `rising_n(adx14, 3)` requires ADX to be increasing, which only happens during
    ///   a trend transition; with deterministic patterns ADX converges to a constant.
    /// - `h_break` (new 20-bar high) is only true every N bars at cycle peaks, rarely
    ///   coinciding with a `cross_above` event.
    ///
    /// The parity test is still valid: both named and Rhai implementations must agree
    /// (both produce empty signal lists). Run on real M1 tick data for actual fills.
    fn kitchen_sink_bars() -> Vec<Bar> {
        let mut ts = 0i64;
        let mut bars = vec![];

        // Phase 1: 4200 M1 bars (70 H1), gentle uptrend 100→300
        for i in 0..4200u32 {
            let p = 100.0 + i as f64 * (200.0 / 4200.0);
            bars.push(bar(ts, p));
            ts += 60_000;
        }

        // Phase 2: 360 M1 bars (6 H1), flat 300±0.5 (BB squeeze)
        for i in 0..360u32 {
            let p = 300.0 + if i % 2 == 0 { 0.5 } else { -0.5 };
            bars.push(bar(ts, p));
            ts += 60_000;
        }

        // Phase 3: 1200 M1 bars (20 H1), sharp uptrend 300→500
        for i in 0..1200u32 {
            let p = 300.0 + i as f64 * (200.0 / 1200.0);
            bars.push(bar(ts, p));
            ts += 60_000;
        }

        // Phase 4: 600 M1 bars (10 H1), decline 500→300
        for i in 0..600u32 {
            let p = 500.0 - i as f64 * (200.0 / 600.0);
            bars.push(bar(ts, p));
            ts += 60_000;
        }

        bars
    }

    const RHAI_SCRIPT: &str = r#"
let ema9    = ind.ema(9, 4);
let ema21   = ind.ema(21, 4);
let ema50   = ind.ema(50, 4);
let rsi14   = ind.rsi(14, 4);
let adx14   = ind.adx(14, 5);
let atr14   = ind.atr(14, 3);
let macd    = ind.macd(12, 3);
let bb      = ind.bbands(20, 3);
let h1_ema  = ind.ema(20, "H1", 3);

let trend   = adx14[0].adx > 25.0 && rising_n(adx14.adx, 3);
let mom     = momentum(rsi14, 3) > 0.0;
let squeeze = (bb[0].upper - bb[0].lower) < atr14[0].atr * 1.5;
let h_break = highest(close, 20) == close[0];

let entry = cross_above(ema9, ema21)
         && above(ema21, ema50)
         && rsi14[0] > 50.0 && rsi14[0] < 70.0
         && trend && mom && squeeze && h_break
         && above(h1_ema, ema50);

let exit  = cross_below(ema9, ema21) || rsi14[0] > 80.0 || falling_n(adx14.adx, 2);
let tp    = close[0] + atr14[0].atr * 2.5;
let sl    = close[0] - atr14[0].atr * 1.5;
"#;

    #[test]
    fn kitchen_sink_parity_named_vs_rhai() {
        let bars = kitchen_sink_bars();

        let mut named = KitchenSinkStrategy::new();
        let named_sigs = run(&mut named, &bars);

        let mut rhai = build_strategy("rhai", &json!({ "script": RHAI_SCRIPT })).unwrap();
        let rhai_sigs = run(rhai.as_mut(), &bars);

        // Both must agree — even if no signals fire on this synthetic data.
        // All 8 entry conditions must be simultaneously true, which is rare on synthetic bars.
        // Run on real M1 tick data to observe actual fills.
        assert_parity("kitchen_sink named vs rhai", &named_sigs, &rhai_sigs);
    }

    #[test]
    fn kitchen_sink_named_reset_reproducible() {
        let bars = kitchen_sink_bars();
        let mut s = KitchenSinkStrategy::new();
        let r1 = run(&mut s, &bars);
        s.reset();
        let r2 = run(&mut s, &bars);
        assert_parity("kitchen_sink reset: run1 vs run2", &r1, &r2);
    }

    #[test]
    fn kitchen_sink_rhai_reset_reproducible() {
        let bars = kitchen_sink_bars();
        let mut s = build_strategy("rhai", &json!({ "script": RHAI_SCRIPT })).unwrap();
        let r1 = run(s.as_mut(), &bars);
        s.reset();
        let r2 = run(s.as_mut(), &bars);
        assert_parity("kitchen_sink rhai reset: run1 vs run2", &r1, &r2);
    }

    // ── Real M1 data integration tests ────────────────────────────────────────
    //
    // Load actual BTCUSDT M1 bars from testdata and verify:
    //   1. Named and Rhai strategies produce identical signals (parity on real data).
    //   2. Signals actually fire on real market conditions.
    //
    // Run with:
    //   cargo test -p alm-strategy kitchen_sink_real_m1 -- --ignored --nocapture
    //   cargo test -p alm-strategy kitchen_sink_real_m1_full -- --ignored --nocapture

    /// Recent data (~7 000 bars, 2026-04-13 to 2026-04-17). Fast enough for normal runs.
    #[test]
    #[ignore = "requires testdata; run with --ignored"]
    fn kitchen_sink_real_m1() {
        use alm_data::{ParquetFeed, BarFeed};
        use std::path::Path;

        let dir = Path::new(env!("CARGO_MANIFEST_DIR"))
            .join("../data/testdata/BTCUSDT/M1");

        let mut feed = ParquetFeed::load_dir(&dir, "BTCUSDT")
            .expect("failed to load BTCUSDT M1 testdata");

        let bars: Vec<_> = std::iter::from_fn(|| feed.next()).collect();
        eprintln!("Loaded {} M1 bars from recent testdata", bars.len());

        // Named strategy
        let mut named = KitchenSinkStrategy::new();
        let named_sigs = run(&mut named, &bars);

        // Rhai strategy
        let mut rhai = build_strategy("rhai", &json!({ "script": RHAI_SCRIPT })).unwrap();
        let rhai_sigs = run(rhai.as_mut(), &bars);

        eprintln!("Signals — named: {}, rhai: {}", named_sigs.len(), rhai_sigs.len());
        for (ts, dir) in &named_sigs {
            eprintln!("  named [{ts}] {dir:?}");
        }

        assert_parity("kitchen_sink real M1 named vs rhai", &named_sigs, &rhai_sigs);
        assert!(!named_sigs.is_empty(), "no signals fired on real data — check strategy logic");
    }

    /// Full 4-year history (~2 100 000 bars, 2022-04-13 to 2026-04-12). Slow.
    #[test]
    #[ignore = "requires testdata + ~30s; run with --ignored"]
    fn kitchen_sink_real_m1_full() {
        use alm_data::{ParquetFeed, BarFeed};
        use std::path::Path;

        let file = Path::new(env!("CARGO_MANIFEST_DIR"))
            .join("../data/testdata/BTCUSDT/M1/BTCUSDT_M1_2022-04-13_to_2026-04-12.parquet");

        let mut feed = ParquetFeed::load(&file, "BTCUSDT")
            .expect("failed to load BTCUSDT M1 4-year data");

        let bars: Vec<_> = std::iter::from_fn(|| feed.next()).collect();
        eprintln!("Loaded {} M1 bars (4-year)", bars.len());

        let mut named = KitchenSinkStrategy::new();
        let named_sigs = run(&mut named, &bars);

        let mut rhai = build_strategy("rhai", &json!({ "script": RHAI_SCRIPT })).unwrap();
        let rhai_sigs = run(rhai.as_mut(), &bars);

        eprintln!("Signals — named: {}, rhai: {} over {} bars",
            named_sigs.len(), rhai_sigs.len(), bars.len());

        assert_parity("kitchen_sink 4yr named vs rhai", &named_sigs, &rhai_sigs);
        assert!(!named_sigs.is_empty(), "no signals over 4 years — check strategy logic");
    }
}

use std::collections::VecDeque;

use alm_core::{signal::Signal, MtfSnapshot, MtfStrategy, Timeframe};
use alm_indicator::{Adx, Atr, BBands, Ema, Macd, Rsi};

/// Kitchen Sink — demonstrator MTF strategy that exercises every script DSL helper:
/// `cross_above/below`, `above/below`, `rising_n/falling_n`, `momentum`,
/// `highest`, H1 MTF indicator, and ATR-based `tp/sl`.
///
/// Receives M1 and H1 bars via [`MtfSnapshot`]. Requires the engine to supply
/// both `Timeframe::M1` and `Timeframe::H1` feeds.
///
/// ```text
/// let ema9    = ind.ema(9,  buf=4);
/// let ema21   = ind.ema(21, buf=4);
/// let ema50   = ind.ema(50, buf=4);
/// let rsi14   = ind.rsi(14, buf=4);
/// let adx14   = ind.adx(14, buf=5);
/// let atr14   = ind.atr(14, buf=3);
/// let macd    = ind.macd(12, buf=3);
/// let bb      = ind.bbands(20, buf=3);
/// let h1_ema  = ind.ema(20, "live_H1", buf=3);
///
/// let trend   = adx14[0] > 25.0 && rising_n(adx14, 3);
/// let mom     = momentum(rsi14, 3) > 0.0;
/// let squeeze = (bb[0].upper - bb[0].lower) < atr14[0] * 1.5;
/// let h_break = highest(close, 20) == close[0];
///
/// if cross_above(ema9, ema21) && above(ema21, ema50)
///     && rsi14[0] > 50.0 && rsi14[0] < 70.0
///     && trend && mom && squeeze && h_break && above(h1_ema, ema50) {
///   entry = true; tp = close[0] + atr14[0] * 2.5; sl = close[0] - atr14[0] * 1.5;
/// }
/// if cross_below(ema9, ema21) || rsi14[0] > 80.0 || falling_n(adx14, 2) {
///   exit = true;
/// }
/// ```
pub struct KitchenSinkStrategy {
    symbol: String,

    // ── M1 indicators ─────────────────────────────────────────────────────────
    ema9:  Ema,
    ema21: Ema,
    ema50: Ema,
    rsi14: Rsi,
    adx14: Adx,
    atr14: Atr,
    macd:  Macd,
    bb20:  BBands,

    // ── Depth-limited history buffers (newest = index 0) ──────────────────────
    ema9_hist:     VecDeque<f64>,
    ema21_hist:    VecDeque<f64>,
    ema50_hist:    VecDeque<f64>,
    rsi14_hist:    VecDeque<f64>,
    adx14_hist:    VecDeque<f64>,
    atr14_hist:    VecDeque<f64>,
    bb_upper_hist: VecDeque<f64>,
    bb_lower_hist: VecDeque<f64>,
    close_buf:     VecDeque<f64>,

    // ── H1 MTF ────────────────────────────────────────────────────────────────
    h1_ema:      Ema,
    h1_ema_hist: VecDeque<f64>,
}

impl KitchenSinkStrategy {
    pub fn new(symbol: impl Into<String>) -> Self {
        Self {
            symbol: symbol.into(),

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

            h1_ema:      Ema::new(20),
            h1_ema_hist: VecDeque::with_capacity(3),
        }
    }
}

// ── helpers ────────────────────────────────────────────────────────────────────

#[inline]
fn push(hist: &mut VecDeque<f64>, val: f64, cap: usize) {
    hist.push_front(val);
    if hist.len() > cap { hist.pop_back(); }
}

#[inline]
fn cross_above(a: &VecDeque<f64>, b: &VecDeque<f64>) -> bool {
    a.len() >= 2 && b.len() >= 2 && a[1] <= b[1] && a[0] > b[0]
}

#[inline]
fn cross_below(a: &VecDeque<f64>, b: &VecDeque<f64>) -> bool {
    a.len() >= 2 && b.len() >= 2 && a[1] >= b[1] && a[0] < b[0]
}

#[inline]
fn above(a: &VecDeque<f64>, b: &VecDeque<f64>) -> bool {
    !a.is_empty() && !b.is_empty() && a[0] > b[0]
}

#[inline]
fn rising_n(arr: &VecDeque<f64>, n: usize) -> bool {
    arr.len() >= n + 1 && (0..n).all(|i| arr[i] > arr[i + 1])
}

#[inline]
fn falling_n(arr: &VecDeque<f64>, n: usize) -> bool {
    arr.len() >= n + 1 && (0..n).all(|i| arr[i] < arr[i + 1])
}

#[inline]
fn momentum(arr: &VecDeque<f64>, n: usize) -> f64 {
    if arr.len() > n { arr[0] - arr[n] } else { 0.0 }
}

#[inline]
fn highest(arr: &VecDeque<f64>, n: usize) -> f64 {
    arr.iter().take(n).copied().fold(f64::NEG_INFINITY, f64::max)
}

// ── MtfStrategy impl ───────────────────────────────────────────────────────────

impl MtfStrategy for KitchenSinkStrategy {
    fn on_bars(&mut self, snap: MtfSnapshot<'_>) -> Vec<Signal> {
        // ── Process any H1 bars that arrived this tick ─────────────────────────
        for event in snap.events {
            if event.tf == Timeframe::H1 {
                if let Some(v) = self.h1_ema.update(event.bar.close) {
                    push(&mut self.h1_ema_hist, v, 3);
                }
            }
        }

        // ── Find the base (M1) bar for this tick ──────────────────────────────
        let m1_bar = snap.events.iter()
            .find(|e| e.tf == Timeframe::M1)
            .map(|e| e.bar);

        let Some(bar) = m1_bar else { return vec![]; };

        // ── Update M1 indicators ──────────────────────────────────────────────
        let e9  = self.ema9.update(bar.close);
        let e21 = self.ema21.update(bar.close);
        let e50 = self.ema50.update(bar.close);
        let rsi = self.rsi14.update(bar.close);
        let adx = self.adx14.update(bar.high, bar.low, bar.close);
        let atr = self.atr14.update(bar.high, bar.low, bar.close);
        let _m  = self.macd.update(bar.close);
        let bb  = self.bb20.update(bar.close);

        push(&mut self.close_buf, bar.close, 20);

        let (Some(e9), Some(e21), Some(e50), Some(rsi), Some(adx), Some(atr), Some(bb)) =
            (e9, e21, e50, rsi, adx, atr, bb)
        else {
            return vec![];
        };

        push(&mut self.ema9_hist,     e9,       4);
        push(&mut self.ema21_hist,    e21,      4);
        push(&mut self.ema50_hist,    e50,      4);
        push(&mut self.rsi14_hist,    rsi,      4);
        push(&mut self.adx14_hist,    adx.adx,  5);
        push(&mut self.atr14_hist,    atr.atr,  3);
        push(&mut self.bb_upper_hist, bb.upper, 3);
        push(&mut self.bb_lower_hist, bb.lower, 3);

        // Wait for H1 EMA to have 3 confirmed values.
        if self.h1_ema_hist.len() < 3 { return vec![]; }

        // ── Entry ─────────────────────────────────────────────────────────────
        let trend   = !self.adx14_hist.is_empty() && self.adx14_hist[0] > 25.0
                      && rising_n(&self.adx14_hist, 3);
        let mom     = momentum(&self.rsi14_hist, 3) > 0.0;
        let squeeze = !self.bb_upper_hist.is_empty() && !self.bb_lower_hist.is_empty()
                      && !self.atr14_hist.is_empty()
                      && (self.bb_upper_hist[0] - self.bb_lower_hist[0])
                         < self.atr14_hist[0] * 1.5;
        let h_break = !self.close_buf.is_empty()
                      && highest(&self.close_buf, 20) == self.close_buf[0];
        let entry   = cross_above(&self.ema9_hist, &self.ema21_hist)
                      && above(&self.ema21_hist, &self.ema50_hist)
                      && !self.rsi14_hist.is_empty()
                      && self.rsi14_hist[0] > 50.0 && self.rsi14_hist[0] < 70.0
                      && trend && mom && squeeze && h_break
                      && above(&self.h1_ema_hist, &self.ema50_hist);

        if entry {
            let tp  = bar.close + atr.atr * 2.5;
            let sl  = bar.close - atr.atr * 1.5;
            let mut sig = Signal::long(bar.timestamp, &self.symbol, 1.0);
            sig.price        = Some(bar.close);
            sig.target_price = Some(tp);
            sig.stop_price   = Some(sl);
            return vec![sig];
        }

        // ── Exit ──────────────────────────────────────────────────────────────
        let exit = cross_below(&self.ema9_hist, &self.ema21_hist)
                   || (!self.rsi14_hist.is_empty() && self.rsi14_hist[0] > 80.0)
                   || falling_n(&self.adx14_hist, 2);

        if exit {
            return vec![Signal::exit(bar.timestamp, &self.symbol)];
        }

        vec![]
    }

    fn name(&self) -> &str { "kitchen_sink" }
    fn script(&self) -> Option<&'static str> { Some(RHAI_SCRIPT) }

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

        self.h1_ema = Ema::new(20);
        self.h1_ema_hist.clear();
    }
}

pub(crate) const RHAI_SCRIPT: &str = r#"
let ema9    = ind.ema(9, buf=4);
let ema21   = ind.ema(21, buf=4);
let ema50   = ind.ema(50, buf=4);
let rsi14   = ind.rsi(14, buf=4);
let adx14   = ind.adx(14, buf=5);
let atr14   = ind.atr(14, buf=3);
let macd    = ind.macd(12, buf=3);
let bb      = ind.bbands(20, buf=3);
let h1_ema  = ind.ema(20, "H1", buf=3);

let trend   = adx14[0] > 25.0 && rising_n(adx14, 3);
let mom     = momentum(rsi14, 3) > 0.0;
let squeeze = (bb[0].upper - bb[0].lower) < atr14[0] * 1.5;
let h_break = highest(close, 20) == close[0];

if cross_above(ema9, ema21)
    && above(ema21, ema50)
    && rsi14[0] > 50.0 && rsi14[0] < 70.0
    && trend && mom && squeeze && h_break
    && above(h1_ema, ema50)
{
    entry = true;
    tp = close[0] + atr14[0] * 2.5;
    sl = close[0] - atr14[0] * 1.5;
}

if cross_below(ema9, ema21) || rsi14[0] > 80.0 || falling_n(adx14, 2) {
    exit = true;
}
"#;

#[cfg(test)]
mod tests {
    use super::*;
    use alm_core::{bar::Bar, MtfSnapshot, TfBarEvent, TfView, Timeframe};
    use std::collections::{HashMap, VecDeque, BTreeMap};

    #[test]
    fn on_bars_exit_fires_when_h1_warm() {
        let Some((m1_bars, h1_bars)) = crate::test_utils::load_real_m1_h1() else { return; };

        let mut s = KitchenSinkStrategy::new("BTCUSDT");

        let mut by_ts: BTreeMap<i64, Vec<(Timeframe, Bar)>> = BTreeMap::new();
        for b in &m1_bars {
            by_ts.entry(b.timestamp + Timeframe::M1.duration_ms()).or_default().push((Timeframe::M1, b.clone()));
        }
        for b in &h1_bars {
            by_ts.entry(b.timestamp + Timeframe::H1.duration_ms()).or_default().push((Timeframe::H1, b.clone()));
        }

        let mut confirmed: HashMap<Timeframe, VecDeque<Bar>> = HashMap::new();
        let mut exits = 0;

        for (&close_ts, tick) in &by_ts {
            for (tf, b) in tick {
                confirmed.entry(*tf).or_default().push_back(b.clone());
            }
            let events: Vec<TfBarEvent<'_>> = tick.iter()
                .map(|(tf, b)| TfBarEvent { tf: *tf, bar: b })
                .collect();
            let views: HashMap<Timeframe, TfView<'_>> = confirmed.iter()
                .map(|(tf, w)| (*tf, TfView { tf: *tf, confirmed: w }))
                .collect();
            let snap = MtfSnapshot { base_tf: Timeframe::M1, close_ts, events: &events, views: &views };
            let sigs = s.on_bars(snap);
            for sig in sigs {
                if sig.direction == alm_core::signal::Direction::Exit {
                    exits += 1;
                }
            }
        }
        assert!(exits > 0, "expected at least one Exit signal on real data");
    }
}

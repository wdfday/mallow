use alm_core::{signal::Signal, MtfSnapshot, MtfStrategy, Timeframe};
use alm_indicator::Ema;

/// Multi-timeframe MA-crossover strategy.
///
/// H1 EMA(50) sets the directional bias; M1 EMA(9)/EMA(21) crossover triggers.
///
/// Entry: close > H1 EMA(50) AND EMA9 crosses above EMA21.
/// Exit:  close < H1 EMA(50) OR  EMA9 crosses below  EMA21.
///
/// Logic mirrors the v2 script:
/// ```text
/// let h1_trend = ind.ema(50, "H1");
/// let ema9     = ind.ema(9);
/// let ema21    = ind.ema(21);
/// if close[0] > h1_trend[0] && cross_above(ema9, ema21) { entry = true; }
/// if close[0] < h1_trend[0] || cross_below(ema9, ema21) { exit  = true; }
/// ```
pub struct MtfMaCrossStrategy {
    h1_ema50:  Ema,
    m1_fast:   Ema,
    m1_slow:   Ema,
    curr_h1:   f64,
    h1_count:  usize,
    prev_fast: Option<f64>,
    curr_fast: Option<f64>,
    prev_slow: Option<f64>,
    curr_slow: Option<f64>,
}

impl MtfMaCrossStrategy {
    pub fn new() -> Self {
        Self {
            h1_ema50:  Ema::new(50),
            m1_fast:   Ema::new(9),
            m1_slow:   Ema::new(21),
            curr_h1:   0.0,
            h1_count:  0,
            prev_fast: None,
            curr_fast: None,
            prev_slow: None,
            curr_slow: None,
        }
    }
}

impl Default for MtfMaCrossStrategy {
    fn default() -> Self { Self::new() }
}

impl MtfStrategy for MtfMaCrossStrategy {
    fn name(&self) -> &str { "mtf_ma_cross" }

    fn reset(&mut self) {
        self.h1_ema50  = Ema::new(50);
        self.m1_fast   = Ema::new(9);
        self.m1_slow   = Ema::new(21);
        self.curr_h1   = 0.0;
        self.h1_count  = 0;
        self.prev_fast = None;
        self.curr_fast = None;
        self.prev_slow = None;
        self.curr_slow = None;
    }

    fn on_bars(&mut self, snap: MtfSnapshot<'_>) -> Vec<Signal> {
        // ── 1. Advance H1 confirmed EMA ────────────────────────────────────────
        for ev in snap.events {
            if ev.tf == Timeframe::H1 {
                if let Some(v) = self.h1_ema50.update(ev.bar.close) {
                    self.curr_h1  = v;
                    self.h1_count += 1;
                }
            }
        }

        // ── 2. Require a base (M1) bar ─────────────────────────────────────────
        let Some(base) = snap.base_bar() else { return vec![]; };

        // ── 3. Advance M1 EMAs, shift prev ────────────────────────────────────
        self.prev_fast = self.curr_fast;
        self.curr_fast = self.m1_fast.update(base.close);
        self.prev_slow = self.curr_slow;
        self.curr_slow = self.m1_slow.update(base.close);

        // ── 4. Warmth gate: 2 confirmed H1 values + both M1 EMAs have prev ────
        // (2 H1 bars = 120 M1 bars, so m1 EMAs are warm well before h1 gate clears)
        if self.h1_count < 2 { return vec![]; }
        let (Some(pf), Some(cf)) = (self.prev_fast, self.curr_fast) else { return vec![]; };
        let (Some(ps), Some(cs)) = (self.prev_slow, self.curr_slow) else { return vec![]; };

        // cross_above(ema9, ema21) = arr9[1] <= arr21[1] && arr9[0] > arr21[0]
        let cross_above = pf <= ps && cf > cs;
        // cross_below(ema9, ema21) = arr9[1] >= arr21[1] && arr9[0] < arr21[0]
        let cross_below = pf >= ps && cf < cs;

        let above_h1 = base.close > self.curr_h1;
        let below_h1 = base.close < self.curr_h1;

        if above_h1 && cross_above {
            return vec![Signal::long(base.timestamp, &base.symbol, 1.0)];
        }
        if below_h1 || cross_below {
            return vec![Signal::exit(base.timestamp, &base.symbol)];
        }

        vec![]
    }
}

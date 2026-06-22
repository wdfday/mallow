use alm_core::{signal::Signal, MtfSnapshot, MtfStrategy, Timeframe};
use alm_indicator::{Adx, Ema, Rsi};

/// Multi-timeframe ADX + MA + RSI pullback strategy.
///
/// H1 ADX(14) acts as a regime gate (>25 = trending);
/// H1 EMA(50) sets the directional bias;
/// M1 RSI(14) times the pullback entry.
///
/// Entry: H1 ADX > 25 AND close > H1 EMA(50) AND M1 RSI < 40.
/// Exit:  H1 ADX < 20 OR close < H1 EMA(50) OR M1 RSI > 70.
///
/// Mirrors:
/// ```text
/// let h1_adx   = ind.adx(14, "H1");
/// let h1_trend = ind.ema(50, "H1");
/// let rsi      = ind.rsi(14);
/// if h1_adx[0] > 25.0 && close[0] > h1_trend[0] && rsi[0] < 40.0 { entry = true; }
/// if h1_adx[0] < 20.0 || close[0] < h1_trend[0] || rsi[0] > 70.0 { exit = true; }
/// ```
pub struct MtfAdxPullbackStrategy {
    h1_adx:    Adx,
    h1_ema50:  Ema,
    m1_rsi:    Rsi,
    curr_adx:  f64,
    adx_count: usize,
    curr_h1:   f64,
    ema_count: usize,
}

impl MtfAdxPullbackStrategy {
    pub fn new() -> Self {
        Self {
            h1_adx:    Adx::new(14),
            h1_ema50:  Ema::new(50),
            m1_rsi:    Rsi::new(14),
            curr_adx:  0.0,
            adx_count: 0,
            curr_h1:   0.0,
            ema_count: 0,
        }
    }
}

impl Default for MtfAdxPullbackStrategy {
    fn default() -> Self { Self::new() }
}

impl MtfStrategy for MtfAdxPullbackStrategy {
    fn name(&self) -> &str { "mtf_adx_pullback" }

    fn reset(&mut self) {
        self.h1_adx    = Adx::new(14);
        self.h1_ema50  = Ema::new(50);
        self.m1_rsi    = Rsi::new(14);
        self.curr_adx  = 0.0;
        self.adx_count = 0;
        self.curr_h1   = 0.0;
        self.ema_count = 0;
    }

    fn on_bars(&mut self, snap: MtfSnapshot<'_>) -> Vec<Signal> {
        // ── 1. Advance H1 ADX and H1 EMA on confirmed bars ───────────────────
        for ev in snap.events {
            if ev.tf == Timeframe::H1 {
                if let Some(v) = self.h1_adx.update(ev.bar.high, ev.bar.low, ev.bar.close) {
                    self.curr_adx = v.adx;
                    self.adx_count += 1;
                }
                if let Some(v) = self.h1_ema50.update(ev.bar.close) {
                    self.curr_h1 = v;
                    self.ema_count += 1;
                }
            }
        }

        // ── 2. Require base bar ───────────────────────────────────────────────
        let Some(base) = snap.base_bar() else { return vec![]; };

        // ── 3. Advance M1 RSI ─────────────────────────────────────────────────
        let rsi = self.m1_rsi.update(base.close).unwrap_or(0.0);

        // ── 4. Warmth gate: both H1 indicators must have ≥2 confirmed values ─
        // ADX(14): first value at H1 bar ~28 (2×14); EMA(50): at H1 bar 50.
        // EMA dominates → effective gate clears ~H1 bar 51.
        if self.adx_count < 2 || self.ema_count < 2 { return vec![]; }

        let trending    = self.curr_adx > 25.0;
        let weak_trend  = self.curr_adx < 20.0;
        let above_trend = base.close > self.curr_h1;
        let below_trend = base.close < self.curr_h1;

        if trending && above_trend && rsi < 40.0 {
            return vec![Signal::long(base.timestamp, &base.symbol, 1.0)];
        }
        if weak_trend || below_trend || rsi > 70.0 {
            return vec![Signal::exit(base.timestamp, &base.symbol)];
        }

        vec![]
    }

    fn script(&self) -> Option<&'static str> {
        Some(RHAI_SCRIPT)
    }
}

pub(crate) const RHAI_SCRIPT: &str = r#"
let h1_adx   = ind.adx(14, "H1");
let h1_trend = ind.ema(50, "H1");
let rsi      = ind.rsi(14);
if h1_adx[0] > 25.0 && close[0] > h1_trend[0] && rsi[0] < 40.0 { entry = true; }
if h1_adx[0] < 20.0 || close[0] < h1_trend[0] || rsi[0] > 70.0 { exit  = true; }
"#;


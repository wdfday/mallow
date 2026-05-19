use alm_core::{signal::Signal, MtfSnapshot, MtfStrategy, Timeframe};
use alm_indicator::{BBands, Macd, Rsi};

/// Multi-timeframe Bollinger Bands + MACD + RSI strategy.
///
/// H1 BBands(20, 2.0) defines the structural zone;
/// M1 MACD(12, 26, 9) histogram signs momentum direction;
/// M1 RSI(14) acts as a final overbought/oversold filter.
///
/// Entry: close > H1 BB upper AND M1 MACD histogram > 0 AND M1 RSI < 55.
/// Exit:  close < H1 BB middle OR M1 MACD histogram < 0.
///
/// Mirrors:
/// ```text
/// let h1_bb = ind.bbands(20, "H1");
/// let macd  = ind.macd(12);
/// let rsi   = ind.rsi(14);
/// if close[0] > h1_bb[0].upper && macd[0].histogram > 0.0 && rsi[0] < 55.0 { entry = true; }
/// if close[0] < h1_bb[0].middle || macd[0].histogram < 0.0 { exit = true; }
/// ```
pub struct MtfBbMacdStrategy {
    h1_bb:    BBands,
    m1_macd:  Macd,
    m1_rsi:   Rsi,
    bb_upper:  f64,
    bb_middle: f64,
    bb_count:  usize,
}

impl MtfBbMacdStrategy {
    pub fn new() -> Self {
        Self {
            h1_bb:    BBands::new(20, 2.0),
            m1_macd:  Macd::new(12, 26, 9),
            m1_rsi:   Rsi::new(14),
            bb_upper:  0.0,
            bb_middle: 0.0,
            bb_count:  0,
        }
    }
}

impl Default for MtfBbMacdStrategy {
    fn default() -> Self { Self::new() }
}

impl MtfStrategy for MtfBbMacdStrategy {
    fn name(&self) -> &str { "mtf_bb_macd" }

    fn reset(&mut self) {
        self.h1_bb    = BBands::new(20, 2.0);
        self.m1_macd  = Macd::new(12, 26, 9);
        self.m1_rsi   = Rsi::new(14);
        self.bb_upper  = 0.0;
        self.bb_middle = 0.0;
        self.bb_count  = 0;
    }

    fn on_bars(&mut self, snap: MtfSnapshot<'_>) -> Vec<Signal> {
        // ── 1. Advance H1 Bollinger Bands on confirmed bars ───────────────────
        for ev in snap.events {
            if ev.tf == Timeframe::H1 {
                if let Some(v) = self.h1_bb.update(ev.bar.close) {
                    self.bb_upper  = v.upper;
                    self.bb_middle = v.middle;
                    self.bb_count  += 1;
                }
            }
        }

        // ── 2. Require base bar ───────────────────────────────────────────────
        let Some(base) = snap.base_bar() else { return vec![]; };

        // ── 3. Advance M1 MACD and RSI ────────────────────────────────────────
        let macd = self.m1_macd.update(base.close);
        let rsi  = self.m1_rsi.update(base.close).unwrap_or(50.0);

        // ── 4. Warmth gate: H1 BB needs ≥2 confirmed values; MACD must emit ──
        if self.bb_count < 2 { return vec![]; }
        let Some(m) = macd else { return vec![]; };

        let above_upper  = base.close > self.bb_upper;
        let below_middle = base.close < self.bb_middle;

        if above_upper && m.histogram > 0.0 && rsi < 55.0 {
            return vec![Signal::long(base.timestamp, &base.symbol, 1.0)];
        }
        if below_middle || m.histogram < 0.0 {
            return vec![Signal::exit(base.timestamp, &base.symbol)];
        }

        vec![]
    }
}

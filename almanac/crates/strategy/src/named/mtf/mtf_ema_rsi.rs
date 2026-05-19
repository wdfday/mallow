use alm_core::{signal::Signal, MtfSnapshot, MtfStrategy, Timeframe};
use alm_indicator::{Ema, Rsi};

/// Multi-timeframe EMA+RSI strategy.
///
/// H1 EMA(20) acts as trend filter; M1 RSI(14) provides the entry/exit trigger.
///
/// Entry: H1 EMA rising (arr[0] > arr[1]) AND M1 RSI < 40.
/// Exit:  H1 EMA falling (arr[0] < arr[1]) OR  M1 RSI > 70.
///
/// Logic mirrors the v2 script:
/// ```text
/// let h1_ema = ind.ema(20, "H1");
/// let rsi    = ind.rsi(14);
/// if rising(h1_ema) && rsi[0] < 40.0 { entry = true; }
/// if falling(h1_ema) || rsi[0] > 70.0 { exit = true; }
/// ```
pub struct MtfEmaRsiStrategy {
    h1_ema:   Ema,
    m1_rsi:   Rsi,
    prev_h1:  f64,
    curr_h1:  f64,
    h1_count: usize,
}

impl MtfEmaRsiStrategy {
    pub fn new() -> Self {
        Self {
            h1_ema:   Ema::new(20),
            m1_rsi:   Rsi::new(14),
            prev_h1:  0.0,
            curr_h1:  0.0,
            h1_count: 0,
        }
    }
}

impl Default for MtfEmaRsiStrategy {
    fn default() -> Self { Self::new() }
}

impl MtfStrategy for MtfEmaRsiStrategy {
    fn name(&self) -> &str { "mtf_ema_rsi" }

    fn reset(&mut self) {
        self.h1_ema   = Ema::new(20);
        self.m1_rsi   = Rsi::new(14);
        self.prev_h1  = 0.0;
        self.curr_h1  = 0.0;
        self.h1_count = 0;
    }

    fn on_bars(&mut self, snap: MtfSnapshot<'_>) -> Vec<Signal> {
        // ── 1. Advance H1 confirmed state ──────────────────────────────────────
        for ev in snap.events {
            if ev.tf == Timeframe::H1 {
                if let Some(v) = self.h1_ema.update(ev.bar.close) {
                    self.prev_h1  = self.curr_h1;
                    self.curr_h1  = v;
                    self.h1_count += 1;
                }
            }
        }

        // ── 2. Require a base (M1) bar ─────────────────────────────────────────
        let Some(base) = snap.base_bar() else { return vec![]; };

        // ── 3. Advance M1 RSI ──────────────────────────────────────────────────
        let rsi = self.m1_rsi.update(base.close).unwrap_or(0.0);

        // ── 4. Warmth gate: need 2 confirmed H1 values (matches buf_depth=2) ───
        if self.h1_count < 2 { return vec![]; }

        let rising  = self.curr_h1 > self.prev_h1;
        let falling = self.curr_h1 < self.prev_h1;

        if rising && rsi < 40.0 {
            return vec![Signal::long(base.timestamp, &base.symbol, 1.0)];
        }
        if falling || rsi > 70.0 {
            return vec![Signal::exit(base.timestamp, &base.symbol)];
        }

        vec![]
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use alm_core::{bar::Bar, MtfSnapshot, TfBarEvent, TfView, Timeframe};
    use std::collections::{HashMap, VecDeque};

    fn b(ts: i64, c: f64) -> Bar {
        Bar::new(ts, "TEST", c, c*1.005, c*0.995, c, 1000.0)
    }

    /// Feed 22 H1 periods (60 M1 bars each, price=100 flat) directly via on_bars.
    /// EMA(20) returns first value at H1 bar 19 (0-indexed) → h1_count=1.
    /// At H1 bar 20 → h1_count=2; RSI=100>70 → Exit fires.
    #[test]
    fn on_bars_exit_fires_when_h1_warm_and_rsi_high() {
        const M1_MS: i64 = 60_000;
        const H1_MS: i64 = 3_600_000;

        let mut s = MtfEmaRsiStrategy::new();
        let mut m1_win: VecDeque<Bar> = VecDeque::new();
        let mut h1_win: VecDeque<Bar> = VecDeque::new();
        let mut first_exit: Option<i64> = None;

        for h in 0..22_i64 {
            // 59 M1-only bars within this H1 period
            for m in 0..59_i64 {
                let ts = h * H1_MS + m * M1_MS;
                let mb = b(ts, 100.0);
                m1_win.push_back(mb.clone());
                let evs = vec![TfBarEvent { tf: Timeframe::M1, bar: &mb }];
                let mut views = HashMap::new();
                views.insert(Timeframe::M1, TfView { tf: Timeframe::M1, confirmed: &m1_win });
                if !h1_win.is_empty() {
                    views.insert(Timeframe::H1, TfView { tf: Timeframe::H1, confirmed: &h1_win });
                }
                let snap = MtfSnapshot { base_tf: Timeframe::M1, close_ts: ts + M1_MS, events: &evs, views: &views };
                let sigs = s.on_bars(snap);
                if first_exit.is_none() {
                    assert!(sigs.is_empty(), "h={h},m={m}: no exit expected yet");
                }
            }
            // Co-close: 60th M1 bar + H1 bar h
            let ts_m1 = h * H1_MS + 59 * M1_MS;
            let mb = b(ts_m1, 100.0);
            let hb = b(h * H1_MS, 100.0);
            m1_win.push_back(mb.clone());
            h1_win.push_back(hb.clone());
            let evs = vec![
                TfBarEvent { tf: Timeframe::M1, bar: &mb },
                TfBarEvent { tf: Timeframe::H1, bar: &hb },
            ];
            let mut views = HashMap::new();
            views.insert(Timeframe::M1, TfView { tf: Timeframe::M1, confirmed: &m1_win });
            views.insert(Timeframe::H1, TfView { tf: Timeframe::H1, confirmed: &h1_win });
            let snap = MtfSnapshot {
                base_tf: Timeframe::M1,
                close_ts: (h + 1) * H1_MS,
                events: &evs,
                views: &views,
            };
            let sigs = s.on_bars(snap);
            if h >= 20 {
                // h1_count ≥ 2 after EMA(20) bar 19 and 20 (0-indexed)
                if first_exit.is_none() {
                    assert!(!sigs.is_empty(), "h={h}: Exit expected (h1_count≥2, rsi=100>70)");
                    assert_eq!(sigs[0].direction, alm_core::signal::Direction::Exit);
                    first_exit = Some(sigs[0].timestamp);
                }
            } else if first_exit.is_none() {
                assert!(sigs.is_empty(), "h={h}: too early, h1_count<2");
            }
        }
        assert!(first_exit.is_some(), "expected at least one Exit signal");
    }
}

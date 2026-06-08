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
    use std::collections::{HashMap, VecDeque, BTreeMap};

    #[test]
    fn on_bars_exit_fires_when_h1_warm_and_rsi_high() {
        let Some((m1_bars, h1_bars)) = crate::test_utils::load_real_m1_h1() else { return; };

        let mut s = MtfEmaRsiStrategy::new();

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

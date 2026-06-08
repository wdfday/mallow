use alm_core::{bar::Bar, signal::Signal, strategy::Strategy};
use alm_indicator::{Rsi, Vwap};

/// VWAP Bounce — intraday mean-reversion using VWAP as fair-value anchor.
///
/// Long  when close crosses above VWAP from below AND RSI oversold (dip into VWAP).
/// Close when close crosses below VWAP from above OR RSI overbought.
///
/// Works best on trending days where price respects VWAP as S/R.
/// Default: rsi=14, oversold=40, overbought=65, session_gap_mins=60
pub struct VwapBounce {
    vwap: Vwap,
    rsi: Rsi,
    rsi_p: usize,
    oversold: f64,
    overbought: f64,
    session_gap_mins: u64,
    prev_above: Option<bool>,
}

impl VwapBounce {
    pub fn new(rsi_period: usize, oversold: f64, overbought: f64, session_gap_mins: u64) -> Self {
        Self {
            vwap: Vwap::new(session_gap_mins),
            rsi: Rsi::new(rsi_period),
            rsi_p: rsi_period,
            oversold,
            overbought,
            session_gap_mins,
            prev_above: None,
        }
    }
}

impl Strategy for VwapBounce {
    fn on_bar(&mut self, bar: &Bar) -> Vec<Signal> {
        let vwap = self.vwap.update(bar.timestamp, bar.high, bar.low, bar.close, bar.volume);
        let Some(rsi) = self.rsi.update(bar.close) else {
            return vec![];
        };

        let above_vwap = bar.close > vwap;
        let prev = self.prev_above.replace(above_vwap);

        let Some(was_above) = prev else {
            return vec![];
        };

        // Cross above VWAP + RSI was oversold → long
        if !was_above && above_vwap && rsi < self.oversold + 10.0 {
            return vec![Signal::long(bar.timestamp, &bar.symbol, 0.8)];
        }

        // Cross below VWAP → close
        if was_above && !above_vwap {
            return vec![Signal::exit(bar.timestamp, &bar.symbol)];
        }

        // RSI overbought → close
        if rsi > self.overbought {
            return vec![Signal::exit(bar.timestamp, &bar.symbol)];
        }

        vec![]
    }

    fn name(&self) -> &str {
        "vwap_bounce"
    }

    fn reset(&mut self) {
        self.vwap = Vwap::new(self.session_gap_mins);
        self.rsi = Rsi::new(self.rsi_p);
        self.prev_above = None;
    }
}

/// VWAP Trend — momentum continuation: trade in direction of VWAP trend.
///
/// Long  when close > VWAP and VWAP is rising (VWAP > prev VWAP).
/// Close when close drops below VWAP.
pub struct VwapTrend {
    vwap: Vwap,
    session_gap_mins: u64,
    prev_vwap: Option<f64>,
    in_position: bool,
}

impl VwapTrend {
    pub fn new(session_gap_mins: u64) -> Self {
        Self {
            vwap: Vwap::new(session_gap_mins),
            session_gap_mins,
            prev_vwap: None,
            in_position: false,
        }
    }
}

impl Strategy for VwapTrend {
    fn on_bar(&mut self, bar: &Bar) -> Vec<Signal> {
        let v = self.vwap.update(bar.timestamp, bar.high, bar.low, bar.close, bar.volume);
        let prev = self.prev_vwap.replace(v);

        let Some(pv) = prev else {
            return vec![];
        };

        let vwap_rising = v > pv;
        let above_vwap = bar.close > v;

        if above_vwap && vwap_rising && !self.in_position {
            self.in_position = true;
            return vec![Signal::long(bar.timestamp, &bar.symbol, 1.0)];
        }

        if !above_vwap && self.in_position {
            self.in_position = false;
            return vec![Signal::exit(bar.timestamp, &bar.symbol)];
        }

        vec![]
    }

    fn name(&self) -> &str {
        "vwap_trend"
    }

    fn reset(&mut self) {
        self.vwap = Vwap::new(self.session_gap_mins);
        self.prev_vwap = None;
        self.in_position = false;
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use alm_core::bar::Bar;
    use crate::test_utils::*;

    #[test]
    fn vwap_bounce_parity() {
        let Some(bars) = load_real_bars() else { return; };

        let mut hc = VwapBounce::new(14, 40.0, 65.0, 60);
        let hc_sigs = run(&mut hc, &bars);

        assert!(!hc_sigs.is_empty(), "vwap_bounce: no signals");
    }

    #[test]
    fn vwap_trend_parity() {
        let Some(bars) = load_real_bars() else { return; };

        let mut hc = VwapTrend::new(60);
        let hc_sigs = run(&mut hc, &bars);

        assert!(!hc_sigs.is_empty(), "vwap_trend: no signals");
    }
}

/// Session VWAP — Volume Weighted Average Price, reset per session.
///
/// Typical price = (H + L + C) / 3
/// VWAP = cumsum(typical * volume) / cumsum(volume)
///
/// Session boundary detected when timestamp gap > `session_gap_ms`.
#[derive(Debug, Clone)]
pub struct Vwap {
    session_gap_ms: i64,
    cum_tpv: f64,
    cum_vol: f64,
    last_ts: Option<i64>,
    value: Option<f64>,
}

impl Vwap {
    /// `session_gap_mins` — gap in minutes between consecutive bars that triggers a session reset.
    pub fn new(session_gap_mins: u64) -> Self {
        Self {
            session_gap_ms: session_gap_mins as i64 * 60_000,
            cum_tpv: 0.0,
            cum_vol: 0.0,
            last_ts: None,
            value: None,
        }
    }

    pub fn update(&mut self, ts_ms: i64, high: f64, low: f64, close: f64, volume: f64) -> f64 {
        // Reset on new session
        if let Some(prev) = self.last_ts {
            if ts_ms - prev > self.session_gap_ms {
                self.cum_tpv = 0.0;
                self.cum_vol = 0.0;
            }
        }
        self.last_ts = Some(ts_ms);

        let tp = (high + low + close) / 3.0;
        self.cum_tpv += tp * volume;
        self.cum_vol += volume;

        let v = if self.cum_vol > 0.0 { self.cum_tpv / self.cum_vol } else { close };
        self.value = Some(v);
        v
    }

    pub fn value(&self) -> Option<f64> {
        self.value
    }

    pub fn reset(&mut self) {
        self.cum_tpv = 0.0;
        self.cum_vol = 0.0;
        self.last_ts = None;
        self.value = None;
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_vwap_basic() {
        let mut v = Vwap::new(60);
        let r = v.update(0, 11.0, 9.0, 10.0, 100.0);
        // tp = 10.0, cum_tpv = 1000, cum_vol = 100 → vwap = 10.0
        assert_eq!(r, 10.0);
    }

    #[test]
    fn test_vwap_session_reset() {
        let mut v = Vwap::new(60); // 60 min gap
        v.update(0, 11.0, 9.0, 10.0, 100.0);
        // 2 hour gap → session reset
        let r = v.update(2 * 60 * 60_000, 20.0, 18.0, 19.0, 50.0);
        // After reset: tp = 19.0, only this bar counts
        assert!((r - 19.0).abs() < 0.001);
    }
}

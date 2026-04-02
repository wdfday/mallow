use std::collections::VecDeque;

/// Money Flow Index — volume-weighted RSI.
///
/// MFI = 100 − 100 / (1 + Money Flow Ratio)
/// MFR = Positive Money Flow / Negative Money Flow
///
/// Typical interpretation: MFI > 80 = overbought, MFI < 20 = oversold.
pub struct Mfi {
    period: usize,
    prev_tp: Option<f64>,
    pos_flows: VecDeque<f64>,
    neg_flows: VecDeque<f64>,
}

impl Mfi {
    pub fn new(period: usize) -> Self {
        assert!(period > 0);
        Self {
            period,
            prev_tp: None,
            pos_flows: VecDeque::with_capacity(period),
            neg_flows: VecDeque::with_capacity(period),
        }
    }

    pub fn update(&mut self, high: f64, low: f64, close: f64, volume: f64) -> Option<f64> {
        let tp = (high + low + close) / 3.0;
        let mf = tp * volume;

        let (pos, neg) = match self.prev_tp {
            Some(prev) if tp > prev => (mf, 0.0),
            Some(prev) if tp < prev => (0.0, mf),
            _ => (0.0, 0.0), // unchanged TP → neutral
        };

        self.prev_tp = Some(tp);

        self.pos_flows.push_back(pos);
        self.neg_flows.push_back(neg);

        if self.pos_flows.len() > self.period {
            self.pos_flows.pop_front();
            self.neg_flows.pop_front();
        }

        if self.pos_flows.len() < self.period {
            return None;
        }

        let pos_sum: f64 = self.pos_flows.iter().sum();
        let neg_sum: f64 = self.neg_flows.iter().sum();

        if neg_sum == 0.0 {
            return Some(100.0);
        }

        let mfr = pos_sum / neg_sum;
        Some(100.0 - 100.0 / (1.0 + mfr))
    }

    pub fn reset(&mut self) {
        self.prev_tp = None;
        self.pos_flows.clear();
        self.neg_flows.clear();
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_mfi_bounds() {
        let mut mfi = Mfi::new(5);
        let mut last = None;
        for i in 0..10 {
            let p = 100.0 + i as f64;
            last = mfi.update(p + 1.0, p - 1.0, p, 1000.0);
        }
        let v = last.unwrap();
        assert!(v >= 0.0 && v <= 100.0, "MFI out of bounds: {v}");
    }

    #[test]
    fn test_mfi_all_positive_flow_is_100() {
        // All bars rising → only positive money flow → MFI approaches 100
        let mut mfi = Mfi::new(3);
        let mut last = None;
        for i in 1..=10 {
            let p = 100.0 + i as f64;
            last = mfi.update(p + 0.5, p - 0.5, p, 1000.0);
        }
        let v = last.unwrap();
        assert!(v > 90.0, "MFI should be near 100 with all rising bars: {v}");
    }

    #[test]
    fn test_mfi_all_negative_flow_is_low() {
        let mut mfi = Mfi::new(3);
        let mut last = None;
        for i in 1..=10 {
            let p = 200.0 - i as f64;
            last = mfi.update(p + 0.5, p - 0.5, p, 1000.0);
        }
        let v = last.unwrap();
        assert!(v < 10.0, "MFI should be near 0 with all falling bars: {v}");
    }
}

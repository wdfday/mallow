//! Chaikin Money Flow (CMF)
//!
//! MFM = ((Close - Low) - (High - Close)) / (High - Low)
//! MFV = MFM * Volume
//! CMF = Sum(MFV, n) / Sum(Volume, n)
//!
//! Range: -1 to +1. Above 0 = buying pressure, below 0 = selling pressure.

use std::collections::VecDeque;

pub struct Cmf {
    period: usize,
    mfv_buf: VecDeque<f64>,
    vol_buf: VecDeque<f64>,
    mfv_sum: f64,
    vol_sum: f64,
}

impl Cmf {
    pub fn new(period: usize) -> Self {
        Self {
            period,
            mfv_buf: VecDeque::with_capacity(period),
            vol_buf: VecDeque::with_capacity(period),
            mfv_sum: 0.0,
            vol_sum: 0.0,
        }
    }

    pub fn update(&mut self, high: f64, low: f64, close: f64, volume: f64) -> Option<f64> {
        let range = high - low;
        let mfv = if range > f64::EPSILON {
            let mfm = ((close - low) - (high - close)) / range;
            mfm * volume
        } else {
            0.0
        };

        self.mfv_buf.push_back(mfv);
        self.vol_buf.push_back(volume);
        self.mfv_sum += mfv;
        self.vol_sum += volume;

        if self.mfv_buf.len() > self.period {
            self.mfv_sum -= self.mfv_buf.pop_front().unwrap();
            self.vol_sum -= self.vol_buf.pop_front().unwrap();
        }

        if self.mfv_buf.len() == self.period && self.vol_sum > f64::EPSILON {
            Some(self.mfv_sum / self.vol_sum)
        } else {
            None
        }
    }

    pub fn reset(&mut self) {
        self.mfv_buf.clear();
        self.vol_buf.clear();
        self.mfv_sum = 0.0;
        self.vol_sum = 0.0;
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_warmup() {
        let mut cmf = Cmf::new(20);
        for i in 0..20 {
            let v = cmf.update(110.0, 90.0, 100.0, 1000.0);
            if i < 19 {
                assert!(v.is_none());
            } else {
                assert!(v.is_some());
            }
        }
    }

    #[test]
    fn test_neutral_close() {
        // Close at midpoint → MFM = 0 → CMF = 0
        let mut cmf = Cmf::new(5);
        let mut last = None;
        for _ in 0..10 {
            last = cmf.update(110.0, 90.0, 100.0, 1000.0);
        }
        assert!(last.unwrap().abs() < 0.001);
    }

    #[test]
    fn test_bullish() {
        // Close near high → positive CMF
        let mut cmf = Cmf::new(5);
        let mut last = None;
        for _ in 0..10 {
            last = cmf.update(110.0, 90.0, 108.0, 1000.0);
        }
        assert!(last.unwrap() > 0.5);
    }

    #[test]
    fn test_reset() {
        let mut cmf = Cmf::new(5);
        for _ in 0..10 { cmf.update(110.0, 90.0, 100.0, 1000.0); }
        cmf.reset();
        assert!(cmf.update(110.0, 90.0, 100.0, 1000.0).is_none());
    }
}

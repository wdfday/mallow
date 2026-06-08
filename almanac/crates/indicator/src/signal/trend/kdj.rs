//! KDJ — Random Index, biến thể Stochastic phổ biến tại thị trường châu Á.
//!
//! KDJ là phiên bản mở rộng của Stochastic Oscillator, thêm đường J để tăng
//! độ nhạy. Rất phổ biến tại Việt Nam, Trung Quốc, Đài Loan và các thị trường
//! châu Á khác. J-line có thể vượt ra ngoài 0–100, cho tín hiệu sớm hơn K/D.
//!
//! # Công thức
//! ```text
//! RSV = (Close - LowestLow(n)) / (HighestHigh(n) - LowestLow(n)) × 100
//!       (Raw Stochastic Value — vị trí giá trong range n bar)
//!
//! K = SMA(RSV, k_period)      ← %K smoothed
//! D = SMA(K,   d_period)      ← %D thêm smoothed
//! J = 3×K − 2×D               ← extrapolation; thường vượt 0–100
//! ```
//!
//! # Ngưỡng thông dụng
//! - K/D > 80: overbought — xem xét bán
//! - K/D < 20: oversold  — xem xét mua
//! - J > 100: cực kỳ overbought; J < 0: cực kỳ oversold (nhưng không đảo chiều ngay)
//!
//! # Tín hiệu giao dịch
//! - **K cắt D từ dưới lên** (death cross ngược): long signal
//! - **K cắt D từ trên xuống**: short signal
//! - **J < 0 trong downtrend rồi bật lên**: mua mạnh
//! - **J > 100 trong uptrend rồi quay xuống**: bán mạnh
//!
//! # Warmup
//! Cần `period + k_period + d_period - 2` bar để tất cả SMA warm up.
//! Ví dụ: KDJ(9, 3, 3) cần 9 + 2 + 2 = 13 bar.

use std::collections::VecDeque;
use crate::util::{RollingMax, RollingMin};

pub struct KdjValue {
    pub k: f64,
    pub d: f64,
    pub j: f64,
}

#[derive(Clone)]
struct RollingSma {
    period: usize,
    buf: VecDeque<f64>,
    sum: f64,
}

impl RollingSma {
    fn new(period: usize) -> Self {
        Self { period, buf: VecDeque::with_capacity(period), sum: 0.0 }
    }
    fn update(&mut self, v: f64) -> Option<f64> {
        self.buf.push_back(v);
        self.sum += v;
        if self.buf.len() > self.period {
            self.sum -= self.buf.pop_front().unwrap();
        }
        if self.buf.len() == self.period {
            Some(self.sum / self.period as f64)
        } else {
            None
        }
    }
    fn reset(&mut self) {
        self.buf.clear();
        self.sum = 0.0;
    }
}

#[derive(Clone)]
pub struct Kdj {
    max_high: RollingMax,
    min_low: RollingMin,
    k_sma: RollingSma,
    d_sma: RollingSma,
}

impl Kdj {
    pub fn new(period: usize, k_period: usize, d_period: usize) -> Self {
        Self {
            max_high: RollingMax::new(period),
            min_low: RollingMin::new(period),
            k_sma: RollingSma::new(k_period),
            d_sma: RollingSma::new(d_period),
        }
    }

    pub fn description() -> &'static str {
        "KDJ — extension of Stochastic with a J line (3K - 2D) that leads K and D. Popular in Asian markets; J > 100 or < 0 flags extremes. Outputs: `.k` (default, smoothed RSV), `.d` (smoothed K), `.j` (3K − 2D, leading line)."
    }

    pub fn update(&mut self, high: f64, low: f64, close: f64) -> Option<KdjValue> {
        let highest = self.max_high.push(high);
        let lowest = self.min_low.push(low);
        let (highest, lowest) = match (highest, lowest) {
            (Some(h), Some(l)) => (h, l),
            _ => return None,
        };

        let rsv = if (highest - lowest).abs() > f64::EPSILON {
            ((close - lowest) / (highest - lowest) * 100.0).clamp(0.0, 100.0)
        } else {
            50.0
        };

        let k = self.k_sma.update(rsv)?.clamp(0.0, 100.0);
        let d = self.d_sma.update(k)?.clamp(0.0, 100.0);
        let j = 3.0 * k - 2.0 * d;

        Some(KdjValue { k, d, j })
    }

    pub fn reset(&mut self) {
        self.max_high.reset();
        self.min_low.reset();
        self.k_sma.reset();
        self.d_sma.reset();
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_warmup() {
        let mut kdj = Kdj::new(9, 3, 3);
        // needs 9 + 3-1 + 3-1 = 13 bars (idx 0..12 are None, idx 12 is first Some)
        for i in 0..14 {
            let v = kdj.update(105.0, 95.0, 100.0);
            if i < 12 {
                assert!(v.is_none(), "bar {i} should be None");
            } else {
                assert!(v.is_some(), "bar {i} should be Some");
            }
        }
    }

    #[test]
    fn test_uptrend() {
        let mut kdj = Kdj::new(9, 3, 3);
        let mut last = None;
        for i in 0..25 {
            let base = 100.0 + i as f64 * 2.0;
            last = kdj.update(base + 5.0, base - 5.0, base + 4.0); // close near high
        }
        let v = last.unwrap();
        assert!(v.k > 60.0, "K should be elevated in uptrend: {}", v.k);
    }

    #[test]
    fn test_j_range() {
        // J can exceed 0–100, that's expected
        let mut kdj = Kdj::new(9, 3, 3);
        for _ in 0..20 {
            kdj.update(110.0, 90.0, 108.0); // close near high
        }
    }

    #[test]
    fn test_reset() {
        let mut kdj = Kdj::new(9, 3, 3);
        for _ in 0..20 { kdj.update(105.0, 95.0, 100.0); }
        kdj.reset();
        assert!(kdj.update(105.0, 95.0, 100.0).is_none());
    }
}

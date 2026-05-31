use crate::util::{RollingMax, RollingMin};

/// Williams %R — momentum oscillator đo vị trí close so với highest high.
///
/// Được Larry Williams phát triển. Về mặt toán học là nghịch đảo của Stochastic %K:
/// thay vì đo close so với lowest low (%K = 0..100), %R đo từ highest high xuống
/// (range: −100..0). Chỉ là một cách nhìn khác của cùng một thông tin.
///
/// # Công thức
/// ```text
/// %R = (HighestHigh(n) − Close) / (HighestHigh(n) − LowestLow(n)) × (−100)
/// ```
///
/// - **%R = 0**: close = HighestHigh → giá ở đỉnh của range → overbought signal
/// - **%R = −100**: close = LowestLow → giá ở đáy của range → oversold signal
///
/// # Ngưỡng thông dụng
/// - **0 đến −20**: overbought — giá gần đỉnh range → xem xét bán
/// - **−80 đến −100**: oversold — giá gần đáy range → xem xét mua
///
/// # Cách đọc tín hiệu
/// - **%R thoát vùng oversold (vượt −80 lên)**: buy signal
/// - **%R thoát vùng overbought (xuống dưới −20)**: sell signal
/// - **%R giữ vùng −20 trong uptrend**: trend mạnh, giá liên tục đóng gần high
///
/// # So sánh với Stochastic
/// - Williams %R(n) = −100 × (1 − Stochastic %K(n)) — về toán học tương đương
/// - Williams %R thường không smooth (không có %D), nhạy hơn
/// - Stochastic thêm %D (SMA của %K) → ít whipsaw hơn
///
/// # Flat market
/// Khi HighestHigh = LowestLow: trả về −50.0 (trung lập).
///
/// # Warmup
/// Cần đúng `period` bar.
#[derive(Debug, Clone)]
pub struct WilliamsR {
    max_high: RollingMax,
    min_low: RollingMin,
}

impl WilliamsR {
    pub fn new(period: usize) -> Self {
        assert!(period > 0);
        Self {
            max_high: RollingMax::new(period),
            min_low: RollingMin::new(period),
        }
    }

    pub fn description() -> &'static str {
        "Williams %R — momentum oscillator (0 to -100) showing the close relative to the N-period high-low range. -80 = oversold; -20 = overbought. Outputs a single −100 to 0 value."
    }

    /// Standard Williams %R(14)
    pub fn standard() -> Self {
        Self::new(14)
    }

    pub fn update(&mut self, high: f64, low: f64, close: f64) -> Option<f64> {
        let highest = self.max_high.push(high);
        let lowest = self.min_low.push(low);
        let (highest, lowest) = match (highest, lowest) {
            (Some(h), Some(l)) => (h, l),
            _ => return None,
        };
        let range = highest - lowest;

        if range < f64::EPSILON {
            Some(-50.0)
        } else {
            Some((highest - close) / range * -100.0)
        }
    }

    pub fn is_ready(&self) -> bool {
        self.max_high.value().is_some()
    }

    pub fn reset(&mut self) {
        self.max_high.reset();
        self.min_low.reset();
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_williams_r_bounds() {
        let mut wr = WilliamsR::new(5);
        for i in 0..10 {
            let p = 100.0 + i as f64;
            if let Some(v) = wr.update(p + 1.0, p - 1.0, p) {
                assert!(v >= -100.0 && v <= 0.0, "%R={} out of bounds", v);
            }
        }
    }

    #[test]
    fn test_williams_r_extremes() {
        let mut wr = WilliamsR::new(3);
        // Create range 10..12, close at highest → %R near 0
        wr.update(12.0, 10.0, 11.0);
        wr.update(12.0, 10.0, 11.0);
        let v = wr.update(12.0, 10.0, 12.0).unwrap();
        assert!(
            (v - 0.0).abs() < 1.0,
            "Close at high should give %R near 0, got {v}"
        );
    }
}

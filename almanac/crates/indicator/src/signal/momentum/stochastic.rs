use crate::{util::{RollingMax, RollingMin}, Sma};

/// Stochastic Oscillator — đo vị trí giá hiện tại trong range n bar.
///
/// Được George Lane phát triển cuối thập niên 1950. Stochastic dựa trên quan sát:
/// trong uptrend, giá có xu hướng đóng cửa gần High; trong downtrend, gần Low.
/// Khi giá đóng cửa bắt đầu xa High trong uptrend → momentum yếu dần.
///
/// # Công thức
/// ```text
/// %K = (Close − LowestLow(n)) / (HighestHigh(n) − LowestLow(n)) × 100
///      (vị trí của close trong range n bar — 0% = đáy, 100% = đỉnh)
///
/// %D = SMA(%K, d_period)    ← signal line (smoothed %K)
/// ```
///
/// - **Fast Stochastic**: %K raw và %D = SMA(%K, 3)
/// - **Slow Stochastic**: %K = Fast %D (smoothed thêm); %D = SMA(Slow %K, 3)
///
/// # Ngưỡng thông dụng
/// - **%K / %D > 80**: overbought — giá ở vùng cao trong range
/// - **%K / %D < 20**: oversold — giá ở vùng thấp trong range
///
/// # Tín hiệu giao dịch
/// - **%K cắt %D từ dưới** trong vùng oversold (<20): buy signal
/// - **%K cắt %D từ trên** trong vùng overbought (>80): sell signal
/// - **Divergence**: giá new high nhưng %K thấp hơn → bearish divergence
/// - **Bull/Bear setup**: %K < 50 trong uptrend → pullback → long entry
///
/// # Flat market
/// Khi HighestHigh = LowestLow (range = 0): trả về %K = 50.0 (trung lập).
///
/// # Warmup
/// Cần `k_period + d_period - 1` bar.
#[derive(Debug, Clone)]
pub struct Stochastic {
    max_high: RollingMax,
    min_low: RollingMin,
    d_smooth: Sma,
}

#[derive(Debug, Clone, Copy)]
pub struct StochasticValue {
    pub k: f64,
    pub d: f64,
}

impl Stochastic {
    pub fn new(k_period: usize, d_period: usize) -> Self {
        Self {
            max_high: RollingMax::new(k_period),
            min_low: RollingMin::new(k_period),
            d_smooth: Sma::new(d_period),
        }
    }

    /// Standard Stochastic(14, 3)
    pub fn standard() -> Self {
        Self::new(14, 3)
    }

    pub fn update(&mut self, high: f64, low: f64, close: f64) -> Option<StochasticValue> {
        let highest = self.max_high.push(high);
        let lowest = self.min_low.push(low);
        let (highest, lowest) = match (highest, lowest) {
            (Some(h), Some(l)) => (h, l),
            _ => return None,
        };
        let range = highest - lowest;

        let k = if range > f64::EPSILON {
            (close - lowest) / range * 100.0
        } else {
            50.0 // flat market
        };

        self.d_smooth.update(k).map(|d| StochasticValue { k, d })
    }

    pub fn is_ready(&self) -> bool {
        self.d_smooth.is_ready()
    }

    pub fn reset(&mut self) {
        self.max_high.reset();
        self.min_low.reset();
        self.d_smooth.reset();
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_stochastic_bounds() {
        let mut stoch = Stochastic::new(5, 3);
        // Feed uptrend — %K should be near 100
        for i in 0..10 {
            let p = 100.0 + i as f64;
            if let Some(v) = stoch.update(p + 1.0, p - 1.0, p) {
                assert!(v.k >= 0.0 && v.k <= 100.0, "K={} out of bounds", v.k);
                assert!(v.d >= 0.0 && v.d <= 100.0, "D={} out of bounds", v.d);
            }
        }
    }
}

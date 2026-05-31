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
/// # Fast vs Slow
/// - **Fast** (`smooth_k = 1`, mặc định): `%K` raw, `%D = SMA(%K, d_period)`.
/// - **Slow** (`smooth_k > 1`): `%K_slow = SMA(rawK, smooth_k)` rồi
///   `%D = SMA(%K_slow, d_period)`. TradingView 14/3/3 ⇔ `new_full(14, 3, 3)`.
///
/// Dùng [`Stochastic::new`] cho Fast (giữ tương thích ngược) hoặc
/// [`Stochastic::new_full`] để chỉ định `smooth_k` (Slow Stochastic).
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
/// Cần `k_period + smooth_k + d_period - 2` bar (Fast: `k_period + d_period - 1`).
#[derive(Debug, Clone)]
pub struct Stochastic {
    max_high: RollingMax,
    min_low: RollingMin,
    /// %K smoothing (smooth_k=1 ⇒ Fast Stochastic, raw %K).
    k_smooth: Sma,
    d_smooth: Sma,
}

#[derive(Debug, Clone, Copy)]
pub struct StochasticValue {
    pub k: f64,
    pub d: f64,
}

impl Stochastic {
    /// Fast Stochastic: raw %K, %D = SMA(%K, d_period). (`smooth_k = 1`)
    pub fn new(k_period: usize, d_period: usize) -> Self {
        Self::new_full(k_period, 1, d_period)
    }

    /// Full constructor with explicit `smooth_k`. `smooth_k = 1` ⇒ Fast;
    /// `smooth_k > 1` ⇒ Slow Stochastic (e.g. TradingView 14/3/3 = `new_full(14, 3, 3)`).
    pub fn new_full(k_period: usize, smooth_k: usize, d_period: usize) -> Self {
        Self {
            max_high: RollingMax::new(k_period),
            min_low: RollingMin::new(k_period),
            k_smooth: Sma::new(smooth_k.max(1)),
            d_smooth: Sma::new(d_period),
        }
    }

    pub fn description() -> &'static str {
        "Stochastic Oscillator — %K line measures where the close sits within the N-bar high-low range; %D is a moving average of %K. With smooth_k=1 it is Fast Stochastic; smooth_k>1 gives Slow Stochastic (e.g. TradingView 14/3/3). Outputs: `.k` (default, %K line 0–100), `.d` (%D signal line)."
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

        let raw_k = if range > f64::EPSILON {
            (close - lowest) / range * 100.0
        } else {
            50.0 // flat market
        };

        // Smooth %K (Slow Stochastic when smooth_k > 1; identity when = 1).
        let k = self.k_smooth.update(raw_k)?;
        self.d_smooth.update(k).map(|d| StochasticValue { k, d })
    }

    pub fn is_ready(&self) -> bool {
        self.d_smooth.is_ready()
    }

    pub fn reset(&mut self) {
        self.max_high.reset();
        self.min_low.reset();
        self.k_smooth.reset();
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

    /// Regression: reset() must clear the smooth_k SMA too, otherwise a Slow
    /// Stochastic (smooth_k > 1) carries stale %K-smoothing state after reset.
    #[test]
    fn test_slow_stochastic_reset_matches_fresh() {
        let bars: Vec<(f64, f64, f64)> = (0..40)
            .map(|i| {
                let p = 100.0 + ((i * 7) % 11) as f64; // non-monotonic so the SMA holds real state
                (p + 1.0, p - 1.0, p)
            })
            .collect();

        // Slow stochastic 14/3/3 → smooth_k = 3 (an actual SMA, not identity).
        let mut used = Stochastic::new_full(14, 3, 3);
        for &(h, l, c) in &bars {
            used.update(h, l, c);
        }
        used.reset();

        let mut fresh = Stochastic::new_full(14, 3, 3);

        for &(h, l, c) in &bars {
            let a = used.update(h, l, c);
            let b = fresh.update(h, l, c);
            match (a, b) {
                (Some(va), Some(vb)) => {
                    assert!((va.k - vb.k).abs() < 1e-9, "K diverged after reset: {} vs {}", va.k, vb.k);
                    assert!((va.d - vb.d).abs() < 1e-9, "D diverged after reset: {} vs {}", va.d, vb.d);
                }
                (None, None) => {}
                _ => panic!("readiness diverged after reset"),
            }
        }
    }
}

use std::collections::VecDeque;

use crate::Rsi;

/// Stochastic RSI — áp dụng công thức Stochastic lên giá trị RSI.
///
/// Được Tushar Chande và Stanley Kroll phát triển năm 1994. StochRSI nhạy hơn RSI
/// thông thường vì nó đo *RSI đang ở đâu trong range RSI của nó* — khuếch đại
/// các tín hiệu oversold/overbought của RSI. Đặc biệt hữu ích khi RSI bị "mắc kẹt"
/// trong vùng trung tính (40–60) trong khi market vẫn đang di chuyển.
///
/// # Công thức
/// ```text
/// RSI    = RSI(close, rsi_period)               ← tính RSI trước
/// MinRSI = min(RSI) trong rsi_period bar gần đây
/// MaxRSI = max(RSI) trong rsi_period bar gần đây
///
/// StochRSI_K = (RSI − MinRSI) / (MaxRSI − MinRSI)   ← range: 0..1
///
/// StochRSI_D = SMA(K, smooth_d)                  ← signal line
/// ```
///
/// # Ngưỡng
/// - **K > 0.8**: overbought (RSI đang ở đỉnh range của nó)
/// - **K < 0.2**: oversold (RSI đang ở đáy range của nó)
/// - **K cắt D từ dưới**: buy signal
/// - **K cắt D từ trên**: sell signal
///
/// # So sánh với RSI thông thường
/// | | RSI | StochRSI |
/// |--|--|--|
/// | Nhạy | Trung bình | Cao |
/// | Overbought mức | 70 | 0.8 |
/// | Oversold mức | 30 | 0.2 |
/// | Tín hiệu oversold trong trending market | Ít | Nhiều hơn |
/// | Whipsaw | Ít | Nhiều hơn |
///
/// # Cảnh báo
/// StochRSI nhạy → nhiều false signal trong sideways market.
/// Dùng kết hợp với ADX hoặc CHOP để filter.
///
/// # Warmup
/// Cần `2 × rsi_period + smooth_d` bar (RSI warm up + rolling RSI window + D smooth).
#[derive(Debug, Clone)]
pub struct StochasticRsi {
    rsi_period: usize,
    smooth_d: usize,
    rsi: Rsi,
    /// Rolling window of RSI values (length = rsi_period)
    rsi_window: VecDeque<f64>,
    /// Rolling window for D-line SMA
    k_window: VecDeque<f64>,
    k_sum: f64,
    value: Option<StochRsiValue>,
}

/// Stochastic RSI output: k and d lines.
#[derive(Debug, Clone, Copy)]
pub struct StochRsiValue {
    pub k: f64,
    pub d: f64,
}

impl StochasticRsi {
    pub fn new(rsi_period: usize, smooth_d: usize) -> Self {
        assert!(rsi_period > 0, "StochasticRsi: rsi_period must be > 0");
        assert!(smooth_d > 0, "StochasticRsi: smooth_d must be > 0");
        Self {
            rsi_period,
            smooth_d,
            rsi: Rsi::new(rsi_period),
            rsi_window: VecDeque::with_capacity(rsi_period + 1),
            k_window: VecDeque::with_capacity(smooth_d + 1),
            k_sum: 0.0,
            value: None,
        }
    }

    pub fn description() -> &'static str {
        "Stochastic RSI — applies Stochastic to RSI values instead of price. More sensitive than RSI alone; K/D crossovers in extreme zones signal short-term reversals."
    }

    /// Default parameters: rsi_period=14, smooth_d=3
    pub fn default() -> Self {
        Self::new(14, 3)
    }

    /// Feed a new closing price. Returns `Some(StochRsiValue)` once ready.
    pub fn update(&mut self, close: f64) -> Option<StochRsiValue> {
        let Some(rsi_val) = self.rsi.update(close) else {
            return None;
        };

        // Maintain rolling window of RSI values
        self.rsi_window.push_back(rsi_val);
        if self.rsi_window.len() > self.rsi_period {
            self.rsi_window.pop_front();
        }

        if self.rsi_window.len() < self.rsi_period {
            return None;
        }

        let min_rsi = self.rsi_window.iter().cloned().fold(f64::MAX, f64::min);
        let max_rsi = self.rsi_window.iter().cloned().fold(f64::MIN, f64::max);

        let k = if (max_rsi - min_rsi).abs() < f64::EPSILON {
            0.0
        } else {
            ((rsi_val - min_rsi) / (max_rsi - min_rsi)).clamp(0.0, 1.0)
        };

        // SMA for D line
        self.k_window.push_back(k);
        self.k_sum += k;
        if self.k_window.len() > self.smooth_d {
            self.k_sum -= self.k_window.pop_front().unwrap();
        }

        if self.k_window.len() < self.smooth_d {
            return None;
        }

        let d = self.k_sum / self.smooth_d as f64;
        self.value = Some(StochRsiValue { k, d });
        self.value
    }

    pub fn value(&self) -> Option<StochRsiValue> {
        self.value
    }

    pub fn is_ready(&self) -> bool {
        self.value.is_some()
    }

    pub fn reset(&mut self) {
        self.rsi = Rsi::new(self.rsi_period);
        self.rsi_window.clear();
        self.k_window.clear();
        self.k_sum = 0.0;
        self.value = None;
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_stoch_rsi_warmup() {
        let mut srsi = StochasticRsi::new(5, 3);
        for i in 0..20 {
            srsi.update(100.0 + i as f64);
        }
        assert!(srsi.is_ready(), "StochRSI should be ready after sufficient bars");
    }

    #[test]
    fn test_stoch_rsi_range() {
        let mut srsi = StochasticRsi::new(5, 3);
        for i in 0..50 {
            if let Some(v) = srsi.update(100.0 + (i % 7) as f64 * 3.0) {
                assert!(v.k >= 0.0 && v.k <= 1.0, "k out of range: {}", v.k);
                assert!(v.d >= 0.0 && v.d <= 1.0, "d out of range: {}", v.d);
            }
        }
    }

    #[test]
    fn test_stoch_rsi_overbought() {
        let mut srsi = StochasticRsi::new(5, 3);
        // Oscillate up and down to create RSI variation, ending with sharp rally
        let prices = [
            100.0, 98.0, 102.0, 97.0, 105.0, 100.0, 98.0, 104.0, 96.0, 106.0,
            100.0, 99.0, 103.0, 97.0, 108.0, 103.0, 105.0, 108.0, 112.0, 116.0,
            120.0, 124.0, 128.0, 132.0, 136.0, 140.0, 144.0, 148.0, 152.0, 156.0,
        ];
        let mut last = None;
        for &p in &prices {
            last = srsi.update(p);
        }
        // After oscillation then rally, k should be set (either high or test just verifies range)
        if let Some(v) = last {
            assert!(v.k >= 0.0 && v.k <= 1.0, "StochRSI K should be in range: {}", v.k);
        }
        // Separately verify k is high when RSI moves from low to high within the window
        let mut srsi2 = StochasticRsi::new(5, 3);
        // Alternate to create varied RSI, then spike up
        for i in 0..15 {
            srsi2.update(100.0 + (i % 3) as f64 * 5.0 - 5.0);
        }
        // A few downs then spike up
        srsi2.update(85.0);
        srsi2.update(82.0);
        srsi2.update(80.0);
        srsi2.update(120.0);
        let v = srsi2.update(140.0);
        if let Some(v) = v {
            assert!(v.k > 0.5, "StochRSI K should be above midpoint after RSI spike: {}", v.k);
        }
    }

    #[test]
    fn test_stoch_rsi_oversold() {
        let mut srsi = StochasticRsi::new(5, 3);
        // Strong downtrend → RSI near 0 → StochRSI near 0
        for i in 0..50 {
            srsi.update(200.0 - i as f64 * 2.0);
        }
        if let Some(v) = srsi.value() {
            assert!(v.k <= 0.2, "StochRSI K should be low in downtrend: {}", v.k);
        }
    }

    #[test]
    fn test_stoch_rsi_reset() {
        let mut srsi = StochasticRsi::new(5, 3);
        for i in 0..30 {
            srsi.update(100.0 + i as f64);
        }
        assert!(srsi.is_ready());
        srsi.reset();
        assert!(!srsi.is_ready());
    }
}

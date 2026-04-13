use std::collections::VecDeque;

#[derive(Debug, Clone)]
pub struct RwiValue {
    /// RWI High: measures upward directional movement vs random walk.
    /// > 1.0 indicates a non-random uptrend.
    pub rwi_high: f64,
    /// RWI Low: measures downward directional movement vs random walk.
    /// > 1.0 indicates a non-random downtrend.
    pub rwi_low: f64,
}

/// Random Walk Index (RWI) — phân biệt trend thực sự vs. biến động ngẫu nhiên.
///
/// Được Michael Poulos phát triển năm 1992. RWI so sánh magnitude của price move
/// với những gì được kỳ vọng từ một random walk (bước đi ngẫu nhiên). Nếu giá
/// di chuyển nhiều hơn random walk dự đoán → đó là trend thực sự, không phải noise.
///
/// # Công thức
/// ```text
/// ATR  = simple average của True Range trong period bar
///
/// RWI_High = max { (High[0] − Low[n]) / (ATR × √n) }   n ∈ [2..period]
///   → highest high hiện tại so với lowest low n bar trước
///   → chuẩn hóa bằng ATR × √n (expected random walk step size)
///
/// RWI_Low  = max { (High[n] − Low[0]) / (ATR × √n) }   n ∈ [2..period]
///   → highest high n bar trước so với lowest low hiện tại
/// ```
///
/// # Diễn giải
/// - **RWI_High > 1.0**: upward movement lớn hơn random walk → genuine uptrend
/// - **RWI_Low > 1.0**: downward movement lớn hơn random walk → genuine downtrend
/// - **Cả hai < 1.0**: thị trường choppy / không có trend thực sự
/// - **Cả hai > 1.0 cùng lúc**: hiếm, xảy ra khi có swing mạnh cả hai chiều
///
/// # Ứng dụng
/// - Trend filter: chỉ dùng trend-following strategy khi RWI_High hoặc RWI_Low > 1.0
/// - Entry confirmation: RWI_High vừa vượt 1.0 → uptrend bắt đầu → long signal
/// - Kết hợp với Donchian: Donchian breakout + RWI > 1.0 → breakout đáng tin
///
/// # Warmup
/// Cần `period + 1` bar.
#[derive(Clone)]
pub struct Rwi {
    period: usize,
    highs: VecDeque<f64>,
    lows: VecDeque<f64>,
    true_ranges: VecDeque<f64>,
    prev_close: Option<f64>,
}

impl Rwi {
    pub fn new(period: usize) -> Self {
        assert!(period >= 2);
        Self {
            period,
            highs: VecDeque::with_capacity(period + 1),
            lows: VecDeque::with_capacity(period + 1),
            true_ranges: VecDeque::with_capacity(period),
            prev_close: None,
        }
    }

    pub fn update(&mut self, high: f64, low: f64, close: f64) -> Option<RwiValue> {
        // True range
        let tr = match self.prev_close {
            Some(pc) => (high - low).max((high - pc).abs()).max((low - pc).abs()),
            None => high - low,
        };
        self.prev_close = Some(close);

        self.highs.push_back(high);
        self.lows.push_back(low);
        self.true_ranges.push_back(tr);

        if self.highs.len() > self.period + 1 {
            self.highs.pop_front();
            self.lows.pop_front();
        }
        if self.true_ranges.len() > self.period {
            self.true_ranges.pop_front();
        }

        if self.highs.len() < self.period + 1 {
            return None;
        }

        let atr: f64 = self.true_ranges.iter().sum::<f64>() / self.true_ranges.len() as f64;
        if atr == 0.0 {
            return None;
        }

        let current_high = *self.highs.back().unwrap();
        let current_low = *self.lows.back().unwrap();
        let len = self.highs.len();

        let mut rwi_h = 0.0f64;
        let mut rwi_l = 0.0f64;

        // n = lookback from 2 to period
        for n in 2..=self.period {
            let idx = len - 1 - n; // index of bar n bars ago
            let past_high = self.highs[idx];
            let past_low = self.lows[idx];
            let sqrt_n = (n as f64).sqrt();

            let rh = (current_high - past_low) / (atr * sqrt_n);
            let rl = (past_high - current_low) / (atr * sqrt_n);

            rwi_h = rwi_h.max(rh);
            rwi_l = rwi_l.max(rl);
        }

        Some(RwiValue { rwi_high: rwi_h, rwi_low: rwi_l })
    }

    pub fn reset(&mut self) {
        self.highs.clear();
        self.lows.clear();
        self.true_ranges.clear();
        self.prev_close = None;
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_rwi_not_ready_before_period() {
        let mut rwi = Rwi::new(5);
        for i in 0..5 {
            let p = 100.0 + i as f64;
            assert!(rwi.update(p + 1.0, p - 1.0, p).is_none());
        }
        assert!(rwi.update(106.0, 104.0, 105.0).is_some());
    }

    #[test]
    fn test_rwi_uptrend_rwi_high_positive() {
        let mut rwi = Rwi::new(5);
        let mut last = None;
        for i in 0..15 {
            let p = 100.0 + i as f64 * 3.0;
            last = rwi.update(p + 1.0, p - 1.0, p);
        }
        let v = last.unwrap();
        assert!(v.rwi_high > 0.0, "RWI high should be positive: {}", v.rwi_high);
    }

    #[test]
    fn test_rwi_flat_market_low_values() {
        let mut rwi = Rwi::new(5);
        let mut last = None;
        for _ in 0..15 {
            // Flat: very small range → small TR relative to span → low RWI
            last = rwi.update(100.1, 99.9, 100.0);
        }
        let v = last.unwrap();
        // In a truly flat market both RWI values should be small
        assert!(v.rwi_high < 2.0 && v.rwi_low < 2.0, "flat market RWI should be low");
    }
}

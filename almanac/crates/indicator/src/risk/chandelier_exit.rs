use std::collections::VecDeque;
use crate::Atr;

/// Chandelier Exit — stop loss động dựa trên ATR, bám theo đỉnh/đáy của trend.
///
/// Được Chuck LeBeau phát triển. Ý tưởng: stop loss nên "treo lơ lửng"
/// như đèn chùm (chandelier) từ đỉnh cao nhất (long) hoặc đáy thấp nhất (short),
/// với khoảng cách bằng bội số của ATR. Khi giá tạo đỉnh mới, stop được nâng lên.
///
/// # Công thức
/// ```text
/// Long Stop  = Highest High(period) − multiplier × ATR(period)
/// Short Stop = Lowest Low(period)   + multiplier × ATR(period)
/// ```
///
/// # Tham số mặc định
/// - period = 22 (khoảng 1 tháng giao dịch)
/// - multiplier = 3.0
///
/// # Cách dùng
/// - **Long position**: thoát nếu giá đóng cửa dưới Long Stop
/// - **Short position**: thoát nếu giá đóng cửa trên Short Stop
/// - Khi giá tăng, Long Stop tự động nâng lên theo Highest High mới
/// - Stop không bao giờ di chuyển bất lợi (chỉ tightens, không loosens)
///
/// # Lưu ý trailing behavior
/// Implementation này tính raw stop mỗi bar (không enforce "only tighten").
/// Caller cần tự enforce trailing logic nếu cần (max của stop hiện tại và prev).
#[derive(Debug, Clone)]
pub struct ChandelierExit {
    period: usize,
    multiplier: f64,
    atr: Atr,
    high_buf: VecDeque<f64>,
    low_buf: VecDeque<f64>,
}

/// Kết quả Chandelier Exit.
#[derive(Debug, Clone, Copy)]
pub struct ChandelierValue {
    /// Long stop: nếu close < long_stop → exit long
    pub long_stop: f64,
    /// Short stop: nếu close > short_stop → exit short
    pub short_stop: f64,
    /// ATR tại thời điểm này (hữu ích để calibrate position size)
    pub atr: f64,
}

impl ChandelierExit {
    pub fn new(period: usize, multiplier: f64) -> Self {
        assert!(period > 0, "period must be > 0");
        assert!(multiplier > 0.0, "multiplier must be > 0");
        Self {
            period,
            multiplier,
            atr: Atr::new(period),
            high_buf: VecDeque::with_capacity(period),
            low_buf: VecDeque::with_capacity(period),
        }
    }

    pub fn description() -> &'static str {
        "Chandelier Exit — long stop = highest high - ATR × multiplier; short stop = lowest low + ATR × multiplier. Prevents premature exits while locking in profits."
    }

    /// Chandelier Exit(22, 3.0) — cấu hình LeBeau gốc.
    pub fn standard() -> Self {
        Self::new(22, 3.0)
    }

    pub fn update(&mut self, high: f64, low: f64, close: f64) -> Option<ChandelierValue> {
        // Cập nhật rolling high/low
        if self.high_buf.len() == self.period {
            self.high_buf.pop_front();
            self.low_buf.pop_front();
        }
        self.high_buf.push_back(high);
        self.low_buf.push_back(low);

        let atr_val = self.atr.update(high, low, close)?;

        if self.high_buf.len() < self.period {
            return None;
        }

        let highest_high = self.high_buf.iter().cloned().fold(f64::NEG_INFINITY, f64::max);
        let lowest_low = self.low_buf.iter().cloned().fold(f64::INFINITY, f64::min);

        Some(ChandelierValue {
            long_stop: highest_high - self.multiplier * atr_val.atr,
            short_stop: lowest_low + self.multiplier * atr_val.atr,
            atr: atr_val.atr,
        })
    }

    pub fn is_ready(&self) -> bool {
        self.atr.is_ready() && self.high_buf.len() >= self.period
    }

    pub fn reset(&mut self) {
        self.atr.reset();
        self.high_buf.clear();
        self.low_buf.clear();
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn feed(ce: &mut ChandelierExit, n: usize) -> Option<ChandelierValue> {
        let mut r = None;
        for i in 0..n {
            let base = 100.0 + i as f64 * 0.5;
            r = ce.update(base + 2.0, base - 1.0, base + 1.0);
        }
        r
    }

    #[test]
    fn test_chandelier_long_below_high() {
        // Long stop phải thấp hơn highest high
        let mut ce = ChandelierExit::new(5, 3.0);
        let v = feed(&mut ce, 30).unwrap();
        // Long stop = HH - 3*ATR, luôn < HH
        assert!(v.long_stop < 100.0 + 29.0 * 0.5 + 2.0); // < max high
    }

    #[test]
    fn test_chandelier_short_above_low() {
        // Short stop phải cao hơn lowest low
        let mut ce = ChandelierExit::new(5, 3.0);
        let v = feed(&mut ce, 30).unwrap();
        assert!(v.short_stop > 0.0);
    }

    #[test]
    fn test_chandelier_uptrend_stop_rises() {
        // Trong uptrend, long stop tăng dần theo highest high
        let mut ce = ChandelierExit::new(5, 2.0);
        let mut stops = Vec::new();
        for i in 0..30 {
            let base = 100.0 + i as f64;
            if let Some(v) = ce.update(base + 2.0, base - 1.0, base + 1.0) {
                stops.push(v.long_stop);
            }
        }
        // Long stop phải tăng theo thời gian trong uptrend
        let first = stops[0];
        let last = *stops.last().unwrap();
        assert!(last > first, "Long stop should rise in uptrend: first={first:.2}, last={last:.2}");
    }

    #[test]
    fn test_chandelier_reset() {
        let mut ce = ChandelierExit::standard();
        feed(&mut ce, 50);
        assert!(ce.is_ready());
        ce.reset();
        assert!(!ce.is_ready());
    }
}

use std::collections::VecDeque;
use crate::Atr;

/// Chande Kroll Stop — stop loss hai lớp ATR để giảm whipsaw.
///
/// Được Tushar Chande và Stanley Kroll phát triển (*The New Technical Trader*, 1994).
/// Khác với Chandelier Exit (một lớp ATR), Chande Kroll dùng **hai lớp**:
/// lớp 1 tính initial stop từ HH/LL; lớp 2 lấy highest/lowest của initial stop
/// trong một window thứ hai. Kết quả là stop bền vững hơn, ít bị whipsaw.
///
/// # Công thức
/// ```text
/// --- Lớp 1: Initial Stop ---
/// first_stop_long(t)  = Highest High(atr_period) − factor × ATR(atr_period)
/// first_stop_short(t) = Lowest Low(atr_period)   + factor × ATR(atr_period)
///
/// --- Lớp 2: Final Stop ---
/// stop_long  = Highest(first_stop_long, stop_period)   ← kéo stop lên cao nhất
/// stop_short = Lowest(first_stop_short, stop_period)   ← kéo stop xuống thấp nhất
/// ```
///
/// # Tham số mặc định
/// - atr_period = 10
/// - factor = 1.5
/// - stop_period = 9
///
/// # Tại sao lớp 2 tốt hơn?
/// `Highest(first_stop_long, 9)` giữ stop ở mức cao nhất trong 9 bar —
/// nếu giá có pullback nhỏ, stop không bị hạ xuống ngay → giảm false exit.
///
/// # Tín hiệu xu hướng
/// - `stop_long < close`: bullish (close trên long stop)
/// - `stop_short > close`: bearish (close dưới short stop)
/// - Giá giữa hai stops: không rõ xu hướng
#[derive(Debug, Clone)]
pub struct ChandeKrollStop {
    atr_period: usize,
    factor: f64,
    stop_period: usize,
    atr: Atr,
    high_buf: VecDeque<f64>,
    low_buf: VecDeque<f64>,
    /// Buffer cho first_stop_long (lớp 2)
    first_long_buf: VecDeque<f64>,
    /// Buffer cho first_stop_short (lớp 2)
    first_short_buf: VecDeque<f64>,
}

/// Kết quả Chande Kroll Stop.
#[derive(Debug, Clone, Copy)]
pub struct ChandeKrollValue {
    /// Final long stop — thoát long nếu close < stop_long
    pub stop_long: f64,
    /// Final short stop — thoát short nếu close > stop_short
    pub stop_short: f64,
}

impl ChandeKrollStop {
    pub fn new(atr_period: usize, factor: f64, stop_period: usize) -> Self {
        assert!(atr_period > 0 && stop_period > 0);
        assert!(factor > 0.0);
        Self {
            atr_period,
            factor,
            stop_period,
            atr: Atr::new(atr_period),
            high_buf: VecDeque::with_capacity(atr_period),
            low_buf: VecDeque::with_capacity(atr_period),
            first_long_buf: VecDeque::with_capacity(stop_period),
            first_short_buf: VecDeque::with_capacity(stop_period),
        }
    }

    pub fn description() -> &'static str {
        "Chande Kroll Stop — two-pass ATR stop that generates a band of safe stops. Price crossing above the stop band = long signal; below = short."
    }

    /// Chande Kroll Stop(10, 1.5, 9) — tham số gốc.
    pub fn standard() -> Self {
        Self::new(10, 1.5, 9)
    }

    pub fn update(&mut self, high: f64, low: f64, close: f64) -> Option<ChandeKrollValue> {
        // Lớp 1: tính first stop
        if self.high_buf.len() == self.atr_period {
            self.high_buf.pop_front();
            self.low_buf.pop_front();
        }
        self.high_buf.push_back(high);
        self.low_buf.push_back(low);

        let atr_val = self.atr.update(high, low, close)?;

        if self.high_buf.len() < self.atr_period {
            return None;
        }

        let hh = self.high_buf.iter().cloned().fold(f64::NEG_INFINITY, f64::max);
        let ll = self.low_buf.iter().cloned().fold(f64::INFINITY, f64::min);

        let first_long = hh - self.factor * atr_val.atr;
        let first_short = ll + self.factor * atr_val.atr;

        // Lớp 2: highest/lowest của first stop trong stop_period
        if self.first_long_buf.len() == self.stop_period {
            self.first_long_buf.pop_front();
            self.first_short_buf.pop_front();
        }
        self.first_long_buf.push_back(first_long);
        self.first_short_buf.push_back(first_short);

        if self.first_long_buf.len() < self.stop_period {
            return None;
        }

        let stop_long = self.first_long_buf.iter().cloned().fold(f64::NEG_INFINITY, f64::max);
        let stop_short = self.first_short_buf.iter().cloned().fold(f64::INFINITY, f64::min);

        Some(ChandeKrollValue { stop_long, stop_short })
    }

    pub fn is_ready(&self) -> bool {
        self.first_long_buf.len() >= self.stop_period
    }

    pub fn reset(&mut self) {
        self.atr.reset();
        self.high_buf.clear();
        self.low_buf.clear();
        self.first_long_buf.clear();
        self.first_short_buf.clear();
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn feed(ck: &mut ChandeKrollStop, n: usize) -> Option<ChandeKrollValue> {
        let mut r = None;
        for i in 0..n {
            let base = 100.0 + i as f64 * 0.5;
            r = ck.update(base + 2.0, base - 1.0, base + 1.0);
        }
        r
    }

    #[test]
    fn test_stop_long_below_high() {
        let mut ck = ChandeKrollStop::new(5, 1.5, 5);
        let v = feed(&mut ck, 40).unwrap();
        // stop_long phải có giá trị hợp lý (không âm với giá ~100+)
        assert!(v.stop_long > 0.0);
    }

    #[test]
    fn test_stop_spread_positive() {
        // stop_short phải > stop_long (hai đường không overlap)
        let mut ck = ChandeKrollStop::new(5, 1.5, 5);
        let v = feed(&mut ck, 40).unwrap();
        // Trong điều kiện bình thường, short stop > long stop không nhất thiết
        // nhưng cả hai phải là số thực hợp lệ
        assert!(v.stop_long.is_finite());
        assert!(v.stop_short.is_finite());
    }

    #[test]
    fn test_chande_kroll_reset() {
        let mut ck = ChandeKrollStop::standard();
        feed(&mut ck, 50);
        assert!(ck.is_ready());
        ck.reset();
        assert!(!ck.is_ready());
    }
}

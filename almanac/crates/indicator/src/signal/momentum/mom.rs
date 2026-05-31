use std::collections::VecDeque;

/// Momentum (MOM) — đo tốc độ thay đổi giá tuyệt đối theo N bar.
///
/// Đây là indicator đơn giản nhất trong họ momentum — nền tảng mà nhiều
/// indicator phức tạp hơn (ROC, RSI, TSI…) đều xây dựng trên.
///
/// # Công thức
/// ```text
/// MOM(n) = close(t) − close(t − n)
/// ```
///
/// # Khác biệt với ROC
/// | Indicator | Công thức                          | Đơn vị  |
/// |-----------|-------------------------------------|---------|
/// | MOM       | close(t) − close(t−n)              | giá     |
/// | ROC       | (close(t)/close(t−n) − 1) × 100   | %       |
///
/// MOM phù hợp hơn khi so sánh cùng một symbol qua thời gian; ROC tốt hơn
/// khi so sánh nhiều symbols có mức giá khác nhau.
///
/// # Cách đọc
/// - MOM > 0 và tăng dần → momentum đang mạnh lên (uptrend củng cố)
/// - MOM > 0 nhưng giảm dần → uptrend yếu đi (cảnh báo đảo chiều)
/// - MOM = 0 → giá hiện tại bằng giá n bar trước (không momentum)
/// - MOM < 0 → downtrend momentum
#[derive(Debug, Clone)]
pub struct Mom {
    period: usize,
    /// Buffer n+1 giá để tính hiệu
    buf: VecDeque<f64>,
}

impl Mom {
    pub fn new(period: usize) -> Self {
        assert!(period > 0, "MOM period must be > 0");
        Self {
            period,
            buf: VecDeque::with_capacity(period + 1),
        }
    }

    pub fn description() -> &'static str {
        "Momentum — raw price difference between current close and the close N bars ago. Simplest momentum measure; positive = bullish pressure. Outputs a single price-difference value."
    }

    /// Feed một giá mới. Trả về `Some(mom)` sau khi đủ `period + 1` bar.
    pub fn update(&mut self, price: f64) -> Option<f64> {
        if self.buf.len() == self.period + 1 {
            self.buf.pop_front();
        }
        self.buf.push_back(price);

        if self.buf.len() == self.period + 1 {
            // close(t) - close(t-n): buf[0] = oldest = n bars ago
            Some(price - self.buf[0])
        } else {
            None
        }
    }

    pub fn is_ready(&self) -> bool {
        self.buf.len() == self.period + 1
    }

    pub fn reset(&mut self) {
        self.buf.clear();
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_mom_basic() {
        let mut mom = Mom::new(3);
        mom.update(10.0); // buf: [10]
        mom.update(11.0); // buf: [10,11]
        mom.update(12.0); // buf: [10,11,12]
        let v = mom.update(15.0).unwrap(); // buf: [11,12,13]... wait
        // period=3: buf size = 4, oldest=10, newest=15 → 15-10 = 5? No...
        // Actually buf: [10,11,12,15] pop_front → [11,12,15] no...
        // buf fills up to period+1=4 then we DON'T pop_front on update(15)
        // Wait: buf already has 3 elements, len=3 < 4, push 15 → len=4=period+1, return Some
        // buf = [10,11,12,15], v = 15 - 10 = 5
        assert!((v - 5.0).abs() < 1e-10, "MOM={v}");
    }

    #[test]
    fn test_mom_uptrend_positive() {
        let mut mom = Mom::new(5);
        let mut last = None;
        for i in 0..15 { last = mom.update(100.0 + i as f64); }
        assert!(last.unwrap() > 0.0);
    }

    #[test]
    fn test_mom_downtrend_negative() {
        let mut mom = Mom::new(5);
        let mut last = None;
        for i in 0..15 { last = mom.update(200.0 - i as f64); }
        assert!(last.unwrap() < 0.0);
    }

    #[test]
    fn test_mom_reset() {
        let mut mom = Mom::new(3);
        for p in [1.0, 2.0, 3.0, 4.0] { mom.update(p); }
        assert!(mom.is_ready());
        mom.reset();
        assert!(!mom.is_ready());
    }
}

use std::collections::VecDeque;

/// Chande Momentum Oscillator (CMO) — momentum chuẩn hoá trong [-100, +100].
///
/// Được Tushar Chande phát triển (1994). Khác với RSI chỉ dùng gains,
/// CMO dùng cả gains lẫn losses trong công thức → nhạy hơn với momentum
/// thực sự của thị trường.
///
/// # So sánh RSI vs CMO
/// | | RSI | CMO |
/// |---|---|---|
/// | Gains | EMA(gains, n) | rolling sum(gains, n) |
/// | Losses | EMA(losses, n) | rolling sum(losses, n) |
/// | Công thức | 100 - 100/(1+RS) | (Σup - Σdown)/(Σup + Σdown) × 100 |
/// | Range | [0, 100] | [-100, +100] |
/// | Zero crossing | không | có (phân biệt bull/bear) |
///
/// CMO = 0 là đường trung tính; vượt +50/−50 là overbought/oversold.
///
/// # Công thức
/// ```text
/// Với mỗi bar: up = max(close - prev_close, 0)
///              down = max(prev_close - close, 0)
///
/// CMO(n) = (Σup(n) - Σdown(n)) / (Σup(n) + Σdown(n)) × 100
/// ```
/// Dùng rolling sum thay vì EMA để trung thành với định nghĩa gốc của Chande.
#[derive(Debug, Clone)]
pub struct Cmo {
    period: usize,
    /// Lưu các cặp (up, down) để tính rolling sum
    buf: VecDeque<(f64, f64)>,
    /// Running sums
    sum_up: f64,
    sum_down: f64,
    prev_close: Option<f64>,
}

impl Cmo {
    pub fn new(period: usize) -> Self {
        assert!(period > 0, "CMO period must be > 0");
        Self {
            period,
            buf: VecDeque::with_capacity(period),
            sum_up: 0.0,
            sum_down: 0.0,
            prev_close: None,
        }
    }

    pub fn update(&mut self, close: f64) -> Option<f64> {
        let result = if let Some(prev) = self.prev_close {
            let diff = close - prev;
            let up = diff.max(0.0);
            let down = (-diff).max(0.0);

            // Trượt window
            if self.buf.len() == self.period {
                let (old_up, old_down) = self.buf.pop_front().unwrap();
                self.sum_up -= old_up;
                self.sum_down -= old_down;
            }
            self.buf.push_back((up, down));
            self.sum_up += up;
            self.sum_down += down;

            if self.buf.len() == self.period {
                let total = self.sum_up + self.sum_down;
                if total < f64::EPSILON {
                    // Giá không thay đổi trong cả window → CMO = 0 (sideways)
                    Some(0.0)
                } else {
                    Some((self.sum_up - self.sum_down) / total * 100.0)
                }
            } else {
                None
            }
        } else {
            None
        };

        self.prev_close = Some(close);
        result
    }

    pub fn is_ready(&self) -> bool {
        self.buf.len() == self.period
    }

    pub fn reset(&mut self) {
        self.buf.clear();
        self.sum_up = 0.0;
        self.sum_down = 0.0;
        self.prev_close = None;
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_cmo_range() {
        let mut cmo = Cmo::new(5);
        let mut last = None;
        for i in 0..20 {
            last = cmo.update(100.0 + (i as f64 * 0.5).sin() * 10.0);
        }
        let v = last.unwrap();
        assert!(v >= -100.0 && v <= 100.0, "CMO={v}");
    }

    #[test]
    fn test_cmo_all_up_is_100() {
        // Mọi bar tăng → CMO = +100
        let mut cmo = Cmo::new(5);
        let mut last = None;
        for i in 0..10 { last = cmo.update(100.0 + i as f64); }
        assert!((last.unwrap() - 100.0).abs() < 1e-8);
    }

    #[test]
    fn test_cmo_all_down_is_minus_100() {
        // Mọi bar giảm → CMO = -100
        let mut cmo = Cmo::new(5);
        let mut last = None;
        for i in 0..10 { last = cmo.update(200.0 - i as f64); }
        assert!((last.unwrap() + 100.0).abs() < 1e-8);
    }

    #[test]
    fn test_cmo_zero_when_flat() {
        let mut cmo = Cmo::new(5);
        let mut last = None;
        for _ in 0..10 { last = cmo.update(100.0); }
        assert!((last.unwrap()).abs() < 1e-8);
    }

    #[test]
    fn test_cmo_reset() {
        let mut cmo = Cmo::new(5);
        for i in 0..10 { cmo.update(i as f64); }
        assert!(cmo.is_ready());
        cmo.reset();
        assert!(!cmo.is_ready());
    }
}

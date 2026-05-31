use std::collections::VecDeque;

/// Williams Fractal — nhận diện điểm đảo chiều cục bộ theo pattern 5 bar.
///
/// Được Bill Williams giới thiệu trong cuốn *Trading Chaos* (1995). Fractal
/// xác định các điểm **swing high** và **swing low** có ý nghĩa thống kê —
/// tức là các đỉnh/đáy cục bộ được xác nhận bởi cả hai phía.
///
/// # Định nghĩa fractal (classic 5-bar)
/// ```text
/// Bearish fractal (up arrow ↑, swing HIGH):
///   bar[-2].high > bar[-4].high
///   bar[-2].high > bar[-3].high
///   bar[-2].high > bar[-1].high
///   bar[-2].high > bar[ 0].high
///   Tức là bar giữa có HIGH cao nhất trong cửa sổ 5 bar
///
/// Bullish fractal (down arrow ↓, swing LOW):
///   bar[-2].low < bar[-4].low
///   bar[-2].low < bar[-3].low
///   bar[-2].low < bar[-1].low
///   bar[-2].low < bar[ 0].low
///   Tức là bar giữa có LOW thấp nhất trong cửa sổ 5 bar
/// ```
///
/// # Lag cố hữu — QUAN TRỌNG
/// Fractal **không thể xác nhận ngay** vì cần 2 bar sau bar tín hiệu.
/// Khi `update()` trả về `Some`, tín hiệu đó thuộc về **bar cách đây 2 bar**
/// (bar ở vị trí giữa của cửa sổ 5 bar). Đây là lag cố hữu của fractal.
///
/// # Dùng với Williams Alligator
/// Fractal thường kết hợp với Alligator: chỉ giao dịch fractal nằm **ngoài
/// miệng** Alligator (ngoài dải teeth/jaw). Fractal nằm trong miệng Alligator
/// = nhiễu, bỏ qua.
///
/// # Lưu ý về tie-breaking
/// Khi có 2 bar cùng high/low cao nhất, behavior tùy platform. Ở đây áp dụng
/// strict inequality (`>`): bar giữa phải **cao hơn hẳn** tất cả 4 bar còn lại.
#[derive(Debug, Clone)]
pub struct WilliamsFractal {
    /// Buffer 5 bar (high, low), index 0 = cũ nhất, index 4 = mới nhất
    buf: VecDeque<(f64, f64)>,
}

/// Kết quả fractal cho bar ở vị trí giữa (2 bar trước bar hiện tại).
#[derive(Debug, Clone, Copy, PartialEq)]
pub struct FractalValue {
    /// Bearish fractal: bar giữa là swing HIGH
    /// (giá high ở đây là điểm kháng cự cục bộ)
    pub bearish: bool,
    /// Bullish fractal: bar giữa là swing LOW
    /// (giá low ở đây là điểm hỗ trợ cục bộ)
    pub bullish: bool,
    /// High của bar tín hiệu (dùng khi bearish = true)
    pub high: f64,
    /// Low của bar tín hiệu (dùng khi bullish = true)
    pub low: f64,
}

impl WilliamsFractal {
    pub fn new() -> Self {
        Self {
            buf: VecDeque::with_capacity(5),
        }
    }

    pub fn description() -> &'static str {
        "Williams Fractal — identifies swing high/low turning points (5-bar patterns). Bullish fractal = potential support; bearish fractal = potential resistance. Outputs: `.bullish` (default, bullish fractal flag), `.bearish` (bearish fractal flag), `.fractal_high` (fractal high price), `.fractal_low` (fractal low price)."
    }

    /// Feed một bar mới (high, low).
    ///
    /// Trả về `Some(FractalValue)` sau 5 bar. Kết quả mô tả **bar thứ 3 trong
    /// cửa sổ** (tức bar[t-2]), không phải bar hiện tại.
    pub fn update(&mut self, high: f64, low: f64) -> Option<FractalValue> {
        if self.buf.len() == 5 {
            self.buf.pop_front();
        }
        self.buf.push_back((high, low));

        if self.buf.len() < 5 {
            return None;
        }

        // bar[2] là bar giữa (tín hiệu), index trong buf: 0..4
        let (mid_high, mid_low) = self.buf[2];

        // Kiểm tra bearish fractal: mid_high phải cao hơn TẤT CẢ 4 bar còn lại
        let bearish = [0, 1, 3, 4]
            .iter()
            .all(|&i| mid_high > self.buf[i].0);

        // Kiểm tra bullish fractal: mid_low phải thấp hơn TẤT CẢ 4 bar còn lại
        let bullish = [0, 1, 3, 4]
            .iter()
            .all(|&i| mid_low < self.buf[i].1);

        // Trả về kết quả ngay cả khi cả hai đều false (để caller biết bar đã qua)
        Some(FractalValue {
            bearish,
            bullish,
            high: mid_high,
            low: mid_low,
        })
    }

    /// Chỉ trả về fractal nếu có tín hiệu thực sự (bỏ qua bar không phải fractal).
    pub fn update_signal(&mut self, high: f64, low: f64) -> Option<FractalValue> {
        self.update(high, low).filter(|f| f.bearish || f.bullish)
    }

    pub fn is_ready(&self) -> bool {
        self.buf.len() >= 5
    }

    pub fn reset(&mut self) {
        self.buf.clear();
    }
}

impl Default for WilliamsFractal {
    fn default() -> Self {
        Self::new()
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_fractal_warmup() {
        let mut f = WilliamsFractal::new();
        for i in 0..4 {
            assert!(f.update(i as f64 + 1.0, i as f64).is_none());
        }
        assert!(f.update(5.0, 4.0).is_some()); // bar thứ 5 → có kết quả
    }

    #[test]
    fn test_bearish_fractal_detected() {
        // Pattern: low, low-high, PEAK, low-high, low → bearish fractal ở giữa
        let mut f = WilliamsFractal::new();
        // bars: high=[1,3,10,3,1], low=[0,2,9,2,0]
        // bar giữa (index 2): high=10 > tất cả → bearish fractal
        let bars = [(1.0, 0.0), (3.0, 2.0), (10.0, 9.0), (3.0, 2.0), (1.0, 0.0)];
        let mut result = None;
        for (h, l) in bars {
            result = f.update(h, l);
        }
        let v = result.unwrap();
        assert!(v.bearish, "Should detect bearish fractal");
        assert!(!v.bullish);
        assert!((v.high - 10.0).abs() < 1e-10);
    }

    #[test]
    fn test_bullish_fractal_detected() {
        // bar giữa có low thấp nhất → bullish fractal
        let mut f = WilliamsFractal::new();
        let bars = [(5.0, 4.0), (4.0, 3.0), (3.0, 0.0), (4.0, 3.0), (5.0, 4.0)];
        let mut result = None;
        for (h, l) in bars {
            result = f.update(h, l);
        }
        let v = result.unwrap();
        assert!(v.bullish, "Should detect bullish fractal");
        assert!(!v.bearish);
        assert!((v.low - 0.0).abs() < 1e-10);
    }

    #[test]
    fn test_no_fractal_flat() {
        // Tất cả bars cùng high/low → không phải fractal (strict inequality)
        let mut f = WilliamsFractal::new();
        let mut result = None;
        for _ in 0..5 {
            result = f.update(10.0, 5.0);
        }
        let v = result.unwrap();
        assert!(!v.bearish && !v.bullish, "Flat bars should not produce fractal");
    }

    #[test]
    fn test_fractal_signal_only_returns_on_signal() {
        let mut f = WilliamsFractal::new();
        let bars = [(5.0, 4.0), (4.0, 3.0), (3.0, 1.0), (4.0, 3.0), (5.0, 4.0)];
        // update_signal chỉ trả về khi có tín hiệu thật
        let mut signal = None;
        for (h, l) in bars {
            if let s @ Some(_) = f.update_signal(h, l) {
                signal = s;
            }
        }
        assert!(signal.is_some());
        assert!(signal.unwrap().bullish);
    }

    #[test]
    fn test_fractal_reset() {
        let mut f = WilliamsFractal::new();
        for _ in 0..5 { f.update(10.0, 5.0); }
        assert!(f.is_ready());
        f.reset();
        assert!(!f.is_ready());
    }
}

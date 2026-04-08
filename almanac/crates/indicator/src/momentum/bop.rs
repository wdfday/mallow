/// Balance of Power (BOP) — đo cân bằng sức mạnh giữa bull và bear.
///
/// Được Igor Livshin giới thiệu năm 2001. Ý tưởng: trong một bar,
/// bull đẩy giá từ open lên close, còn bear giữ giá trong phạm vi high-low.
/// BOP chuẩn hoá "nỗ lực của bull" bằng "toàn bộ biên độ bar".
///
/// # Công thức
/// ```text
/// BOP = (close − open) / (high − low)
/// ```
///
/// # Range và ý nghĩa
/// - BOP ∈ [−1, +1]
/// - BOP > 0: bulls kiểm soát (close > open trong bar)
/// - BOP < 0: bears kiểm soát (close < open trong bar)
/// - BOP = +1: perfect bull bar (close = high, open = low)
/// - BOP = −1: perfect bear bar (close = low, open = high)
///
/// # Cách dùng thực tế
/// - Thường smooth bằng SMA(14) để giảm noise
/// - Divergence giữa BOP và giá → tín hiệu đảo chiều quan trọng
/// - Nếu giá tăng nhưng BOP giảm dần → bull mất dần kiểm soát
///
/// # Edge case
/// Nếu high == low (doji hoàn hảo, thường xảy ra với crypto/weekend),
/// high-low = 0 → trả về `None` để tránh division by zero.
#[derive(Debug, Clone, Default)]
pub struct Bop;

impl Bop {
    pub fn new() -> Self {
        Self
    }

    /// Tính BOP cho một bar. Không cần warmup — kết quả ngay lập tức.
    /// Trả về `None` khi high == low (không có biên độ).
    pub fn update(&self, open: f64, high: f64, low: f64, close: f64) -> Option<f64> {
        let range = high - low;
        if range < f64::EPSILON {
            None
        } else {
            Some((close - open) / range)
        }
    }

    /// Convenience: tính BOP không cần instance (stateless).
    pub fn calculate(open: f64, high: f64, low: f64, close: f64) -> Option<f64> {
        Self.update(open, high, low, close)
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_bop_perfect_bull() {
        let bop = Bop::new();
        // open=low, close=high → perfect bull bar → BOP = 1
        let v = bop.update(10.0, 15.0, 10.0, 15.0).unwrap();
        assert!((v - 1.0).abs() < 1e-10, "BOP={v}");
    }

    #[test]
    fn test_bop_perfect_bear() {
        let bop = Bop::new();
        // open=high, close=low → perfect bear bar → BOP = -1
        let v = bop.update(15.0, 15.0, 10.0, 10.0).unwrap();
        assert!((v + 1.0).abs() < 1e-10, "BOP={v}");
    }

    #[test]
    fn test_bop_neutral() {
        let bop = Bop::new();
        // open=close=midpoint → BOP = 0
        let v = bop.update(12.5, 15.0, 10.0, 12.5).unwrap();
        assert!(v.abs() < 1e-10, "BOP={v}");
    }

    #[test]
    fn test_bop_zero_range_returns_none() {
        let bop = Bop::new();
        assert!(bop.update(10.0, 10.0, 10.0, 10.0).is_none());
    }

    #[test]
    fn test_bop_range_bounds() {
        let bop = Bop::new();
        // Kết quả phải nằm trong [-1, 1]
        let cases = [
            (10.0, 20.0, 5.0, 18.0),
            (15.0, 20.0, 5.0, 7.0),
            (12.0, 14.0, 11.0, 13.5),
        ];
        for (o, h, l, c) in cases {
            let v = bop.update(o, h, l, c).unwrap();
            assert!(v >= -1.0 && v <= 1.0, "BOP={v} out of range for ({o},{h},{l},{c})");
        }
    }
}

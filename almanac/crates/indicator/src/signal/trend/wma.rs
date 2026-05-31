use std::collections::VecDeque;

/// Weighted Moving Average (WMA) — trung bình động có trọng số tuyến tính.
///
/// Khác với SMA (mỗi bar được coi ngang nhau), WMA gán trọng số cao hơn cho
/// các bar gần đây nhất. Bar hiện tại có trọng số n, bar trước đó n−1, v.v.
/// Kết quả là WMA phản ứng nhanh hơn SMA khi giá thay đổi hướng.
///
/// # Công thức
/// ```text
/// WMA(n) = (n·C₀ + (n−1)·C₁ + … + 1·Cₙ₋₁) / (n·(n+1)/2)
///
/// Divisor = n·(n+1)/2  (tổng các trọng số 1+2+…+n)
/// C₀ = bar gần nhất, Cₙ₋₁ = bar cũ nhất trong cửa sổ
/// ```
///
/// Ví dụ WMA(3) với giá [1, 2, 3]:
/// - Trọng số: [1, 2, 3], divisor = 6
/// - WMA = (1×1 + 2×2 + 3×3) / 6 = 14/6 ≈ 2.33
///
/// # So sánh
/// | Indicator | Phản ứng | Noise | Ứng dụng |
/// |-----------|----------|-------|----------|
/// | SMA       | Chậm     | Thấp  | Trend dài hạn |
/// | WMA       | Trung bình | Trung bình | HMA building block |
/// | EMA       | Nhanh    | Cao hơn | Mọi thứ |
///
/// WMA là thành phần cốt lõi của Hull Moving Average (HMA):
/// `HMA(n) = WMA(2·WMA(n/2) − WMA(n), √n)`
///
/// # Warmup
/// Cần đúng `period` bar để trả về giá trị đầu tiên.
#[derive(Clone)]
pub struct Wma {
    period: usize,
    buffer: VecDeque<f64>,
    weight_sum: f64, // n*(n+1)/2
}

impl Wma {
    pub fn new(period: usize) -> Self {
        assert!(period > 0);
        let weight_sum = (period * (period + 1) / 2) as f64;
        Self {
            period,
            buffer: VecDeque::with_capacity(period),
            weight_sum,
        }
    }

    pub fn description() -> &'static str {
        "Weighted Moving Average — linearly increasing weights so the most recent bar has the highest weight. Smoother reaction than EMA with less lag than SMA. Outputs a single value (price scale)."
    }

    pub fn update(&mut self, value: f64) -> Option<f64> {
        self.buffer.push_back(value);
        if self.buffer.len() > self.period {
            self.buffer.pop_front();
        }
        if self.buffer.len() < self.period {
            return None;
        }
        let wma = self
            .buffer
            .iter()
            .enumerate()
            .map(|(i, &v)| v * (i + 1) as f64)
            .sum::<f64>()
            / self.weight_sum;
        Some(wma)
    }

    pub fn is_ready(&self) -> bool {
        self.buffer.len() >= self.period
    }

    pub fn reset(&mut self) {
        self.buffer.clear();
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_wma_basic() {
        // WMA(3): weights [1,2,3], divisor = 6
        // Values [1,2,3] → (1*1 + 2*2 + 3*3) / 6 = 14/6
        let mut wma = Wma::new(3);
        assert_eq!(wma.update(1.0), None);
        assert_eq!(wma.update(2.0), None);
        let v = wma.update(3.0).unwrap();
        assert!((v - 14.0 / 6.0).abs() < 1e-9, "WMA = {v}");
    }

    #[test]
    fn test_wma_rolling() {
        // Values [2,3,4] → (2*1 + 3*2 + 4*3) / 6 = 20/6
        let mut wma = Wma::new(3);
        wma.update(1.0);
        wma.update(2.0);
        wma.update(3.0);
        let v = wma.update(4.0).unwrap();
        assert!((v - 20.0 / 6.0).abs() < 1e-9, "WMA rolling = {v}");
    }

    #[test]
    fn test_wma_constant_equals_value() {
        // All same values → WMA should equal that value
        let mut wma = Wma::new(5);
        let mut last = None;
        for _ in 0..5 {
            last = wma.update(42.0);
        }
        assert!((last.unwrap() - 42.0).abs() < 1e-9);
    }
}

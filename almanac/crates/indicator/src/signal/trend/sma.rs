use std::collections::VecDeque;

/// Simple Moving Average (SMA) — trung bình động đơn giản.
///
/// Indicator nền tảng nhất trong phân tích kỹ thuật. SMA tính trung bình
/// cộng của `n` bar gần nhất với trọng số bằng nhau.
///
/// # Công thức
/// ```text
/// SMA(n) = (C₀ + C₁ + … + Cₙ₋₁) / n
/// ```
///
/// # Đặc điểm
/// - **Lag**: SMA lag `n/2` bar so với giá thực — lag lớn nhất trong các họ MA
/// - **Smoothing**: loại bỏ noise hiệu quả, tốt cho trend dài hạn
/// - **O(1) update**: dùng rolling sum, không cần tính lại toàn bộ window
/// - **Equal weights**: bar cũ và bar mới được tính như nhau — phản ứng chậm
///
/// # So sánh với EMA
/// | | SMA | EMA |
/// |---|---|---|
/// | Weights | bằng nhau | exponential (gần nhất = lớn nhất) |
/// | Lag | n/2 | ≈ n/2.9 |
/// | Smoothing | mượt hơn | nhiều noise hơn |
/// | Dùng cho | trend dài hạn, support/resistance | crossover tín hiệu |
///
/// # Warmup
/// Cần đúng `n` bar trước khi trả về giá trị đầu tiên.
#[derive(Debug, Clone)]
pub struct Sma {
    period: usize,
    buffer: VecDeque<f64>,
    /// Running sum để tính SMA trong O(1)
    sum: f64,
}

impl Sma {
    pub fn new(period: usize) -> Self {
        assert!(period > 0, "SMA period must be > 0");
        Self { period, buffer: VecDeque::with_capacity(period + 1), sum: 0.0 }
    }

    pub fn description() -> &'static str {
        "Simple Moving Average — unweighted mean of the last N closes. Baseline smoothing filter used in crossover and trend-detection systems."
    }

    /// Feed một giá mới. Trả về `Some(sma)` sau khi đủ `period` bar.
    pub fn update(&mut self, value: f64) -> Option<f64> {
        self.buffer.push_back(value);
        self.sum += value;

        if self.buffer.len() > self.period {
            self.sum -= self.buffer.pop_front().unwrap();
        }

        if self.buffer.len() == self.period {
            Some(self.sum / self.period as f64)
        } else {
            None
        }
    }

    pub fn value(&self) -> Option<f64> {
        if self.buffer.len() == self.period {
            Some(self.sum / self.period as f64)
        } else {
            None
        }
    }

    pub fn is_ready(&self) -> bool {
        self.buffer.len() == self.period
    }

    pub fn reset(&mut self) {
        self.buffer.clear();
        self.sum = 0.0;
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_sma_basic() {
        let mut sma = Sma::new(3);
        assert_eq!(sma.update(1.0), None);
        assert_eq!(sma.update(2.0), None);
        assert_eq!(sma.update(3.0), Some(2.0));
        assert_eq!(sma.update(4.0), Some(3.0));
        assert_eq!(sma.update(5.0), Some(4.0));
    }
}

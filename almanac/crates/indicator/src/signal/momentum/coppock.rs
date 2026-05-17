use std::collections::VecDeque;

/// Coppock Curve — long-term momentum oscillator, nổi tiếng với tín hiệu buy đáy chu kỳ lớn.
///
/// Được Edwin Sedgwick Coppock phát triển năm 1962, ban đầu đăng trên tạp chí
/// *Barron's*. Thú vị là Coppock — một người Episcopalian — hỏi các giám mục
/// Anh giáo mất bao lâu để hồi phục tâm lý sau mất mát. Họ trả lời 11-14 tháng.
/// Coppock dùng con số này làm cơ sở cho ROC periods.
///
/// # Thiết kế
/// Kết hợp hai ROC với period khác nhau để nắm bắt cả momentum trung hạn và
/// dài hạn, rồi smooth bằng WMA để loại bỏ nhiễu.
///
/// # Công thức
/// ```text
/// Coppock = WMA(ROC(long) + ROC(short), wma_period)
/// ```
/// Tham số gốc cho monthly chart: long=14, short=11, wma=10
///
/// # Tín hiệu
/// - **Coppock cắt lên từ dưới 0** → tín hiệu buy dài hạn (rất đáng tin khi monthly)
/// - Thiết kế gốc chỉ cho buy signals, không có sell
/// - Dùng với daily/weekly: thêm sell khi Coppock cắt xuống từ trên 0
///
/// # Lưu ý
/// - Tốt nhất với monthly/weekly charts — trên daily có nhiều false signals
/// - Lag lớn nhưng ít false positive cho major bottom
#[derive(Debug, Clone)]
pub struct Coppock {
    short_period: usize,
    long_period: usize,
    wma_period: usize,
    /// Buffer cho short ROC (cần short_period + 1 giá)
    short_buf: VecDeque<f64>,
    /// Buffer cho long ROC (cần long_period + 1 giá)
    long_buf: VecDeque<f64>,
    /// Buffer các giá trị (ROC_long + ROC_short) để tính WMA
    sum_buf: VecDeque<f64>,
}

impl Coppock {
    pub fn new(short_period: usize, long_period: usize, wma_period: usize) -> Self {
        assert!(short_period < long_period, "short_period must be < long_period");
        assert!(wma_period > 0);
        Self {
            short_period,
            long_period,
            wma_period,
            short_buf: VecDeque::with_capacity(short_period + 1),
            long_buf: VecDeque::with_capacity(long_period + 1),
            sum_buf: VecDeque::with_capacity(wma_period),
        }
    }

    pub fn description() -> &'static str {
        "Coppock Curve — long-term momentum indicator; sum of two weighted ROCs. Originally designed to identify buying opportunities after major market lows."
    }

    /// Coppock Curve với tham số gốc (11, 14, 10).
    pub fn standard() -> Self {
        Self::new(11, 14, 10)
    }

    pub fn update(&mut self, price: f64) -> Option<f64> {
        // Cập nhật buffer short
        if self.short_buf.len() == self.short_period + 1 {
            self.short_buf.pop_front();
        }
        self.short_buf.push_back(price);

        // Cập nhật buffer long
        if self.long_buf.len() == self.long_period + 1 {
            self.long_buf.pop_front();
        }
        self.long_buf.push_back(price);

        // Chỉ tính khi cả hai buffer đầy
        if self.short_buf.len() < self.short_period + 1
            || self.long_buf.len() < self.long_period + 1
        {
            return None;
        }

        let oldest_short = self.short_buf[0];
        let oldest_long = self.long_buf[0];

        // ROC = (close / close[n] - 1) * 100
        let roc_short = if oldest_short.abs() > f64::EPSILON {
            (price / oldest_short - 1.0) * 100.0
        } else {
            0.0
        };
        let roc_long = if oldest_long.abs() > f64::EPSILON {
            (price / oldest_long - 1.0) * 100.0
        } else {
            0.0
        };

        let combined = roc_short + roc_long;

        // Tích lũy cho WMA
        if self.sum_buf.len() == self.wma_period {
            self.sum_buf.pop_front();
        }
        self.sum_buf.push_back(combined);

        if self.sum_buf.len() < self.wma_period {
            return None;
        }

        // WMA: weight tuyến tính, bar mới nhất có weight cao nhất
        let n = self.wma_period as f64;
        let denom = n * (n + 1.0) / 2.0;
        let wma = self
            .sum_buf
            .iter()
            .enumerate()
            .map(|(i, &v)| v * (i as f64 + 1.0))
            .sum::<f64>()
            / denom;

        Some(wma)
    }

    pub fn is_ready(&self) -> bool {
        self.sum_buf.len() >= self.wma_period
            && self.long_buf.len() >= self.long_period + 1
    }

    pub fn reset(&mut self) {
        self.short_buf.clear();
        self.long_buf.clear();
        self.sum_buf.clear();
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_coppock_warmup() {
        // Cần long_period+1 + wma_period - 1 bar tối thiểu
        let mut c = Coppock::new(3, 5, 4);
        let mut count = 0;
        for i in 0..20 {
            if c.update(100.0 + i as f64).is_some() { count += 1; }
        }
        assert!(count > 0);
    }

    #[test]
    fn test_coppock_uptrend_positive() {
        // Uptrend dài hạn → Coppock phải dương
        let mut c = Coppock::new(3, 5, 4);
        let mut last = None;
        for i in 0..30 { last = c.update(100.0 + i as f64); }
        assert!(last.unwrap() > 0.0);
    }

    #[test]
    fn test_coppock_reset() {
        let mut c = Coppock::standard();
        for i in 0..50 { c.update(100.0 + i as f64); }
        assert!(c.is_ready());
        c.reset();
        assert!(!c.is_ready());
    }
}

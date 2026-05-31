use std::collections::VecDeque;

/// Rate of Change (ROC) — đo % thay đổi giá so với n bar trước.
///
/// ROC là indicator momentum đơn giản nhất: so sánh giá hiện tại với giá n bar
/// trước đó. Dương = giá cao hơn n bar trước (upward momentum); Âm = ngược lại.
///
/// # Công thức
/// ```text
/// ROC = ((Close − Close[n]) / Close[n]) × 100    (đơn vị: %)
/// ```
///
/// # Cách đọc tín hiệu
/// - **ROC > 0**: giá cao hơn n bar trước → positive momentum
/// - **ROC < 0**: giá thấp hơn → negative momentum
/// - **ROC cắt 0 từ dưới lên**: momentum chuyển positive → buy signal
/// - **ROC cắt 0 từ trên xuống**: momentum chuyển negative → sell signal
/// - **ROC divergence vs giá**: tín hiệu đảo chiều (giá new high nhưng ROC thấp hơn)
///
/// # Ứng dụng
/// - **Momentum ranking**: dùng ROC(n) để rank cổ phiếu theo momentum (momentum factor)
/// - **Thành phần của TRIX**: TRIX = ROC(EMA3) — ROC của triple smoothed EMA
/// - **Overbought/oversold**: ROC quá cao/thấp so với lịch sử → mean reversion
///
/// # So sánh với momentum indicator khác
/// - ROC: đơn giản nhất, không smoothed, nhiều noise
/// - RSI: chuẩn hóa 0..100, smoothed → ít noise hơn
/// - MACD: so sánh 2 EMA → smoothed và có signal line
///
/// # Warmup
/// Cần `period + 1` bar (bar hiện tại + bar n bar trước).
#[derive(Debug, Clone)]
pub struct Roc {
    period: usize,
    buffer: VecDeque<f64>,
}

impl Roc {
    pub fn new(period: usize) -> Self {
        assert!(period > 0);
        Self {
            period,
            buffer: VecDeque::with_capacity(period + 1),
        }
    }

    pub fn description() -> &'static str {
        "Rate of Change — percentage change in price over N bars. Positive ROC = upward momentum; zero-cross signals momentum reversal. Outputs a single percentage-change value."
    }

    pub fn update(&mut self, close: f64) -> Option<f64> {
        self.buffer.push_back(close);
        if self.buffer.len() > self.period + 1 {
            self.buffer.pop_front();
        }
        if self.buffer.len() < self.period + 1 {
            return None;
        }
        let past = self.buffer[0];
        if past == 0.0 {
            return Some(0.0);
        }
        Some((close - past) / past * 100.0)
    }

    pub fn is_ready(&self) -> bool {
        self.buffer.len() >= self.period + 1
    }

    pub fn reset(&mut self) {
        self.buffer.clear();
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_roc_basic() {
        // ROC(3): close now vs close 3 bars ago
        // 100 → 100 → 100 → 110 → ROC = (110-100)/100 * 100 = 10.0
        let mut roc = Roc::new(3);
        assert!(roc.update(100.0).is_none());
        assert!(roc.update(100.0).is_none());
        assert!(roc.update(100.0).is_none());
        let v = roc.update(110.0).unwrap();
        assert!((v - 10.0).abs() < 1e-9, "ROC = {v}");
    }

    #[test]
    fn test_roc_negative() {
        let mut roc = Roc::new(2);
        roc.update(100.0);
        roc.update(100.0);
        let v = roc.update(90.0).unwrap();
        assert!((v - (-10.0)).abs() < 1e-9, "ROC = {v}");
    }

    #[test]
    fn test_roc_zero_return() {
        let mut roc = Roc::new(2);
        roc.update(100.0);
        roc.update(100.0);
        let v = roc.update(100.0).unwrap();
        assert!((v - 0.0).abs() < 1e-9);
    }
}

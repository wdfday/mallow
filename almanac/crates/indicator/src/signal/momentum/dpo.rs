use std::collections::VecDeque;

/// Detrended Price Oscillator (DPO) — loại bỏ trend dài hạn, giữ lại chu kỳ ngắn.
///
/// DPO so sánh giá của một điểm trong quá khứ với SMA tại thời điểm hiện tại.
/// Bằng cách dịch SMA về quá khứ, DPO loại bỏ trend dài hạn và làm nổi bật
/// **các chu kỳ giá ngắn hạn** — hữu ích để tìm đỉnh/đáy chu kỳ.
///
/// # Tại sao dùng DPO thay SMA oscillator thông thường?
/// SMA thông thường có lag = n/2 bar. DPO cố tình exploit lag này để
/// "detrend" — tức là khi giá hiện tại đang ở đỉnh chu kỳ, SMA (đang tính
/// từ một vị trí trễ hơn) sẽ thấp hơn → DPO dương.
///
/// # Công thức
/// ```text
/// displacement = floor(period / 2) + 1
///
/// DPO(t) = close(t − displacement) − SMA(period, t)
/// ```
/// Tức là: lấy giá cách đây `displacement` bar, trừ đi SMA hiện tại của `period` bar.
///
/// # Cách đọc
/// - DPO > 0: giá quá khứ trên SMA → đang ở pha trên của chu kỳ
/// - DPO < 0: giá quá khứ dưới SMA → đang ở pha dưới của chu kỳ
/// - Điểm giao cắt zero = trung điểm chu kỳ
/// - **Không dùng để dự báo trend** — chỉ để đo chu kỳ và thời điểm
///
/// # Warmup
/// Cần `period + displacement` bar trước khi trả về kết quả.
#[derive(Debug, Clone)]
pub struct Dpo {
    period: usize,
    displacement: usize,
    /// Buffer size = period + displacement (oldest first)
    buf: VecDeque<f64>,
}

impl Dpo {
    pub fn new(period: usize) -> Self {
        assert!(period >= 2, "DPO period must be >= 2");
        let displacement = period / 2 + 1;
        Self {
            period,
            displacement,
            buf: VecDeque::with_capacity(period + displacement),
        }
    }

    pub fn description() -> &'static str {
        "Detrended Price Oscillator — removes the longer-term trend to expose shorter cycles in price. Useful for identifying cycle lengths."
    }

    /// Feed một giá mới. Trả về `Some(dpo)` sau khi đủ `period + displacement` bar.
    pub fn update(&mut self, price: f64) -> Option<f64> {
        let max_len = self.period + self.displacement;
        if self.buf.len() == max_len {
            self.buf.pop_front();
        }
        self.buf.push_back(price);

        if self.buf.len() < max_len {
            return None;
        }

        // Giá bị dịch về quá khứ: cách bar mới nhất `displacement` bước
        // Trong buf (oldest first, size = period + displacement):
        // newest = buf[max_len-1], displaced = buf[max_len-1-displacement] = buf[period-1]
        let displaced_price = self.buf[self.period - 1];

        // SMA của `period` bar gần nhất = buf[displacement..max_len]
        let sma: f64 = self.buf.iter().skip(self.displacement).sum::<f64>()
            / self.period as f64;

        Some(displaced_price - sma)
    }

    pub fn is_ready(&self) -> bool {
        self.buf.len() >= self.period + self.displacement
    }

    pub fn reset(&mut self) {
        self.buf.clear();
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_dpo_warmup() {
        let period = 5;
        let displacement = 5 / 2 + 1; // = 3
        let mut dpo = Dpo::new(period);
        for i in 0..(period + displacement - 1) {
            assert!(dpo.update(i as f64).is_none());
        }
        assert!(dpo.update(100.0).is_some());
    }

    #[test]
    fn test_dpo_flat_price_zero() {
        // Giá không đổi → displaced price = SMA → DPO = 0
        let mut dpo = Dpo::new(5);
        let mut last = None;
        for _ in 0..20 { last = dpo.update(50.0); }
        assert!((last.unwrap()).abs() < 1e-10);
    }

    #[test]
    fn test_dpo_oscillates_with_price() {
        // DPO phải dao động quanh 0 khi giá dao động
        let mut dpo = Dpo::new(5);
        let mut values = Vec::new();
        for i in 0..30 {
            let p = 100.0 + (i as f64 * 0.5).sin() * 5.0;
            if let Some(v) = dpo.update(p) { values.push(v); }
        }
        let has_positive = values.iter().any(|&v| v > 0.0);
        let has_negative = values.iter().any(|&v| v < 0.0);
        assert!(has_positive && has_negative, "DPO should oscillate around 0");
    }

    #[test]
    fn test_dpo_reset() {
        let mut dpo = Dpo::new(5);
        for i in 0..20 { dpo.update(i as f64); }
        assert!(dpo.is_ready());
        dpo.reset();
        assert!(!dpo.is_ready());
    }
}

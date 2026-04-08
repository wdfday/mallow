/// Smoothed Moving Average (SMMA) — còn gọi là Wilder's Moving Average hoặc RMA.
///
/// # Sự khác biệt so với EMA
/// EMA dùng alpha = 2 / (n + 1), SMMA dùng alpha = 1 / n. Kết quả là SMMA
/// chạy chậm hơn và mượt hơn EMA với cùng period. Wilder dùng SMMA trong
/// RSI, ATR, ADX gốc của ông.
///
/// # Công thức
/// ```text
/// Seed    = SMA(n) của n bar đầu tiên
/// SMMA(t) = (SMMA(t-1) * (n - 1) + price(t)) / n
///         = SMMA(t-1) + (price(t) - SMMA(t-1)) / n
/// ```
///
/// # So sánh alpha
/// | MA    | alpha       | Tốc độ  |
/// |-------|-------------|---------|
/// | SMA   | 1/n (flat)  | N/A     |
/// | SMMA  | 1/n         | chậm    |
/// | EMA   | 2/(n+1)     | nhanh   |
#[derive(Debug, Clone)]
pub struct Smma {
    period: usize,
    /// 1/n — hệ số smoothing của Wilder
    alpha: f64,
    /// Giá trị SMMA hiện tại (None trong giai đoạn warmup)
    value: Option<f64>,
    /// Số bar đã nhận
    count: usize,
    /// Tổng tích lũy để tính SMA seed
    seed_sum: f64,
}

impl Smma {
    pub fn new(period: usize) -> Self {
        assert!(period > 0, "SMMA period must be > 0");
        Self {
            period,
            alpha: 1.0 / period as f64,
            value: None,
            count: 0,
            seed_sum: 0.0,
        }
    }

    /// Feed một bar mới. Trả về `Some(smma)` sau khi đủ `period` bar.
    pub fn update(&mut self, price: f64) -> Option<f64> {
        self.count += 1;

        if self.count < self.period {
            // Giai đoạn thu thập dữ liệu để tính SMA seed
            self.seed_sum += price;
            return None;
        }

        if self.count == self.period {
            // Bar thứ n: seed SMMA = SMA(n)
            self.seed_sum += price;
            self.value = Some(self.seed_sum / self.period as f64);
            return self.value;
        }

        // SMMA(t) = SMMA(t-1) + (price - SMMA(t-1)) / n
        let prev = self.value.unwrap();
        self.value = Some(prev + self.alpha * (price - prev));
        self.value
    }

    pub fn value(&self) -> Option<f64> {
        self.value
    }

    pub fn is_ready(&self) -> bool {
        self.value.is_some()
    }

    pub fn reset(&mut self) {
        self.value = None;
        self.count = 0;
        self.seed_sum = 0.0;
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_smma_seed_equals_sma() {
        // Bar đầu tiên sau warmup phải bằng SMA của n bar đầu
        let mut smma = Smma::new(3);
        assert_eq!(smma.update(10.0), None);
        assert_eq!(smma.update(20.0), None);
        let v = smma.update(30.0).unwrap(); // SMA(10,20,30) = 20
        assert!((v - 20.0).abs() < 1e-10);
    }

    #[test]
    fn test_smma_slower_than_ema() {
        // SMMA phản ứng chậm hơn EMA khi giá đột biến
        use crate::Ema;
        let period = 5;
        let mut smma = Smma::new(period);
        let mut ema = Ema::new(period);
        let prices = [10.0, 10.0, 10.0, 10.0, 10.0, 100.0, 100.0];

        let (mut sv, mut ev) = (0.0, 0.0);
        for &p in &prices {
            if let Some(v) = smma.update(p) { sv = v; }
            if let Some(v) = ema.update(p) { ev = v; }
        }
        // EMA alpha=2/6 ≈ 0.33 vs SMMA alpha=1/5 = 0.20 → EMA phản ứng nhanh hơn
        assert!(ev > sv, "EMA should react faster than SMMA: ema={ev}, smma={sv}");
    }

    #[test]
    fn test_smma_reset() {
        let mut smma = Smma::new(3);
        for p in [1.0, 2.0, 3.0, 4.0] {
            smma.update(p);
        }
        assert!(smma.is_ready());
        smma.reset();
        assert!(!smma.is_ready());
        assert_eq!(smma.update(5.0), None);
    }
}

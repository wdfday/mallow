use std::collections::VecDeque;

/// Vortex Indicator (VI) — đo hướng và sức mạnh của chuyển động xu hướng.
///
/// Được Etienne Botes và Douglas Siepman công bố trên tạp chí *Technical Analysis
/// of Stocks & Commodities* năm 2010. Lấy cảm hứng từ chuyển động xoáy trong tự
/// nhiên (vortex): giá tăng tạo "vortex upward", giá giảm tạo "vortex downward".
///
/// # Ý tưởng cốt lõi
/// - `+VM` đo sức mạnh upward: khoảng cách từ **high hôm nay** đến **low hôm qua**
/// - `-VM` đo sức mạnh downward: khoảng cách từ **low hôm nay** đến **high hôm qua**
/// - Cả hai được chuẩn hoá bởi tổng True Range để có thể so sánh
///
/// # Công thức
/// ```text
/// +VM(t) = |high(t) − low(t-1)|
/// -VM(t) = |low(t)  − high(t-1)|
/// TR(t)  = max(high(t)−low(t), |high(t)−close(t-1)|, |low(t)−close(t-1)|)
///
/// +VI(n) = Σ(+VM, n) / Σ(TR, n)   ← rolling sum, không phải EMA
/// -VI(n) = Σ(-VM, n) / Σ(TR, n)
/// ```
///
/// # Tín hiệu giao dịch
/// - **+VI cắt lên trên -VI** → bullish crossover, xem xét long
/// - **-VI cắt lên trên +VI** → bearish crossover, xem xét short
/// - Cả hai đường hội tụ (gần nhau, thấp) → thị trường consolidation
/// - Cả hai đường phân kỳ (cách xa nhau) → trend mạnh
///
/// # Khác biệt với DMI/ADX
/// | Chỉ báo | Smoothing | Cách tính VM           |
/// |---------|-----------|------------------------|
/// | DMI/ADX | SMMA      | so sánh up/down move   |
/// | Vortex  | Rolling Σ | cross-bar measurement  |
///
/// Vortex phản ứng nhanh hơn ADX vì dùng rolling sum thay vì exponential smoothing.
#[derive(Debug, Clone)]
pub struct Vortex {
    period: usize,
    /// Buffer cho (+VM, -VM, TR) của các bar trong window
    buf: VecDeque<(f64, f64, f64)>,
    /// Running sums để O(1) update
    sum_plus_vm: f64,
    sum_minus_vm: f64,
    sum_tr: f64,
    /// Dữ liệu bar trước để tính VM và TR
    prev_high: Option<f64>,
    prev_low: Option<f64>,
    prev_close: Option<f64>,
}

/// Output của Vortex Indicator.
#[derive(Debug, Clone, Copy)]
pub struct VortexValue {
    /// Positive Vortex Indicator (thường > 1.0 trong uptrend mạnh)
    pub plus_vi: f64,
    /// Negative Vortex Indicator (thường > 1.0 trong downtrend mạnh)
    pub minus_vi: f64,
}

impl Vortex {
    pub fn new(period: usize) -> Self {
        assert!(period >= 2, "Vortex period must be >= 2");
        Self {
            period,
            buf: VecDeque::with_capacity(period),
            sum_plus_vm: 0.0,
            sum_minus_vm: 0.0,
            sum_tr: 0.0,
            prev_high: None,
            prev_low: None,
            prev_close: None,
        }
    }

    /// Vortex(14) — period chuẩn được tác giả khuyến nghị.
    pub fn standard() -> Self {
        Self::new(14)
    }

    /// Feed một bar OHLC mới. Trả về `Some(VortexValue)` sau khi đủ `period` bar.
    pub fn update(&mut self, high: f64, low: f64, close: f64) -> Option<VortexValue> {
        let result = if let (Some(ph), Some(pl), Some(pc)) =
            (self.prev_high, self.prev_low, self.prev_close)
        {
            // +VM: high hôm nay vs low hôm qua (đo sức bật lên)
            let plus_vm = (high - pl).abs();
            // -VM: low hôm nay vs high hôm qua (đo sức kéo xuống)
            let minus_vm = (low - ph).abs();
            // True Range: khoảng biến động thực (gồm cả gap qua đêm)
            let tr = (high - low)
                .max((high - pc).abs())
                .max((low - pc).abs());

            // Trượt window: loại phần tử cũ nhất
            if self.buf.len() == self.period {
                let (old_p, old_m, old_tr) = self.buf.pop_front().unwrap();
                self.sum_plus_vm -= old_p;
                self.sum_minus_vm -= old_m;
                self.sum_tr -= old_tr;
            }

            self.buf.push_back((plus_vm, minus_vm, tr));
            self.sum_plus_vm += plus_vm;
            self.sum_minus_vm += minus_vm;
            self.sum_tr += tr;

            if self.buf.len() == self.period && self.sum_tr > f64::EPSILON {
                Some(VortexValue {
                    plus_vi: self.sum_plus_vm / self.sum_tr,
                    minus_vi: self.sum_minus_vm / self.sum_tr,
                })
            } else {
                None
            }
        } else {
            None
        };

        self.prev_high = Some(high);
        self.prev_low = Some(low);
        self.prev_close = Some(close);

        result
    }

    pub fn is_ready(&self) -> bool {
        self.buf.len() >= self.period
    }

    pub fn reset(&mut self) {
        self.buf.clear();
        self.sum_plus_vm = 0.0;
        self.sum_minus_vm = 0.0;
        self.sum_tr = 0.0;
        self.prev_high = None;
        self.prev_low = None;
        self.prev_close = None;
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn feed_uptrend(v: &mut Vortex, n: usize) -> Option<VortexValue> {
        let mut r = None;
        for i in 0..n {
            let base = 100.0 + i as f64;
            r = v.update(base + 2.0, base - 1.0, base + 1.0);
        }
        r
    }

    #[test]
    fn test_vortex_range() {
        // +VI và -VI thường nằm trong [0, ~2], không âm
        let mut v = Vortex::new(5);
        let val = feed_uptrend(&mut v, 20).unwrap();
        assert!(val.plus_vi >= 0.0);
        assert!(val.minus_vi >= 0.0);
    }

    #[test]
    fn test_vortex_uptrend_plus_dominates() {
        // Trong uptrend rõ ràng, +VI phải lớn hơn -VI
        let mut v = Vortex::new(5);
        let val = feed_uptrend(&mut v, 30).unwrap();
        assert!(
            val.plus_vi > val.minus_vi,
            "+VI={:.4} should > -VI={:.4} in uptrend",
            val.plus_vi,
            val.minus_vi
        );
    }

    #[test]
    fn test_vortex_downtrend_minus_dominates() {
        let mut v = Vortex::new(5);
        let mut r = None;
        for i in 0..30 {
            let base = 200.0 - i as f64;
            r = v.update(base + 1.0, base - 2.0, base - 1.0);
        }
        let val = r.unwrap();
        assert!(
            val.minus_vi > val.plus_vi,
            "-VI={:.4} should > +VI={:.4} in downtrend",
            val.minus_vi,
            val.plus_vi
        );
    }

    #[test]
    fn test_vortex_warmup() {
        let mut v = Vortex::new(3);
        // Bar đầu tiên không có prev → None
        assert!(v.update(10.0, 8.0, 9.0).is_none());
        // Bar 2 và 3: buf chưa đủ period
        assert!(v.update(11.0, 9.0, 10.0).is_none());
        assert!(v.update(12.0, 10.0, 11.0).is_none());
        // Bar 4: buf đủ period=3
        assert!(v.update(13.0, 11.0, 12.0).is_some());
    }

    #[test]
    fn test_vortex_reset() {
        let mut v = Vortex::new(5);
        feed_uptrend(&mut v, 20);
        assert!(v.is_ready());
        v.reset();
        assert!(!v.is_ready());
        assert!(v.update(100.0, 98.0, 99.0).is_none());
    }
}

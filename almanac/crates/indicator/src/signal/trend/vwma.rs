use std::collections::VecDeque;

/// Volume-Weighted Moving Average (VWMA) — MA có trọng số theo volume.
///
/// # Khác biệt với VWAP
/// | Chỉ báo | Reset session? | Window  | Dùng cho        |
/// |---------|----------------|---------|-----------------|
/// | VWAP    | Có (mỗi ngày)  | từ open | Intraday anchor |
/// | VWMA    | Không          | N bar   | Trend following |
///
/// VWMA là một MA thông thường nhưng bar nào có volume lớn thì được tính
/// trọng số cao hơn. Điều này khiến VWMA phản ánh tốt hơn mức giá mà
/// *phần lớn giao dịch* thực sự xảy ra.
///
/// # Ý nghĩa thực tế
/// - VWMA **trên** SMA: giá cao được giao dịch nhiều hơn → tích lũy (bullish)
/// - VWMA **dưới** SMA: giá thấp được giao dịch nhiều hơn → phân phối (bearish)
/// - Khoảng cách VWMA–SMA thể hiện áp lực mua/bán của smart money
///
/// # Công thức
/// ```text
/// VWMA(n) = Σ(close[i] × volume[i]) / Σ(volume[i])   i ∈ window n bars
/// ```
///
/// # Edge case
/// Nếu tổng volume = 0 (dữ liệu thiếu), trả về SMA đơn thuần để tránh NaN.
#[derive(Debug, Clone)]
pub struct Vwma {
    period: usize,
    /// Rolling buffer (close, volume)
    buf: VecDeque<(f64, f64)>,
    /// Tổng tích lũy để O(1) update thay vì O(n) duyệt lại toàn buffer
    sum_pv: f64,
    sum_v: f64,
    sum_p: f64, // fallback cho trường hợp volume = 0
}

impl Vwma {
    pub fn new(period: usize) -> Self {
        assert!(period > 0, "VWMA period must be > 0");
        Self {
            period,
            buf: VecDeque::with_capacity(period),
            sum_pv: 0.0,
            sum_v: 0.0,
            sum_p: 0.0,
        }
    }

    pub fn description() -> &'static str {
        "Volume-Weighted MA — averages price weighted by volume, so high-volume bars pull the average more. Useful as a volume-adjusted trend baseline. Outputs a single value (price scale)."
    }

    /// Feed một bar mới với close và volume.
    /// Trả về `Some(vwma)` sau khi đủ `period` bar.
    pub fn update(&mut self, close: f64, volume: f64) -> Option<f64> {
        let volume = volume.max(0.0); // Bảo vệ volume âm (dữ liệu lỗi)

        if self.buf.len() == self.period {
            // Loại bar cũ nhất ra khỏi running sum
            let (old_p, old_v) = self.buf.pop_front().unwrap();
            self.sum_pv -= old_p * old_v;
            self.sum_v -= old_v;
            self.sum_p -= old_p;
        }

        self.buf.push_back((close, volume));
        self.sum_pv += close * volume;
        self.sum_v += volume;
        self.sum_p += close;

        if self.buf.len() < self.period {
            return None;
        }

        if self.sum_v < f64::EPSILON {
            // Không có volume data → fallback SMA
            Some(self.sum_p / self.period as f64)
        } else {
            Some(self.sum_pv / self.sum_v)
        }
    }

    pub fn is_ready(&self) -> bool {
        self.buf.len() >= self.period
    }

    pub fn reset(&mut self) {
        self.buf.clear();
        self.sum_pv = 0.0;
        self.sum_v = 0.0;
        self.sum_p = 0.0;
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_vwma_equal_volume_equals_sma() {
        // Khi volume giống nhau, VWMA = SMA
        let mut vwma = Vwma::new(3);
        assert!(vwma.update(10.0, 100.0).is_none());
        assert!(vwma.update(20.0, 100.0).is_none());
        let v = vwma.update(30.0, 100.0).unwrap();
        assert!((v - 20.0).abs() < 1e-10, "VWMA={v}"); // SMA = 20
    }

    #[test]
    fn test_vwma_high_volume_on_high_price() {
        // Volume cao ở giá cao → VWMA > SMA
        let mut vwma = Vwma::new(3);
        vwma.update(10.0, 100.0);
        vwma.update(20.0, 100.0);
        let v = vwma.update(30.0, 1000.0).unwrap(); // volume lớn ở giá 30
        let sma = (10.0 + 20.0 + 30.0) / 3.0; // = 20
        assert!(v > sma, "VWMA={v} should be > SMA={sma} when high price has high volume");
    }

    #[test]
    fn test_vwma_high_volume_on_low_price() {
        // Volume cao ở giá thấp → VWMA < SMA
        let mut vwma = Vwma::new(3);
        vwma.update(10.0, 1000.0); // volume lớn ở giá thấp
        vwma.update(20.0, 100.0);
        let v = vwma.update(30.0, 100.0).unwrap();
        let sma = 20.0;
        assert!(v < sma, "VWMA={v} should be < SMA={sma} when low price has high volume");
    }

    #[test]
    fn test_vwma_zero_volume_fallback() {
        // Nếu volume = 0, fallback về SMA không panic
        let mut vwma = Vwma::new(3);
        vwma.update(10.0, 0.0);
        vwma.update(20.0, 0.0);
        let v = vwma.update(30.0, 0.0).unwrap();
        assert!((v - 20.0).abs() < 1e-10);
    }

    #[test]
    fn test_vwma_reset() {
        let mut vwma = Vwma::new(3);
        for p in [10.0, 20.0, 30.0] { vwma.update(p, 100.0); }
        assert!(vwma.is_ready());
        vwma.reset();
        assert!(!vwma.is_ready());
        assert_eq!(vwma.update(10.0, 100.0), None);
    }
}

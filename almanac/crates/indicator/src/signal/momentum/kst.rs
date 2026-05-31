use crate::{Roc, Sma};

/// Know Sure Thing (KST) — composite momentum dùng 4 ROC smoothed với trọng số tăng dần.
///
/// Được Martin Pring phát triển và trình bày trong *Technical Analysis Explained*.
/// KST là "grand summary" của momentum: kết hợp momentum ngắn hạn, trung hạn
/// và dài hạn vào một oscillator duy nhất. Tên "Know Sure Thing" phản ánh
/// sự tự tin của Pring vào tính toàn diện của nó.
///
/// # Thiết kế
/// 4 ROC với period tăng dần, mỗi ROC được smooth bằng SMA, sau đó
/// gộp lại với trọng số 1:2:3:4 (period dài = trọng số cao hơn).
///
/// # Tham số mặc định (short-term / daily)
/// ```text
/// RCMA1 = SMA(ROC(10), 10) × 1
/// RCMA2 = SMA(ROC(13), 13) × 2
/// RCMA3 = SMA(ROC(14), 14) × 3
/// RCMA4 = SMA(ROC(15), 15) × 4    ← trọng số cao nhất = ảnh hưởng lớn nhất
///
/// KST    = RCMA1 + RCMA2 + RCMA3 + RCMA4
/// Signal = SMA(KST, 9)
/// ```
/// Pring cũng có cấu hình cho weekly và monthly với period khác.
///
/// # Cách đọc
/// - KST cắt lên Signal → buy (xác nhận momentum dương)
/// - KST cắt xuống Signal → sell
/// - KST vượt 0 từ dưới → bắt đầu uptrend
/// - Divergence KST vs giá → tín hiệu đảo chiều sớm
#[derive(Debug, Clone)]
pub struct Kst {
    // 4 cặp (ROC, SMA) với weight 1..4
    roc1: Roc,
    sma1: Sma,
    roc2: Roc,
    sma2: Sma,
    roc3: Roc,
    sma3: Sma,
    roc4: Roc,
    sma4: Sma,
    signal_sma: Sma,
}

/// Kết quả KST.
#[derive(Debug, Clone, Copy)]
pub struct KstValue {
    /// KST line: tổng 4 RCMA có trọng số
    pub kst: f64,
    /// Signal line: SMA(KST, signal_period)
    pub signal: f64,
    /// Histogram: kst - signal
    pub histogram: f64,
}

impl Kst {
    /// Khởi tạo với tham số đầy đủ.
    /// `roc_periods`: 4 period cho ROC
    /// `sma_periods`: 4 period cho SMA smoothing tương ứng
    /// `signal_period`: period cho signal SMA
    pub fn new(
        roc_periods: [usize; 4],
        sma_periods: [usize; 4],
        signal_period: usize,
    ) -> Self {
        Self {
            roc1: Roc::new(roc_periods[0]),
            sma1: Sma::new(sma_periods[0]),
            roc2: Roc::new(roc_periods[1]),
            sma2: Sma::new(sma_periods[1]),
            roc3: Roc::new(roc_periods[2]),
            sma3: Sma::new(sma_periods[2]),
            roc4: Roc::new(roc_periods[3]),
            sma4: Sma::new(sma_periods[3]),
            signal_sma: Sma::new(signal_period),
        }
    }

    pub fn description() -> &'static str {
        "Know Sure Thing — weighted sum of four different ROC smoothings. Designed for long-term trend identification with a signal line. Outputs: `.kst` (default, KST line), `.signal` (signal line), `.histogram` (KST minus signal)."
    }

    /// KST short-term (daily) — cấu hình Pring gốc.
    pub fn standard() -> Self {
        Self::new([10, 13, 14, 15], [10, 13, 14, 15], 9)
    }

    pub fn update(&mut self, price: f64) -> Option<KstValue> {
        // Feed cả 4 ROC + SMA
        let r1 = self.roc1.update(price).and_then(|r| self.sma1.update(r));
        let r2 = self.roc2.update(price).and_then(|r| self.sma2.update(r));
        let r3 = self.roc3.update(price).and_then(|r| self.sma3.update(r));
        let r4 = self.roc4.update(price).and_then(|r| self.sma4.update(r));

        match (r1, r2, r3, r4) {
            (Some(v1), Some(v2), Some(v3), Some(v4)) => {
                // Tổng có trọng số 1:2:3:4
                let kst = 1.0 * v1 + 2.0 * v2 + 3.0 * v3 + 4.0 * v4;
                self.signal_sma.update(kst).map(|signal| KstValue {
                    kst,
                    signal,
                    histogram: kst - signal,
                })
            }
            _ => None,
        }
    }

    pub fn is_ready(&self) -> bool {
        self.signal_sma.is_ready()
    }

    pub fn reset(&mut self) {
        self.roc1.reset(); self.sma1.reset();
        self.roc2.reset(); self.sma2.reset();
        self.roc3.reset(); self.sma3.reset();
        self.roc4.reset(); self.sma4.reset();
        self.signal_sma.reset();
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_kst_uptrend_positive() {
        let mut kst = Kst::new([3, 4, 5, 6], [3, 4, 5, 6], 3);
        let mut last = None;
        for i in 0..60 { last = kst.update(100.0 + i as f64); }
        assert!(last.unwrap().kst > 0.0);
    }

    #[test]
    fn test_kst_downtrend_negative() {
        let mut kst = Kst::new([3, 4, 5, 6], [3, 4, 5, 6], 3);
        let mut last = None;
        for i in 0..60 { last = kst.update(200.0 - i as f64); }
        assert!(last.unwrap().kst < 0.0);
    }

    #[test]
    fn test_kst_reset() {
        let mut kst = Kst::standard();
        for i in 0..100 { kst.update(100.0 + i as f64); }
        assert!(kst.is_ready());
        kst.reset();
        assert!(!kst.is_ready());
    }
}

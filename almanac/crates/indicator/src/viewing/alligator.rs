use crate::Wma;

#[derive(Debug, Clone)]
pub struct AlligatorValue {
    /// Jaw (blue): WMA(13) of median price — shifted 8 forward in display.
    pub jaw: f64,
    /// Teeth (red): WMA(8) of median price — shifted 5 forward.
    pub teeth: f64,
    /// Lips (green): WMA(5) of median price — shifted 3 forward.
    pub lips: f64,
    /// `true` when Lips > Teeth > Jaw (bullish spread / alligator eating upward).
    pub bullish: bool,
}

/// Williams Alligator — ba đường WMA mô phỏng hành vi của cá sấu.
///
/// Được Bill Williams phát triển. Ba đường đặt tên theo "cơ quan" của cá sấu:
/// - **Jaw** (hàm): WMA(13) của median price, shift 8 bar về tương lai trong chart
/// - **Teeth** (răng): WMA(8), shift 5 bar
/// - **Lips** (môi): WMA(5), shift 3 bar
///
/// Metaphor: "Cá sấu ăn" khi ba đường mở rộng ra và theo thứ tự (Lips > Teeth > Jaw
/// trong uptrend). "Cá sấu ngủ" khi ba đường đan chéo nhau — không trade.
///
/// **Note**: Codebase này trả về giá trị tại bar hiện tại (không shift) vì trong
/// backtesting signal-generation, shift không có ý nghĩa thực tiễn.
///
/// # Tín hiệu
/// - **Lips > Teeth > Jaw** (`bullish = true`): alligator đang ăn lên → long
/// - **Lips < Teeth < Jaw**: alligator đang ăn xuống → short
/// - **Ba đường đan nhau**: alligator đang ngủ → không trade / consolidation
/// - **Lips cắt ra ngoài Teeth**: báo hiệu sắp có trend mới
///
/// # Warmup
/// Vì cascade WMA, warmup ≈ jaw_period + (teeth−1) + (lips−1).
/// Ví dụ default(13,8,5): 13 + 7 + 4 = 24 bar.
#[derive(Clone)]
pub struct Alligator {
    jaw: Wma,
    teeth: Wma,
    lips: Wma,
}

impl Alligator {
    /// Standard Williams periods: jaw=13, teeth=8, lips=5.
    pub fn new(jaw_period: usize, teeth_period: usize, lips_period: usize) -> Self {
        Self {
            jaw: Wma::new(jaw_period),
            teeth: Wma::new(teeth_period),
            lips: Wma::new(lips_period),
        }
    }

    pub fn update(&mut self, high: f64, low: f64) -> Option<AlligatorValue> {
        let median = (high + low) / 2.0;
        let jaw = self.jaw.update(median)?;
        let teeth = self.teeth.update(median)?;
        let lips = self.lips.update(median)?;
        let bullish = lips > teeth && teeth > jaw;
        Some(AlligatorValue { jaw, teeth, lips, bullish })
    }

    pub fn reset(&mut self) {
        self.jaw.reset();
        self.teeth.reset();
        self.lips.reset();
    }
}

impl Default for Alligator {
    fn default() -> Self {
        Self::new(13, 8, 5)
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_alligator_ready_eventually() {
        // Due to cascading ? (jaw → teeth → lips), Alligator(5,4,3) needs:
        // jaw(5) + teeth(4-1) + lips(3-1) = 5 + 3 + 2 = 10 bars minimum.
        let mut al = Alligator::new(5, 4, 3);
        let mut got = false;
        for i in 0..15 {
            let p = 100.0 + i as f64;
            if al.update(p + 1.0, p - 1.0).is_some() {
                got = true;
            }
        }
        assert!(got, "Alligator(5,4,3) should produce a value within 15 bars");
    }

    #[test]
    fn test_alligator_bullish_uptrend() {
        // Default Alligator(13,8,5) cascades: 13 + 7 + 4 = 24 bars minimum.
        let mut al = Alligator::default();
        let mut last = None;
        for i in 0..60 {
            let p = 100.0 + i as f64 * 2.0;
            last = al.update(p + 1.0, p - 1.0);
        }
        let v = last.unwrap();
        assert!(v.bullish, "Alligator should be bullish in strong uptrend");
    }

    #[test]
    fn test_alligator_lips_gt_teeth_gt_jaw_in_uptrend() {
        let mut al = Alligator::default();
        let mut last = None;
        for i in 0..60 {
            let p = 100.0 + i as f64 * 2.0;
            last = al.update(p + 1.0, p - 1.0);
        }
        let v = last.unwrap();
        assert!(v.lips > v.teeth && v.teeth > v.jaw);
    }
}

use crate::Ema;

/// Bull Bear Power — đo sức mạnh tương đối của bulls và bears quanh EMA.
///
/// Được Alexander Elder phát triển trong *Trading for a Living* (1993).
/// Dựa trên quan sát: trong một bar, bulls đẩy giá lên đến HIGH còn bears
/// kéo giá về đến LOW. EMA là "điểm cân bằng thị trường". Khoảng cách
/// từ HIGH/LOW đến EMA thể hiện ai đang mạnh hơn.
///
/// # Công thức
/// ```text
/// EMA = EMA(close, period)
///
/// Bull Power = high − EMA     (bulls đẩy giá cao hơn mức cân bằng bao nhiêu)
/// Bear Power = low  − EMA     (bears kéo giá thấp hơn mức cân bằng bao nhiêu — thường âm)
/// ```
///
/// # Cách đọc (Elder's 3-Screen system)
/// - **Bull Power dương và tăng** → bull impulse đang mạnh → buy signal
/// - **Bear Power âm và giảm** → bear impulse đang mạnh → sell signal
/// - **Bull Power < 0**: bears hoàn toàn kiểm soát (giá không vượt được EMA cả bar)
/// - **Bear Power > 0**: bulls hoàn toàn kiểm soát (giá không xuống dưới EMA cả bar)
/// - Divergence: giá tăng nhưng Bull Power giảm → uptrend yếu
///
/// # Kết hợp thực tế
/// Thường dùng kết hợp với MACD (trend filter): chỉ buy khi MACD dương
/// VÀ Bear Power âm nhưng đang tăng (divergence bullish).
#[derive(Debug, Clone)]
pub struct BullBearPower {
    ema: Ema,
}

/// Kết quả Bull Bear Power.
#[derive(Debug, Clone, Copy)]
pub struct BullBearValue {
    /// Bull Power = high - EMA (dương khi bulls đang kiểm soát vùng trên)
    pub bull: f64,
    /// Bear Power = low - EMA (âm trong điều kiện bình thường)
    pub bear: f64,
    /// Giá trị EMA tại thời điểm này (hữu ích cho context)
    pub ema: f64,
}

impl BullBearPower {
    pub fn new(period: usize) -> Self {
        Self {
            ema: Ema::new(period),
        }
    }

    pub fn description() -> &'static str {
        "Elder Ray Bull/Bear Power — bull power = high minus EMA; bear power = low minus EMA. Both positive = bull market; both negative = bear market. Outputs: `.bull` (default, bull power), `.bear` (bear power), `.ema` (baseline EMA)."
    }

    /// Elder dùng EMA(13) cho daily bars.
    pub fn standard() -> Self {
        Self::new(13)
    }

    /// Feed bar mới. Trả về `Some` sau khi EMA warm up.
    pub fn update(&mut self, high: f64, low: f64, close: f64) -> Option<BullBearValue> {
        self.ema.update(close).map(|ema_val| BullBearValue {
            bull: high - ema_val,
            bear: low - ema_val,
            ema: ema_val,
        })
    }

    pub fn is_ready(&self) -> bool {
        self.ema.is_ready()
    }

    pub fn reset(&mut self) {
        self.ema.reset();
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_bull_bear_normal_market() {
        // Trong thị trường bình thường: bull > 0, bear < 0
        let mut bb = BullBearPower::new(5);
        let mut last = None;
        for i in 0..20 {
            let base = 100.0 + i as f64 * 0.1;
            last = bb.update(base + 1.0, base - 1.0, base);
        }
        let v = last.unwrap();
        assert!(v.bull > 0.0, "Bull power should be positive: {}", v.bull);
        assert!(v.bear < 0.0, "Bear power should be negative: {}", v.bear);
    }

    #[test]
    fn test_bull_power_uptrend() {
        // Trong uptrend mạnh, bull power tăng dần
        let mut bb = BullBearPower::new(5);
        let mut v1 = None;
        let mut v2 = None;
        for i in 0..10 {
            let base = 100.0 + i as f64;
            let r = bb.update(base + 2.0, base - 1.0, base + 1.0);
            if i == 8 { v1 = r; }
            if i == 9 { v2 = r; }
        }
        // Bull power phải dương trong uptrend
        assert!(v1.unwrap().bull > 0.0);
        assert!(v2.unwrap().bull > 0.0);
    }

    #[test]
    fn test_reset() {
        let mut bb = BullBearPower::new(5);
        for i in 0..10 {
            bb.update(100.0 + i as f64, 99.0 + i as f64, 100.0 + i as f64);
        }
        assert!(bb.is_ready());
        bb.reset();
        assert!(!bb.is_ready());
    }
}

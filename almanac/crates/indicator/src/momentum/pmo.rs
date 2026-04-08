use crate::Ema;

/// Price Momentum Oscillator (PMO) — ROC double-smoothed bằng EMA.
///
/// Được Carl Swenlin phát triển tại DecisionPoint. PMO là phiên bản
/// "làm mượt kép" của ROC, giữ lại tính chất momentum nhưng loại bỏ
/// noise hiệu quả hơn MACD hay single-EMA oscillators.
///
/// # Công thức
/// ```text
/// ROC_1bar = ((close / close[1]) - 1) × 10 × 100   ← % change × 10 để scale
///
/// EMA1  = EMA(ROC_1bar, smooth1)      ← làm mượt lần 1 (mặc định 35)
/// PMO   = EMA(EMA1 × 10, smooth2)     ← làm mượt lần 2 (mặc định 20)
/// Signal = EMA(PMO, signal)            ← signal line (mặc định 10)
/// ```
/// Factor × 10 ở mỗi bước để giữ magnitude hợp lý (không quá nhỏ).
///
/// # Cách đọc
/// - PMO > 0 → bullish momentum
/// - PMO < 0 → bearish momentum
/// - PMO cắt lên Signal → buy
/// - PMO cắt xuống Signal → sell
/// - PMO vượt overbought/oversold (tùy symbol, thường ±2.5 cho US stocks)
///
/// # Điểm khác biệt với MACD
/// PMO dùng ROC (% change) thay vì hiệu EMA → normalized hơn, so sánh
/// được giữa các symbols với mức giá khác nhau (giống PPO vs MACD).
#[derive(Debug, Clone)]
pub struct Pmo {
    /// Lưu close trước để tính ROC 1 bar
    prev_close: Option<f64>,
    ema1: Ema,
    ema2: Ema,
    signal_ema: Ema,
}

/// Kết quả PMO.
#[derive(Debug, Clone, Copy)]
pub struct PmoValue {
    /// PMO line
    pub pmo: f64,
    /// Signal line
    pub signal: f64,
    /// Histogram: pmo - signal
    pub histogram: f64,
}

impl Pmo {
    pub fn new(smooth1: usize, smooth2: usize, signal: usize) -> Self {
        Self {
            prev_close: None,
            ema1: Ema::new(smooth1),
            ema2: Ema::new(smooth2),
            signal_ema: Ema::new(signal),
        }
    }

    /// PMO(35, 20, 10) — cấu hình Swenlin gốc.
    pub fn standard() -> Self {
        Self::new(35, 20, 10)
    }

    pub fn update(&mut self, close: f64) -> Option<PmoValue> {
        let result = if let Some(prev) = self.prev_close {
            if prev.abs() > f64::EPSILON {
                // ROC 1-bar × 10 × 100 = % change × 10 × 100
                let roc = ((close / prev) - 1.0) * 1000.0;
                let ema1_val = self.ema1.update(roc);
                // PMO = EMA(EMA1 × 10, smooth2)
                let pmo_raw = ema1_val.and_then(|e1| self.ema2.update(e1 * 10.0));
                pmo_raw.and_then(|pmo| {
                    self.signal_ema.update(pmo).map(|signal| PmoValue {
                        pmo,
                        signal,
                        histogram: pmo - signal,
                    })
                })
            } else {
                None
            }
        } else {
            None
        };

        self.prev_close = Some(close);
        result
    }

    pub fn is_ready(&self) -> bool {
        self.signal_ema.is_ready()
    }

    pub fn reset(&mut self) {
        self.prev_close = None;
        self.ema1.reset();
        self.ema2.reset();
        self.signal_ema.reset();
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_pmo_uptrend_positive() {
        let mut pmo = Pmo::new(5, 3, 3);
        let mut last = None;
        for i in 0..30 { last = pmo.update(100.0 + i as f64); }
        assert!(last.unwrap().pmo > 0.0);
    }

    #[test]
    fn test_pmo_downtrend_negative() {
        let mut pmo = Pmo::new(5, 3, 3);
        let mut last = None;
        for i in 0..30 { last = pmo.update(200.0 - i as f64); }
        assert!(last.unwrap().pmo < 0.0);
    }

    #[test]
    fn test_pmo_constant_zero() {
        let mut pmo = Pmo::new(5, 3, 3);
        let mut last = None;
        for _ in 0..30 { last = pmo.update(100.0); }
        let v = last.unwrap();
        assert!(v.pmo.abs() < 1e-6, "PMO={}", v.pmo);
    }

    #[test]
    fn test_pmo_reset() {
        let mut pmo = Pmo::standard();
        for i in 0..100 { pmo.update(100.0 + i as f64); }
        assert!(pmo.is_ready());
        pmo.reset();
        assert!(!pmo.is_ready());
    }
}

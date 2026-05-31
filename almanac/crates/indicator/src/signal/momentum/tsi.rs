use crate::Ema;

/// True Strength Index (TSI) — momentum oscillator double-smoothed.
///
/// Được William Blau phát triển năm 1991. TSI chuẩn hóa momentum (price change)
/// bằng cách chia cho absolute price change đã được double-smooth. Kết quả là
/// oscillator trong khoảng −100..+100, phản ứng với trend nhưng ít noise hơn ROC.
///
/// # Công thức
/// ```text
/// PC   = Close − prev_Close                     ← price change
/// |PC| = |Close − prev_Close|                   ← absolute price change
///
/// PCDS  = EMA(EMA(PC,  first), second)           ← double-smoothed momentum
/// APCDS = EMA(EMA(|PC|, first), second)          ← double-smoothed magnitude
///
/// TSI = (PCDS / APCDS) × 100
/// ```
///
/// - **Numerator** (PCDS): smooth momentum, giảm noise
/// - **Denominator** (APCDS): chuẩn hóa theo biến động → range −100..+100
///
/// # Tham số mặc định
/// - `first = 25`: EMA dài → loại bỏ noise
/// - `second = 13`: EMA ngắn hơn → thêm một lớp smooth
///
/// # Cách đọc tín hiệu
/// - **TSI > 0**: momentum positive (uptrend)
/// - **TSI < 0**: momentum negative (downtrend)
/// - **TSI cắt 0**: trend change signal
/// - **TSI cắt signal line** (EMA của TSI): buy/sell signal nhanh hơn
/// - **Divergence** TSI vs giá: reversal mạnh
///
/// # Ưu điểm vs RSI
/// - TSI không bị overbought/oversold cứng nhắc → phù hợp cho trending market
/// - Double smoothing → ít whipsaw trong sideways hơn RSI
///
/// # Warmup
/// Cần `first + second + 1` bar (double cascade EMA + prev_close).
/// Ví dụ: TSI(25,13) cần ~38 bar.
#[derive(Debug, Clone)]
pub struct Tsi {
    first: usize,
    second: usize,
    prev_close: Option<f64>,
    /// First-pass EMA of price change
    ema1_pc: Ema,
    /// Second-pass EMA of first-pass EMA (price change)
    ema2_pc: Ema,
    /// First-pass EMA of |price change|
    ema1_apc: Ema,
    /// Second-pass EMA of first-pass |price change|
    ema2_apc: Ema,
    value: Option<f64>,
}

impl Tsi {
    pub fn new(first: usize, second: usize) -> Self {
        assert!(first > 0 && second > 0, "TSI periods must be > 0");
        Self {
            first,
            second,
            prev_close: None,
            ema1_pc: Ema::new(first),
            ema2_pc: Ema::new(second),
            ema1_apc: Ema::new(first),
            ema2_apc: Ema::new(second),
            value: None,
        }
    }

    pub fn description() -> &'static str {
        "True Strength Index — double-smoothed momentum oscillator normalised to ±100. Zero-line crossovers indicate trend changes. Outputs a single −100 to +100 value."
    }

    /// Default parameters: first=25, second=13
    pub fn default() -> Self {
        Self::new(25, 13)
    }

    /// Feed a new closing price. Returns `Some(tsi)` once double-smoothed series is ready.
    pub fn update(&mut self, close: f64) -> Option<f64> {
        let Some(prev) = self.prev_close else {
            self.prev_close = Some(close);
            return None;
        };
        self.prev_close = Some(close);

        let pc = close - prev;
        let apc = pc.abs();

        // Feed BOTH first-pass EMAs in lockstep every bar. They share the same
        // period (`first`) so they become ready on the same bar — keeping the
        // numerator (PC) and denominator (|PC|) cascades perfectly in sync.
        // (Feeding them conditionally would drop |PC| samples and desync the
        // two double-smoothed series, biasing TSI.)
        let e1_pc = self.ema1_pc.update(pc);
        let e1_apc = self.ema1_apc.update(apc);

        let (Some(e1_pc), Some(e1_apc)) = (e1_pc, e1_apc) else {
            return None;
        };

        // Same lockstep for the second pass (both period `second`).
        let pcds = self.ema2_pc.update(e1_pc);
        let apcds = self.ema2_apc.update(e1_apc);

        let (Some(pcds), Some(apcds)) = (pcds, apcds) else {
            return None;
        };

        self.value = if apcds.abs() < f64::EPSILON {
            Some(0.0)
        } else {
            Some((pcds / apcds) * 100.0)
        };
        self.value
    }

    pub fn value(&self) -> Option<f64> {
        self.value
    }

    pub fn is_ready(&self) -> bool {
        self.value.is_some()
    }

    pub fn reset(&mut self) {
        self.prev_close = None;
        self.ema1_pc = Ema::new(self.first);
        self.ema2_pc = Ema::new(self.second);
        self.ema1_apc = Ema::new(self.first);
        self.ema2_apc = Ema::new(self.second);
        self.value = None;
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_tsi_not_ready_during_warmup() {
        let mut tsi = Tsi::new(5, 3);
        // Warmup: 1 bar (prev_close) + first(5) feeds → ema1 ready at bar 5,
        // + second(3) feeds → ema2 ready at bar 7. So bars 0..=6 are not ready.
        for i in 0..7 {
            let v = tsi.update(100.0 + i as f64);
            assert!(v.is_none(), "TSI should not be ready during warmup: bar {i}");
        }
    }

    #[test]
    fn test_tsi_ready_after_warmup() {
        let mut tsi = Tsi::new(5, 3);
        // Need prev_close + first period + second period = 1 + 5 + 3 - 1 = 8 bars minimum
        for i in 0..20 {
            tsi.update(100.0 + i as f64 * 0.5);
        }
        assert!(tsi.is_ready(), "TSI should be ready after sufficient bars");
    }

    #[test]
    fn test_tsi_uptrend_positive() {
        let mut tsi = Tsi::new(5, 3);
        for i in 0..50 {
            tsi.update(100.0 + i as f64);
        }
        let v = tsi.value().unwrap();
        assert!(v > 0.0, "TSI should be positive in strong uptrend: {v}");
    }

    #[test]
    fn test_tsi_range_bounded() {
        let mut tsi = Tsi::new(5, 3);
        for i in 0..50 {
            tsi.update(100.0 + (i % 3) as f64 * 10.0);
        }
        if let Some(v) = tsi.value() {
            assert!(v >= -100.0 && v <= 100.0, "TSI out of range: {v}");
        }
    }

    #[test]
    fn test_tsi_reset() {
        let mut tsi = Tsi::new(5, 3);
        for i in 0..30 {
            tsi.update(100.0 + i as f64);
        }
        assert!(tsi.is_ready());
        tsi.reset();
        assert!(!tsi.is_ready());
    }
}

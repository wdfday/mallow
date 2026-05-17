use crate::Ema;

/// TRIX — oscillator đo tốc độ thay đổi của Triple EMA.
///
/// Được Jack Hutson phát triển năm 1983. TRIX lọc bỏ các minor cycle bằng triple
/// smoothing (EMA của EMA của EMA), chỉ phản ứng với các chu kỳ quan trọng hơn.
/// Kết quả là oscillator rất ít bị nhiễu nhiễu nhỏ.
///
/// # Công thức
/// ```text
/// EMA1 = EMA(close, n)
/// EMA2 = EMA(EMA1, n)
/// EMA3 = EMA(EMA2, n)
///
/// TRIX = (EMA3 − prev_EMA3) / prev_EMA3 × 100   ← % change của EMA3
/// Signal = EMA(TRIX, signal_period)
/// Histogram = TRIX − Signal
/// ```
///
/// # Tín hiệu giao dịch
/// - **TRIX cắt Signal từ dưới lên**: long signal
/// - **TRIX cắt Signal từ trên xuống**: short signal
/// - **TRIX > 0**: EMA3 đang tăng → momentum bullish
/// - **TRIX < 0**: EMA3 đang giảm → momentum bearish
/// - **Divergence TRIX vs giá**: tín hiệu đảo chiều sớm
///
/// # Ưu điểm
/// - Ít false signal hơn MACD vì triple smoothing lọc noise tốt hơn
/// - Hoạt động tốt trong trending market
/// - Kém hiệu quả hơn trong sideways (do lag của triple EMA)
///
/// # Warmup
/// Cần khoảng `3 × (period - 1) + signal_period` bar.
#[derive(Debug, Clone)]
pub struct TrixValue {
    /// TRIX: % thay đổi của triple EMA so với bar trước
    pub trix: f64,
    /// Signal line: EMA của TRIX
    pub signal: f64,
    /// Histogram: TRIX − Signal (dương khi TRIX > Signal)
    pub histogram: f64,
}

/// TRIX — Triple EMA oscillator đo tốc độ thay đổi của EMA3.
#[allow(missing_docs)]
#[derive(Clone)]
pub struct Trix {
    ema1: Ema,
    ema2: Ema,
    ema3: Ema,
    signal_ema: Ema,
    prev_ema3: Option<f64>,
    period: usize,
    signal_period: usize,
}

impl Trix {
    pub fn new(period: usize, signal_period: usize) -> Self {
        Self {
            ema1: Ema::new(period),
            ema2: Ema::new(period),
            ema3: Ema::new(period),
            signal_ema: Ema::new(signal_period),
            prev_ema3: None,
            period,
            signal_period,
        }
    }

    pub fn description() -> &'static str {
        "Triple-smoothed EMA oscillator — rate of change of a triple EMA. Filters short-term cycles; histogram crossovers signal momentum shifts."
    }

    pub fn update(&mut self, close: f64) -> Option<TrixValue> {
        let e1 = self.ema1.update(close)?;
        let e2 = self.ema2.update(e1)?;
        let e3 = self.ema3.update(e2)?;

        let trix = match self.prev_ema3 {
            Some(prev) if prev != 0.0 => (e3 - prev) / prev * 100.0,
            Some(_) => 0.0,
            None => {
                self.prev_ema3 = Some(e3);
                return None;
            }
        };
        self.prev_ema3 = Some(e3);

        let signal = self.signal_ema.update(trix)?;

        Some(TrixValue {
            trix,
            signal,
            histogram: trix - signal,
        })
    }

    pub fn reset(&mut self) {
        self.ema1 = Ema::new(self.period);
        self.ema2 = Ema::new(self.period);
        self.ema3 = Ema::new(self.period);
        self.signal_ema = Ema::new(self.signal_period);
        self.prev_ema3 = None;
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_trix_produces_values() {
        let mut trix = Trix::new(3, 2);
        let mut got = false;
        for i in 0..20 {
            if trix.update(100.0 + i as f64).is_some() {
                got = true;
            }
        }
        assert!(got, "TRIX(3,2) should produce values within 20 bars");
    }

    #[test]
    fn test_trix_uptrend_positive() {
        let mut trix = Trix::new(3, 2);
        let mut last = None;
        for i in 0..30 {
            last = trix.update(100.0 + i as f64 * 2.0);
        }
        let v = last.unwrap();
        assert!(v.trix > 0.0, "TRIX should be positive in uptrend: {}", v.trix);
    }

    #[test]
    fn test_trix_reset() {
        let mut trix = Trix::new(3, 2);
        for i in 0..20 {
            trix.update(100.0 + i as f64);
        }
        trix.reset();
        assert!(trix.update(100.0).is_none());
    }
}

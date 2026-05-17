use crate::{Atr, Ema};

/// Keltner Channel — dải động lực dựa trên EMA ± ATR.
///
/// Được Chester Keltner phát triển ban đầu (1960), sau đó được Linda Raschke
/// cải tiến (dùng EMA + ATR thay cho MA + daily range). Dải Keltner co/nở theo
/// volatility thực tế (ATR), không bị ảnh hưởng bởi một spike đơn lẻ như Donchian.
///
/// # Công thức
/// ```text
/// Middle = EMA(close, period)
/// Upper  = Middle + multiplier × ATR(atr_period)
/// Lower  = Middle − multiplier × ATR(atr_period)
/// ```
///
/// # Tham số thông dụng
/// - **Trend-following**: EMA(20), ATR(10), multiplier=2.0
/// - **Squeeze detection**: EMA(20), ATR(20), multiplier=1.5
///
/// # Tín hiệu giao dịch
/// - **Giá đóng trên Upper**: uptrend momentum mạnh (breakout)
/// - **Giá đóng dưới Lower**: downtrend momentum mạnh
/// - **Giá quay lại Middle**: mean-reversion target
///
/// # Keltner Squeeze (kết hợp Bollinger Bands)
/// Khi Bollinger Bands nằm *bên trong* Keltner Channel → Squeeze:
/// volatility cực thấp, sắp có breakout mạnh.
/// Bollinger nở ra ngoài Keltner → Squeeze kết thúc, trend bắt đầu.
///
/// # So sánh với Bollinger Bands
/// - Bollinger: dựa trên stddev → nhạy với price spike ngắn hạn
/// - Keltner: dựa trên ATR → mượt hơn, ít bị distort bởi spike đơn lẻ
///
/// # Warmup
/// Cần `max(period, atr_period)` bar (EMA và ATR warm up song song).
#[derive(Debug, Clone)]
pub struct KeltnerValue {
    /// Middle band: EMA của close
    pub middle: f64,
    /// Upper band: Middle + multiplier × ATR
    pub upper: f64,
    /// Lower band: Middle − multiplier × ATR
    pub lower: f64,
}

/// Keltner Channel — EMA ± multiplier×ATR.
#[allow(missing_docs)]
#[derive(Clone)]
pub struct Keltner {
    ema: Ema,
    atr: Atr,
    multiplier: f64,
    period: usize,
    atr_period: usize,
}

impl Keltner {
    pub fn new(period: usize, atr_period: usize, multiplier: f64) -> Self {
        Self {
            ema: Ema::new(period),
            atr: Atr::new(atr_period),
            multiplier,
            period,
            atr_period,
        }
    }

    pub fn description() -> &'static str {
        "Keltner Channel — EMA ± ATR multiplier. Wider and smoother than Bollinger Bands; often used together to identify BB squeezes inside the Keltner."
    }

    pub fn update(&mut self, high: f64, low: f64, close: f64) -> Option<KeltnerValue> {
        let mid = self.ema.update(close)?;
        let atr = self.atr.update(high, low, close)?;
        let band = self.multiplier * atr.atr;
        Some(KeltnerValue {
            middle: mid,
            upper: mid + band,
            lower: mid - band,
        })
    }

    pub fn reset(&mut self) {
        self.ema = Ema::new(self.period);
        self.atr = Atr::new(self.atr_period);
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_keltner_symmetry() {
        let mut kc = Keltner::new(3, 3, 2.0);
        let mut last = None;
        for i in 0..10 {
            let p = 100.0 + i as f64;
            last = kc.update(p + 1.0, p - 1.0, p);
        }
        let v = last.unwrap();
        // Upper > middle > lower
        assert!(v.upper > v.middle, "upper > middle");
        assert!(v.middle > v.lower, "middle > lower");
    }

    #[test]
    fn test_keltner_constant_price_zero_atr() {
        let mut kc = Keltner::new(3, 3, 2.0);
        let mut last = None;
        for _ in 0..10 {
            last = kc.update(100.0, 100.0, 100.0);
        }
        let v = last.unwrap();
        // Zero ATR → upper = lower = middle
        assert!((v.upper - v.middle).abs() < 1e-9);
        assert!((v.lower - v.middle).abs() < 1e-9);
    }
}

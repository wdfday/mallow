use crate::Ema;

#[derive(Debug, Clone, Copy)]
pub struct ElderRayValue {
    pub ema: f64,
    /// Bull Power = High - EMA (positive = bulls in control)
    pub bull_power: f64,
    /// Bear Power = Low - EMA  (negative = bears in control)
    pub bear_power: f64,
}

/// Elder Ray Index — tách biệt sức mạnh của phe mua và phe bán.
///
/// Được Dr. Alexander Elder phát triển và trình bày trong cuốn *Trading for a Living*
/// (1993). Elder Ray đo khoảng cách từ High/Low đến EMA: Bulls kiểm soát khi có
/// thể đẩy High lên cao hơn EMA; Bears kiểm soát khi kéo Low xuống dưới EMA.
///
/// # Công thức
/// ```text
/// EMA       = EMA(close, period)
/// Bull Power = High  − EMA    (>0: high trên EMA — bulls đang "đẩy cao")
/// Bear Power = Low   − EMA    (<0: low dưới EMA — bears đang "kéo xuống")
/// ```
///
/// # Cách đọc tín hiệu (Elder's Triple Screen system)
/// **Long entry** (tất cả phải đúng):
/// 1. Timeframe dài hơn: EMA đang tăng (uptrend)
/// 2. Bear Power < 0 (bears chưa bị đè hoàn toàn)
/// 3. Bear Power đang tăng dần (bears đang yếu đi → phe mua lấy lại sức)
/// 4. Bull Power không phải mức âm mới
///
/// **Short entry** (tất cả phải đúng):
/// 1. Timeframe dài hơn: EMA đang giảm (downtrend)
/// 2. Bull Power > 0 (bulls chưa bị diệt hoàn toàn)
/// 3. Bull Power đang giảm dần
/// 4. Bear Power không phải mức dương mới
///
/// # Warmup
/// Cần `period` bar để EMA warm up.
#[derive(Debug, Clone)]
pub struct ElderRay {
    ema: Ema,
    period: usize,
}

impl ElderRay {
    pub fn new(period: usize) -> Self {
        Self { ema: Ema::new(period), period }
    }

    pub fn description() -> &'static str {
        "Elder Ray — separates price into bull power (high - EMA) and bear power (low - EMA). Used with a trend filter to confirm entries in Elder's Triple Screen system."
    }

    pub fn update(&mut self, high: f64, low: f64, close: f64) -> Option<ElderRayValue> {
        let ema = self.ema.update(close)?;
        Some(ElderRayValue {
            ema,
            bull_power: high - ema,
            bear_power: low - ema,
        })
    }

    pub fn reset(&mut self) {
        self.ema = Ema::new(self.period);
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_elder_ray_signs() {
        let mut er = ElderRay::new(3);
        er.update(11.0, 9.0, 10.0);
        er.update(12.0, 10.0, 11.0);
        let v = er.update(13.0, 11.0, 12.0).unwrap();
        // In uptrend, close > ema so bull_power could be negative or positive depending on H vs EMA
        assert!(v.bull_power >= v.bear_power); // Bull >= Bear always (high >= low)
    }
}

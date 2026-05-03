use crate::{Atr, Ema};

/// Chop Zone — phân loại thị trường thành trending/choppy dựa trên góc của EMA.
///
/// Khác với **Choppiness Index** (CHOP) đo volatility tổng thể, Chop Zone
/// dùng **góc nghiêng của EMA** để phân loại — phản ánh tốc độ và hướng
/// thay đổi của xu hướng trực quan hơn.
///
/// # Công thức
/// ```text
/// EMA = EMA(close, ema_period)     (mặc định 34)
/// ATR = ATR(1)                     (single-bar true range)
///
/// angle = atan((EMA(t) − EMA(t-1)) / ATR(t)) × (180 / π)
/// ```
/// Chia `ema_change` cho `ATR` để chuẩn hoá theo volatility hiện tại —
/// cùng một mức thay đổi EMA nhưng ATR lớn → thị trường choppy → angle nhỏ.
///
/// # Phân loại (ChopZone)
/// ```text
/// angle > 5°   : trending up    (🟢)
/// angle < -5°  : trending down  (🔴)
/// |angle| ≤ 5° : choppy/sideways (🟡)
/// ```
/// Threshold ±5° là giá trị TradingView dùng; có thể điều chỉnh.
///
/// # Khác biệt với CHOP Index
/// | Indicator   | Dựa trên         | Output          |
/// |-------------|------------------|-----------------|
/// | CHOP        | ATR vs range     | 0-100 scalar    |
/// | Chop Zone   | EMA angle vs ATR | discrete zone   |
///
/// Chop Zone phản ứng nhanh hơn CHOP và cho biết **hướng** của trend.
#[derive(Debug, Clone)]
pub struct ChopZone {
    ema: Ema,
    atr: Atr,
    /// EMA giá trước để tính delta
    prev_ema: Option<f64>,
    /// Threshold góc (degree) để phân loại
    threshold: f64,
}

/// Phân loại thị trường từ Chop Zone.
#[derive(Debug, Clone, Copy, PartialEq)]
pub enum ChopZoneClass {
    /// Trending up: EMA đang nghiêng lên rõ ràng
    TrendingUp,
    /// Trending down: EMA đang nghiêng xuống rõ ràng
    TrendingDown,
    /// Choppy/sideways: EMA gần phẳng
    Choppy,
}

/// Kết quả Chop Zone.
#[derive(Debug, Clone, Copy)]
pub struct ChopZoneValue {
    /// Góc nghiêng của EMA (độ, -90 đến +90)
    pub angle: f64,
    /// Phân loại thị trường
    pub zone: ChopZoneClass,
}

impl ChopZone {
    pub fn new(ema_period: usize, threshold_degrees: f64) -> Self {
        Self {
            ema: Ema::new(ema_period),
            atr: Atr::new(1), // ATR(1) = single-bar true range
            prev_ema: None,
            threshold: threshold_degrees,
        }
    }

    /// ChopZone với EMA(34) và threshold ±5° — cấu hình TradingView mặc định.
    pub fn standard() -> Self {
        Self::new(34, 5.0)
    }

    pub fn update(&mut self, high: f64, low: f64, close: f64) -> Option<ChopZoneValue> {
        let ema_val = self.ema.update(close)?;
        let atr_val = self.atr.update(high, low, close)?;

        let result = if let Some(prev) = self.prev_ema {
            let ema_change = ema_val - prev;

            // Chuẩn hoá: chia cho ATR để scale-independent
            let angle = if atr_val.atr > f64::EPSILON {
                (ema_change / atr_val.atr).atan() * (180.0 / std::f64::consts::PI)
            } else {
                0.0
            };

            let zone = if angle > self.threshold {
                ChopZoneClass::TrendingUp
            } else if angle < -self.threshold {
                ChopZoneClass::TrendingDown
            } else {
                ChopZoneClass::Choppy
            };

            Some(ChopZoneValue { angle, zone })
        } else {
            None
        };

        self.prev_ema = Some(ema_val);
        result
    }

    pub fn is_ready(&self) -> bool {
        self.prev_ema.is_some() && self.ema.is_ready()
    }

    pub fn reset(&mut self) {
        self.ema.reset();
        self.atr.reset();
        self.prev_ema = None;
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_chop_zone_uptrend_classified() {
        let mut cz = ChopZone::new(5, 5.0);
        let mut last = None;
        for i in 0..30 {
            let base = 100.0 + i as f64;
            last = cz.update(base + 1.0, base - 1.0, base);
        }
        let v = last.unwrap();
        assert_eq!(v.zone, ChopZoneClass::TrendingUp, "angle={:.2}", v.angle);
    }

    #[test]
    fn test_chop_zone_downtrend_classified() {
        let mut cz = ChopZone::new(5, 5.0);
        let mut last = None;
        for i in 0..30 {
            let base = 200.0 - i as f64;
            last = cz.update(base + 1.0, base - 1.0, base);
        }
        let v = last.unwrap();
        assert_eq!(v.zone, ChopZoneClass::TrendingDown, "angle={:.2}", v.angle);
    }

    #[test]
    fn test_chop_zone_flat_choppy() {
        let mut cz = ChopZone::new(5, 5.0);
        let mut last = None;
        for _ in 0..30 {
            last = cz.update(101.0, 99.0, 100.0);
        }
        let v = last.unwrap();
        assert_eq!(v.zone, ChopZoneClass::Choppy, "angle={:.2}", v.angle);
    }

    #[test]
    fn test_chop_zone_angle_finite() {
        let mut cz = ChopZone::standard();
        let mut last = None;
        for i in 0..50 {
            let base = 100.0 + i as f64 * 0.2;
            last = cz.update(base + 1.0, base - 1.0, base);
        }
        assert!(last.unwrap().angle.is_finite());
    }

    #[test]
    fn test_chop_zone_reset() {
        let mut cz = ChopZone::standard();
        for i in 0..50 {
            cz.update(100.0 + i as f64, 99.0 + i as f64, 100.0 + i as f64);
        }
        assert!(cz.is_ready());
        cz.reset();
        assert!(!cz.is_ready());
    }
}

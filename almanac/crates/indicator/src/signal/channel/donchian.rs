use crate::util::{RollingMax, RollingMin};

/// Donchian Channel — kênh giá dựa trên highest high / lowest low trong N bar.
///
/// Được Richard Donchian phát triển trong thập niên 1950, nổi tiếng qua chiến lược
/// Turtle Trading. Là indicator cơ bản nhất cho breakout trading: giá vượt upper
/// channel = breakout upward; giá phá lower channel = breakout downward.
///
/// # Công thức
/// ```text
/// Upper  = max(High₀, High₁, …, Highₙ₋₁)    ← highest high trong period bar
/// Lower  = min(Low₀,  Low₁,  …, Lowₙ₋₁)     ← lowest low trong period bar
/// Middle = (Upper + Lower) / 2
/// ```
///
/// # Tín hiệu giao dịch
/// - **Giá đóng cửa > Upper**: breakout upward → long signal
/// - **Giá đóng cửa < Lower**: breakout downward → short signal
/// - **Middle**: dynamic support/resistance trong ranging market
/// - **Channel width** (Upper − Lower): đo volatility; hẹp = consolidation → breakout sắp xảy ra
///
/// # Turtle Trading rules (Richard Dennis)
/// - Entry: breakout khỏi Donchian(20)
/// - Exit: reverse breakout của Donchian(10)
///
/// # So sánh với Bollinger Bands và Keltner Channel
/// - Donchian: dựa trên price extremes (cứng), dễ bị breakout giả
/// - Bollinger: dựa trên stddev (mềm), co/nở theo volatility
/// - Keltner: dựa trên ATR (mềm), ít bị ảnh hưởng bởi spike đơn lẻ
///
/// # Warmup
/// Cần đúng `period` bar.
#[derive(Debug, Clone, Copy)]
pub struct DonchianValue {
    /// Highest high trong period bar gần nhất
    pub upper: f64,
    /// Lowest low trong period bar gần nhất
    pub lower: f64,
    /// (Upper + Lower) / 2 — midpoint của kênh
    pub middle: f64,
}

/// Donchian Channel — kênh giá highest high / lowest low trong N bar.
#[derive(Debug, Clone)]
pub struct Donchian {
    max_high: RollingMax,
    min_low: RollingMin,
}

impl Donchian {
    pub fn new(period: usize) -> Self {
        assert!(period > 0, "Donchian period must be > 0");
        Self {
            max_high: RollingMax::new(period),
            min_low: RollingMin::new(period),
        }
    }

    pub fn description() -> &'static str {
        "Donchian Channel — highest high and lowest low over N bars with a midline. Breakout above upper or below lower band signals trend entries."
    }

    pub fn update(&mut self, high: f64, low: f64) -> Option<DonchianValue> {
        // Gọi cả hai push độc lập — không dùng `?` để tránh min_low bị bỏ qua
        // khi max_high chưa ready (cùng bug cascade như GMMA).
        let upper = self.max_high.push(high);
        let lower = self.min_low.push(low);
        match (upper, lower) {
            (Some(u), Some(l)) => Some(DonchianValue { upper: u, lower: l, middle: (u + l) / 2.0 }),
            _ => None,
        }
    }

    pub fn reset(&mut self) {
        self.max_high.reset();
        self.min_low.reset();
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_donchian_upper_lower() {
        let mut d = Donchian::new(3);
        d.update(10.0, 5.0);
        d.update(12.0, 4.0);
        let v = d.update(11.0, 6.0).unwrap();
        assert_eq!(v.upper, 12.0);
        assert_eq!(v.lower, 4.0);
        assert_eq!(v.middle, 8.0);
    }

    #[test]
    fn test_donchian_rolling() {
        let mut d = Donchian::new(2);
        d.update(10.0, 5.0);
        let v = d.update(8.0, 3.0).unwrap();
        assert_eq!(v.upper, 10.0);
        // Old bar rolls out
        let v2 = d.update(9.0, 6.0).unwrap();
        assert_eq!(v2.upper, 9.0);
        assert_eq!(v2.lower, 3.0);
    }
}

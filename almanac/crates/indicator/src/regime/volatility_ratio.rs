use std::collections::VecDeque;

/// Volatility Ratio (Schwager) — đo mức độ "bùng nổ" của bar hiện tại so với range lịch sử.
///
/// Được Jack Schwager mô tả trong *Schwager on Futures*. VR so sánh True Range của bar
/// hiện tại với khoảng dao động tổng (HH − LL) trong `lookback` bar gần đây. Khi VR cao
/// → bar hiện tại "kéo dài" gần bằng toàn bộ range lịch sử → explosive move / breakout.
///
/// # Công thức
/// ```text
/// TR    = max(High − Low, |High − prev_Close|, |Low − prev_Close|)
/// HH    = max(High₀, High₁, …, Highₙ₋₁)    ← highest high trong lookback bar
/// LL    = min(Low₀,  Low₁,  …, Lowₙ₋₁)     ← lowest low
///
/// VR = TR / (HH − LL)
/// ```
///
/// # Diễn giải
/// - **VR ≈ 1.0**: bar hiện tại có TR gần bằng toàn bộ lookback range → explosive breakout
/// - **VR ≈ 0.0**: bar hiện tại rất nhỏ so với range lịch sử → consolidation
/// - **VR > 0.5**: ngưỡng thông thường cho breakout signal
///
/// # Ứng dụng
/// - Lọc breakout: chỉ trade khi VR > threshold để tránh false breakout
/// - Volatility regime detection: VR thấp liên tục → sắp có biến động lớn
/// - Kết hợp với Donchian Channel: giá vượt channel AND VR > 0.5 → breakout đáng tin
///
/// # Warmup
/// Cần `lookback` bar để có đủ dữ liệu HH/LL.
#[derive(Clone)]
pub struct VolatilityRatio {
    lookback: usize,
    highs: VecDeque<f64>,
    lows: VecDeque<f64>,
    prev_close: Option<f64>,
}

impl VolatilityRatio {
    pub fn new(lookback: usize) -> Self {
        assert!(lookback >= 2);
        Self {
            lookback,
            highs: VecDeque::with_capacity(lookback),
            lows: VecDeque::with_capacity(lookback),
            prev_close: None,
        }
    }

    pub fn description() -> &'static str {
        "Volatility Ratio (Schwager) — current true range divided by the N-bar high-low range. Values near 1 indicate an explosive bar (potential breakout); near 0 = consolidation. Outputs a single value (>1 = expanding volatility)."
    }

    pub fn update(&mut self, high: f64, low: f64, close: f64) -> Option<f64> {
        let tr = match self.prev_close {
            Some(pc) => (high - low).max((high - pc).abs()).max((low - pc).abs()),
            None => high - low,
        };
        self.prev_close = Some(close);

        self.highs.push_back(high);
        self.lows.push_back(low);

        if self.highs.len() > self.lookback {
            self.highs.pop_front();
            self.lows.pop_front();
        }

        if self.highs.len() < self.lookback {
            return None;
        }

        let hh = self.highs.iter().cloned().fold(f64::NEG_INFINITY, f64::max);
        let ll = self.lows.iter().cloned().fold(f64::INFINITY, f64::min);
        let range = hh - ll;

        if range == 0.0 {
            return Some(0.0);
        }

        Some(tr / range)
    }

    pub fn reset(&mut self) {
        self.highs.clear();
        self.lows.clear();
        self.prev_close = None;
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_vr_bounds() {
        let mut vr = VolatilityRatio::new(5);
        let mut last = None;
        for i in 0..10 {
            let p = 100.0 + i as f64;
            last = vr.update(p + 1.5, p - 1.5, p);
        }
        let v = last.unwrap();
        assert!(v >= 0.0, "VR must be non-negative: {v}");
    }

    #[test]
    fn test_vr_flat_market_near_zero() {
        // All same prices → TR tiny but range also tiny → can be near 1 or 0
        let mut vr = VolatilityRatio::new(5);
        let mut last = None;
        for _ in 0..10 {
            last = vr.update(100.0, 100.0, 100.0);
        }
        // Flat: TR = 0, range = 0 → returns 0.0
        assert_eq!(last.unwrap(), 0.0);
    }

    #[test]
    fn test_vr_explosive_move_near_one() {
        // One big move after flat consolidation → TR ≈ full range → VR near 1
        let mut vr = VolatilityRatio::new(5);
        for _ in 0..4 {
            vr.update(100.0, 99.0, 99.5);
        }
        // Explosive bar that spans entire 5-bar range
        let v = vr.update(110.0, 90.0, 100.0).unwrap();
        assert!(v > 0.5, "explosive move VR should be > 0.5: {v}");
    }
}

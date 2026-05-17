use crate::Ema;

#[derive(Debug, Clone)]
pub struct HaBar {
    pub open: f64,
    pub high: f64,
    pub low: f64,
    pub close: f64,
    pub is_bullish: bool,
}

/// Heiken Ashi — nến Nhật biến thể làm mượt nhiễu.
///
/// "Heiken Ashi" tiếng Nhật nghĩa là "thanh trung bình". HA biến đổi OHLC truyền
/// thống thành dạng nến làm mượt, giúp xu hướng dễ nhìn hơn: nến HA xanh liên tục
/// = uptrend; nến HA đỏ liên tục = downtrend. Không có upper/lower shadow = trend mạnh.
///
/// # Công thức
/// ```text
/// HA_Close = (Open + High + Low + Close) / 4
/// HA_Open  = (prev_HA_Open + prev_HA_Close) / 2   (lần đầu = (Open+Close)/2)
/// HA_High  = max(High, HA_Open, HA_Close)
/// HA_Low   = min(Low,  HA_Open, HA_Close)
/// ```
///
/// # Smooth Heiken Ashi
/// Khi `smooth > 1`, từng thành phần OHLC được EMA(smooth) trước khi tính HA.
/// Kết quả mượt hơn nhiều nhưng lag nhiều hơn. Dùng cho signal confirmation hơn
/// là timing entry chính xác.
///
/// # Cách đọc tín hiệu
/// - **Nến xanh không có lower shadow**: uptrend mạnh — không bán
/// - **Nến đỏ không có upper shadow**: downtrend mạnh — không mua
/// - **Nến nhỏ có cả hai shadow**: consolidation / potential reversal
/// - **Màu đổi từ đỏ → xanh**: reversal signal (không chính xác như candlestick thật)
///
/// # Hạn chế
/// - HA không phản ánh giá thực → không thể dùng để xác định entry/exit giá chính xác
/// - Chỉ dùng để phân tích xu hướng; cần nến thật để đặt lệnh
///
/// # Warmup
/// Standard (smooth=1): trả về ngay từ bar đầu tiên (seed bar).
/// Smooth > 1: cần `smooth × 4 - 3` bar do cascade EMA của 4 components.
#[derive(Debug, Clone)]
pub struct HeikenAshi {
    smooth: usize,
    ema_open: Option<Ema>,
    ema_high: Option<Ema>,
    ema_low: Option<Ema>,
    ema_close: Option<Ema>,
    prev_ha_open: Option<f64>,
    prev_ha_close: Option<f64>,
}

impl HeikenAshi {
    /// `smooth = 1` → standard Heiken Ashi, no EMA smoothing.
    pub fn new(smooth: usize) -> Self {
        let smooth = smooth.max(1);
        let (eo, eh, el, ec) = if smooth > 1 {
            (
                Some(Ema::new(smooth)),
                Some(Ema::new(smooth)),
                Some(Ema::new(smooth)),
                Some(Ema::new(smooth)),
            )
        } else {
            (None, None, None, None)
        };
        Self {
            smooth,
            ema_open: eo,
            ema_high: eh,
            ema_low: el,
            ema_close: ec,
            prev_ha_open: None,
            prev_ha_close: None,
        }
    }

    pub fn description() -> &'static str {
        "Heikin-Ashi — modified candlestick using averaged OHLC values to smooth price action. Consecutive same-colour bars confirm a trend; doji-like bars signal potential reversal."
    }

    pub fn update(&mut self, open: f64, high: f64, low: f64, close: f64) -> Option<HaBar> {
        // Smooth input via EMA if configured
        let (so, sh, sl, sc) = if self.smooth > 1 {
            let so = self.ema_open.as_mut().unwrap().update(open)?;
            let sh = self.ema_high.as_mut().unwrap().update(high)?;
            let sl = self.ema_low.as_mut().unwrap().update(low)?;
            let sc = self.ema_close.as_mut().unwrap().update(close)?;
            (so, sh, sl, sc)
        } else {
            (open, high, low, close)
        };

        let ha_close = (so + sh + sl + sc) / 4.0;

        let ha_open = match (self.prev_ha_open, self.prev_ha_close) {
            (Some(po), Some(pc)) => (po + pc) / 2.0,
            _ => (so + sc) / 2.0, // seed on first bar
        };

        let ha_high = sh.max(ha_open).max(ha_close);
        let ha_low = sl.min(ha_open).min(ha_close);

        self.prev_ha_open = Some(ha_open);
        self.prev_ha_close = Some(ha_close);

        Some(HaBar {
            open: ha_open,
            high: ha_high,
            low: ha_low,
            close: ha_close,
            is_bullish: ha_close >= ha_open,
        })
    }

    pub fn reset(&mut self) {
        if self.smooth > 1 {
            self.ema_open = Some(Ema::new(self.smooth));
            self.ema_high = Some(Ema::new(self.smooth));
            self.ema_low = Some(Ema::new(self.smooth));
            self.ema_close = Some(Ema::new(self.smooth));
        }
        self.prev_ha_open = None;
        self.prev_ha_close = None;
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_ha_standard_first_bar() {
        let mut ha = HeikenAshi::new(1);
        let v = ha.update(10.0, 12.0, 9.0, 11.0).unwrap();
        // HA_Close = (10+12+9+11)/4 = 10.5
        assert!((v.close - 10.5).abs() < 1e-9, "HA close = {}", v.close);
        // HA_Open (seed) = (open+close)/2 = (10+11)/2 = 10.5
        assert!((v.open - 10.5).abs() < 1e-9, "HA open = {}", v.open);
    }

    #[test]
    fn test_ha_bullish_uptrend() {
        let mut ha = HeikenAshi::new(1);
        let mut last = None;
        for i in 0..10 {
            let o = 100.0 + i as f64;
            let c = o + 2.0;
            last = ha.update(o, c + 1.0, o - 0.5, c);
        }
        let v = last.unwrap();
        assert!(v.is_bullish, "HA should be bullish in uptrend");
    }

    #[test]
    fn test_ha_smooth_requires_warmup() {
        // Due to cascading ? (ema_open → ema_high → ema_low → ema_close), smooth=3 needs
        // 3 + 2 + 2 + 2 = 9 bars before first value. Verify it eventually produces one.
        let mut ha = HeikenAshi::new(3);
        let mut got = false;
        for i in 0..15 {
            let p = 10.0 + i as f64;
            if ha.update(p, p + 1.0, p - 1.0, p + 0.5).is_some() {
                got = true;
            }
        }
        assert!(got, "smooth=3 HA should produce a value within 15 bars");
    }

    #[test]
    fn test_ha_high_ge_open_close_and_low_le() {
        let mut ha = HeikenAshi::new(1);
        let v = ha.update(10.0, 15.0, 8.0, 12.0).unwrap();
        assert!(v.high >= v.open && v.high >= v.close);
        assert!(v.low <= v.open && v.low <= v.close);
    }
}

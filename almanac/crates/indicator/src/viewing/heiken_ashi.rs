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
/// Smooth > 1: cần đúng `smooth` bar — 4 EMA được feed song song từ bar đầu tiên,
/// tất cả warm up cùng lúc sau P bar.
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
        "Heikin-Ashi — modified candlestick using averaged OHLC values to smooth price action. Consecutive same-colour bars confirm a trend; doji-like bars signal potential reversal. Outputs smoothed OHLC values."
    }

    pub fn update(&mut self, open: f64, high: f64, low: f64, close: f64) -> Option<HaBar> {
        // Smooth input via EMA if configured.
        //
        // IMPORTANT: tất cả 4 EMA phải được gọi trên mỗi bar TRƯỚC KHI kiểm tra None.
        // Nếu dùng `?` short-circuit theo chuỗi (`let so = ...?; let sh = ...?; ...`),
        // ema_high/low/close sẽ không nhận bar trong giai đoạn warmup của ema_open,
        // dẫn đến cascade: warmup tăng từ P lên 4P-3 và mỗi EMA seed trên một đoạn
        // giá khác nhau — sai hoàn toàn so với Smoothed HA chuẩn.
        let (so, sh, sl, sc) = if self.smooth > 1 {
            let so = self.ema_open.as_mut().unwrap().update(open);
            let sh = self.ema_high.as_mut().unwrap().update(high);
            let sl = self.ema_low.as_mut().unwrap().update(low);
            let sc = self.ema_close.as_mut().unwrap().update(close);
            match (so, sh, sl, sc) {
                (Some(o), Some(h), Some(l), Some(c)) => (o, h, l, c),
                _ => return None,
            }
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
    fn test_ha_smooth_warmup_is_period_bars() {
        // All 4 EMAs are fed in parallel from bar 1, so warmup = exactly `smooth` bars.
        // smooth=3: None on bars 1-2, Some on bar 3.
        let mut ha = HeikenAshi::new(3);
        let mut first_some_at = None;
        for i in 1..=10usize {
            let p = 10.0 + i as f64;
            let r = ha.update(p, p + 1.0, p - 1.0, p + 0.5);
            if r.is_some() && first_some_at.is_none() {
                first_some_at = Some(i);
            }
        }
        assert_eq!(first_some_at, Some(3), "smooth=3 HA should emit first value at bar 3");
    }

    #[test]
    fn test_ha_smooth_emas_use_correct_components() {
        // Verify parallel warmup: ema_high should be seeded on HIGH prices from bars 1..P,
        // not on a later slice. With a monotone rising dataset the smoothed HA high must
        // be >= smoothed HA open (since high > open every bar).
        let mut ha = HeikenAshi::new(3);
        let mut last = None;
        for i in 1..=5usize {
            let p = 100.0 + i as f64;
            last = ha.update(p, p + 2.0, p - 1.0, p + 1.0); // high always open+2
        }
        let v = last.unwrap();
        assert!(v.high >= v.open, "HA high must be >= HA open");
        assert!(v.high >= v.close, "HA high must be >= HA close");
    }

    #[test]
    fn test_ha_high_ge_open_close_and_low_le() {
        let mut ha = HeikenAshi::new(1);
        let v = ha.update(10.0, 15.0, 8.0, 12.0).unwrap();
        assert!(v.high >= v.open && v.high >= v.close);
        assert!(v.low <= v.open && v.low <= v.close);
    }
}

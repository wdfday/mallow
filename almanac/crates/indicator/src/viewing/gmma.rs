use crate::Ema;

/// Guppy Multiple Moving Average (GMMA) — phân tích tâm lý trader vs. investor.
///
/// Được Daryl Guppy phát triển tại Úc. GMMA dùng 12 EMA chia thành 2 nhóm:
/// - **Short group** (3,5,8,10,12,15): phản ánh hành vi của short-term trader
/// - **Long group** (30,35,40,45,50,60): phản ánh hành vi của long-term investor
///
/// Khi hai nhóm phân kỳ rộng rãi → trend mạnh; khi hai nhóm co lại gần nhau →
/// trend yếu dần, khả năng đảo chiều.
///
/// # Đọc tín hiệu
/// - **Short group vượt hẳn lên trên Long group**: bullish trend xác nhận
/// - **Short group cắt xuống dưới Long group**: bearish trend xác nhận
/// - **Short group "co" lại (spread hẹp)**: trader đang do dự, trend không chắc
/// - **Long group "nở" rộng**: investor trend mạnh, ít có khả năng đảo chiều
/// - **Two groups cross đi cross lại nhiều**: thị trường sideways → tránh giao dịch
///
/// # Bullish alignment
/// `bullish = true` khi min(short group) > max(long group) — tức short group
/// hoàn toàn nằm trên long group (không chồng lấn). Đây là điều kiện ideal cho
/// trend-following entry.
///
/// # Warmup
/// Do cascade EMA (mỗi EMA chỉ nhận input sau khi EMA trước ready), warmup
/// thực tế ≈ 3 + 4 + 7 + 9 + 11 + 14 + 29 + 34 + 39 + 44 + 49 + 59 ≈ 302 bar.
#[derive(Debug, Clone)]
pub struct GmmaValue {
    /// Nhóm ngắn hạn: EMA(3,5,8,10,12,15) — tâm lý short-term trader
    pub short: [f64; 6],
    /// Nhóm dài hạn: EMA(30,35,40,45,50,60) — tâm lý long-term investor
    pub long: [f64; 6],
    /// `true` khi toàn bộ short group nằm trên toàn bộ long group (bullish alignment)
    pub bullish: bool,
}

/// Guppy Multiple Moving Average — 12 EMA chia 2 nhóm trader/investor.
#[allow(missing_docs)]
#[derive(Clone)]
pub struct Gmma {
    short_emas: [Ema; 6],
    long_emas: [Ema; 6],
}

const SHORT_PERIODS: [usize; 6] = [3, 5, 8, 10, 12, 15];
const LONG_PERIODS: [usize; 6] = [30, 35, 40, 45, 50, 60];

impl Gmma {
    /// Standard GMMA with default periods.
    pub fn new() -> Self {
        Self {
            short_emas: SHORT_PERIODS.map(Ema::new),
            long_emas: LONG_PERIODS.map(Ema::new),
        }
    }

    pub fn description() -> &'static str {
        "Guppy Multiple MA — twelve EMAs split into a short-term trader group (3-15) and a long-term investor group (30-60). The default output `.spread` is the normalised gap between the two group means: positive = bullish, negative = bearish, a zero-cross marks a group crossover, and the magnitude measures ribbon separation (wide = strong trend, near-zero = compression / possible reversal)."
    }

    /// Custom periods. `short` and `long` must each have exactly 6 elements.
    pub fn with_periods(short: [usize; 6], long: [usize; 6]) -> Self {
        Self {
            short_emas: short.map(Ema::new),
            long_emas: long.map(Ema::new),
        }
    }

    pub fn update(&mut self, close: f64) -> Option<GmmaValue> {
        let mut short_vals = [0.0f64; 6];
        let mut long_vals = [0.0f64; 6];

        // Feed tất cả EMA độc lập — không dùng `?` để tránh cascade short-circuit.
        // Bug cũ: `?` khiến EMA(5) không nhận data cho đến khi EMA(3) ready, v.v.
        // → warmup thực tế ~302 bar thay vì 60.
        let mut short_ready = true;
        let mut long_ready = true;

        for (i, ema) in self.short_emas.iter_mut().enumerate() {
            match ema.update(close) {
                Some(v) => short_vals[i] = v,
                None => short_ready = false,
            }
        }
        for (i, ema) in self.long_emas.iter_mut().enumerate() {
            match ema.update(close) {
                Some(v) => long_vals[i] = v,
                None => long_ready = false,
            }
        }

        if !short_ready || !long_ready {
            return None;
        }

        let short_min = short_vals.iter().cloned().fold(f64::INFINITY, f64::min);
        let long_max = long_vals.iter().cloned().fold(f64::NEG_INFINITY, f64::max);
        let bullish = short_min > long_max;

        Some(GmmaValue { short: short_vals, long: long_vals, bullish })
    }

    pub fn reset(&mut self) {
        self.short_emas = SHORT_PERIODS.map(Ema::new);
        self.long_emas = LONG_PERIODS.map(Ema::new);
    }
}

impl Default for Gmma {
    fn default() -> Self {
        Self::new()
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    // Warmup: longest EMA = EMA(60) → cần 60 bar. Tất cả EMA nhận data song song.

    #[test]
    fn test_gmma_ready_after_warmup() {
        let mut gmma = Gmma::new();
        let mut got = false;
        for i in 0..65 {
            if gmma.update(100.0 + i as f64).is_some() {
                got = true;
            }
        }
        assert!(got, "GMMA should produce values after ~60 bars warmup");
    }

    #[test]
    fn test_gmma_bullish_strong_uptrend() {
        let mut gmma = Gmma::new();
        let mut last = None;
        for i in 0..150 {
            last = gmma.update(100.0 + i as f64 * 3.0);
        }
        let v = last.unwrap();
        assert!(v.bullish, "GMMA should be bullish in strong sustained uptrend");
    }

    #[test]
    fn test_gmma_short_above_long_in_uptrend() {
        let mut gmma = Gmma::new();
        let mut last = None;
        for i in 0..150 {
            last = gmma.update(100.0 + i as f64 * 3.0);
        }
        let v = last.unwrap();
        let short_min = v.short.iter().cloned().fold(f64::INFINITY, f64::min);
        let long_max = v.long.iter().cloned().fold(f64::NEG_INFINITY, f64::max);
        assert!(short_min > long_max, "short group should be above long group");
    }
}

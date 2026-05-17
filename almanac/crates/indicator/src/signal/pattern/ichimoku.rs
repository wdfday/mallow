use std::collections::VecDeque;

#[derive(Debug, Clone)]
pub struct IchimokuValue {
    /// Tenkan-sen (Conversion Line): (highest_high + lowest_low) / 2 over `tenkan` periods.
    pub tenkan: f64,
    /// Kijun-sen (Base Line): (highest_high + lowest_low) / 2 over `kijun` periods.
    pub kijun: f64,
    /// Senkou Span A: (tenkan + kijun) / 2 — plotted `kijun` periods forward.
    pub senkou_a: f64,
    /// Senkou Span B: midpoint of `senkou_b` periods — plotted `kijun` periods forward.
    pub senkou_b: f64,
    /// Chikou Span: current close — plotted `kijun` periods back (returned as-is).
    pub chikou: f64,
    /// `true` when price is above the cloud (senkou_a and senkou_b).
    pub above_cloud: bool,
}

/// Ichimoku Kinko Hyo — hệ thống phân tích toàn diện trong một indicator.
///
/// "Ichimoku Kinko Hyo" tiếng Nhật nghĩa là "biểu đồ nhìn một lần cân bằng".
/// Được Goichi Hosoda (bút danh Ichimoku Sanjin) phát triển trong thập niên 1930–1960.
/// Là một trong số ít indicator cung cấp **hỗ trợ/kháng cự, momentum, xu hướng, và
/// tín hiệu** trong cùng một công cụ.
///
/// # Năm thành phần
/// ```text
/// Tenkan-sen (Conversion Line): (HH + LL) / 2 trong tenkan bar
///   → midpoint của range 9 bar, giống EMA nhưng dựa trên H/L
///
/// Kijun-sen (Base Line): (HH + LL) / 2 trong kijun bar
///   → midpoint của range 26 bar; hỗ trợ/kháng cự quan trọng
///
/// Senkou Span A: (Tenkan + Kijun) / 2 → plotted kijun bar về phía trước
///   → cạnh trên/dưới của cloud (mây)
///
/// Senkou Span B: (HH + LL) / 2 trong senkou_b bar → plotted kijun bar về phía trước
///   → cạnh còn lại của cloud
///
/// Chikou Span: Close hiện tại → plotted kijun bar về phía sau
///   → so sánh close hôm nay với giá kijun bar trước
/// ```
///
/// **Cloud (Kumo)**: vùng giữa Senkou A và B. Giá trên cloud = bullish;
/// dưới cloud = bearish; trong cloud = không rõ ràng (sideways).
///
/// **Note**: Implementation này trả về giá trị không displaced (không shift) để
/// backtesting signal-generation hoạt động đúng theo bar thực tế.
///
/// # Tín hiệu chính
/// - **TK Cross**: Tenkan cắt Kijun từ dưới → long signal (nếu giá trên cloud)
/// - **Giá phá cloud từ dưới lên**: bullish breakout
/// - **Chikou trên giá cách kijun bar**: xác nhận bullish
///
/// # Tham số chuẩn
/// - tenkan = 9: ngắn hạn
/// - kijun = 26: trung hạn (tương đương 1 tháng giao dịch)
/// - senkou_b = 52: dài hạn (tương đương 2 tháng)
///
/// # Warmup
/// Cần `senkou_b_period` bar (52 bar với tham số chuẩn).
#[derive(Clone)]
pub struct Ichimoku {
    tenkan: usize,
    kijun: usize,
    senkou_b_period: usize,
    highs: VecDeque<f64>,
    lows: VecDeque<f64>,
}

impl Ichimoku {
    pub fn new(tenkan: usize, kijun: usize, senkou_b_period: usize) -> Self {
        Self {
            tenkan,
            kijun,
            senkou_b_period,
            highs: VecDeque::new(),
            lows: VecDeque::new(),
        }
    }

    pub fn description() -> &'static str {
        "Ichimoku Cloud — five lines (Tenkan, Kijun, Senkou A/B, Chikou) forming a complete trend system. Price above the cloud = bullish; inside = neutral; below = bearish."
    }

    pub fn update(&mut self, high: f64, low: f64, close: f64) -> Option<IchimokuValue> {
        self.highs.push_back(high);
        self.lows.push_back(low);

        // Keep only as much history as needed
        while self.highs.len() > self.senkou_b_period {
            self.highs.pop_front();
            self.lows.pop_front();
        }

        if self.highs.len() < self.senkou_b_period {
            return None;
        }

        let tenkan = midpoint(&self.highs, &self.lows, self.tenkan)?;
        let kijun = midpoint(&self.highs, &self.lows, self.kijun)?;
        let senkou_a = (tenkan + kijun) / 2.0;
        let senkou_b = midpoint(&self.highs, &self.lows, self.senkou_b_period)?;

        let cloud_top = senkou_a.max(senkou_b);
        let cloud_bottom = senkou_a.min(senkou_b);
        let above_cloud = close > cloud_top;
        let _ = cloud_bottom;

        Some(IchimokuValue {
            tenkan,
            kijun,
            senkou_a,
            senkou_b,
            chikou: close,
            above_cloud,
        })
    }

    pub fn reset(&mut self) {
        self.highs.clear();
        self.lows.clear();
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_ichimoku_requires_senkou_b_bars() {
        // Standard: need senkou_b_period=52 bars before first value
        let mut ich = Ichimoku::new(9, 26, 52);
        for i in 0..51 {
            let p = 100.0 + i as f64;
            assert!(ich.update(p + 1.0, p - 1.0, p).is_none());
        }
        // 52nd bar should return Some
        assert!(ich.update(152.0, 150.0, 151.0).is_some());
    }

    #[test]
    fn test_ichimoku_above_cloud() {
        // Use small periods for speed
        let mut ich = Ichimoku::new(3, 5, 8);
        let mut last = None;
        // Strong uptrend: close well above all bands
        for i in 0..20 {
            let p = 100.0 + i as f64 * 10.0;
            last = ich.update(p + 1.0, p - 1.0, p);
        }
        let v = last.unwrap();
        // After strong uptrend, cloud should be well below current close
        assert!(v.above_cloud, "should be above cloud in strong uptrend");
    }

    #[test]
    fn test_ichimoku_tenkan_kijun_midpoint() {
        // Feed constant prices: tenkan = kijun = senkou_b = close
        let mut ich = Ichimoku::new(3, 5, 8);
        let mut last = None;
        for _ in 0..10 {
            last = ich.update(100.0, 100.0, 100.0);
        }
        let v = last.unwrap();
        assert!((v.tenkan - 100.0).abs() < 1e-9);
        assert!((v.kijun - 100.0).abs() < 1e-9);
        assert!((v.senkou_a - 100.0).abs() < 1e-9);
    }
}

/// Midpoint of the highest high and lowest low over the last `n` bars.
fn midpoint(highs: &VecDeque<f64>, lows: &VecDeque<f64>, n: usize) -> Option<f64> {
    let len = highs.len();
    if len < n {
        return None;
    }
    let start = len - n;
    let hh = highs.range(start..).cloned().fold(f64::NEG_INFINITY, f64::max);
    let ll = lows.range(start..).cloned().fold(f64::INFINITY, f64::min);
    Some((hh + ll) / 2.0)
}

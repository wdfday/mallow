use crate::Ema;

/// Giá trị snapshot của MACD.
#[derive(Debug, Clone, Copy, PartialEq)]
pub struct MacdValue {
    /// MACD Line: EMA(fast) − EMA(slow)
    pub macd: f64,
    /// Signal Line: EMA(MACD, signal_period)
    pub signal: f64,
    /// Histogram: macd − signal (dương khi momentum tăng)
    pub histogram: f64,
}

/// MACD (Moving Average Convergence/Divergence) — oscillator momentum kinh điển.
///
/// Được Gerald Appel phát triển cuối thập niên 1970. MACD đo động lượng bằng
/// cách so sánh hai EMA khác period — khi EMA nhanh cắt EMA chậm từ dưới lên,
/// momentum chuyển từ âm sang dương.
///
/// # Công thức (tham số cổ điển: fast=12, slow=26, signal=9)
/// ```text
/// MACD Line  = EMA(12) − EMA(26)
/// Signal     = EMA(MACD Line, 9)
/// Histogram  = MACD Line − Signal
/// ```
///
/// # Tín hiệu giao dịch
/// - **MACD cắt Signal từ dưới lên** → buy signal
/// - **MACD cắt Signal từ trên xuống** → sell signal
/// - **Histogram đổi từ âm sang dương** → tín hiệu sớm hơn crossover
/// - **MACD > 0** (cả MACD line > 0): EMA nhanh trên EMA chậm → bullish momentum
/// - **Divergence** MACD vs giá: tín hiệu đảo chiều mạnh nhất
///
/// # Warmup
/// Do cascading EMA: cần `slow + signal - 1` bar để signal EMA warm up.
/// Standard MACD(12,26,9): bar thứ ≈ 34 trả về giá trị đầu tiên.
///
/// # Điểm yếu
/// - Là lagging indicator (dựa trên EMA, không leading)
/// - Nhiều false signal trong thị trường sideways
/// - Nên kết hợp với ADX hoặc CHOP để lọc
#[derive(Debug, Clone)]
pub struct Macd {
    fast: Ema,
    slow: Ema,
    signal: Ema,
}

impl Macd {
    pub fn new(fast: usize, slow: usize, signal: usize) -> Self {
        assert!(fast < slow, "fast period must be < slow period");
        Self {
            fast: Ema::new(fast),
            slow: Ema::new(slow),
            signal: Ema::new(signal),
        }
    }

    pub fn description() -> &'static str {
        "Moving Average Convergence Divergence — difference between fast and slow EMAs, plus a signal line and histogram. Measures momentum and trend direction changes."
    }

    /// MACD(12, 26, 9) — tham số Gerald Appel gốc, phổ biến nhất.
    pub fn standard() -> Self {
        Self::new(12, 26, 9)
    }

    /// Feed một giá đóng cửa. Trả về `Some(MacdValue)` sau khi tất cả EMA warm up.
    ///
    /// Cả fast và slow EMA đều được feed *mọi bar* (song song),
    /// tránh lỗi cascading `?` làm slow chỉ chạy sau khi fast warm-up.
    pub fn update(&mut self, close: f64) -> Option<MacdValue> {
        let fast = self.fast.update(close);
        let slow = self.slow.update(close);
        let macd_line = fast? - slow?;
        let signal_line = self.signal.update(macd_line)?;
        Some(MacdValue {
            macd: macd_line,
            signal: signal_line,
            histogram: macd_line - signal_line,
        })
    }

    pub fn value(&self) -> Option<MacdValue> {
        let fast = self.fast.value()?;
        let slow = self.slow.value()?;
        let macd_line = fast - slow;
        let signal_line = self.signal.value()?;
        Some(MacdValue {
            macd: macd_line,
            signal: signal_line,
            histogram: macd_line - signal_line,
        })
    }

    pub fn is_ready(&self) -> bool {
        self.fast.is_ready() && self.slow.is_ready() && self.signal.is_ready()
    }

    pub fn reset(&mut self) {
        self.fast.reset();
        self.slow.reset();
        self.signal.reset();
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_macd_ready_after_warmup() {
        let mut macd = Macd::new(3, 6, 2);
        // EMA(3) seeds at bar 3, EMA(6) at bar 6, signal EMA(2) needs 2 more MACD values → bar 8
        let mut got = false;
        for i in 0..10 {
            if macd.update(100.0 + i as f64).is_some() {
                got = true;
            }
        }
        assert!(got, "should produce at least one value");
        assert!(macd.is_ready());
    }

    #[test]
    fn test_macd_uptrend_positive() {
        let mut macd = Macd::new(3, 6, 2);
        let mut last = None;
        for i in 0..20 {
            last = macd.update(100.0 + i as f64 * 2.0);
        }
        let v = last.unwrap();
        // In a strong uptrend fast EMA > slow EMA → positive MACD line
        assert!(v.macd > 0.0, "MACD line should be positive: {}", v.macd);
    }

    #[test]
    fn test_macd_reset_clears_state() {
        // Due to cascading ? short-circuits (fast→slow→signal), standard MACD(12,26,9)
        // needs 12 + (26-12) + (9-1) = ~45 bars to produce first value.
        let mut macd = Macd::standard();
        for i in 0..50 {
            macd.update(100.0 + i as f64);
        }
        assert!(macd.is_ready());
        macd.reset();
        assert!(!macd.is_ready());
        assert!(macd.update(100.0).is_none());
    }
}

/// Parabolic SAR (Stop and Reverse) — trailing stop tự động theo dạng parabol.
///
/// Được Welles Wilder phát triển năm 1978 (cùng RSI, ATR, ADX). SAR cung cấp
/// một đường trailing stop tự động "gia tốc" dần theo xu hướng. Khi xu hướng
/// đảo chiều, SAR nhảy sang phía bên kia (Stop AND Reverse → luôn trong thị trường).
///
/// # Công thức
/// ```text
/// SAR(t) = SAR(t-1) + AF × (EP − SAR(t-1))
///
/// AF  = Acceleration Factor, bắt đầu = initial_af, tăng thêm `step` mỗi khi EP mới
/// EP  = Extreme Point (highest high trong bullish; lowest low trong bearish)
/// max_af = giới hạn tối đa của AF (thường 0.2)
/// ```
///
/// **Trong bullish trend**: SAR nằm *dưới* giá (support động). Khi giá phá xuống
/// dưới SAR → đảo chiều bearish, SAR nhảy lên trên giá.
///
/// **Trong bearish trend**: SAR nằm *trên* giá (resistance động). Khi giá phá lên
/// trên SAR → đảo chiều bullish.
///
/// # Tham số thông dụng
/// - step = 0.02, max_af = 0.2 (Wilder gốc): cân bằng sensitivity vs stability
/// - step = 0.01, max_af = 0.1: ít nhạy hơn, ít reversal giả
/// - step = 0.03, max_af = 0.3: nhạy hơn, nhiều reversal giả hơn
///
/// # Hạn chế
/// - Hoạt động kém trong sideways market → nhiều false reversal
/// - Cần kết hợp với ADX (chỉ dùng SAR khi ADX > 25) hoặc filter
///
/// # Warmup
/// Cần 2 bar (bar thứ 2 trả về giá trị đầu tiên để có prev_high/prev_low).
#[derive(Debug, Clone)]
pub struct SarValue {
    /// Giá trị SAR tại bar hiện tại (stop level)
    pub sar: f64,
    /// `true` = SAR bên dưới giá (bullish); `false` = SAR bên trên giá (bearish)
    pub is_bullish: bool,
}

/// Parabolic SAR — trailing stop tự động tăng tốc theo xu hướng.
#[allow(missing_docs)]
#[derive(Clone)]
pub struct ParabolicSar {
    initial_af: f64,
    step: f64,
    max_af: f64,
    // State
    sar: Option<f64>,
    ep: Option<f64>,
    af: f64,
    is_bullish: bool,
    prev_high: Option<f64>,
    prev_low: Option<f64>,
}

impl ParabolicSar {
    pub fn new(step: f64, max_af: f64) -> Self {
        Self {
            initial_af: step,
            step,
            max_af,
            sar: None,
            ep: None,
            af: step,
            is_bullish: true,
            prev_high: None,
            prev_low: None,
        }
    }

    pub fn description() -> &'static str {
        "Parabolic SAR — trailing stop that accelerates as the trend matures. Dot above price = bearish; dot below = bullish. Flip signals trend reversal entries."
    }

    pub fn update(&mut self, high: f64, low: f64, _close: f64) -> Option<SarValue> {
        let (Some(prev_high), Some(prev_low)) = (self.prev_high, self.prev_low) else {
            self.prev_high = Some(high);
            self.prev_low = Some(low);
            return None;
        };

        if self.sar.is_none() {
            // Initialize: start bullish with SAR = previous low, EP = current high
            self.sar = Some(prev_low.min(low));
            self.ep = Some(high);
            self.is_bullish = true;
            self.af = self.initial_af;
            self.prev_high = Some(high);
            self.prev_low = Some(low);
            return Some(SarValue { sar: self.sar.unwrap(), is_bullish: self.is_bullish });
        }

        let prev_sar = self.sar.unwrap();
        let prev_ep = self.ep.unwrap();

        // Calculate new SAR
        let mut new_sar = prev_sar + self.af * (prev_ep - prev_sar);

        if self.is_bullish {
            // Clamp SAR below the prior bar's low (Wilder: two prior lows; we use one for
            // simplicity). Must clamp BEFORE checking reversal so SAR stays valid.
            new_sar = new_sar.min(prev_low);
            // Reversal: current low breaks below SAR
            if low < new_sar {
                self.is_bullish = false;
                new_sar = prev_ep; // SAR jumps to previous EP (highest high)
                self.ep = Some(low);
                self.af = self.initial_af;
            } else {
                // Update EP and AF if new high
                if high > prev_ep {
                    self.ep = Some(high);
                    self.af = (self.af + self.step).min(self.max_af);
                } else {
                    self.ep = Some(prev_ep);
                }
            }
        } else {
            // Clamp SAR above the prior bar's high (Wilder: two prior highs; we use one).
            new_sar = new_sar.max(prev_high);
            // Reversal: current high breaks above SAR
            if high > new_sar {
                self.is_bullish = true;
                new_sar = prev_ep; // SAR jumps to previous EP (lowest low)
                self.ep = Some(high);
                self.af = self.initial_af;
            } else {
                // Update EP and AF if new low
                if low < prev_ep {
                    self.ep = Some(low);
                    self.af = (self.af + self.step).min(self.max_af);
                } else {
                    self.ep = Some(prev_ep);
                }
            }
        }

        self.sar = Some(new_sar);
        self.prev_high = Some(high);
        self.prev_low = Some(low);

        Some(SarValue { sar: new_sar, is_bullish: self.is_bullish })
    }

    pub fn reset(&mut self) {
        self.sar = None;
        self.ep = None;
        self.af = self.initial_af;
        self.is_bullish = true;
        self.prev_high = None;
        self.prev_low = None;
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_sar_not_ready_first_bar() {
        let mut sar = ParabolicSar::new(0.02, 0.2);
        assert!(sar.update(105.0, 95.0, 100.0).is_none());
    }

    #[test]
    fn test_sar_ready_second_bar() {
        let mut sar = ParabolicSar::new(0.02, 0.2);
        sar.update(105.0, 95.0, 100.0);
        let v = sar.update(106.0, 96.0, 101.0);
        assert!(v.is_some(), "SAR should produce value on 2nd bar");
    }

    #[test]
    fn test_sar_bullish_uptrend() {
        let mut sar = ParabolicSar::new(0.02, 0.2);
        let mut last = None;
        for i in 0..15 {
            let p = 100.0 + i as f64 * 3.0;
            last = sar.update(p + 2.0, p - 0.5, p);
        }
        let v = last.unwrap();
        assert!(v.is_bullish, "SAR should be bullish in strong uptrend");
        assert!(v.sar < 100.0 + 14.0 * 3.0, "SAR below current price");
    }

    #[test]
    fn test_sar_reset() {
        let mut sar = ParabolicSar::new(0.02, 0.2);
        sar.update(105.0, 95.0, 100.0);
        sar.update(106.0, 96.0, 101.0);
        sar.reset();
        assert!(sar.update(105.0, 95.0, 100.0).is_none());
    }
}

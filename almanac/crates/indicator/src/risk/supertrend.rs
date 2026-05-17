use crate::Atr;

#[derive(Debug, Clone)]
pub struct SuperTrendValue {
    /// The SuperTrend line value.
    pub value: f64,
    /// `true` = bullish (line below price), `false` = bearish (line above price).
    pub is_bullish: bool,
}

/// SuperTrend — đường hỗ trợ/kháng cự động dựa trên ATR.
///
/// Được Olivier Seban phổ biến. SuperTrend tạo ra một đường duy nhất thay đổi phía
/// (trên hoặc dưới giá) theo trend. Rất phổ biến trong trading crypto và futures vì
/// đơn giản — chỉ cần một đường để xác định hướng và đặt stop loss.
///
/// # Công thức
/// ```text
/// HL2 = (High + Low) / 2
/// Basic_Upper = HL2 + multiplier × ATR(period)
/// Basic_Lower = HL2 − multiplier × ATR(period)
///
/// Final_Upper = min(Basic_Upper, prev_Final_Upper)  nếu prev_close ≤ prev_Final_Upper
///             = Basic_Upper                          nếu prev_close > prev_Final_Upper (breakout)
///
/// Final_Lower = max(Basic_Lower, prev_Final_Lower)  nếu prev_close ≥ prev_Final_Lower
///             = Basic_Lower                          nếu prev_close < prev_Final_Lower (breakdown)
///
/// SuperTrend = Final_Lower  nếu bullish (đường hỗ trợ dưới giá)
///            = Final_Upper  nếu bearish (đường kháng cự trên giá)
///
/// Chuyển sang bearish khi close < Final_Lower
/// Chuyển sang bullish khi close > Final_Upper
/// ```
///
/// # Tham số thông dụng
/// - ATR(7), multiplier=3.0: nhạy, nhiều signal
/// - ATR(14), multiplier=3.0: cân bằng (phổ biến nhất)
/// - ATR(21), multiplier=4.0: ít signal, phù hợp daily/weekly
///
/// # Ứng dụng
/// - **Entry**: giá cắt SuperTrend từ trên → buy; từ dưới → sell
/// - **Stop loss**: đặt stop tại đường SuperTrend (trailing stop)
/// - **Filter**: chỉ trade khi trend direction khớp với signal chính
///
/// # Warmup
/// Cần `atr_period` bar để ATR warm up, sau đó trả về giá trị ngay.
#[derive(Clone)]
pub struct SuperTrend {
    atr: Atr,
    multiplier: f64,
    prev_close: Option<f64>,
    prev_upper: Option<f64>,
    prev_lower: Option<f64>,
    prev_supertrend: Option<f64>,
    prev_is_bullish: Option<bool>,
    atr_period: usize,
}

impl SuperTrend {
    pub fn new(atr_period: usize, multiplier: f64) -> Self {
        Self {
            atr: Atr::new(atr_period),
            multiplier,
            prev_close: None,
            prev_upper: None,
            prev_lower: None,
            prev_supertrend: None,
            prev_is_bullish: None,
            atr_period,
        }
    }

    pub fn description() -> &'static str {
        "SuperTrend — ATR-based trailing stop that flips between bullish and bearish states. When price crosses above the band = bullish signal; below = bearish."
    }

    pub fn update(&mut self, high: f64, low: f64, close: f64) -> Option<SuperTrendValue> {
        let atr_val = self.atr.update(high, low, close)?;
        let atr = atr_val.atr;

        let hl2 = (high + low) / 2.0;
        let basic_upper = hl2 + self.multiplier * atr;
        let basic_lower = hl2 - self.multiplier * atr;

        // Final upper band: tighten only; never widen when price is above it
        let final_upper = match (self.prev_upper, self.prev_close) {
            (Some(pu), Some(pc)) => {
                if basic_upper < pu || pc > pu {
                    basic_upper
                } else {
                    pu
                }
            }
            _ => basic_upper,
        };

        // Final lower band: lift only; never drop when price is below it
        let final_lower = match (self.prev_lower, self.prev_close) {
            (Some(pl), Some(pc)) => {
                if basic_lower > pl || pc < pl {
                    basic_lower
                } else {
                    pl
                }
            }
            _ => basic_lower,
        };

        // SuperTrend value and direction
        let (st, is_bullish) = match (self.prev_supertrend, self.prev_is_bullish) {
            (Some(_prev_st), Some(prev_bull)) => {
                if prev_bull {
                    // Was bullish: stay bullish unless close drops below lower band
                    if close < final_lower {
                        (final_upper, false)
                    } else {
                        (final_lower, true)
                    }
                } else {
                    // Was bearish: turn bullish if close breaks above upper band
                    if close > final_upper {
                        (final_lower, true)
                    } else {
                        (final_upper, false)
                    }
                }
            }
            _ => {
                // First value: determine initial direction from close vs bands
                if close > final_lower {
                    (final_lower, true)
                } else {
                    (final_upper, false)
                }
            }
        };

        self.prev_close = Some(close);
        self.prev_upper = Some(final_upper);
        self.prev_lower = Some(final_lower);
        self.prev_supertrend = Some(st);
        self.prev_is_bullish = Some(is_bullish);

        Some(SuperTrendValue { value: st, is_bullish })
    }

    pub fn reset(&mut self) {
        self.atr = Atr::new(self.atr_period);
        self.prev_close = None;
        self.prev_upper = None;
        self.prev_lower = None;
        self.prev_supertrend = None;
        self.prev_is_bullish = None;
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_supertrend_uptrend_bullish() {
        let mut st = SuperTrend::new(3, 2.0);
        let mut last = None;
        // Strong uptrend: rising prices with small ATR → should turn bullish
        for i in 0..20 {
            let p = 100.0 + i as f64 * 5.0;
            last = st.update(p + 1.0, p - 1.0, p);
        }
        let v = last.unwrap();
        assert!(v.is_bullish, "SuperTrend should be bullish in uptrend");
    }

    #[test]
    fn test_supertrend_downtrend_bearish() {
        let mut st = SuperTrend::new(3, 2.0);
        let mut last = None;
        // Strong downtrend
        for i in 0..20 {
            let p = 200.0 - i as f64 * 5.0;
            last = st.update(p + 1.0, p - 1.0, p);
        }
        let v = last.unwrap();
        assert!(!v.is_bullish, "SuperTrend should be bearish in downtrend");
    }

    #[test]
    fn test_supertrend_reset() {
        let mut st = SuperTrend::new(3, 2.0);
        for i in 0..15 {
            let p = 100.0 + i as f64;
            st.update(p + 1.0, p - 1.0, p);
        }
        st.reset();
        assert!(st.update(100.0, 99.0, 99.5).is_none());
    }
}

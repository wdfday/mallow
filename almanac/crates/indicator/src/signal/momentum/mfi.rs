use std::collections::VecDeque;

/// Money Flow Index (MFI) — RSI có tích hợp volume (volume-weighted RSI).
///
/// Được Gene Quong và Avrum Soudack phát triển. MFI kết hợp cả giá lẫn volume,
/// nên nhạy hơn RSI thuần giá trong việc phát hiện divergence. Khi giá tăng
/// nhưng MFI giảm (negative divergence) → volume không xác nhận uptrend.
///
/// # Công thức
/// ```text
/// TP  = (High + Low + Close) / 3          ← Typical Price
/// MF  = TP × Volume                        ← Raw Money Flow (tiền vào/ra)
///
/// Bar rising  (TP > prev_TP): cộng MF vào Positive Money Flow
/// Bar falling (TP < prev_TP): cộng MF vào Negative Money Flow
/// Bar unchanged:              trung tính (không cộng vào đâu)
///
/// PMF = Σ Positive Money Flow (n bar)
/// NMF = Σ Negative Money Flow (n bar)
/// MFR = PMF / NMF
///
/// MFI = 100 − 100 / (1 + MFR)
/// ```
///
/// # Ngưỡng và cách đọc
/// - **MFI > 80**: overbought — volume lớn đẩy giá lên → có thể pullback
/// - **MFI < 20**: oversold  — volume lớn đẩy giá xuống → có thể rebound
/// - **NMF = 0** (chỉ có positive flow): MFI = 100
/// - **PMF = 0** (chỉ có negative flow): MFI = 0
///
/// # Divergence signals (mạnh nhất)
/// - **Bullish divergence**: giá tạo lower low nhưng MFI tạo higher low → reversal lên
/// - **Bearish divergence**: giá tạo higher high nhưng MFI tạo lower high → reversal xuống
///
/// # Ưu điểm vs RSI
/// - Nhạy với volume → phát hiện institutional money flow sớm hơn
/// - Ít bị lag hơn RSI trong trending market có volume rõ ràng
///
/// # Warmup
/// Cần `period + 1` bar (bar đầu tiên cần prev_TP để xác định hướng flow).
#[derive(Clone)]
pub struct Mfi {
    period: usize,
    prev_tp: Option<f64>,
    pos_flows: VecDeque<f64>,
    neg_flows: VecDeque<f64>,
}

impl Mfi {
    pub fn new(period: usize) -> Self {
        assert!(period > 0);
        Self {
            period,
            prev_tp: None,
            pos_flows: VecDeque::with_capacity(period),
            neg_flows: VecDeque::with_capacity(period),
        }
    }

    pub fn description() -> &'static str {
        "Money Flow Index — volume-weighted RSI using typical price. Combines price momentum and volume to identify overbought/oversold conditions. Outputs a single 0–100 value."
    }

    pub fn update(&mut self, high: f64, low: f64, close: f64, volume: f64) -> Option<f64> {
        let tp = (high + low + close) / 3.0;
        let mf = tp * volume;

        let (pos, neg) = match self.prev_tp {
            Some(prev) if tp > prev => (mf, 0.0),
            Some(prev) if tp < prev => (0.0, mf),
            _ => (0.0, 0.0), // unchanged TP → neutral
        };

        self.prev_tp = Some(tp);

        self.pos_flows.push_back(pos);
        self.neg_flows.push_back(neg);

        if self.pos_flows.len() > self.period {
            self.pos_flows.pop_front();
            self.neg_flows.pop_front();
        }

        if self.pos_flows.len() < self.period {
            return None;
        }

        let pos_sum: f64 = self.pos_flows.iter().sum();
        let neg_sum: f64 = self.neg_flows.iter().sum();

        if neg_sum == 0.0 {
            return Some(100.0);
        }

        let mfr = pos_sum / neg_sum;
        Some(100.0 - 100.0 / (1.0 + mfr))
    }

    pub fn reset(&mut self) {
        self.prev_tp = None;
        self.pos_flows.clear();
        self.neg_flows.clear();
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_mfi_bounds() {
        let mut mfi = Mfi::new(5);
        let mut last = None;
        for i in 0..10 {
            let p = 100.0 + i as f64;
            last = mfi.update(p + 1.0, p - 1.0, p, 1000.0);
        }
        let v = last.unwrap();
        assert!(v >= 0.0 && v <= 100.0, "MFI out of bounds: {v}");
    }

    #[test]
    fn test_mfi_all_positive_flow_is_100() {
        // All bars rising → only positive money flow → MFI approaches 100
        let mut mfi = Mfi::new(3);
        let mut last = None;
        for i in 1..=10 {
            let p = 100.0 + i as f64;
            last = mfi.update(p + 0.5, p - 0.5, p, 1000.0);
        }
        let v = last.unwrap();
        assert!(v > 90.0, "MFI should be near 100 with all rising bars: {v}");
    }

    #[test]
    fn test_mfi_all_negative_flow_is_low() {
        let mut mfi = Mfi::new(3);
        let mut last = None;
        for i in 1..=10 {
            let p = 200.0 - i as f64;
            last = mfi.update(p + 0.5, p - 0.5, p, 1000.0);
        }
        let v = last.unwrap();
        assert!(v < 10.0, "MFI should be near 0 with all falling bars: {v}");
    }
}

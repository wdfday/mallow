use std::collections::VecDeque;
use crate::Ema;

/// Stochastic Momentum Index (SMI) — phiên bản cải tiến của Stochastic.
///
/// Được William Blau phát triển năm 1993 (*Momentum, Direction, and Divergence*).
/// Thay vì đo vị trí close trong dải [low, high] như Stochastic cổ điển,
/// SMI đo khoảng cách từ close đến **midpoint** của dải, rồi double-smooth.
///
/// # Tại sao SMI tốt hơn Stochastic thông thường?
/// Stochastic cổ điển có vấn đề với giá gap: close ở top của range không
/// nhất thiết nghĩa là overbought nếu range rất hẹp. SMI dùng midpoint
/// (HH+LL)/2 làm tham chiếu → phản ánh đúng hơn vị trí tương đối.
///
/// # Công thức
/// ```text
/// HH(t) = highest high trong period bar
/// LL(t) = lowest low trong period bar
/// M(t)  = (HH + LL) / 2         ← midpoint của dải
///
/// D(t)  = close - M(t)           ← deviation từ midpoint
///
/// DS   = EMA(EMA(D,   smooth1), smooth2)   ← double-smooth numerator
/// DHL  = EMA(EMA(HH-LL, smooth1), smooth2) ← double-smooth range
///
/// SMI  = 100 × DS / (0.5 × DHL)
/// Signal = EMA(SMI, signal_period)
/// ```
///
/// # Tham số mặc định
/// - period=13 (window cho HH/LL)
/// - smooth1=25, smooth2=2 (double EMA smoothing)
/// - signal=9
///
/// # Range lý thuyết: [-100, +100]
/// Trong thực tế có thể vượt nhẹ do double-smoothing.
///
/// # Cách đọc
/// - SMI > +40 → overbought
/// - SMI < -40 → oversold
/// - SMI cắt lên Signal từ oversold → buy
/// - SMI cắt xuống Signal từ overbought → sell
#[derive(Debug, Clone)]
pub struct Smi {
    period: usize,
    /// Rolling buffer cho high và low
    high_buf: VecDeque<f64>,
    low_buf: VecDeque<f64>,
    /// Double EMA cho D (deviation)
    d_ema1: Ema,
    d_ema2: Ema,
    /// Double EMA cho HL range
    hl_ema1: Ema,
    hl_ema2: Ema,
    /// Signal EMA
    signal_ema: Ema,
}

/// Kết quả SMI.
#[derive(Debug, Clone, Copy)]
pub struct SmiValue {
    /// SMI line [-100, +100]
    pub smi: f64,
    /// Signal line
    pub signal: f64,
}

impl Smi {
    pub fn new(period: usize, smooth1: usize, smooth2: usize, signal: usize) -> Self {
        assert!(period > 0 && smooth1 > 0 && smooth2 > 0 && signal > 0);
        Self {
            period,
            high_buf: VecDeque::with_capacity(period),
            low_buf: VecDeque::with_capacity(period),
            d_ema1: Ema::new(smooth1),
            d_ema2: Ema::new(smooth2),
            hl_ema1: Ema::new(smooth1),
            hl_ema2: Ema::new(smooth2),
            signal_ema: Ema::new(signal),
        }
    }

    pub fn description() -> &'static str {
        "Stochastic Momentum Index — measures distance from the close to the midpoint of the high-low range (not just the low). Produces cleaner signals than classic Stochastic."
    }

    /// SMI(13, 25, 2, 9) — tham số Blau gốc.
    pub fn standard() -> Self {
        Self::new(13, 25, 2, 9)
    }

    pub fn update(&mut self, high: f64, low: f64, close: f64) -> Option<SmiValue> {
        // Cập nhật HH/LL window
        if self.high_buf.len() == self.period {
            self.high_buf.pop_front();
            self.low_buf.pop_front();
        }
        self.high_buf.push_back(high);
        self.low_buf.push_back(low);

        if self.high_buf.len() < self.period {
            return None;
        }

        let hh = self.high_buf.iter().cloned().fold(f64::NEG_INFINITY, f64::max);
        let ll = self.low_buf.iter().cloned().fold(f64::INFINITY, f64::min);
        let midpoint = (hh + ll) / 2.0;
        let range = hh - ll;

        let d = close - midpoint;

        // Double smooth D
        let ds = self.d_ema1.update(d).and_then(|e1| self.d_ema2.update(e1));
        // Double smooth HL range
        let dhl = self.hl_ema1.update(range).and_then(|e1| self.hl_ema2.update(e1));

        match (ds, dhl) {
            (Some(ds_val), Some(dhl_val)) if dhl_val.abs() > f64::EPSILON => {
                let smi = 100.0 * ds_val / (0.5 * dhl_val);
                self.signal_ema.update(smi).map(|signal| SmiValue { smi, signal })
            }
            _ => None,
        }
    }

    pub fn is_ready(&self) -> bool {
        self.signal_ema.is_ready()
    }

    pub fn reset(&mut self) {
        self.high_buf.clear();
        self.low_buf.clear();
        self.d_ema1.reset(); self.d_ema2.reset();
        self.hl_ema1.reset(); self.hl_ema2.reset();
        self.signal_ema.reset();
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn feed(smi: &mut Smi, n: usize) -> Option<SmiValue> {
        let mut r = None;
        for i in 0..n {
            let base = 100.0 + i as f64;
            r = smi.update(base + 2.0, base - 1.0, base + 1.5);
        }
        r
    }

    #[test]
    fn test_smi_uptrend_positive() {
        let mut smi = Smi::new(5, 5, 2, 3);
        let v = feed(&mut smi, 50).unwrap();
        assert!(v.smi > 0.0, "SMI={}", v.smi);
    }

    #[test]
    fn test_smi_downtrend_negative() {
        let mut smi = Smi::new(5, 5, 2, 3);
        let mut r = None;
        for i in 0..50 {
            let base = 200.0 - i as f64;
            r = smi.update(base + 1.0, base - 2.0, base - 1.5);
        }
        assert!(r.unwrap().smi < 0.0);
    }

    #[test]
    fn test_smi_reset() {
        let mut smi = Smi::new(5, 5, 2, 3);
        feed(&mut smi, 50);
        assert!(smi.is_ready());
        smi.reset();
        assert!(!smi.is_ready());
    }
}

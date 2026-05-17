use std::collections::VecDeque;

/// Fisher Transform — chuyển đổi giá sang phân phối Gaussian để dễ nhận diện đảo chiều.
///
/// Được John Ehlers phát triển (*Cybernetic Analysis for Stocks and Futures*, 2004).
/// Giá thị trường có phân phối phi Gaussian (fat tails), khiến các threshold
/// overbought/oversold của các indicator thông thường kém đáng tin cậy.
/// Fisher Transform "bình thường hoá" dữ liệu về Gaussian → các giá trị cực trị
/// (thường ±1.5 đến ±2.5) đáng tin cậy hơn làm tín hiệu đảo chiều.
///
/// # Công thức
/// ```text
/// HL_half = (high + low) / 2
/// HH(n)   = highest(HL_half, n)
/// LL(n)   = lowest(HL_half, n)
///
/// value = 2 × (HL_half − LL) / (HH − LL) − 1    → chuẩn hoá về [-1, 1]
/// value = clamp(value, -0.999, 0.999)              → tránh log(0) hoặc log(∞)
///
/// Fisher  = 0.5 × ln((1 + value) / (1 − value))   → Fisher Transform
/// Signal  = Fisher giá trị trước (1-bar lag)
/// ```
///
/// # Cách đọc
/// - Fisher vượt +1.5 → overbought, xem xét sell
/// - Fisher xuống dưới -1.5 → oversold, xem xét buy
/// - Fisher đổi chiều (cắt qua signal) → tín hiệu đảo chiều
/// - Divergence Fisher vs giá → tín hiệu mạnh
///
/// # Điểm mạnh so với RSI
/// Fisher Transform phản ứng sắc nét hơn tại điểm đảo chiều vì hàm
/// Fisher (arctanh) khuếch đại mạnh vùng cận ±1 → "tắc nghẽn momentum"
/// trở nên rõ ràng hơn.
#[derive(Debug, Clone)]
pub struct Fisher {
    period: usize,
    /// Rolling buffer cho HL midpoints
    buf: VecDeque<f64>,
    /// Giá trị Fisher trước để làm signal line
    prev_fisher: f64,
}

/// Kết quả Fisher Transform.
#[derive(Debug, Clone, Copy)]
pub struct FisherValue {
    /// Fisher Transform line
    pub fisher: f64,
    /// Signal line = Fisher một bar trước
    pub signal: f64,
}

impl Fisher {
    pub fn new(period: usize) -> Self {
        assert!(period >= 2, "Fisher period must be >= 2");
        Self {
            period,
            buf: VecDeque::with_capacity(period),
            prev_fisher: 0.0,
        }
    }

    pub fn description() -> &'static str {
        "Fisher Transform — normalises price into a Gaussian distribution to produce sharper turning points. Signal line crossovers mark reversals."
    }

    /// Fisher(9) — period phổ biến nhất.
    pub fn standard() -> Self {
        Self::new(9)
    }

    pub fn update(&mut self, high: f64, low: f64) -> Option<FisherValue> {
        let midpoint = (high + low) / 2.0;

        if self.buf.len() == self.period {
            self.buf.pop_front();
        }
        self.buf.push_back(midpoint);

        if self.buf.len() < self.period {
            return None;
        }

        let hh = self.buf.iter().cloned().fold(f64::NEG_INFINITY, f64::max);
        let ll = self.buf.iter().cloned().fold(f64::INFINITY, f64::min);

        let range = hh - ll;
        let value = if range < f64::EPSILON {
            // Không có biên độ — tránh division by zero
            0.0
        } else {
            // Chuẩn hoá HL midpoint về [-1, 1], clamp để tránh ln(0)
            let raw = 2.0 * (midpoint - ll) / range - 1.0;
            raw.clamp(-0.999, 0.999)
        };

        // Fisher Transform = arctanh(value) = 0.5 * ln((1+v)/(1-v))
        let fisher = 0.5 * ((1.0 + value) / (1.0 - value)).ln();

        let result = FisherValue {
            fisher,
            signal: self.prev_fisher,
        };
        self.prev_fisher = fisher;

        Some(result)
    }

    pub fn is_ready(&self) -> bool {
        self.buf.len() >= self.period
    }

    pub fn reset(&mut self) {
        self.buf.clear();
        self.prev_fisher = 0.0;
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_fisher_warmup() {
        let mut f = Fisher::new(5);
        for i in 0..4 { assert!(f.update(10.0 + i as f64, 9.0 + i as f64).is_none()); }
        assert!(f.update(14.0, 13.0).is_some());
    }

    #[test]
    fn test_fisher_constant_returns_finite() {
        // Khi giá không đổi, range = 0 → không crash, trả về fisher = 0
        let mut f = Fisher::new(5);
        let mut last = None;
        for _ in 0..10 { last = f.update(10.0, 9.0); }
        let v = last.unwrap();
        assert!(v.fisher.is_finite());
    }

    #[test]
    fn test_fisher_overbought_signal() {
        // Trong uptrend mạnh, Fisher phải dương và tăng
        let mut f = Fisher::new(9);
        let mut last = None;
        for i in 0..30 {
            let base = 100.0 + i as f64;
            last = f.update(base + 2.0, base - 1.0);
        }
        assert!(last.unwrap().fisher > 0.0);
    }

    #[test]
    fn test_fisher_reset() {
        let mut f = Fisher::new(5);
        for i in 0..10 { f.update(100.0 + i as f64, 99.0 + i as f64); }
        assert!(f.is_ready());
        f.reset();
        assert!(!f.is_ready());
    }
}

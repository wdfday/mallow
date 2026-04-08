use std::collections::VecDeque;

/// Ultimate Oscillator (UO) — kết hợp momentum của 3 timeframe để giảm false signal.
///
/// Được Larry Williams phát triển (1976). UO giải quyết vấn đề của single-period
/// oscillators: period ngắn → nhiều tín hiệu nhưng nhiều false signal; period dài
/// → ít false signal nhưng lag nhiều. UO kết hợp cả hai ưu điểm bằng cách dùng
/// 3 timeframe với weights ưu tiên period ngắn.
///
/// # Công thức
/// ```text
/// BP(t)  = close(t) − min(low(t), close(t-1))          ← Buying Pressure
/// TR(t)  = max(high(t), close(t-1)) − min(low(t), close(t-1)) ← True Range
///
/// Avg7  = Σ(BP, 7)  / Σ(TR, 7)    ← short-term (weight 4)
/// Avg14 = Σ(BP, 14) / Σ(TR, 14)   ← medium-term (weight 2)
/// Avg28 = Σ(BP, 28) / Σ(TR, 28)   ← long-term (weight 1)
///
/// UO = 100 × (4×Avg7 + 2×Avg14 + Avg28) / (4 + 2 + 1)
/// ```
///
/// # Tham số mặc định
/// - fast=7, medium=14, slow=28 (tỷ lệ 1:2:4)
///
/// # Cách đọc (Williams' original signals)
/// - UO > 70 → overbought; UO < 30 → oversold
/// - **Bullish divergence** (giá tạo đáy mới nhưng UO không): nếu UO sau đó
///   vượt lên đỉnh divergence → buy signal
/// - **Bearish divergence**: tương tự ngược lại
///
/// # Ưu điểm
/// Weight 4:2:1 khiến short-term chiếm 4/7 ≈ 57% → responsive nhưng
/// medium/long-term giảm noise hiệu quả.
#[derive(Debug, Clone)]
pub struct Uo {
    fast: usize,
    medium: usize,
    slow: usize,
    /// BP/TR buffer cho period slow (dùng chung cho cả 3 period)
    buf: VecDeque<(f64, f64)>,
    /// Running sums cho slow period
    sum_bp_slow: f64,
    sum_tr_slow: f64,
    prev_close: Option<f64>,
}

impl Uo {
    pub fn new(fast: usize, medium: usize, slow: usize) -> Self {
        assert!(fast < medium && medium < slow, "UO: fast < medium < slow required");
        Self {
            fast,
            medium,
            slow,
            buf: VecDeque::with_capacity(slow),
            sum_bp_slow: 0.0,
            sum_tr_slow: 0.0,
            prev_close: None,
        }
    }

    /// UO(7, 14, 28) — cấu hình gốc của Williams.
    pub fn standard() -> Self {
        Self::new(7, 14, 28)
    }

    pub fn update(&mut self, high: f64, low: f64, close: f64) -> Option<f64> {
        // Bar đầu tiên dùng chính close của nó làm prev_close (không có gap)
        let pc = self.prev_close.unwrap_or(close);

        // Buying Pressure: giá đóng cửa so với điểm thấp nhất trong bar (kể cả gap)
        let min_lc = low.min(pc);
        let bp = close - min_lc;
        // True Range: biên độ thực (kể cả gap qua đêm)
        let tr = high.max(pc) - min_lc;

        // Cập nhật running sum cho slow period
        if self.buf.len() == self.slow {
            let (old_bp, old_tr) = self.buf.pop_front().unwrap();
            self.sum_bp_slow -= old_bp;
            self.sum_tr_slow -= old_tr;
        }
        self.buf.push_back((bp, tr));
        self.sum_bp_slow += bp;
        self.sum_tr_slow += tr;

        self.prev_close = Some(close);

        if self.buf.len() < self.slow {
            return None;
        }

        // Tính sum cho fast và medium từ tail của buffer
        let len = self.buf.len();

        let (sum_bp_fast, sum_tr_fast) = self
            .buf
            .iter()
            .skip(len - self.fast)
            .fold((0.0, 0.0), |(sb, st), &(b, t)| (sb + b, st + t));

        let (sum_bp_med, sum_tr_med) = self
            .buf
            .iter()
            .skip(len - self.medium)
            .fold((0.0, 0.0), |(sb, st), &(b, t)| (sb + b, st + t));

        // Tránh division by zero
        if sum_tr_fast < f64::EPSILON
            || sum_tr_med < f64::EPSILON
            || self.sum_tr_slow < f64::EPSILON
        {
            return Some(50.0); // neutral fallback
        }

        let avg_fast = sum_bp_fast / sum_tr_fast;
        let avg_med = sum_bp_med / sum_tr_med;
        let avg_slow = self.sum_bp_slow / self.sum_tr_slow;

        // UO = 100 × (4×fast + 2×medium + 1×slow) / 7
        Some(100.0 * (4.0 * avg_fast + 2.0 * avg_med + avg_slow) / 7.0)
    }

    pub fn is_ready(&self) -> bool {
        self.buf.len() >= self.slow
    }

    pub fn reset(&mut self) {
        self.buf.clear();
        self.sum_bp_slow = 0.0;
        self.sum_tr_slow = 0.0;
        self.prev_close = None;
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_uo_range() {
        let mut uo = Uo::standard();
        let mut last = None;
        for i in 0..60 {
            let base = 100.0 + (i as f64 * 0.3).sin() * 5.0;
            last = uo.update(base + 1.0, base - 1.0, base);
        }
        let v = last.unwrap();
        assert!(v >= 0.0 && v <= 100.0, "UO={v} out of [0,100]");
    }

    #[test]
    fn test_uo_uptrend_high() {
        // Uptrend mạnh → UO > 50
        let mut uo = Uo::standard();
        let mut last = None;
        for i in 0..60 {
            let base = 100.0 + i as f64;
            last = uo.update(base + 2.0, base - 0.5, base + 1.5);
        }
        assert!(last.unwrap() > 50.0);
    }

    #[test]
    fn test_uo_warmup() {
        let mut uo = Uo::new(3, 6, 10);
        for i in 0..9 {
            let base = 100.0 + i as f64;
            assert!(uo.update(base + 1.0, base - 1.0, base).is_none());
        }
        let base = 109.0;
        assert!(uo.update(base + 1.0, base - 1.0, base).is_some());
    }

    #[test]
    fn test_uo_reset() {
        let mut uo = Uo::standard();
        for i in 0..60 {
            uo.update(100.0 + i as f64, 99.0 + i as f64, 100.0 + i as f64);
        }
        assert!(uo.is_ready());
        uo.reset();
        assert!(!uo.is_ready());
    }
}

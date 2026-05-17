use crate::Ema;

/// Percentage Price Oscillator (PPO) — phiên bản chuẩn hoá % của MACD.
///
/// # So sánh với MACD
/// | Chỉ báo | Công thức line               | Ý nghĩa                          |
/// |---------|------------------------------|----------------------------------|
/// | MACD    | EMA_fast − EMA_slow          | absolute price difference        |
/// | PPO     | (EMA_fast − EMA_slow) / EMA_slow × 100 | percentage difference |
///
/// PPO giải quyết điểm yếu của MACD: MACD của BTC (giá 60,000 USD) không thể
/// so sánh trực tiếp với MACD của một cổ phiếu 10 USD. PPO chuẩn hoá về %,
/// cho phép **so sánh momentum giữa các symbols khác nhau**.
///
/// # Tham số mặc định
/// - fast = 12, slow = 26, signal = 9 (giống MACD chuẩn)
///
/// # Công thức
/// ```text
/// PPO Line   = (EMA(fast) − EMA(slow)) / EMA(slow) × 100
/// Signal     = EMA(PPO Line, signal)
/// Histogram  = PPO Line − Signal
/// ```
///
/// # Cách đọc
/// - PPO > 0: EMA nhanh trên EMA chậm → bullish momentum
/// - PPO < 0: EMA nhanh dưới EMA chậm → bearish momentum
/// - Histogram đổi dấu: momentum shift (entry/exit signal)
/// - PPO crossover signal line: tương tự MACD crossover
#[derive(Debug, Clone)]
pub struct Ppo {
    fast_ema: Ema,
    slow_ema: Ema,
    signal_ema: Ema,
}

/// Kết quả PPO đầy đủ.
#[derive(Debug, Clone, Copy)]
pub struct PpoValue {
    /// PPO Line: % difference giữa EMA nhanh và EMA chậm
    pub ppo: f64,
    /// Signal Line: EMA của PPO — dùng để phát hiện crossover
    pub signal: f64,
    /// Histogram: ppo − signal — dương khi momentum tăng
    pub histogram: f64,
}

impl Ppo {
    pub fn new(fast: usize, slow: usize, signal: usize) -> Self {
        assert!(fast < slow, "PPO fast period must be < slow period");
        Self {
            fast_ema: Ema::new(fast),
            slow_ema: Ema::new(slow),
            signal_ema: Ema::new(signal),
        }
    }

    pub fn description() -> &'static str {
        "Percentage Price Oscillator — MACD expressed as a percentage of the slow EMA. Makes histogram values comparable across different price scales."
    }

    /// PPO(12, 26, 9) — cấu hình chuẩn, tương đương MACD mặc định.
    pub fn standard() -> Self {
        Self::new(12, 26, 9)
    }

    /// Feed một giá mới. Trả về `Some(PpoValue)` sau khi cả hai EMA và signal
    /// đã đủ warmup (phụ thuộc vào `slow` period lớn hơn).
    pub fn update(&mut self, price: f64) -> Option<PpoValue> {
        let fast = self.fast_ema.update(price);
        let slow = self.slow_ema.update(price);

        match (fast, slow) {
            (Some(f), Some(s)) if s.abs() > f64::EPSILON => {
                // PPO = % deviation của EMA nhanh so với EMA chậm
                let ppo = (f - s) / s * 100.0;

                self.signal_ema.update(ppo).map(|signal| PpoValue {
                    ppo,
                    signal,
                    histogram: ppo - signal,
                })
            }
            _ => None,
        }
    }

    pub fn is_ready(&self) -> bool {
        self.signal_ema.is_ready()
    }

    pub fn reset(&mut self) {
        self.fast_ema.reset();
        self.slow_ema.reset();
        self.signal_ema.reset();
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_ppo_constant_price_zero() {
        // Khi giá không đổi, cả hai EMA bằng nhau → PPO = 0
        let mut ppo = Ppo::new(3, 5, 2);
        let mut last = None;
        for _ in 0..20 {
            last = ppo.update(100.0);
        }
        let v = last.unwrap();
        assert!(v.ppo.abs() < 1e-8, "PPO should be ~0 for constant price, got {}", v.ppo);
        assert!(v.histogram.abs() < 1e-8);
    }

    #[test]
    fn test_ppo_uptrend_positive() {
        // Trong uptrend: EMA nhanh > EMA chậm → PPO > 0
        let mut ppo = Ppo::new(3, 6, 2);
        let mut last = None;
        for i in 0..30 {
            last = ppo.update(100.0 + i as f64);
        }
        let v = last.unwrap();
        assert!(v.ppo > 0.0, "PPO should be positive in uptrend, got {}", v.ppo);
    }

    #[test]
    fn test_ppo_downtrend_negative() {
        let mut ppo = Ppo::new(3, 6, 2);
        let mut last = None;
        for i in 0..30 {
            last = ppo.update(200.0 - i as f64);
        }
        let v = last.unwrap();
        assert!(v.ppo < 0.0, "PPO should be negative in downtrend, got {}", v.ppo);
    }

    #[test]
    fn test_ppo_percentage_normalized() {
        // PPO phải cho kết quả tương đồng giữa symbols giá khác nhau
        // Symbol A: giá ~100, Symbol B: giá ~10,000 — scale tương tự
        let mut ppo_a = Ppo::new(3, 6, 2);
        let mut ppo_b = Ppo::new(3, 6, 2);
        let mut va = None;
        let mut vb = None;
        for i in 0..30 {
            va = ppo_a.update(100.0 + i as f64);
            vb = ppo_b.update(10_000.0 + 100.0 * i as f64); // scale 100x
        }
        let a = va.unwrap().ppo;
        let b = vb.unwrap().ppo;
        assert!(
            (a - b).abs() < 1e-6,
            "PPO should be scale-invariant: a={a:.6}, b={b:.6}"
        );
    }

    #[test]
    fn test_ppo_reset() {
        let mut ppo = Ppo::standard();
        for i in 0..50 { ppo.update(i as f64); }
        assert!(ppo.is_ready());
        ppo.reset();
        assert!(!ppo.is_ready());
    }
}

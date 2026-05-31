//! Triple Exponential Moving Average (TEMA) — giảm lag triệt để hơn DEMA.
//!
//! Được Patrick Mulloy giới thiệu cùng DEMA năm 1994. TEMA sử dụng ba lớp EMA
//! để loại bỏ nhiều hơn phần lag so với DEMA. Công thức được suy ra từ phép
//! triệt tiêu đại số của các thành phần lag bậc 1 và bậc 2.
//!
//! # Công thức
//! ```text
//! EMA1 = EMA(close, n)
//! EMA2 = EMA(EMA1, n)
//! EMA3 = EMA(EMA2, n)
//!
//! TEMA = 3·EMA1 − 3·EMA2 + EMA3
//! ```
//!
//! Trực giác: EMA2 là lag bậc 1 của EMA1, EMA3 là lag bậc 2.
//! Triệt tiêu cả hai → TEMA rất gần giá hiện tại nhưng rất nhiều noise.
//!
//! # So sánh lag reduction
//! | MA   | Lag reduction | Noise |
//! |------|--------------|-------|
//! | EMA  | 0            | Thấp  |
//! | DEMA | ~50%         | Trung bình |
//! | TEMA | ~67%         | Cao   |
//! | HMA  | ~75%         | Trung bình |
//!
//! # Warmup
//! Cần `3 × (period - 1) + 1` bar vì ba EMA phải warm up tuần tự.
//! Ví dụ: TEMA(5) cần 13 bar; TEMA(14) cần 39 bar.

use crate::Ema;

#[derive(Clone)]
pub struct Tema {
    ema1: Ema,
    ema2: Ema,
    ema3: Ema,
}

impl Tema {
    pub fn new(period: usize) -> Self {
        Self {
            ema1: Ema::new(period),
            ema2: Ema::new(period),
            ema3: Ema::new(period),
        }
    }

    pub fn description() -> &'static str {
        "Triple EMA — three EMA passes for minimal lag. Aggressive smoothing that stays very close to current price in trending markets. Outputs a single value (price scale)."
    }

    pub fn update(&mut self, close: f64) -> Option<f64> {
        let e1 = self.ema1.update(close)?;
        let e2 = self.ema2.update(e1)?;
        let e3 = self.ema3.update(e2)?;
        Some(3.0 * e1 - 3.0 * e2 + e3)
    }

    pub fn reset(&mut self) {
        self.ema1.reset();
        self.ema2.reset();
        self.ema3.reset();
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_warmup() {
        let mut t = Tema::new(3);
        // Needs 3 EMA warmups = 3*(3-1) = 6 extra bars before first value
        for i in 0..8 {
            let v = t.update(100.0 + i as f64);
            if i < 6 {
                assert!(v.is_none(), "bar {i} should be None");
            }
        }
    }

    #[test]
    fn test_trend() {
        let mut t = Tema::new(3);
        let mut last = None;
        for i in 0..20 {
            last = t.update(100.0 + i as f64 * 2.0);
        }
        assert!(last.unwrap() > 100.0);
    }

    #[test]
    fn test_reset() {
        let mut t = Tema::new(3);
        for i in 0..15 { t.update(100.0 + i as f64); }
        t.reset();
        assert!(t.update(100.0).is_none());
    }
}

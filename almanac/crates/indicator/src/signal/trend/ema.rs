/// Exponential Moving Average (EMA) — trung bình động có trọng số hàm mũ.
///
/// EMA gán trọng số lớn hơn cho các bar gần đây, phản ứng nhanh hơn SMA với
/// biến động giá trong khi vẫn smooth tốt hơn raw price.
///
/// # Công thức
/// ```text
/// alpha = 2 / (period + 1)
/// EMA(t) = price(t) × alpha + EMA(t-1) × (1 − alpha)
/// ```
/// Seed ban đầu = SMA(period) của `period` bar đầu tiên.
///
/// # So sánh với SMA
/// | | EMA | SMA |
/// |---|---|---|
/// | Weights | exponential (gần = lớn) | bằng nhau |
/// | Lag | thấp hơn | cao hơn |
/// | Noise | nhiều hơn | ít hơn |
/// | Crossover signal | nhanh hơn | chậm hơn |
///
/// # Ứng dụng phổ biến
/// - EMA(12) và EMA(26): MACD
/// - EMA(9): signal line
/// - EMA(20/50/200): dynamic support/resistance
/// - EMA(13): Elder's force index, Bull Bear Power
///
/// # Warmup
/// Cần đúng `period` bar để seed SMA. Bar thứ `period` trả về giá trị đầu tiên.
#[derive(Debug, Clone)]
pub struct Ema {
    period: usize,
    /// α = 2/(period+1) — hệ số smoothing
    multiplier: f64,
    value: Option<f64>,
    /// Số bar đã nhận (dùng để seed EMA bằng SMA trong `period` bar đầu)
    count: usize,
    /// Tổng tích lũy để tính SMA seed
    seed_sum: f64,
}

impl Ema {
    pub fn new(period: usize) -> Self {
        assert!(period > 0, "EMA period must be > 0");
        Self {
            period,
            multiplier: 2.0 / (period as f64 + 1.0),
            value: None,
            count: 0,
            seed_sum: 0.0,
        }
    }

    pub fn description() -> &'static str {
        "Exponential Moving Average — applies exponentially decreasing weights to older prices. Reacts faster than SMA; widely used in crossover strategies and as a trend filter."
    }

    /// Feed một giá mới. Trả về `Some(ema)` sau khi đủ `period` bar để seed.
    pub fn update(&mut self, value: f64) -> Option<f64> {
        self.count += 1;

        if self.count < self.period {
            self.seed_sum += value;
            return None;
        }

        if self.count == self.period {
            self.seed_sum += value;
            let seed = self.seed_sum / self.period as f64;
            self.value = Some(seed);
            return self.value;
        }

        let prev = self.value.unwrap();
        self.value = Some(value * self.multiplier + prev * (1.0 - self.multiplier));
        self.value
    }

    pub fn value(&self) -> Option<f64> {
        self.value
    }

    pub fn is_ready(&self) -> bool {
        self.value.is_some()
    }

    pub fn reset(&mut self) {
        self.value = None;
        self.count = 0;
        self.seed_sum = 0.0;
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_ema_seed() {
        let mut ema = Ema::new(3);
        assert_eq!(ema.update(1.0), None);
        assert_eq!(ema.update(2.0), None);
        // seeds with SMA(1,2,3) = 2.0
        assert_eq!(ema.update(3.0), Some(2.0));
        // multiplier = 2/(3+1) = 0.5 → 4*0.5 + 2.0*0.5 = 3.0
        assert_eq!(ema.update(4.0), Some(3.0));
    }
}

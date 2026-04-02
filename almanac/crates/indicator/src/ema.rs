/// Exponential Moving Average — O(1) update, incremental state.
#[derive(Debug, Clone)]
pub struct Ema {
    period: usize,
    multiplier: f64,
    value: Option<f64>,
    /// Number of values seen (used to seed EMA with SMA for the first `period` bars)
    count: usize,
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

    /// Feed a new value. Returns `Some(ema)` once seeded (after `period` bars).
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

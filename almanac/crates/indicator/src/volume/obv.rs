/// On-Balance Volume — cumulative volume based on price direction.
///
/// If close > prev_close: OBV += volume
/// If close < prev_close: OBV -= volume
/// If close == prev_close: OBV unchanged
#[derive(Debug, Clone)]
pub struct Obv {
    value: f64,
    prev_close: Option<f64>,
    count: usize,
}

impl Obv {
    pub fn new() -> Self {
        Self {
            value: 0.0,
            prev_close: None,
            count: 0,
        }
    }

    pub fn update(&mut self, close: f64, volume: f64) -> f64 {
        if let Some(pc) = self.prev_close {
            if close > pc {
                self.value += volume;
            } else if close < pc {
                self.value -= volume;
            }
        }
        self.prev_close = Some(close);
        self.count += 1;
        self.value
    }

    pub fn value(&self) -> f64 {
        self.value
    }

    pub fn is_ready(&self) -> bool {
        self.count >= 2
    }

    pub fn reset(&mut self) {
        self.value = 0.0;
        self.prev_close = None;
        self.count = 0;
    }
}

impl Default for Obv {
    fn default() -> Self {
        Self::new()
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_obv_basic() {
        let mut obv = Obv::new();
        obv.update(10.0, 100.0); // first bar, no prev
        assert_eq!(obv.value(), 0.0);

        obv.update(11.0, 200.0); // up → +200
        assert_eq!(obv.value(), 200.0);

        obv.update(10.0, 150.0); // down → -150
        assert_eq!(obv.value(), 50.0);

        obv.update(10.0, 300.0); // flat → unchanged
        assert_eq!(obv.value(), 50.0);
    }
}

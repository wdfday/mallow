use std::collections::VecDeque;

#[derive(Debug, Clone, Copy)]
pub struct DonchianValue {
    pub upper: f64,
    pub lower: f64,
    pub middle: f64,
}

/// Donchian Channel — highest high / lowest low over N bars.
#[derive(Debug, Clone)]
pub struct Donchian {
    period: usize,
    highs: VecDeque<f64>,
    lows: VecDeque<f64>,
}

impl Donchian {
    pub fn new(period: usize) -> Self {
        assert!(period > 0, "Donchian period must be > 0");
        Self {
            period,
            highs: VecDeque::with_capacity(period),
            lows: VecDeque::with_capacity(period),
        }
    }

    pub fn update(&mut self, high: f64, low: f64) -> Option<DonchianValue> {
        self.highs.push_back(high);
        self.lows.push_back(low);
        if self.highs.len() > self.period {
            self.highs.pop_front();
            self.lows.pop_front();
        }
        if self.highs.len() < self.period {
            return None;
        }

        let upper = self.highs.iter().cloned().fold(f64::NEG_INFINITY, f64::max);
        let lower = self.lows.iter().cloned().fold(f64::INFINITY, f64::min);
        Some(DonchianValue { upper, lower, middle: (upper + lower) / 2.0 })
    }

    pub fn reset(&mut self) {
        self.highs.clear();
        self.lows.clear();
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_donchian_upper_lower() {
        let mut d = Donchian::new(3);
        d.update(10.0, 5.0);
        d.update(12.0, 4.0);
        let v = d.update(11.0, 6.0).unwrap();
        assert_eq!(v.upper, 12.0);
        assert_eq!(v.lower, 4.0);
        assert_eq!(v.middle, 8.0);
    }

    #[test]
    fn test_donchian_rolling() {
        let mut d = Donchian::new(2);
        d.update(10.0, 5.0);
        let v = d.update(8.0, 3.0).unwrap();
        assert_eq!(v.upper, 10.0);
        // Old bar rolls out
        let v2 = d.update(9.0, 6.0).unwrap();
        assert_eq!(v2.upper, 9.0);
        assert_eq!(v2.lower, 3.0);
    }
}

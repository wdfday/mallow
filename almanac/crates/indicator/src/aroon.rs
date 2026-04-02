use std::collections::VecDeque;

#[derive(Debug, Clone)]
pub struct AroonValue {
    pub up: f64,
    pub down: f64,
    /// Aroon Oscillator = Aroon Up - Aroon Down (-100..+100)
    pub oscillator: f64,
}

/// Aroon indicator — measures time since highest high / lowest low over N bars.
///
/// Aroon Up   = ((period - bars_since_high) / period) * 100
/// Aroon Down = ((period - bars_since_low)  / period) * 100
#[derive(Debug, Clone)]
pub struct Aroon {
    period: usize,
    highs: VecDeque<f64>,
    lows: VecDeque<f64>,
}

impl Aroon {
    pub fn new(period: usize) -> Self {
        assert!(period > 0, "Aroon period must be > 0");
        Self {
            period,
            highs: VecDeque::with_capacity(period + 1),
            lows: VecDeque::with_capacity(period + 1),
        }
    }

    pub fn update(&mut self, high: f64, low: f64) -> Option<AroonValue> {
        self.highs.push_back(high);
        self.lows.push_back(low);
        if self.highs.len() > self.period + 1 {
            self.highs.pop_front();
            self.lows.pop_front();
        }
        if self.highs.len() < self.period + 1 {
            return None;
        }

        // bars_since_high = index of max from the end (0 = current bar)
        let bars_since_high = self.highs
            .iter()
            .rev()
            .enumerate()
            .max_by(|a, b| a.1.partial_cmp(b.1).unwrap())
            .map(|(i, _)| i)
            .unwrap_or(0);

        let bars_since_low = self.lows
            .iter()
            .rev()
            .enumerate()
            .min_by(|a, b| a.1.partial_cmp(b.1).unwrap())
            .map(|(i, _)| i)
            .unwrap_or(0);

        let up = ((self.period - bars_since_high) as f64 / self.period as f64) * 100.0;
        let down = ((self.period - bars_since_low) as f64 / self.period as f64) * 100.0;

        Some(AroonValue { up, down, oscillator: up - down })
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
    fn test_aroon_new_high_gives_up_100() {
        let mut a = Aroon::new(5);
        for i in 0..5 { a.update(i as f64 + 1.0, i as f64); }
        let v = a.update(10.0, 5.0).unwrap();
        assert_eq!(v.up, 100.0);
    }

    #[test]
    fn test_aroon_oscillator_range() {
        let mut a = Aroon::new(5);
        let mut last = None;
        for i in 0..10 { last = a.update(i as f64, 10.0 - i as f64); }
        let v = last.unwrap();
        assert!(v.oscillator >= -100.0 && v.oscillator <= 100.0);
    }
}

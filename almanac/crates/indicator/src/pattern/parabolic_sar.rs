#[derive(Debug, Clone)]
pub struct SarValue {
    pub sar: f64,
    pub is_bullish: bool,
}

/// Parabolic SAR (Stop and Reverse).
///
/// Acceleration factor starts at `initial_af`, increases by `step` each time
/// the extreme point (EP) is updated, and is capped at `max_af`.
pub struct ParabolicSar {
    initial_af: f64,
    step: f64,
    max_af: f64,
    // State
    sar: Option<f64>,
    ep: Option<f64>,
    af: f64,
    is_bullish: bool,
    prev_high: Option<f64>,
    prev_low: Option<f64>,
}

impl ParabolicSar {
    pub fn new(step: f64, max_af: f64) -> Self {
        Self {
            initial_af: step,
            step,
            max_af,
            sar: None,
            ep: None,
            af: step,
            is_bullish: true,
            prev_high: None,
            prev_low: None,
        }
    }

    pub fn update(&mut self, high: f64, low: f64, _close: f64) -> Option<SarValue> {
        let (Some(prev_high), Some(prev_low)) = (self.prev_high, self.prev_low) else {
            self.prev_high = Some(high);
            self.prev_low = Some(low);
            return None;
        };

        if self.sar.is_none() {
            // Initialize: start bullish with SAR = previous low, EP = current high
            self.sar = Some(prev_low.min(low));
            self.ep = Some(high);
            self.is_bullish = true;
            self.af = self.initial_af;
            self.prev_high = Some(high);
            self.prev_low = Some(low);
            return Some(SarValue { sar: self.sar.unwrap(), is_bullish: self.is_bullish });
        }

        let prev_sar = self.sar.unwrap();
        let prev_ep = self.ep.unwrap();

        // Calculate new SAR
        let mut new_sar = prev_sar + self.af * (prev_ep - prev_sar);

        if self.is_bullish {
            // Bullish: SAR must be below the two previous lows
            new_sar = new_sar.min(prev_low).min(low);
            // Check for reversal
            if low < new_sar {
                self.is_bullish = false;
                new_sar = prev_ep; // SAR jumps to previous EP (highest high)
                self.ep = Some(low);
                self.af = self.initial_af;
            } else {
                // Update EP and AF if new high
                if high > prev_ep {
                    self.ep = Some(high);
                    self.af = (self.af + self.step).min(self.max_af);
                } else {
                    self.ep = Some(prev_ep);
                }
            }
        } else {
            // Bearish: SAR must be above the two previous highs
            new_sar = new_sar.max(prev_high).max(high);
            // Check for reversal
            if high > new_sar {
                self.is_bullish = true;
                new_sar = prev_ep; // SAR jumps to previous EP (lowest low)
                self.ep = Some(high);
                self.af = self.initial_af;
            } else {
                // Update EP and AF if new low
                if low < prev_ep {
                    self.ep = Some(low);
                    self.af = (self.af + self.step).min(self.max_af);
                } else {
                    self.ep = Some(prev_ep);
                }
            }
        }

        self.sar = Some(new_sar);
        self.prev_high = Some(high);
        self.prev_low = Some(low);

        Some(SarValue { sar: new_sar, is_bullish: self.is_bullish })
    }

    pub fn reset(&mut self) {
        self.sar = None;
        self.ep = None;
        self.af = self.initial_af;
        self.is_bullish = true;
        self.prev_high = None;
        self.prev_low = None;
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_sar_not_ready_first_bar() {
        let mut sar = ParabolicSar::new(0.02, 0.2);
        assert!(sar.update(105.0, 95.0, 100.0).is_none());
    }

    #[test]
    fn test_sar_ready_second_bar() {
        let mut sar = ParabolicSar::new(0.02, 0.2);
        sar.update(105.0, 95.0, 100.0);
        let v = sar.update(106.0, 96.0, 101.0);
        assert!(v.is_some(), "SAR should produce value on 2nd bar");
    }

    #[test]
    fn test_sar_bullish_uptrend() {
        let mut sar = ParabolicSar::new(0.02, 0.2);
        let mut last = None;
        for i in 0..15 {
            let p = 100.0 + i as f64 * 3.0;
            last = sar.update(p + 2.0, p - 0.5, p);
        }
        let v = last.unwrap();
        assert!(v.is_bullish, "SAR should be bullish in strong uptrend");
        assert!(v.sar < 100.0 + 14.0 * 3.0, "SAR below current price");
    }

    #[test]
    fn test_sar_reset() {
        let mut sar = ParabolicSar::new(0.02, 0.2);
        sar.update(105.0, 95.0, 100.0);
        sar.update(106.0, 96.0, 101.0);
        sar.reset();
        assert!(sar.update(105.0, 95.0, 100.0).is_none());
    }
}

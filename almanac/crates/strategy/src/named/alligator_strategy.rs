use alm_core::{bar::Bar, signal::Signal, strategy::Strategy};
use alm_indicator::Alligator;

/// Williams Alligator.
///
/// Long when Alligator is bullish: Lips > Teeth > Jaw (alligator eating upward).
/// Close when alignment breaks (any line inverts).
pub struct AlligatorStrategy {
    alligator: Alligator,
    prev_bullish: Option<bool>,
    in_position: bool,
}

impl AlligatorStrategy {
    pub fn new(jaw: usize, teeth: usize, lips: usize) -> Self {
        Self {
            alligator: Alligator::new(jaw, teeth, lips),
            prev_bullish: None,
            in_position: false,
        }
    }
}

impl Strategy for AlligatorStrategy {
    fn on_bar(&mut self, bar: &Bar) -> Vec<Signal> {
        let Some(v) = self.alligator.update(bar.high, bar.low) else {
            return vec![];
        };

        let prev = self.prev_bullish.replace(v.bullish);
        let Some(was_bullish) = prev else {
            return vec![];
        };

        if v.bullish && !was_bullish && !self.in_position {
            self.in_position = true;
            return vec![Signal::long(bar.timestamp, &bar.symbol, 1.0)];
        }
        if !v.bullish && was_bullish && self.in_position {
            self.in_position = false;
            return vec![Signal::close(bar.timestamp, &bar.symbol)];
        }
        vec![]
    }

    fn name(&self) -> &str {
        "alligator"
    }

    fn reset(&mut self) {
        self.alligator = Alligator::default();
        self.prev_bullish = None;
        self.in_position = false;
    }
}

#[cfg(test)]
mod tests {
}

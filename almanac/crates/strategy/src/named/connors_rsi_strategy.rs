use alm_core::{bar::Bar, signal::Signal, strategy::Strategy};
use alm_indicator::ConnorsRsi;

/// ConnorsRSI Strategy.
/// Long when CRSI drops below `oversold`; close when CRSI rises above `overbought`.
pub struct ConnorsRsiStrategy {
    rsi_period: usize,
    streak_period: usize,
    rank_period: usize,
    oversold: f64,
    overbought: f64,
    crsi: ConnorsRsi,
    in_position: bool,
}

impl ConnorsRsiStrategy {
    pub fn new(
        rsi_period: usize,
        streak_period: usize,
        rank_period: usize,
        oversold: f64,
        overbought: f64,
    ) -> Self {
        Self {
            rsi_period,
            streak_period,
            rank_period,
            oversold,
            overbought,
            crsi: ConnorsRsi::new(rsi_period, streak_period, rank_period),
            in_position: false,
        }
    }
}

impl Strategy for ConnorsRsiStrategy {
    fn on_bar(&mut self, bar: &Bar) -> Vec<Signal> {
        let Some(crsi_val) = self.crsi.update(bar.close) else {
            return vec![];
        };

        if crsi_val < self.oversold && !self.in_position {
            self.in_position = true;
            let strength = ((self.oversold - crsi_val) / self.oversold).clamp(0.0, 1.0);
            return vec![Signal::long(bar.timestamp, &bar.symbol, strength)];
        }

        if crsi_val > self.overbought && self.in_position {
            self.in_position = false;
            return vec![Signal::close(bar.timestamp, &bar.symbol)];
        }

        vec![]
    }

    fn name(&self) -> &str {
        "connors_rsi"
    }

    fn reset(&mut self) {
        self.crsi = ConnorsRsi::new(self.rsi_period, self.streak_period, self.rank_period);
        self.in_position = false;
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use alm_core::bar::Bar;
    use crate::test_utils::*;
    use crate::factory::build_strategy;
    use serde_json::json;

    fn bar(ts: i64, close: f64) -> Bar {
        Bar::new(ts, "TEST", close, close + 1.0, close - 1.0, close, 1000.0)
    }

    #[test]
    fn test_no_signal_before_ready() {
        let mut strat = ConnorsRsiStrategy::new(3, 2, 10, 10.0, 70.0);
        for i in 0..9 {
            let s = strat.on_bar(&bar(i, 100.0 + i as f64));
            assert!(s.is_empty(), "no signal before CRSI ready: bar {i}");
        }
    }

    #[test]
    fn test_long_on_oversold() {
        use alm_core::signal::Direction;
        let mut strat = ConnorsRsiStrategy::new(3, 2, 10, 10.0, 70.0);
        let mut signals = Vec::new();
        // Sharp decline → CRSI should drop below 10
        for i in 0..30 {
            let price = 200.0 - i as f64 * 2.5;
            signals.extend(strat.on_bar(&bar(i, price.max(1.0))));
        }
        assert!(
            signals.iter().any(|s| s.direction == Direction::Long),
            "should emit Long when CRSI oversold"
        );
    }

    #[test]
    fn test_close_on_overbought() {
        use alm_core::signal::Direction;
        // Use a wider oversold/overbought band so we can exercise both signals
        let mut strat = ConnorsRsiStrategy::new(3, 2, 10, 20.0, 60.0);
        let mut signals = Vec::new();
        // First: strong downtrend to push CRSI below oversold
        for i in 0..25 {
            let price = 200.0 - i as f64 * 3.0;
            signals.extend(strat.on_bar(&bar(i, price.max(1.0))));
        }
        // Then: strong uptrend to push CRSI above overbought
        for i in 25..60 {
            signals.extend(strat.on_bar(&bar(i, 80.0 + (i - 25) as f64 * 3.0)));
        }
        assert!(
            signals.iter().any(|s| s.direction == Direction::Close),
            "should emit Close when CRSI overbought"
        );
    }

    #[test]
    fn test_reset_clears_state() {
        let mut strat = ConnorsRsiStrategy::new(3, 2, 10, 10.0, 70.0);
        for i in 0..30 {
            strat.on_bar(&bar(i, 100.0 + (i % 5) as f64));
        }
        strat.reset();
        assert!(!strat.in_position);
    }

    /* // deprecated — DynamicStrategy removed
    #[test]
    fn connors_rsi_parity() {
        let bars = connors_rsi_bars();

        let mut hc = ConnorsRsiStrategy::new(3, 2, 100, 20.0, 80.0);
        let hc_sigs = run(&mut hc, &bars);

        let mut dyn_s = build_strategy("dynamic", &json!({
            "indicators": { "crsi": { "type": "connors_rsi",
                "rsi_period": 3, "streak_period": 2, "rank_period": 100 } },
            "entry": { "logic": "and", "rules": [
                { "source": "crsi", "field": "value", "op": "lt", "value": 20.0 }
            ]},
            "exit": { "logic": "and", "rules": [
                { "source": "crsi", "field": "value", "op": "gt", "value": 80.0 }
            ]}
        })).unwrap();
        let dyn_sigs = run(dyn_s.as_mut(), &bars);

        let mut cel = build_strategy("cel", &json!({
            "entry": "connors_rsi(3) < 20.0",
            "exit":  "connors_rsi(3) > 80.0"
        })).unwrap();
        let cel_sigs = run(cel.as_mut(), &bars);

        assert!(!hc_sigs.is_empty(), "connors_rsi: no signals");
        assert_parity("connors_rsi hc vs dynamic", &hc_sigs, &dyn_sigs);
        assert_parity("connors_rsi hc vs cel",     &hc_sigs, &cel_sigs);
    }
    */
}

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
        }
    }
}

impl Strategy for ConnorsRsiStrategy {
    fn on_bar(&mut self, bar: &Bar) -> Vec<Signal> {
        let Some(crsi_val) = self.crsi.update(bar.close) else {
            return vec![];
        };

        if crsi_val < self.oversold {
            let strength = ((self.oversold - crsi_val) / self.oversold).clamp(0.0, 1.0);
            return vec![Signal::long(bar.timestamp, &bar.symbol, strength)];
        }

        if crsi_val > self.overbought {
            return vec![Signal::exit(bar.timestamp, &bar.symbol)];
        }

        vec![]
    }

    fn name(&self) -> &str {
        "connors_rsi"
    }

    fn reset(&mut self) {
        self.crsi = ConnorsRsi::new(self.rsi_period, self.streak_period, self.rank_period);
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
            signals.iter().any(|s| s.direction == Direction::Exit),
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
    }

    #[test]
    fn script_parity() {
        use alm_core::signal::Direction;

        let bars = connors_rsi_bars();

        let mut named = ConnorsRsiStrategy::new(3, 2, 100, 10.0, 70.0);
        let named_sigs: Vec<(i64, Direction)> = bars.iter()
            .flat_map(|b| named.on_bar(b))
            .map(|s| (s.timestamp, s.direction))
            .collect();

        // ind.connors_rsi(3) uses defaults: rsi_period=3, streak_period=2, rank_period=100
        let script = r#"
let crsi = ind.connors_rsi(3, buf=1);
if crsi[0] < 10.0 { entry = true; }
if crsi[0] > 70.0 { exit  = true; }
"#;
        let mut script_strat = build_strategy("script", &json!({ "script": script })).unwrap();
        let script_sigs: Vec<(i64, Direction)> = bars.iter()
            .flat_map(|b| script_strat.on_bar(b))
            .map(|s| (s.timestamp, s.direction))
            .collect();

        assert!(!named_sigs.is_empty(), "connors_rsi: must produce signals");
        assert_eq!(named_sigs, script_sigs, "script parity failed");
    }
}

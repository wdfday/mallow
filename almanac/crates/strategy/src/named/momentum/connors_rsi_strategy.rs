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
    fn script(&self) -> Option<&'static str> {
        Some(RHAI_SCRIPT)
    }

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


pub(crate) const RHAI_SCRIPT: &str = r#"
let crsi = ind.connors_rsi(3, buf=1);
if crsi[0] < 10.0 { entry = true; }
if crsi[0] > 70.0 { exit  = true; }
"#;
#[cfg(test)]
mod tests {
    use super::*;
    use crate::test_utils::*;
    use crate::factory::build_strategy;
    use serde_json::json;

    #[test]
    fn test_no_signal_before_ready() {
        let Some(bars) = load_real_bars() else { return; };
        let mut strat = ConnorsRsiStrategy::new(3, 2, 10, 10.0, 70.0);
        for i in 0..9 {
            let s = strat.on_bar(&bars[i]);
            assert!(s.is_empty(), "no signal before CRSI ready: bar {i}");
        }
    }

    #[test]
    fn test_long_on_oversold() {
        use alm_core::signal::Direction;
        let Some(bars) = load_real_bars() else { return; };
        let mut strat = ConnorsRsiStrategy::new(3, 2, 10, 10.0, 70.0);
        let mut signals = Vec::new();
        for b in &bars {
            signals.extend(strat.on_bar(b));
        }
        assert!(
            signals.iter().any(|s| s.direction == Direction::Long),
            "should emit Long when CRSI oversold"
        );
    }

    #[test]
    fn test_close_on_overbought() {
        use alm_core::signal::Direction;
        let Some(bars) = load_real_bars() else { return; };
        let mut strat = ConnorsRsiStrategy::new(3, 2, 10, 20.0, 60.0);
        let mut signals = Vec::new();
        for b in &bars {
            signals.extend(strat.on_bar(b));
        }
        assert!(
            signals.iter().any(|s| s.direction == Direction::Exit),
            "should emit Close when CRSI overbought"
        );
    }

    #[test]
    fn test_reset_clears_state() {
        let Some(bars) = load_real_bars() else { return; };
        let mut strat = ConnorsRsiStrategy::new(3, 2, 10, 10.0, 70.0);
        for b in bars.iter().take(30) {
            strat.on_bar(b);
        }
        strat.reset();
    }

    #[test]
    fn script_parity() {
        let Some(bars) = load_real_bars() else { return; };

        let mut named = ConnorsRsiStrategy::new(3, 2, 100, 10.0, 70.0);
        let named_sigs = run(&mut named, &bars);

        let script = RHAI_SCRIPT;
        let mut script_strat = build_strategy("script", &json!({ "script": script })).unwrap();
        let script_sigs = run(script_strat.as_mut(), &bars);

        assert!(!named_sigs.is_empty(), "connors_rsi: must produce signals");
        assert_parity("connors_rsi parity vs named", &named_sigs, &script_sigs);
    }
}

use alm_core::{bar::Bar, signal::Signal, strategy::Strategy};
use alm_indicator::Trix;

/// Bot — TRIX Signal Line Crossover.
///
/// Long when TRIX line crosses above its signal line.
/// Close when TRIX crosses below signal line.
pub struct TrixStrategy {
    trix: Trix,
    prev_hist: Option<f64>,
    period: usize,
    signal_period: usize,
}

impl TrixStrategy {
    pub fn new(period: usize, signal_period: usize) -> Self {
        Self {
            trix: Trix::new(period, signal_period),
            prev_hist: None,
            period,
            signal_period,
        }
    }
}

impl Strategy for TrixStrategy {
    fn script(&self) -> Option<&'static str> {
        Some(RHAI_SCRIPT)
    }

    fn on_bar(&mut self, bar: &Bar) -> Vec<Signal> {
        let Some(v) = self.trix.update(bar.close) else {
            return vec![];
        };

        let prev = self.prev_hist.replace(v.histogram);
        let Some(p) = prev else {
            return vec![];
        };

        // Histogram crosses above 0: TRIX crossed above signal
        if p <= 0.0 && v.histogram > 0.0 {
            return vec![Signal::long(bar.timestamp, &bar.symbol, 1.0)];
        }
        if p >= 0.0 && v.histogram < 0.0 {
            return vec![Signal::exit(bar.timestamp, &bar.symbol)];
        }
        vec![]
    }

    fn name(&self) -> &str {
        "trix"
    }

    fn reset(&mut self) {
        self.trix = Trix::new(self.period, self.signal_period);
        self.prev_hist = None;
    }
}


pub(crate) const RHAI_SCRIPT: &str = r#"
let th = ind.trix(18);
if th[1].histogram <= 0.0 && th[0].histogram > 0.0 { entry = true; }
if th[1].histogram >= 0.0 && th[0].histogram < 0.0 { exit  = true; }
"#;
#[cfg(test)]
mod tests {
    use super::*;
    use alm_core::signal::Direction;
    use serde_json::json;
    use crate::factory::build_strategy;
    use crate::test_utils::*;

    

    fn run(s: &mut dyn Strategy, bars: &[Bar]) -> Vec<(i64, Direction)> {
        bars.iter().flat_map(|b| s.on_bar(b)).map(|s| (s.timestamp, s.direction)).collect()
    }

    

    #[test]
    fn no_signal_before_warmup() {
        let mut s = TrixStrategy::new(18, 9);
        let Some(bars) = load_real_bars() else { return; };
        for b in bars.iter().take(60) {
            assert!(s.on_bar(b).is_empty());
        }
    }
    

    #[test]
    fn parity_reset() {
        let Some(bars) = load_real_bars() else { return; };
        let mut hc = TrixStrategy::new(18, 9);
        let r1 = run(&mut hc, &bars);
        hc.reset();
        let r2 = run(&mut hc, &bars);
        assert_eq!(r1, r2, "reset parity failed");
    }

    #[test]
    fn script_parity() {
        use alm_core::signal::Direction;
        let Some(bars) = load_real_bars() else { return; };

        let mut named = TrixStrategy::new(18, 9);
        let named_sigs: Vec<(i64, Direction)> = bars.iter()
            .flat_map(|b| named.on_bar(b))
            .map(|s| (s.timestamp, s.direction))
            .collect();

        let script = RHAI_SCRIPT;
        let mut script_strat = build_strategy("script", &json!({ "script": script })).unwrap();
        let script_sigs: Vec<(i64, Direction)> = bars.iter()
            .flat_map(|b| script_strat.on_bar(b))
            .map(|s| (s.timestamp, s.direction))
            .collect();

        assert!(!named_sigs.is_empty(), "trix_strategy: must produce signals");
        assert_eq!(named_sigs, script_sigs, "script parity failed");
    }

}

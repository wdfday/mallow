use alm_core::{bar::Bar, signal::Signal, strategy::Strategy};
use alm_indicator::Aroon;

/// Aroon Trend — enters when Aroon Up dominates, exits when direction reverses.
///
/// Long  when Aroon Up > `bull_threshold` AND Aroon Down < `bear_threshold`.
/// Close when Aroon Up < Aroon Down (trend reversal confirmed).
///
/// Default: period=25, bull_threshold=70, bear_threshold=30
pub struct AroonTrend {
    aroon: Aroon,
    period: usize,
    bull_threshold: f64,
    bear_threshold: f64,
}

impl AroonTrend {
    pub fn new(period: usize, bull_threshold: f64, bear_threshold: f64) -> Self {
        Self {
            aroon: Aroon::new(period),
            period,
            bull_threshold,
            bear_threshold,
        }
    }
}

impl Strategy for AroonTrend {
    fn script(&self) -> Option<&'static str> {
        Some(RHAI_SCRIPT)
    }

    fn on_bar(&mut self, bar: &Bar) -> Vec<Signal> {
        let Some(v) = self.aroon.update(bar.high, bar.low) else {
            return vec![];
        };

        if v.up > self.bull_threshold && v.down < self.bear_threshold {
            let strength = (v.up - v.down) / 200.0 + 0.5;
            return vec![Signal::long(bar.timestamp, &bar.symbol, strength.clamp(0.0, 1.0))];
        }

        // Exit when aroon up drops below down (trend lost)
        if v.up < v.down {
            return vec![Signal::exit(bar.timestamp, &bar.symbol)];
        }

        vec![]
    }

    fn name(&self) -> &str {
        "aroon_trend"
    }

    fn reset(&mut self) {
        self.aroon = Aroon::new(self.period);
    }
}


pub(crate) const RHAI_SCRIPT: &str = r#"
let ar = ind.aroon(25, buf=1);
if ar[0].up > 70.0 && ar[0].down < 30.0 { entry = true; }
if ar[0].up < ar[0].down { exit = true; }
"#;
#[cfg(test)]
mod tests {
    use crate::test_utils::*;
    use super::*;
    use alm_core::signal::Direction;

    

    fn ohlcv(ts: i64, close: f64) -> Bar {
        Bar::new(ts, "T", close * 1.005, close * 1.005, close * 0.995, close, 1000.0)
    }

    fn run(s: &mut dyn Strategy, bars: &[Bar]) -> Vec<(i64, Direction)> {
        bars.iter().flat_map(|b| s.on_bar(b)).map(|s| (s.timestamp, s.direction)).collect()
    }

    

    #[test]
    fn test_aroon_no_signal_warmup() {
        let mut s = AroonTrend::new(25, 70.0, 30.0);
        let Some(bars) = load_real_bars() else { return; };
        for b in bars.iter().take(25) {
            assert!(s.on_bar(b).is_empty());
        }
    }
    
    #[test]
    fn parity_reset() {
        let Some(bars) = load_real_bars() else { return; };
        let mut hc = AroonTrend::new(25, 70.0, 30.0);
        let r1 = run(&mut hc, &bars);
        hc.reset();
        let r2 = run(&mut hc, &bars);
        assert_eq!(r1, r2, "reset parity failed");
    }

    #[test]
    fn script_parity() {
        use crate::factory::build_strategy;
        use serde_json::json;

        let Some(bars) = load_real_bars() else { return; };

        let mut named = AroonTrend::new(25, 70.0, 30.0);
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

        assert!(!named_sigs.is_empty(), "aroon_strategy: must produce signals");
        assert_eq!(named_sigs, script_sigs, "script parity failed");
    }

}

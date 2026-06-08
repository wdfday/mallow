use alm_core::{bar::Bar, signal::Signal, strategy::Strategy};
use alm_indicator::Adx;

const RHAI: &str = r#"
let adx14 = ind.adx(14, buf=1);
let dmi14 = ind.dmi(14, buf=1);
if adx14[0] > 27.5 && dmi14[0].plus_di > dmi14[0].minus_di { entry = true; }
if adx14[0] < 20.5 { exit  = true; }
"#;

/// Bot #46 — Wolfstein Trending.
///
/// Enters long when ADX > `long_threshold` AND +DI > -DI (strong bullish trend).
/// Exits when ADX falls below `short_threshold` (trend weakening).
pub struct Wolfstein {
    adx: Adx,
    long_threshold: f64,
    short_threshold: f64,
    period: usize,
}

impl Wolfstein {
    pub fn new(period: usize, long_threshold: f64, short_threshold: f64) -> Self {
        Self {
            adx: Adx::new(period),
            long_threshold,
            short_threshold,
            period,
        }
    }
}

impl Strategy for Wolfstein {
    fn on_bar(&mut self, bar: &Bar) -> Vec<Signal> {
        let Some(v) = self.adx.update(bar.high, bar.low, bar.close) else {
            return vec![];
        };

        if v.adx > self.long_threshold && v.plus_di > v.minus_di {
            return vec![Signal::long(bar.timestamp, &bar.symbol, v.adx / 100.0)];
        }
        if v.adx < self.short_threshold {
            return vec![Signal::exit(bar.timestamp, &bar.symbol)];
        }
        vec![]
    }

    fn name(&self) -> &str {
        "wolfstein"
    }

    fn description(&self) -> &'static str {
        "Long when ADX > long_threshold and +DI > -DI (strong bullish trend). Exit when ADX falls below short_threshold."
    }

    fn script(&self) -> Option<&'static str> { Some(RHAI) }

    fn reset(&mut self) {
        self.adx = Adx::new(self.period);
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use alm_core::signal::Direction;
    use crate::test_utils::*;
    use crate::factory::build_strategy;
    use serde_json::json;

    fn run(s: &mut dyn Strategy, bars: &[Bar]) -> Vec<(i64, Direction)> {
        bars.iter().flat_map(|b| s.on_bar(b)).map(|s| (s.timestamp, s.direction)).collect()
    }

    #[test]
    fn script_parity() {
        // Wolfstein fires every bar when conditions hold (no crossover gate).
        let Some(bars) = load_real_bars() else { return; };

        let mut named = Wolfstein::new(14, 27.5, 20.5);
        let named_sigs = run(&mut named, &bars);

        let script = Wolfstein::new(14, 27.5, 20.5).script().unwrap();
        let mut script_strat = build_strategy("script", &json!({ "script": script })).unwrap();
        let script_sigs = run(script_strat.as_mut(), &bars);

        assert!(!named_sigs.is_empty(), "wolfstein: must produce signals");
        assert_eq!(named_sigs, script_sigs, "script parity failed");
    }
}

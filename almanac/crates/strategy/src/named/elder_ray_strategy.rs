use alm_core::{bar::Bar, signal::Signal, strategy::Strategy};
use alm_indicator::ElderRay;

/// Elder Ray — Dr. Alexander Elder's trend + power system.
///
/// Long  when EMA is rising (close > EMA) AND Bear Power < 0 but increasing (bears weakening).
/// Close when Bull Power turns negative (bulls exhausted).
///
/// Default: ema_period=13
pub struct ElderRayStrategy {
    er: ElderRay,
    period: usize,
    prev_bear: Option<f64>,
    prev_bull: Option<f64>,
    prev_ema: Option<f64>,
    in_position: bool,
}

impl ElderRayStrategy {
    pub fn new(period: usize) -> Self {
        Self {
            er: ElderRay::new(period),
            period,
            prev_bear: None,
            prev_bull: None,
            prev_ema: None,
            in_position: false,
        }
    }
}

impl Strategy for ElderRayStrategy {
    fn on_bar(&mut self, bar: &Bar) -> Vec<Signal> {
        let Some(v) = self.er.update(bar.high, bar.low, bar.close) else {
            return vec![];
        };

        let prev_bear = self.prev_bear.replace(v.bear_power);
        let prev_bull = self.prev_bull.replace(v.bull_power);
        let prev_ema = self.prev_ema.replace(v.ema);

        let (Some(pb), Some(pbl), Some(pe)) = (prev_bear, prev_bull, prev_ema) else {
            return vec![];
        };

        let ema_rising = v.ema > pe;

        // Long: EMA uptrend + bear power negative but rising (weakening bears)
        if ema_rising && v.bear_power < 0.0 && v.bear_power > pb && !self.in_position {
            self.in_position = true;
            return vec![Signal::long(bar.timestamp, &bar.symbol, 1.0)];
        }

        // Close: bull power turns negative (bulls exhausted)
        if v.bull_power < 0.0 && pbl >= 0.0 && self.in_position {
            self.in_position = false;
            return vec![Signal::close(bar.timestamp, &bar.symbol)];
        }

        vec![]
    }

    fn name(&self) -> &str {
        "elder_ray"
    }

    fn reset(&mut self) {
        self.er = ElderRay::new(self.period);
        self.prev_bear = None;
        self.prev_bull = None;
        self.prev_ema = None;
        self.in_position = false;
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::test_utils::*;
    use crate::factory::build_strategy;
    use serde_json::json;

    #[test]
    fn elder_ray_parity() {
        let bars = elder_ray_bars();

        let mut hc = ElderRayStrategy::new(13);
        let hc_sigs = run(&mut hc, &bars);

        let mut cel = build_strategy("cel", &json!({
            "entry": "ema(13) > prev_ema(13) && bear_power(13) < 0.0 && bear_power(13) > prev_bear_power(13)",
            "exit":  "prev_bull_power(13) >= 0.0 && bull_power(13) < 0.0"
        })).unwrap();
        let cel_sigs = run(cel.as_mut(), &bars);

        assert!(!hc_sigs.is_empty(), "elder_ray: no signals");
        assert_parity("elder_ray hc vs cel", &hc_sigs, &cel_sigs);
    }
}

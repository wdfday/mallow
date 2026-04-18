use alm_core::{bar::Bar, signal::Signal, strategy::Strategy};
use alm_indicator::{Ema, Macd, Stochastic};

/// Bot #23 — Equilibrium Explorer.
///
/// Long when price is above EMA(200), stochastic %K is oversold,
/// and MACD histogram is positive (momentum aligned).
/// Closes when stochastic turns overbought OR MACD histogram turns negative.
pub struct EquilibriumExplorer {
    ema: Ema,
    stoch: Stochastic,
    macd: Macd,
    stoch_oversold: f64,
    stoch_overbought: f64,
    in_position: bool,
    ema_period: usize,
    stoch_k: usize,
    stoch_d: usize,
    macd_fast: usize,
    macd_slow: usize,
    macd_signal: usize,
}

impl EquilibriumExplorer {
    pub fn new(
        ema_period: usize,
        stoch_k: usize,
        stoch_d: usize,
        stoch_oversold: f64,
        stoch_overbought: f64,
        macd_fast: usize,
        macd_slow: usize,
        macd_signal: usize,
    ) -> Self {
        Self {
            ema: Ema::new(ema_period),
            stoch: Stochastic::new(stoch_k, stoch_d),
            macd: Macd::new(macd_fast, macd_slow, macd_signal),
            stoch_oversold,
            stoch_overbought,
            in_position: false,
            ema_period,
            stoch_k,
            stoch_d,
            macd_fast,
            macd_slow,
            macd_signal,
        }
    }
}

impl Strategy for EquilibriumExplorer {
    fn on_bar(&mut self, bar: &Bar) -> Vec<Signal> {
        let ema_val = self.ema.update(bar.close);
        let stoch_val = self.stoch.update(bar.high, bar.low, bar.close);
        let macd_val = self.macd.update(bar.close);

        let (Some(ema), Some(st), Some(m)) = (ema_val, stoch_val, macd_val) else {
            return vec![];
        };

        let trending_up = bar.close > ema;
        let oversold = st.k < self.stoch_oversold;
        let overbought = st.k > self.stoch_overbought;
        let hist_positive = m.histogram > 0.0;
        let hist_negative = m.histogram < 0.0;

        if !self.in_position && trending_up && oversold && hist_positive {
            self.in_position = true;
            return vec![Signal::long(bar.timestamp, &bar.symbol, 1.0)];
        }
        if self.in_position && (overbought || hist_negative) {
            self.in_position = false;
            return vec![Signal::close(bar.timestamp, &bar.symbol)];
        }
        vec![]
    }

    fn name(&self) -> &str {
        "equilibrium_explorer"
    }

    fn reset(&mut self) {
        self.ema = Ema::new(self.ema_period);
        self.stoch = Stochastic::new(self.stoch_k, self.stoch_d);
        self.macd = Macd::new(self.macd_fast, self.macd_slow, self.macd_signal);
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
    fn equilibrium_explorer_parity() {
        let bars = dip_in_uptrend_bars();

        let mut hc = EquilibriumExplorer::new(200, 14, 3, 20.0, 80.0, 12, 26, 9);
        let hc_sigs = run(&mut hc, &bars);

        let mut dyn_s = build_strategy("dynamic", &json!({
            "indicators": {
                "ema":   { "type": "ema",        "period": 200 },
                "stoch": { "type": "stochastic", "k_period": 14, "d_period": 3 },
                "macd":  { "type": "macd",       "fast": 12, "slow": 26, "signal": 9 }
            },
            "entry": { "logic": "and", "rules": [
                { "source": "close",  "field": "value", "op": "gt", "compare": "ema" },
                { "source": "stoch",  "field": "k",     "op": "lt", "value": 20.0 },
                { "source": "macd",   "field": "histogram", "op": "gt", "value": 0.0 }
            ]},
            "exit": { "logic": "or", "rules": [
                { "source": "stoch", "field": "k",         "op": "gt", "value": 80.0 },
                { "source": "macd",  "field": "histogram", "op": "lt", "value": 0.0 }
            ]}
        })).unwrap();
        let dyn_sigs = run(dyn_s.as_mut(), &bars);

        let mut cel = build_strategy("cel", &json!({
            "entry": "close > ema(200) && stoch_k(14) < 20.0 && macd_hist(12) > 0.0",
            "exit":  "stoch_k(14) > 80.0 || macd_hist(12) < 0.0"
        })).unwrap();
        let cel_sigs = run(cel.as_mut(), &bars);

        assert!(!hc_sigs.is_empty(), "equilibrium_explorer: no signals");
        assert_parity("equilibrium_explorer hc vs dynamic", &hc_sigs, &dyn_sigs);
        assert_parity("equilibrium_explorer hc vs cel",     &hc_sigs, &cel_sigs);
    }
}

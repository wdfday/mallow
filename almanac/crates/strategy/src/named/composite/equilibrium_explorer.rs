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

        if trending_up && oversold && hist_positive {
            return vec![Signal::long(bar.timestamp, &bar.symbol, 1.0)];
        }
        if overbought || hist_negative {
            return vec![Signal::exit(bar.timestamp, &bar.symbol)];
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
    }

    fn script(&self) -> Option<&'static str> {
        Some(RHAI_SCRIPT)
    }
}

pub(crate) const RHAI_SCRIPT: &str = r#"
let ema200 = ind.ema(200, buf=1);
let st = ind.stochastic(14, buf=1);
let mh = ind.macd(12, buf=1);
if close[0] > ema200[0] && st[0].k < 20.0 && mh[0].histogram > 0.0 { entry = true; }
if st[0].k > 80.0 || mh[0].histogram < 0.0 { exit = true; }
"#;

#[cfg(test)]
mod tests {
    use crate::test_utils::*;
}

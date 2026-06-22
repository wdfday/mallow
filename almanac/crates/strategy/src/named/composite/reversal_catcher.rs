use alm_core::{bar::Bar, signal::Signal, strategy::Strategy};
use alm_indicator::{Rsi, Stochastic};

/// Bot #31 — Reversal Catcher.
///
/// Long when %K crosses above %D (momentum turning up) AND RSI < 50.
/// Closes when %K crosses back below %D OR RSI > 70.
pub struct ReversalCatcher {
    stoch: Stochastic,
    rsi: Rsi,
    prev_k: Option<f64>,
    prev_d: Option<f64>,
    k_period: usize,
    d_period: usize,
    rsi_period: usize,
}

impl ReversalCatcher {
    pub fn new(k_period: usize, d_period: usize, rsi_period: usize) -> Self {
        Self {
            stoch: Stochastic::new(k_period, d_period),
            rsi: Rsi::new(rsi_period),
            prev_k: None,
            prev_d: None,
            k_period,
            d_period,
            rsi_period,
        }
    }
}

impl Strategy for ReversalCatcher {
    fn on_bar(&mut self, bar: &Bar) -> Vec<Signal> {
        let stoch_val = self.stoch.update(bar.high, bar.low, bar.close);
        let rsi_val = self.rsi.update(bar.close);

        let (Some(st), Some(rsi)) = (stoch_val, rsi_val) else {
            return vec![];
        };

        let (Some(pk), Some(pd)) = (self.prev_k, self.prev_d) else {
            self.prev_k = Some(st.k);
            self.prev_d = Some(st.d);
            return vec![];
        };

        let k_crossed_above_d = pk <= pd && st.k > st.d;
        let k_crossed_below_d = pk >= pd && st.k < st.d;

        self.prev_k = Some(st.k);
        self.prev_d = Some(st.d);

        if k_crossed_above_d && rsi < 50.0 {
            return vec![Signal::long(bar.timestamp, &bar.symbol, 1.0)];
        }
        if k_crossed_below_d || rsi > 70.0 {
            return vec![Signal::exit(bar.timestamp, &bar.symbol)];
        }
        vec![]
    }

    fn name(&self) -> &str {
        "reversal_catcher"
    }

    fn reset(&mut self) {
        self.stoch = Stochastic::new(self.k_period, self.d_period);
        self.rsi = Rsi::new(self.rsi_period);
        self.prev_k = None;
        self.prev_d = None;
    }

    fn script(&self) -> Option<&'static str> {
        Some(RHAI_SCRIPT)
    }
}

pub(crate) const RHAI_SCRIPT: &str = r#"
let st = ind.stochastic(14);
let rsi14 = ind.rsi(14, buf=1);
if st[1].k <= st[1].d && st[0].k > st[0].d && rsi14[0] < 50.0 { entry = true; }
if (st[1].k >= st[1].d && st[0].k < st[0].d) || rsi14[0] > 70.0 { exit = true; }
"#;


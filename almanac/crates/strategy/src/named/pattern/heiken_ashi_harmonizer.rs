use alm_core::{bar::Bar, signal::Signal, strategy::Strategy};
use alm_indicator::{Ema, HeikenAshi};

pub(crate) const RHAI_SCRIPT: &str = r#"
if state["ema50_count"] == () {
    let ha_open_1 = (open[1] + close[1]) / 2.0;
    let ha_close_1 = (open[1] + high[1] + low[1] + close[1]) / 4.0;
    
    let ha_close_2 = (open[0] + high[0] + low[0] + close[0]) / 4.0;
    let ha_open_2 = (ha_open_1 + ha_close_1) / 2.0;
    
    state["ha_open"] = ha_open_2;
    state["ha_close"] = ha_close_2;
    
    state["ema50_count"] = 2;
    state["ema50_sum"] = close[1] + close[0];
    state["ema50_val"] = ();
} else {
    let prev_ha_open = state["ha_open"];
    let prev_ha_close = state["ha_close"];
    let ha_close = (open[0] + high[0] + low[0] + close[0]) / 4.0;
    let ha_open = (prev_ha_open + prev_ha_close) / 2.0;
    state["ha_open"] = ha_open;
    state["ha_close"] = ha_close;
    
    state["ema50_count"] = state["ema50_count"] + 1;
    let count = state["ema50_count"];
    let cur_ema50 = ();
    if count < 50 {
        state["ema50_sum"] = state["ema50_sum"] + close[0];
    } else if count == 50 {
        state["ema50_sum"] = state["ema50_sum"] + close[0];
        let val = state["ema50_sum"] / 50.0;
        state["ema50_val"] = val;
        cur_ema50 = val;
    } else {
        let prev = state["ema50_val"];
        let k = 2.0 / 51.0;
        let val = close[0] * k + prev * (1.0 - k);
        state["ema50_val"] = val;
        cur_ema50 = val;
    }
    
    if cur_ema50 != () {
        let is_bullish = state["ha_close"] >= state["ha_open"];
        if is_bullish && close[0] > cur_ema50 {
            entry = true;
        }
        if !is_bullish || close[0] < cur_ema50 {
            exit = true;
        }
    }
}
"#;

/// Bot — Heiken Ashi Harmonizer.
///
/// Long when HA is bullish AND close > EMA(period).
/// Close when HA turns bearish OR close < EMA.
pub struct HaHarmonizer {
    ha: HeikenAshi,
    ema: Ema,
    smooth: usize,
    ema_period: usize,
}

impl HaHarmonizer {
    pub fn new(smooth: usize, ema_period: usize) -> Self {
        Self {
            ha: HeikenAshi::new(smooth),
            ema: Ema::new(ema_period),
            smooth,
            ema_period,
        }
    }
}

impl Strategy for HaHarmonizer {
    fn on_bar(&mut self, bar: &Bar) -> Vec<Signal> {
        let ha = self.ha.update(bar.open, bar.high, bar.low, bar.close);
        let Some(ema) = self.ema.update(bar.close) else {
            return vec![];
        };
        let Some(ha) = ha else {
            return vec![];
        };

        let bullish_setup = ha.is_bullish && bar.close > ema;

        if bullish_setup {
            return vec![Signal::long(bar.timestamp, &bar.symbol, 1.0)];
        }
        if !ha.is_bullish || bar.close < ema {
            return vec![Signal::exit(bar.timestamp, &bar.symbol)];
        }
        vec![]
    }

    fn name(&self) -> &str {
        "heiken_ashi_harmonizer"
    }

    fn reset(&mut self) {
        self.ha = HeikenAshi::new(self.smooth);
        self.ema = Ema::new(self.ema_period);
    }

    fn script(&self) -> Option<&'static str> {
        Some(RHAI_SCRIPT)
    }
}

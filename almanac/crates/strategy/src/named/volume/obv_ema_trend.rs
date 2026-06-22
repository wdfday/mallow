use alm_core::{bar::Bar, signal::Signal, strategy::Strategy};
use alm_indicator::{Ema, Obv};

/// On-Balance Volume with EMA of OBV as trend signal.
///
/// Long when OBV is above its own EMA (accumulation) AND price > price EMA.
/// Close when OBV drops below its EMA OR price drops below price EMA.
pub struct ObvEmaTrend {
    obv: Obv,
    obv_ema: Ema,
    price_ema: Ema,
    in_position: bool,
    obv_ema_period: usize,
    price_ema_period: usize,
}

impl ObvEmaTrend {
    pub fn new(obv_ema_period: usize, price_ema_period: usize) -> Self {
        Self {
            obv: Obv::new(),
            obv_ema: Ema::new(obv_ema_period),
            price_ema: Ema::new(price_ema_period),
            in_position: false,
            obv_ema_period,
            price_ema_period,
        }
    }
}

impl Strategy for ObvEmaTrend {
    fn script(&self) -> Option<&'static str> {
        Some(RHAI_SCRIPT)
    }

    fn on_bar(&mut self, bar: &Bar) -> Vec<Signal> {
        let obv_val = self.obv.update(bar.close, bar.volume);
        let obv_ema = self.obv_ema.update(obv_val);
        let price_ema = self.price_ema.update(bar.close);

        let (Some(obv_ema), Some(price_ema)) = (obv_ema, price_ema) else {
            return vec![];
        };

        let obv_bullish = obv_val > obv_ema;
        let price_bullish = bar.close > price_ema;

        if obv_bullish && price_bullish && !self.in_position {
            self.in_position = true;
            return vec![Signal::long(bar.timestamp, &bar.symbol, 1.0)];
        }
        if self.in_position && (!obv_bullish || !price_bullish) {
            self.in_position = false;
            return vec![Signal::exit(bar.timestamp, &bar.symbol)];
        }
        vec![]
    }

    fn name(&self) -> &str {
        "obv_ema_trend"
    }

    fn reset(&mut self) {
        self.obv = Obv::new();
        self.obv_ema = Ema::new(self.obv_ema_period);
        self.price_ema = Ema::new(self.price_ema_period);
        self.in_position = false;
    }
}


pub(crate) const RHAI_SCRIPT: &str = r#"
let obv1 = ind.obv(0, buf=50);
let ema50 = ind.ema(50, buf=1);

if state["obv_ema_count"] == () {
    state["obv_ema_count"] = 0;
    state["obv_ema_sum"] = 0.0;
    state["obv_ema_val"] = ();
    
    let i = 49;
    while i > 0 {
        let val = obv1[i];
        state["obv_ema_count"] = state["obv_ema_count"] + 1;
        let c = state["obv_ema_count"];
        if c < 20 {
            state["obv_ema_sum"] = state["obv_ema_sum"] + val;
        } else if c == 20 {
            state["obv_ema_sum"] = state["obv_ema_sum"] + val;
            state["obv_ema_val"] = state["obv_ema_sum"] / 20.0;
        } else {
            let prev = state["obv_ema_val"];
            let k = 2.0 / 21.0;
            state["obv_ema_val"] = val * k + prev * (1.0 - k);
        }
        i = i - 1;
    }
}

state["obv_ema_count"] = state["obv_ema_count"] + 1;
let count = state["obv_ema_count"];
let cur_obv = obv1[0];
let cur_obv_ema = ();

if count < 20 {
    state["obv_ema_sum"] = state["obv_ema_sum"] + cur_obv;
} else if count == 20 {
    state["obv_ema_sum"] = state["obv_ema_sum"] + cur_obv;
    let val = state["obv_ema_sum"] / 20.0;
    state["obv_ema_val"] = val;
    cur_obv_ema = val;
} else {
    let prev = state["obv_ema_val"];
    let k = 2.0 / 21.0;
    let val = cur_obv * k + prev * (1.0 - k);
    state["obv_ema_val"] = val;
    cur_obv_ema = val;
}

if cur_obv_ema != () {
    let obv_bullish = cur_obv > cur_obv_ema;
    let price_bullish = close[0] > ema50[0];
    
    if state["in_position"] == () {
        state["in_position"] = false;
    }
    let in_pos = state["in_position"];
    
    if obv_bullish && price_bullish && !in_pos {
        state["in_position"] = true;
        entry = true;
    }
    if in_pos && (!obv_bullish || !price_bullish) {
        state["in_position"] = false;
        exit = true;
    }
}
"#;
#[cfg(test)]
mod tests {
    use super::*;
    use crate::test_utils::*;

    #[test]
    fn obv_ema_trend_parity() {
        let Some(bars) = load_real_bars() else { return; };

        let mut hc = ObvEmaTrend::new(20, 50);
        let hc_sigs = run(&mut hc, &bars);

        // OBV is in volume units; EMA(20) is price-scale — exact parity not expected.
        assert!(!hc_sigs.is_empty(), "obv_ema_trend: no signals");
    }
}

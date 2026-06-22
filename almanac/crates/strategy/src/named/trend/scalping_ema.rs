use alm_core::{bar::Bar, signal::Signal, strategy::Strategy};
use alm_indicator::{Atr, Ema};

/// Scalping EMA — fast/slow EMA cross confirmed by ATR expansion.
///
/// Long  when fast EMA crosses above slow EMA AND ATR > ATR MA (momentum expanding).
/// Close when fast EMA crosses below slow EMA.
///
/// Default: fast=8, slow=21, atr=14, atr_ma=20
pub struct ScalpingEma {
    fast: Ema,
    slow: Ema,
    atr: Atr,
    atr_ma: Ema,
    fast_p: usize,
    slow_p: usize,
    atr_p: usize,
    atr_ma_p: usize,
    prev_fast: Option<f64>,
    prev_slow: Option<f64>,
}

impl ScalpingEma {
    pub fn new(fast: usize, slow: usize, atr_period: usize, atr_ma_period: usize) -> Self {
        Self {
            fast: Ema::new(fast),
            slow: Ema::new(slow),
            atr: Atr::new(atr_period),
            atr_ma: Ema::new(atr_ma_period),
            fast_p: fast,
            slow_p: slow,
            atr_p: atr_period,
            atr_ma_p: atr_ma_period,
            prev_fast: None,
            prev_slow: None,
        }
    }
}

impl Strategy for ScalpingEma {
    fn script(&self) -> Option<&'static str> {
        Some(RHAI_SCRIPT)
    }

    fn on_bar(&mut self, bar: &Bar) -> Vec<Signal> {
        let fast = self.fast.update(bar.close);
        let slow = self.slow.update(bar.close);
        let atr_val = self.atr.update(bar.high, bar.low, bar.close);

        let atr_ma = atr_val.and_then(|a| self.atr_ma.update(a.atr));

        let (Some(f), Some(s), Some(a), Some(a_ma)) = (fast, slow, atr_val, atr_ma) else {
            self.prev_fast = fast;
            self.prev_slow = slow;
            return vec![];
        };

        let prev_f = self.prev_fast.replace(f);
        let prev_s = self.prev_slow.replace(s);

        let (Some(pf), Some(ps)) = (prev_f, prev_s) else {
            return vec![];
        };

        let atr_expanding = a.atr > a_ma;

        // Bullish cross: fast crosses above slow
        if pf <= ps && f > s && atr_expanding {
            let strength = ((f - s) / s).clamp(0.0, 1.0);
            return vec![Signal::long(bar.timestamp, &bar.symbol, strength)];
        }

        // Bearish cross: fast crosses below slow
        if pf >= ps && f < s {
            return vec![Signal::exit(bar.timestamp, &bar.symbol)];
        }

        vec![]
    }

    fn name(&self) -> &str {
        "scalping_ema"
    }

    fn reset(&mut self) {
        self.fast = Ema::new(self.fast_p);
        self.slow = Ema::new(self.slow_p);
        self.atr = Atr::new(self.atr_p);
        self.atr_ma = Ema::new(self.atr_ma_p);
        self.prev_fast = None;
        self.prev_slow = None;
    }
}


pub(crate) const RHAI_SCRIPT: &str = r#"
let fast = ind.ema(8, buf=2);
let slow = ind.ema(21, buf=2);
let atr14 = ind.atr(14, buf=8);

if state["atr_ema_count"] == () {
    state["atr_ema_count"] = 0;
    state["atr_ema_sum"] = 0.0;
    state["atr_ema_val"] = ();
    
    let i = 7;
    while i > 0 {
        let val = atr14[i].atr;
        state["atr_ema_count"] = state["atr_ema_count"] + 1;
        state["atr_ema_sum"] = state["atr_ema_sum"] + val;
        i = i - 1;
    }
}

state["atr_ema_count"] = state["atr_ema_count"] + 1;
let count = state["atr_ema_count"];
let cur_atr = atr14[0].atr;
let cur_atr_ema = ();

if count < 20 {
    state["atr_ema_sum"] = state["atr_ema_sum"] + cur_atr;
} else if count == 20 {
    state["atr_ema_sum"] = state["atr_ema_sum"] + cur_atr;
    let val = state["atr_ema_sum"] / 20.0;
    state["atr_ema_val"] = val;
    cur_atr_ema = val;
} else {
    let prev = state["atr_ema_val"];
    let k = 2.0 / 21.0;
    let val = cur_atr * k + prev * (1.0 - k);
    state["atr_ema_val"] = val;
    cur_atr_ema = val;
}

if cur_atr_ema != () {
    let cross_up = fast[1] <= slow[1] && fast[0] > slow[0];
    let cross_down = fast[1] >= slow[1] && fast[0] < slow[0];
    let atr_expanding = cur_atr > cur_atr_ema;
    
    if cross_up && atr_expanding {
        let val = (fast[0] - slow[0]) / slow[0];
        strength = val.clamp(0.0, 1.0);
        entry = true;
    }
    if cross_down {
        exit = true;
    }
}
"#;
#[cfg(test)]
mod tests {
    use crate::test_utils::*;
    use super::*;

    

    #[test]
    fn test_scalping_ema_no_signal_before_warmup() {
        let mut s = ScalpingEma::new(8, 21, 14, 20);
        let Some(bars) = load_real_bars() else { return; };
        for b in bars.iter().take(20) {
            assert!(s.on_bar(b).is_empty());
        }
    }

    #[test]
    fn test_scalping_ema_long_on_cross() {
        let Some(bars) = load_real_bars() else { return; };
        let mut s = ScalpingEma::new(3, 7, 3, 5);
        let mut sigs = vec![];
        for b in &bars {
            sigs.extend(s.on_bar(b));
        }
        use alm_core::signal::Direction;
        assert!(sigs.iter().any(|s| s.direction == Direction::Long));
    }
}

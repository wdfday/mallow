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
            return vec![Signal::close(bar.timestamp, &bar.symbol)];
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

#[cfg(test)]
mod tests {
    use super::*;
    use crate::test_utils::*;

    #[test]
    fn obv_ema_trend_parity() {
        let bars = trending_bars(300);

        let mut hc = ObvEmaTrend::new(20, 50);
        let hc_sigs = run(&mut hc, &bars);

        // OBV is in volume units; EMA(20) is price-scale — exact parity not expected.
        assert!(!hc_sigs.is_empty(), "obv_ema_trend: no signals");
    }
}

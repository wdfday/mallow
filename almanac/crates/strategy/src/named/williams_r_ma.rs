use alm_core::{bar::Bar, signal::Signal, strategy::Strategy};
use alm_indicator::{Ema, WilliamsR};

/// Williams %R + EMA trend filter.
///
/// Long when %R exits oversold (crosses above -80) while price is above the trend EMA.
/// Close when %R enters overbought (crosses below -20) or price falls below EMA.
pub struct WilliamsRMa {
    wr: WilliamsR,
    ema: Ema,
    prev_wr: Option<f64>,
    in_position: bool,
    wr_period: usize,
    ema_period: usize,
    oversold: f64,
    overbought: f64,
}

impl WilliamsRMa {
    pub fn new(wr_period: usize, ema_period: usize, oversold: f64, overbought: f64) -> Self {
        Self {
            wr: WilliamsR::new(wr_period),
            ema: Ema::new(ema_period),
            prev_wr: None,
            in_position: false,
            wr_period,
            ema_period,
            oversold,
            overbought,
        }
    }
}

impl Strategy for WilliamsRMa {
    fn on_bar(&mut self, bar: &Bar) -> Vec<Signal> {
        let wr = self.wr.update(bar.high, bar.low, bar.close);
        let ema = self.ema.update(bar.close);

        let (Some(wr), Some(ema)) = (wr, ema) else {
            return vec![];
        };

        let Some(prev_wr) = self.prev_wr else {
            self.prev_wr = Some(wr);
            return vec![];
        };

        let exited_oversold = prev_wr <= self.oversold && wr > self.oversold;
        let entered_overbought = prev_wr >= self.overbought && wr < self.overbought;
        self.prev_wr = Some(wr);

        if exited_oversold && bar.close > ema && !self.in_position {
            self.in_position = true;
            return vec![Signal::long(bar.timestamp, &bar.symbol, 1.0)];
        }
        if self.in_position && (entered_overbought || bar.close < ema) {
            self.in_position = false;
            return vec![Signal::close(bar.timestamp, &bar.symbol)];
        }
        vec![]
    }

    fn name(&self) -> &str {
        "williams_r_ma"
    }

    fn reset(&mut self) {
        self.wr = WilliamsR::new(self.wr_period);
        self.ema = Ema::new(self.ema_period);
        self.prev_wr = None;
        self.in_position = false;
    }
}

#[cfg(test)]
mod tests {

}

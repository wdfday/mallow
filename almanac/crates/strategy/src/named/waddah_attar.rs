use alm_core::{bar::Bar, signal::Signal, strategy::Strategy};
use alm_indicator::{BBands, Macd};

/// Bot #44 — Volume Explosion (Waddah Attar Explosion).
///
/// The explosion value is defined as the change in MACD line × sensitivity (150),
/// compared against the Bollinger Band width (dead zone).
///
/// Long when explosion > BB width AND MACD histogram positive (bullish pressure).
/// Closes when explosion falls below BB width OR histogram turns negative.
///
/// References: Waddah Attar indicator — originally a volume-based momentum burst detector.
pub struct WaddahAttar {
    macd: Macd,
    bb: BBands,
    prev_macd_line: Option<f64>,
    sensitivity: f64,
    fast: usize,
    slow: usize,
    signal_period: usize,
    bb_period: usize,
    bb_std: f64,
}

impl WaddahAttar {
    pub fn new(fast: usize, slow: usize, bb_period: usize, bb_std: f64) -> Self {
        // signal_period = 1 for simpler MACD difference calculation
        Self {
            macd: Macd::new(fast, slow, 9),
            bb: BBands::new(bb_period, bb_std),
            prev_macd_line: None,
            sensitivity: 150.0,
            fast,
            slow,
            signal_period: 9,
            bb_period,
            bb_std,
        }
    }
}

impl Strategy for WaddahAttar {
    fn on_bar(&mut self, bar: &Bar) -> Vec<Signal> {
        let macd_val = self.macd.update(bar.close);
        let bb_val = self.bb.update(bar.close);

        let (Some(m), Some(bb)) = (macd_val, bb_val) else {
            return vec![];
        };

        let Some(prev_macd) = self.prev_macd_line else {
            self.prev_macd_line = Some(m.macd);
            return vec![];
        };

        // Explosion = change in MACD line × sensitivity
        let explosion = (m.macd - prev_macd) * self.sensitivity;
        let dead_zone = bb.upper - bb.lower; // BB width

        self.prev_macd_line = Some(m.macd);

        if explosion > dead_zone && m.histogram > 0.0 {
            return vec![Signal::long(bar.timestamp, &bar.symbol, 1.0)];
        }
        if explosion < dead_zone || m.histogram < 0.0 {
            return vec![Signal::close(bar.timestamp, &bar.symbol)];
        }
        vec![]
    }

    fn name(&self) -> &str {
        "waddah_attar"
    }

    fn reset(&mut self) {
        self.macd = Macd::new(self.fast, self.slow, self.signal_period);
        self.bb = BBands::new(self.bb_period, self.bb_std);
        self.prev_macd_line = None;
    }
}

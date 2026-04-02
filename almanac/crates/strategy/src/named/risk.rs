use alm_core::{portfolio::Portfolio, signal::Signal, strategy::RiskManager};

/// Fixed fractional risk: risk `pct` of equity per trade.
pub struct FixedFractional {
    /// Fraction of equity to allocate, e.g. 0.95 = 95%
    pub pct: f64,
    /// Maximum number of simultaneous open positions
    pub max_positions: usize,
    /// Minimum tradeable lot size.
    /// `0.0` → fractional (crypto, forex).
    /// `1.0` → whole shares (US stocks).
    /// `100.0` → VN stocks (HOSE lot = 100 shares).
    pub lot_size: f64,
}

impl FixedFractional {
    pub fn new(pct: f64, max_positions: usize) -> Self {
        Self { pct, max_positions, lot_size: 1.0 }
    }

    pub fn fractional(pct: f64, max_positions: usize) -> Self {
        Self { pct, max_positions, lot_size: 0.0 }
    }

    pub fn with_lot_size(mut self, lot_size: f64) -> Self {
        self.lot_size = lot_size;
        self
    }
}

impl RiskManager for FixedFractional {
    fn validate(&self, _signal: &Signal, portfolio: &Portfolio) -> bool {
        portfolio.positions.len() < self.max_positions
    }

    fn size(&self, _signal: &Signal, portfolio: &Portfolio, price: f64) -> f64 {
        if price <= f64::EPSILON {
            return 0.0;
        }
        let raw = portfolio.cash * self.pct / price;
        if self.lot_size > f64::EPSILON {
            // Round down to nearest lot
            (raw / self.lot_size).floor() * self.lot_size
        } else {
            // Fractional — crypto / forex
            raw
        }
    }
}

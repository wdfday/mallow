use alm_core::{portfolio::Portfolio, signal::{Direction, Signal}, strategy::RiskManager};

/// Allocate a fixed fraction of **cash** per trade.
///
/// `qty = cash × pct × strength / price`
pub struct PercentEquity {
    pub pct: f64,
    pub max_positions: usize,
    /// `0.0` → fractional. `1.0` → whole shares. `100.0` → HOSE lots.
    pub lot_size: f64,
    pub strength_sizing: bool,
}

impl PercentEquity {
    pub fn new(pct: f64, max_positions: usize) -> Self {
        Self { pct, max_positions, lot_size: 1.0, strength_sizing: true }
    }
    /// Fractional variant (no lot rounding — crypto / forex).
    pub fn fractional(pct: f64, max_positions: usize) -> Self {
        Self { pct, max_positions, lot_size: 0.0, strength_sizing: true }
    }
    pub fn with_lot_size(mut self, lot_size: f64) -> Self { self.lot_size = lot_size; self }
    pub fn with_strength_sizing(mut self, v: bool) -> Self { self.strength_sizing = v; self }
}

impl RiskManager for PercentEquity {
    fn validate(&self, signal: &Signal, portfolio: &Portfolio) -> bool {
        if signal.direction == Direction::Exit { return true; }
        if let Some(pos) = portfolio.positions.get(&signal.symbol) {
            let same = matches!(
                (signal.direction, pos.is_long()),
                (Direction::Long, true) | (Direction::Short, false)
            ) && pos.qty.abs() > f64::EPSILON;
            if same { return false; }
        }
        portfolio.positions.len() < self.max_positions
    }

    fn size(&self, signal: &Signal, portfolio: &Portfolio, price: f64) -> f64 {
        if price <= f64::EPSILON { return 0.0; }
        let s = if self.strength_sizing { signal.strength } else { 1.0 };
        let raw = portfolio.cash * self.pct * s / price;
        if self.lot_size > f64::EPSILON { (raw / self.lot_size).floor() * self.lot_size } else { raw }
    }
}

use alm_core::{portfolio::Portfolio, signal::{Direction, Signal}, strategy::RiskManager};

/// Fixed-USD: always allocates `amount_usd` notional per trade.
///
/// `qty = amount_usd / price`
pub struct FixedUsd {
    pub amount_usd: f64,
    pub max_positions: usize,
    pub lot_size: f64,
}

impl FixedUsd {
    pub fn new(amount_usd: f64, max_positions: usize) -> Self {
        Self { amount_usd, max_positions, lot_size: 0.0 }
    }
    pub fn with_lot_size(mut self, lot_size: f64) -> Self { self.lot_size = lot_size; self }
}

impl RiskManager for FixedUsd {
    fn validate(&self, signal: &Signal, portfolio: &Portfolio) -> bool {
        if signal.direction == Direction::Exit { return true; }
        if let Some(pos) = portfolio.positions.get(&signal.symbol) {
            let same = matches!(
                (signal.direction, pos.is_long()),
                (Direction::Long, true) | (Direction::Short, false)
            ) && pos.qty.abs() > f64::EPSILON;
            if same { return false; }
        }
        if portfolio.cash < self.amount_usd { return false; }
        portfolio.positions.len() < self.max_positions
    }

    fn size(&self, _signal: &Signal, _portfolio: &Portfolio, price: f64) -> f64 {
        if price <= f64::EPSILON { return 0.0; }
        let raw = self.amount_usd / price;
        if self.lot_size > f64::EPSILON { (raw / self.lot_size).floor() * self.lot_size } else { raw }
    }
}

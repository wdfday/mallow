use alm_core::{portfolio::Portfolio, signal::{Direction, Signal}, strategy::RiskManager};

/// Fixed-quantity: always trades exactly `qty` units.
pub struct FixedQuantity {
    pub qty: f64,
    pub max_positions: usize,
}

impl FixedQuantity {
    pub fn new(qty: f64, max_positions: usize) -> Self {
        Self { qty, max_positions }
    }
}

impl RiskManager for FixedQuantity {
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

    fn size(&self, _signal: &Signal, _portfolio: &Portfolio, _price: f64) -> f64 {
        self.qty
    }
}

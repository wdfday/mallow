use alm_core::{portfolio::Portfolio, signal::{Direction, Signal}, strategy::RiskManager};

/// Equal-weight: each slot gets `equity / max_positions`.
pub struct EqualWeight {
    pub max_positions: usize,
    pub lot_size: f64,
    pub strength_sizing: bool,
}

impl EqualWeight {
    pub fn new(max_positions: usize) -> Self {
        Self { max_positions, lot_size: 0.0, strength_sizing: true }
    }
    pub fn with_lot_size(mut self, lot_size: f64) -> Self { self.lot_size = lot_size; self }
    pub fn with_strength_sizing(mut self, v: bool) -> Self { self.strength_sizing = v; self }

    fn equity(portfolio: &Portfolio) -> f64 {
        portfolio.equity_curve.last().map(|p| p.equity).unwrap_or(portfolio.initial_capital)
    }
}

impl RiskManager for EqualWeight {
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
        if price <= f64::EPSILON || self.max_positions == 0 { return 0.0; }
        let slot = Self::equity(portfolio) / self.max_positions as f64;
        let s = if self.strength_sizing { signal.strength } else { 1.0 };
        let raw = slot * s / price;
        if self.lot_size > f64::EPSILON { (raw / self.lot_size).floor() * self.lot_size } else { raw }
    }
}

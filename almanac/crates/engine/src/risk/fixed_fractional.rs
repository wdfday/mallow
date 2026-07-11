use alm_core::{portfolio::Portfolio, signal::{Direction, Signal}, strategy::RiskManager};

/// Classic Ralph Vince fixed-fractional: risk `risk_per_trade` of equity to the
/// signal's own stop distance.
///
/// `qty = equity × risk_per_trade × strength / |entry − stop|`
///
/// Returns `0.0` when `signal.stop_price` is absent — cannot size without a stop.
pub struct FixedFractional {
    pub risk_per_trade: f64,
    pub max_positions: usize,
    pub lot_size: f64,
    pub strength_sizing: bool,
}

impl FixedFractional {
    pub fn new(risk_per_trade: f64, max_positions: usize) -> Self {
        Self { risk_per_trade, max_positions, lot_size: 0.0, strength_sizing: true }
    }
    pub fn with_lot_size(mut self, lot_size: f64) -> Self { self.lot_size = lot_size; self }
    pub fn with_strength_sizing(mut self, v: bool) -> Self { self.strength_sizing = v; self }

    fn equity(portfolio: &Portfolio) -> f64 {
        portfolio.equity_curve.last().map(|p| p.equity).unwrap_or(portfolio.initial_capital)
    }
}

impl RiskManager for FixedFractional {
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
        let dist = match signal.stop_price {
            Some(sl) if signal.is_offset => sl.abs(),
            Some(sl) => (price - sl).abs(),
            None => return 0.0,
        };
        if dist <= f64::EPSILON { return 0.0; }
        let raw = Self::equity(portfolio) * self.risk_per_trade / dist;
        if self.lot_size > f64::EPSILON { (raw / self.lot_size).floor() * self.lot_size } else { raw }
    }
}

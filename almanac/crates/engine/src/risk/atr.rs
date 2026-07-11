use alm_core::{portfolio::Portfolio, signal::{Direction, Signal}, strategy::RiskManager};

/// ATR-based risk-parity sizing.
///
/// `qty = equity × risk_per_trade × strength / (atr_multiplier × signal.atr)`
///
/// Reads ATR from `signal.atr` (set by the strategy). Returns `0.0` when the
/// signal carries no ATR — the strategy must compute and attach it.
pub struct AtrSizing {
    pub risk_per_trade: f64,
    pub atr_multiplier: f64,
    pub max_positions: usize,
    pub strength_sizing: bool,
}

impl AtrSizing {
    pub fn new(risk_per_trade: f64, atr_multiplier: f64, max_positions: usize) -> Self {
        Self { risk_per_trade, atr_multiplier, max_positions, strength_sizing: true }
    }

    pub fn with_strength_sizing(mut self, v: bool) -> Self { self.strength_sizing = v; self }

    fn equity(portfolio: &Portfolio) -> f64 {
        portfolio.equity_curve.last().map(|p| p.equity).unwrap_or(portfolio.initial_capital)
    }
}

impl RiskManager for AtrSizing {
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
        let atr = match signal.atr.filter(|a| *a > f64::EPSILON) {
            Some(a) => a,
            None => return 0.0,
        };
        let stop_dist = atr * self.atr_multiplier;
        if stop_dist <= f64::EPSILON { return 0.0; }
        (Self::equity(portfolio) * self.risk_per_trade / stop_dist).max(0.0)
    }
}

use alm_core::{portfolio::Portfolio, signal::{Direction, Signal}, strategy::RiskManager};

/// Kelly criterion: `f* = W − (1−W)/R`, applied as `qty = f* × fraction × equity / price`.
///
/// Uses live portfolio trade stats when available; falls back to constructor priors.
pub struct KellySizing {
    pub win_rate: f64,
    pub avg_win: f64,
    pub avg_loss: f64,
    /// Fractional Kelly multiplier (e.g. `0.5` = half-Kelly).
    pub fraction: f64,
    pub max_positions: usize,
    pub strength_sizing: bool,
}

impl KellySizing {
    pub fn new(win_rate: f64, avg_win: f64, avg_loss: f64, fraction: f64, max_positions: usize) -> Self {
        Self { win_rate, avg_win, avg_loss, fraction, max_positions, strength_sizing: true }
    }
    pub fn with_strength_sizing(mut self, v: bool) -> Self { self.strength_sizing = v; self }

    fn kelly_f(win_rate: f64, avg_win: f64, avg_loss: f64) -> f64 {
        if avg_win < f64::EPSILON || avg_loss < f64::EPSILON { return 0.0; }
        win_rate - (1.0 - win_rate) / (avg_win / avg_loss)
    }
}

impl RiskManager for KellySizing {
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
        let (wr, aw, al) = if portfolio.trades.is_empty() {
            (self.win_rate, self.avg_win, self.avg_loss)
        } else {
            let wins: Vec<_>   = portfolio.trades.iter().filter(|t| t.is_winner()).collect();
            let losses: Vec<_> = portfolio.trades.iter().filter(|t| !t.is_winner()).collect();
            let wr = wins.len() as f64 / portfolio.trades.len() as f64;
            let aw = if wins.is_empty()   { self.avg_win }
                else { wins.iter().map(|t| t.pnl_pct).sum::<f64>() / wins.len() as f64 };
            let al = if losses.is_empty() { self.avg_loss }
                else { losses.iter().map(|t| t.pnl_pct.abs()).sum::<f64>() / losses.len() as f64 };
            (wr, aw, al)
        };
        let f = Self::kelly_f(wr, aw, al);
        if f <= 0.0 { return 0.0; }
        let eq = portfolio.equity_curve.last().map(|p| p.equity).unwrap_or(portfolio.initial_capital);
        let s  = if self.strength_sizing { signal.strength } else { 1.0 };
        (eq * (self.fraction * f).min(1.0) * s / price).max(0.0)
    }
}

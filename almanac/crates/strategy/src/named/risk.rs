use alm_core::{bar::Bar, portfolio::Portfolio, signal::{Direction, Signal}, strategy::RiskManager};
use alm_indicator::Atr;

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
    fn validate(&self, signal: &Signal, portfolio: &Portfolio) -> bool {
        // Close always allowed — never block an exit.
        if signal.direction == Direction::Close { return true; }
        // Block same-direction re-entry for this symbol (prevents unintended pyramiding).
        if let Some(pos) = portfolio.positions.get(&signal.symbol) {
            let same_dir = matches!(
                (signal.direction, pos.is_long()),
                (Direction::Long, true) | (Direction::Short, false)
            ) && pos.qty.abs() > f64::EPSILON;
            if same_dir { return false; }
        }
        portfolio.positions.len() < self.max_positions
    }

    fn size(&self, signal: &Signal, portfolio: &Portfolio, price: f64) -> f64 {
        if price <= f64::EPSILON { return 0.0; }
        let raw = portfolio.cash * self.pct * signal.strength / price;
        if self.lot_size > f64::EPSILON {
            (raw / self.lot_size).floor() * self.lot_size
        } else {
            raw
        }
    }
}

// ── ATR-based position sizing ─────────────────────────────────────────────────

/// ATR-based position sizing: size = (equity × risk_per_trade) / (atr_multiplier × ATR).
///
/// Risks a fixed fraction of equity per trade, scaled by volatility.
/// A wider ATR reduces position size, tighter ATR increases it.
pub struct AtrSizing {
    /// Fraction of equity to risk per trade (e.g. 0.01 = 1%).
    pub risk_per_trade: f64,
    /// ATR multiplier for stop distance (e.g. 2.0 = 2× ATR stop).
    pub atr_multiplier: f64,
    /// Maximum simultaneous open positions.
    pub max_positions: usize,
    /// Internal ATR indicator (period 14 by default).
    atr: Atr,
}

impl AtrSizing {
    pub fn new(risk_per_trade: f64, atr_multiplier: f64, max_positions: usize) -> Self {
        Self {
            risk_per_trade,
            atr_multiplier,
            max_positions,
            atr: Atr::standard(), // period 14
        }
    }

    pub fn with_atr_period(mut self, period: usize) -> Self {
        self.atr = Atr::new(period);
        self
    }

    /// Current equity from portfolio (last equity curve point or initial capital).
    fn current_equity(portfolio: &Portfolio) -> f64 {
        portfolio
            .equity_curve
            .last()
            .map(|p| p.equity)
            .unwrap_or(portfolio.initial_capital)
    }
}

impl RiskManager for AtrSizing {
    fn validate(&self, signal: &Signal, portfolio: &Portfolio) -> bool {
        if signal.direction == Direction::Close { return true; }
        if let Some(pos) = portfolio.positions.get(&signal.symbol) {
            let same_dir = matches!(
                (signal.direction, pos.is_long()),
                (Direction::Long, true) | (Direction::Short, false)
            ) && pos.qty.abs() > f64::EPSILON;
            if same_dir { return false; }
        }
        portfolio.positions.len() < self.max_positions
    }

    fn size(&self, signal: &Signal, portfolio: &Portfolio, price: f64) -> f64 {
        if price <= f64::EPSILON { return 0.0; }
        let current_atr = match self.atr.value() {
            Some(v) if v > f64::EPSILON => v,
            _ => return 0.0,
        };
        let equity = Self::current_equity(portfolio);
        let stop_distance = self.atr_multiplier * current_atr;
        let raw = (equity * self.risk_per_trade * signal.strength) / stop_distance;
        raw.max(0.0)
    }

    fn on_bar(&mut self, bar: &Bar) {
        self.atr.update(bar.high, bar.low, bar.close);
    }
}

// ── Kelly criterion position sizing ──────────────────────────────────────────

// ── Equal-weight position sizing ──────────────────────────────────────────────

/// Equal-weight sizing: each slot gets `equity / max_positions`.
///
/// Allocates a fixed fraction of total equity to each position regardless of
/// volatility or conviction.  When all slots are full the new signal is rejected.
///
/// # Example
/// ```rust,ignore
/// // 5-symbol portfolio, 0.1 lot minimum (e.g. crypto), each position = 20% equity
/// let risk = EqualWeight::new(5).with_lot_size(0.1);
/// ```
pub struct EqualWeight {
    /// Maximum simultaneous open positions.
    pub max_positions: usize,
    /// Minimum tradeable lot.  `0.0` → fractional.  `1.0` → whole shares.
    pub lot_size: f64,
}

impl EqualWeight {
    pub fn new(max_positions: usize) -> Self {
        Self { max_positions, lot_size: 0.0 }
    }

    /// Set the minimum lot size (e.g. `1.0` for whole shares, `100.0` for HOSE).
    pub fn with_lot_size(mut self, lot_size: f64) -> Self {
        self.lot_size = lot_size;
        self
    }

    fn equity(portfolio: &Portfolio) -> f64 {
        portfolio.equity_curve.last().map(|p| p.equity).unwrap_or(portfolio.initial_capital)
    }
}

impl RiskManager for EqualWeight {
    fn validate(&self, signal: &Signal, portfolio: &Portfolio) -> bool {
        if signal.direction == Direction::Close { return true; }
        if let Some(pos) = portfolio.positions.get(&signal.symbol) {
            let same_dir = matches!(
                (signal.direction, pos.is_long()),
                (Direction::Long, true) | (Direction::Short, false)
            ) && pos.qty.abs() > f64::EPSILON;
            if same_dir { return false; }
        }
        portfolio.positions.len() < self.max_positions
    }

    fn size(&self, signal: &Signal, portfolio: &Portfolio, price: f64) -> f64 {
        if price <= f64::EPSILON || self.max_positions == 0 { return 0.0; }
        let slot_value = Self::equity(portfolio) / self.max_positions as f64;
        let raw = slot_value * signal.strength / price;
        if self.lot_size > f64::EPSILON {
            (raw / self.lot_size).floor() * self.lot_size
        } else {
            raw
        }
    }
}

/// Kelly criterion position sizing.
///
/// Uses historical trade statistics from the portfolio to compute the Kelly fraction.
/// `size = fraction × kelly_f × equity / price`
/// where `kelly_f = win_rate / avg_loss - (1 - win_rate) / avg_win`.
///
/// The initial `win_rate`, `avg_win`, `avg_loss` are used until enough trades accumulate.
pub struct KellySizing {
    /// Initial win rate estimate (e.g. 0.5 = 50%).
    pub win_rate: f64,
    /// Initial average win as a fraction (e.g. 0.05 = 5%).
    pub avg_win: f64,
    /// Initial average loss as a fraction (e.g. 0.02 = 2%, always positive).
    pub avg_loss: f64,
    /// Fractional Kelly multiplier to reduce risk (e.g. 0.5 = half Kelly).
    pub fraction: f64,
    /// Maximum simultaneous open positions.
    pub max_positions: usize,
}

impl KellySizing {
    pub fn new(
        win_rate: f64,
        avg_win: f64,
        avg_loss: f64,
        fraction: f64,
        max_positions: usize,
    ) -> Self {
        Self { win_rate, avg_win, avg_loss, fraction, max_positions }
    }

    /// Compute Kelly fraction from historical trades (or fall back to initial estimates).
    fn compute_kelly_f(win_rate: f64, avg_win: f64, avg_loss: f64) -> f64 {
        if avg_win < f64::EPSILON || avg_loss < f64::EPSILON {
            return 0.0;
        }
        win_rate / avg_loss - (1.0 - win_rate) / avg_win
    }
}

impl RiskManager for KellySizing {
    fn validate(&self, signal: &Signal, portfolio: &Portfolio) -> bool {
        if signal.direction == Direction::Close { return true; }
        if let Some(pos) = portfolio.positions.get(&signal.symbol) {
            let same_dir = matches!(
                (signal.direction, pos.is_long()),
                (Direction::Long, true) | (Direction::Short, false)
            ) && pos.qty.abs() > f64::EPSILON;
            if same_dir { return false; }
        }
        portfolio.positions.len() < self.max_positions
    }

    fn size(&self, signal: &Signal, portfolio: &Portfolio, price: f64) -> f64 {
        if price <= f64::EPSILON { return 0.0; }

        let (win_rate, avg_win, avg_loss) = if portfolio.trades.is_empty() {
            (self.win_rate, self.avg_win, self.avg_loss)
        } else {
            let trades = &portfolio.trades;
            let wins: Vec<_> = trades.iter().filter(|t| t.is_winner()).collect();
            let losses: Vec<_> = trades.iter().filter(|t| !t.is_winner()).collect();
            let wr = wins.len() as f64 / trades.len() as f64;
            let aw = if wins.is_empty() { self.avg_win }
                else { wins.iter().map(|t| t.pnl_pct).sum::<f64>() / wins.len() as f64 };
            let al = if losses.is_empty() { self.avg_loss }
                else { losses.iter().map(|t| t.pnl_pct.abs()).sum::<f64>() / losses.len() as f64 };
            (wr, aw, al)
        };

        let kelly_f = Self::compute_kelly_f(win_rate, avg_win, avg_loss);
        if kelly_f <= 0.0 { return 0.0; }

        let effective_f = (self.fraction * kelly_f).min(1.0);
        let equity = portfolio.equity_curve.last()
            .map(|p| p.equity).unwrap_or(portfolio.initial_capital);
        let raw = equity * effective_f * signal.strength / price;
        raw.max(0.0)
    }
}

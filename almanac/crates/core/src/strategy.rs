use crate::{bar::Bar, portfolio::{Portfolio, PortfolioSnapshot}, signal::Signal};

/// Core strategy interface.
/// Strategies are stateful — they hold indicator state and are updated bar-by-bar.
pub trait Strategy: Send {
    /// Process a new bar and return zero or more signals.
    ///
    /// Called once per bar, before `on_window`. Use this for indicator-based
    /// signal generation (RSI, MACD, crossovers, etc.).
    fn on_bar(&mut self, bar: &Bar) -> Vec<Signal>;

    /// Window-based hook for geometric / multi-bar pattern detection.
    ///
    /// Called once per bar **after** `on_bar`, with a slice of the most recent
    /// bars ordered oldest-first. The slice length grows until `window_size`
    /// is reached, then stays fixed (sliding window).
    ///
    /// Default is a no-op — override when using pattern detectors from `alm-pattern`.
    fn on_window(&mut self, _bars: &[Bar]) -> Vec<Signal> {
        vec![]
    }

    fn name(&self) -> &str;

    /// Reset all indicator state (used between batch backtest runs).
    fn reset(&mut self);

    /// Called by the engine once per bar **before** `on_bar`, with a snapshot of
    /// the current portfolio state (cash, equity, open positions).
    ///
    /// Default is a no-op — override in strategies that need to know current
    /// position state (e.g. to avoid double-entry or to size based on equity).
    ///
    /// # Example
    /// ```rust,ignore
    /// fn set_portfolio_snapshot(&mut self, snapshot: &PortfolioSnapshot) {
    ///     self.in_position = snapshot.is_long(&self.symbol);
    /// }
    /// ```
    fn set_portfolio_snapshot(&mut self, _snapshot: &PortfolioSnapshot) {}
}

/// Blanket impl so `Box<dyn Strategy>` can be used as a concrete `Strategy`.
impl Strategy for Box<dyn Strategy> {
    fn on_bar(&mut self, bar: &Bar) -> Vec<Signal> {
        (**self).on_bar(bar)
    }
    fn on_window(&mut self, bars: &[Bar]) -> Vec<Signal> {
        (**self).on_window(bars)
    }
    fn name(&self) -> &str {
        (**self).name()
    }
    fn reset(&mut self) {
        (**self).reset()
    }
    fn set_portfolio_snapshot(&mut self, snapshot: &PortfolioSnapshot) {
        (**self).set_portfolio_snapshot(snapshot)
    }
}

/// Risk management — validates signals and sizes positions.
pub trait RiskManager: Send {
    /// Return false to discard the signal entirely.
    fn validate(&self, signal: &Signal, portfolio: &Portfolio) -> bool;

    /// Return the quantity to trade (in units of the asset).
    fn size(&self, signal: &Signal, portfolio: &Portfolio, price: f64) -> f64;
}

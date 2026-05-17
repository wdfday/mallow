use std::collections::HashMap;
use crate::{bar::Bar, portfolio::{Portfolio, PortfolioSnapshot}, regime::RegimeState, signal::Signal};

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

    /// One-line description of this strategy's entry/exit logic.
    /// Defaults to an empty string; named strategies should override.
    fn description(&self) -> &'static str { "" }

    /// The canonical script equivalent for this named strategy.
    /// Used by tooling and the catalog endpoint to expose the script.
    /// Returns `None` for strategies that have no script equivalent.
    fn script(&self) -> Option<&'static str> { None }

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

    /// Called once per bar with the current detected market regime, before `on_bar`.
    /// Default is a no-op — override to adapt strategy behaviour by regime.
    ///
    /// # Example
    /// ```rust,ignore
    /// fn on_regime(&mut self, regime: &RegimeState) {
    ///     self.active = regime.is_trending(); // only trade in trending markets
    /// }
    /// ```
    fn on_regime(&mut self, _regime: &RegimeState) {}

    /// Return true if this strategy uses `on_window` — engine pre-allocates the sliding bar buffer.
    /// Default false — override when you implement `on_window`.
    fn uses_window(&self) -> bool {
        false
    }

    /// Return true if this strategy uses `set_portfolio_snapshot` — engine skips the snapshot
    /// call for strategies that don't need it (avoids cloning the portfolio every bar).
    /// Default false — override when you implement `set_portfolio_snapshot`.
    fn uses_portfolio_snapshot(&self) -> bool {
        false
    }

    /// Drain and return all collected indicator series since the last call (or since construction).
    ///
    /// Keys: e.g. `"rsi14.value"`, `"macd.histogram"`.
    /// Values: chronological `(timestamp_ms, value)` pairs — one per bar after warmup.
    ///
    /// Default returns empty — only script strategies populate this.
    fn take_indicator_series(&mut self) -> HashMap<String, Vec<(i64, f64)>> {
        HashMap::new()
    }

    /// Latest regime state computed by the strategy (e.g. from a `regime { ... }`
    /// block in a script). The engine reads this after each `on_bar` to
    /// track regime transitions and tag trades.
    ///
    /// Default returns `None` — strategies without an internal regime detector
    /// leave this unset and the engine produces no regime summary.
    fn current_regime(&self) -> Option<&RegimeState> {
        None
    }

    /// Candle-transform spec declared inside the strategy source (e.g. via a
    /// `candle.transform("heiken_ashi")` directive at the top of a script
    /// script). Returns `(kind, optional smooth_period)`.
    ///
    /// The engine builder reads this and lets the script-level setting
    /// override the request-level `candle_type` field — strategy definition
    /// wins over execution config.
    ///
    /// Default returns `None` — most strategies have no script-level directive.
    fn script_candle_spec(&self) -> Option<(String, Option<usize>)> {
        None
    }

    /// Whether this strategy applies its own candle transform inside `on_bar`.
    ///
    /// When `true`, the engine MUST pass raw bars and skip its own
    /// `candle_transform` step — otherwise the bars would be transformed twice
    /// (once by engine, once by strategy). ScriptStrategy returns `true` because
    /// it owns its `candle.transform(...)` directive internally, so the same
    /// transform applies in both backtest and live (registry) paths.
    fn handles_candle_internally(&self) -> bool {
        false
    }
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
    fn description(&self) -> &'static str {
        (**self).description()
    }
    fn script(&self) -> Option<&'static str> {
        (**self).script()
    }
    fn reset(&mut self) {
        (**self).reset()
    }
    fn set_portfolio_snapshot(&mut self, snapshot: &PortfolioSnapshot) {
        (**self).set_portfolio_snapshot(snapshot)
    }
    fn on_regime(&mut self, regime: &RegimeState) {
        (**self).on_regime(regime)
    }
    fn uses_window(&self) -> bool {
        (**self).uses_window()
    }
    fn uses_portfolio_snapshot(&self) -> bool {
        (**self).uses_portfolio_snapshot()
    }
    fn take_indicator_series(&mut self) -> HashMap<String, Vec<(i64, f64)>> {
        (**self).take_indicator_series()
    }
    fn current_regime(&self) -> Option<&RegimeState> {
        (**self).current_regime()
    }
    fn script_candle_spec(&self) -> Option<(String, Option<usize>)> {
        (**self).script_candle_spec()
    }
    fn handles_candle_internally(&self) -> bool {
        (**self).handles_candle_internally()
    }
}

/// Risk management — validates signals and sizes positions.
pub trait RiskManager: Send {
    /// Return false to discard the signal entirely.
    fn validate(&self, signal: &Signal, portfolio: &Portfolio) -> bool;

    /// Return the quantity to trade (in units of the asset).
    fn size(&self, signal: &Signal, portfolio: &Portfolio, price: f64) -> f64;

    /// Called once per bar with the current bar data, before signal processing.
    /// Default is a no-op — override for ATR-based or other bar-aware sizing.
    fn on_bar(&mut self, _bar: &Bar) {}
}

/// Blanket impl so `Box<dyn RiskManager>` can be used as a concrete `RiskManager`.
impl RiskManager for Box<dyn RiskManager> {
    fn validate(&self, signal: &Signal, portfolio: &Portfolio) -> bool {
        (**self).validate(signal, portfolio)
    }
    fn size(&self, signal: &Signal, portfolio: &Portfolio, price: f64) -> f64 {
        (**self).size(signal, portfolio, price)
    }
    fn on_bar(&mut self, bar: &Bar) {
        (**self).on_bar(bar)
    }
}

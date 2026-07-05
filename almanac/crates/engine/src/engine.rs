use std::collections::VecDeque;

use crate::core::EngineCore;
use alm_core::{
    bar::Bar,
    exit::IntraBarMode,
    order::{Fill, Side},
    signal::{Direction, Signal},
    strategy::{RiskManager, Strategy},
};
use alm_data::BarFeed;
use alm_report::BacktestReport;
use tracing::{debug, info};


/// Controls engine behaviour when a directional signal arrives while the
/// opposite position is already open.
///
/// | Variant | What happens |
/// |---------|-------------|
/// | `Exit`  | Close the existing position; **do not** open a new one. |
/// | `Flip`  | Close the existing position, then immediately open a new one in the signalled direction using the normal `risk.size()` path. |
///
/// When no policy is set (default), the engine's original net-qty behaviour
/// is preserved: the directional order is sent as-is and the portfolio nets
/// the quantities, which may leave a residual or an unintended exposure when
/// the two legs are sized differently.
pub use crate::reverse_policy::ReversePolicy;

/// Strip the synthetic pyramiding leg suffix (`SYM#3` → `SYM`).
pub(crate) fn base_symbol(key: &str) -> &str {
    match key.find('#') {
        Some(i) => &key[..i],
        None => key,
    }
}

/// Resolve signal-level TP/SL to absolute prices. When `is_offset`, target/stop are
/// magnitudes whose side depends on direction (long → TP above / SL below; short →
/// TP below / SL above). When not offset they are already absolute and returned as-is.
pub(crate) fn resolve_offset_levels(
    price: f64,
    target: Option<f64>,
    stop: Option<f64>,
    is_offset: bool,
    is_long: bool,
) -> (Option<f64>, Option<f64>) {
    if !is_offset {
        return (target, stop);
    }
    let tp = target.map(|d| if is_long { price + d.abs() } else { price - d.abs() });
    let sl = stop.map(|d| if is_long { price - d.abs() } else { price + d.abs() });
    (tp, sl)
}

/// Default sliding-window size for `on_window()` calls.
const DEFAULT_WINDOW_SIZE: usize = 200;

/// Event-driven backtesting engine — single symbol.
///
/// Drives a single [`BarFeed`] and calls `Strategy::on_bar` / `on_window`
/// each bar. All per-bar housekeeping (fills, exit rules, equity, sizing,
/// pyramiding, regime) is handled by the embedded [`EngineCore`].
///
/// ```text
/// BarFeed ──► MarketEvent
///                │
///                ├─► SimBroker::process_pending   ──► FillEvent (prev-bar orders)
///                ├─► Portfolio::record_equity
///                ├─► PositionTracker::update_and_check ──► force_close if SL/TP/time hit
///                ├─► Strategy::on_bar / on_window ──► SignalEvent
///                │
///                └── SignalEvent
///                        │
///                        ├─► RiskManager::validate + size
///                        └─► OrderEvent ──► SimBroker::submit ──► (filled next bar)
/// ```
pub struct Engine<S: Strategy, R: RiskManager> {
    pub core: EngineCore<R>,
    pub strategy: S,
    /// Sliding window of recent bars for `on_window()` pattern strategies.
    bar_window: VecDeque<Bar>,
    window_size: usize,
    /// When `true`, signals from bar[i] are buffered and executed at bar[i+1].open
    /// via a direct fill. When `false` (default), signals flow through the broker
    /// pending queue (same effective timing, less bookkeeping).
    next_bar: bool,
    pending_signals: Vec<Signal>,
    metrics_symbol: String,
}

// ── Constructor ───────────────────────────────────────────────────────────────

impl<S: Strategy, R: RiskManager> Engine<S, R> {
    /// Create an engine backed by a `SyncBus` (zero-overhead VecDeque bus).
    pub fn sync(
        initial_capital: f64,
        strategy: S,
        risk: R,
        commission_pct: f64,
        slippage_pct: f64,
    ) -> Self {
        Engine {
            core: EngineCore::new(initial_capital, risk, commission_pct, slippage_pct),
            strategy,
            bar_window: VecDeque::new(),
            window_size: DEFAULT_WINDOW_SIZE,
            next_bar: false,
            pending_signals: Vec::new(),
            metrics_symbol: String::new(),
        }
    }
}

// ── Builder methods ───────────────────────────────────────────────────────────

impl<S: Strategy, R: RiskManager> Engine<S, R> {
    /// Override the sliding window size (default: 200 bars).
    pub fn with_window(mut self, size: usize) -> Self {
        self.window_size = size.max(1);
        self
    }

    /// Enable next-bar execution mode.
    ///
    /// When `true`, signals from bar[i] are buffered and executed at bar[i+1].open
    /// via a direct fill, making the execution timing explicit.
    /// When `false` (default), signals flow through the broker pending queue
    /// which also fills at next bar's open (same effective timing).
    pub fn with_next_bar(mut self, enabled: bool) -> Self {
        self.next_bar = enabled;
        self
    }

    /// Enable single-entry guard: block Long/Short re-entry while a position
    /// in the same direction is already open.
    pub fn with_single_entry(mut self) -> Self {
        self.core.single_entry = true;
        self
    }

    /// Set the policy for handling a directional signal that arrives while the
    /// opposite position is already open. See [`ReversePolicy`] for options.
    pub fn with_reverse_policy(mut self, policy: ReversePolicy) -> Self {
        self.core.reverse_policy = Some(policy);
        self
    }

    /// Enable pyramiding: accumulate up to `max_units` same-direction legs,
    /// adding only when price advances past the last leg and total exposure
    /// stays under `max_position_pct` (fraction of equity; 0 = no cap).
    pub fn with_pyramiding(mut self, max_units: usize, max_position_pct: f64) -> Self {
        self.core.max_units = max_units.max(1);
        self.core.max_position_pct = max_position_pct.max(0.0);
        self
    }

    /// Warm indicators on bars before `until` (Unix ms) without trading.
    pub fn with_warmup_until(mut self, until: i64) -> Self {
        self.core.warmup_until = if until > 0 { Some(until) } else { None };
        self
    }

    /// Switch pyramiding to INDEPENDENT-leg mode (each leg its own position `SYM#n`).
    pub fn with_independent_legs(mut self) -> Self {
        self.core.pyramid_merge = false;
        self
    }

    /// Set how bar OHLC is read when checking signal-level exit levels.
    pub fn with_intra_bar_mode(mut self, mode: IntraBarMode) -> Self {
        self.core.intra_bar_mode = mode;
        self
    }
}

// ── Run loop ──────────────────────────────────────────────────────────────────

impl<S: Strategy, R: RiskManager> Engine<S, R> {
    /// Run a full backtest over the provided feed. Returns a [`BacktestReport`].
    pub fn run(&mut self, feed: &mut impl BarFeed, risk_free_annual: f64) -> BacktestReport {
        let symbol = feed.symbol().to_string();
        let strategy_name = self.strategy.name().to_string();
        let initial_capital = self.core.portfolio.cash;
        info!(symbol = %symbol, strategy = %strategy_name, capital = initial_capital, "backtest start");

        self.metrics_symbol = symbol.clone();
        self.core.metrics_strategy = strategy_name.clone();

        let mut bar_count: usize = 0;
        while let Some(bar) = feed.next() {
            bar_count += 1;
            metrics::counter!("alm_engine_bars_total",
                "strategy" => strategy_name.clone()
            ).increment(1);

            // Flush next-bar pending signals: execute at this bar's open.
            if self.next_bar && !self.pending_signals.is_empty() {
                let pending = std::mem::take(&mut self.pending_signals);
                for sig in pending {
                    self.execute_signal_at_open(&sig, &bar);
                }
            }

            // Update sliding window before dispatch.
            self.bar_window.push_back(bar.clone());
            if self.bar_window.len() > self.window_size {
                self.bar_window.pop_front();
            }

            let trading = self.core.run_base_tick(&bar);

            if trading {
                // Portfolio snapshot → strategy (opt-in).
                if self.strategy.uses_portfolio_snapshot() {
                    let prices =
                        self.core.leg_price_map(&bar.symbol, self.core.last_price);
                    let snapshot = self.core.portfolio.snapshot(&prices);
                    self.strategy.set_portfolio_snapshot(&snapshot);
                }

                let signals = self.collect_signals(&bar);
                self.core.dispatch_signals(signals);
            } else {
                // Warm-up: advance strategy indicator state only.
                let _ = self.strategy.on_bar(&bar);
            }
        }

        // Force-close all open positions at last bar's close.
        if let Some(last_bar) = self.bar_window.back().cloned() {
            self.core.force_close_all(&last_bar);
        }

        let report =
            self.core.build_report(&strategy_name, &symbol, risk_free_annual);

        // Record aggregate run stats.
        let total = report.total_trades as u64;
        let wins =
            (report.win_rate_pct / 100.0 * report.total_trades as f64).round() as u64;
        let losses = total.saturating_sub(wins);
        metrics::counter!("alm_engine_trades_total",
            "strategy" => strategy_name.clone(), "result" => "all"
        ).increment(total);
        metrics::counter!("alm_engine_trades_total",
            "strategy" => strategy_name.clone(), "result" => "win"
        ).increment(wins);
        metrics::counter!("alm_engine_trades_total",
            "strategy" => strategy_name.clone(), "result" => "loss"
        ).increment(losses);
        metrics::histogram!("alm_engine_run_bars",
            "strategy" => strategy_name.clone()
        ).record(bar_count as f64);

        info!(
            symbol = %symbol,
            strategy = %strategy_name,
            bars = bar_count,
            trades = report.total_trades,
            final_equity = format!("{:.2}", report.final_equity),
            total_return_pct = format!("{:.2}", report.total_return_pct),
            sharpe = format!("{:.3}", report.sharpe_ratio),
            max_dd_pct = format!("{:.2}", report.max_drawdown_pct),
            "backtest complete"
        );
        report
    }

    /// Collect signals from the strategy: `on_bar` plus optional `on_window`.
    /// Also updates regime state.
    fn collect_signals(&mut self, bar: &Bar) -> Vec<Signal> {
        #[cfg(not(target_arch = "wasm32"))]
        let eval_start = std::time::Instant::now();

        let mut signals = self.strategy.on_bar(bar);

        #[cfg(not(target_arch = "wasm32"))]
        metrics::histogram!("alm_engine_strategy_eval_us",
            "strategy" => self.core.metrics_strategy.clone()
        ).record(eval_start.elapsed().as_micros() as f64);

        // Update regime tracking from strategy.
        let regime = self.strategy.current_regime().cloned();
        self.core.update_regime(bar.timestamp, regime);

        // Pattern-based signals (sliding window, opt-in).
        if self.strategy.uses_window() {
            let (a, b) = self.bar_window.as_slices();
            let window_signals = if b.is_empty() {
                self.strategy.on_window(a)
            } else {
                let contiguous: Vec<Bar> = a.iter().chain(b.iter()).cloned().collect();
                self.strategy.on_window(&contiguous)
            };
            signals.extend(window_signals);
        }

        signals
    }

    /// Execute a buffered signal directly at bar.open (used when `next_bar = true`).
    fn execute_signal_at_open(&mut self, signal: &Signal, bar: &Bar) {
        match signal.direction {
            Direction::Exit => {
                let base = signal.symbol.as_str();
                let legs: Vec<(String, f64, bool)> = self
                    .core
                    .portfolio
                    .positions
                    .iter()
                    .filter(|(k, p)| {
                        base_symbol(k) == base && p.qty.abs() > f64::EPSILON
                    })
                    .map(|(k, p)| (k.clone(), p.qty.abs(), p.is_long()))
                    .collect();
                for (key, qty, is_long) in legs {
                    let side = if is_long { Side::Sell } else { Side::Buy };
                    let fill = self.core.broker.force_close(
                        &key, qty, side, bar.timestamp, bar.open,
                    );
                    self.core.portfolio.apply_fill(&fill);
                    let regime_state = self.core.regime_at_entry.remove(&key);
                    if let Some(tr) = self.core.position_trackers.remove(&key) {
                        if let Some(trade) = self.core.portfolio.trades.last_mut() {
                            trade.mae_pct = tr.mae;
                            trade.mfe_pct = tr.mfe;
                            trade.bars_held = tr.bars_held;
                            if let Some(state) = regime_state {
                                trade.regime_at_entry = Some(state);
                            }
                        }
                    }
                    debug!(symbol = %key, qty, ?side, "next-bar close fill at open");
                }
            }
            Direction::Long => {
                if self.core.single_entry
                    && self
                        .core
                        .portfolio
                        .positions
                        .get(&signal.symbol)
                        .map_or(false, |p| p.is_long())
                {
                    return;
                }
                if self.core.risk.validate(signal, &self.core.portfolio) {
                    let qty =
                        self.core.risk.size(signal, &self.core.portfolio, bar.open);
                    if qty > f64::EPSILON {
                        let slip = 1.0 + self.core.broker.slippage_pct;
                        let fill_price = bar.open * slip;
                        let commission =
                            fill_price * qty * self.core.broker.commission_pct;
                        let fill = Fill {
                            timestamp: bar.timestamp,
                            symbol: signal.symbol.clone(),
                            side: Side::Buy,
                            qty,
                            price: fill_price,
                            commission,
                        };
                        self.core.portfolio.apply_fill(&fill);
                        let (tp_abs, sl_abs) = resolve_offset_levels(
                            fill_price,
                            signal.target_price,
                            signal.stop_price,
                            signal.is_offset,
                            true,
                        );
                        let was_new = !self
                            .core
                            .position_trackers
                            .contains_key(&signal.symbol);
                        self.core
                            .position_trackers
                            .entry(signal.symbol.clone())
                            .or_insert_with(|| {
                                alm_core::exit::PositionTracker::with_levels(
                                    fill_price,
                                    sl_abs,
                                    tp_abs,
                                    signal.trailing_stop_pct,
                                    signal.max_bars_held,
                                    true,
                                )
                            });
                        if was_new {
                            if let Some(state) = &self.core.last_regime {
                                self.core.regime_at_entry
                                    .insert(signal.symbol.clone(), state.clone());
                            }
                        }
                        debug!(symbol = %signal.symbol, qty, "next-bar long fill at open");
                    }
                }
            }
            Direction::Short => {
                if self.core.single_entry
                    && self
                        .core
                        .portfolio
                        .positions
                        .get(&signal.symbol)
                        .map_or(false, |p| p.is_short())
                {
                    return;
                }
                if self.core.risk.validate(signal, &self.core.portfolio) {
                    let qty =
                        self.core.risk.size(signal, &self.core.portfolio, bar.open);
                    if qty > f64::EPSILON {
                        let slip = 1.0 - self.core.broker.slippage_pct;
                        let fill_price = bar.open * slip;
                        let commission =
                            fill_price * qty * self.core.broker.commission_pct;
                        let fill = Fill {
                            timestamp: bar.timestamp,
                            symbol: signal.symbol.clone(),
                            side: Side::Sell,
                            qty,
                            price: fill_price,
                            commission,
                        };
                        self.core.portfolio.apply_fill(&fill);
                        let (tp_abs, sl_abs) = resolve_offset_levels(
                            fill_price,
                            signal.target_price,
                            signal.stop_price,
                            signal.is_offset,
                            false,
                        );
                        let was_new = !self
                            .core
                            .position_trackers
                            .contains_key(&signal.symbol);
                        self.core
                            .position_trackers
                            .entry(signal.symbol.clone())
                            .or_insert_with(|| {
                                alm_core::exit::PositionTracker::with_levels(
                                    fill_price,
                                    sl_abs,
                                    tp_abs,
                                    signal.trailing_stop_pct,
                                    signal.max_bars_held,
                                    false,
                                )
                            });
                        if was_new {
                            if let Some(state) = &self.core.last_regime {
                                self.core.regime_at_entry
                                    .insert(signal.symbol.clone(), state.clone());
                            }
                        }
                        debug!(symbol = %signal.symbol, qty, "next-bar short fill at open");
                    }
                }
            }
        }
    }

    /// Reset engine state for reuse in batch optimisation.
    pub fn reset(&mut self, initial_capital: f64) {
        self.core.reset(initial_capital);
        self.bar_window.clear();
        self.pending_signals.clear();
        self.strategy.reset();
    }
}


// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use alm_core::regime::{RegimeDimension, RegimeState};
    use alm_data::ParquetFeed;
    use crate::risk::PercentEquity;
    use std::path::Path;

    const M1: &str = concat!(
        env!("CARGO_MANIFEST_DIR"),
        "/../data/testdata/BTCUSDT/M1/BTCUSDT_M1_2026-01.parquet"
    );
    const SYM: &str = "BTCUSDT";

    fn btc_feed() -> ParquetFeed {
        ParquetFeed::load(Path::new(M1), SYM).expect("testdata missing")
    }

    // ── Strategy fixtures ─────────────────────────────────────────────────────

    struct AlwaysLong { symbol: String }
    impl Strategy for AlwaysLong {
        fn on_bar(&mut self, bar: &Bar) -> Vec<Signal> {
            vec![Signal::long(bar.timestamp, &self.symbol, 1.0)]
        }
        fn name(&self) -> &str { "always_long" }
        fn reset(&mut self) {}
    }

    struct AlwaysShort { symbol: String }
    impl Strategy for AlwaysShort {
        fn on_bar(&mut self, bar: &Bar) -> Vec<Signal> {
            vec![Signal::short(bar.timestamp, &self.symbol, 1.0)]
        }
        fn name(&self) -> &str { "always_short" }
        fn reset(&mut self) {}
    }

    struct ReverseMockStrategy {
        i: usize,
        symbol: String,
    }
    impl Strategy for ReverseMockStrategy {
        fn on_bar(&mut self, bar: &Bar) -> Vec<Signal> {
            self.i += 1;
            if self.i == 10 {
                vec![Signal::long(bar.timestamp, &self.symbol, 1.0)]
            } else if self.i == 20 {
                vec![Signal::short(bar.timestamp, &self.symbol, 1.0)]
            } else {
                vec![]
            }
        }
        fn name(&self) -> &str { "reverse_mock" }
        fn reset(&mut self) { self.i = 0; }
    }

    struct FakeRegimeStrategy {
        i: usize,
        symbol: String,
        regime: RegimeState,
    }

    impl FakeRegimeStrategy {
        fn new(symbol: &str) -> Self {
            Self {
                i: 0,
                symbol: symbol.into(),
                regime: RegimeState::new(
                    RegimeDimension::new(20.0, "ranging"),
                    RegimeDimension::new(1.0, "normal"),
                ),
            }
        }
    }

    impl Strategy for FakeRegimeStrategy {
        fn on_bar(&mut self, bar: &Bar) -> Vec<Signal> {
            if self.i == 50 {
                self.regime = RegimeState::new(
                    RegimeDimension::new(35.0, "trending"),
                    RegimeDimension::new(1.4, "high"),
                );
            }
            self.i += 1;
            match self.i {
                21 | 71 => vec![Signal::long(bar.timestamp, &self.symbol, 1.0)],
                61 | 81 => vec![Signal::exit(bar.timestamp, &self.symbol)],
                _ => vec![],
            }
        }
        fn name(&self) -> &str { "fake_regime" }
        fn reset(&mut self) {}
        fn current_regime(&self) -> Option<&RegimeState> { Some(&self.regime) }
    }

    // ── Helpers ───────────────────────────────────────────────────────────────

    /// Run an AlwaysLong engine on the BTC feed, return # trades produced.
    fn run_long(max_units: usize, single_entry: bool) -> usize {
        let mut engine = Engine::sync(
            10_000.0,
            AlwaysLong { symbol: SYM.into() },
            PercentEquity::fractional(0.10, 10),
            0.0, 0.0,
        )
        .with_pyramiding(max_units, 0.0);
        if single_entry {
            engine = engine.with_single_entry();
        }
        let mut feed = btc_feed();
        engine.run(&mut feed, 0.0);
        engine.core.portfolio.trades.len()
    }

    // ── Tests ─────────────────────────────────────────────────────────────────

    #[test]
    fn runs_to_completion_on_real_data() {
        let mut feed = btc_feed();
        let mut engine = Engine::sync(
            10_000.0,
            AlwaysLong { symbol: SYM.into() },
            PercentEquity::fractional(0.95, 1),
            0.001, 0.0005,
        )
        .with_single_entry();
        let report = engine.run(&mut feed, 0.0);
        assert!(report.total_trades >= 1, "expected trades on real BTC data");
        assert!(report.final_equity > 0.0, "equity should remain positive");
    }

    #[test]
    fn single_entry_produces_fewer_or_equal_trades_than_multi() {
        let single = run_long(1, true);
        let multi  = run_long(1, false);
        assert!(single <= multi, "single={single} multi={multi}");
    }

    #[test]
    fn pyramiding_accumulates_more_legs_than_single_unit() {
        let single  = run_long(1, false);
        let pyramid = run_long(4, false);
        assert!(pyramid >= single, "pyramid={pyramid} single={single}");
    }

    #[test]
    fn pyramiding_short_on_falling_price() {
        // Use real data — BTC had both up and down moves in Jan 2026.
        let mut e = Engine::sync(
            10_000.0,
            AlwaysShort { symbol: SYM.into() },
            PercentEquity::fractional(0.10, 10),
            0.0, 0.0,
        )
        .with_pyramiding(4, 0.0);
        let mut e_single = Engine::sync(
            10_000.0,
            AlwaysShort { symbol: SYM.into() },
            PercentEquity::fractional(0.10, 10),
            0.0, 0.0,
        );

        e.run(&mut btc_feed(), 0.0);
        e_single.run(&mut btc_feed(), 0.0);

        let pyramid = e.core.portfolio.trades.len();
        let single  = e_single.core.portfolio.trades.len();

        assert!(single >= 1, "single short produced no trades");
        assert!(pyramid >= single,
            "short pyramid={pyramid} should be >= single={single}");
    }

    #[test]
    fn regime_tagging_and_summary() {
        let mut feed = btc_feed();
        let mut engine = Engine::sync(
            10_000.0,
            FakeRegimeStrategy::new(SYM),
            PercentEquity::fractional(0.10, 1),
            0.0, 0.0,
        );
        let report = engine.run(&mut feed, 0.0);
        assert!(report.regime_summary.is_some(), "expected regime summary");
        let summary = report.regime_summary.unwrap();
        assert!(summary.changes.len() >= 1, "expected at least one regime change");
        assert!(!summary.trade_breakdown.is_empty(), "expected trade breakdown");
        let trending = summary.trade_breakdown.iter().find(|s| s.label == "trending/high");
        assert!(trending.is_some(), "expected trades in trending/high regime");
    }

    #[test]
    fn tp_sl_resolve_offset_levels_long() {
        let (tp, sl) = resolve_offset_levels(100.0, Some(10.0), Some(5.0), true, true);
        assert_eq!(tp, Some(110.0));
        assert_eq!(sl, Some(95.0));
        let (tp, sl) = resolve_offset_levels(100.0, Some(10.0), Some(5.0), true, false);
        assert_eq!(tp, Some(90.0));
        assert_eq!(sl, Some(105.0));
        let (tp, sl) = resolve_offset_levels(100.0, Some(110.0), Some(95.0), false, true);
        assert_eq!(tp, Some(110.0));
        assert_eq!(sl, Some(95.0));
    }

    #[test]
    fn test_reverse_policy_exit() {
        let mut feed = btc_feed();
        let mut engine = Engine::sync(
            10_000.0,
            ReverseMockStrategy { i: 0, symbol: SYM.into() },
            PercentEquity::fractional(0.10, 1),
            0.0, 0.0,
        )
        .with_reverse_policy(ReversePolicy::Exit);

        engine.run(&mut feed, 0.0);

        assert_eq!(engine.core.portfolio.trades.len(), 1);
        let open_qty = engine.core.portfolio.positions.get(SYM).map(|p| p.qty).unwrap_or(0.0);
        assert!(open_qty.abs() < 1e-5);
    }

    #[test]
    fn test_reverse_policy_exit_short_meets_long() {
        struct ReverseShortFirstStrategy {
            i: usize,
            symbol: String,
        }
        impl Strategy for ReverseShortFirstStrategy {
            fn on_bar(&mut self, bar: &Bar) -> Vec<Signal> {
                self.i += 1;
                if self.i == 10 {
                    vec![Signal::short(bar.timestamp, &self.symbol, 1.0)]
                } else if self.i == 20 {
                    vec![Signal::long(bar.timestamp, &self.symbol, 1.0)]
                } else {
                    vec![]
                }
            }
            fn name(&self) -> &str { "reverse_short_first" }
            fn reset(&mut self) { self.i = 0; }
        }

        let mut feed = btc_feed();
        let mut engine = Engine::sync(
            10_000.0,
            ReverseShortFirstStrategy { i: 0, symbol: SYM.into() },
            PercentEquity::fractional(0.10, 1),
            0.0, 0.0,
        )
        .with_reverse_policy(ReversePolicy::Exit);

        engine.run(&mut feed, 0.0);

        assert_eq!(engine.core.portfolio.trades.len(), 1);
        let open_qty = engine.core.portfolio.positions.get(SYM).map(|p| p.qty).unwrap_or(0.0);
        assert!(open_qty.abs() < 1e-5);
    }

    #[test]
    fn test_reverse_policy_none() {
        let mut feed = btc_feed();
        let mut engine = Engine::sync(
            10_000.0,
            ReverseMockStrategy { i: 0, symbol: SYM.into() },
            PercentEquity::fractional(0.10, 1),
            0.0, 0.0,
        );

        engine.run(&mut feed, 0.0);

        // When reverse_policy is None, the Short signal at bar 20 is rejected by the sizer
        // because the Long position is still open and max_positions = 1.
        // So the Long position is never closed. It remains open until force close at the end.
        // Thus, completed trades count is 1 (the force closed Long position at the end).
        // Wait, is force close at the end counted in completed trades? Yes, force close generates a fill
        // which completes the trade.
        assert_eq!(engine.core.portfolio.trades.len(), 1);
        // And the trade exit reason should be EndOfData.
        assert_eq!(engine.core.portfolio.trades[0].exit_reason, alm_core::exit::ExitReason::EndOfData);
    }

    #[test]
    fn test_reverse_policy_flip() {
        let mut feed = btc_feed();
        let mut engine = Engine::sync(
            10_000.0,
            ReverseMockStrategy { i: 0, symbol: SYM.into() },
            PercentEquity::fractional(0.10, 1),
            0.0, 0.0,
        )
        .with_reverse_policy(ReversePolicy::Flip);

        engine.run(&mut feed, 0.0);

        assert_eq!(engine.core.portfolio.trades.len(), 2);
    }
}

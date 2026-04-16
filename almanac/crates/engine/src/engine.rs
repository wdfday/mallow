use std::collections::{HashMap, VecDeque};

use crate::broker::SimBroker;
use alm_core::{
    bar::Bar,
    bus::EventBus,
    event::{EquityEvent, Event, FillEvent, MarketEvent},
    exit::{ExitRules, PositionTracker},
    order::{Fill, OrderRequest, Side},
    portfolio::Portfolio,
    regime::{RegimeState, RegimeSummary, TrendRegime, VolRegime},
    signal::{Direction, Signal},
    strategy::{RiskManager, Strategy},
};
use alm_data::BarFeed;
use alm_indicator::RegimeDetector;
use alm_report::BacktestReport;
use tracing::debug;

use crate::bus_sync::SyncBus;
use crate::bus_tokio::TokioBus;

/// Default sliding-window size for `on_window()` calls.
const DEFAULT_WINDOW_SIZE: usize = 200;

/// Event-driven backtesting engine, generic over event bus implementation.
///
/// Components communicate exclusively through events:
///   MarketEvent → Strategy → SignalEvent → RiskManager → OrderEvent → Broker → FillEvent
///
/// Use `Engine::sync()` for max throughput backtesting (VecDeque).
/// Use `Engine::tokio()` for live-extensible mode (tokio::mpsc).
pub struct Engine<S: Strategy, R: RiskManager, B: EventBus> {
    pub portfolio: Portfolio,
    pub broker: SimBroker,
    pub strategy: S,
    pub risk: R,
    bus: B,
    /// Track last known price per symbol for risk sizing.
    last_prices: HashMap<String, f64>,
    /// Sliding window of recent bars for pattern detection via `on_window()`.
    bar_window: VecDeque<Bar>,
    /// Maximum bars kept in `bar_window`.
    window_size: usize,
    /// Optional exit rules applied on top of strategy signal logic.
    exit_rules: ExitRules,
    /// Per-position state for evaluating exit rules.
    position_trackers: HashMap<String, PositionTracker>,
    /// When true, signals from bar[i] are buffered and executed at bar[i+1].open.
    /// When false (default), signals flow through the broker pending queue
    /// which also fills at next bar's open.
    next_bar: bool,
    /// Signals buffered when `next_bar = true`.
    pending_signals: Vec<Signal>,
    /// When true, the engine itself blocks Long/Short signals for a symbol that
    /// already has an open position in the same direction.  This is a safety net
    /// on top of RiskManager.validate() — useful for multi-symbol strategies whose
    /// individual strategies don't track in-position state.
    single_entry: bool,
    // ── Regime detection ──────────────────────────────────────────────────────
    regime_detector: RegimeDetector,
    /// Regime transitions recorded on-change: (timestamp_ms, RegimeState).
    regime_changes: Vec<(i64, RegimeState)>,
    last_regime: Option<RegimeState>,
    /// Bar counts per (TrendRegime, VolRegime) pair for summary statistics.
    regime_bar_counts: HashMap<(TrendRegime, VolRegime), usize>,
    /// Total bars processed (including warm-up bars without a regime).
    total_bars: usize,
}

// ── Constructors ─────────────────────────────────────────────────────────────

impl<S: Strategy, R: RiskManager> Engine<S, R, SyncBus> {
    /// Create an engine with sync VecDeque event bus (max throughput).
    pub fn sync(
        initial_capital: f64,
        strategy: S,
        risk: R,
        commission_pct: f64,
        slippage_pct: f64,
    ) -> Self {
        Engine {
            portfolio: Portfolio::new(initial_capital),
            broker: SimBroker::new(commission_pct, slippage_pct),
            strategy,
            risk,
            bus: SyncBus::new(),
            last_prices: HashMap::new(),
            bar_window: VecDeque::new(),
            window_size: DEFAULT_WINDOW_SIZE,
            exit_rules: ExitRules::default(),
            position_trackers: HashMap::new(),
            next_bar: false,
            pending_signals: Vec::new(),
            single_entry: false,
            regime_detector: RegimeDetector::new(),
            regime_changes: Vec::new(),
            last_regime: None,
            regime_bar_counts: HashMap::new(),
            total_bars: 0,
        }
    }
}

impl<S: Strategy, R: RiskManager> Engine<S, R, TokioBus> {
    /// Create an engine with tokio::mpsc event bus (live-extensible).
    pub fn tokio(
        initial_capital: f64,
        strategy: S,
        risk: R,
        commission_pct: f64,
        slippage_pct: f64,
    ) -> Self {
        Engine {
            portfolio: Portfolio::new(initial_capital),
            broker: SimBroker::new(commission_pct, slippage_pct),
            strategy,
            risk,
            bus: TokioBus::new(),
            last_prices: HashMap::new(),
            bar_window: VecDeque::new(),
            window_size: DEFAULT_WINDOW_SIZE,
            exit_rules: ExitRules::default(),
            position_trackers: HashMap::new(),
            next_bar: false,
            pending_signals: Vec::new(),
            single_entry: false,
            regime_detector: RegimeDetector::new(),
            regime_changes: Vec::new(),
            last_regime: None,
            regime_bar_counts: HashMap::new(),
            total_bars: 0,
        }
    }

    /// Get a sender handle for the tokio bus (for external event producers).
    pub fn sender(&self) -> ::tokio::sync::mpsc::UnboundedSender<alm_core::Event> {
        self.bus.sender()
    }
}

// ── Shared engine logic ───────────────────────────────────────────────────────

impl<S: Strategy, R: RiskManager, B: EventBus> Engine<S, R, B> {
    /// Override the sliding window size (default: 200 bars).
    pub fn with_window(mut self, size: usize) -> Self {
        self.window_size = size.max(1);
        self
    }

    /// Customise regime detection thresholds.
    ///
    /// - `adx_threshold`: ADX above which market is Trending (default 25.0).
    /// - `chop_threshold`: Chop above which market is Ranging (default 61.8).
    /// - `vol_high_ratio`: ATR(14)/ATR(50) above which vol is High (default 1.3).
    /// - `vol_low_ratio`: ATR(14)/ATR(50) below which vol is Low (default 0.7).
    pub fn with_regime_thresholds(
        mut self,
        adx_threshold: f64,
        chop_threshold: f64,
        vol_high_ratio: f64,
        vol_low_ratio: f64,
    ) -> Self {
        self.regime_detector = RegimeDetector::new().with_thresholds(
            adx_threshold,
            chop_threshold,
            vol_high_ratio,
            vol_low_ratio,
        );
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

    /// Enable single-entry guard: the engine blocks Long/Short signals for a symbol
    /// that already has an open position in the same direction, independently of
    /// what `RiskManager::validate()` returns.
    ///
    /// Use this for multi-symbol strategies whose individual sub-strategies don't
    /// track their own `in_position` state.  Off by default (single-symbol strategies
    /// already manage `in_position` themselves, so the guard is harmless but redundant).
    pub fn with_single_entry(mut self) -> Self {
        self.single_entry = true;
        self
    }

    /// Set exit rules applied on top of strategy logic.
    ///
    /// ```rust,ignore
    /// let engine = Engine::sync(10_000.0, strategy, risk, 0.001, 0.0005)
    ///     .with_exit_rules(ExitRules {
    ///         stop_loss_pct: Some(0.05),
    ///         trailing_stop_pct: Some(0.02),
    ///         ..Default::default()
    ///     });
    /// ```
    pub fn with_exit_rules(mut self, rules: ExitRules) -> Self {
        self.exit_rules = rules;
        self
    }

    /// Run a full backtest over the provided feed. Returns a `BacktestReport`.
    pub fn run(&mut self, feed: &mut impl BarFeed, risk_free_annual: f64) -> BacktestReport {
        let symbol = feed.symbol().to_string();

        while let Some(bar) = feed.next() {
            // Flush next-bar pending signals: execute at this bar's open.
            if self.next_bar && !self.pending_signals.is_empty() {
                let pending = std::mem::take(&mut self.pending_signals);
                for sig in pending {
                    self.execute_signal_at_open(&sig, &bar);
                }
            }

            // Update sliding window before dispatch so on_window() sees the current bar.
            self.bar_window.push_back(bar.clone());
            if self.bar_window.len() > self.window_size {
                self.bar_window.pop_front();
            }

            // Move bar into the event — no extra clone here.
            self.bus.send(Event::Market(MarketEvent { bar }));

            while let Some(event) = self.bus.try_recv() {
                self.dispatch(event);
            }
        }

        // Force-close remaining open positions at the last bar's close.
        if let Some(bar) = self.bar_window.back().cloned() {
            let open_positions: Vec<_> = self
                .portfolio
                .positions
                .values()
                .filter(|p| p.qty.abs() > f64::EPSILON && p.symbol == symbol)
                .map(|p| (p.symbol.clone(), p.qty))
                .collect();

            for (sym, qty) in open_positions {
                // Long position → sell to close; short position → buy to cover.
                let side = if qty > 0.0 { Side::Sell } else { Side::Buy };
                let fill =
                    self.broker.force_close(&sym, qty.abs(), side, bar.timestamp, bar.close);
                self.portfolio.apply_fill(&fill);
            }
        }

        let mut report = BacktestReport::generate(
            self.strategy.name(),
            &symbol,
            &self.portfolio,
            risk_free_annual,
        );
        report.regime_summary = Some(self.compute_regime_summary());
        report
    }

    /// Execute a buffered signal directly at bar.open (used when `next_bar = true`).
    fn execute_signal_at_open(&mut self, signal: &Signal, bar: &Bar) {
        match signal.direction {
            Direction::Close => {
                if let Some(pos) = self.portfolio.positions.get(&signal.symbol) {
                    let qty = pos.qty.abs();
                    if qty > f64::EPSILON {
                        let side = if pos.is_long() { Side::Sell } else { Side::Buy };
                        let fill =
                            self.broker.force_close(&signal.symbol, qty, side, bar.timestamp, bar.open);
                        self.portfolio.apply_fill(&fill);
                        self.position_trackers.remove(&signal.symbol);
                        debug!(symbol = %signal.symbol, qty, ?side, "next-bar close fill at open");
                    }
                }
            }
            Direction::Long => {
                if self.single_entry && self.portfolio.positions.get(&signal.symbol)
                    .map_or(false, |p| p.is_long()) { return; }
                if self.risk.validate(signal, &self.portfolio) {
                    let qty = self.risk.size(signal, &self.portfolio, bar.open);
                    if qty > f64::EPSILON {
                        let slip = 1.0 + self.broker.slippage_pct;
                        let fill_price = bar.open * slip;
                        let commission = fill_price * qty * self.broker.commission_pct;
                        let fill = Fill {
                            timestamp: bar.timestamp,
                            symbol: signal.symbol.clone(),
                            side: Side::Buy,
                            qty,
                            price: fill_price,
                            commission,
                        };
                        self.portfolio.apply_fill(&fill);
                        self.position_trackers
                            .entry(signal.symbol.clone())
                            .or_insert_with(|| PositionTracker::new(fill_price));
                        debug!(symbol = %signal.symbol, qty, "next-bar long fill at open");
                    }
                }
            }
            Direction::Short => {
                if self.single_entry && self.portfolio.positions.get(&signal.symbol)
                    .map_or(false, |p| p.is_short()) { return; }
                if self.risk.validate(signal, &self.portfolio) {
                    let qty = self.risk.size(signal, &self.portfolio, bar.open);
                    if qty > f64::EPSILON {
                        let slip = 1.0 - self.broker.slippage_pct;
                        let fill_price = bar.open * slip;
                        let commission = fill_price * qty * self.broker.commission_pct;
                        let fill = Fill {
                            timestamp: bar.timestamp,
                            symbol: signal.symbol.clone(),
                            side: Side::Sell,
                            qty,
                            price: fill_price,
                            commission,
                        };
                        self.portfolio.apply_fill(&fill);
                        self.position_trackers
                            .entry(signal.symbol.clone())
                            .or_insert_with(|| PositionTracker::new(fill_price));
                        debug!(symbol = %signal.symbol, qty, "next-bar short fill at open");
                    }
                }
            }
        }
    }

    /// Dispatch a single event — may produce new events via the bus.
    fn dispatch(&mut self, event: Event) {
        match event {
            Event::Market(ref market) => {
                let bar = &market.bar;
                self.last_prices.insert(bar.symbol.clone(), bar.close);

                // Notify risk manager with current bar (e.g. for ATR-based sizing).
                self.risk.on_bar(bar);

                // ── Regime detection ──────────────────────────────────────────
                self.total_bars += 1;
                if let Some(regime) = self.regime_detector.update(bar) {
                    *self
                        .regime_bar_counts
                        .entry((regime.trend.clone(), regime.volatility.clone()))
                        .or_insert(0) += 1;
                    let changed =
                        self.last_regime.as_ref().map_or(true, |last| *last != regime);
                    if changed {
                        self.regime_changes.push((bar.timestamp, regime.clone()));
                        self.last_regime = Some(regime.clone());
                        debug!(regime = %regime.label(), "regime change");
                    }
                    self.strategy.on_regime(&regime);
                }

                // Process pending orders at this bar's open (avoids look-ahead bias).
                let fills = self.broker.process_pending(bar);
                for fill in fills {
                    self.bus.send(Event::Fill(FillEvent { fill }));
                }

                // Snapshot equity at bar close.
                let prices: HashMap<String, f64> =
                    [(bar.symbol.clone(), bar.close)].into_iter().collect();
                self.portfolio.record_equity(bar.timestamp, &prices);

                let eq = self.portfolio.equity(&prices);
                self.bus.send(Event::Equity(EquityEvent {
                    timestamp: bar.timestamp,
                    equity: eq,
                    cash: self.portfolio.cash,
                }));

                // ── Exit rules ────────────────────────────────────────────────
                // Check before strategy signals so SL/TP fires as soon as possible.
                if self.exit_rules.is_active() {
                    // Collect which symbols should be closed this bar.
                    let to_close: Vec<String> = self
                        .position_trackers
                        .iter_mut()
                        .filter_map(|(sym, tracker)| {
                            if tracker.update_and_check(bar.close, &self.exit_rules) {
                                Some(sym.clone())
                            } else {
                                None
                            }
                        })
                        .collect();

                    for sym in to_close {
                        // Remove tracker immediately to prevent re-firing next bar
                        // before the fill arrives.
                        self.position_trackers.remove(&sym);
                        let sig = Signal::close(bar.timestamp, &sym);
                        self.bus
                            .send(Event::Signal(alm_core::event::SignalEvent { signal: sig }));
                        debug!(symbol = %sym, "exit rule triggered → close");
                    }
                }

                // ── Portfolio snapshot → strategy ─────────────────────────────
                // Give the strategy a read-only view of current positions before
                // it decides what signals to emit. Non-breaking: default is no-op.
                let snapshot = self.portfolio.snapshot(&prices);
                self.strategy.set_portfolio_snapshot(&snapshot);

                // ── Indicator-based signals (bar-by-bar) ──────────────────────
                let signals = self.strategy.on_bar(bar);
                for signal in signals {
                    self.bus
                        .send(Event::Signal(alm_core::event::SignalEvent { signal }));
                }

                // ── Pattern-based signals (sliding window) ────────────────────
                let (a, b) = self.bar_window.as_slices();
                let window_signals = if b.is_empty() {
                    self.strategy.on_window(a)
                } else {
                    let contiguous: Vec<Bar> = a.iter().chain(b.iter()).cloned().collect();
                    self.strategy.on_window(&contiguous)
                };
                for signal in window_signals {
                    self.bus
                        .send(Event::Signal(alm_core::event::SignalEvent { signal }));
                }
            }

            Event::Signal(ref sig_event) => {
                // In next-bar mode: buffer signals to be executed at the next bar's open.
                if self.next_bar {
                    self.pending_signals.push(sig_event.signal.clone());
                    return;
                }
                let signal = &sig_event.signal;
                match signal.direction {
                    Direction::Close => {
                        if let Some(pos) = self.portfolio.positions.get(&signal.symbol) {
                            let qty = pos.qty.abs();
                            if qty > f64::EPSILON {
                                // Long → sell to close; Short → buy to cover
                                let side = if pos.is_long() { Side::Sell } else { Side::Buy };
                                let order = OrderRequest::market(
                                    signal.timestamp,
                                    &signal.symbol,
                                    side,
                                    qty,
                                );
                                self.bus
                                    .send(Event::Order(alm_core::event::OrderEvent { order }));
                                debug!(symbol = %signal.symbol, qty, ?side, "close signal → order");
                            }
                        }
                    }
                    Direction::Long => {
                        if self.single_entry {
                            if self.portfolio.positions.get(&signal.symbol)
                                .map_or(false, |p| p.is_long()) { return; }
                        }
                        if self.risk.validate(signal, &self.portfolio) {
                            let price =
                                self.last_prices.get(&signal.symbol).copied().unwrap_or(0.0);
                            let qty = self.risk.size(signal, &self.portfolio, price);
                            if qty > f64::EPSILON {
                                let order = OrderRequest::market(
                                    signal.timestamp,
                                    &signal.symbol,
                                    Side::Buy,
                                    qty,
                                );
                                self.bus
                                    .send(Event::Order(alm_core::event::OrderEvent { order }));
                                debug!(symbol = %signal.symbol, qty, "long signal → buy order");
                            }
                        }
                    }
                    Direction::Short => {
                        if self.single_entry {
                            if self.portfolio.positions.get(&signal.symbol)
                                .map_or(false, |p| p.is_short()) { return; }
                        }
                        if self.risk.validate(signal, &self.portfolio) {
                            let price =
                                self.last_prices.get(&signal.symbol).copied().unwrap_or(0.0);
                            let qty = self.risk.size(signal, &self.portfolio, price);
                            if qty > f64::EPSILON {
                                let order = OrderRequest::market(
                                    signal.timestamp,
                                    &signal.symbol,
                                    Side::Sell,
                                    qty,
                                );
                                self.bus
                                    .send(Event::Order(alm_core::event::OrderEvent { order }));
                                debug!(symbol = %signal.symbol, qty, "short signal → sell order");
                            }
                        }
                    }
                }
            }

            Event::Order(ref order_event) => {
                self.broker.submit(order_event.order.clone());
            }

            Event::Fill(ref fill_event) => {
                let fill = &fill_event.fill;
                self.portfolio.apply_fill(fill);

                // Maintain per-position tracker for exit rules.
                // After the fill, check whether the position is still open.
                let still_open = self
                    .portfolio
                    .positions
                    .get(&fill.symbol)
                    .map(|p| p.qty.abs() > f64::EPSILON)
                    .unwrap_or(false);

                if still_open {
                    // Only insert a new tracker when opening a position (not on add-to).
                    self.position_trackers
                        .entry(fill.symbol.clone())
                        .or_insert_with(|| PositionTracker::new(fill.price));
                } else {
                    // Position closed — remove tracker.
                    self.position_trackers.remove(&fill.symbol);
                }
            }

            Event::Equity(_) => {
                // Consumed by loggers / dashboards.
            }
        }
    }

    /// Reset engine state for reuse in batch optimization.
    pub fn reset(&mut self, initial_capital: f64) {
        self.portfolio = Portfolio::new(initial_capital);
        self.bar_window.clear();
        self.position_trackers.clear();
        self.pending_signals.clear();
        self.regime_detector.reset();
        self.regime_changes.clear();
        self.last_regime = None;
        self.regime_bar_counts.clear();
        self.total_bars = 0;
        self.strategy.reset();
    }

    /// Compute regime summary statistics from bar counts accumulated during `run()`.
    fn compute_regime_summary(&self) -> RegimeSummary {
        let detected_bars: usize = self.regime_bar_counts.values().sum();
        let pct = |count: usize| -> f64 {
            if detected_bars == 0 { 0.0 } else { count as f64 / detected_bars as f64 * 100.0 }
        };

        let trending = self
            .regime_bar_counts
            .iter()
            .filter(|((t, _), _)| *t == TrendRegime::Trending)
            .map(|(_, &c)| c)
            .sum::<usize>();
        let ranging = self
            .regime_bar_counts
            .iter()
            .filter(|((t, _), _)| *t == TrendRegime::Ranging)
            .map(|(_, &c)| c)
            .sum::<usize>();
        let neutral = self
            .regime_bar_counts
            .iter()
            .filter(|((t, _), _)| *t == TrendRegime::Neutral)
            .map(|(_, &c)| c)
            .sum::<usize>();
        let high_vol = self
            .regime_bar_counts
            .iter()
            .filter(|((_, v), _)| *v == VolRegime::High)
            .map(|(_, &c)| c)
            .sum::<usize>();
        let low_vol = self
            .regime_bar_counts
            .iter()
            .filter(|((_, v), _)| *v == VolRegime::Low)
            .map(|(_, &c)| c)
            .sum::<usize>();

        let changes = self
            .regime_changes
            .iter()
            .map(|(ts, r)| (*ts, r.label()))
            .collect();

        RegimeSummary {
            trending_pct: pct(trending),
            ranging_pct: pct(ranging),
            neutral_pct: pct(neutral),
            high_vol_pct: pct(high_vol),
            low_vol_pct: pct(low_vol),
            changes,
        }
    }
}

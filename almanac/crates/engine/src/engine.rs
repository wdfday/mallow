use std::collections::{HashMap, VecDeque};

use crate::broker::SimBroker;
use alm_core::{
    bar::Bar,
    bus::EventBus,
    event::{EquityEvent, Event, FillEvent, MarketEvent},
    exit::{ExitRules, PositionTracker},
    order::{OrderRequest, Side},
    portfolio::Portfolio,
    signal::{Direction, Signal},
    strategy::{RiskManager, Strategy},
};
use alm_data::BarFeed;
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

        BacktestReport::generate(
            self.strategy.name(),
            &symbol,
            &self.portfolio,
            risk_free_annual,
        )
    }

    /// Dispatch a single event — may produce new events via the bus.
    fn dispatch(&mut self, event: Event) {
        match event {
            Event::Market(ref market) => {
                let bar = &market.bar;
                self.last_prices.insert(bar.symbol.clone(), bar.close);

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
        self.strategy.reset();
    }
}

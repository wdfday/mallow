//! Multi-symbol backtesting engine.
//!
//! `MultiEngine` time-merges bars from multiple symbols via a min-heap and
//! routes each bar to a `MultiStrategy`. The strategy receives the current bar
//! plus a read-only view of the last-known price for every tracked symbol —
//! enabling cross-symbol logic (e.g. pair trading, relative-strength filters).
//!
//! # Event ordering guarantee
//!
//! Bars are processed in strict ascending timestamp order. When two symbols
//! share the same timestamp the order between them is deterministic (sorted by
//! symbol name) to ensure reproducible results.
//!
//! # Example
//!
//! ```rust,ignore
//! let engine = MultiEngine::new(10_000.0, strategy, risk, 0.001, 0.0);
//! engine.add_feed(BarVecFeed::new(btc_bars, "BTCUSDT"));
//! engine.add_feed(BarVecFeed::new(eth_bars, "ETHUSDT"));
//! let report = engine.run(0.0);
//! ```

use std::collections::{BinaryHeap, HashMap};
use std::cmp::Reverse;

use alm_core::{
    Bar,
    bus::EventBus,
    event::{EquityEvent, Event, FillEvent, MarketEvent},
    exit::{ExitRules, PositionTracker},
    order::{OrderRequest, Side},
    portfolio::Portfolio,
    signal::{Direction, Signal},
    strategy::RiskManager,
};
use alm_data::BarFeed;
use alm_report::BacktestReport;
use alm_strategy::candle_type::CandleTransform;

use alm_core::portfolio::PortfolioSnapshot;
use crate::{broker::SimBroker, bus_sync::SyncBus};

/// Strategy interface for multi-symbol engines.
///
/// Receives one bar at a time (the next chronological bar across all feeds)
/// plus a snapshot of the latest known price for every tracked symbol.
pub trait MultiStrategy: Send {
    fn on_bar(&mut self, bar: &Bar, last_prices: &HashMap<String, f64>) -> Vec<Signal>;

    /// Window-based hook — same semantics as `Strategy::on_window`.
    fn on_window(
        &mut self,
        _bar: &Bar,
        _window: &[Bar],
        _last_prices: &HashMap<String, f64>,
    ) -> Vec<Signal> {
        vec![]
    }

    fn name(&self) -> &str;
    fn reset(&mut self);

    fn set_portfolio_snapshot(&mut self, _snapshot: &PortfolioSnapshot) {}
}

/// Entry in the merge heap: (Reverse<timestamp>, symbol, bar).
/// Reverse so BinaryHeap acts as a min-heap.
struct HeapEntry {
    ts: Reverse<i64>,
    symbol: String,
    bar: Bar,
}

impl PartialEq for HeapEntry {
    fn eq(&self, other: &Self) -> bool {
        self.ts == other.ts && self.symbol == other.symbol
    }
}
impl Eq for HeapEntry {}

impl Ord for HeapEntry {
    fn cmp(&self, other: &Self) -> std::cmp::Ordering {
        self.ts
            .cmp(&other.ts)
            .then_with(|| self.symbol.cmp(&other.symbol))
    }
}
impl PartialOrd for HeapEntry {
    fn partial_cmp(&self, other: &Self) -> Option<std::cmp::Ordering> {
        Some(self.cmp(other))
    }
}

const DEFAULT_WINDOW_SIZE: usize = 200;

/// Multi-symbol event-driven backtesting engine.
pub struct MultiEngine<S: MultiStrategy, R: RiskManager> {
    pub portfolio: Portfolio,
    pub broker: SimBroker,
    pub strategy: S,
    pub risk: R,
    bus: SyncBus,

    /// Registered feeds indexed by symbol for O(1) refill lookup.
    feeds: HashMap<String, Box<dyn BarFeed>>,

    /// Merge heap — always holds the next bar from each feed.
    heap: BinaryHeap<HeapEntry>,

    /// Last known price per symbol.
    last_prices: HashMap<String, f64>,

    /// Per-symbol sliding window for on_window() calls.
    bar_windows: HashMap<String, std::collections::VecDeque<Bar>>,
    window_size: usize,

    exit_rules: ExitRules,
    position_trackers: HashMap<String, PositionTracker>,
    /// Pending TP/SL absolute price levels from entry Signal — consumed at Fill time.
    pending_signal_levels: HashMap<String, (Option<f64>, Option<f64>)>,
    /// Block re-entry in the same direction while a position is open.
    single_entry: bool,
    /// Optional candle transform applied before strategy sees each bar.
    /// Broker fills and exit-rule checks always use the raw bar.
    candle_transform: Option<CandleTransform>,
    /// Per-symbol sliding window of transformed bars for on_window().
    strategy_bar_windows: HashMap<String, std::collections::VecDeque<Bar>>,
}

impl<S: MultiStrategy, R: RiskManager> MultiEngine<S, R> {
    pub fn new(
        initial_capital: f64,
        strategy: S,
        risk: R,
        commission_pct: f64,
        slippage_pct: f64,
    ) -> Self {
        Self {
            portfolio: Portfolio::new(initial_capital),
            broker: SimBroker::new(commission_pct, slippage_pct),
            strategy,
            risk,
            bus: SyncBus::new(),
            feeds: HashMap::new(),
            heap: BinaryHeap::new(),
            last_prices: HashMap::new(),
            bar_windows: HashMap::new(),
            window_size: DEFAULT_WINDOW_SIZE,
            exit_rules: ExitRules::default(),
            position_trackers: HashMap::new(),
            pending_signal_levels: HashMap::new(),
            single_entry: false,
            candle_transform: None,
            strategy_bar_windows: HashMap::new(),
        }
    }

    pub fn with_exit_rules(mut self, rules: ExitRules) -> Self {
        self.exit_rules = rules;
        self
    }

    pub fn with_window_size(mut self, size: usize) -> Self {
        self.window_size = size;
        self
    }

    pub fn with_single_entry(mut self) -> Self {
        self.single_entry = true;
        self
    }

    pub fn with_candle_transform(mut self, transform: CandleTransform) -> Self {
        self.candle_transform = Some(transform);
        self
    }

    /// Register a data feed. Call before `run()`.
    pub fn add_feed(&mut self, mut feed: impl BarFeed + 'static) {
        // Prime the heap with the first bar from this feed.
        if let Some(bar) = feed.next() {
            self.heap.push(HeapEntry {
                ts: Reverse(bar.timestamp),
                symbol: bar.symbol.clone(),
                bar,
            });
        }
        self.feeds.insert(feed.symbol().to_string(), Box::new(feed));
    }

    /// Run the backtest. Returns one `BacktestReport` per symbol.
    pub fn run(&mut self, risk_free_annual: f64) -> Vec<BacktestReport> {
        let mut last_bars: HashMap<String, Bar> = HashMap::new();

        while let Some(entry) = self.heap.pop() {
            let bar = entry.bar;
            let symbol = bar.symbol.clone();

            // Advance the feed that produced this bar and push its next bar.
            if let Some(feed) = self.feeds.get_mut(&symbol) {
                if let Some(next) = feed.next() {
                    self.heap.push(HeapEntry {
                        ts: Reverse(next.timestamp),
                        symbol: next.symbol.clone(),
                        bar: next,
                    });
                }
            }

            self.bus.send(Event::Market(MarketEvent { bar: bar.clone() }));
            while let Some(event) = self.bus.try_recv() {
                self.dispatch(event);
            }

            last_bars.insert(symbol, bar);
        }

        // Force-close all open positions at last known price.
        for (sym, bar) in &last_bars {
            if let Some(pos) = self.portfolio.positions.get(sym) {
                if pos.qty.abs() > f64::EPSILON {
                    let side = if pos.qty > 0.0 { Side::Sell } else { Side::Buy };
                    let qty = pos.qty.abs();
                    let fill = self.broker.force_close(sym, qty, side, bar.timestamp, bar.close);
                    self.portfolio.apply_fill(&fill);
                }
            }
        }

        // Generate one report per symbol.
        last_bars
            .keys()
            .map(|sym| {
                BacktestReport::generate(self.strategy.name(), sym, &self.portfolio, risk_free_annual)
            })
            .collect()
    }

    fn dispatch(&mut self, event: Event) {
        match event {
            Event::Market(ref market) => {
                let bar = &market.bar;
                self.last_prices.insert(bar.symbol.clone(), bar.close);

                // Apply candle transform for strategy. Broker/exit rules use raw bar.
                let transformed = self.candle_transform.as_mut().and_then(|t| t.apply(bar));
                let strategy_bar: &Bar = transformed.as_ref().unwrap_or(bar);

                // Fill pending orders at this bar's open.
                let fills = self.broker.process_pending(bar);
                for fill in fills {
                    self.bus.send(Event::Fill(FillEvent { fill }));
                }

                // Record equity.
                self.portfolio.record_equity(bar.timestamp, &self.last_prices);
                let eq = self.portfolio.equity(&self.last_prices);
                self.bus.send(Event::Equity(EquityEvent {
                    timestamp: bar.timestamp,
                    equity: eq,
                    cash: self.portfolio.cash,
                }));

                // Exit rules — always run to track MAE/MFE/bars_held even without active rules.
                {
                    let to_close: Vec<(String, f64, alm_core::ExitReason)> = self
                        .position_trackers
                        .iter_mut()
                        .filter_map(|(sym, tracker)| {
                            tracker
                                .update_and_check(bar.open, bar.high, bar.low, bar.close, &self.exit_rules)
                                .map(|(fp, reason)| (sym.clone(), fp, reason))
                        })
                        .collect();

                    for (sym, fill_price, reason) in to_close {
                        let tracker = self.position_trackers.remove(&sym);
                        if let Some(pos) = self.portfolio.positions.get(&sym) {
                            let qty  = pos.qty.abs();
                            let side = if pos.qty > 0.0 { Side::Sell } else { Side::Buy };
                            let fill = self.broker.force_close(&sym, qty, side, bar.timestamp, fill_price);
                            self.portfolio.apply_fill(&fill);
                            if let Some(tr) = tracker {
                                if let Some(trade) = self.portfolio.trades.last_mut() {
                                    trade.mae_pct     = tr.mae;
                                    trade.mfe_pct     = tr.mfe;
                                    trade.bars_held   = tr.bars_held;
                                    trade.exit_reason = reason;
                                }
                            }
                        }
                    }
                }

                // Portfolio snapshot → strategy.
                let snapshot = self.portfolio.snapshot(&self.last_prices);
                self.strategy.set_portfolio_snapshot(&snapshot);

                // Update raw sliding window (for bar_windows).
                let window = self.bar_windows.entry(bar.symbol.clone()).or_default();
                if window.len() >= self.window_size { window.pop_front(); }
                window.push_back(bar.clone());

                // Update transformed sliding window (for on_window strategy calls).
                let strat_window = self.strategy_bar_windows.entry(bar.symbol.clone()).or_default();
                if strat_window.len() >= self.window_size { strat_window.pop_front(); }
                strat_window.push_back(strategy_bar.clone());

                // Strategy: on_bar (transformed).
                let strat_window_slice: Vec<Bar> = strat_window.iter().cloned().collect();
                let mut signals = self.strategy.on_bar(strategy_bar, &self.last_prices);

                // Strategy: on_window (transformed).
                let window_signals = self.strategy.on_window(strategy_bar, &strat_window_slice, &self.last_prices);
                signals.extend(window_signals);

                for signal in signals {
                    self.bus.send(Event::Signal(alm_core::event::SignalEvent { signal }));
                }
            }

            Event::Signal(ref sig_event) => {
                let signal = &sig_event.signal;
                match signal.direction {
                    Direction::Close => {
                        if let Some(pos) = self.portfolio.positions.get(&signal.symbol) {
                            let qty = pos.qty.abs();
                            if qty > f64::EPSILON {
                                let side = if pos.qty > 0.0 { Side::Sell } else { Side::Buy };
                                let order = OrderRequest::market(
                                    signal.timestamp, &signal.symbol, side, qty,
                                );
                                self.bus.send(Event::Order(alm_core::event::OrderEvent { order }));
                            }
                        }
                    }
                    Direction::Long | Direction::Short => {
                        if self.single_entry {
                            let blocked = self.portfolio.positions.get(&signal.symbol)
                                .map_or(false, |p| match signal.direction {
                                    Direction::Long  => p.qty > 0.0,
                                    Direction::Short => p.qty < 0.0,
                                    Direction::Close => false,
                                });
                            if blocked { return; }
                        }
                        if self.risk.validate(signal, &self.portfolio) {
                            let price =
                                self.last_prices.get(&signal.symbol).copied().unwrap_or(0.0);
                            let qty = self.risk.size(signal, &self.portfolio, price);
                            if qty > f64::EPSILON {
                                // Store signal-level TP/SL for PositionTracker creation at fill.
                                if signal.target_price.is_some() || signal.stop_price.is_some() {
                                    self.pending_signal_levels.insert(
                                        signal.symbol.clone(),
                                        (signal.target_price, signal.stop_price),
                                    );
                                }
                                let side = match signal.direction {
                                    Direction::Long => Side::Buy,
                                    Direction::Short => Side::Sell,
                                    Direction::Close => unreachable!(),
                                };
                                let order = OrderRequest::market(
                                    signal.timestamp, &signal.symbol, side, qty,
                                );
                                self.bus.send(Event::Order(alm_core::event::OrderEvent { order }));
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

                let still_open = self
                    .portfolio
                    .positions
                    .get(&fill.symbol)
                    .map_or(false, |p| p.qty.abs() > f64::EPSILON);

                if still_open {
                    let sig_levels = self.pending_signal_levels.remove(&fill.symbol);
                    self.position_trackers.entry(fill.symbol.clone()).or_insert_with(|| {
                        let is_long = self.portfolio.positions.get(&fill.symbol)
                            .map_or(true, |p| p.qty > 0.0);
                        let (sig_tp, sig_sl) = sig_levels.unwrap_or((None, None));
                        PositionTracker::with_levels(fill.price, sig_sl, sig_tp, is_long)
                    });
                } else {
                    self.pending_signal_levels.remove(&fill.symbol);
                    if let Some(tr) = self.position_trackers.remove(&fill.symbol) {
                        if let Some(trade) = self.portfolio.trades.last_mut() {
                            trade.mae_pct   = tr.mae;
                            trade.mfe_pct   = tr.mfe;
                            trade.bars_held = tr.bars_held;
                        }
                    }
                }
            }

            Event::Equity(_) => {}
        }
    }
}

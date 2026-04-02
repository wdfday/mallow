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
//! engine.add_feed(InMemoryFeed::new(btc_bars, "BTCUSDT"));
//! engine.add_feed(InMemoryFeed::new(eth_bars, "ETHUSDT"));
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

    /// Registered feeds (one per symbol).
    feeds: Vec<Box<dyn BarFeed>>,

    /// Merge heap — always holds the next bar from each feed.
    heap: BinaryHeap<HeapEntry>,

    /// Last known price per symbol.
    last_prices: HashMap<String, f64>,

    /// Per-symbol sliding window for on_window() calls.
    bar_windows: HashMap<String, std::collections::VecDeque<Bar>>,
    window_size: usize,

    exit_rules: ExitRules,
    position_trackers: HashMap<String, PositionTracker>,
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
            feeds: Vec::new(),
            heap: BinaryHeap::new(),
            last_prices: HashMap::new(),
            bar_windows: HashMap::new(),
            window_size: DEFAULT_WINDOW_SIZE,
            exit_rules: ExitRules::default(),
            position_trackers: HashMap::new(),
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
        self.feeds.push(Box::new(feed));
    }

    /// Run the backtest. Returns one `BacktestReport` per symbol.
    pub fn run(&mut self, risk_free_annual: f64) -> Vec<BacktestReport> {
        let mut last_bars: HashMap<String, Bar> = HashMap::new();

        while let Some(entry) = self.heap.pop() {
            let bar = entry.bar;
            let symbol = bar.symbol.clone();

            // Advance the feed that produced this bar and push its next bar.
            for feed in &mut self.feeds {
                if feed.symbol() == symbol {
                    if let Some(next) = feed.next() {
                        self.heap.push(HeapEntry {
                            ts: Reverse(next.timestamp),
                            symbol: next.symbol.clone(),
                            bar: next,
                        });
                    }
                    break;
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

                // Exit rules.
                if self.exit_rules.is_active() {
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
                        self.position_trackers.remove(&sym);
                        let sig = Signal::close(bar.timestamp, &sym);
                        self.bus.send(Event::Signal(alm_core::event::SignalEvent { signal: sig }));
                    }
                }

                // Portfolio snapshot → strategy.
                let snapshot = self.portfolio.snapshot(&self.last_prices);
                self.strategy.set_portfolio_snapshot(&snapshot);

                // Update sliding window.
                let window = self.bar_windows.entry(bar.symbol.clone()).or_default();
                if window.len() >= self.window_size {
                    window.pop_front();
                }
                window.push_back(bar.clone());

                // Strategy: on_bar.
                let mut signals = self.strategy.on_bar(bar, &self.last_prices);

                // Strategy: on_window.
                let window_slice: Vec<Bar> = window.iter().cloned().collect();
                let window_signals = self.strategy.on_window(bar, &window_slice, &self.last_prices);
                signals.extend(window_signals);

                for signal in signals {
                    self.bus.send(Event::Signal(alm_core::event::SignalEvent { signal }));
                }
            }

            Event::Signal(ref sig_event) => {
                let signal = &sig_event.signal;
                if self.risk.validate(signal, &self.portfolio) {
                    let price = self.last_prices.get(&signal.symbol).copied().unwrap_or(0.0);
                    let qty = self.risk.size(signal, &self.portfolio, price);
                    if qty > f64::EPSILON {
                        let side = match signal.direction {
                            Direction::Long => Side::Buy,
                            Direction::Short => Side::Sell,
                            Direction::Close => {
                                if let Some(pos) = self.portfolio.positions.get(&signal.symbol) {
                                    if pos.qty > 0.0 { Side::Sell } else { Side::Buy }
                                } else {
                                    return;
                                }
                            }
                        };
                        let order = OrderRequest::market(signal.timestamp, &signal.symbol, side, qty);
                        self.bus.send(Event::Order(alm_core::event::OrderEvent { order }));
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

                if self.exit_rules.is_active() {
                    if still_open {
                        self.position_trackers
                            .entry(fill.symbol.clone())
                            .or_insert_with(|| PositionTracker::new(fill.price));
                    } else {
                        self.position_trackers.remove(&fill.symbol);
                    }
                }
            }

            Event::Equity(_) => {}
        }
    }
}

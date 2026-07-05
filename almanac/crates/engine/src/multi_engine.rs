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
    exit::{IntraBarMode, PositionTracker},
    order::{OrderRequest, Side},
    portfolio::Portfolio,
    signal::{Direction, Signal},
    strategy::RiskManager,
};
use alm_data::BarFeed;
use alm_report::BacktestReport;

use alm_core::portfolio::PortfolioSnapshot;
use crate::{broker::SimBroker, bus::SyncBus};

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

    intra_bar_mode: IntraBarMode,
    position_trackers: HashMap<String, PositionTracker>,
    /// Pending TP/SL absolute price levels from entry Signal — consumed at Fill time.
    pending_signal_levels: HashMap<String, (Option<f64>, Option<f64>, Option<f64>, Option<usize>)>,
    /// Block re-entry in the same direction while a position is open.
    single_entry: bool,
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
            intra_bar_mode: IntraBarMode::default(),
            position_trackers: HashMap::new(),
            pending_signal_levels: HashMap::new(),
            single_entry: false,
        }
    }

    pub fn with_intra_bar_mode(mut self, mode: IntraBarMode) -> Self {
        self.intra_bar_mode = mode;
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

        // Force-close all open positions at last known price, propagating
        // tracker state (MAE/MFE/bars_held) and tagging exit_reason=EndOfData.
        let open: Vec<(String, f64)> = self.portfolio.positions.values()
            .filter(|p| p.qty.abs() > f64::EPSILON)
            .map(|p| (p.symbol.clone(), p.qty))
            .collect();
        for (sym, qty) in open {
            let Some(bar) = last_bars.get(&sym) else { continue; };
            let side = if qty > 0.0 { Side::Sell } else { Side::Buy };
            let tracker = self.position_trackers.remove(&sym);
            let fill = self.broker.force_close(&sym, qty.abs(), side, bar.timestamp, bar.close);
            self.portfolio.apply_fill(&fill);
            if let Some(tr) = tracker {
                if let Some(trade) = self.portfolio.trades.last_mut() {
                    trade.mae_pct     = tr.mae;
                    trade.mfe_pct     = tr.mfe;
                    trade.bars_held   = tr.bars_held;
                    trade.exit_reason = alm_core::ExitReason::EndOfData;
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
                let fill_count = fills.len();
                for fill in fills {
                    self.bus.send(Event::Fill(FillEvent { fill }));
                }

                // Drain Fills only (same invariant as Engine — bus holds only
                // the Fills just enqueued at this point).
                let mut guard_iters = fill_count + 2;
                while let Some(early_ev) = self.bus.try_recv() {
                    if guard_iters == 0 {
                        tracing::error!("multi-engine early-drain guard tripped");
                        self.bus.send(early_ev);
                        break;
                    }
                    guard_iters -= 1;
                    match early_ev {
                        Event::Fill(_) | Event::Equity(_) => self.dispatch(early_ev),
                        other => { self.bus.send(other); break; }
                    }
                }

                // Exit rules BEFORE recording equity so SL/TP fills land in this
                // bar's equity point. Always runs to track MAE/MFE/bars_held.
                {
                    let to_close: Vec<(String, f64, alm_core::ExitReason)> = self
                        .position_trackers
                        .iter_mut()
                        .filter_map(|(sym, tracker)| {
                            tracker
                                .update_and_check(bar.open, bar.high, bar.low, bar.close, self.intra_bar_mode)
                                .map(|(fp, reason)| (sym.clone(), fp, reason))
                        })
                        .collect();

                    for (sym, fill_price, reason) in to_close {
                        let tracker = self.position_trackers.remove(&sym);
                        // Cancel any still-pending order for the symbol so it
                        // doesn't fire on the next bar and unintentionally open
                        // a position in the opposite direction.
                        self.broker.cancel_for_symbol(&sym);
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

                // Record equity AFTER exit rules — see Engine for rationale.
                self.portfolio.record_equity(bar.timestamp, &self.last_prices);
                let eq = self.portfolio.equity(&self.last_prices);
                self.bus.send(Event::Equity(EquityEvent {
                    timestamp: bar.timestamp,
                    equity: eq,
                    cash: self.portfolio.cash,
                }));

                // Portfolio snapshot → strategy.
                let snapshot = self.portfolio.snapshot(&self.last_prices);
                self.strategy.set_portfolio_snapshot(&snapshot);

                // Update sliding window.
                let window = self.bar_windows.entry(bar.symbol.clone()).or_default();
                if window.len() >= self.window_size { window.pop_front(); }
                window.push_back(bar.clone());
                let window_slice: Vec<Bar> = window.iter().cloned().collect();

                let mut signals = self.strategy.on_bar(bar, &self.last_prices);

                let window_signals = self.strategy.on_window(bar, &window_slice, &self.last_prices);
                signals.extend(window_signals);

                for signal in signals {
                    self.bus.send(Event::Signal(alm_core::event::SignalEvent { signal }));
                }
            }

            Event::Signal(ref sig_event) => {
                let signal = &sig_event.signal;
                match signal.direction {
                    Direction::Exit => {
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
                                    Direction::Exit => false,
                                });
                            if blocked { return; }
                        }
                        if self.risk.validate(signal, &self.portfolio) {
                            let price =
                                self.last_prices.get(&signal.symbol).copied().unwrap_or(0.0);
                            let qty = self.risk.size(signal, &self.portfolio, price);
                            if qty > f64::EPSILON {
                                // Store signal-level exit levels for PositionTracker creation at fill.
                                if signal.target_price.is_some() || signal.stop_price.is_some()
                                    || signal.trailing_stop_pct.is_some() || signal.max_bars_held.is_some() {
                                    self.pending_signal_levels.insert(
                                        signal.symbol.clone(),
                                        (signal.target_price, signal.stop_price, signal.trailing_stop_pct, signal.max_bars_held),
                                    );
                                }
                                let side = match signal.direction {
                                    Direction::Long => Side::Buy,
                                    Direction::Short => Side::Sell,
                                    Direction::Exit => unreachable!(),
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
                        let (sig_tp, sig_sl, sig_trail, sig_max_bars) = sig_levels.unwrap_or((None, None, None, None));
                        PositionTracker::with_levels(fill.price, sig_sl, sig_tp, sig_trail, sig_max_bars, is_long)
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

            Event::Equity(_) => {
                // No-op in backtest: the equity curve is built directly by
                // `portfolio.record_equity()`. The event is emitted as an
                // extension hook for a live bus whose subscribers (dashboards,
                // realtime feeds) tap equity snapshots — SyncBus has none.
            }
        }
    }
}

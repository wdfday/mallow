use std::collections::{HashMap, VecDeque};

use crate::broker::SimBroker;
use alm_core::{
    bar::Bar,
    bus::EventBus,
    event::{EquityEvent, Event, FillEvent, MarketEvent},
    exit::{ExitReason, ExitRules, PositionTracker},
    order::{Fill, OrderRequest, Side},
    portfolio::Portfolio,
    regime::{RegimeState, RegimeSummary, RegimeTradeStats},
    signal::{Direction, Signal},
    strategy::{RiskManager, Strategy},
};
use alm_data::BarFeed;
use alm_strategy::candle_type::CandleTransform;
use alm_report::BacktestReport;
use tracing::{debug, info, trace};

use crate::bus_sync::SyncBus;

/// Default sliding-window size for `on_window()` calls.
const DEFAULT_WINDOW_SIZE: usize = 200;

/// Event-driven backtesting engine, generic over event bus implementation.
///
/// Components communicate exclusively through events:
///   MarketEvent → Strategy → SignalEvent → RiskManager → OrderEvent → Broker → FillEvent
///
/// Use `Engine::sync()` for backtesting (SyncBus, VecDeque, zero atomic overhead).
pub struct Engine<S: Strategy, R: RiskManager, B: EventBus> {
    pub portfolio: Portfolio,
    pub broker: SimBroker,
    pub strategy: S,
    pub risk: R,
    bus: B,
    /// Last known close price for the tracked symbol.
    last_price: f64,
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
    /// Pending TP/SL absolute price levels from the entry Signal — consumed at Fill time.
    /// Key = symbol; value = (target_price, stop_price). Signal-level levels take
    /// priority over ATR-rule-derived levels when creating a PositionTracker.
    pending_signal_levels: HashMap<String, (Option<f64>, Option<f64>)>,
    /// Optional candle transform (Heikin-Ashi etc.) applied before strategy sees the bar.
    /// Broker and exit-rule checks always use the raw bar; only strategy.on_bar /
    /// on_window receive the transformed bar.
    candle_transform: Option<CandleTransform>,
    /// Sliding window of transformed bars for on_window() — mirrors bar_window but
    /// contains post-transform bars so strategy sees consistent candle type.
    strategy_bar_window: VecDeque<Bar>,
    /// Full regime snapshot active when each currently-open position was entered.
    /// Cleared when the position closes (after the snapshot is copied into the Trade).
    regime_at_entry:     HashMap<String, RegimeState>,
    /// Most recent regime state observed from `strategy.current_regime()`.
    /// Used to detect transitions (only on-change is pushed to `regime_changes`)
    /// and to tag newly-opened positions.
    last_regime:         Option<RegimeState>,
    /// Timestamped regime transitions over the run: `(timestamp_ms, label)`.
    regime_changes:      Vec<(i64, String)>,
    /// Set at run() start; used by dispatch() for metric labels.
    metrics_symbol:   String,
    /// Set at run() start; used by dispatch() for metric labels.
    metrics_strategy: String,
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
            last_price: 0.0,
            bar_window: VecDeque::new(),
            window_size: DEFAULT_WINDOW_SIZE,
            exit_rules: ExitRules::default(),
            position_trackers: HashMap::new(),
            next_bar: false,
            pending_signals: Vec::new(),
            single_entry: false,
            pending_signal_levels: HashMap::new(),
            candle_transform: None,
            strategy_bar_window: VecDeque::new(),
            regime_at_entry: HashMap::new(),
            last_regime: None,
            regime_changes: Vec::new(),
            metrics_symbol:   String::new(),
            metrics_strategy: String::new(),
        }
    }
}

// ── Shared engine logic ───────────────────────────────────────────────────────

impl<S: Strategy, R: RiskManager, B: EventBus> Engine<S, R, B> {
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

    /// Set candle transform applied before strategy sees each bar.
    /// Broker fills and exit-rule checks always use the raw bar.
    pub fn with_candle_transform(mut self, transform: CandleTransform) -> Self {
        self.candle_transform = Some(transform);
        self
    }

    /// Set exit rules applied on top of strategy logic.
    pub fn with_exit_rules(mut self, rules: ExitRules) -> Self {
        self.exit_rules = rules;
        self
    }

    /// Run a full backtest over the provided feed. Returns a `BacktestReport`.
    pub fn run(&mut self, feed: &mut impl BarFeed, risk_free_annual: f64) -> BacktestReport {
        let symbol = feed.symbol().to_string();
        let strategy_name = self.strategy.name().to_string();
        let initial_capital = self.portfolio.cash;
        info!(symbol = %symbol, strategy = %strategy_name, capital = initial_capital, "backtest start");

        self.metrics_symbol   = symbol.clone();
        self.metrics_strategy = strategy_name.clone();

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

        // Force-close ALL remaining open positions at the last bar's close.
        // Multi-symbol strategies may hold positions on symbols other than the
        // feed's primary — leaving them open would understate trade count and
        // leave the equity curve mid-trade.
        if let Some(bar) = self.bar_window.back().cloned() {
            let open_positions: Vec<_> = self
                .portfolio
                .positions
                .values()
                .filter(|p| p.qty.abs() > f64::EPSILON)
                .map(|p| (p.symbol.clone(), p.qty))
                .collect();

            for (sym, qty) in open_positions {
                // Long position → sell to close; short position → buy to cover.
                let side = if qty > 0.0 { Side::Sell } else { Side::Buy };
                let tracker = self.position_trackers.remove(&sym);
                let regime_state = self.regime_at_entry.remove(&sym);
                let fill =
                    self.broker.force_close(&sym, qty.abs(), side, bar.timestamp, bar.close);
                self.portfolio.apply_fill(&fill);
                if let Some(tr) = tracker {
                    if let Some(trade) = self.portfolio.trades.last_mut() {
                        trade.mae_pct = tr.mae;
                        trade.mfe_pct = tr.mfe;
                        trade.bars_held = tr.bars_held;
                        trade.exit_reason = ExitReason::EndOfData;
                        if let Some(state) = regime_state {
                            trade.regime_at_entry = Some(state);
                        }
                    }
                }
            }
        }

        let mut report = BacktestReport::generate(
            self.strategy.name(),
            &symbol,
            &self.portfolio,
            risk_free_annual,
        );
        report.regime_summary = self.build_regime_summary();
        // Record aggregate run stats.
        {
            let strat = strategy_name.clone();
            let total = report.total_trades as u64;
            let wins  = (report.win_rate_pct / 100.0 * report.total_trades as f64).round() as u64;
            let losses = total.saturating_sub(wins);
            metrics::counter!("alm_engine_trades_total",
                "strategy" => strat.clone(), "result" => "all"
            ).increment(total);
            metrics::counter!("alm_engine_trades_total",
                "strategy" => strat.clone(), "result" => "win"
            ).increment(wins);
            metrics::counter!("alm_engine_trades_total",
                "strategy" => strat.clone(), "result" => "loss"
            ).increment(losses);
            metrics::histogram!("alm_engine_run_bars",
                "strategy" => strat
            ).record(bar_count as f64);
        }
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

    /// Execute a buffered signal directly at bar.open (used when `next_bar = true`).
    fn execute_signal_at_open(&mut self, signal: &Signal, bar: &Bar) {
        match signal.direction {
            Direction::Exit => {
                if let Some(pos) = self.portfolio.positions.get(&signal.symbol) {
                    let qty = pos.qty.abs();
                    if qty > f64::EPSILON {
                        let side = if pos.is_long() { Side::Sell } else { Side::Buy };
                        let fill =
                            self.broker.force_close(&signal.symbol, qty, side, bar.timestamp, bar.open);
                        self.portfolio.apply_fill(&fill);
                        let regime_state = self.regime_at_entry.remove(&signal.symbol);
                        if let Some(tr) = self.position_trackers.remove(&signal.symbol) {
                            if let Some(trade) = self.portfolio.trades.last_mut() {
                                trade.mae_pct = tr.mae;
                                trade.mfe_pct = tr.mfe;
                                trade.bars_held = tr.bars_held;
                                if let Some(state) = regime_state {
                                    trade.regime_at_entry = Some(state);
                                }
                            }
                        }
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
                        let was_new = !self.position_trackers.contains_key(&signal.symbol);
                        self.position_trackers.entry(signal.symbol.clone()).or_insert_with(|| {
                            PositionTracker::with_levels(fill_price, signal.stop_price, signal.target_price, true)
                        });
                        if was_new {
                            if let Some(state) = &self.last_regime {
                                self.regime_at_entry.insert(signal.symbol.clone(), state.clone());
                            }
                        }
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
                        let was_new = !self.position_trackers.contains_key(&signal.symbol);
                        self.position_trackers.entry(signal.symbol.clone()).or_insert_with(|| {
                            PositionTracker::with_levels(fill_price, signal.stop_price, signal.target_price, false)
                        });
                        if was_new {
                            if let Some(state) = &self.last_regime {
                                self.regime_at_entry.insert(signal.symbol.clone(), state.clone());
                            }
                        }
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
                trace!(
                    symbol = %bar.symbol, ts = bar.timestamp,
                    open = bar.open, high = bar.high, low = bar.low, close = bar.close, vol = bar.volume,
                    "bar"
                );

                // Apply candle transform for strategy (HA etc.). Broker/exit rules always
                // use the raw bar so fills happen at real market prices.
                let transformed: Option<Bar> = self.candle_transform.as_mut().and_then(|t| t.apply(bar));
                let strategy_bar: &Bar = transformed.as_ref().unwrap_or(bar);

                // Mirror bar_window for on_window() — stores transformed bars so strategy
                // sees a consistent candle type across both on_bar and on_window.
                self.strategy_bar_window.push_back(strategy_bar.clone());
                if self.strategy_bar_window.len() > self.window_size {
                    self.strategy_bar_window.pop_front();
                }

                self.last_price = bar.close;

                // Notify risk manager with current bar (e.g. for ATR-based sizing).
                self.risk.on_bar(bar);

                // Process pending orders at this bar's open (avoids look-ahead bias).
                let fills = self.broker.process_pending(bar);
                let fill_count = fills.len();
                for fill in fills {
                    self.bus.send(Event::Fill(FillEvent { fill }));
                }

                // Immediately drain Fill (and Equity) events from the bus before
                // running exit rules. This prevents a double-close race condition:
                // when a strategy emits `exit = true` on bar N (creating a pending
                // Sell in the broker), and the PositionTracker SL/TP also fires on
                // bar N+1, both would try to close the same long position. By
                // processing the pending-order Fill first, the position is already
                // flat when exit rules run, so exit rules skip it.
                //
                // Invariant: at this point the bus only holds the `fill_count` Fill
                // events we just enqueued. Fill dispatch does not re-emit, so the
                // drain terminates after exactly `fill_count` iterations. We bound
                // the loop defensively in case a future regression introduces
                // recursive event emission from Fill dispatch.
                let mut guard_iters = fill_count + 2;
                while let Some(early_ev) = self.bus.try_recv() {
                    if guard_iters == 0 {
                        tracing::error!("early-drain guard tripped — bailing out");
                        self.bus.send(early_ev);
                        break;
                    }
                    guard_iters -= 1;
                    match early_ev {
                        Event::Fill(_) | Event::Equity(_) => self.dispatch(early_ev),
                        other => {
                            tracing::warn!(
                                "unexpected non-Fill/Equity event during early drain"
                            );
                            self.bus.send(other);
                            break;
                        }
                    }
                }

                // ── Exit rules ────────────────────────────────────────────────
                // Run BEFORE recording equity so that exit-fired close fills are
                // reflected in this bar's equity point (otherwise the curve lags
                // the exit by one bar). Always call update_and_check: even with
                // no exit rules it tracks bars_held, MAE, and MFE for every open
                // position.
                {
                    let to_close: Vec<(String, f64, ExitReason)> = self
                        .position_trackers
                        .iter_mut()
                        .filter_map(|(sym, tracker)| {
                            tracker
                                .update_and_check(bar.open, bar.high, bar.low, bar.close, &self.exit_rules)
                                .map(|(fill_price, reason)| (sym.clone(), fill_price, reason))
                        })
                        .collect();

                    for (sym, fill_price, reason) in to_close {
                        let tracker = self.position_trackers.remove(&sym);
                        let regime_state = self.regime_at_entry.remove(&sym);
                        // Cancel any pending close order for this symbol so it
                        // does not fire again in the same bar's inner event loop
                        // and accidentally open an unintended short position.
                        self.broker.cancel_for_symbol(&sym);
                        if let Some(pos) = self.portfolio.positions.get(&sym) {
                            let qty  = pos.qty.abs();
                            let side = if pos.is_long() { Side::Sell } else { Side::Buy };
                            let fill = self.broker.force_close(&sym, qty, side, bar.timestamp, fill_price);
                            self.portfolio.apply_fill(&fill);
                            self.last_price = fill_price;
                            if let Some(tr) = tracker {
                                if let Some(trade) = self.portfolio.trades.last_mut() {
                                    trade.mae_pct = tr.mae;
                                    trade.mfe_pct = tr.mfe;
                                    trade.bars_held = tr.bars_held;
                                    trade.exit_reason = reason.clone();
                                    if let Some(state) = regime_state.clone() {
                                        trade.regime_at_entry = Some(state);
                                    }
                                }
                            }
                        }
                        debug!(symbol = %sym, fill_price, ?reason, "exit rule triggered → force_close");
                    }
                }

                // Snapshot equity at bar close — AFTER exit rules so that any
                // SL/TP/trailing fills applied this bar are reflected.
                let prices = std::iter::once((bar.symbol.clone(), self.last_price)).collect::<HashMap<_, _>>();
                self.portfolio.record_equity(bar.timestamp, &prices);
                let eq = self.portfolio.equity(&prices);
                self.bus.send(Event::Equity(EquityEvent {
                    timestamp: bar.timestamp,
                    equity: eq,
                    cash: self.portfolio.cash,
                }));

                // ── Portfolio snapshot → strategy (opt-in) ───────────────────
                if self.strategy.uses_portfolio_snapshot() {
                    let prices = std::iter::once((bar.symbol.clone(), self.last_price)).collect::<HashMap<_, _>>();
                    let snapshot = self.portfolio.snapshot(&prices);
                    self.strategy.set_portfolio_snapshot(&snapshot);
                }

                // ── Indicator-based signals (bar-by-bar) ──────────────────────
                let eval_start = std::time::Instant::now();
                let signals = self.strategy.on_bar(strategy_bar);
                metrics::histogram!("alm_engine_strategy_eval_us",
                    "strategy" => self.metrics_strategy.clone()
                ).record(eval_start.elapsed().as_micros() as f64);

                // Snapshot the regime state produced by the strategy on this bar
                // (e.g. from a `regime { ... }` block). Record transitions on label
                // change; strategy the full state so position opens can tag the trade
                // with all three dimensions (status + value) at entry time.
                if let Some(state) = self.strategy.current_regime() {
                    let prev_label = self.last_regime.as_ref().map(|s| s.label());
                    let new_label  = state.label();
                    if prev_label.as_deref() != Some(new_label.as_str()) {
                        self.regime_changes.push((bar.timestamp, new_label));
                    }
                    self.last_regime = Some(state.clone());
                }

                for signal in signals {
                    metrics::counter!("alm_engine_signals_total",
                        "strategy"  => self.metrics_strategy.clone(),
                        "direction" => format!("{:?}", signal.direction),
                    ).increment(1);
                    self.bus
                        .send(Event::Signal(alm_core::event::SignalEvent { signal }));
                }

                // ── Pattern-based signals (sliding window, opt-in) ────────────
                if self.strategy.uses_window() {
                    let (a, b) = self.strategy_bar_window.as_slices();
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
            }

            Event::Signal(ref sig_event) => {
                // In next-bar mode: buffer signals to be executed at the next bar's open.
                if self.next_bar {
                    self.pending_signals.push(sig_event.signal.clone());
                    return;
                }
                let signal = &sig_event.signal;
                match signal.direction {
                    Direction::Exit => {
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
                                metrics::counter!("alm_engine_orders_total",
                                    "strategy" => self.metrics_strategy.clone(),
                                    "side"     => "sell",
                                ).increment(1);
                                self.bus
                                    .send(Event::Order(alm_core::event::OrderEvent { order }));
                                debug!(symbol = %signal.symbol, qty, ?side, strength = signal.strength, "close signal → order");
                            }
                        }
                    }
                    Direction::Long => {
                        if self.single_entry {
                            if self.portfolio.positions.get(&signal.symbol)
                                .map_or(false, |p| p.is_long()) { return; }
                        }
                        if self.risk.validate(signal, &self.portfolio) {
                            let qty = self.risk.size(signal, &self.portfolio, self.last_price);
                            if qty > f64::EPSILON {
                                // Store signal-level TP/SL for PositionTracker creation at fill.
                                if signal.target_price.is_some() || signal.stop_price.is_some() {
                                    self.pending_signal_levels.insert(
                                        signal.symbol.clone(),
                                        (signal.target_price, signal.stop_price),
                                    );
                                }
                                let order = OrderRequest::market(
                                    signal.timestamp,
                                    &signal.symbol,
                                    Side::Buy,
                                    qty,
                                );
                                metrics::counter!("alm_engine_orders_total",
                                    "strategy" => self.metrics_strategy.clone(),
                                    "side"     => "buy",
                                ).increment(1);
                                self.bus
                                    .send(Event::Order(alm_core::event::OrderEvent { order }));
                                debug!(symbol = %signal.symbol, qty, strength = signal.strength, "long signal → buy order");
                            }
                        }
                    }
                    Direction::Short => {
                        if self.single_entry {
                            if self.portfolio.positions.get(&signal.symbol)
                                .map_or(false, |p| p.is_short()) { return; }
                        }
                        if self.risk.validate(signal, &self.portfolio) {
                            let qty = self.risk.size(signal, &self.portfolio, self.last_price);
                            if qty > f64::EPSILON {
                                // Store signal-level TP/SL for PositionTracker creation at fill.
                                if signal.target_price.is_some() || signal.stop_price.is_some() {
                                    self.pending_signal_levels.insert(
                                        signal.symbol.clone(),
                                        (signal.target_price, signal.stop_price),
                                    );
                                }
                                let order = OrderRequest::market(
                                    signal.timestamp,
                                    &signal.symbol,
                                    Side::Sell,
                                    qty,
                                );
                                metrics::counter!("alm_engine_orders_total",
                                    "strategy" => self.metrics_strategy.clone(),
                                    "side"     => "sell",
                                ).increment(1);
                                self.bus
                                    .send(Event::Order(alm_core::event::OrderEvent { order }));
                                debug!(symbol = %signal.symbol, qty, strength = signal.strength, "short signal → sell order");
                            }
                        }
                    }
                }
            }

            Event::Order(ref order_event) => {
                self.broker.submit(order_event.order.clone());
            }

            Event::Fill(ref fill_event) => {
                metrics::counter!("alm_engine_fills_total",
                    "strategy" => self.metrics_strategy.clone()
                ).increment(1);
                let fill = &fill_event.fill;
                debug!(
                    symbol = %fill.symbol, side = ?fill.side,
                    qty = fill.qty, price = fill.price, commission = fill.commission,
                    "fill"
                );
                self.portfolio.apply_fill(fill);
                debug!(cash = format!("{:.2}", self.portfolio.cash), "portfolio after fill");

                // Maintain per-position tracker for exit rules.
                // After the fill, check whether the position is still open.
                let still_open = self
                    .portfolio
                    .positions
                    .get(&fill.symbol)
                    .map(|p| p.qty.abs() > f64::EPSILON)
                    .unwrap_or(false);

                if still_open {
                    let sig_levels = self.pending_signal_levels.remove(&fill.symbol);
                    let was_new = !self.position_trackers.contains_key(&fill.symbol);
                    self.position_trackers.entry(fill.symbol.clone()).or_insert_with(|| {
                        let is_long = self.portfolio.positions.get(&fill.symbol)
                            .map_or(true, |p| p.qty > 0.0);
                        let (sig_tp, sig_sl) = sig_levels.unwrap_or((None, None));
                        PositionTracker::with_levels(fill.price, sig_sl, sig_tp, is_long)
                    });
                    // Record the regime active when this position was opened, so
                    // we can tag the Trade on close.
                    if was_new {
                        if let Some(state) = &self.last_regime {
                            self.regime_at_entry.insert(fill.symbol.clone(), state.clone());
                        }
                    }
                } else {
                    // Position closed — propagate MAE/MFE/bars_held, then remove tracker.
                    self.pending_signal_levels.remove(&fill.symbol);
                    let regime_state = self.regime_at_entry.remove(&fill.symbol);
                    if let Some(tr) = self.position_trackers.remove(&fill.symbol) {
                        if let Some(trade) = self.portfolio.trades.last_mut() {
                            trade.mae_pct = tr.mae;
                            trade.mfe_pct = tr.mfe;
                            trade.bars_held = tr.bars_held;
                            if let Some(state) = regime_state {
                                trade.regime_at_entry = Some(state);
                            }
                        }
                    }
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
        self.pending_signal_levels.clear();
        self.regime_at_entry.clear();
        self.last_regime = None;
        self.regime_changes.clear();
        self.strategy.reset();
    }

    /// Build a `RegimeSummary` from collected regime transitions and per-trade
    /// regime labels. Returns `None` when the strategy never produced a regime
    /// (no `regime { … }` block).
    fn build_regime_summary(&self) -> Option<RegimeSummary> {
        if self.regime_changes.is_empty() {
            return None;
        }
        // Group trades by regime label and compute win-rate / avg-return / profit-factor.
        let mut buckets: HashMap<String, (usize, usize, f64, f64, f64)> = HashMap::new();
        // tuple: (trades, wins, sum_pnl_pct, sum_gain, sum_loss_abs)
        for t in &self.portfolio.trades {
            let key = match &t.regime_at_entry {
                Some(state) => state.label(),
                None        => continue,
            };
            let entry = buckets.entry(key).or_insert((0, 0, 0.0, 0.0, 0.0));
            entry.0 += 1;
            if t.pnl > 0.0 {
                entry.1 += 1;
                entry.3 += t.pnl;
            } else {
                entry.4 += -t.pnl;
            }
            entry.2 += t.pnl_pct;
        }
        let mut trade_breakdown: Vec<RegimeTradeStats> = buckets
            .into_iter()
            .map(|(label, (trades, wins, sum_pct, gain, loss))| {
                let win_rate_pct = if trades > 0 { 100.0 * wins as f64 / trades as f64 } else { 0.0 };
                let avg_return_pct = if trades > 0 { 100.0 * sum_pct / trades as f64 } else { 0.0 };
                let profit_factor = if loss > f64::EPSILON { gain / loss } else if gain > 0.0 { f64::INFINITY } else { 0.0 };
                RegimeTradeStats { label, trades, win_rate_pct, avg_return_pct, profit_factor }
            })
            .collect();
        trade_breakdown.sort_by(|a, b| b.trades.cmp(&a.trades));
        Some(RegimeSummary {
            changes: self.regime_changes.clone(),
            trade_breakdown,
        })
    }
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use alm_core::regime::{RegimeDimension, RegimeState};
    use alm_data::BarVecFeed;
    use alm_strategy::FixedFractional;

    /// A Strategy that fires Long → Exit on alternating bars and exposes a
    /// regime label that flips at bar 50. Used to exercise engine regime
    /// tracking and trade tagging without depending on script-level details.
    struct FakeRegimeStrategy {
        i:       usize,
        symbol:  String,
        regime:  RegimeState,
    }

    impl FakeRegimeStrategy {
        fn new(symbol: &str) -> Self {
            Self {
                i:      0,
                symbol: symbol.into(),
                regime: RegimeState::new(
                    RegimeDimension::new(20.0, "ranging"),
                    RegimeDimension::new(1.0,  "normal"),
                    RegimeDimension::new(1.0,  "normal"),
                ),
            }
        }
    }

    impl Strategy for FakeRegimeStrategy {
        fn on_bar(&mut self, bar: &Bar) -> Vec<Signal> {
            // Flip regime once at bar index 50.
            if self.i == 50 {
                self.regime = RegimeState::new(
                    RegimeDimension::new(35.0, "trending"),
                    RegimeDimension::new(1.4,  "high"),
                    RegimeDimension::new(1.0,  "normal"),
                );
            }
            self.i += 1;
            // Open at bar 20, close at bar 60 (in trending regime), then open
            // again at bar 70 and close at bar 80 (still in trending).
            match self.i {
                21 | 71 => vec![Signal::long(bar.timestamp, &self.symbol, 1.0)],
                61 | 81 => vec![Signal::exit(bar.timestamp, &self.symbol)],
                _       => vec![],
            }
        }
        fn name(&self) -> &str { "fake_regime" }
        fn reset(&mut self) {}
        fn current_regime(&self) -> Option<&RegimeState> { Some(&self.regime) }
    }

    fn synth_bars(n: usize, symbol: &str) -> Vec<Bar> {
        (0..n).map(|i| {
            let c = 100.0 + (i as f64) * 0.5;
            Bar::new(i as i64 * 60_000, symbol, c, c * 1.01, c * 0.99, c, 1000.0)
        }).collect()
    }

    #[test]
    fn engine_tracks_regime_changes_and_tags_trades() {
        let bars = synth_bars(100, "TEST");
        let strategy = FakeRegimeStrategy::new("TEST");
        let risk = FixedFractional::fractional(0.95, 1);
        let mut engine = Engine::sync(10_000.0, strategy, risk, 0.0, 0.0);
        let mut feed = BarVecFeed::new(bars, "TEST".into());
        let report = engine.run(&mut feed, 0.0);

        let summary = report.regime_summary.as_ref().expect("regime summary present");
        // Initial "ranging/normal/normal" + transition to "trending/high/normal" → 2 entries.
        assert_eq!(summary.changes.len(), 2);
        assert_eq!(summary.changes[0].1, "ranging/normal/normal");
        assert_eq!(summary.changes[1].1, "trending/high/normal");

        // Closed trades should carry the full regime snapshot at entry, not just a label.
        let trades = &engine.portfolio.trades;
        assert!(!trades.is_empty(), "expected at least one closed trade");
        for t in trades {
            assert!(t.regime_at_entry.is_some(), "trade missing regime snapshot");
        }
        // First trade opens at bar 21 (ranging). Verify all three dimensions
        // (status + raw value) are snapshotted, not just the joined label.
        let r0 = trades[0].regime_at_entry.as_ref().unwrap();
        assert_eq!(r0.trend.status,      "ranging");
        assert!((r0.trend.value      - 20.0).abs() < f64::EPSILON);
        assert_eq!(r0.volatility.status, "normal");
        assert!((r0.volatility.value -  1.0).abs() < f64::EPSILON);
        assert_eq!(r0.liquidity.status,  "normal");
        assert!((r0.liquidity.value  -  1.0).abs() < f64::EPSILON);

        // Second trade opens at bar 71 (after the regime flipped at bar 50).
        if trades.len() >= 2 {
            let r1 = trades[1].regime_at_entry.as_ref().unwrap();
            assert_eq!(r1.trend.status,      "trending");
            assert!((r1.trend.value      - 35.0).abs() < f64::EPSILON);
            assert_eq!(r1.volatility.status, "high");
            assert!((r1.volatility.value -  1.4).abs() < f64::EPSILON);
            assert_eq!(r1.liquidity.status,  "normal");
        }
    }

    #[test]
    fn engine_no_regime_summary_when_strategy_has_none() {
        struct PlainStrategy;
        impl Strategy for PlainStrategy {
            fn on_bar(&mut self, _bar: &Bar) -> Vec<Signal> { vec![] }
            fn name(&self) -> &str { "plain" }
            fn reset(&mut self) {}
        }
        let bars = synth_bars(30, "TEST");
        let risk = FixedFractional::fractional(0.95, 1);
        let mut engine = Engine::sync(10_000.0, PlainStrategy, risk, 0.0, 0.0);
        let mut feed = BarVecFeed::new(bars, "TEST".into());
        let report = engine.run(&mut feed, 0.0);
        assert!(report.regime_summary.is_none());
    }
}

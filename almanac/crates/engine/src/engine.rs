use std::collections::{HashMap, VecDeque};

use crate::broker::SimBroker;
use alm_core::{
    bar::Bar,
    bus::EventBus,
    event::{EquityEvent, Event, FillEvent, MarketEvent},
    exit::{ExitReason, IntraBarMode, PositionTracker},
    order::{Fill, OrderRequest, Side},
    portfolio::Portfolio,
    regime::{RegimeState, RegimeSummary, RegimeTradeStats},
    signal::{Direction, Signal},
    strategy::{RiskManager, Strategy},
};
use alm_data::BarFeed;
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
    /// How the bar's OHLC is read when checking signal-level exit levels.
    intra_bar_mode: IntraBarMode,
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
    /// Pyramiding: max accumulated entry legs per symbol (1 = single entry, no
    /// pyramiding — preserves legacy behavior). When >1, a same-direction signal
    /// while in-position adds a leg if price advanced past the last leg and total
    /// exposure stays under `max_position_pct`. TP/SL re-base to each new leg.
    max_units: usize,
    /// Pyramiding: hard cap on total open exposure as a fraction of equity
    /// (0 = no cap). Checked before each pyramiding add.
    max_position_pct: f64,
    /// Pyramiding mode. `true` (default) = MERGE: all legs collapse into one
    /// averaged position keyed by the base symbol, TP/SL re-based to the last leg.
    /// `false` = INDEPENDENT: each leg is a separate position keyed `SYM#n`, with
    /// its own entry / TP / SL / trade, exited individually.
    pyramid_merge: bool,
    /// Warm-up boundary (Unix ms). Bars with `timestamp < warmup_until` only advance
    /// indicators — no trading / equity. `None` = trade from the first bar.
    warmup_until: Option<i64>,
    /// Pending exit levels from the entry Signal — consumed at Fill time.
    /// Key = symbol; value = (target_price, stop_price, trailing_stop_pct, max_bars_held).
    /// (target_price, stop_price, trailing_stop_pct, max_bars_held, is_offset).
    /// When `is_offset`, target/stop are distances resolved to absolute levels at
    /// fill time using the fill price + position direction.
    pending_signal_levels: HashMap<String, (Option<f64>, Option<f64>, Option<f64>, Option<usize>, bool)>,
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

/// Strip the synthetic pyramiding leg suffix (`SYM#3` → `SYM`). Real exchange
/// symbols never contain `#` (e.g. `BTCUSDT`, `BTC-USDT`), so it is a safe
/// separator for independent-leg position keys.
pub(crate) fn base_symbol(key: &str) -> &str {
    match key.find('#') {
        Some(i) => &key[..i],
        None => key,
    }
}

/// Resolve signal-level TP/SL to absolute prices. When `is_offset`, target/stop are
/// magnitudes whose side depends on direction (long → TP above / SL below; short →
/// TP below / SL above). When not offset they are already absolute and returned as-is.
fn resolve_offset_levels(
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
            intra_bar_mode: IntraBarMode::default(),
            position_trackers: HashMap::new(),
            next_bar: false,
            pending_signals: Vec::new(),
            single_entry: false,
            max_units: 1,
            max_position_pct: 0.0,
            pyramid_merge: true,
            warmup_until: None,
            pending_signal_levels: HashMap::new(),
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

    /// Enable pyramiding: accumulate up to `max_units` same-direction legs into one
    /// averaged position, adding only when price advances past the last leg and total
    /// exposure stays under `max_position_pct` (fraction of equity; 0 = no cap).
    /// `max_units = 1` disables pyramiding (default). TP/SL re-base to each new leg.
    pub fn with_pyramiding(mut self, max_units: usize, max_position_pct: f64) -> Self {
        self.max_units = max_units.max(1);
        self.max_position_pct = max_position_pct.max(0.0);
        self
    }

    /// Warm indicators on bars before `until` (Unix ms) without trading — the report
    /// and trades begin at `until` with indicators already converged. No-op if 0/None.
    pub fn with_warmup_until(mut self, until: i64) -> Self {
        self.warmup_until = if until > 0 { Some(until) } else { None };
        self
    }

    /// Switch pyramiding to INDEPENDENT-leg mode: each added leg becomes its own
    /// position (keyed `SYM#n`) with its own entry / TP / SL / trade, exited
    /// individually — instead of the default MERGE mode (one averaged position,
    /// TP/SL re-based to the last leg). Only meaningful when `max_units > 1`.
    pub fn with_independent_legs(mut self) -> Self {
        self.pyramid_merge = false;
        self
    }

    /// Set how the bar's OHLC is read when checking signal-level exit levels.
    pub fn with_intra_bar_mode(mut self, mode: IntraBarMode) -> Self {
        self.intra_bar_mode = mode;
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
                let base = signal.symbol.as_str();
                let legs: Vec<(String, f64, bool)> = self
                    .portfolio
                    .positions
                    .iter()
                    .filter(|(k, p)| base_symbol(k) == base && p.qty.abs() > f64::EPSILON)
                    .map(|(k, p)| (k.clone(), p.qty.abs(), p.is_long()))
                    .collect();
                for (key, qty, is_long) in legs {
                    let side = if is_long { Side::Sell } else { Side::Buy };
                    let fill =
                        self.broker.force_close(&key, qty, side, bar.timestamp, bar.open);
                    self.portfolio.apply_fill(&fill);
                    let regime_state = self.regime_at_entry.remove(&key);
                    if let Some(tr) = self.position_trackers.remove(&key) {
                        if let Some(trade) = self.portfolio.trades.last_mut() {
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
                        let (tp_abs, sl_abs) = resolve_offset_levels(fill_price, signal.target_price, signal.stop_price, signal.is_offset, true);
                        let was_new = !self.position_trackers.contains_key(&signal.symbol);
                        self.position_trackers.entry(signal.symbol.clone()).or_insert_with(|| {
                            PositionTracker::with_levels(fill_price, sl_abs, tp_abs, signal.trailing_stop_pct, signal.max_bars_held, true)
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
                        let (tp_abs, sl_abs) = resolve_offset_levels(fill_price, signal.target_price, signal.stop_price, signal.is_offset, false);
                        let was_new = !self.position_trackers.contains_key(&signal.symbol);
                        self.position_trackers.entry(signal.symbol.clone()).or_insert_with(|| {
                            PositionTracker::with_levels(fill_price, sl_abs, tp_abs, signal.trailing_stop_pct, signal.max_bars_held, false)
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
    /// Thin router; each arm delegates to a dedicated handler.
    fn dispatch(&mut self, event: Event) {
        match event {
            Event::Market(ref market) => self.on_market(&market.bar),
            Event::Signal(ref sig_event) => self.on_signal(&sig_event.signal),
            Event::Order(ref order_event) => self.broker.submit(order_event.order.clone()),
            Event::Fill(ref fill_event) => self.on_fill(&fill_event.fill),
            Event::Equity(_) => {
                // No-op in backtest: the equity curve is built directly by
                // `portfolio.record_equity()`. The event is emitted as an
                // extension hook for a live bus whose subscribers (dashboards,
                // realtime feeds) tap equity snapshots — SyncBus has none.
            }
        }
    }

    /// Handle a market bar: process pending fills, run exit checks, snapshot
    /// equity, then ask the strategy for new signals.
    fn on_market(&mut self, bar: &Bar) {
        trace!(
            symbol = %bar.symbol, ts = bar.timestamp,
            open = bar.open, high = bar.high, low = bar.low, close = bar.close, vol = bar.volume,
            "bar"
        );

        self.last_price = bar.close;

        // Notify risk manager with current bar (e.g. for ATR-based sizing).
        self.risk.on_bar(bar);

        // Warm-up phase: bars before `warmup_until` only advance the strategy's
        // indicators (so they're converged by the trading start) — no fills, no
        // exits, no equity points, no trades. This lets the report begin clean at
        // `from` with indicators already warm instead of cold.
        if let Some(until) = self.warmup_until {
            if bar.timestamp < until {
                let _ = self.strategy.on_bar(bar); // advance indicator state only
                return;
            }
        }

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

        // Run BEFORE recording equity so that exit-fired close fills are
        // reflected in this bar's equity point (otherwise the curve lags
        // the exit by one bar).
        self.run_exit_checks(bar);

        // Snapshot equity at bar close — AFTER exit rules so that any
        // SL/TP/trailing fills applied this bar are reflected. Include any
        // independent pyramiding legs (`SYM#n`) so open legs are valued too.
        let prices = self.leg_price_map(&bar.symbol, self.last_price);
        self.portfolio.record_equity(bar.timestamp, &prices);
        let eq = self.portfolio.equity(&prices);
        self.bus.send(Event::Equity(EquityEvent {
            timestamp: bar.timestamp,
            equity: eq,
            cash: self.portfolio.cash,
        }));

        // ── Portfolio snapshot → strategy (opt-in) ───────────────────
        if self.strategy.uses_portfolio_snapshot() {
            let prices = self.leg_price_map(&bar.symbol, self.last_price);
            let snapshot = self.portfolio.snapshot(&prices);
            self.strategy.set_portfolio_snapshot(&snapshot);
        }

        self.emit_strategy_signals(bar);
    }

    /// Tick each open position's tracker against this bar; force-close any that
    /// hit a signal-level exit (SL/TP/trailing/max-bars). Always called even
    /// with no exit levels set — it tracks bars_held, MAE, and MFE.
    fn run_exit_checks(&mut self, bar: &Bar) {
        let to_close: Vec<(String, f64, ExitReason)> = self
            .position_trackers
            .iter_mut()
            .filter_map(|(sym, tracker)| {
                tracker
                    .update_and_check(bar.open, bar.high, bar.low, bar.close, self.intra_bar_mode)
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

    /// Run the strategy over this bar (and the sliding window, if used) and
    /// emit the resulting signals onto the bus.
    fn emit_strategy_signals(&mut self, bar: &Bar) {
        // ── Indicator-based signals (bar-by-bar) ──────────────────────
        // `Instant::now()` panics on wasm32-unknown-unknown — skip eval timing there.
        #[cfg(not(target_arch = "wasm32"))]
        let eval_start = std::time::Instant::now();
        let signals = self.strategy.on_bar(bar);
        #[cfg(not(target_arch = "wasm32"))]
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
    }

    /// Turn a strategy signal into an order (immediate-execution mode). In
    /// next-bar mode signals are buffered for execution at the next bar's open.
    fn on_signal(&mut self, signal: &Signal) {
        if self.next_bar {
            self.pending_signals.push(signal.clone());
            return;
        }
        match signal.direction {
            Direction::Exit => self.close_position_market(signal),
            Direction::Long => self.open_position(signal, Side::Buy),
            Direction::Short => self.open_position(signal, Side::Sell),
        }
    }

    /// Emit market orders that close EVERY open leg for the signal's symbol —
    /// the bare `base` position plus any independent legs (`base#n`).
    fn close_position_market(&mut self, signal: &Signal) {
        let base = signal.symbol.as_str();
        let legs: Vec<(String, f64, bool)> = self
            .portfolio
            .positions
            .iter()
            .filter(|(k, p)| base_symbol(k) == base && p.qty.abs() > f64::EPSILON)
            .map(|(k, p)| (k.clone(), p.qty.abs(), p.is_long()))
            .collect();
        for (key, qty, is_long) in legs {
            // Long → sell to close; Short → buy to cover
            let side = if is_long { Side::Sell } else { Side::Buy };
            let order = OrderRequest::market(signal.timestamp, &key, side, qty);
            metrics::counter!("alm_engine_orders_total",
                "strategy" => self.metrics_strategy.clone(),
                "side"     => "sell",
            ).increment(1);
            self.bus
                .send(Event::Order(alm_core::event::OrderEvent { order }));
            debug!(symbol = %key, qty, ?side, strength = signal.strength, "close signal → order");
        }
    }

    /// All currently-open position keys for `base` (including independent legs
    /// `base#n`) in the given direction → `(key, entry_price, qty)`.
    fn same_dir_legs(&self, base: &str, side: Side) -> Vec<(String, f64, f64)> {
        self.portfolio
            .positions
            .iter()
            .filter(|(k, p)| {
                base_symbol(k) == base
                    && p.qty.abs() > f64::EPSILON
                    && match side { Side::Buy => p.is_long(), Side::Sell => p.is_short() }
            })
            .map(|(k, p)| (k.clone(), p.avg_price, p.qty))
            .collect()
    }

    /// Mint the next free independent-leg key for `base` (`base#2`, `base#3`, …).
    /// Leg 1 uses the bare `base` key; adds get a suffix.
    fn mint_leg_key(&self, base: &str) -> String {
        let mut i = 2;
        loop {
            let key = format!("{base}#{i}");
            if !self.portfolio.positions.contains_key(&key) { return key; }
            i += 1;
        }
    }

    /// Price map covering `base` and all its open independent legs (`base#n`),
    /// every entry set to `price` — used for equity/exposure of a pyramided symbol.
    fn leg_price_map(&self, base: &str, price: f64) -> HashMap<String, f64> {
        let mut m = HashMap::new();
        m.insert(base.to_string(), price);
        for k in self.portfolio.positions.keys() {
            if base_symbol(k) == base { m.insert(k.clone(), price); }
        }
        m
    }

    /// Open a position in `side` from a Long/Short signal, honoring single-entry
    /// and risk validation, and stashing any signal-level exit levels for the fill.
    fn open_position(&mut self, signal: &Signal, side: Side) {
        let base = signal.symbol.as_str();
        let legs = self.same_dir_legs(base, side);
        let in_pos_same = !legs.is_empty();
        let mut is_pyramid_add = false;
        // Default leg key = base symbol (first leg, or MERGE-mode adds).
        let mut leg_key = signal.symbol.clone();
        if in_pos_same {
            if self.max_units > 1 {
                // Pyramiding: add a leg only if under the leg cap, price advanced
                // past the most recent leg, and total exposure stays under the cap.
                let count = if self.pyramid_merge {
                    self.position_trackers.get(base).map_or(1, |t| t.legs)
                } else {
                    legs.len()
                };
                if count >= self.max_units { return; }
                // Most-recent leg's entry: MERGE keeps it on the tracker; INDEPENDENT
                // derives it from the extreme leg (adds only ever happen on advance).
                let last_px = if self.pyramid_merge {
                    self.position_trackers.get(base).map_or(self.last_price, |t| t.last_entry_price)
                } else {
                    match side {
                        Side::Buy  => legs.iter().map(|l| l.1).fold(f64::MIN, f64::max),
                        Side::Sell => legs.iter().map(|l| l.1).fold(f64::MAX, f64::min),
                    }
                };
                let advanced = match side {
                    Side::Buy  => self.last_price > last_px,
                    Side::Sell => self.last_price < last_px,
                };
                if !advanced { return; }
                if self.max_position_pct > 0.0 {
                    let prices = self.leg_price_map(base, self.last_price);
                    let eq = self.portfolio.equity(&prices);
                    let pos_val: f64 = legs.iter().map(|l| l.2.abs() * self.last_price).sum();
                    if eq > 0.0 && pos_val >= self.max_position_pct * eq { return; }
                }
                is_pyramid_add = true;
                if !self.pyramid_merge {
                    leg_key = self.mint_leg_key(base);
                }
            } else if self.single_entry {
                return; // legacy single-entry guard (no pyramiding)
            }
        }
        // A pyramid add is already authorized by the guards above; bypass the
        // RiskManager's same-direction veto (which exists to prevent *unintended*
        // pyramiding). Sizing (`risk.size`) still applies per leg.
        if is_pyramid_add || self.risk.validate(signal, &self.portfolio) {
            let qty = self.risk.size(signal, &self.portfolio, self.last_price);
            if qty > f64::EPSILON {
                // Store signal-level exit levels for PositionTracker creation at fill
                // — keyed by the LEG key so independent legs get their own TP/SL.
                if signal.target_price.is_some() || signal.stop_price.is_some()
                    || signal.trailing_stop_pct.is_some() || signal.max_bars_held.is_some() {
                    self.pending_signal_levels.insert(
                        leg_key.clone(),
                        (signal.target_price, signal.stop_price, signal.trailing_stop_pct, signal.max_bars_held, signal.is_offset),
                    );
                }
                let order = OrderRequest::market(signal.timestamp, &leg_key, side, qty);
                let side_label = match side {
                    Side::Buy => "buy",
                    Side::Sell => "sell",
                };
                metrics::counter!("alm_engine_orders_total",
                    "strategy" => self.metrics_strategy.clone(),
                    "side"     => side_label,
                ).increment(1);
                self.bus
                    .send(Event::Order(alm_core::event::OrderEvent { order }));
                debug!(symbol = %signal.symbol, qty, strength = signal.strength, ?side, "entry signal → order");
            }
        }
    }

    /// Apply a fill to the portfolio and maintain the per-position exit tracker.
    fn on_fill(&mut self, fill: &Fill) {
        metrics::counter!("alm_engine_fills_total",
            "strategy" => self.metrics_strategy.clone()
        ).increment(1);
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
            let (sig_tp, sig_sl, sig_trail, sig_max_bars, sig_offset) =
                sig_levels.unwrap_or((None, None, None, None, false));
            let is_long = self.portfolio.positions.get(&fill.symbol)
                .map_or(true, |p| p.qty > 0.0);
            // Offset TP/SL are distances from the fill — resolve to absolute here so the
            // tracker (and the chart box, which resolves the same way) agree on levels.
            let (sig_tp, sig_sl) = resolve_offset_levels(fill.price, sig_tp, sig_sl, sig_offset, is_long);
            let was_new = !self.position_trackers.contains_key(&fill.symbol);
            if was_new {
                self.position_trackers.insert(
                    fill.symbol.clone(),
                    PositionTracker::with_levels(fill.price, sig_sl, sig_tp, sig_trail, sig_max_bars, is_long),
                );
                // Record the regime active when this position was opened, to tag the Trade on close.
                if let Some(state) = &self.last_regime {
                    self.regime_at_entry.insert(fill.symbol.clone(), state.clone());
                }
            } else if self.max_units > 1 {
                // Pyramiding add — re-base the tracker's exit levels to the new leg.
                if let Some(tr) = self.position_trackers.get_mut(&fill.symbol) {
                    tr.add_leg(fill.price, sig_sl, sig_tp, sig_trail, sig_max_bars);
                }
            }
            // else: legacy re-entry (max_units<=1) — keep the existing tracker.
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

    struct AlwaysLong { symbol: String }
    impl Strategy for AlwaysLong {
        fn on_bar(&mut self, bar: &Bar) -> Vec<Signal> {
            vec![Signal::long(bar.timestamp, &self.symbol, 1.0)]
        }
        fn name(&self) -> &str { "always_long" }
        fn reset(&mut self) {}
    }

    fn final_position_qty(max_units: usize, single_entry: bool) -> f64 {
        let bars = synth_bars(12, "TEST"); // strictly rising → advance guard always passes
        let risk = FixedFractional::fractional(0.10, 5);
        let mut engine = Engine::sync(10_000.0, AlwaysLong { symbol: "TEST".into() }, risk, 0.0, 0.0);
        if single_entry { engine = engine.with_single_entry(); }
        if max_units > 1 { engine = engine.with_pyramiding(max_units, 0.0); }
        let mut feed = BarVecFeed::new(bars, "TEST".into());
        engine.run(&mut feed, 0.0);
        // Closing trade carries the full accumulated qty (force-closed at end of data).
        engine.portfolio.trades.last().map(|t| t.qty).unwrap_or(0.0)
    }

    /// Pyramiding accumulates more total qty (multiple legs) than a single entry on
    /// the same rising series + continuous long signal.
    #[test]
    fn pyramiding_accumulates_more_than_single_entry() {
        let single = final_position_qty(1, true);   // single-entry: 1 leg
        let pyramid = final_position_qty(3, false);  // up to 3 legs
        assert!(single > 0.0, "single-entry should open a position");
        assert!(pyramid > single * 1.5,
            "pyramiding(3) qty {pyramid} should exceed single-entry qty {single} (multiple legs)");
    }

    /// Price-advance guard: on a FLAT series the continuous long signal must NOT
    /// pyramid (each add requires price > last leg). Only the first leg opens.
    #[test]
    fn pyramiding_does_not_add_on_flat_price() {
        let bars: Vec<Bar> = (0..12)
            .map(|i| Bar::new(i as i64 * 60_000, "TEST", 100.0, 100.5, 99.5, 100.0, 1000.0))
            .collect();
        let risk = FixedFractional::fractional(0.10, 5);
        let mut engine = Engine::sync(10_000.0, AlwaysLong { symbol: "TEST".into() }, risk, 0.0, 0.0)
            .with_pyramiding(3, 0.0);
        let mut feed = BarVecFeed::new(bars, "TEST".into());
        engine.run(&mut feed, 0.0);
        // Single leg only: 10% of 10k / 100 ≈ 10 units.
        let qty = engine.portfolio.trades.last().map(|t| t.qty).unwrap_or(0.0);
        assert!((qty - 10.0).abs() < 1.0, "flat price must not pyramid, qty={qty}");
    }

    /// Leg cap: a higher `max_units` accumulates more total qty on rising bars.
    #[test]
    fn pyramiding_leg_cap_bounds_accumulation() {
        let q2 = final_position_qty(2, false);
        let q5 = final_position_qty(5, false);
        assert!(q5 > q2, "max_units=5 should accumulate more than max_units=2: q5={q5} q2={q2}");
    }

    /// Short pyramiding mirrors long: adds legs only when price FALLS past the last leg.
    #[test]
    fn pyramiding_short_on_falling_price() {
        struct AlwaysShort { symbol: String }
        impl Strategy for AlwaysShort {
            fn on_bar(&mut self, bar: &Bar) -> Vec<Signal> {
                vec![Signal::short(bar.timestamp, &self.symbol, 1.0)]
            }
            fn name(&self) -> &str { "always_short" }
            fn reset(&mut self) {}
        }
        let falling: Vec<Bar> = (0..12)
            .map(|i| { let c = 100.0 - i as f64 * 0.5; Bar::new(i as i64 * 60_000, "TEST", c, c * 1.01, c * 0.99, c, 1000.0) })
            .collect();
        let run = |max_units: usize, single: bool| -> f64 {
            let risk = FixedFractional::fractional(0.10, 5);
            let mut e = Engine::sync(10_000.0, AlwaysShort { symbol: "TEST".into() }, risk, 0.0, 0.0);
            if single { e = e.with_single_entry(); }
            if max_units > 1 { e = e.with_pyramiding(max_units, 0.0); }
            let mut f = BarVecFeed::new(falling.clone(), "TEST".into());
            e.run(&mut f, 0.0);
            e.portfolio.trades.last().map(|t| t.qty).unwrap_or(0.0)
        };
        let single = run(1, true);
        let pyramid = run(3, false);
        assert!(single > 0.0, "single short should open a position");
        assert!(pyramid > single * 1.5,
            "short pyramiding on falling price should accumulate: pyramid={pyramid} single={single}");
    }

    /// Run continuous-long pyramiding on a rising series and return each closing
    /// trade as `(entry_price, qty)`. `merge=false` → independent-leg mode.
    fn run_pyramid_trades(max_units: usize, merge: bool) -> Vec<(f64, f64)> {
        let bars = synth_bars(12, "TEST"); // strictly rising → advance guard always passes
        let risk = FixedFractional::fractional(0.10, 5);
        let mut engine = Engine::sync(10_000.0, AlwaysLong { symbol: "TEST".into() }, risk, 0.0, 0.0)
            .with_pyramiding(max_units, 0.0);
        if !merge { engine = engine.with_independent_legs(); }
        let mut feed = BarVecFeed::new(bars, "TEST".into());
        engine.run(&mut feed, 0.0);
        engine.portfolio.trades.iter().map(|t| (t.entry_price, t.qty)).collect()
    }

    /// MERGE collapses legs into one averaged position (one closing trade); INDEPENDENT
    /// keeps each leg as its own position → its own trade, with distinct entry prices.
    #[test]
    fn pyramiding_independent_creates_separate_legs() {
        let merge = run_pyramid_trades(3, true);
        let indep = run_pyramid_trades(3, false);
        assert_eq!(merge.len(), 1, "merge mode → single averaged trade, got {}", merge.len());
        assert!(indep.len() >= 2, "independent mode → multiple leg trades, got {}", indep.len());
        // Legs entered at distinct (rising) prices — proves separate entries, not averaging.
        let entries: std::collections::BTreeSet<u64> =
            indep.iter().map(|t| (t.0 * 1000.0).round() as u64).collect();
        assert!(entries.len() >= 2, "independent legs should have distinct entry prices: {indep:?}");
        // The single merged trade's entry is the AVERAGE of the legs (not any one leg).
        assert!(merge[0].1 > indep[0].1, "merged trade qty should exceed a single independent leg");
    }

    /// Independent legs each carry their OWN stop → an early leg can exit on its stop
    /// while a later leg stays open (impossible under merge/averaging).
    #[test]
    fn pyramiding_independent_legs_exit_individually() {
        // Rise then fall: legs open on the way up, the lowest-stop leg trips on the way down.
        struct LongWithStop { symbol: String }
        impl Strategy for LongWithStop {
            fn on_bar(&mut self, bar: &Bar) -> Vec<Signal> {
                // Stop 2% below the current close → each leg gets its own stop level.
                let mut s = Signal::long(bar.timestamp, &self.symbol, 1.0);
                s.stop_price = Some(bar.close * 0.98);
                vec![s]
            }
            fn name(&self) -> &str { "long_with_stop" }
            fn reset(&mut self) {}
        }
        let mut bars: Vec<Bar> = (0..6)
            .map(|i| { let c = 100.0 + i as f64; Bar::new(i as i64 * 60_000, "TEST", c, c * 1.005, c * 0.995, c, 1000.0) })
            .collect();
        // Hold one bar so the highest leg fills cleanly (not on the drop bar).
        bars.push(Bar::new(6 * 60_000, "TEST", 105.0, 105.0, 105.0, 105.0, 1000.0));
        // Sharp drop → trips the higher-entry legs' stops, leaving the lowest leg open.
        bars.push(Bar::new(7 * 60_000, "TEST", 105.0, 105.0, 100.0, 100.5, 1000.0));
        // Settle bar so the close fill is recorded on a non-terminal bar.
        bars.push(Bar::new(8 * 60_000, "TEST", 100.5, 100.5, 100.5, 100.5, 1000.0));
        let risk = FixedFractional::fractional(0.10, 5);
        let mut engine = Engine::sync(10_000.0, LongWithStop { symbol: "TEST".into() }, risk, 0.0, 0.0)
            .with_pyramiding(4, 0.0)
            .with_independent_legs()
            .with_intra_bar_mode(alm_core::exit::IntraBarMode::Pessimistic);
        let mut feed = BarVecFeed::new(bars, "TEST".into());
        engine.run(&mut feed, 0.0);
        // At least one leg must have exited via its STOP (not just end-of-data),
        // proving per-leg stops fire independently.
        let stop_exits = engine.portfolio.trades.iter()
            .filter(|t| t.exit_reason == ExitReason::StopLoss).count();
        assert!(stop_exits >= 1, "expected ≥1 independent leg to exit on its own stop, trades={:?}",
            engine.portfolio.trades.iter().map(|t| (t.entry_price, t.exit_price, format!("{:?}", t.exit_reason))).collect::<Vec<_>>());
    }

    /// Offset TP/SL resolve to the correct side per direction (long: TP up / SL down;
    /// short: TP down / SL up). The chart box (alm-wasm SignalOut) resolves identically.
    #[test]
    fn offset_levels_resolve_by_direction() {
        // long
        assert_eq!(resolve_offset_levels(100.0, Some(5.0), Some(3.0), true, true), (Some(105.0), Some(97.0)));
        // short
        assert_eq!(resolve_offset_levels(100.0, Some(5.0), Some(3.0), true, false), (Some(95.0), Some(103.0)));
        // not offset → passthrough (already absolute)
        assert_eq!(resolve_offset_levels(100.0, Some(110.0), Some(90.0), false, true), (Some(110.0), Some(90.0)));
        // magnitude taken as abs (a stray negative offset still lands on the right side)
        assert_eq!(resolve_offset_levels(100.0, None, Some(-3.0), true, true), (None, Some(97.0)));
    }

    /// Bars before `warmup_until` warm indicators but must not trade or record equity.
    #[test]
    fn warmup_suppresses_trading_before_until() {
        let bars = synth_bars(12, "TEST"); // rising; ts = i*60_000
        let until = bars[5].timestamp;     // trade only from bar 5 onward
        let risk = FixedFractional::fractional(0.95, 1);
        let mut engine = Engine::sync(10_000.0, AlwaysLong { symbol: "TEST".into() }, risk, 0.0, 0.0)
            .with_warmup_until(until);
        let mut feed = BarVecFeed::new(bars, "TEST".into());
        engine.run(&mut feed, 0.0);
        // No equity point recorded during warm-up.
        assert!(engine.portfolio.equity_curve.iter().all(|p| p.timestamp >= until),
            "equity recorded during warm-up");
        // No trade entered during warm-up.
        assert!(engine.portfolio.trades.iter().all(|t| t.entry_timestamp >= until),
            "trade entered during warm-up");
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
        // Initial "ranging/normal" + transition to "trending/high" → 2 entries.
        assert_eq!(summary.changes.len(), 2);
        assert_eq!(summary.changes[0].1, "ranging/normal");
        assert_eq!(summary.changes[1].1, "trending/high");

        // Closed trades should carry the full regime snapshot at entry, not just a label.
        let trades = &engine.portfolio.trades;
        assert!(!trades.is_empty(), "expected at least one closed trade");
        for t in trades {
            assert!(t.regime_at_entry.is_some(), "trade missing regime snapshot");
        }
        // First trade opens at bar 21 (ranging). Verify both dimensions
        // (status + raw value) are snapshotted, not just the joined label.
        let r0 = trades[0].regime_at_entry.as_ref().unwrap();
        assert_eq!(r0.trend.status,      "ranging");
        assert!((r0.trend.value      - 20.0).abs() < f64::EPSILON);
        assert_eq!(r0.volatility.status, "normal");
        assert!((r0.volatility.value -  1.0).abs() < f64::EPSILON);

        // Second trade opens at bar 71 (after the regime flipped at bar 50).
        if trades.len() >= 2 {
            let r1 = trades[1].regime_at_entry.as_ref().unwrap();
            assert_eq!(r1.trend.status,      "trending");
            assert!((r1.trend.value      - 35.0).abs() < f64::EPSILON);
            assert_eq!(r1.volatility.status, "high");
            assert!((r1.volatility.value -  1.4).abs() < f64::EPSILON);
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
